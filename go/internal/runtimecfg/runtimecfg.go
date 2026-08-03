// Package runtimecfg holds configuration that arrives after startup.
//
// Endpoints come by on-chain instruction and credentials by direct instruction,
// so the extension boots with neither and assembles an oracle.Config only once
// both are present. Until then it is not_configured and refuses to price.
//
// # Credentials are memory-only, and that is deliberate
//
// Nothing here is written to disk. A restart loses every credential and the
// extension returns to not_configured until the keeper re-submits. That is the
// intended trade: a TEE that persisted secrets would need somewhere to persist
// them to, and that store becomes a thing to protect, back up, and eventually
// leak. The cost is a provisioning round trip after every restart, which the
// keeper automates by holding the ciphertext envelopes — safe precisely because
// they are encrypted to the TEE.
//
// # The host allowlist is the substitution defence
//
// Endpoints arrive over a channel we do not fully control, so an attacker who
// could set them could point a chain at a server they control and dictate the
// price outright. The allowlist is baked into the image and covered by the code
// hash: whatever endpoints are configured, they can only ever be hosts that were
// attested. Publishing endpoints on-chain then makes the data sources
// verifiable without making them substitutable.
package runtimecfg

import (
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"

	"fce-usdx-oracle/internal/backing"
	"fce-usdx-oracle/internal/oracle"
	"fce-usdx-oracle/internal/report"
	"fce-usdx-oracle/internal/supply"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// secretPlaceholder marks where a credential is substituted into a URL. It may
// appear in the path or a query value, never in the scheme or host.
const secretPlaceholder = "{secret}"

// Endpoints is the configuration delivered by SET_ENDPOINTS.
//
// JSON rather than ABI: it is a nested config blob carried as opaque bytes by
// the instruction sender, and a human has to be able to read what was published
// on-chain and confirm it matches what they intended.
type Endpoints struct {
	Backing BackingEndpoints `json:"backing"`
	Chains  []Chain          `json:"chains"`
}

// BackingEndpoints configures the two reserve sources.
type BackingEndpoints struct {
	Bonds Endpoint `json:"bonds"`
	Cash  Endpoint `json:"cash"`
}

// Endpoint is one HTTP backing source.
//
// A credential can be needed in two distinct places, so they are two distinct
// fields. Collapsing them into one "secretRef" reads simpler and is wrong: the
// Hex private endpoint authenticates with a header and has no URL placeholder,
// while a keyed RPC URL has a placeholder and no header. One field cannot mean
// both without the validation silently rejecting one of the two real cases.
type Endpoint struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	RequireCurrency bool   `json:"requireCurrency"`
	MaxAgeMs        int    `json:"maxAgeMs"`
	TimeoutMs       int    `json:"timeoutMs"`

	// AuthHeader and AuthSecretRef send a credential as a request header.
	AuthHeader    string `json:"authHeader,omitempty"`
	AuthSecretRef string `json:"authSecretRef,omitempty"`

	// URLSecretRef is substituted into {secret} in URL.
	URLSecretRef string `json:"urlSecretRef,omitempty"`
}

// Chain is one chain's supply read.
type Chain struct {
	Name           string     `json:"name"`
	Token          string     `json:"token"`
	Quorum         int        `json:"quorum"`
	FinalityTag    string     `json:"finalityTag"`
	FinalityMargin uint64     `json:"finalityMargin"`
	Providers      []Provider `json:"providers"`
}

// Provider is one RPC endpoint.
type Provider struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	SecretRef string `json:"secretRef,omitempty"`
}

// Pinned is the part of the configuration that stays in the image, because it
// must not be substitutable.
type Pinned struct {
	// AllowedHosts bounds every endpoint that can ever be configured.
	AllowedHosts []string

	ChainID  *big.Int
	Store    common.Address
	FeedID   report.FeedID
	Decimals int8

	HaircutBps uint16
	Deadline   time.Duration
	Window     time.Duration
}

// Store holds runtime configuration and derives an oracle.Config from it.
//
// Safe for concurrent use: the extension serialises handlers, but GET /state
// reads concurrently.
type Store struct {
	mu sync.RWMutex

	pinned Pinned

	endpoints *Endpoints
	secrets   map[string]string

	// snapshot is the assembled config, rebuilt whenever inputs change. nil
	// means not ready.
	snapshot *oracle.Config
}

// New returns an unconfigured Store.
func New(pinned Pinned) *Store {
	return &Store{pinned: pinned, secrets: map[string]string{}}
}

// Readiness describes what the extension is still waiting for.
//
// Reported in GET /state so an operator can tell "never provisioned" from
// "provisioned but a source is down" without shell access.
type Readiness struct {
	EndpointsSet bool     `json:"endpointsSet"`
	SecretsSet   bool     `json:"secretsSet"`
	Ready        bool     `json:"ready"`
	MissingRefs  []string `json:"missingSecretRefs"`
}

// Readiness returns the current provisioning state.
func (s *Store) Readiness() Readiness {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r := Readiness{
		EndpointsSet: s.endpoints != nil,
		SecretsSet:   len(s.secrets) > 0,
		Ready:        s.snapshot != nil,
		MissingRefs:  []string{},
	}
	if s.endpoints != nil {
		for _, ref := range requiredRefs(*s.endpoints) {
			if s.secrets[ref] == "" {
				r.MissingRefs = append(r.MissingRefs, ref)
			}
		}
	}
	return r
}

// Config returns the assembled configuration, or false when not provisioned.
func (s *Store) Config() (oracle.Config, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snapshot == nil {
		return oracle.Config{}, false
	}
	return *s.snapshot, true
}

// SetEndpoints installs a new endpoint configuration.
//
// The whole configuration is replaced atomically. A partial update would let an
// attacker who could land one message leave the extension in a state that is
// neither the old configuration nor the intended new one.
func (s *Store) SetEndpoints(e Endpoints) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateEndpoints(e); err != nil {
		return err
	}

	s.endpoints = &e
	s.rebuild()
	return nil
}

// SetSecret installs one credential.
func (s *Store) SetSecret(ref, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ref == "" || value == "" {
		return errors.New("runtimecfg: ref and value are required")
	}
	// A credential containing a URL separator could restructure an endpoint
	// after substitution.
	if strings.ContainsAny(value, " \t\r\n/?#") {
		return errors.New("runtimecfg: secret contains characters that would alter a url")
	}

	s.secrets[ref] = value
	s.rebuild()
	return nil
}

// rebuild reassembles the snapshot. Callers hold the write lock.
//
// A failure leaves the previous snapshot in place: a bad update must not take a
// working feed down.
func (s *Store) rebuild() {
	if s.endpoints == nil {
		return
	}
	cfg, err := s.assemble(*s.endpoints)
	if err != nil {
		return
	}
	s.snapshot = &cfg
}

func (s *Store) assemble(e Endpoints) (oracle.Config, error) {
	bonds, err := s.buildEndpoint(e.Backing.Bonds)
	if err != nil {
		return oracle.Config{}, err
	}
	cash, err := s.buildEndpoint(e.Backing.Cash)
	if err != nil {
		return oracle.Config{}, err
	}

	chains := make([]supply.ChainConfig, 0, len(e.Chains))
	for _, c := range e.Chains {
		ch, err := s.buildChain(c)
		if err != nil {
			return oracle.Config{}, err
		}
		chains = append(chains, ch)
	}

	cfg := oracle.Config{
		Chains:     chains,
		Bonds:      bonds,
		Cash:       cash,
		HaircutBps: s.pinned.HaircutBps,
		Decimals:   s.pinned.Decimals,
		ChainID:    s.pinned.ChainID,
		Store:      s.pinned.Store,
		FeedID:     s.pinned.FeedID,
		Deadline:   s.pinned.Deadline,
		Window:     s.pinned.Window,
	}
	return cfg, cfg.Validate()
}

func (s *Store) buildEndpoint(e Endpoint) (backing.Source, error) {
	resolved, err := s.resolveURL(e.URL, e.URLSecretRef)
	if err != nil {
		return nil, err
	}
	cfg := backing.Config{
		Name:            e.Name,
		URL:             resolved,
		RequireCurrency: e.RequireCurrency,
		MaxAge:          time.Duration(e.MaxAgeMs) * time.Millisecond,
		Timeout:         time.Duration(e.TimeoutMs) * time.Millisecond,
	}
	if e.AuthHeader != "" {
		value := s.secrets[e.AuthSecretRef]
		if value == "" {
			return nil, fmt.Errorf("runtimecfg: secret %q is not set", e.AuthSecretRef)
		}
		cfg.AuthHeader = e.AuthHeader
		cfg.AuthValue = value
	}
	return backing.New(cfg)
}

func (s *Store) buildChain(c Chain) (supply.ChainConfig, error) {
	var out supply.ChainConfig

	if !common.IsHexAddress(c.Token) {
		return out, fmt.Errorf("runtimecfg: chain %q: token is not an address", c.Name)
	}

	var tag supply.BlockTag
	switch c.FinalityTag {
	case string(supply.TagLatest):
		tag = supply.TagLatest
	case string(supply.TagFinalized):
		tag = supply.TagFinalized
	default:
		return out, fmt.Errorf("runtimecfg: chain %q: bad finalityTag", c.Name)
	}

	providers := make([]supply.Provider, 0, len(c.Providers))
	for _, p := range c.Providers {
		resolved, err := s.resolveURL(p.URL, p.SecretRef)
		if err != nil {
			return out, err
		}
		client, err := ethclient.Dial(resolved)
		if err != nil {
			return out, fmt.Errorf("runtimecfg: chain %q provider %q: cannot dial", c.Name, p.Name)
		}
		providers = append(providers, supply.Provider{Name: p.Name, Client: client})
	}

	out = supply.ChainConfig{
		Name: c.Name, Token: common.HexToAddress(c.Token), Providers: providers,
		Quorum: c.Quorum, FinalityTag: tag, FinalityMargin: c.FinalityMargin,
	}
	return out, out.Validate()
}

// resolveURL substitutes a credential and enforces the allowlist.
func (s *Store) resolveURL(raw, secretRef string) (string, error) {
	value := ""
	if secretRef != "" {
		value = s.secrets[secretRef]
		if value == "" {
			return "", fmt.Errorf("runtimecfg: secret %q is not set", secretRef)
		}
	}
	return resolveAgainst(s.pinned.AllowedHosts, raw, secretRef, value)
}

// resolveAgainst validates a URL against an allowlist and substitutes a secret.
// Split out from Store so it is testable without one.
func resolveAgainst(allowed []string, raw, secretRef, value string) (string, error) {
	hasPlaceholder := strings.Contains(raw, secretPlaceholder)
	if hasPlaceholder != (secretRef != "") {
		return "", errors.New("runtimecfg: {secret} and secretRef must be used together")
	}

	const sentinel = "zzsecretsentinelzz"
	probe := strings.ReplaceAll(raw, secretPlaceholder, sentinel)

	u, err := url.Parse(probe)
	if err != nil {
		return "", errors.New("runtimecfg: url is not a valid URL")
	}
	if u.Scheme != "https" {
		return "", errors.New("runtimecfg: url must use https")
	}
	if strings.Contains(u.Host, sentinel) || strings.Contains(u.Scheme, sentinel) {
		return "", errors.New("runtimecfg: {secret} may not appear in the scheme or host")
	}

	// The allowlist check. Compared against the parsed host, never against the
	// raw string: a substring match on the raw URL would accept
	// https://evil.com/?x=allowed.example.com.
	if !hostAllowed(allowed, u.Hostname()) {
		return "", fmt.Errorf("runtimecfg: host %q is not in the baked allowlist", u.Hostname())
	}

	if !hasPlaceholder {
		return raw, nil
	}
	if strings.ContainsAny(value, " \t\r\n/?#") {
		return "", errors.New("runtimecfg: secret would alter the url")
	}
	return strings.ReplaceAll(raw, secretPlaceholder, value), nil
}

// hostAllowed matches a hostname exactly, case-insensitively.
//
// Exact match, not suffix: a suffix rule like ".example.com" would also accept
// "evil-example.com" under a careless implementation, and there is no need for
// wildcards when the provider set is known and pinned.
func hostAllowed(allowed []string, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(a), host) {
			return true
		}
	}
	return false
}

// validateEndpoints checks a configuration before it replaces the current one.
// Callers hold the write lock.
func (s *Store) validateEndpoints(e Endpoints) error {
	if len(e.Chains) == 0 {
		return errors.New("runtimecfg: at least one chain is required")
	}
	if e.Backing.Bonds.Name == "" || e.Backing.Cash.Name == "" {
		return errors.New("runtimecfg: both backing sources need names")
	}
	if e.Backing.Bonds.Name == e.Backing.Cash.Name {
		return errors.New("runtimecfg: backing sources must have distinct names")
	}

	// Every URL must clear the allowlist even when its secret is missing, so a
	// disallowed host is rejected at configuration time rather than lying
	// dormant until a credential arrives.
	if (e.Backing.Bonds.AuthHeader == "") != (e.Backing.Bonds.AuthSecretRef == "") ||
		(e.Backing.Cash.AuthHeader == "") != (e.Backing.Cash.AuthSecretRef == "") {
		return errors.New("runtimecfg: authHeader and authSecretRef must be set together")
	}

	urls := []struct{ raw, ref string }{
		{e.Backing.Bonds.URL, e.Backing.Bonds.URLSecretRef},
		{e.Backing.Cash.URL, e.Backing.Cash.URLSecretRef},
	}
	seenChain := map[string]bool{}
	for _, c := range e.Chains {
		if seenChain[c.Name] {
			return fmt.Errorf("runtimecfg: duplicate chain %q", c.Name)
		}
		seenChain[c.Name] = true

		seenProvider := map[string]bool{}
		for _, p := range c.Providers {
			if p.Name == "" || strings.Contains(p.Name, "://") || strings.Contains(p.Name, ".") {
				// Names reach GET /state, which is public.
				return fmt.Errorf("runtimecfg: provider name %q must be an identifier, not a URL", p.Name)
			}
			if seenProvider[p.Name] {
				return fmt.Errorf("runtimecfg: chain %q: duplicate provider %q", c.Name, p.Name)
			}
			seenProvider[p.Name] = true
			urls = append(urls, struct{ raw, ref string }{p.URL, p.SecretRef})
		}
	}

	for _, u := range urls {
		// A dummy value: this checks structure and the allowlist, not whether
		// the credential has arrived yet.
		if _, err := resolveAgainst(s.pinned.AllowedHosts, u.raw, u.ref, "placeholder"); err != nil {
			return err
		}
	}
	return nil
}

// requiredRefs lists every secret the configuration needs.
func requiredRefs(e Endpoints) []string {
	var refs []string
	add := func(r string) {
		if r == "" {
			return
		}
		for _, existing := range refs {
			if existing == r {
				return
			}
		}
		refs = append(refs, r)
	}

	add(e.Backing.Bonds.AuthSecretRef)
	add(e.Backing.Bonds.URLSecretRef)
	add(e.Backing.Cash.AuthSecretRef)
	add(e.Backing.Cash.URLSecretRef)
	for _, c := range e.Chains {
		for _, p := range c.Providers {
			add(p.SecretRef)
		}
	}
	return refs
}
