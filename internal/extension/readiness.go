package extension

import (
	"context"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/common"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// processGetReadiness returns the machine's signed account of itself: the
// provisioning block and the poller's view of its observations. It does not
// fail closed: an unprovisioned machine reports that it is unprovisioned.
//
// correlator is bound into the signed report. This command is direct-only, so
// there is no instruction id and the proxy's action id stands in; it must be
// non-zero, or every readiness report would bind the same value.
func (e *Extension) processGetReadiness(di *teetypes.DirectInstruction, correlator common.Hash) outcome {
	// The pinned envelope or nothing, and neither the length nor the content is
	// echoed back into the public log.
	if len(di.Message) != 1 || di.Message[0] != config.ReadinessRequestV1 {
		return refuse(failure.UnexpectedPayload)
	}

	// Both halves are required to describe readiness at all, so their absence is
	// itself reported as NotConfigured.
	if e.observer == nil || e.provisioner == nil {
		return refuse(failure.NotConfigured)
	}

	data, err := e.observer.ReadinessReport(
		context.Background(), correlator, e.provisioner.Readiness())
	if err != nil {
		return refuse(failure.Coerce(err, failure.Internal))
	}

	return succeed(data)
}
