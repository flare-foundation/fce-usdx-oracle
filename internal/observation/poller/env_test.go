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
		if err := os.Setenv("CHAIN_ID", "31337"); err != nil {
			panic(err)
		}
	}
	if os.Getenv("EXTENSION_ID") == "" {
		if err := os.Setenv("EXTENSION_ID", "1"); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
