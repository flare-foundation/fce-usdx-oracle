package extension

import (
	"encoding/json"
	"fmt"
	"net/http"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/common"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// outcome is what a handler produces: the payload it answers with, or the
// failure it refuses with.
//
// Handlers are handed their own decoded input and nothing else — no action
// envelope — so nothing from the envelope can be echoed back by accident. The
// router assembles the result around this.
type outcome struct {
	data []byte

	// Zero value means success. A Failure, never an error: a raw error cannot
	// reach the world-readable Log even by accident.
	fail failure.Failure
}

func succeed(data []byte) outcome      { return outcome{data: data} }
func refuse(f failure.Failure) outcome { return outcome{fail: f} }

// result turns an outcome into the wire result, and records a refusal in the
// counters GET /state publishes.
func (e *Extension) result(
	action teetypes.Action, opCommand common.Hash, o outcome,
) (int, []byte) {
	status, err := uint8(1), error(nil)
	if !o.fail.IsZero() {
		status, err = 0, o.fail

		e.mu.Lock()
		e.failureCount++
		e.lastErrorClass = o.fail.Class()
		e.mu.Unlock()
	}

	b, encErr := json.Marshal(buildResult(action, opCommand, o.data, status, err))
	if encErr != nil {
		// The envelope is built from this package's own types, so this is a
		// programming error; answered with a constant, nothing runtime-derived.
		return http.StatusInternalServerError, []byte("encoding the action result")
	}
	return http.StatusOK, b
}

// buildResult assembles the result envelope for one action. OPType is this
// extension's own constant rather than the received value; checkOPType has
// already established that the two are equal.
func buildResult(
	a teetypes.Action, opCommand common.Hash, data []byte, status uint8, err error,
) teetypes.ActionResult {
	ar := teetypes.ActionResult{
		ID:            a.Data.ID,
		SubmissionTag: a.Data.SubmissionTag,
		Version:       config.Version,
		OPType:        config.OPTypeTeeOracle,
		OPCommand:     opCommand,
		Data:          data,
		Status:        status,
	}
	// Log values are part of the wire contract — see docs/extension-contract.md §4.6.
	switch status {
	case 0:
		ar.Log = fmt.Sprintf("error: %v", err)
	case 1:
		ar.Log = "ok"
	default:
		ar.Log = "pending"
	}
	return ar
}

func getLogFromBody(body []byte) string {
	var ar teetypes.ActionResult
	if err := json.Unmarshal(body, &ar); err != nil {
		return string(body)
	}
	return ar.Log
}
