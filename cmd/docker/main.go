// Package main is the container entrypoint: it starts the tee-node server and
// the extension in one process. cmd/start-tee is the same pairing as a host
// process, for `start-services.sh --local`. Docker sets PROXY_URL as an env var,
// which tee-node reads directly via settings.init().
package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teeServer "github.com/flare-foundation/tee-node/pkg/server"

	"fce-usdx-oracle/internal/config"
	extserver "fce-usdx-oracle/pkg/server"
)

// configPort is where tee-node's configuration server listens. Fixed rather
// than configurable, for tee-node's own reason for fixing its port: the
// Confidential Space launch policy enumerates the environment variables an
// operator may override, this is not one of them, so a deployment could never
// change it in production anyway. The Dockerfile's EXPOSE names the same port.
const configPort = 5501

func main() {
	// config.SignPort and config.ExtensionPort are set from SIGN_PORT and
	// EXTENSION_PORT env vars via config.init().
	signPort := config.SignPort
	extensionPort := config.ExtensionPort

	// The extension and tee-node read the same EXTENSION_ID and CHAIN_ID, so they
	// cannot disagree on the extension or the chain every update is signed for.
	// tee-node wants the extension id as a 32-byte hex word where the compose
	// file carries the decimal id, so it is normalised. Neither value has a
	// default: a process without them stops here.
	extEnv, err := config.ExtensionIDEnv(os.Getenv("EXTENSION_ID"))
	if err != nil {
		logger.Fatalf("%v", err)
	}
	os.Setenv("EXTENSION_ID", extEnv)
	logger.Infof("extension id %s (EXTENSION_ID=%s), chain id %d", config.ExtensionID(), extEnv, config.ChainID())

	// Start tee-node in extension mode.
	go teeServer.StartServerExtension(configPort, signPort, extensionPort)

	// Start extension server — fail fast if port binding fails.
	extErrCh := extserver.StartExtension(extensionPort, signPort)

	// Give server a moment to bind, then check for early failures.
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-extErrCh:
		logger.Fatalf("extension server failed to start: %v", err)
	default:
	}

	logger.Infof("extension TEE running (config=%d, sign=%d, ext=%d)", configPort, signPort, extensionPort)

	// Wait for signal or server error.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigChan:
		logger.Info("shutting down")
	case err := <-extErrCh:
		logger.Fatalf("extension server error: %v", err)
	}
}

