package configs

import (
	"crypto/ecdsa"
	"encoding/json"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// Defaults honour the same env vars the scripts use (full-setup.sh, test.sh),
// so the tools run flag-free from a sourced .env. Fallbacks point at the
// coston2 deployment: the old docker/sim_dump path no longer exists.
var (
	// The port Docker Compose publishes: the container listens on 6664 and
	// compose maps 6674 -> 6664, so these are one proxy seen from two sides.
	// Under `start-services.sh --local` the proxy binds 6664 on the host, so a
	// hand-run tool there needs EXT_PROXY_URL=http://localhost:6664 or -p.
	ExtensionProxyURL = envOrDefault("EXT_PROXY_URL", "http://localhost:6674")
	ChainNodeURL      = envOrDefault("CHAIN_URL", "https://coston2-api.flare.network/ext/C/rpc")
	AddressesFile     = envOrDefault("ADDRESSES_FILE", "../config/coston2/deployed-addresses.json")
)

const (
	ExtensionProxyConfigFile = "./configs/proxy/extension_proxy.toml"
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

const (
	ExtConfigurationPort = 5501 // port on tee for setting the configurations (proxyURL, initialOwner, extensionID)
	ExtProxyInternalPort = 6663 // internal port for tee to get actions from the queue from the proxy
	ExtensionServerPort  = 7701 // port on the tee that the extension server calls for signing, encrypting, etc.
	ExtensionPort        = 7702 // the port on the extension server that the tee calls to send non system actions
)

var PrvWithFunds *ecdsa.PrivateKey

func init() {
	var err error
	PrvWithFunds, err = crypto.HexToECDSA("804b01a8c27a65cc694a867be76edae3ccce7a7161cda1f67a8349df696d2207")
	if err != nil {
		panic("cannot read privateKey with funds")
	}
}

func ReadAddresses[T any](filePath string, dest *T) error {
	file, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	err = json.Unmarshal(file, dest)

	return err
}

// contractEntry mirrors one entry in the deployed-addresses file. Declared here
// rather than reusing support.RawContract, which would be an import cycle.
type contractEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// ResolveContract prefers an explicit address, otherwise looks the contract up
// by name in the deployed-addresses file.
func ResolveContract(explicit, addressesFile, name string) (common.Address, error) {
	if explicit != "" {
		if !common.IsHexAddress(explicit) {
			return common.Address{}, errors.Errorf("%q is not an address", explicit)
		}
		return common.HexToAddress(explicit), nil
	}

	raw, err := os.ReadFile(addressesFile)
	if err != nil {
		return common.Address{}, errors.Errorf("reading %s: %s", addressesFile, err)
	}
	var entries []contractEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return common.Address{}, errors.Errorf("parsing %s: %s", addressesFile, err)
	}
	for _, e := range entries {
		if e.Name == name && common.IsHexAddress(e.Address) {
			return common.HexToAddress(e.Address), nil
		}
	}
	return common.Address{}, errors.Errorf(
		"no %s entry in %s — pass the address explicitly, or run extension-setup.sh to deploy it",
		name, addressesFile)
}
