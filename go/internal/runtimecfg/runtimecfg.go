// Package runtimecfg manages post-startup configuration: RPC provider URLs,
// admin signer sets, and credentials. All other config is fixed in internal/config.
//
//	ApplyEndpoints ─┐
//	ApplyAdmins    ─┼─► Store ──► Resolve() ──► oracle.Config
//	ApplySecret    ─┘
//
// On-chain payloads are ABI-encoded structs from TeeOracleInstructionsSender.
// The contract only validates structure and uniqueness; all tag content, meanings,
// and threshold limits are enforced here.
//
// Group and role tags are on-chain bytes32; unknown or missing required tags are rejected.
// Schema mismatches halt processing rather than applying unexpected subsets.
//
// Chain RPC URLs have no allowlist, so anyone who can publish SET_ENDPOINTS controls them.
// Only HTTPS is accepted, and credentials can't alter URL structure.
// Reserved sources are restricted: hex_bonds is fixed, hex_cash must be PRIVATE and match a preprovisioned secret.
//
// All runtimecfg state is in-memory only; a restart reverts to not_configured.
// SET_ADMINS must always be republished after restart before other settings can be applied.
package runtimecfg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"fce-usdx-oracle/internal/backing"
	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/oracle"
	"fce-usdx-oracle/internal/provision"
	"fce-usdx-oracle/internal/supply"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// secretPlaceholder may appear in the path or a query value, never in the
// scheme or host.
const secretPlaceholder = "{secret}"

// maxPayloadBytes bounds decoding work. The contract no longer caps payload
// size — MAX_ENDPOINTS_BYTES is gone and transaction size is the only on-chain
// limit — so this is the enclave's own bound, not a mirror of one.
const maxPayloadBytes = 256 << 10

// urlUnsafe lists characters a credential may not contain, because each can
// restructure the URL it lands in. One copy, used at every checkpoint.
const urlUnsafe = " \t\r\n/?#&"

var errBadPayload = errors.New("runtimecfg: payload is not a known configuration")

// Endpoints is a decoded SET_ENDPOINTS payload, keyed by tag.
type Endpoints struct {
	// Contract-assigned, one increment per publication of THIS feed across the
	// whole fleet. New: the old JSON payload carried no version at all.
	Version uint64

	// The feed this configuration serves. One sender publishes for every feed of
	// its extension, so a payload is only meaningful alongside its feed id.
	FeedID [21]byte

	Groups map[string]Group
}

// Group is one tagged endpoint group.
type Group struct {
	// How many endpoints in the group must agree.
	Threshold int

	Endpoints []Endpoint
}

// Endpoint is one upstream, either published in the clear or committed to.
type Endpoint struct {
	Kind uint8

	// PUBLIC only. May embed secretPlaceholder, filled from SecretRef.
	URL string

	// PRIVATE only: commitment to a URL provisioned off-chain. See
	// CommitPrivateURL.
	URLHash common.Hash

	// Names a credential or a provisioned URL entry. A name, never a value.
	SecretRef string
}

// Node is the subset of tee-node this package needs.
type Node interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
	TeeID(ctx context.Context) (common.Address, error)
}

// Store receives provisioning messages and resolves them into an oracle.Config.
type Store struct {
	// applyMu covers a whole Apply* call including the verifier's version map; mu
	// covers only the data fields, and is never held across a tee-node round trip,
	// or a hung /decrypt would freeze /state. Order is applyMu then mu.
	applyMu sync.Mutex

	mu        sync.RWMutex
	endpoints *Endpoints

	// keccak256 of the raw payload AS RECEIVED. It must equal the hash the
	// sender recorded, which is keccak256 of the message it dispatched — so it
	// cannot be recomputed from the decoded struct. These payloads are dynamic;
	// re-encoding is not guaranteed to reproduce the input.
	endpointsHash common.Hash

	// Strictly increasing, on the same terms as adminsVersion below. The
	// endpoints payload never carried a version before, so this bar is new: a
	// captured SET_ENDPOINTS must not reinstate a superseded provider set.
	endpointsVersion uint64

	// keccak256 of the raw SET_ADMINS payload as received, on the same terms:
	// the feed store compares it against expectedAdminsHash.
	adminsHash common.Hash

	// adminsSet records that admin sets were installed, so Readiness can
	// distinguish "no admins yet" from "admins installed" without reaching into
	// the verifier, which is guarded by applyMu rather than mu.
	adminsSet bool

	// Highest SET_ADMINS version applied. Not leaning on the registry's own replay
	// protection: the admin sets are the root of trust for every credential that
	// follows, so a captured SET_ADMINS must not reinstate a rotated-out signer.
	adminsVersion uint64

	// Raw plaintext, held only in memory. Bytes rather than strings because a
	// PRIVATE endpoint's URL is provisioned as an ABI-encoded (salt, url) blob,
	// not as a credential — see verifySecret.
	secrets map[string][]byte

	verifier *provision.Verifier
	node     Node
}

func New(verifier *provision.Verifier, node Node) *Store {
	return &Store{secrets: map[string][]byte{}, verifier: verifier, node: node}
}

// ApplyEndpoints installs a provider configuration from an on-chain
// instruction. Authorisation is the onlyGovernance check in the instructions
// sender; what is checked here is content.
func (s *Store) ApplyEndpoints(payload []byte) error {
	// Held even though this path does not touch the verifier: the rule is one
	// provisioning operation at a time.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return errors.New("runtimecfg: endpoint payload is empty or too large")
	}

	wire, err := decodeEndpoints(payload)
	if err != nil {
		return err
	}

	e, err := convertEndpoints(wire)
	if err != nil {
		return err
	}
	if err := validate(e); err != nil {
		return err
	}

	// Strictly increasing, checked before anything is installed so a replay
	// leaves the live configuration untouched.
	s.mu.RLock()
	lastVersion := s.endpointsVersion
	s.mu.RUnlock()
	if e.Version <= lastVersion {
		return fmt.Errorf(
			"runtimecfg: endpoints version %d is not newer than %d", e.Version, lastVersion)
	}

	// Any credential already provisioned must fit the configuration that is
	// arriving, so the mismatch fails THIS step rather than the next
	// observation. The other direction is covered in ApplySecret.
	s.mu.RLock()
	for ref, value := range s.secrets {
		if err := verifySecret(&e, ref, value); err != nil {
			s.mu.RUnlock()
			return err
		}
	}
	s.mu.RUnlock()

	s.mu.Lock()
	s.endpoints = &e
	s.endpointsHash = crypto.Keccak256Hash(payload)
	s.endpointsVersion = e.Version
	s.mu.Unlock()
	return nil
}

// ApplyAdmins installs the admin signer sets from an on-chain instruction.
// Authorisation is the onlyGovernance check in the sender; what is checked here
// is content. Separate from SET_ENDPOINTS so either can rotate alone.
// Rationale §9.
func (s *Store) ApplyAdmins(payload []byte) error {
	// Covers the verifier for the whole call, like ApplySecret: SetAdmins writes
	// state that a concurrent Verify would read.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return errors.New("runtimecfg: admin payload is empty or too large")
	}

	wire, err := decodeAdmins(payload)
	if err != nil {
		return err
	}

	version, sets, err := convertAdmins(wire)
	if err != nil {
		return err
	}

	// Strictly increasing: a captured envelope must not reinstate a superseded
	// set. Checked before SetAdmins so a replay leaves the live sets untouched.
	if version <= s.adminsVersion {
		return fmt.Errorf(
			"runtimecfg: admin version %d is not newer than %d", version, s.adminsVersion)
	}

	if err := s.verifier.SetAdmins(sets); err != nil {
		return err
	}

	s.mu.Lock()
	s.adminsHash = crypto.Keccak256Hash(payload)
	s.adminsVersion = version
	s.adminsSet = true
	s.mu.Unlock()
	return nil
}

// checkFeedID refuses configuration published for a different feed of the same
// extension. This machine serves exactly one, and the sender keeps commitments
// per feed and machine — so a sibling feed's payload would install cleanly, and
// then every feed update would carry a hash the store compares against THIS
// feed's commitment and rejects. Refusing here turns a silent, permanent
// StaleEndpoints into a rejected instruction that names the reason.
func checkFeedID(feedID [21]byte) error {
	if feedID != config.FeedID {
		return fmt.Errorf(
			"runtimecfg: payload names feed %#x, this machine serves %#x",
			feedID, config.FeedID)
	}
	return nil
}

// convertEndpoints turns the wire form into the tag-keyed form, rejecting
// anything the contract's shape rules let through: a tag that is not printable
// ASCII, a duplicate after tag decoding, or a threshold too large to narrow.
func convertEndpoints(w wireEndpoints) (Endpoints, error) {
	e := Endpoints{
		Version: w.Version,
		FeedID:  w.FeedId,
		Groups:  make(map[string]Group, len(w.Groups)),
	}

	for _, g := range w.Groups {
		tag, ok := tagString(g.Group)
		if !ok {
			// Not echoed: an unprintable tag is attacker-chosen bytes.
			return Endpoints{}, errors.New("runtimecfg: endpoint group tag is not a printable name")
		}
		if _, dup := e.Groups[tag]; dup {
			return Endpoints{}, fmt.Errorf("runtimecfg: duplicate endpoint group %q", tag)
		}

		threshold, ok := narrow(g.Threshold)
		if !ok {
			return Endpoints{}, fmt.Errorf("runtimecfg: group %q threshold is out of range", tag)
		}

		endpoints := make([]Endpoint, 0, len(g.Endpoints))
		for _, ep := range g.Endpoints {
			endpoints = append(endpoints, Endpoint{
				Kind:      ep.Kind,
				URL:       ep.Url,
				URLHash:   common.Hash(ep.UrlHash),
				SecretRef: strings.TrimSpace(ep.SecretRef),
			})
		}

		e.Groups[tag] = Group{Threshold: threshold, Endpoints: endpoints}
	}
	return e, nil
}

// convertAdmins turns the wire form into the role-keyed sets the verifier
// takes. Role semantics are pinned here; the contract enforces only that role
// tags are non-zero and unique.
func convertAdmins(w wireAdmins) (uint64, map[string]provision.AdminSet, error) {
	// Zero is the "never applied" sentinel for adminsVersion, so it could never
	// be accepted; saying so here beats a confusing version comparison.
	if w.Version == 0 {
		return 0, nil, errors.New("runtimecfg: admin version must be non-zero")
	}
	if err := checkFeedID(w.FeedId); err != nil {
		return 0, nil, err
	}

	sets := make(map[string]provision.AdminSet, len(w.Roles))
	for _, r := range w.Roles {
		tag, ok := tagString(r.Role)
		if !ok {
			return 0, nil, errors.New("runtimecfg: admin role tag is not a printable name")
		}
		if _, dup := sets[tag]; dup {
			return 0, nil, fmt.Errorf("runtimecfg: duplicate admin role %q", tag)
		}

		floor, known := provision.FloorForRole(tag)
		if !known {
			// Same posture as an unknown endpoint group: a role this build does
			// not understand means publisher and build disagree about the schema.
			return 0, nil, fmt.Errorf("runtimecfg: unknown admin role %q", tag)
		}

		set, err := parseAdminSet(tag, r, floor)
		if err != nil {
			return 0, nil, err
		}
		sets[tag] = set
	}

	// The sets MAY overlap — one person can hold both roles — so no disjointness
	// check. What matters is that each envelope is verified against exactly one.
	for _, role := range provision.Roles() {
		if _, ok := sets[role]; !ok {
			return 0, nil, fmt.Errorf("runtimecfg: admin role %q is missing", role)
		}
	}
	return w.Version, sets, nil
}

func parseAdminSet(role string, in wireAdminRole, floor int) (provision.AdminSet, error) {
	if len(in.Admins) == 0 || len(in.Admins) > provision.MaxAdmins {
		return provision.AdminSet{}, fmt.Errorf(
			"runtimecfg: %s admin count %d is not between 1 and %d",
			role, len(in.Admins), provision.MaxAdmins)
	}

	threshold, ok := narrow(in.Threshold)
	if !ok {
		return provision.AdminSet{}, fmt.Errorf("runtimecfg: %s admin threshold is out of range", role)
	}
	// The ceiling is the smaller of the set size and provision.MaxThreshold:
	// beyond the latter the verifier drops the envelope before it counts
	// signers, so a higher threshold would install here and then reject every
	// SET_SECRET that follows, with only an opaque rejection to go on.
	ceiling := min(len(in.Admins), provision.MaxThreshold)
	if threshold < floor || threshold > ceiling {
		return provision.AdminSet{}, fmt.Errorf(
			"runtimecfg: %s admin threshold %d is not between %d and %d",
			role, threshold, floor, ceiling)
	}

	seen := make(map[common.Address]bool, len(in.Admins))
	out := make([]common.Address, 0, len(in.Admins))
	for _, addr := range in.Admins {
		// The contract already rejects zero and duplicate admins within a role.
		// Re-checked because the floor and ceiling above are ours alone, and a
		// set that passed on-chain can still be unsatisfiable here.
		if addr == (common.Address{}) {
			return provision.AdminSet{}, fmt.Errorf("runtimecfg: %s admin address is zero", role)
		}
		if seen[addr] {
			return provision.AdminSet{}, fmt.Errorf("runtimecfg: duplicate %s admin address", role)
		}
		seen[addr] = true
		out = append(out, addr)
	}
	return provision.AdminSet{Admins: out, Threshold: threshold}, nil
}

// ApplySecret verifies a signed envelope and installs the credential it
// carries, decrypted inside the enclave and held only in memory.
func (s *Store) ApplySecret(ctx context.Context, message []byte) error {
	// The only Apply* reachable without an admin quorum's signatures, and the
	// work it hands off grows with the input. Checked before applyMu is taken,
	// so a large message cannot hold the provisioning lock while it is rejected.
	if len(message) == 0 || len(message) > maxPayloadBytes {
		return failure.ProvisionRejected
	}

	// Covers the verifier for the whole call: two concurrent envelopes for one
	// ref could otherwise both read the old version and both be accepted.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	if !s.verifier.HasTeeID() {
		teeID, err := s.node.TeeID(ctx)
		if err != nil {
			return failure.ProvisionRejected
		}
		s.verifier.SetTeeID(teeID)
	}

	env, err := s.verifier.Verify(message)
	if err != nil {
		return err
	}

	plaintext, err := s.node.Decrypt(ctx, env.Payload)
	if err != nil {
		return failure.ProvisionRejected
	}
	if len(plaintext) == 0 {
		return failure.ProvisionRejected
	}

	// Checked against the live configuration when there is one, so a URL whose
	// commitment does not match, or a credential that would restructure the URL
	// it lands in, fails the provisioning step that introduced it. When no
	// configuration has arrived yet there is nothing to check against, and
	// ApplyEndpoints re-runs this for every stored secret.
	//
	// The value is a credential: never logged, never wrapped into an error,
	// never returned.
	s.mu.RLock()
	endpoints := s.endpoints
	s.mu.RUnlock()
	if endpoints != nil {
		if err := verifySecret(endpoints, env.Ref, plaintext); err != nil {
			return failure.ProvisionRejected
		}
	}

	s.mu.Lock()
	s.secrets[env.Ref] = plaintext
	s.mu.Unlock()
	return nil
}

// verifySecret checks a provisioned value against every endpoint that names it.
// A ref the configuration does not mention is accepted as-is: it may be the
// pinned backing credential, or one a later publication will name.
func verifySecret(e *Endpoints, ref string, value []byte) error {
	if ref == config.HexAPIKeyRef {
		return checkCredential(ref, value)
	}

	for tag, g := range e.Groups {
		for _, ep := range g.Endpoints {
			if ep.SecretRef != ref {
				continue
			}

			if ep.Kind == KindPrivate {
				if _, err := resolvePrivateURL(ep, value); err != nil {
					return fmt.Errorf("runtimecfg: group %q: %w", tag, err)
				}
				continue
			}
			if err := checkCredential(ref, value); err != nil {
				return fmt.Errorf("runtimecfg: group %q: %w", tag, err)
			}
			if _, err := resolveURL(ep.URL, ep.SecretRef, string(value)); err != nil {
				return fmt.Errorf("runtimecfg: group %q: %w", tag, err)
			}
		}
	}
	return nil
}

// checkCredential enforces that a credential cannot restructure the URL it
// lands in or the header it is written into.
func checkCredential(ref string, value []byte) error {
	text := string(value)
	if text == "" || strings.ContainsAny(text, urlUnsafe) {
		// Names the ref, never the value.
		return fmt.Errorf("credential %q is empty or would alter the url", ref)
	}
	return nil
}

// resolvePrivateURL opens a PRIVATE endpoint's provisioned blob and checks it
// against the published commitment. The salt is what makes the commitment safe
// to publish: a bare hash of a low-entropy URL is brute-forceable from chain
// data.
func resolvePrivateURL(ep Endpoint, value []byte) (string, error) {
	salt, url, ok := decodePrivateURL(value)
	if !ok {
		return "", errors.New("private endpoint value is not an encoded (salt, url)")
	}
	if salt == (common.Hash{}) {
		return "", errors.New("private endpoint commitment is unsalted")
	}

	commitment, err := CommitPrivateURL(salt, url)
	if err != nil {
		return "", errors.New("private endpoint value could not be committed to")
	}
	if commitment != ep.URLHash {
		// The published commitment and the provisioned URL disagree. Neither is
		// named in the error: one is a secret and the other is attacker-visible.
		return "", errors.New("private endpoint does not match its commitment")
	}

	// A private URL carries no placeholder — there is no second ref to fill it —
	// so it is validated as a complete URL.
	if strings.Contains(url, secretPlaceholder) {
		return "", errors.New("private endpoint url may not contain a placeholder")
	}
	if _, err := resolveURL(url, "", ""); err != nil {
		return "", err
	}
	return url, nil
}

// Readiness reports what provisioning is still outstanding, in the shape
// GET /state publishes. Names refs, never values.
func (s *Store) Readiness() types.Readiness {
	s.mu.RLock()
	defer s.mu.RUnlock()

	missing := []string{}
	for _, ref := range s.required() {
		if len(s.secrets[ref]) == 0 {
			missing = append(missing, ref)
		}
	}

	return types.Readiness{
		EndpointsSet:  s.endpoints != nil,
		EndpointsHash: s.endpointsHash.Hex(),
		AdminsSet:     s.adminsSet,
		AdminsHash:    s.adminsHash.Hex(),
		// Admins are part of being provisioned, not merely a precondition for
		// it: without them no credential can arrive, and the store will not
		// accept an update that does not commit to a published set.
		Ready:       s.endpoints != nil && s.adminsSet && len(missing) == 0,
		MissingRefs: missing,
	}
}

// required lists every credential the current configuration needs. Callers hold
// at least the read lock.
func (s *Store) required() []string {
	// The Hex key stays listed even though Resolve no longer refuses without it:
	// Ready means "fully provisioned", not "able to answer", and an operator
	// needs to see that a machine is running cash-less.
	refs := []string{config.HexAPIKeyRef}
	if s.endpoints == nil {
		return refs
	}

	for _, tag := range sortedTags(s.endpoints.Groups) {
		for _, ep := range s.endpoints.Groups[tag].Endpoints {
			if ep.SecretRef != "" && !contains(refs, ep.SecretRef) {
				refs = append(refs, ep.SecretRef)
			}
		}
	}
	return refs
}

// Resolve turns the stored configuration into a live oracle.Config, once per
// observation. Cheap: an HTTP RPC client opens no connection until used.
func (s *Store) Resolve() (oracle.Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.endpoints == nil {
		return oracle.Config{}, failure.NotConfigured
	}
	// An update that cannot commit to published admin sets would be rejected by
	// the store anyway; refusing to observe says so at the honest point.
	if !s.adminsSet {
		return oracle.Config{}, failure.NotConfigured
	}

	bonds, err := newBacking(config.BondsSource())
	if err != nil {
		return oracle.Config{}, failure.Internal
	}

	// A nil cash source is how "running cash-less" is carried; the oracle then
	// publishes only where the figure provably cannot change the answer. Both
	// halves have to be present — the committed URL and the credential — and a
	// missing either is the same operational state.
	var cash backing.Source
	if url, ok := s.cashURL(); ok {
		if cashKey := string(s.secrets[config.HexAPIKeyRef]); cashKey != "" {
			cash, err = newBacking(config.CashSource(url, cashKey))
			if err != nil {
				return oracle.Config{}, failure.Internal
			}
		}
	}

	chains := make([]supply.ChainConfig, 0, len(config.Chains))
	for _, pinned := range config.Chains {
		g, ok := s.endpoints.Groups[pinned.Name]
		if !ok {
			// A missing chain undercounts supply, which OVERSTATES the price.
			return oracle.Config{}, failure.NotConfigured
		}

		providers := make([]supply.Provider, 0, len(g.Endpoints))
		for i, ep := range g.Endpoints {
			url, err := s.endpointURL(ep)
			if err != nil {
				return oracle.Config{}, failure.NotConfigured
			}
			client, err := ethclient.Dial(url)
			if err != nil {
				return oracle.Config{}, failure.NotConfigured
			}
			// Positional names, so nothing an operator supplies reaches /state.
			providers = append(providers, supply.Provider{
				Name:   fmt.Sprintf("rpc_%s_%d", pinned.Name, i+1),
				Client: client,
			})
		}

		chains = append(chains, supply.ChainConfig{
			Name:      pinned.Name,
			Token:     common.HexToAddress(pinned.Token),
			Providers: providers,
			Quorum:    g.Threshold,
			// From the pinned policy, never from the payload: the party that
			// chooses the providers must not also choose how stale a block from
			// them may be.
			MaxBlockAge: pinned.MaxBlockAge,
		})
	}

	cfg := oracle.Config{
		Chains: chains, Bonds: bonds, Cash: cash,
		ExtensionID:   config.ExtensionID,
		FeedID:        config.FeedID,
		EndpointsHash: s.endpointsHash,
		AdminsHash:    s.adminsHash,
	}
	if err := cfg.Validate(); err != nil {
		return oracle.Config{}, failure.Internal
	}
	return cfg, nil
}

// endpointURL resolves one endpoint to a dialable URL. Callers hold at least
// the read lock.
func (s *Store) endpointURL(ep Endpoint) (string, error) {
	if ep.Kind == KindPrivate {
		return resolvePrivateURL(ep, s.secrets[ep.SecretRef])
	}
	return resolveURL(ep.URL, ep.SecretRef, string(s.secrets[ep.SecretRef]))
}

// cashURL resolves the restricted cash endpoint. Callers hold at least the read
// lock. A machine with no provisioned URL runs cash-less rather than failing.
func (s *Store) cashURL() (string, bool) {
	g, ok := s.endpoints.Groups[config.HexCashGroup]
	if !ok || len(g.Endpoints) == 0 {
		return "", false
	}
	url, err := s.endpointURL(g.Endpoints[0])
	if err != nil {
		return "", false
	}
	return url, true
}

func newBacking(spec config.BackingSpec) (backing.Source, error) {
	return backing.New(backing.Config{
		Name: spec.Name, URL: spec.URL,
		RequireCurrency: spec.RequireCurrency,
		Timeout:         spec.Timeout,
		AuthHeader:      spec.AuthHeader, AuthValue: spec.AuthValue,
	})
}

// validate checks a decoded payload against the rules the contract does not
// know: which tags this build requires, what each one means, and the URL shape
// rules. Pure string work, so a bad configuration is rejected when sent rather
// than at the next observation.
func validate(e Endpoints) error {
	if e.Version == 0 {
		return errors.New("runtimecfg: endpoints version must be non-zero")
	}
	if err := checkFeedID(e.FeedID); err != nil {
		return err
	}
	if len(e.Groups) == 0 {
		return errors.New("runtimecfg: no endpoint groups configured")
	}

	for _, tag := range sortedTags(e.Groups) {
		g := e.Groups[tag]

		_, isChain := config.LookupChain(tag)
		if !isChain && tag != config.HexCashGroup {
			return fmt.Errorf("runtimecfg: unknown endpoint group %q", tag)
		}
		if len(g.Endpoints) == 0 {
			return fmt.Errorf("runtimecfg: group %q has no endpoints", tag)
		}
		if g.Threshold < 1 || g.Threshold > len(g.Endpoints) {
			return fmt.Errorf("runtimecfg: group %q threshold %d is not between 1 and %d",
				tag, g.Threshold, len(g.Endpoints))
		}

		if tag == config.HexCashGroup {
			if err := validateCashGroup(g); err != nil {
				return err
			}
		}

		for _, ep := range g.Endpoints {
			if err := validateEndpoint(tag, isChain, ep); err != nil {
				return err
			}
		}
	}

	for _, c := range config.Chains {
		if _, ok := e.Groups[c.Name]; !ok {
			return fmt.Errorf("runtimecfg: endpoint group %q is missing", c.Name)
		}
	}
	// Without it the machine cannot even run cash-less deliberately — it would
	// run cash-less because a group was forgotten, which is a different thing.
	if _, ok := e.Groups[config.HexCashGroup]; !ok {
		return fmt.Errorf("runtimecfg: endpoint group %q is missing", config.HexCashGroup)
	}
	return nil
}

// validateCashGroup pins the restricted reserve endpoint. PRIVATE is required,
// not merely allowed: a PUBLIC entry would publish the restricted URL on-chain
// in the clear AND let whoever publishes configuration point the numerator of
// the price wherever they liked. The commitment is what removes that power.
func validateCashGroup(g Group) error {
	if len(g.Endpoints) != 1 || g.Threshold != 1 {
		return fmt.Errorf(
			"runtimecfg: group %q must hold exactly one endpoint at threshold 1", config.HexCashGroup)
	}
	ep := g.Endpoints[0]
	if ep.Kind != KindPrivate {
		return fmt.Errorf("runtimecfg: group %q must be PRIVATE", config.HexCashGroup)
	}
	// Pinned so the role that governs the cash URL is not the publisher's
	// choice — see provision.FloorForRole and the backing ref set.
	if ep.SecretRef != config.HexCashURLRef {
		return fmt.Errorf(
			"runtimecfg: group %q must name secret %q", config.HexCashGroup, config.HexCashURLRef)
	}
	return nil
}

func validateEndpoint(tag string, isChain bool, ep Endpoint) error {
	switch ep.Kind {
	case KindPrivate:
		// Mirrors the contract's kind-consistency rule. Re-checked because a
		// decoder that let a malformed pair through would otherwise dial a URL
		// nobody committed to.
		if ep.URL != "" || ep.URLHash == (common.Hash{}) {
			return fmt.Errorf("runtimecfg: group %q: private endpoint must carry only a commitment", tag)
		}
		if ep.SecretRef == "" {
			return fmt.Errorf("runtimecfg: group %q: private endpoint needs a secret ref", tag)
		}
	case KindPublic:
		if ep.URL == "" || ep.URLHash != (common.Hash{}) {
			return fmt.Errorf("runtimecfg: group %q: public endpoint must carry only a url", tag)
		}
		// A dummy credential: this checks URL shape, not whether the secret has
		// arrived yet.
		if _, err := resolveURL(ep.URL, ep.SecretRef, "placeholder"); err != nil {
			return fmt.Errorf("runtimecfg: group %q: %w", tag, err)
		}
	default:
		return fmt.Errorf("runtimecfg: group %q: unknown endpoint kind", tag)
	}

	// Without this a payload author could name a backing credential as a chain
	// provider's secret and have it substituted into a host of their choosing.
	if isChain && config.IsBackingRef(ep.SecretRef) {
		return fmt.Errorf(
			"runtimecfg: group %q: %q governs the backing sources and may not be used here",
			tag, ep.SecretRef)
	}
	return nil
}

// resolveURL validates a URL's shape and substitutes a credential. It says
// nothing about WHICH host is named, only that a credential cannot change it.
func resolveURL(raw, secretRef, value string) (string, error) {
	hasPlaceholder := strings.Contains(raw, secretPlaceholder)
	if hasPlaceholder != (secretRef != "") {
		return "", errors.New("{secret} and secret must be used together")
	}
	if !strings.HasPrefix(raw, "https://") {
		return "", errors.New("url must use https")
	}
	if !hasPlaceholder {
		return raw, nil
	}

	// The placeholder must sit after the authority, so a credential cannot
	// change the host.
	authorityEnd := strings.Index(raw[len("https://"):], "/")
	if authorityEnd < 0 {
		return "", errors.New("{secret} may not appear in the host")
	}
	if strings.Index(raw, secretPlaceholder) <= len("https://")+authorityEnd {
		return "", errors.New("{secret} may not appear in the scheme or host")
	}

	if value == "" {
		return "", fmt.Errorf("secret %q is not set", secretRef)
	}
	if strings.ContainsAny(value, urlUnsafe) {
		return "", errors.New("secret would alter the url")
	}
	return strings.ReplaceAll(raw, secretPlaceholder, value), nil
}

// sortedTags gives map iteration a fixed order, so a payload with two problems
// is always reported by the same one.
func sortedTags(groups map[string]Group) []string {
	tags := make([]string, 0, len(groups))
	for tag := range groups {
		tags = append(tags, tag)
	}
	for i := 1; i < len(tags); i++ {
		for j := i; j > 0 && tags[j] < tags[j-1]; j-- {
			tags[j], tags[j-1] = tags[j-1], tags[j]
		}
	}
	return tags
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
