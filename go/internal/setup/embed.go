package setup

import (
	"embed"
	"fmt"
)

// Configuration is compiled into the binary, not mounted, so it is covered by
// the registered code hash. See the package doc for why that is load-bearing.
//
//go:embed config/*.json
var configFS embed.FS

// embeddedConfig returns the raw config for a network.
func embeddedConfig(name string) ([]byte, error) {
	raw, err := configFS.ReadFile("config/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf(
			"setup: no embedded config for network %q; build with "+
				`-ldflags "-X fce-usdx-oracle/internal/setup.network=<name>"`, name)
	}
	return raw, nil
}

// Networks lists the embedded configurations. Used by tests to assert every
// shipped config is valid, so a broken mainnet config cannot pass CI on the
// strength of a working Coston2 one.
func Networks() ([]string, error) {
	entries, err := configFS.ReadDir("config")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if len(n) > 5 && n[len(n)-5:] == ".json" {
			names = append(names, n[:len(n)-5])
		}
	}
	return names, nil
}
