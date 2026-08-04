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
	if !s.verifier.HasTeeID() {
		teeID, err := s.node.TeeID(ctx)
		if err != nil {
			return failure.ProvisionRejected
		}
		s.verifier.SetTeeID(teeID)
	}

	env, err := s.verifier.Verify(message) //todo: What is this doing exactly?
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
	if value == "" || strings.ContainsAny(value, " \t\r\n/?#") {
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
	// The Hex key is always required: cash reserves are half the numerator, so
	// computing backing without it would understate it.
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

	cashKey := s.secrets[config.HexAPIKeyRef]
	if cashKey == "" {
		return oracle.Config{}, failure.NotConfigured
	}

	bonds, err := newBacking(config.BondsSource())
	if err != nil {
		return oracle.Config{}, failure.Internal
	}
	cash, err := newBacking(config.CashSource(cashKey))
	if err != nil {
		return oracle.Config{}, failure.Internal
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
	if strings.ContainsAny(value, " \t\r\n/?#") {
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
