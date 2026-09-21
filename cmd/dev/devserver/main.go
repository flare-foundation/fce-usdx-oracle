// Command devserver runs the extension with real provisioning, no chain, so
// SET_ENDPOINTS and SET_SECRET can be exercised before anything is deployed.
// Only the delivery path is short-circuited — actions are POSTed at /action.
//
//	go run ./cmd/dev/mocknode &
//	go run ./cmd/dev/devserver &
//	./deploy/scripts/set-endpoints.sh deploy/config/endpoints/coston2.json
//	./deploy/scripts/set-admins.sh deploy/config/admins/dev-wellknown-keys.json
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
