package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/oracle"
	"fce-usdx-oracle/internal/report"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"

	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
)

// Observer returns a signed feed update built from the most recent background
// observation. Report, not Observe: observing takes longer than the 2s
// tee-node allows, so it runs on a timer in internal/observe.
type Observer interface {
	// requestedAt is the requesting instruction's block timestamp. It is a
	// reference clock, not a value to publish: the observer measures its cached
	// observation against it and refuses rather than restamping. See
	// internal/observe.checkClock.
	Report(ctx context.Context, requestedAt uint64) (oracle.Observation, error)

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
	mu     sync.RWMutex
	Server *http.Server

	signPort int

	// nil in stub mode, when no oracle configuration was supplied.
	observer Observer

	// nil in stub mode. When set, the provisioning commands are routed to it.
	provisioner Provisioner

	lastPrice      string
	lastDecimals   int8
	lastObservedAt uint64
	updateCount    int
	failureCount   int
	lastErrorClass failure.Class
	sourceHealth   map[string]string
}

// NewWithObserver builds an Extension backed by a live observer. A nil observer
// panics — the scaffold's New() once silently shipped a hard-coded price.
func NewWithObserver(extensionPort, signPort int, observer Observer) *Extension {
	return NewWithProvisioner(extensionPort, signPort, observer, nil)
}

// NewWithProvisioner builds a fully wired Extension.
//
// A nil observer panics rather than degrading to a hard-coded price: the
// scaffold's New() silently produced one, which is how the production
// entrypoint once served $1.00 while reading no source at all.
func NewWithProvisioner(
	extensionPort, signPort int, observer Observer, provisioner Provisioner,
) *Extension {
	if observer == nil {
		panic("extension: observer is nil")
	}
	return newExtension(extensionPort, signPort, observer, provisioner)
}

func newExtension(
	extensionPort, signPort int, observer Observer, provisioner Provisioner,
) *Extension {
	e := &Extension{
		signPort:       signPort,
		observer:       observer,
		provisioner:    provisioner,
		lastErrorClass: failure.ClassNone,
		sourceHealth:   map[string]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", e.stateHandler)
	mux.HandleFunc("POST /action", e.actionHandler)

	e.Server = &http.Server{Addr: fmt.Sprintf(":%d", extensionPort), Handler: mux}
	return e
}

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
	stateResponse := types.StateResponse{
		StateVersion: teeutils.ToHash(config.Version),
		State: types.State{
			LastPrice:      e.lastPrice,
			LastDecimals:   e.lastDecimals,
			LastObservedAt: e.lastObservedAt,
			UpdateCount:    e.updateCount,
			FailureCount:   e.failureCount,
			LastErrorClass: string(e.lastErrorClass),
			Readiness:      readiness,
			SourceHealth:   health,
		},
	}
	e.mu.RUnlock()

	err := json.NewEncoder(w).Encode(stateResponse)
	if err != nil {
		http.Error(w, fmt.Sprintf("sending response: %v", err), http.StatusInternalServerError)
		return
	}
}

// processAction parses whichever envelope the action carries and hands it to
// that channel's router.
//
// The envelope is chosen by Data.Type, never guessed: parsing one as the other
// does not fail — encoding/json drops unknown fields — it yields an EMPTY
// payload. An unrecognised type is refused rather than treated as an
// instruction, because the instruction router is the privileged one.
func (e *Extension) processAction(action teetypes.Action) (int, []byte) {
	switch action.Data.Type {
	case teetypes.Instruction:
		df, err := processorutils.Parse[instruction.DataFixed](action.Data.Message)
		if err != nil {
			return http.StatusBadRequest, []byte(fmt.Sprintf("decoding fixed data: %v", err))
		}
		if code, body, bad := checkOPType(df.OPType); bad {
			return code, body
		}
		// The action id and the id inside the cosigned payload name the same
		// instruction by construction. A disagreement means the envelope and its
		// contents describe different instructions, which nothing legitimate
		// produces, so it is refused rather than reconciled.
		if action.Data.ID != df.InstructionID {
			return http.StatusBadRequest, []byte("action id does not match the instruction id")
		}
		return e.processInstruction(action, df)

	case teetypes.Direct:
		di, err := processorutils.Parse[teetypes.DirectInstruction](action.Data.Message)
		if err != nil {
			return http.StatusBadRequest, []byte(fmt.Sprintf("decoding direct instruction: %v", err))
		}
		if code, body, bad := checkOPType(di.OPType); bad {
			return code, body
		}
		return e.processDirect(action, di)

	default:
		return http.StatusBadRequest, []byte(fmt.Sprintf(
			"unknown action type %q", action.Data.Type))
	}
}

// checkOPType rejects an action raised for another extension.
//
// A plain-text 501 rather than a failure value, and deliberately more
// forthcoming than any other rejection here. tee-node turns a non-200 into an
// error carrying this body, and processorutils.Invalid puts that into
// ActionResult.Log — so it does reach the public result endpoint, by a different
// route than e.fail.
//
// Chatty anyway, because the only runtime value echoed is the opType the caller
// just sent, next to constants compiled into this build. Nothing is disclosed
// that the caller did not already have, and a mismatch reported as two bare
// hashes is undebuggable. Contrast processGetFeed, which echoes nothing: there
// the payload is attacker-chosen data about OUR state.
func checkOPType(got common.Hash) (int, []byte, bool) {
	if got == config.OPTypeTeeOracle {
		return 0, nil, false
	}
	return http.StatusNotImplemented, []byte(fmt.Sprintf(
		"unsupported op type: received %s, expected %s (%s)",
		got.Hex(), config.OPTypeTeeOracle.Hex(), config.OpName(config.OPTypeTeeOracle),
	)), true
}

// processInstruction routes the commands raised on-chain, through
// TeeOracleInstructionsSender. Reaching this router IS the authorisation: the
// sender's governance check has already run and the data providers have
// cosigned the instruction. Nothing here may become reachable from processDirect.
func (e *Extension) processInstruction(
	action teetypes.Action, df *instruction.DataFixed,
) (int, []byte) {
	switch df.OPCommand {
	case config.OPCommandGetFeed:
		// Requested through requestFeedUpdate(), which pays the registry fee and
		// has the cosigning quorum behind it.
		return e.result(action, df.OPCommand, e.processGetFeed(df))

	case config.OPCommandSetEndpoints:
		if e.provisioner == nil {
			return e.result(action, df.OPCommand, refuse(failure.NotConfigured))
		}
		return e.result(action, df.OPCommand, e.processSetEndpoints(df))

	case config.OPCommandSetAdmins:
		if e.provisioner == nil {
			return e.result(action, df.OPCommand, refuse(failure.NotConfigured))
		}
		return e.result(action, df.OPCommand, e.processSetAdmins(df))

	case config.OPCommandGetReadiness, config.OPCommandSetSecret:
		// Direct-only, and named here rather than left to the default so the
		// refusal says "wrong channel" instead of "unknown command".
		return e.result(action, df.OPCommand, refuse(failure.WrongActionType))

	default:
		return unsupportedCommand(df.OPCommand)
	}
}

// processDirect routes the commands raised on the proxy. Nothing here is
// authorised by having arrived: SET_SECRET carries its own M-of-N admin
// signatures, and GET_READINESS grants nothing.
func (e *Extension) processDirect(
	action teetypes.Action, di *teetypes.DirectInstruction,
) (int, []byte) {
	switch di.OPCommand {
	case config.OPCommandGetReadiness:
		// A price is worth making permissionless; an operational status report is
		// not, and it is stale within minutes.
		return e.result(action, di.OPCommand, e.processGetReadiness(di, action.Data.ID))

	case config.OPCommandSetSecret:
		if e.provisioner == nil {
			return e.result(action, di.OPCommand, refuse(failure.NotConfigured))
		}
		return e.result(action, di.OPCommand, e.processSetSecret(di))

	case config.OPCommandGetFeed, config.OPCommandSetEndpoints, config.OPCommandSetAdmins:
		// Instruction-only. Over this channel GET_FEED would hand the same signed
		// update to anyone holding the proxy API key, and the two SET_ commands
		// would skip the sender's onlyGovernance check entirely.
		return e.result(action, di.OPCommand, refuse(failure.WrongActionType))

	default:
		return unsupportedCommand(di.OPCommand)
	}
}

// unsupportedCommand names every command this build serves, on the same terms as
// checkOPType: whoever reads it is debugging a mismatch.
func unsupportedCommand(cmd common.Hash) (int, []byte) {
	return http.StatusNotImplemented, []byte(fmt.Sprintf(
		"unsupported op command: received %s, expected one of [%s (%s), %s (%s), %s (%s), %s (%s), %s (%s)]",
		cmd.Hex(),
		config.OPCommandGetFeed.Hex(), config.OpName(config.OPCommandGetFeed),
		config.OPCommandGetReadiness.Hex(), config.OpName(config.OPCommandGetReadiness),
		config.OPCommandSetEndpoints.Hex(), config.OpName(config.OPCommandSetEndpoints),
		config.OPCommandSetAdmins.Hex(), config.OpName(config.OPCommandSetAdmins),
		config.OPCommandSetSecret.Hex(), config.OpName(config.OPCommandSetSecret),
	))
}

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

	b, _ := json.Marshal(buildResult(action, opCommand, o.data, status, err))
	return http.StatusOK, b
}

// processGetFeed signs the latest observation and returns the payload. Fails
// closed: never guesses, never falls back to a cached price. A frozen feed with
// an ageing timestamp is the designed failure mode.
func (e *Extension) processGetFeed(df *instruction.DataFixed) outcome {
	// The request names a feed, because one sender serves every feed of its
	// extension. Anything that is not exactly one ABI-encoded FeedUpdateRequest
	// is refused rather than guessed at, and nothing about it is echoed back:
	// ActionResult.Log is public.
	feedID, reqErr := report.DecodeFeedRequest(df.OriginalMessage)
	if reqErr != nil {
		return refuse(failure.Coerce(reqErr, failure.Internal))
	}
	// Refused here rather than observed and signed. An update naming another feed
	// would be rejected by that feed's store anyway, but only after this machine
	// had spent an observation on it and published the answer to a public log.
	if feedID != config.FeedID {
		return refuse(failure.UnexpectedPayload)
	}

	// df.Timestamp is the block timestamp of the instruction event, cosigned by
	// the data-provider quorum along with the rest of DataFixed. Always a real
	// block here — GET_FEED is instruction-only, so this handler has one. The
	// observer treats it as the clock this machine is checked against; it never
	// becomes the update's observedAt, which must stay the moment the inputs
	// were read.
	obs, err := e.observer.Report(context.Background(), df.Timestamp)

	// Health is reported either way: on failure it is what tells an operator
	// WHICH source broke.
	e.mu.Lock()
	e.sourceHealth = obs.Health
	e.mu.Unlock()

	if err != nil {
		return refuse(failure.Coerce(err, failure.Internal))
	}

	e.mu.Lock()
	e.lastPrice = strconv.FormatInt(int64(obs.Value), 10)
	e.lastDecimals = obs.Decimals
	e.lastObservedAt = obs.ObservedAt
	e.updateCount++
	e.lastErrorClass = failure.ClassNone
	e.mu.Unlock()

	return succeed(obs.Data)
}

// processGetReadiness answers with the machine's own account of itself, signed.
// Never fails closed — "nothing is configured" is the answer. Leaves the /state
// counters alone: those describe prices served, and polling is not production.
//
// correlator is the proxy's action id, taken as an argument because the signed
// report binds it: direct-only, so there is no instruction id, and a zero would
// make every readiness answer bind the same one.
func (e *Extension) processGetReadiness(di *teetypes.DirectInstruction, correlator common.Hash) outcome {
	// The pinned envelope or nothing, and neither the length nor the content is
	// echoed back into the public log.
	if len(di.Message) != 1 || di.Message[0] != config.ReadinessRequestV1 {
		return refuse(failure.UnexpectedPayload)
	}

	// Both halves are needed, and without a provisioner neither exists.
	// Reporting NotConfigured is honest: there is no configuration to describe.
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
