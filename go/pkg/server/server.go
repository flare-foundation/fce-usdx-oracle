package server

import (
	"context"
	"math/big"
	"time"

	"fce-usdx-oracle/internal/config"
	extension "fce-usdx-oracle/internal/extension"
	"fce-usdx-oracle/internal/observe"
	"fce-usdx-oracle/internal/provision"
	"fce-usdx-oracle/internal/runtimecfg"
	"fce-usdx-oracle/internal/teenode"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// StartExtension creates and starts the extension server in a goroutine.
// Returns an error channel that receives any ListenAndServe failure.
//
// The extension boots UNPROVISIONED and that is normal. Endpoints arrive by
// on-chain instruction and credentials by direct instruction, so it serves
// not_configured until both have landed — after first start and after every
// restart, since credentials are memory-only.
//
// Two goroutines run from here: the HTTP server, and the observation loop.
// **The loop is what makes the extension answerable at all** — tee-node allows
// an extension 2 seconds per action and an observation takes longer than that,
// so the slow work happens on a timer and a request costs one signature. See
// internal/observe.
//
// There is nothing to load: every pinned value is a constant in internal/config,
// selected at build time. No config file, no parsing, no startup failure mode.
func StartExtension(extensionPort, signPort int) <-chan error {
	node := teenode.New(signPort, 10*time.Second)

	// The machine's own address is resolved lazily on first provisioning.
	verifier := provision.NewVerifier(new(big.Int).SetUint64(config.ChainID), common.Address{})
	store := runtimecfg.New(verifier, node)

	poller := observe.New(store, node)

	e := extension.NewWithProvisioner(extensionPort, signPort, poller, store)

	// Background context: the loop lives as long as the process. Shutdown is by
	// process exit, which is also how the enclave's memory-only credentials are
	// discarded.
	go poller.Run(context.Background())

	logger.Infof("extension started unprovisioned; awaiting SET_ENDPOINTS and SET_SECRET")
	logger.Infof("observing every %s, refusing to sign anything older than %s",
		config.ObservationInterval, config.MaxObservationAge)

	errCh := make(chan error, 1)
	go func() {
		if err := e.Server.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()
	return errCh
}
