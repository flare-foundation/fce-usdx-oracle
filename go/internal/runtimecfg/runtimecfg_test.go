package runtimecfg

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"fce-usdx-oracle/internal/report"

	"github.com/ethereum/go-ethereum/common"
)

func testPinned(t *testing.T) Pinned {
	t.Helper()
	feedID, err := report.ParseFeedID(0x20, "USDX/USD")
	if err != nil {
		t.Fatalf("ParseFeedID: %v", err)
	}
	return Pinned{
		AllowedHosts: []string{
			"flare-api.flare.network",
			"rpc.ankr.com",
			"api.htdigitalassets.com",
		},
		ChainID:  big.NewInt(114),
		Store:    common.HexToAddress("0x00000000000000000000000000000000000000AA"),
		FeedID:   feedID,
		Decimals: 8,
		Deadline: 20 * time.Second,
		Window:   30 * time.Second,
	}
}

func testEndpoints() Endpoints {
	return Endpoints{
		Backing: BackingEndpoints{
			Bonds: Endpoint{
				Name: "hex_bonds", URL: "https://api.htdigitalassets.com/reserves",
				MaxAgeMs: 1800000, TimeoutMs: 5000,
			},
			Cash: Endpoint{
				Name: "hex_cash", URL: "https://api.htdigitalassets.com/cash",
				RequireCurrency: true, MaxAgeMs: 1800000, TimeoutMs: 5000,
				AuthHeader: "X-API-Key", AuthSecretRef: "hex_api_key",
			},
		},
		Chains: []Chain{{
			Name: "flare", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B",
			Quorum: 1, FinalityTag: "latest", FinalityMargin: 54,
			Providers: []Provider{
				{Name: "rpc_flare_1", URL: "https://flare-api.flare.network/ext/C/rpc"},
			},
		}},
	}
}

// --- the host allowlist ---

// The substitution defence. Endpoints arrive over a channel we do not fully
// control, so a host outside the attested allowlist must be impossible to
// configure — otherwise setting endpoints is setting the price.
func TestSetEndpoints_RejectsHostOutsideAllowlist(t *testing.T) {
	s := New(testPinned(t))
	e := testEndpoints()
	e.Chains[0].Providers[0].URL = "https://evil.example.com/rpc"

	if err := s.SetEndpoints(e); err == nil {
		t.Fatal("a host outside the baked allowlist must be rejected")
	}
	if _, ok := s.Config(); ok {
		t.Error("a rejected configuration must not become active")
	}
}

// The allowlist is matched against the parsed hostname, never a substring of
// the raw URL — otherwise an allowed name in a query string would pass.
func TestSetEndpoints_AllowlistMatchesParsedHostNotSubstring(t *testing.T) {
	s := New(testPinned(t))

	for _, raw := range []string{
		"https://evil.example.com/?x=flare-api.flare.network",
		"https://evil.example.com/flare-api.flare.network",
		"https://flare-api.flare.network.evil.com/rpc",
		"https://user@evil.com/rpc",
	} {
		e := testEndpoints()
		e.Chains[0].Providers[0].URL = raw
		if err := s.SetEndpoints(e); err == nil {
			t.Errorf("%q must not satisfy the allowlist", raw)
		}
	}
}

// Exact match, not suffix: "evil-flare-api.flare.network" style near-misses.
func TestHostAllowed_ExactMatchOnly(t *testing.T) {
	allowed := []string{"rpc.ankr.com"}

	for _, host := range []string{"rpc.ankr.com", "RPC.ANKR.COM"} {
		if !hostAllowed(allowed, host) {
			t.Errorf("%q should be allowed (matching is case-insensitive)", host)
		}
	}
	for _, host := range []string{
		"evil-rpc.ankr.com", "rpc.ankr.com.evil.com", "ankr.com", "", "sub.rpc.ankr.com",
	} {
		if hostAllowed(allowed, host) {
			t.Errorf("%q must not be allowed", host)
		}
	}
}

// --- the secret split ---

func TestResolveAgainst_PlaceholderRejectedInHost(t *testing.T) {
	allowed := []string{"rpc.ankr.com"}
	if _, err := resolveAgainst(allowed, "https://{secret}/eth", "k", "evil.com"); err == nil {
		t.Error("{secret} in the host would let a credential repoint the chain")
	}
}

func TestResolveAgainst_SubstitutesInPathAndQuery(t *testing.T) {
	allowed := []string{"rpc.ankr.com"}

	got, err := resolveAgainst(allowed, "https://rpc.ankr.com/eth/{secret}", "k", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://rpc.ankr.com/eth/abc123" {
		t.Errorf("got %q", got)
	}
}

func TestResolveAgainst_RejectsSecretThatWouldAlterURL(t *testing.T) {
	allowed := []string{"rpc.ankr.com"}
	for _, bad := range []string{"a/b", "a?b", "a#b", "a b"} {
		if _, err := resolveAgainst(allowed, "https://rpc.ankr.com/eth/{secret}", "k", bad); err == nil {
			t.Errorf("secret %q could restructure the url", bad)
		}
	}
}

func TestResolveAgainst_RequiresHTTPS(t *testing.T) {
	if _, err := resolveAgainst([]string{"rpc.ankr.com"}, "http://rpc.ankr.com/x", "", ""); err == nil {
		t.Error("plaintext must be rejected")
	}
}

// --- readiness ---

// Boot state: nothing configured, nothing served.
func TestStore_StartsUnconfigured(t *testing.T) {
	s := New(testPinned(t))

	r := s.Readiness()
	if r.Ready || r.EndpointsSet || r.SecretsSet {
		t.Errorf("a fresh store must be unconfigured: %+v", r)
	}
	if _, ok := s.Config(); ok {
		t.Error("Config() must report false before provisioning")
	}
}

// Endpoints alone are not enough when they reference a credential.
func TestStore_EndpointsWithoutSecretIsNotReady(t *testing.T) {
	s := New(testPinned(t))
	if err := s.SetEndpoints(testEndpoints()); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}

	r := s.Readiness()
	if !r.EndpointsSet {
		t.Error("endpointsSet should be true")
	}
	if r.Ready {
		t.Error("must not be ready while a referenced secret is missing")
	}
	if len(r.MissingRefs) != 1 || r.MissingRefs[0] != "hex_api_key" {
		t.Errorf("missing refs = %v, want [hex_api_key]", r.MissingRefs)
	}
	if _, ok := s.Config(); ok {
		t.Error("Config() must report false while unready")
	}
}

func TestStore_BecomesReadyWhenBothArrive(t *testing.T) {
	s := New(testPinned(t))
	if err := s.SetEndpoints(testEndpoints()); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	if err := s.SetSecret("hex_api_key", "s3cr3t"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	r := s.Readiness()
	if !r.Ready {
		t.Fatalf("expected ready, got %+v", r)
	}
	if len(r.MissingRefs) != 0 {
		t.Errorf("missing refs = %v", r.MissingRefs)
	}

	cfg, ok := s.Config()
	if !ok {
		t.Fatal("Config() should report true once ready")
	}
	if cfg.Store != s.pinned.Store {
		t.Error("pinned store address must survive assembly")
	}
	if cfg.ChainID.Cmp(s.pinned.ChainID) != 0 {
		t.Error("pinned chain id must survive assembly")
	}
}

// Order must not matter: fire-and-forget delivery gives no ordering guarantee,
// so a secret may well arrive before the endpoints that reference it.
func TestStore_SecretBeforeEndpoints(t *testing.T) {
	s := New(testPinned(t))
	if err := s.SetSecret("hex_api_key", "s3cr3t"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if s.Readiness().Ready {
		t.Error("a secret alone must not make the store ready")
	}
	if err := s.SetEndpoints(testEndpoints()); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	if !s.Readiness().Ready {
		t.Error("expected ready once endpoints arrive")
	}
}

// A bad update must not take a working feed down.
func TestStore_FailedUpdateKeepsPreviousSnapshot(t *testing.T) {
	s := New(testPinned(t))
	if err := s.SetEndpoints(testEndpoints()); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	if err := s.SetSecret("hex_api_key", "s3cr3t"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if !s.Readiness().Ready {
		t.Fatal("setup: expected ready")
	}

	bad := testEndpoints()
	bad.Chains[0].Providers[0].URL = "https://evil.example.com/rpc"
	if err := s.SetEndpoints(bad); err == nil {
		t.Fatal("expected the bad update to be rejected")
	}

	if !s.Readiness().Ready {
		t.Error("a rejected update must leave the working configuration in place")
	}
}

// --- validation ---

func TestSetEndpoints_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Endpoints)
	}{
		{"no chains", func(e *Endpoints) { e.Chains = nil }},
		{"backing names collide", func(e *Endpoints) { e.Backing.Cash.Name = e.Backing.Bonds.Name }},
		{"missing backing name", func(e *Endpoints) { e.Backing.Bonds.Name = "" }},
		{"url-shaped provider name", func(e *Endpoints) {
			e.Chains[0].Providers[0].Name = "https://rpc.ankr.com"
		}},
		{"duplicate provider", func(e *Endpoints) {
			e.Chains[0].Providers = append(e.Chains[0].Providers, e.Chains[0].Providers[0])
		}},
		{"duplicate chain", func(e *Endpoints) { e.Chains = append(e.Chains, e.Chains[0]) }},
		{"plaintext provider url", func(e *Endpoints) {
			e.Chains[0].Providers[0].URL = "http://flare-api.flare.network/rpc"
		}},
		{"placeholder without ref", func(e *Endpoints) {
			e.Chains[0].Providers[0].URL = "https://rpc.ankr.com/flare/{secret}"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(testPinned(t))
			e := testEndpoints()
			c.mutate(&e)
			if err := s.SetEndpoints(e); err == nil {
				t.Error("expected rejection")
			}
		})
	}
}

func TestSetSecret_Rejects(t *testing.T) {
	s := New(testPinned(t))
	for _, c := range []struct{ ref, value string }{
		{"", "v"}, {"k", ""}, {"k", "has/slash"}, {"k", "has space"},
	} {
		if err := s.SetSecret(c.ref, c.value); err == nil {
			t.Errorf("SetSecret(%q, %q) should be rejected", c.ref, c.value)
		}
	}
}

// Rotation must not need a restart, and must take effect immediately.
func TestStore_SecretRotationTakesEffect(t *testing.T) {
	pinned := testPinned(t)
	s := New(pinned)

	e := testEndpoints()
	e.Chains[0].Providers[0].URL = "https://rpc.ankr.com/flare/{secret}"
	e.Chains[0].Providers[0].SecretRef = "ankr_key"

	if err := s.SetEndpoints(e); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	if err := s.SetSecret("hex_api_key", "a"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if err := s.SetSecret("ankr_key", "old"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if !s.Readiness().Ready {
		t.Fatal("expected ready")
	}

	if err := s.SetSecret("ankr_key", "new"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !s.Readiness().Ready {
		t.Error("the store must stay ready across a rotation")
	}
}

// Readiness is what tells an operator "never provisioned" from "provisioned but
// broken", so it must be reportable without exposing a credential.
func TestReadiness_ExposesNoSecrets(t *testing.T) {
	s := New(testPinned(t))
	_ = s.SetEndpoints(testEndpoints())
	_ = s.SetSecret("hex_api_key", "SUPERSECRETVALUE")

	r := s.Readiness()
	for _, ref := range r.MissingRefs {
		if strings.Contains(ref, "SUPERSECRETVALUE") {
			t.Error("readiness leaks a secret value")
		}
	}
}
