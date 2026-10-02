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
	"fce-usdx-oracle/pkg/protocol"

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

// signTimeout bounds each call to tee-node's signing service. Signing and
// decrypting are in-memory operations over loopback, so a call that has not
// returned within a second means tee-node is not answering, not that it is
// busy. It also has to sit inside the 2s tee-node allows an action before
// abandoning it: a signature or a decrypted secret produced after that point
// is one nobody reads, and for SET_SECRET it would take effect after the
// operator was told it failed. The startup identity probe has its own
// schedule (teeIDRetry, teeIDTimeout) and does not depend on this value.
const signTimeout = time.Second

// StartExtension starts the HTTP server and the observation loop, returning a channel
// that receives any ListenAndServe failure. The extension boots UNPROVISIONED and that
// is normal: it observes once SET_ENDPOINTS has landed and SET_SECRET has delivered
// every RPC credential that configuration names, and it signs a feed update only once
// SET_ADMINS has installed the admin sets. Until then GET_FEED answers not_configured.
func StartExtension(extensionPort, signPort int) <-chan error {
	errCh := make(chan error, 1)

	node := teenode.New(signPort, signTimeout)

	// Resolved before the verifier exists
	teeID, err := resolveTeeID(context.Background(), node)
	if err != nil {
		errCh <- err
		return errCh
	}
	// The address is public: it is what the machine registry publishes, what a
	// keeper names in requestFeedUpdate, and what every provisioning envelope is
	// bound to.
	logger.Infof("tee identity resolved: %s", teeID.Hex())

	// The admin policy — which refs the backing set governs, and each set's
	// floor — is pinned in config, so no published payload can move a credential
	// under the weaker set: role TAGS come off the wire, the policy does not.
	verifier := envelope.NewVerifier(
		new(big.Int).SetUint64(config.ChainID()), teeID, protocol.AdminPolicy())
	configStore := store.New(verifier, node)

	loop := poller.New(configStore, node)

	e := extension.NewExtension(extensionPort, signPort, loop, configStore)

	// The loop lives as long as the process. Shutdown is by process exit, which
	// is also how the memory-only credentials are discarded.
	go loop.Run(context.Background())

	logger.Infof("extension started unprovisioned; awaiting SET_ENDPOINTS, SET_ADMINS and SET_SECRET")
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
			// The signing service is routinely not up yet on the first rounds,
			// so each wait is reported at DEBUG; the deadline error above
			// carries the last cause.
			logger.Debugf("attempt %d: tee identity not resolved (%v); retrying", attempt, err)
		}
	}
}
