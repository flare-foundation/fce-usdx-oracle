package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"fce-usdx-oracle/internal/observation"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// Observer returns a signed feed update built from the most recent background
// observation. It reports a cached observation rather than taking one: an
// observation exceeds the 2s tee-node allows per action, so it runs on a timer
// in internal/observation/poller.
type Observer interface {
	// requestedAt is the requesting instruction's block timestamp. It is a
	// reference clock, not a value to publish: the observer measures its cached
	// observation against it and refuses rather than restamping. See
	// internal/observation/poller.
	Report(ctx context.Context, requestedAt uint64) (observation.Observation, error)

	// ReadinessReport answers GET_READINESS. It takes the provisioning half because
	// the observer does not own it, and returns the finished payload: assembling
	// and signing both happen behind the poller's lock.
	ReadinessReport(
		ctx context.Context, instructionID common.Hash, provisioning types.Readiness,
	) ([]byte, error)
}

// Provisioner applies verified configuration. An interface so the extension's
// tests need no signing service and no live endpoints.
type Provisioner interface {
	ApplyEndpoints(payload []byte) error
	ApplyAdmins(payload []byte) error
	ApplySecret(ctx context.Context, message []byte) error
	Readiness() types.Readiness
}

type Extension struct {
	Server *http.Server

	signPort int

	// Never nil: NewExtension rejects a nil observer.
	observer Observer

	// Nil when the extension cannot be provisioned; the provisioning commands
	// then answer NotConfigured.
	provisioner Provisioner
}

// NewExtension builds an Extension listening on extensionPort and calling
// tee-node on signPort.
//
// A nil provisioner is valid: the provisioning commands then answer
// NotConfigured. A nil observer panics, because an extension that cannot
// observe must not start — a fixed price is indistinguishable from a healthy
// feed on the wire, so nothing downstream would detect one.
func NewExtension(
	extensionPort, signPort int, observer Observer, provisioner Provisioner,
) *Extension {
	if observer == nil {
		panic("extension: observer is nil")
	}

	e := &Extension{
		signPort:    signPort,
		observer:    observer,
		provisioner: provisioner,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", e.stateHandler)
	mux.HandleFunc("POST /action", e.actionHandler)

	// Loopback only. tee-node is the one legitimate caller, shares this process
	// and posts to localhost, and processInstruction treats arrival on the
	// instruction channel as authorisation; a listener reachable from anywhere
	// else would let a network peer install its own admins and endpoints.
	// tee-node binds its sign server the same way.
	e.Server = &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", extensionPort), Handler: mux}
	return e
}

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
