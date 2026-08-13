// Package runtimecfg holds the three things that arrive after startup: RPC
// provider URLs, the admin signer set, and credentials. Everything else is
// pinned in internal/config.
//
//	ApplyEndpoints ─┐
//	ApplyAdmins    ─┼─► Store ──► Resolve() ──► oracle.Config
//	ApplySecret    ─┘
//
// Provider URLs and thresholds are unconstrained by decision — no host
// allowlist — so whoever can send SET_ENDPOINTS can choose the published price.
// What is still enforced is https only and a credential that cannot restructure
// the URL it lands in.
//
// All three are memory-only; a restart returns the extension to not_configured.
// For admins that is load-bearing rather than incidental: the set cannot be
// installed by anyone but the owner key, so a restarted machine fails closed on
// SET_SECRET until SET_ADMINS is republished, and there is no window in which a
// first-come envelope could claim the enclave.
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

// secretPlaceholder may appear in the path or a query value, never in the
// scheme or host.
const secretPlaceholder = "{secret}"

const maxPayloadBytes = 256 << 10

// urlUnsafe lists characters a credential may not contain, because each can
// restructure the URL it is substituted into. The URL author (owner key, via
// SET_ENDPOINTS) and the credential holder (admins, via SET_SECRET) are
// different parties. Declared once and used at both checkpoints — two copies of
// a security-relevant character set drift.
const urlUnsafe = " \t\r\n/?#&"

// Endpoints is the entire SET_ENDPOINTS payload. Chain names must match
// internal/config.Chains.
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
type Endpoints map[string]ChainEndpoints

type ChainEndpoints struct {
	// How many providers must return identical results at the agreed block.
	Threshold int `json:"threshold"`

	Providers []Provider `json:"providers"`
}

type Provider struct {
	URL string `json:"url"`

	// Names the credential substituted for {secret} in URL. A name, never a value.
	Secret string `json:"secret,omitempty"`
}

// Admins is the entire SET_ADMINS payload.
//
//	{
//	  "version": 1,
//	  "threshold": 2,
//	  "admins": [
//	    "0x1111111111111111111111111111111111111111",
//	    "0x2222222222222222222222222222222222222222",
//	    "0x3333333333333333333333333333333333333333"
//	  ]
//	}
//
// Addresses only — never keys, and nothing secret. The whole payload is public
// on-chain by construction, which is the point: who may provision a machine is
// as verifiable as which providers it reads.
type Admins struct {
	// Strictly increasing across the life of the process. Blocks a captured
	// SET_ADMINS from reinstating a superseded signer set.
	Version uint64 `json:"version"`

	// How many DISTINCT admins must sign a provisioning envelope.
	Threshold int `json:"threshold"`

	Admins []string `json:"admins"`
}

// Node is the subset of tee-node this package needs.
type Node interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
	TeeID(ctx context.Context) (common.Address, error)
}

// Store receives provisioning messages and resolves them into an oracle.Config.
type Store struct {
	// applyMu covers a whole Apply* call including the verifier, which keeps
	// per-ref version state in a plain map: /action is an ordinary net/http
	// handler, so two concurrent direct actions would race it.
	//
	// mu covers only the data fields, in short critical sections. Deliberately
	// NOT held across the tee-node round trips in ApplySecret, or a hung
	// /decrypt would freeze GET /state and every observation.
	//
	// Order is applyMu then mu, never the reverse.
	applyMu sync.Mutex

	mu        sync.RWMutex
	endpoints Endpoints

	// keccak256 of the raw payload AS RECEIVED. It must equal keccak256(_config)
	// from the setEndpoints transaction, so it cannot be recomputed from the
	// decoded struct.
	endpointsHash common.Hash

	// keccak256 of the raw SET_ADMINS payload as received, on the same terms:
	// UsdxPriceStore compares it against expectedAdminsHash.
	adminsHash common.Hash

	// adminsSet records that an admin set was installed, so Readiness can
	// distinguish "no admins yet" from "admins installed" without reaching into
	// the verifier, which is guarded by applyMu rather than mu.
	adminsSet bool

	// Highest SET_ADMINS version applied. The instruction channel has the
	// registry's own replay protection, but the admin set is the root of trust
	// for every credential that follows, so this does not rely on it: a captured
	// SET_ADMINS must not be able to reinstate a rotated-out signer.
	//
	// In-memory like everything else here, which is safe in the direction that
	// matters — a restart clears it, and the only party who can exploit that is
	// the owner key, who can publish any set it likes anyway.
	adminsVersion uint64

	secrets map[string]string

	verifier *provision.Verifier
	node     Node
}

func New(verifier *provision.Verifier, node Node) *Store {
	return &Store{secrets: map[string]string{}, verifier: verifier, node: node}
}

// ApplyEndpoints installs a provider configuration from an on-chain
// instruction. Authorisation is the owner-only check in the instruction sender;
// what is checked here is content.
func (s *Store) ApplyEndpoints(payload []byte) error {
	// Held even though this path does not touch the verifier: the rule is one
	// provisioning operation at a time.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return errors.New("runtimecfg: endpoint payload is empty or too large")
	}

	var e Endpoints
	dec := json.NewDecoder(io.LimitReader(strings.NewReader(string(payload)), maxPayloadBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		// An unrecognised field means publisher and build disagree about the
		// schema; stopping beats applying the subset we understand.
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

// ApplyAdmins installs the admin signer set from an on-chain instruction.
// Authorisation is the owner-only check in the instruction sender, exactly as
// for endpoints; what is checked here is content.
//
// The two are separate commands so either can be rotated without republishing
// the other, and because they answer different questions: SET_ENDPOINTS decides
// which sources a price comes from, SET_ADMINS decides who may put credentials
// in the machine that reads them.
func (s *Store) ApplyAdmins(payload []byte) error {
	// Covers the verifier for the whole call, like ApplySecret: SetAdmins writes
	// state that a concurrent Verify would read.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return errors.New("runtimecfg: admin payload is empty or too large")
	}

	var a Admins
	dec := json.NewDecoder(io.LimitReader(strings.NewReader(string(payload)), maxPayloadBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return errors.New("runtimecfg: payload is not a known admin set")
	}

	admins, err := validateAdmins(a)
	if err != nil {
		return err
	}

	// Strictly increasing: a captured envelope must not reinstate a superseded
	// set. Checked before SetAdmins so a replay leaves the live set untouched.
	if a.Version <= s.adminsVersion {
		return fmt.Errorf(
			"runtimecfg: admin version %d is not newer than %d", a.Version, s.adminsVersion)
	}

	if err := s.verifier.SetAdmins(admins, a.Threshold); err != nil {
		return err
	}

	s.mu.Lock()
	s.adminsHash = crypto.Keccak256Hash(payload)
	s.adminsVersion = a.Version
	s.adminsSet = true
	s.mu.Unlock()
	return nil
}

// validateAdmins checks a payload's structure and returns the parsed set. Pure
// string work, so a bad admin set is rejected when sent rather than at the next
// provisioning attempt.
func validateAdmins(a Admins) ([]common.Address, error) {
	// Zero is the "never applied" sentinel for adminsVersion, so it could never
	// be accepted; saying so here beats a confusing version comparison.
	if a.Version == 0 {
		return nil, errors.New("runtimecfg: admin version must be non-zero")
	}
	if len(a.Admins) == 0 || len(a.Admins) > provision.MaxAdmins {
		return nil, fmt.Errorf(
			"runtimecfg: admin count %d is not between 1 and %d", len(a.Admins), provision.MaxAdmins)
	}
	if a.Threshold < provision.MinThreshold || a.Threshold > len(a.Admins) {
		return nil, fmt.Errorf(
			"runtimecfg: admin threshold %d is not between %d and %d",
			a.Threshold, provision.MinThreshold, len(a.Admins))
	}

	seen := make(map[common.Address]bool, len(a.Admins))
	out := make([]common.Address, 0, len(a.Admins))
	for _, raw := range a.Admins {
		text := strings.TrimSpace(raw)
		// common.HexToAddress silently yields the zero address for anything it
		// cannot parse, so a typo would otherwise install a signer nobody holds
		// and quietly shrink the effective set.
		if !common.IsHexAddress(text) {
			return nil, errors.New("runtimecfg: admin is not a hex address")
		}
		addr := common.HexToAddress(text)
		if addr == (common.Address{}) {
			return nil, errors.New("runtimecfg: admin address is zero")
		}
		if seen[addr] {
			return nil, errors.New("runtimecfg: duplicate admin address")
		}
		seen[addr] = true
		out = append(out, addr)
	}
	return out, nil
}

// ApplySecret verifies a signed envelope and installs the credential it
// carries, decrypted inside the enclave and held only in memory.
func (s *Store) ApplySecret(ctx context.Context, message []byte) error {
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

	// A credential: never logged, never wrapped into an error, never returned.
	value := string(plaintext)
	if value == "" || strings.ContainsAny(value, urlUnsafe) {
		return failure.ProvisionRejected
	}

	s.mu.Lock()
	s.secrets[env.Ref] = value
	s.mu.Unlock()
	return nil
}

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
		AdminsSet:     s.adminsSet,
		AdminsHash:    s.adminsHash.Hex(),
		// Admins are part of being provisioned, not merely a precondition for
		// it: without them no credential can arrive, and the store will not
		// accept a price that does not commit to a published set.
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

	for _, ce := range s.endpoints {
		for _, p := range ce.Providers {
			if p.Secret != "" && !contains(refs, p.Secret) {
				refs = append(refs, p.Secret)
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
	// A price that cannot commit to a published admin set would be rejected by
	// the store anyway; refusing to observe says so at the honest point.
	if !s.adminsSet {
		return oracle.Config{}, failure.NotConfigured
	}

	bonds, err := newBacking(config.BondsSource())
	if err != nil {
		return oracle.Config{}, failure.Internal
	}

	// A nil cash source is how "running cash-less" is carried; the oracle then
	// publishes only where the figure provably cannot change the answer.
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
			Quorum:    ce.Threshold,
			// From the pinned policy, never from the payload: the party that
			// chooses the providers must not also choose how stale a block from
			// them may be.
			MaxBlockAge: pinned.MaxBlockAge,
		})
	}

	cfg := oracle.Config{
		Chains: chains, Bonds: bonds, Cash: cash,
		EndpointsHash: s.endpointsHash,
		AdminsHash:    s.adminsHash,
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

// validate checks a payload's structure. Pure string work, so a bad
// configuration is rejected when sent rather than at the next observation.
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
			return fmt.Errorf("runtimecfg: chain %q threshold %d is not between 1 and %d",
				name, ce.Threshold, len(ce.Providers))
		}
		for _, p := range ce.Providers {
			// The backing credential authenticates the PINNED Hex endpoint by
			// header. Without this check a payload author could name it as a
			// provider secret and have it substituted into a host of their
			// choosing, receiving it on the next observation.
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

// resolveURL validates a URL's shape and substitutes a credential. It says
// nothing about WHICH host is named; it enforces that the transport is
// encrypted and that an injected credential cannot change where the request
// goes.
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

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
