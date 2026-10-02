// Command start-tee runs the tee-node and the Go extension as a single host
// process, without Docker. It backs `./deploy/scripts/start-services.sh --local`.
//
// It lives in the extension module rather than tools/ because it links the Go
// extension in-process; tools/ must stay independent of any one language
// implementation. So `--local` is Go-only.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fce-usdx-oracle/internal/config"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	extserver "fce-usdx-oracle/pkg/server"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teeServer "github.com/flare-foundation/tee-node/pkg/server"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/joho/godotenv"
)

// The host-process port block, shared with tools/pkg/configs. It stays clear of
// the e2e simulation's own processes (5500–5503, 6661–6668, 7701, 7702) so both
// can run on one machine; the container image keeps the conventional ports,
// which are never bound on the host.
const (
	ExtConfigurationPort = 5521 // TEE configuration port (proxyURL, initialOwner, extensionID)
	ExtProxyInternalPort = 6683 // Internal port: TEE polls actions from proxy queue
	ExtensionServerPort  = 7721 // TEE signing port: extension calls TEE for signing/encrypting
	ExtensionPort        = 7722 // Extension server port: TEE forwards POST /action here
)

func main() {
	flag.Parse()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	loadEnv()

	_ = setOwnerAddress()

	// The extension and tee-node read the same EXTENSION_ID and CHAIN_ID; the
	// node wants the extension id as a 32-byte hex word, so it is normalised.
	// Neither value has a default: a process without them stops here.
	extEnv, err := config.ExtensionIDEnv(os.Getenv("EXTENSION_ID"))
	if err != nil {
		logger.Fatalf("%v", err)
	}
	if err := os.Setenv("EXTENSION_ID", extEnv); err != nil {
		logger.Fatalf("%v", err)
	}
	logger.Infof("extension id %s, chain id %d", config.ExtensionID(), config.ChainID())

	runExtension()

	sig := <-signalChan
	logger.Infof("Received %v signal, shutting down", sig)
}

func loadEnv() {
	// Try project-root .env first (works even when CWD is elsewhere). This file
	// is cmd/dev/start-tee/main.go, three directories below the root.
	_, thisFile, _, _ := runtime.Caller(0)
	rootEnv := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".env")
	if err := godotenv.Load(rootEnv); err != nil {
		// Fallback to CWD .env.
		if err := godotenv.Load(); err != nil {
			fmt.Printf("Warning: Error loading .env file: %v\n", err)
		}
	}
}

func runExtension() {
	// Start tee-node in extension mode.
	go teeServer.StartServerExtension(ExtConfigurationPort, ExtensionServerPort, ExtensionPort)

	// Start extension server — fail fast if port binding fails.
	extErrCh := extserver.StartExtension(ExtensionPort, ExtensionServerPort)

	// Give server a moment to bind, then check for early failures.
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-extErrCh:
		logger.Fatalf("extension server failed to start: %v", err)
	default:
	}

	logger.Infof("Starting extension TEE on port %d", ExtConfigurationPort)

	time.Sleep(150 * time.Millisecond)

	err := setProxyURL(ExtConfigurationPort, ExtProxyInternalPort)
	if err != nil {
		logger.Fatalf("Error: %v", err)
	}
}

// setProxyURL points the freshly started node at the local extension proxy.
// Inlined from tools/pkg/fccutils so this command carries no dependency on the
// tools module.
func setProxyURL(configurationPort, proxyPort int) error {
	url := fmt.Sprintf("http://localhost:%d", proxyPort)
	request := teetypes.ConfigureProxyURLRequest{URL: &url}

	body, err := json.Marshal(request)
	if err != nil {
		return err
	}

	url = fmt.Sprintf("http://localhost:%d/proxy", configurationPort)
	logger.Infof("Setting proxy url on tee: %s", url)
	// Bounded: the node may accept the connection before it is ready to answer.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func setOwnerAddress() common.Address {
	owner := os.Getenv("INITIAL_OWNER")
	if owner == "" {
		var privKey *ecdsa.PrivateKey
		var err error
		privKeyString := os.Getenv("EXTENSION_OWNER_KEY")
		if privKeyString == "" {
			// Default Hardhat-funded key.
			privKey, err = crypto.HexToECDSA("804b01a8c27a65cc694a867be76edae3ccce7a7161cda1f67a8349df696d2207")
			if err != nil {
				panic("cannot parse default private key")
			}
		} else {
			if strings.HasPrefix(privKeyString, "0x") || strings.HasPrefix(privKeyString, "0X") {
				privKeyString = privKeyString[2:]
			}
			privKey, err = crypto.HexToECDSA(privKeyString)
			if err != nil {
				logger.Fatalf("Error: %v", err)
			}
		}

		ownerAddress := crypto.PubkeyToAddress(privKey.PublicKey)
		if err := os.Setenv("INITIAL_OWNER", ownerAddress.String()); err != nil {
			logger.Fatalf("%v", err)
		}
	}

	return common.HexToAddress(owner)
}
