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

// StartExtension starts the HTTP server and the observation loop, returning a
// channel that receives any ListenAndServe failure.
//
// The extension boots UNPROVISIONED and that is normal: it serves
// not_configured until SET_ENDPOINTS and SET_SECRET land, after every restart.
// The observation loop is what makes it answerable at all — see internal/observe.
func StartExtension(extensionPort, signPort int) <-chan error {
	node := teenode.New(signPort, 10*time.Second)

	// The machine's own address is resolved lazily on first provisioning.
	verifier := provision.NewVerifier(new(big.Int).SetUint64(config.ChainID), common.Address{})
	store := runtimecfg.New(verifier, node)

	poller := observe.New(store, node)

	e := extension.NewWithProvisioner(extensionPort, signPort, poller, store)

	// The loop lives as long as the process. Shutdown is by process exit, which
	// is also how the memory-only credentials are discarded.
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
