package server

import (
	"context"

	extension "fce-usdx-oracle/internal/extension"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/oracle"
	"fce-usdx-oracle/internal/setup"

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
// Only the pinned configuration is required to start: the host allowlist, the
// target binding, and the observation bounds. A failure to load those is fatal,
// because they are what makes the rest safe to accept.
func StartExtension(extensionPort, signPort int) <-chan error {
	errCh := make(chan error, 1)

	ext, err := setup.Load(signPort)
	if err != nil {
		logger.Errorf("extension configuration failed: %v", err)
		errCh <- err
		return errCh
	}

	observer := &storeObserver{store: ext.Store, node: ext.Node}

	e := extension.NewWithProvisioner(extensionPort, signPort, observer, ext.Provisioner)
	logger.Infof("extension started unprovisioned; awaiting SET_ENDPOINTS and SET_SECRET")

	go func() {
		if err := e.Server.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()
	return errCh
}

// storeObserver reads the current configuration for every observation, so a
// rotated credential or a changed endpoint takes effect on the next price
// request without a restart.
type storeObserver struct {
	store interface {
		Config() (oracle.Config, bool)
	}
	node oracle.Signer
}

func (o *storeObserver) Observe(
	ctx context.Context, instructionID common.Hash,
) (oracle.Observation, error) {
	cfg, ok := o.store.Config()
	if !ok {
		return oracle.Observation{Health: map[string]string{}}, failure.NotConfigured
	}
	return oracle.Observe(ctx, cfg, o.node, instructionID)
}
