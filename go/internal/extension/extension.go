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

// parseActionData extracts the op identifiers and payload from a DataFixed
// (instruction) or a DirectInstruction (direct). Parsing one as the other yields an
// EMPTY payload rather than an error, so the envelope comes from Data.Type.
func parseActionData(action teetypes.Action) (*instruction.DataFixed, error) {
	if action.Data.Type != teetypes.Direct {
		return processorutils.Parse[instruction.DataFixed](action.Data.Message)
	}

	di, err := processorutils.Parse[teetypes.DirectInstruction](action.Data.Message)
	if err != nil {
		return nil, err
	}

	// No on-chain instruction behind it, so the proxy's action id is the
	// correlator. The feed store no longer records one at all — GET_READINESS
	// still binds it, which is why it is carried here.
	return &instruction.DataFixed{
		InstructionID:   action.Data.ID,
		OPType:          di.OPType,
		OPCommand:       di.OPCommand,
		OriginalMessage: di.Message,
	}, nil
}

func (e *Extension) processAction(action teetypes.Action) (int, []byte) {
	dataFixed, err := parseActionData(action)
	if err != nil {
		return http.StatusBadRequest, []byte(fmt.Sprintf("decoding fixed data: %v", err))
	}

	if dataFixed.OPType != teeutils.ToHash(config.OPTypeTeeOracle) {
		return http.StatusNotImplemented, []byte(fmt.Sprintf(
			"unsupported op type: received %s, expected %s (%s)",
			dataFixed.OPType.Hex(), teeutils.ToHash(config.OPTypeTeeOracle).Hex(), config.OPTypeTeeOracle,
		))
	}
	return e.processTeeOracle(action, dataFixed)
}

// processTeeOracle routes every command this extension serves. One op type
// covers all of them upstream, so the switch below is the whole routing table
// and each arm's action-type guard is the only fence left between a
// permissionless read and a governance-published write. There is deliberately
// no default arm that accepts both channels: an unrecognised command is a 501,
// and a recognised one arriving over the wrong channel is a refusal.
func (e *Extension) processTeeOracle(action teetypes.Action, df *instruction.DataFixed) (int, []byte) {
	switch {
	case df.OPCommand == teeutils.ToHash(config.OPCommandGetFeed):
		// Instruction only. An update is requested through requestFeedUpdate() on
		// the on-chain sender, which pays the registry fee and has the cosigning
		// quorum behind it; the direct channel would hand the same signed update
		// to anyone holding the proxy API key, for free and off the record.
		if action.Data.Type != teetypes.Instruction {
			return respond(e, action, df, failure.WrongActionType)
		}

		ar := e.processGetFeed(action, df)
		b, _ := json.Marshal(ar)
		return http.StatusOK, b

	case df.OPCommand == teeutils.ToHash(config.OPCommandGetReadiness):
		// Direct only. See config.OPCommandGetReadiness: a price is worth making
		// permissionless, an operational status report is not.
		if action.Data.Type != teetypes.Direct {
			return respond(e, action, df, failure.WrongActionType)
		}

		ar := e.processGetReadiness(action, df)
		b, _ := json.Marshal(ar)
		return http.StatusOK, b

	// The provisioning commands. Their guards live with their handlers in
	// config_handler.go, one per command.
	case df.OPCommand == teeutils.ToHash(config.OPCommandSetEndpoints):
		if e.provisioner == nil {
			return respond(e, action, df, failure.NotConfigured)
		}
		return e.processSetEndpoints(action, df)

	case df.OPCommand == teeutils.ToHash(config.OPCommandSetAdmins):
		if e.provisioner == nil {
			return respond(e, action, df, failure.NotConfigured)
		}
		return e.processSetAdmins(action, df)

	case df.OPCommand == teeutils.ToHash(config.OPCommandSetSecret):
		if e.provisioner == nil {
			return respond(e, action, df, failure.NotConfigured)
		}
		return e.processSetSecret(action, df)

	default:
		return http.StatusNotImplemented, []byte(fmt.Sprintf(
			"unsupported op command: received %s, expected one of [%s (%s), %s (%s), %s (%s), %s (%s), %s (%s)]",
			df.OPCommand.Hex(),
			teeutils.ToHash(config.OPCommandGetFeed).Hex(), config.OPCommandGetFeed,
			teeutils.ToHash(config.OPCommandGetReadiness).Hex(), config.OPCommandGetReadiness,
			teeutils.ToHash(config.OPCommandSetEndpoints).Hex(), config.OPCommandSetEndpoints,
			teeutils.ToHash(config.OPCommandSetAdmins).Hex(), config.OPCommandSetAdmins,
			teeutils.ToHash(config.OPCommandSetSecret).Hex(), config.OPCommandSetSecret,
		))
	}
}

// processGetFeed signs the latest observation and returns the payload. Fails
// closed: never guesses, never falls back to a cached price. A frozen feed with
// an ageing timestamp is the designed failure mode.
func (e *Extension) processGetFeed(action teetypes.Action, df *instruction.DataFixed) teetypes.ActionResult {
	// The request names a feed, because one sender serves every feed of its
	// extension. Anything that is not exactly one ABI-encoded FeedUpdateRequest
	// is refused rather than guessed at, and nothing about it is echoed back:
	// ActionResult.Log is public.
	feedID, reqErr := report.DecodeFeedRequest(df.OriginalMessage)
	if reqErr != nil {
		return e.fail(action, df, failure.Coerce(reqErr, failure.Internal))
	}
	// Refused here rather than observed and signed. An update naming another feed
	// would be rejected by that feed's store anyway, but only after this machine
	// had spent an observation on it and published the answer to a public log.
	if feedID != config.FeedID {
		return e.fail(action, df, failure.UnexpectedPayload)
	}

	// df.Timestamp is the block timestamp of the instruction event, cosigned by
	// the data-provider quorum along with the rest of DataFixed. The observer
	// treats it as the clock this machine is checked against; it never becomes
	// the update's observedAt, which must stay the moment the inputs were read.
	obs, err := e.observer.Report(context.Background(), df.Timestamp)

	// Health is reported either way: on failure it is what tells an operator
	// WHICH source broke.
	e.mu.Lock()
	e.sourceHealth = obs.Health
	e.mu.Unlock()

	if err != nil {
		return e.fail(action, df, failure.Coerce(err, failure.Internal))
	}

	e.mu.Lock()
	e.lastPrice = strconv.FormatInt(int64(obs.Value), 10)
	e.lastDecimals = obs.Decimals
	e.lastObservedAt = obs.ObservedAt
	e.updateCount++
	e.lastErrorClass = failure.ClassNone
	e.mu.Unlock()

	return buildResult(action, df, obs.Data, 1, nil)
}

// processGetReadiness answers with the machine's own account of itself, signed.
// Never fails closed — "nothing is configured" is the answer. Leaves the /state
// counters alone: those describe prices served, and polling is not production.
func (e *Extension) processGetReadiness(
	action teetypes.Action, df *instruction.DataFixed,
) teetypes.ActionResult {
	// The pinned envelope or nothing, and neither the length nor the content is
	// echoed back into the public log.
	if len(df.OriginalMessage) != 1 || df.OriginalMessage[0] != config.ReadinessRequestV1 {
		return e.fail(action, df, failure.UnexpectedPayload)
	}

	// Both halves are needed, and in stub mode neither exists. Reporting
	// NotConfigured is honest: a stub has no configuration to describe.
	if e.observer == nil || e.provisioner == nil {
		return e.fail(action, df, failure.NotConfigured)
	}

	data, err := e.observer.ReadinessReport(
		context.Background(), df.InstructionID, e.provisioner.Readiness())
	if err != nil {
		return e.fail(action, df, failure.Coerce(err, failure.Internal))
	}

	return buildResult(action, df, data, 1, nil)
}

// fail records a failed observation and returns a status-0 result. It takes a
// failure.Failure, not an error, and that is the safety property: a raw error
// cannot reach the world-readable Log even by accident.
func (e *Extension) fail(
	action teetypes.Action, df *instruction.DataFixed, f failure.Failure,
) teetypes.ActionResult {
	e.mu.Lock()
	e.failureCount++
	e.lastErrorClass = f.Class()
	e.mu.Unlock()

	return buildResult(action, df, nil, 0, f)
}
