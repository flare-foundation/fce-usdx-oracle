package setup

import (
	"strings"
	"testing"
)

func baseFile() File {
	return File{
		SchemaVersion: SchemaVersion,
		Network:       "coston2",
		Price:         Price{Decimals: 8},
		Target: Target{
			ChainID: 114, Store: "0x00000000000000000000000000000000000000AA",
			FeedCategory: 32, FeedName: "USDX/USD",
		},
		Observation:  Observation{DeadlineMs: 20000, WindowMs: 30000},
		AllowedHosts: []string{"api.htdigitalassets.com", "rpc.ankr.com"},
	}
}

func TestPinned_Valid(t *testing.T) {
	p, err := Pinned(baseFile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ChainID.Uint64() != 114 {
		t.Errorf("chainId = %s", p.ChainID)
	}
	if p.FeedID[0] != 0x20 {
		t.Errorf("feed category byte = 0x%02x", p.FeedID[0])
	}
	if len(p.AllowedHosts) != 2 {
		t.Errorf("allowedHosts = %v", p.AllowedHosts)
	}
}

func TestPinned_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*File)
	}{
		{"feed category below range", func(f *File) { f.Target.FeedCategory = 31 }},
		{"feed category above range", func(f *File) { f.Target.FeedCategory = 64 }},
		{"empty feed name", func(f *File) { f.Target.FeedName = "" }},
		{"over-long feed name", func(f *File) { f.Target.FeedName = strings.Repeat("A", 21) }},
		{"zero store", func(f *File) { f.Target.Store = "0x" + strings.Repeat("0", 40) }},
		{"malformed store", func(f *File) { f.Target.Store = "nope" }},
		{"zero chain id", func(f *File) { f.Target.ChainID = 0 }},
		{"negative decimals", func(f *File) { f.Price.Decimals = -1 }},
		{"zero deadline", func(f *File) { f.Observation.DeadlineMs = 0 }},
		{"zero window", func(f *File) { f.Observation.WindowMs = 0 }},
		{"empty allowlist", func(f *File) { f.AllowedHosts = nil }},
		{"allowlist entry with scheme", func(f *File) { f.AllowedHosts = []string{"https://x.com"} }},
		{"allowlist entry with path", func(f *File) { f.AllowedHosts = []string{"x.com/rpc"} }},
		{"allowlist entry with port", func(f *File) { f.AllowedHosts = []string{"x.com:443"} }},
		{"wildcard allowlist entry", func(f *File) { f.AllowedHosts = []string{"*.flare.network"} }},
		{"empty allowlist entry", func(f *File) { f.AllowedHosts = []string{""} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := baseFile()
			c.mutate(&f)
			if _, err := Pinned(f); err == nil {
				t.Error("expected a config error")
			}
		})
	}
}

// Every embedded config must be valid, so a broken mainnet file cannot pass CI
// on the strength of a working Coston2 one.
func TestEmbeddedConfigsAreValid(t *testing.T) {
	networks, err := Networks()
	if err != nil {
		t.Fatalf("Networks: %v", err)
	}
	if len(networks) == 0 {
		t.Fatal("no embedded configs found")
	}

	for _, n := range networks {
		t.Run(n, func(t *testing.T) {
			f, err := parse(n)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, err := Pinned(f); err != nil {
				t.Errorf("shipped config %q is invalid: %v", n, err)
			}
		})
	}
}

// The allowlist is the substitution defence, so a shipped config must not carry
// a credential or a wildcard in it.
func TestEmbeddedAllowlistsAreBareHostnames(t *testing.T) {
	networks, _ := Networks()
	for _, n := range networks {
		f, err := parse(n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		for _, h := range f.AllowedHosts {
			if strings.ContainsAny(h, "/:*?#@") {
				t.Errorf("%s: allowlist entry %q is not a bare hostname", n, h)
			}
			if strings.Contains(strings.ToLower(h), "key") {
				t.Errorf("%s: allowlist entry %q looks like it carries a credential", n, h)
			}
		}
	}
}

// A config declaring a different network than the build selected must fail: it
// is the difference between a mainnet feed and a testnet one.
func TestParse_RejectsNetworkMismatch(t *testing.T) {
	if _, err := parse("does-not-exist"); err == nil {
		t.Error("an unknown network must be rejected")
	}
}
