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

// Observer performs one price observation. The extension depends on this
// interface rather than on package oracle directly, so the handler's own tests
// need no RPC endpoints, no credentials and no signing service.
type Observer interface {
	Observe(ctx context.Context, instructionID common.Hash) (oracle.Observation, error)
}

// Provisioner applies verified configuration. The extension depends on this
// interface so its tests need no signing service and no live endpoints.
type Provisioner interface {
	// ApplyEndpoints installs an endpoint configuration from an on-chain
	// instruction.
	ApplyEndpoints(payload []byte) error

	// ApplySecret verifies a signed envelope and installs the credential it
	// carries. The envelope's ciphertext is decrypted in-enclave.
	ApplySecret(ctx context.Context, message []byte) error

	// Readiness reports what provisioning is still outstanding.
	Readiness() types.Readiness
}

type Extension struct {
	mu     sync.RWMutex
	Server *http.Server

	// signPort is where tee-node exposes POST /sign and POST /decrypt.
	signPort int

	// observer is nil in stub mode, when no oracle configuration was supplied.
	observer Observer

	// provisioner is nil in stub mode. When set, USDX_CONFIG is routed to it.
	provisioner Provisioner

	lastPrice      string
	lastDecimals   int8
	lastObservedAt uint64
	updateCount    int
	failureCount   int
	lastErrorClass failure.Class
	sourceHealth   map[string]string
}

// NewWithObserver builds an Extension backed by a live observer. Passing nil
// leaves it in stub mode: it answers GET_PRICE with an unsigned $1.00, which is
// enough to exercise the wire contract without credentials.
//
// There is deliberately no New(observerless) convenience constructor. The
// scaffold's New(port, signPort) was removed because it read as the ordinary
// way to build an extension while silently producing a stub — which is how the
// production entrypoint ended up serving a hard-coded price (findings doc §12).
// Reaching stub mode now requires writing nil at the call site.
//
// Stub mode announces itself loudly and repeatedly. It is a hard-coded price
// masquerading as an oracle — the one failure that produces no errors, no
// alerts and no stale timestamp, because a frozen-at-par feed looks exactly like
// a healthy fully-backed one. It must never reach a live network unnoticed.
func NewWithObserver(extensionPort, signPort int, observer Observer) *Extension {
	return NewWithProvisioner(extensionPort, signPort, observer, nil)
}

// NewWithProvisioner builds a fully wired Extension.
func NewWithProvisioner(
	extensionPort, signPort int, observer Observer, provisioner Provisioner,
) *Extension {
	if observer == nil {
		logger.Errorf("=====================================================================")
		logger.Errorf("STUB MODE: no observer configured. GET_PRICE will return a hard-coded")
		logger.Errorf("$1.00 that is NOT signed and NOT derived from any supply or reserve")
		logger.Errorf("data. This is for wire-contract testing only and must never serve a")
		logger.Errorf("live feed. Build an oracle.Runner and pass it to NewWithObserver.")
		logger.Errorf("=====================================================================")
	}

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

// StubMode reports whether this extension is serving a hard-coded price rather
// than a real observation. Callers should surface it in health checks: it is
// otherwise indistinguishable from a healthy, fully-backed feed.
func (e *Extension) StubMode() bool { return e.observer == nil }

// stateHandler() structure is boilerplate but update the State field mapping to match your Extension fields.
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

func (e *Extension) processAction(action teetypes.Action) (int, []byte) {
	dataFixed, err := processorutils.Parse[instruction.DataFixed](action.Data.Message)
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

// processUsdxOracle routes USDX_ORACLE instructions by OPCommand.
func (e *Extension) processUsdxOracle(action teetypes.Action, df *instruction.DataFixed) (int, []byte) {
	switch {
	case df.OPCommand == teeutils.ToHash(config.OPCommandGetPrice):
		ar := e.processGetPrice(action, df)
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

// processGetPrice handles GET_PRICE instructions: observes the USDX backing
// ratio, signs the result, and returns the payload.
//
// Fail closed. If a source is down, a value is implausible, or the observation
// window is exceeded, this returns status 0 and publishes nothing. It never
// guesses and never falls back to a cached price — a frozen feed with an ageing
// timestamp is the designed failure mode (spec §7.15), and consumers are
// required to check the timestamp.
//
// With no observer configured the extension runs in stub mode, returning $1.00
// unsigned. That keeps the container startable for wire-contract conformance
// without credentials or RPC access; it is never the production path, because
// New() only leaves observer nil when no configuration was supplied.
func (e *Extension) processGetPrice(action teetypes.Action, df *instruction.DataFixed) teetypes.ActionResult {
	// requestPriceUpdate() sends an empty message: everything the observation
	// needs is pinned in extension config, so there is nothing to decode. Reject
	// a non-empty payload rather than ignoring it, so a caller cannot come to
	// depend on a field the extension silently drops.
	//
	// Note the length is deliberately NOT reported: ActionResult.Log is public,
	// and echoing attacker-supplied dimensions back out is free reconnaissance.
	if len(df.OriginalMessage) != 0 {
		return e.fail(action, df, failure.UnexpectedPayload)
	}

	if e.observer == nil {
		return e.stubPrice(action, df)
	}

	obs, err := e.observer.Observe(context.Background(), df.InstructionID)

	// Health is reported whether or not the observation succeeded: on failure it
	// is what tells an operator WHICH source broke.
	e.mu.Lock()
	e.sourceHealth = obs.Health
	e.mu.Unlock()

	if err != nil {
		// err is always a failure.Failure from the observation path. asFailure
		// is belt and braces: anything else becomes a generic internal error
		// rather than reaching the public Log.
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

// stubPrice returns an unsigned $1.00 so the wire contract can be exercised
// without credentials. Only reachable when no observer is configured.
func (e *Extension) stubPrice(action teetypes.Action, df *instruction.DataFixed) teetypes.ActionResult {
	price := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(config.PriceDecimals)), nil)

	resp := types.GetPriceResponse{
		Price:      price.String(),
		Decimals:   config.PriceDecimals,
		ObservedAt: df.Timestamp,
	}

	// GetPriceResponse is string/int8/uint64 only, so encoding/json cannot fail
	// here — it errors only on unsupported types or cycles. Handled anyway, and
	// mapped to the vocabulary rather than wrapped: the uniform rule is that no
	// raw error ever reaches Log, which is easier to audit than a per-site
	// judgement about which errors happen to be safe today.
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

// asFailure passes a Failure through unchanged and replaces anything else with
// a generic internal error. Nothing that is not already vocabulary reaches Log.
func asFailure(err error) failure.Failure {
	var f failure.Failure
	if errors.As(err, &f) {
		return f
	}
	return failure.Internal
}

// fail records a failed observation and returns a status-0 result.
//
// It takes a failure.Failure, not an error, and that is the safety property:
// ActionResult.Log and GET /state are both world-readable, and a Failure's text
// is a compile-time constant from a closed vocabulary. A raw error cannot be
// passed here even by accident — see internal/failure for why that matters.
func (e *Extension) fail(
	action teetypes.Action, df *instruction.DataFixed, f failure.Failure,
) teetypes.ActionResult {
	e.mu.Lock()
	e.failureCount++
	e.lastErrorClass = f.Class()
	e.mu.Unlock()

	return buildResult(action, df, nil, 0, f)
}
