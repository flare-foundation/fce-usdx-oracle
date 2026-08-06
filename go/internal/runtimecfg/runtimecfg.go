// Package runtimecfg holds the two things that arrive after startup: RPC
// provider URLs and credentials.
//
// Everything else — token addresses, finality behaviour, the Hex endpoints, the
// observation bounds, the output encoding — is a settled fact in
// internal/config, compiled in and covered by the registered code hash.
//
// # Shape
//
// One type. It receives provisioning messages, holds what they carried, and
// resolves that into an oracle.Config on demand:
//
//	ApplyEndpoints ─┐
//	                ├─► Store ──► Resolve() ──► oracle.Config
//	ApplySecret   ──┘
//
// Resolve runs per observation rather than being cached. Dialling an HTTP RPC
// endpoint opens no connection, so it costs nothing, and it removes a whole
// class of staleness bug: there is no snapshot that can disagree with the
// configuration it came from.
//
// # The provider list is unconstrained, by decision
//
// Provider URLs and the per-chain threshold are set entirely at runtime, with no
// host allowlist. Whoever can send SET_ENDPOINTS can name any host and any
// threshold, and can therefore choose the published price. The trade — no
// rebuild to add a provider or change 2-of-3 to 2-of-4 — was made deliberately;
// see finding 15.
//
// What is still enforced is unrelated to which host is named: https only, and a
// credential that cannot restructure the URL it is substituted into.
//
// # Credentials are memory-only
//
// Nothing is written to disk. A restart loses every credential and the
// extension returns to not_configured until the keeper re-submits.
package runtimecfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// secretPlaceholder marks where a credential is substituted into a URL. It may
// appear in the path or a query value, never in the scheme or host.
const secretPlaceholder = "{secret}"

// maxPayloadBytes caps an endpoint configuration.
const maxPayloadBytes = 256 << 10

// urlUnsafe lists characters a credential may not contain, because each one can
// restructure the URL it is substituted into: '/' changes the path, '?' opens a
// query and '&' splits or appends parameters, '#' truncates the rest to a
// fragment, and whitespace terminates the request line.
//
// This matters because the URL author (the owner key, via SET_ENDPOINTS) and the
// credential holder (the admins, via SET_SECRET) are different parties. Neither
// should be able to change what the other authorised.
//
// Declared once and used at both checkpoints — ApplySecret on arrival and
// resolveURL at substitution. Two copies of a security-relevant character set
// drift, and the one that drifts is the one that stops matching.
const urlUnsafe = " \t\r\n/?#&"

// Endpoints is the entire SET_ENDPOINTS payload.
//
//	{
//	  "flare": {
//	    "threshold": 2,
//	    "providers": [
//	      {"url": "https://flare-api.flare.network/ext/C/rpc"},
//	      {"url": "https://rpc.ankr.com/flare/{secret}", "secret": "ankr_flare_key"}
//	    ]
//	  }
//	}
//
// Chain names must match internal/config.Chains, which supplies the token
// address and finality behaviour.
type Endpoints map[string]ChainEndpoints

// ChainEndpoints is one chain's provider set and agreement rule.
type ChainEndpoints struct {
	// Threshold is how many providers must return identical results at the
	// agreed block. Identical, not median: a read at a fixed block is
	// deterministic, so disagreement means fault rather than noise.
	Threshold int `json:"threshold"`

	Providers []Provider `json:"providers"`
}

// Provider is one RPC endpoint. Two fields, and Secret is usually absent —
// there is no name to supply, so nothing an operator writes reaches the public
// GET /state.
type Provider struct {
	URL string `json:"url"`

	// Secret names the credential substituted for {secret} in URL. A name,
	// never a value.
	Secret string `json:"secret,omitempty"`
}

// Node is the subset of tee-node this package needs.
type Node interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)

	// TeeID is this machine's own address, which envelopes are bound to.
	TeeID(ctx context.Context) (common.Address, error)
}

// Store receives provisioning messages and resolves them into an oracle.Config.
type Store struct {
	// applyMu serialises provisioning. Two locks rather than one because they
	// protect different things over different spans:
	//
	// applyMu covers a whole Apply* call, including the verifier — which keeps
	// per-ref version state in a plain map and is not safe for concurrent use.
	// Nothing serialises us above: /action is an ordinary net/http handler, so
	// tee-node delivering two direct actions at once runs two ApplySecret
	// goroutines, and a concurrent map write is a hard runtime throw that takes
	// the process down.
	//
	// mu covers only the data fields, in short critical sections. It is
	// deliberately NOT held across the tee-node round trips in ApplySecret: it
	// is also taken by Readiness and Resolve, so a hung /decrypt would otherwise
	// freeze GET /state and every observation.
	//
	// Order is applyMu then mu, never the reverse.
	applyMu sync.Mutex

	mu        sync.RWMutex
	endpoints Endpoints

	// endpointsHash is keccak256 of the raw payload that produced endpoints,
	// hashed AS RECEIVED. It goes into every signed price report and must equal
	// keccak256(_config) from the setEndpoints transaction, so it cannot be
	// recomputed from the decoded struct — json.Marshal would not reproduce the
	// original bytes.
	endpointsHash common.Hash

	secrets map[string]string

	verifier *provision.Verifier
	node     Node
}

// New returns an unprovisioned Store.
func New(verifier *provision.Verifier, node Node) *Store {
	return &Store{secrets: map[string]string{}, verifier: verifier, node: node}
}

// --- provisioning ---

// ApplyEndpoints installs a provider configuration from an on-chain
// instruction.
//
// No envelope: authorisation is the owner-only check in the instruction sender,
// and the extension has already established this arrived over the instruction
// channel. What is checked here is content.
func (s *Store) ApplyEndpoints(payload []byte) error {
	// Held even though this path does not touch the verifier: the rule is one
	// provisioning operation at a time, which stays true as this code changes.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return errors.New("runtimecfg: endpoint payload is empty or too large")
	}

	var e Endpoints
	dec := json.NewDecoder(io.LimitReader(strings.NewReader(string(payload)), maxPayloadBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		// An unrecognised field means the publisher and this build disagree
		// about the schema. Stopping is safer than applying the subset we
		// happen to understand.
		return errors.New("runtimecfg: payload is not a known configuration")
	}

	if err := validate(e); err != nil {
		return err
	}

	s.mu.Lock()
	s.endpoints = e
	s.endpointsHash = crypto.Keccak256Hash(payload)
	s.mu.Unlock()
	return nil
}

// ApplySecret verifies a signed envelope and installs the credential it carries.
//
// The payload is a ciphertext encrypted to this TEE's public key, so the
// plaintext exists only inside the enclave and only in memory.
func (s *Store) ApplySecret(ctx context.Context, message []byte) error {
	// Covers the verifier for the whole call: SetTeeID writes a field, and
	// Verify writes lastVersion[ref] on success. Both are unsynchronised, and
	// the replay defence itself depends on this — two concurrent envelopes for
	// one ref could otherwise both read the old version and both be accepted.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	if !s.verifier.HasTeeID() {
		teeID, err := s.node.TeeID(ctx)
		if err != nil {
			return failure.ProvisionRejected
		}
		s.verifier.SetTeeID(teeID)
	}

	// Verify checks the envelope against the baked admin set: chain and machine
	// binding, payload hash, freshness, per-ref version, then at least Threshold
	// distinct admin signatures over the re-encoded body.
	env, err := s.verifier.Verify(message)
	if err != nil {
		return err
	}

	plaintext, err := s.node.Decrypt(ctx, env.Payload)
	if err != nil {
		return failure.ProvisionRejected
	}

	// The plaintext is a credential. Never logged, never wrapped into an error,
	// never returned.
	value := string(plaintext)
	if value == "" || strings.ContainsAny(value, urlUnsafe) {
		// A credential containing a URL separator could restructure an endpoint
		// after substitution.
		return failure.ProvisionRejected
	}

	s.mu.Lock()
	s.secrets[env.Ref] = value
	s.mu.Unlock()
	return nil
}

// --- readiness ---

// Readiness reports what provisioning is still outstanding, in the shape
// GET /state publishes. Names refs, never values.
func (s *Store) Readiness() types.Readiness {
	s.mu.RLock()
	defer s.mu.RUnlock()

	missing := []string{}
	for _, ref := range s.required() {
		if s.secrets[ref] == "" {
			missing = append(missing, ref)
		}
	}

	return types.Readiness{
		EndpointsSet:  s.endpoints != nil,
		EndpointsHash: s.endpointsHash.Hex(),
		Ready:         s.endpoints != nil && len(missing) == 0,
		MissingRefs:   missing,
	}
}

// required lists every credential the current configuration needs. Callers hold
// at least the read lock.
func (s *Store) required() []string {
	// The Hex key stays on this list even though Resolve no longer refuses
	// without it. Ready means "fully provisioned", not "able to answer": a
	// machine missing this key can still publish, but only prices that do not
	// depend on the cash figure, and every other observation fails closed. That
	// is a state an operator must be able to see, and MissingRefs naming the
	// ref is how they see it.
	refs := []string{config.HexAPIKeyRef}

	for _, ce := range s.endpoints {
		for _, p := range ce.Providers {
			if p.Secret != "" && !contains(refs, p.Secret) {
				refs = append(refs, p.Secret)
			}
		}
	}
	return refs
}

// --- resolution ---

// Resolve turns the stored configuration into a live oracle.Config.
//
// Called once per observation. Everything it builds is cheap: an HTTP RPC
// client opens no connection until it is used.
func (s *Store) Resolve() (oracle.Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.endpoints == nil {
		return oracle.Config{}, failure.NotConfigured
	}

	bonds, err := newBacking(config.BondsSource())
	if err != nil {
		return oracle.Config{}, failure.Internal
	}

	// A missing cash credential no longer fails the observation outright. The
	// oracle runs cash-less and publishes only where the figure provably cannot
	// change the answer, failing closed everywhere else — see
	// price.ComputeWithoutCash. Resolving to a nil source is how that state is
	// carried; readiness still names the ref as missing.
	var cash backing.Source
	if cashKey := s.secrets[config.HexAPIKeyRef]; cashKey != "" {
		cash, err = newBacking(config.CashSource(cashKey))
		if err != nil {
			return oracle.Config{}, failure.Internal
		}
	}

	chains := make([]supply.ChainConfig, 0, len(config.Chains))
	for _, pinned := range config.Chains {
		ce, ok := s.endpoints[pinned.Name]
		if !ok {
			// A missing chain undercounts supply, which OVERSTATES the price.
			return oracle.Config{}, failure.NotConfigured
		}

		providers := make([]supply.Provider, 0, len(ce.Providers))
		for i, p := range ce.Providers {
			url, err := resolveURL(p.URL, p.Secret, s.secrets[p.Secret])
			if err != nil {
				return oracle.Config{}, failure.NotConfigured
			}
			client, err := ethclient.Dial(url)
			if err != nil {
				return oracle.Config{}, failure.NotConfigured
			}
			// Names are positional, so nothing an operator supplies reaches the
			// public GET /state.
			providers = append(providers, supply.Provider{
				Name:   fmt.Sprintf("rpc_%s_%d", pinned.Name, i+1),
				Client: client,
			})
		}

		tag := supply.TagLatest
		if pinned.FinalityTag == string(supply.TagFinalized) {
			tag = supply.TagFinalized
		}

		chains = append(chains, supply.ChainConfig{
			Name:           pinned.Name,
			Token:          common.HexToAddress(pinned.Token),
			Providers:      providers,
			Quorum:         ce.Threshold,
			FinalityTag:    tag,
			FinalityMargin: pinned.FinalityMargin,
		})
	}

	cfg := oracle.Config{
		Chains: chains, Bonds: bonds, Cash: cash,
		EndpointsHash: s.endpointsHash,
	}
	if err := cfg.Validate(); err != nil {
		return oracle.Config{}, failure.Internal
	}
	return cfg, nil
}

func newBacking(spec config.BackingSpec) (backing.Source, error) {
	return backing.New(backing.Config{
		Name: spec.Name, URL: spec.URL,
		RequireCurrency: spec.RequireCurrency,
		MaxAge:          spec.MaxAge, Timeout: spec.Timeout,
		AuthHeader: spec.AuthHeader, AuthValue: spec.AuthValue,
	})
}

// --- validation ---

// validate checks a payload's structure. Pure string work: no dialling, no
// secrets needed, so a bad configuration is rejected when it is sent rather
// than surfacing later as a failed observation.
func validate(e Endpoints) error {
	if len(e) == 0 {
		return errors.New("runtimecfg: no chains configured")
	}

	for name, ce := range e {
		if _, ok := config.LookupChain(name); !ok {
			return fmt.Errorf("runtimecfg: unknown chain %q", name)
		}
		if len(ce.Providers) == 0 {
			return fmt.Errorf("runtimecfg: chain %q has no providers", name)
		}
		if ce.Threshold < 1 || ce.Threshold > len(ce.Providers) {
			// A threshold outside this range can never be met, so it would
			// configure a chain that fails on every observation.
			return fmt.Errorf("runtimecfg: chain %q threshold %d is not between 1 and %d",
				name, ce.Threshold, len(ce.Providers))
		}
		for _, p := range ce.Providers {
			// The backing credential authenticates the PINNED Hex endpoint, by
			// header, and has no business in a provider URL. Without this, a
			// payload author could name it as a provider secret and have it
			// substituted into a path on a host of their choosing — receiving it
			// on the next observation.
			//
			// This is the URL-ref / auth-ref split enforced at the boundary
			// rather than left to convention. It matters most for this ref
			// because it is the one credential whose destination is otherwise
			// pinned and therefore not the owner's to choose.
			if p.Secret == config.HexAPIKeyRef {
				return fmt.Errorf(
					"runtimecfg: chain %q: %q authenticates the backing source and may not be used as a provider secret",
					name, config.HexAPIKeyRef)
			}

			// A dummy credential: this checks URL shape, not whether the secret
			// has arrived yet.
			if _, err := resolveURL(p.URL, p.Secret, "placeholder"); err != nil {
				return fmt.Errorf("runtimecfg: chain %q: %w", name, err)
			}
		}
	}

	for _, c := range config.Chains {
		if _, ok := e[c.Name]; !ok {
			return fmt.Errorf("runtimecfg: chain %q is missing", c.Name)
		}
	}
	return nil
}

// resolveURL validates a URL's shape and substitutes a credential.
//
// It says nothing about WHICH host is named — that is unconstrained by
// decision. What it enforces is that the transport is encrypted, and that an
// injected credential cannot change where the request goes. Those matter
// because the URL author and the credential holder can be different parties.
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
	// change the host. Everything before the first '/' after "https://" is the
	// authority.
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

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
