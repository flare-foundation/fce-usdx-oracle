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

// Observer returns a signed price report for one instruction, built from the
// most recent background observation.
//
// It is Report, not Observe, because the observation does not happen here: a
// complete one takes longer than the 2 seconds tee-node allows an extension to
// answer an action, so it runs on a timer in internal/observe and this call
// costs one signing round trip. The extension depends on the interface rather
// than on that package directly, so the handler's own tests need no RPC
// endpoints, no credentials and no signing service.
type Observer interface {
	Report(ctx context.Context, instructionID common.Hash) (oracle.Observation, error)
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

// NewWithObserver builds an Extension backed by a live observer.
//
// A nil observer panics, and that is deliberate. The scaffold's New(port,
// signPort) was first removed because it read as the ordinary way to build an
// extension while silently producing a stub — which is how the production
// entrypoint ended up serving a hard-coded price (findings doc §12). The next
// defence logged a banner at startup, which is useless inside a TEE: the logs
// cannot be read from outside, and a feed frozen at $1.00 looks exactly like a
// healthy fully-backed one. So there is no way left to build a stub by
// accident — the only stub path is NewStub, named at its single call site.
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

// NewStub builds a wire-contract conformance extension that answers GET_PRICE
// with an unsigned $1.00. It exists because scripts/test-conformance.sh
// exercises the extension alone — no tee-node, no chain, no credentials — and
// a real observation needs all of those.
//
// It must never be called outside that harness. Every production constructor
// panics on a nil observer precisely so this named path is the only way in.
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
// With no observer the extension is in stub mode and returns $1.00 unsigned.
// That path is reachable only through NewStub, which the wire-contract
// conformance server calls by name; the production constructors panic on a nil
// observer rather than ever reaching here.
func (e *Extension) processGetPrice(action teetypes.Action, df *instruction.DataFixed) teetypes.ActionResult {
	// requestPriceUpdate() sends a fixed one-byte request envelope: everything
	// the observation needs is pinned in extension config, so there is nothing
	// to decode. It is not empty only because the registry rejects an empty
	// message outright (MessageEmpty), found the first time the instruction path
	// ran on Coston2 — see config.GetPriceRequestV1.
	//
	// Require exactly that envelope rather than ignoring whatever arrives, so a
	// caller cannot come to depend on a field the extension silently drops, and
	// an unrecognised version is refused rather than guessed at.
	//
	// Note neither the length nor the content is reported: ActionResult.Log is
	// public, and echoing attacker-supplied dimensions back out is free
	// reconnaissance.
	if len(df.OriginalMessage) != 1 || df.OriginalMessage[0] != config.GetPriceRequestV1 {
		return e.fail(action, df, failure.UnexpectedPayload)
	}

	if e.observer == nil {
		return e.stubPrice(action, df)
	}

	obs, err := e.observer.Report(context.Background(), df.InstructionID)

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
