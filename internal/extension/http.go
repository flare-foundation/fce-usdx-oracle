package extension

import (
	"encoding/json"
	"fmt"
	"net/http"

	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// stateHandler serves GET /state, the route tee-node reads on every info
// request and folds into the attested TEE info. It answers the empty state.
func (e *Extension) stateHandler(w http.ResponseWriter, _ *http.Request) {
	empty := types.StateResponse{State: hexutil.Bytes{}}
	if err := json.NewEncoder(w).Encode(empty); err != nil {
		http.Error(w, fmt.Sprintf("sending response: %v", err), http.StatusInternalServerError)
	}
}

// actionHandler serves POST /action: decode the action, route it, write the
// result the router produced.
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
