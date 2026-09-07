// Package main is the container entrypoint: it starts the tee-node server and
// the extension in one process. cmd/start-tee is the same pairing as a host
// process, for `start-services.sh --local`. Docker sets PROXY_URL as an env var,
// which tee-node reads directly via settings.init().
package main

import (
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teeServer "github.com/flare-foundation/tee-node/pkg/server"

	"fce-usdx-oracle/internal/config"
	extserver "fce-usdx-oracle/pkg/server"
)

func main() {
	configPort := intEnv("CONFIG_PORT", 5501)

	// config.SignPort and config.ExtensionPort are set from SIGN_PORT and
	// EXTENSION_PORT env vars via config.init().
	signPort := config.SignPort
	extensionPort := config.ExtensionPort

	// tee-node reads EXTENSION_ID as a 32-byte hex word; the compose file and
	// extension.env carry the decimal id. Normalised here, and refused if it is
	// not the id this build is pinned to.
	extEnv, err := config.ExtensionIDEnv(os.Getenv("EXTENSION_ID"))
	if err != nil {
		logger.Fatalf("%v", err)
	}
	os.Setenv("EXTENSION_ID", extEnv)
	logger.Infof("extension id %s (EXTENSION_ID=%s)", config.ExtensionID, extEnv)

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

func intEnv(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}
