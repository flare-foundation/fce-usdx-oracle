package config

import (
	"strings"
	"testing"
)

// tee-node decodes EXTENSION_ID as a 32-byte hex word, while operators, the
// compose file and extension.env all write the decimal id. Every spelling of
// the pinned id must normalise to the same word, and any other id must be
// refused before tee-node is started for it.
func TestExtensionIDEnv(t *testing.T) {
	want := "0x0000000000000000000000000000000000000000000000000000000000000001"
	for _, raw := range []string{"", "1", " 1 ", "0x1", "0x01", want} {
		got, err := ExtensionIDEnv(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q → %s, want %s", raw, got, want)
		}
	}
	for _, raw := range []string{"2", "0x02", "one", "-1", "0xzz"} {
		if _, err := ExtensionIDEnv(raw); err == nil {
			t.Errorf("%q was accepted; it is not the pinned id %s", raw, ExtensionID)
		} else if !strings.Contains(err.Error(), "EXTENSION_ID") {
			t.Errorf("%q: error does not name the variable: %v", raw, err)
		}
	}
}
