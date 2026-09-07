package poller

import (
	"os"
	"testing"
)

// The extension refuses to run without the chain and extension tee-node is
// configured for; the tests run with the simulation's values unless the
// environment names others.
func TestMain(m *testing.M) {
	if os.Getenv("CHAIN_ID") == "" {
		os.Setenv("CHAIN_ID", "31337")
	}
	if os.Getenv("EXTENSION_ID") == "" {
		os.Setenv("EXTENSION_ID", "1")
	}
	os.Exit(m.Run())
}
