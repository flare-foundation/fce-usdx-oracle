package support

import (
	"os"
	"testing"

	"fce-usdx-oracle/tools/pkg/configs"
)

// The dev key has funds on a local devnet and nowhere else. Falling back to it
// on a live chain produces a run of failures — insufficient funds, OnlyOwner,
// nothing at the expected address — none of which name the missing variable, so
// the fallback is refused there instead.
func TestDefaultPrivateKeyRefusesTheDevKeyOnALiveChain(t *testing.T) {
	// Set rather than unset: godotenv does not override a variable that is
	// already present, so this also keeps a developer's .env out of the test.
	t.Setenv("EXTENSION_OWNER_KEY", "")
	t.Setenv("DEPLOYMENT_PRIVATE_KEY", "")

	chain := configs.Chain
	t.Cleanup(func() { configs.Chain = chain })

	configs.Chain = "coston"
	if _, err := DefaultPrivateKey(); err == nil {
		t.Error("the dev key was accepted on a live chain")
	}

	// The simulation still runs unattended on it, which is the whole point of
	// having a key with a known balance there.
	configs.Chain = "local"
	key, err := DefaultPrivateKey()
	if err != nil {
		t.Fatalf("the dev key must still serve a local devnet: %v", err)
	}
	if key != configs.PrvWithFunds {
		t.Error("a local devnet got some other key than the dev key")
	}
}

// A key in the environment is used whatever the chain, and the 0x prefix is
// optional because both forms are what people paste.
func TestDefaultPrivateKeyReadsTheEnvironment(t *testing.T) {
	const raw = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"

	chain := configs.Chain
	t.Cleanup(func() { configs.Chain = chain })
	configs.Chain = "coston"

	for _, value := range []string{raw, "0x" + raw} {
		t.Setenv("EXTENSION_OWNER_KEY", value)
		key, err := DefaultPrivateKey()
		if err != nil {
			t.Fatalf("EXTENSION_OWNER_KEY=%s: %v", value, err)
		}
		if got := os.Getenv("EXTENSION_OWNER_KEY"); got != value {
			t.Fatalf("the test did not set the variable it meant to (%s)", got)
		}
		if key == configs.PrvWithFunds {
			t.Error("the configured key was ignored in favour of the dev key")
		}
	}
}
