package extension

import (
	"encoding/json"
	"fmt"
	"net/http"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/pkg/types"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
)

func (e *Extension) stateHandler(w http.ResponseWriter, r *http.Request) {
	// Read readiness outside the lock: the provisioner has its own.
	readiness := types.Readiness{MissingRefs: []string{}}
	if e.provisioner != nil {
		readiness = e.provisioner.Readiness()
	}

	e.mu.RLock()
	// Copy the map: handing the live one to the encoder would race with a
	// concurrent action updating source health.
	health := make(map[string]string, len(e.sourceHealth))
	for k, v := range e.sourceHealth {
		health[k] = v
	}
	state := types.State{
		LastPrice:      e.lastPrice,
		LastDecimals:   e.lastDecimals,
		LastObservedAt: e.lastObservedAt,
		UpdateCount:    e.updateCount,
		FailureCount:   e.failureCount,
		LastErrorClass: string(e.lastErrorClass),
		Readiness:      readiness,
		SourceHealth:   health,
	}
	e.mu.RUnlock()

	// Bytes, not an object: tee-node decodes this body as its TeeState and
	// carries State opaquely into the attested info the proxy republishes.
	encoded, err := types.EncodeState(state)
	if err != nil {
		http.Error(w, fmt.Sprintf("encoding state: %v", err), http.StatusInternalServerError)
		return
	}
	stateResponse := types.StateResponse{
		StateVersion: teeutils.ToHash(config.Version),
		State:        encoded,
	}
	if err := json.NewEncoder(w).Encode(stateResponse); err != nil {
		http.Error(w, fmt.Sprintf("sending response: %v", err), http.StatusInternalServerError)
		return
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
