package server

import (
	"context"
	"math/big"
	"time"

	"fce-usdx-oracle/internal/config"
	extension "fce-usdx-oracle/internal/extension"
	"fce-usdx-oracle/internal/oracle"
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
// There is nothing to load: every pinned value is a constant in internal/config,
// selected at build time. No config file, no parsing, no startup failure mode.
func StartExtension(extensionPort, signPort int) <-chan error {
	node := teenode.New(signPort, 10*time.Second)

	// The machine's own address is resolved lazily on first provisioning.
	verifier := provision.NewVerifier(new(big.Int).SetUint64(config.ChainID), common.Address{})
	store := runtimecfg.New(verifier, node)

	e := extension.NewWithProvisioner(
		extensionPort, signPort, &storeObserver{store: store, node: node}, store)

	logger.Infof("extension started unprovisioned; awaiting SET_ENDPOINTS and SET_SECRET")

	errCh := make(chan error, 1)
	go func() {
		if err := e.Server.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()
	return errCh
}

// storeObserver resolves the current configuration for every observation, so a
// rotated credential or a changed endpoint takes effect on the next price
// request without a restart.
type storeObserver struct {
	store *runtimecfg.Store
	node  oracle.Signer
}

func (o *storeObserver) Observe(
	ctx context.Context, instructionID common.Hash,
) (oracle.Observation, error) {
	cfg, err := o.store.Resolve()
	if err != nil {
		return oracle.Observation{Health: map[string]string{}}, err
	}
	return oracle.Observe(ctx, cfg, o.node, instructionID)
}
