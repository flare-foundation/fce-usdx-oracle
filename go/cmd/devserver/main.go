// Command devserver runs the extension with real provisioning, no chain.
//
// It exists so SET_ENDPOINTS and SET_SECRET can be exercised before
// setEndpoints() is deployed and before a TEE machine is running. The store,
// the provisioner and the readiness state are the production ones — only the
// delivery path is short-circuited, by POSTing actions straight at /action.
//
//	go run ./cmd/mocknode &
//	go run ./cmd/devserver &
//	./scripts/set-endpoints.sh testdata/endpoints/coston2.json
//
// SET_SECRET needs a tee-node (or ./cmd/mocknode) on SIGN_PORT, since the
// envelope's payload is decrypted in-enclave.
//
// NOT a production entrypoint. Production is ./cmd/docker.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"fce-usdx-oracle/internal/config"
	extserver "fce-usdx-oracle/pkg/server"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

func main() {
	errCh := extserver.StartExtension(config.ExtensionPort, config.SignPort)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logger.Infof("devserver listening on :%d", config.ExtensionPort)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Fatalf("server: %v", err)
		}
	case <-sigChan:
		_ = context.Background()
		os.Exit(0)
	}
}
