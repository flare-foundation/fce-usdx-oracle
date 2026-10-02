package configs

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate clears every variable resolveChain reads, so a developer's exported
// .env cannot decide the case.
func isolate(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FLARE_CHAIN", "CHAIN_ID", "LOCAL_MODE"} {
		t.Setenv(key, "")
	}
}

// coston_local is Coston with a deployment of our own. A tool run without -c or
// -a under FLARE_CHAIN=coston_local must read Coston and that deployment's
// addresses; falling through to the CHAIN_ID or the coston2 default would send
// it to a different chain or a different deployment.
func TestCostonLocalResolvesToItsOwnRow(t *testing.T) {
	isolate(t)
	t.Setenv("FLARE_CHAIN", "coston_local")
	t.Setenv("CHAIN_ID", "16")

	chain := resolveChain()
	if chain != "coston_local" {
		t.Fatalf("FLARE_CHAIN=coston_local resolved to %q", chain)
	}
	if got, want := chainRPC[chain], chainRPC["coston"]; got != want {
		t.Errorf("coston_local RPC = %q, want Coston's %q", got, want)
	}

	path := defaultAddressesFile(chain)
	if want := filepath.Join("deploy", "config", "coston_local", "deployed-addresses.json"); path != want {
		t.Errorf("default addresses file = %q, want %q", path, want)
	}
	if _, err := os.Stat(rootRelative(projectRoot(), path)); err != nil {
		t.Errorf("the default addresses file for coston_local does not resolve to a file: %v", err)
	}
}

// Coston's two rows share an id, and the id alone names the official one.
func TestChainID16AloneResolvesToCoston(t *testing.T) {
	isolate(t)
	t.Setenv("CHAIN_ID", "16")
	if chain := resolveChain(); chain != "coston" {
		t.Fatalf("CHAIN_ID=16 resolved to %q, want coston", chain)
	}
}

// Every row of deploy/scripts/lib/chains.sh is known here, so no FLARE_CHAIN a
// script accepts is silently replaced by another chain.
func TestEveryScriptChainIsKnown(t *testing.T) {
	for _, chain := range []string{"local", "coston", "coston_local", "coston2", "songbird", "flare"} {
		isolate(t)
		t.Setenv("FLARE_CHAIN", chain)
		if got := resolveChain(); got != chain {
			t.Errorf("FLARE_CHAIN=%s resolved to %q", chain, got)
		}
	}
}

// An unknown FLARE_CHAIN falls through to CHAIN_ID rather than naming a chain
// with no RPC.
func TestUnknownFlareChainFallsThroughToTheChainID(t *testing.T) {
	isolate(t)
	t.Setenv("FLARE_CHAIN", "costonn")
	t.Setenv("CHAIN_ID", "114")
	if chain := resolveChain(); chain != "coston2" {
		t.Fatalf("resolved to %q, want coston2 from CHAIN_ID", chain)
	}
}
