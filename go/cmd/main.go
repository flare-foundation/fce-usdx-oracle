// Command cmd runs the extension in STUB MODE for wire-contract conformance:
// a hard-coded, unsigned $1.00, reading no supply or reserve data. Used by
// scripts/test-conformance.sh, which runs the extension with no tee-node, no
// proxy, no chain and no credentials.
//
// The production entrypoint is ./cmd/docker.
package main

import (
	"context"

	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/flare-foundation/go-flare-common/pkg/logger"

	"fce-usdx-oracle/internal/config"
	extension "fce-usdx-oracle/internal/extension"
)

func main() {
	// The only constructor that can build an observerless extension; the
	// production ones panic on nil, so this binary cannot serve a live feed.
	e := extension.NewStub(config.ExtensionPort, config.SignPort)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		ctx, cancel := context.WithTimeout(context.Background(), config.TimeoutShutdown)
		defer cancel()
		if err := e.Server.Shutdown(ctx); err != nil {
			logger.Errorf("graceful shutdown: %v", err)
		}
		os.Exit(0)
	}()

	logger.Infof("starting extension server on :%d", config.ExtensionPort)
	err := e.Server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		logger.Fatalf("server: %v", err)
	}
}
