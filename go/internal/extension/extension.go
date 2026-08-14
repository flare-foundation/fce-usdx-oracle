package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/oracle"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
)

// Observer returns a signed price report built from the most recent background
// observation. Report, not Observe: observing takes longer than the 2s
// tee-node allows, so it runs on a timer in internal/observe.
type Observer interface {
	Report(ctx context.Context, instructionID common.Hash) (oracle.Observation, error)

	// ReadinessReport answers GET_READINESS. It takes the provisioning half
	// because the observer does not own it, and returns the finished
	// ActionResult payload — assembling and signing happen behind the poller's
	// lock, where the observation state and the signer both live.
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

	// nil in stub mode. When set, USDX_CONFIG is routed to it.
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
// panics: the scaffold's New() silently produced a stub, which is how the
// production entrypoint once served a hard-coded price. NewStub is now the only
// way to a stub, and it is named at its single call site.
func NewWithObserver(extensionPort, signPort int, observer Observer) *Extension {
	return NewWithProvisioner(extensionPort, signPort, observer, nil)
}

// NewWithProvisioner builds a fully wired Extension. A nil observer panics —
// see NewWithObserver.
func NewWithProvisioner(
	extensionPort, signPort int, observer Observer, provisioner Provisioner,
) *Extension {
	if observer == nil {
		panic("extension: observer is nil — build a stub with NewStub, never by passing nil")
	}
	return newExtension(extensionPort, signPort, observer, provisioner)
}

// NewStub answers GET_PRICE with an unsigned $1.00, for
// scripts/test-conformance.sh, which runs the extension with no tee-node, no
// chain and no credentials. It must never be called outside that harness.
func NewStub(extensionPort, signPort int) *Extension {
	return newExtension(extensionPort, signPort, nil, nil)
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

// StubMode reports whether this extension is serving a hard-coded price.
// Surface it in health checks: it is otherwise indistinguishable from a
// healthy, fully-backed feed.
func (e *Extension) StubMode() bool { return e.observer == nil }

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

// parseActionData extracts the operation identifiers and the payload, which
// arrive in two different envelopes depending on how the action was raised.
//
// An on-chain instruction carries an instruction.DataFixed. A direct action
// carries a types.DirectInstruction, whose payload field is "message" and not
// "originalMessage". Parsing one as the other does not fail — encoding/json
// drops unknown fields — it silently yields an EMPTY payload, so the envelope
// has to be chosen from Data.Type rather than guessed at. Reading a direct
// action as DataFixed is what made SET_SECRET over the production /direct path
// receive nothing at all.
func parseActionData(action teetypes.Action) (*instruction.DataFixed, error) {
	if action.Data.Type != teetypes.Direct {
		return processorutils.Parse[instruction.DataFixed](action.Data.Message)
	}

	di, err := processorutils.Parse[teetypes.DirectInstruction](action.Data.Message)
	if err != nil {
		return nil, err
	}

	// A direct action has no on-chain instruction behind it, so the proxy's
	// action id stands in as the correlator bound into the signed report. It
	// must not be left zero: the store records it as observedAtInstructionID,
	// and a zero there would make every direct price indistinguishable.
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

	switch {
	case dataFixed.OPType == teeutils.ToHash(config.OPTypeUsdxOracle):
		return e.processUsdxOracle(action, dataFixed)

	case dataFixed.OPType == teeutils.ToHash(config.OPTypeUsdxConfig):
		return e.processUsdxConfig(action, dataFixed)

	default:
		return http.StatusNotImplemented, []byte(fmt.Sprintf(
			"unsupported op type: received %s, expected %s (%s)",
			dataFixed.OPType.Hex(), teeutils.ToHash(config.OPTypeUsdxOracle).Hex(), config.OPTypeUsdxOracle,
		))
	}
}

func (e *Extension) processUsdxOracle(action teetypes.Action, df *instruction.DataFixed) (int, []byte) {
	switch {
	case df.OPCommand == teeutils.ToHash(config.OPCommandGetPrice):
		// Valid over BOTH channels, unlike the USDX_CONFIG commands, and the
		// asymmetry is deliberate. Those two are authorised by the channel
		// itself — the sender's owner-only check, or M-of-N admin signatures —
		// so the channel is load-bearing. GET_PRICE is authorised by nothing,
		// because it grants nothing: it signs the observation the enclave
		// already made on its own timer, and UsdxPriceStore verifies that
		// signature against the machine registry however it was requested.
		//
		// So the instruction channel is the permissionless path (anyone can pay
		// the registry fee) and the direct channel is the operator's cheap one
		// (no fee, no transaction). The worst a direct caller with the proxy's
		// API key can do is ask too often — an availability cost, not a
		// correctness one. Listed explicitly rather than left open so a future
		// action type cannot quietly acquire a signed price.
		if action.Data.Type != teetypes.Instruction && action.Data.Type != teetypes.Direct {
			return respond(e, action, df, failure.WrongActionType)
		}

		ar := e.processGetPrice(action, df)
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

	default:
		return http.StatusNotImplemented, []byte(fmt.Sprintf(
			"unsupported op command: received %s, expected %s (%s)",
			df.OPCommand.Hex(),
			teeutils.ToHash(config.OPCommandGetPrice).Hex(), config.OPCommandGetPrice,
		))
	}
}

// processGetPrice signs the latest observation and returns the payload. Fails
// closed: never guesses, never falls back to a cached price. A frozen feed with
// an ageing timestamp is the designed failure mode.
func (e *Extension) processGetPrice(action teetypes.Action, df *instruction.DataFixed) teetypes.ActionResult {
	// Require exactly the pinned one-byte envelope, so an unrecognised version
	// is refused rather than guessed at. Neither the length nor the content is
	// reported back: ActionResult.Log is public, and echoing attacker-supplied
	// dimensions is free reconnaissance.
	if len(df.OriginalMessage) != 1 || df.OriginalMessage[0] != config.GetPriceRequestV1 {
		return e.fail(action, df, failure.UnexpectedPayload)
	}

	if e.observer == nil {
		return e.stubPrice(action, df)
	}

	obs, err := e.observer.Report(context.Background(), df.InstructionID)

	// Health is reported either way: on failure it is what tells an operator
	// WHICH source broke.
	e.mu.Lock()
	e.sourceHealth = obs.Health
	e.mu.Unlock()

	if err != nil {
		return e.fail(action, df, asFailure(err))
	}

	e.mu.Lock()
	e.lastPrice = obs.Price.String()
	e.lastDecimals = obs.Decimals
	e.lastObservedAt = obs.ObservedAt
	e.updateCount++
	e.lastErrorClass = failure.ClassNone
	e.mu.Unlock()

	return buildResult(action, df, obs.Data, 1, nil)
}

// processGetReadiness answers with the machine's own account of itself, signed.
//
// Unlike processGetPrice this never fails closed: an unprovisioned or stale
// machine returns a report saying so, because "nothing is configured" is the
// answer rather than a failure to produce one. Only an encoding or signing
// fault is an error here, and that is a machine broken in a way no readiness
// report could describe anyway.
//
// It deliberately does not touch the GET /state counters. Those describe
// prices served, and a status query is not one — incrementing them would make
// polling look like production.
func (e *Extension) processGetReadiness(
	action teetypes.Action, df *instruction.DataFixed,
) teetypes.ActionResult {
	// Same strictness as GET_PRICE: the pinned envelope or nothing, and neither
	// the length nor the content is echoed back into the public log.
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
		return e.fail(action, df, asFailure(err))
	}

	return buildResult(action, df, data, 1, nil)
}

// stubPrice returns an unsigned $1.00 so the wire contract can be exercised
// without credentials. Only reachable when no observer is configured.
func (e *Extension) stubPrice(action teetypes.Action, df *instruction.DataFixed) teetypes.ActionResult {
	price := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(config.PriceDecimals)), nil)

	resp := types.GetPriceResponse{
		Price:      price.String(),
		Decimals:   config.PriceDecimals,
		ObservedAt: df.Timestamp,
	}

	// Cannot fail for these field types, but handled anyway and mapped to the
	// vocabulary: the uniform rule is that no raw error ever reaches Log.
	data, err := json.Marshal(resp)
	if err != nil {
		logger.Errorf("encoding GET_PRICE response: %v", err)
		return e.fail(action, df, failure.Internal)
	}

	e.mu.Lock()
	e.lastPrice = resp.Price
	e.lastDecimals = resp.Decimals
	e.lastObservedAt = resp.ObservedAt
	e.updateCount++
	e.lastErrorClass = failure.ClassNone
	e.mu.Unlock()

	return buildResult(action, df, data, 1, nil)
}

// asFailure keeps anything outside the vocabulary out of the public Log.
func asFailure(err error) failure.Failure {
	var f failure.Failure
	if errors.As(err, &f) {
		return f
	}
	return failure.Internal
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
