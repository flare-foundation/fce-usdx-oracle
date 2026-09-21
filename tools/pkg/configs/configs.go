package configs

import (
	"crypto/ecdsa"
	"encoding/json"
	"fce-usdx-oracle/pkg/protocol"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/joho/godotenv"
	"github.com/pkg/errors"
)

// Defaults honour the same env vars the scripts use (full-setup.sh, post-build.sh),
// so the tools run flag-free from the project's .env and the recorded stack: both
// are loaded here, before the defaults are taken, from the module root. What .env
// leaves unset falls back to the chain it names (FLARE_CHAIN, else CHAIN_ID), the
// same rows as deploy/scripts/lib/chains.sh.
var (
	// Chain is the network .env targets: local, coston, coston2, songbird or flare.
	Chain string
	// The port Docker Compose publishes: the container listens on 6664 and compose
	// maps 6674 -> 6664. A host-process stack binds 6684 directly; on the
	// simulation start-services.sh records whichever is running.
	ExtensionProxyURL string
	ChainNodeURL      string
	AddressesFile     string
	// ExtensionID is EXTENSION_ID from .env: the reserved 1 upstream's
	// configuration uses, or the public id deploy-oracle.sh recorded when the
	// registry minted one. The default the tools' -extensionId flags take.
	ExtensionID string
)

func init() {
	root := projectRoot()
	_ = godotenv.Load(filepath.Join(root, ".env"))
	// The stack start-services.sh brought up last on the simulation names its
	// proxy in deploy/config/stack.env. It is authoritative, above .env and above
	// an EXT_PROXY_URL inherited from the shell; -p is the explicit override.
	_ = godotenv.Overload(filepath.Join(root, "deploy", "config", "stack.env"))
	Chain = resolveChain()
	ExtensionProxyURL = envOrDefault("EXT_PROXY_URL", "http://localhost:6674")
	ChainNodeURL = envOrDefault("CHAIN_URL", chainRPC[Chain])
	AddressesFile = rootRelative(root, envOrDefault("ADDRESSES_FILE", "deploy/config/"+Chain+"/deployed-addresses.json"))
	// No default: the extension refuses to guess its id, and so does the tooling.
	ExtensionID = os.Getenv("EXTENSION_ID")
}

// The chains the scripts know (deploy/scripts/lib/chains.sh), as far as the tools
// need them: the public RPC and the chain id that names each.
var (
	chainRPC = map[string]string{
		"local":    "http://127.0.0.1:8545",
		"coston":   "https://coston-api.flare.network/ext/C/rpc",
		"coston2":  "https://coston2-api.flare.network/ext/C/rpc",
		"songbird": "https://songbird-api.flare.network/ext/C/rpc",
		"flare":    "https://flare-api.flare.network/ext/C/rpc",
	}
	chainByID = map[string]string{"31337": "local", "16": "coston", "114": "coston2", "19": "songbird", "14": "flare"}
)

// resolveChain is the chain .env targets: FLARE_CHAIN, which use-chain.sh pins
// (not CHAIN: Foundry's cast reads that as its --chain option); else the chain
// with the CHAIN_ID given; else the simulation unless LOCAL_MODE says otherwise,
// in which case the legacy default, coston2.
func resolveChain() string {
	if c := os.Getenv("FLARE_CHAIN"); chainRPC[c] != "" {
		return c
	}
	if c := chainByID[os.Getenv("CHAIN_ID")]; c != "" {
		return c
	}
	if envOrDefault("LOCAL_MODE", "true") == "true" {
		return "local"
	}
	return "coston2"
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
	// The host-process port block, as cmd/dev/start-tee binds it; clear of the e2e
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

// FeedID is the feed this extension serves, as the flags take it: the extension's
// build fact rendered as hex, so a tool that names no feed names this one.
var FeedID = hexutil.Encode(protocol.FeedID[:])
