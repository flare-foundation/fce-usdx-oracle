package configs

import (
	"crypto/ecdsa"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/joho/godotenv"
	"github.com/pkg/errors"
)

// Defaults honour the same env vars the scripts use (full-setup.sh, post-build.sh),
// so the tools run flag-free from the project's .env and the recorded stack: both
// are loaded here, before the defaults are taken, from the module root. Fallbacks
// point at the coston2 deployment.
var (
	// The port Docker Compose publishes: the container listens on 6664 and compose
	// maps 6674 -> 6664. A host-process stack binds 6684 directly; on the
	// simulation start-services.sh records whichever is running.
	ExtensionProxyURL string
	ChainNodeURL      string
	AddressesFile     string
)

func init() {
	root := projectRoot()
	_ = godotenv.Load(filepath.Join(root, ".env"))
	// The stack start-services.sh brought up last on the simulation names its
	// proxy in deploy/config/stack.env. It is authoritative, above .env and above
	// an EXT_PROXY_URL inherited from the shell; -p is the explicit override.
	_ = godotenv.Overload(filepath.Join(root, "deploy", "config", "stack.env"))
	ExtensionProxyURL = envOrDefault("EXT_PROXY_URL", "http://localhost:6674")
	ChainNodeURL = envOrDefault("CHAIN_URL", "https://coston2-api.flare.network/ext/C/rpc")
	AddressesFile = rootRelative(root, envOrDefault("ADDRESSES_FILE", "deploy/config/coston2/deployed-addresses.json"))
}

// projectRoot is the repository root: this file sits in tools/pkg/configs.
func projectRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// rootRelative resolves a relative path against the repository root, which is
// how .env spells paths, unless it exists from the working directory.
func rootRelative(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return filepath.Join(root, path)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

const (
	// The host-process port block, as cmd/start-tee binds it; clear of the e2e
	// simulation's own processes (5500–5503, 6661–6668, 7701, 7702).
	ExtConfigurationPort = 5521 // port on tee for setting the configurations (proxyURL, initialOwner, extensionID)
	ExtProxyInternalPort = 6683 // internal port for tee to get actions from the queue from the proxy
	ExtProxyExternalPort = 6684 // the proxy's public port: /info, /action, /direct
	ExtensionServerPort  = 7721 // port on the tee that the extension server calls for signing, encrypting, etc.
	ExtensionPort        = 7722 // the port on the extension server that the tee calls to send non system actions
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
		"no %s entry in %s — pass the address explicitly with the -a/-store flag",
		name, addressesFile)
}
