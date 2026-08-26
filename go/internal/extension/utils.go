package extension

import (
	"encoding/json"
	"fce-usdx-oracle/internal/config"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// --- In most cases, you will not need to modify this file. ---

func (e *Extension) actionHandler(w http.ResponseWriter, r *http.Request) {
	var action teetypes.Action
	err := json.NewDecoder(r.Body).Decode(&action)
	if err != nil {
		http.Error(w, fmt.Sprintf("decoding action: %v", err), http.StatusBadRequest)
		return
	}

	logger.Infof("received action, ID: %s", action.Data.ID)

	status, body := e.processAction(action)

	logger.Infof("sending action result, ID: %s, status: %d, log: %s", action.Data.ID, status, getLogFromBody(body))

	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// buildResult assembles the result envelope. OPType is this extension's own,
// not the received value: nothing reaches here until checkOPType has confirmed
// the two are equal.
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
