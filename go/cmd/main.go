// Command cmd runs the extension in STUB MODE for wire-contract conformance.
//
// It serves a hard-coded, unsigned $1.00 and reads no supply or reserve data.
// This exists because scripts/test-conformance.sh exercises the extension alone
// — no tee-node, no proxy, no chain, no credentials — and a real observation
// needs all of those.
//
// The production entrypoint is ./cmd/docker, which is what go/Dockerfile builds.
// It loads the embedded configuration and refuses to start without it.
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
	// NewStub is the only constructor that can build an observerless extension;
	// the production constructors panic on nil, so this binary cannot be
	// mistaken for a live feed by accident — or serve one.
	e := extension.NewStub(config.ExtensionPort, config.SignPort)

	// Graceful shutdown.
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
