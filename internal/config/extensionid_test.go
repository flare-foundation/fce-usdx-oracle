package config

import (
	"math/big"
	"os"
	"testing"
)

// The package's tests run with the two ids set, as every deployment does; the
// getters are exercised through the same variables.
func TestMain(m *testing.M) {
	if os.Getenv("CHAIN_ID") == "" {
		os.Setenv("CHAIN_ID", "31337")
	}
	if os.Getenv("EXTENSION_ID") == "" {
		os.Setenv("EXTENSION_ID", "1")
	}
	os.Exit(m.Run())
}

// Both ids are read from the environment tee-node reads, decimal or 0x-hex, and
// an empty value is refused rather than defaulted.
func TestIDsAreMandatory(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want int64
	}{{"7", 7}, {"0x0a", 10}, {" 1 ", 1}} {
		got, err := extensionIDFrom(tc.env)
		if err != nil || got.Cmp(big.NewInt(tc.want)) != 0 {
			t.Errorf("extensionIDFrom(%q) = %v, %v; want %d", tc.env, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "  ", "x", "-1", "0xzz"} {
		if _, err := extensionIDFrom(bad); err == nil {
			t.Errorf("extensionIDFrom(%q) accepted", bad)
		}
	}
	for _, tc := range []struct {
		env  string
		want uint64
	}{{"31337", 31337}, {"0x7a69", 31337}, {"114", 114}} {
		got, err := chainIDFrom(tc.env)
		if err != nil || got != tc.want {
			t.Errorf("chainIDFrom(%q) = %d, %v; want %d", tc.env, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "0", "abc", "0x"} {
		if _, err := chainIDFrom(bad); err == nil {
			t.Errorf("chainIDFrom(%q) accepted", bad)
		}
	}
}

// The word handed to tee-node is the configured id in every spelling, and an id
// that is not the configured one is refused.
func TestExtensionIDEnv(t *testing.T) {
	id := ExtensionID()
	want := "0x" + leftPad(id.Text(16))
	for _, raw := range []string{"", id.String(), " " + id.String() + " ", "0x" + id.Text(16), want} {
		got, err := ExtensionIDEnv(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got != want {
			t.Errorf("%q → %s, want %s", raw, got, want)
		}
	}
	other := new(big.Int).Add(id, big.NewInt(1)).String()
	for _, raw := range []string{other, "0xzz", "-1"} {
		if _, err := ExtensionIDEnv(raw); err == nil {
			t.Errorf("%q was accepted; the process is configured for %s", raw, id)
		}
	}
}

func leftPad(hex string) string {
	for len(hex) < 64 {
		hex = "0" + hex
	}
	return hex
}
