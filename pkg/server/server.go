package server

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"fce-usdx-oracle/internal/config"
	extension "fce-usdx-oracle/internal/extension"
	"fce-usdx-oracle/internal/observation/poller"
	"fce-usdx-oracle/internal/provisioning/envelope"
	"fce-usdx-oracle/internal/provisioning/store"
	"fce-usdx-oracle/internal/teenode"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// teeIDTimeout bounds the startup wait for tee-node's signing service. Sized
// for a service coming up alongside this process, not for one that is down:
// past it the extension refuses to start rather than serving without an
// identity.
const teeIDTimeout = time.Minute

// teeIDRetry is the gap between resolution attempts during startup.
const teeIDRetry = time.Second

// StartExtension starts the HTTP server and the observation loop, returning a channel
// that receives any ListenAndServe failure. The extension boots UNPROVISIONED and that
// is normal: it serves not_configured until SET_ENDPOINTS and SET_SECRET land.
func StartExtension(extensionPort, signPort int) <-chan error {
	errCh := make(chan error, 1)

	node := teenode.New(signPort, 10*time.Second)

	// Resolved before the verifier exists
	teeID, err := resolveTeeID(context.Background(), node)
	if err != nil {
		errCh <- err
		return errCh
	}
	// The address is public: it is what governance names in setEndpoints and
	// what the machine registry publishes.
	logger.Infof("tee identity resolved: %s", teeID.Hex())

	// The admin policy — which refs the backing set governs, and each set's
	// floor — is pinned in config, so no published payload can move a credential
	// under the weaker set: role TAGS come off the wire, the policy does not.
	verifier := envelope.NewVerifier(
		new(big.Int).SetUint64(config.ChainID()), teeID, config.AdminPolicy())
	configStore := store.New(verifier, node)

	loop := poller.New(configStore, node)

	e := extension.NewExtension(extensionPort, signPort, loop, configStore)

	// The loop lives as long as the process. Shutdown is by process exit, which
	// is also how the memory-only credentials are discarded.
	go loop.Run(context.Background())

	logger.Infof("extension started unprovisioned; awaiting SET_ENDPOINTS and SET_SECRET")
	logger.Infof("observing every %s, refusing to sign anything older than %s",
		config.ObservationInterval, config.MaxObservationAge)

	go func() {
		if err := e.Server.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()
	return errCh
}

// resolveTeeID waits for tee-node to report this machine's own address.
//
// Retried rather than attempted once: the signing service shares this process
// and is routinely not listening yet when the extension reaches this point.
// Exhausting the deadline is fatal rather than deferred, because an identity
// that never arrives leaves the machine unable to accept any credential.
func resolveTeeID(ctx context.Context, node *teenode.Client) (common.Address, error) {
	ctx, cancel := context.WithTimeout(ctx, teeIDTimeout)
	defer cancel()

	for attempt := 1; ; attempt++ {
		teeID, err := node.TeeID(ctx)
		if err == nil && teeID == (common.Address{}) {
			// Reported rather than passed on: NewVerifier panics on a zero
			// address, and a malfunctioning signing service should not read as
			// a programmer error.
			err = errors.New("signing service reported the zero address")
		}
		if err == nil {
			return teeID, nil
		}

		select {
		case <-ctx.Done():
			return common.Address{}, fmt.Errorf(
				"tee identity unresolved after %s, so the extension cannot be provisioned: %w",
				teeIDTimeout, err)
		case <-time.After(teeIDRetry):
			logger.Warnf("attempt %d: tee identity not resolved (%v); retrying", attempt, err)
		}
	}
}
