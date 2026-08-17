package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/oracle"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
)

const testTimestamp uint64 = 1700000000

var testInstructionID = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

// toHash mirrors teeutils.ToHash for clarity: left-pads a string into a 32-byte hash.
func toHash(s string) common.Hash { return teeutils.ToHash(s) }

// newTestExtension builds an Extension the way New() does, without binding a
// listener. Constructing &Extension{} directly leaves sourceHealth nil, which
// hides map-initialisation bugs that only surface via GET /state.
func newTestExtension() *Extension {
	return &Extension{
		lastErrorClass: failure.ClassNone,
		sourceHealth:   map[string]string{},
	}
}

// buildTestAction constructs a teetypes.Action whose Data.Message is the
// JSON-encoded DataFixed payload. This is what processAction expects to parse.
func buildTestAction(opType, opCommand common.Hash, originalMessage []byte) teetypes.Action {
	// DataFixed is the structure that processorutils.Parse extracts from Data.Message.
	type dataFixed struct {
		InstructionID      common.Hash    `json:"instructionId"`
		TeeID              common.Address `json:"teeId"`
		Timestamp          uint64         `json:"timestamp"`
		RewardEpochID      uint32         `json:"rewardEpochId"`
		OPType             common.Hash    `json:"opType"`
		OPCommand          common.Hash    `json:"opCommand"`
		Cosigners          []string       `json:"cosigners"`
		CosignersThreshold uint64         `json:"cosignersThreshold"`
		OriginalMessage    hexutil.Bytes  `json:"originalMessage"`
	}

	df := dataFixed{
		// The instruction id is bound into the signed report, so it must be
		// present here or the observer test asserts nothing.
		InstructionID:   testInstructionID,
		Timestamp:       testTimestamp,
		OPType:          opType,
		OPCommand:       opCommand,
		OriginalMessage: originalMessage,
	}
	msg, _ := json.Marshal(df)

	return teetypes.Action{
		Data: teetypes.ActionData{
			ID:            common.HexToHash("0x1234"),
			Type:          teetypes.Instruction,
			SubmissionTag: "submit",
			Message:       msg,
		},
	}
}

// testDirectActionID is the proxy-generated action id for a direct action. It
// stands in for the instruction id, which a direct action does not have.
var testDirectActionID = common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

// buildDirectAction constructs the envelope the PROXY actually delivers: Data.Message
// is a JSON DirectInstruction, whose payload field is "message". Deliberately not a
// DataFixed — building it that way hid a real direct action arriving empty.
func buildDirectAction(opType, opCommand common.Hash, message []byte) teetypes.Action {
	di := teetypes.DirectInstruction{
		OPType:    opType,
		OPCommand: opCommand,
		Message:   message,
	}
	raw, _ := json.Marshal(di)

	return teetypes.Action{
		Data: teetypes.ActionData{
			ID:            testDirectActionID,
			Type:          teetypes.Direct,
			SubmissionTag: "submit",
			Message:       raw,
		},
	}
}

// getPriceAction builds a well-formed GET_PRICE action carrying the fixed
// request envelope, exactly as requestPriceUpdate() emits it.
func getPriceAction() teetypes.Action {
	return buildTestAction(toHash(config.OPTypeUsdxOracle), toHash(config.OPCommandGetPrice),
		[]byte{config.GetPriceRequestV1})
}

// mustResult unwraps an ActionResult from a handler body, failing the test if it
// does not decode or the HTTP status is unexpected.
func mustResult(t *testing.T, status int, body []byte, wantStatus int) teetypes.ActionResult {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("expected HTTP status %d, got %d: %s", wantStatus, status, body)
	}
	var result teetypes.ActionResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to unmarshal ActionResult: %v", err)
	}
	return result
}

// --- Dispatch: OPType/OPCommand debug info ---

func TestProcessAction_UnknownOPType(t *testing.T) {
	e := newTestExtension()
	action := buildTestAction(toHash("UNKNOWN_TYPE"), toHash(config.OPCommandGetPrice), nil)

	status, body := e.processAction(action)

	if status != http.StatusNotImplemented {
		t.Fatalf("expected status %d, got %d", http.StatusNotImplemented, status)
	}

	bodyStr := string(body)
	if !contains(bodyStr, "unsupported op type") {
		t.Error("expected body to contain 'unsupported op type'")
	}
	if receivedHash := toHash("UNKNOWN_TYPE").Hex(); !contains(bodyStr, receivedHash) {
		t.Errorf("expected body to contain received hash %s", receivedHash)
	}
	if expectedHash := toHash(config.OPTypeUsdxOracle).Hex(); !contains(bodyStr, expectedHash) {
		t.Errorf("expected body to contain expected hash %s", expectedHash)
	}
	if !contains(bodyStr, config.OPTypeUsdxOracle) {
		t.Errorf("expected body to contain %q", config.OPTypeUsdxOracle)
	}
}

func TestProcessAction_UnknownOPCommand(t *testing.T) {
	e := newTestExtension()
	action := buildTestAction(toHash(config.OPTypeUsdxOracle), toHash("UNKNOWN_COMMAND"), nil)

	status, body := e.processAction(action)

	if status != http.StatusNotImplemented {
		t.Fatalf("expected status %d, got %d", http.StatusNotImplemented, status)
	}

	bodyStr := string(body)
	if !contains(bodyStr, "unsupported op command") {
		t.Error("expected body to contain 'unsupported op command'")
	}
	if receivedHash := toHash("UNKNOWN_COMMAND").Hex(); !contains(bodyStr, receivedHash) {
		t.Errorf("expected body to contain received hash %s", receivedHash)
	}
	if cmdHash := toHash(config.OPCommandGetPrice).Hex(); !contains(bodyStr, cmdHash) {
		t.Errorf("expected body to contain hash for %s: %s", config.OPCommandGetPrice, cmdHash)
	}
	if !contains(bodyStr, config.OPCommandGetPrice) {
		t.Errorf("expected body to contain command name %q", config.OPCommandGetPrice)
	}
}

// --- GET_PRICE ---

func TestProcessGetPrice_Success(t *testing.T) {
	e := newTestExtension()

	status, body := e.processAction(getPriceAction())
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 1 {
		t.Fatalf("expected ActionResult.Status=1, got %d: %s", result.Status, result.Log)
	}

	var resp types.GetPriceResponse
	if err := json.Unmarshal(result.Data, &resp); err != nil {
		t.Fatalf("failed to unmarshal GetPriceResponse: %v", err)
	}

	// SCAFFOLD: $1.00 at 8 decimals.
	if want := "100000000"; resp.Price != want {
		t.Errorf("expected price %q, got %q", want, resp.Price)
	}
	if resp.Decimals != config.PriceDecimals {
		t.Errorf("expected decimals %d, got %d", config.PriceDecimals, resp.Decimals)
	}
	if resp.ObservedAt != testTimestamp {
		t.Errorf("expected observedAt %d, got %d", testTimestamp, resp.ObservedAt)
	}
}

// The price is a JSON string, not a number. On-chain it is a uint256, which does
// not survive a float64 round-trip, so the encoding has to be a string from the
// start rather than being widened later.
func TestProcessGetPrice_PriceIsJSONString(t *testing.T) {
	e := newTestExtension()

	status, body := e.processAction(getPriceAction())
	result := mustResult(t, status, body, http.StatusOK)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(result.Data, &raw); err != nil {
		t.Fatalf("failed to unmarshal data: %v", err)
	}
	if got := string(raw["price"]); !strings.HasPrefix(got, `"`) {
		t.Errorf("expected price to be JSON-encoded as a string, got %s", got)
	}
}

// requestPriceUpdate() sends no payload. A caller that supplies one must be
// rejected rather than silently ignored, so nobody builds on a field the
// extension drops.
func TestProcessGetPrice_RejectsPayload(t *testing.T) {
	e := newTestExtension()

	// A sentinel that cannot collide with the vocabulary's own wording, so a hit
	// below is unambiguously an echo of caller-supplied bytes.
	const sentinel = "Zq7CanaryTokenXyz"
	payload := []byte(`{"` + sentinel + `":"` + sentinel + `"}`)

	action := buildTestAction(
		toHash(config.OPTypeUsdxOracle),
		toHash(config.OPCommandGetPrice),
		payload,
	)

	status, body := e.processAction(action)
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 0 {
		t.Fatalf("expected ActionResult.Status=0 for an unexpected payload, got %d", result.Status)
	}
	if result.Log != "error: "+failure.UnexpectedPayload.Error() {
		t.Errorf("expected the vocabulary reason, got %q", result.Log)
	}

	// ActionResult.Log is public (contract §4.6) and must echo back neither the
	// submitted bytes nor their length. The whole result is checked, not just Log,
	// since data must be empty on a failure too.
	whole := string(body)
	if strings.Contains(whole, sentinel) {
		t.Errorf("result echoes caller-supplied bytes: %s", whole)
	}
	if strings.Contains(result.Log, strconv.Itoa(len(payload))) {
		t.Errorf("log %q discloses the payload length", result.Log)
	}

	if e.lastErrorClass != failure.ClassSourceInvalid {
		t.Errorf("expected error class %q, got %q", failure.ClassSourceInvalid, e.lastErrorClass)
	}
	if e.failureCount != 1 {
		t.Errorf("expected failureCount=1, got %d", e.failureCount)
	}
}

// The request envelope is exactly one byte and exactly this value; anything else is
// refused rather than ignored. Empty is included deliberately: the registry rejects
// it first, but the handler must not rely on that — direct is not the registry.
func TestProcessGetPrice_RequiresTheRequestEnvelope(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		accept  bool
	}{
		{"the v1 envelope", []byte{config.GetPriceRequestV1}, true},
		{"empty", nil, false},
		{"a future version", []byte{0x02}, false},
		{"the right byte, twice", []byte{config.GetPriceRequestV1, config.GetPriceRequestV1}, false},
		{"zero byte", []byte{0x00}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestExtension()
			action := buildTestAction(
				toHash(config.OPTypeUsdxOracle), toHash(config.OPCommandGetPrice), c.payload)

			status, body := e.processAction(action)
			result := mustResult(t, status, body, http.StatusOK)

			if c.accept {
				if result.Status != 1 {
					t.Fatalf("expected the envelope to be accepted, got status %d: %s",
						result.Status, result.Log)
				}
				return
			}
			if result.Status != 0 {
				t.Fatalf("expected status 0, got %d", result.Status)
			}
			if result.Log != "error: "+failure.UnexpectedPayload.Error() {
				t.Errorf("expected the vocabulary reason, got %q", result.Log)
			}
		})
	}
}

func TestProcessAction_InvalidDataMessage(t *testing.T) {
	e := newTestExtension()

	// Data.Message is not valid JSON — processorutils.Parse should fail.
	action := teetypes.Action{
		Data: teetypes.ActionData{
			ID:      common.HexToHash("0xabcd"),
			Message: []byte(`not json at all`),
		},
	}

	status, body := e.processAction(action)

	if status != http.StatusBadRequest {
		t.Fatalf("expected status %d for invalid Data.Message, got %d: %s",
			http.StatusBadRequest, status, body)
	}
	if !contains(string(body), "decoding fixed data") {
		t.Errorf("expected body to mention 'decoding fixed data', got %q", string(body))
	}
}

// --- State tracking ---

func TestState_UpdateCountIncrementsAcrossCalls(t *testing.T) {
	e := newTestExtension()

	for i := 1; i <= 3; i++ {
		status, body := e.processAction(getPriceAction())
		result := mustResult(t, status, body, http.StatusOK)
		if result.Status != 1 {
			t.Fatalf("call %d: expected status 1, got %d: %s", i, result.Status, result.Log)
		}
		if e.updateCount != i {
			t.Errorf("call %d: expected updateCount=%d, got %d", i, i, e.updateCount)
		}
	}
}

func TestState_ReflectsLastObservation(t *testing.T) {
	e := newTestExtension()

	if _, body := e.processAction(getPriceAction()); len(body) == 0 {
		t.Fatal("expected a result body")
	}

	if e.lastPrice != "100000000" {
		t.Errorf("expected lastPrice=100000000, got %q", e.lastPrice)
	}
	if e.lastDecimals != config.PriceDecimals {
		t.Errorf("expected lastDecimals=%d, got %d", config.PriceDecimals, e.lastDecimals)
	}
	if e.lastObservedAt != testTimestamp {
		t.Errorf("expected lastObservedAt=%d, got %d", testTimestamp, e.lastObservedAt)
	}
	if e.lastErrorClass != failure.ClassNone {
		t.Errorf("expected no error class, got %q", e.lastErrorClass)
	}
}

// A success after a failure must clear the error class, otherwise /state reports
// a stale failure forever and the monitoring in WP7 fires on a healthy feed.
func TestState_SuccessClearsErrorClass(t *testing.T) {
	e := newTestExtension()

	bad := buildTestAction(
		toHash(config.OPTypeUsdxOracle),
		toHash(config.OPCommandGetPrice),
		[]byte(`{"unexpected":"payload"}`),
	)
	e.processAction(bad)
	if e.lastErrorClass == failure.ClassNone {
		t.Fatal("expected an error class to be recorded")
	}

	e.processAction(getPriceAction())
	if e.lastErrorClass != failure.ClassNone {
		t.Errorf("expected error class cleared after success, got %q", e.lastErrorClass)
	}
	if e.failureCount != 1 {
		t.Errorf("expected failureCount to stay at 1, got %d", e.failureCount)
	}
	if e.updateCount != 1 {
		t.Errorf("expected updateCount=1, got %d", e.updateCount)
	}
}

// GET /state is public and attested (spec §4.1). This asserts the wire shape
// carries only operational fields — no supply, reserves, endpoints or secrets.
func TestState_ExposesOnlyOperationalFields(t *testing.T) {
	e := newTestExtension()
	e.processAction(getPriceAction())

	b, err := json.Marshal(types.State{
		Readiness:      types.Readiness{MissingRefs: []string{}},
		LastPrice:      e.lastPrice,
		LastDecimals:   e.lastDecimals,
		LastObservedAt: e.lastObservedAt,
		UpdateCount:    e.updateCount,
		FailureCount:   e.failureCount,
		LastErrorClass: string(e.lastErrorClass),
		SourceHealth:   e.sourceHealth,
	})
	if err != nil {
		t.Fatalf("marshalling state: %v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshalling state: %v", err)
	}

	allowed := map[string]bool{
		"lastPrice": true, "lastDecimals": true, "lastObservedAt": true,
		"updateCount": true, "failureCount": true, "lastErrorClass": true,
		"sourceHealth": true,
		// readiness names outstanding secret REFS, never their values —
		// TestState_ReadinessNamesRefsNotValues holds that line.
		"readiness": true,
	}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("unexpected field %q in public state — /state is world-readable, "+
				"see spec §4.1 before adding fields here", k)
		}
	}
	for k := range allowed {
		if _, ok := fields[k]; !ok {
			t.Errorf("expected field %q in state", k)
		}
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// --- observer path ---

// fakeObserver stands in for the oracle so the handler can be tested without
// RPC endpoints, credentials or a signing service.
type fakeObserver struct {
	obs oracle.Observation
	err error

	// readiness is returned verbatim as the signed payload would be; the
	// handler's job is routing and envelope checking, not encoding, which
	// internal/readiness covers on its own.
	readiness    []byte
	readinessErr error

	mu               sync.Mutex
	calls            int
	lastID           common.Hash
	readinessCalls   int
	lastProvisioning types.Readiness
}

func (f *fakeObserver) ReadinessReport(
	_ context.Context, instructionID common.Hash, provisioning types.Readiness,
) ([]byte, error) {
	f.mu.Lock()
	f.readinessCalls++
	f.lastID = instructionID
	f.lastProvisioning = provisioning
	f.mu.Unlock()
	if f.readinessErr != nil {
		return nil, f.readinessErr
	}
	if f.readiness == nil {
		return []byte("readiness-payload"), nil
	}
	return f.readiness, nil
}

func (f *fakeObserver) Report(ctx context.Context, instructionID common.Hash) (oracle.Observation, error) {
	f.mu.Lock()
	f.calls++
	f.lastID = instructionID
	f.mu.Unlock()
	return f.obs, f.err
}

func TestProcessGetPrice_UsesObserverResult(t *testing.T) {
	obs := oracle.Observation{
		Price:      big.NewInt(95_374_751),
		Decimals:   8,
		ObservedAt: 1_700_000_123,
		Data:       []byte{0xAB, 0xCD},
		Health:     map[string]string{"hex_bonds": config.HealthOK},
	}
	o := &fakeObserver{obs: obs}
	e := newTestExtension()
	e.observer = o

	status, body := e.processAction(getPriceAction())
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 1 {
		t.Fatalf("expected status 1, got %d: %s", result.Status, result.Log)
	}
	// The payload must be the observer's signed bytes, verbatim.
	if !bytes.Equal(result.Data, obs.Data) {
		t.Errorf("data = %x, want %x", result.Data, obs.Data)
	}
	if e.lastPrice != "95374751" {
		t.Errorf("lastPrice = %q", e.lastPrice)
	}
	if e.lastObservedAt != 1_700_000_123 {
		t.Errorf("lastObservedAt = %d", e.lastObservedAt)
	}
	if e.sourceHealth["hex_bonds"] != config.HealthOK {
		t.Errorf("health not adopted from the observation: %v", e.sourceHealth)
	}
}

// The instruction id must reach the observer, since it is bound into the signed
// report to tie the price to the request that asked for it.
func TestProcessGetPrice_PassesInstructionID(t *testing.T) {
	o := &fakeObserver{obs: oracle.Observation{
		Price: big.NewInt(1), Decimals: 8, ObservedAt: 1, Data: []byte{0x01},
	}}
	e := newTestExtension()
	e.observer = o

	e.processAction(getPriceAction())

	if o.calls != 1 {
		t.Fatalf("expected 1 observation, got %d", o.calls)
	}
	if o.lastID != testInstructionID {
		t.Errorf("observer got instruction id %s, want %s", o.lastID.Hex(), testInstructionID.Hex())
	}
}

// A failed observation must publish nothing and surface only the vocabulary.
func TestProcessGetPrice_ObserverFailureFailsClosed(t *testing.T) {
	o := &fakeObserver{
		err: failure.BackingUnavailable,
		obs: oracle.Observation{Health: map[string]string{"hex_bonds": config.HealthDown}},
	}
	e := newTestExtension()
	e.observer = o

	status, body := e.processAction(getPriceAction())
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 0 {
		t.Fatalf("expected status 0, got %d", result.Status)
	}
	if len(result.Data) != 0 {
		t.Error("a failed observation must publish no data")
	}
	if result.Log != "error: "+failure.BackingUnavailable.Error() {
		t.Errorf("log = %q", result.Log)
	}
	if e.lastErrorClass != failure.ClassSourceDown {
		t.Errorf("lastErrorClass = %q", e.lastErrorClass)
	}
	// Health must still be reported, so an operator can see which source broke.
	if e.sourceHealth["hex_bonds"] != config.HealthDown {
		t.Errorf("health not reported on failure: %v", e.sourceHealth)
	}
}

// A non-vocabulary error must not reach the public Log.
func TestProcessGetPrice_ForeignErrorIsNotPublished(t *testing.T) {
	o := &fakeObserver{err: errors.New("dial tcp 10.0.0.1:8545?apikey=SECRET: refused")}
	e := newTestExtension()
	e.observer = o

	status, body := e.processAction(getPriceAction())
	result := mustResult(t, status, body, http.StatusOK)

	if strings.Contains(string(body), "SECRET") || strings.Contains(string(body), "10.0.0.1") {
		t.Errorf("a raw error reached the public result: %s", body)
	}
	if result.Log != "error: "+failure.Internal.Error() {
		t.Errorf("log = %q, want the generic internal reason", result.Log)
	}
}

// A failed observation must not overwrite the last good price: the feed freezes
// at its last value with an ageing timestamp rather than going blank.
func TestProcessGetPrice_FailureKeepsLastGoodPrice(t *testing.T) {
	o := &fakeObserver{obs: oracle.Observation{
		Price: big.NewInt(100_000_000), Decimals: 8, ObservedAt: 1_700_000_000,
		Data: []byte{0x01}, Health: map[string]string{},
	}}
	e := newTestExtension()
	e.observer = o

	e.processAction(getPriceAction())
	if e.lastPrice != "100000000" {
		t.Fatalf("setup: lastPrice = %q", e.lastPrice)
	}

	o.err = failure.BackingUnavailable
	e.processAction(getPriceAction())

	if e.lastPrice != "100000000" {
		t.Errorf("lastPrice = %q; a failure must not clear the last good price", e.lastPrice)
	}
	if e.lastObservedAt != 1_700_000_000 {
		t.Errorf("lastObservedAt = %d; it must age rather than reset", e.lastObservedAt)
	}
}

// Stub mode must be impossible to enter accidentally: the production constructors
// panic on a nil observer, and NewStub is the only stub path. A hard-coded par price
// produces no errors, no alerts and no stale timestamp — a banner is no defence.
func TestStubMode_CannotBeEnteredAccidentally(t *testing.T) {
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: a nil observer must panic, not build a stub", name)
			}
		}()
		f()
	}
	mustPanic("NewWithObserver", func() { NewWithObserver(0, 0, nil) })
	mustPanic("NewWithProvisioner", func() { NewWithProvisioner(0, 0, nil, nil) })

	stub := NewStub(0, 0)
	if !stub.StubMode() {
		t.Error("StubMode() must report true for a NewStub extension")
	}

	live := NewWithObserver(0, 0, &fakeObserver{})
	if live.StubMode() {
		t.Error("StubMode() must report false once an observer is configured")
	}
}

// The stub's payload is JSON; a live observation's payload is the ABI-encoded
// signed report. They are NOT the same wire format, which is why conformance
// fixtures generated in stub mode do not pin the production payload.
func TestStubMode_PayloadDiffersFromObserverPayload(t *testing.T) {
	stub := newTestExtension() // observer nil
	_, stubBody := stub.processAction(getPriceAction())
	stubResult := mustResult(t, http.StatusOK, stubBody, http.StatusOK)

	// The stub emits JSON, which must parse as GetPriceResponse.
	var resp types.GetPriceResponse
	if err := json.Unmarshal(stubResult.Data, &resp); err != nil {
		t.Fatalf("stub payload is not the JSON shape: %v", err)
	}

	live := newTestExtension()
	live.observer = &fakeObserver{obs: oracle.Observation{
		Price: big.NewInt(1), Decimals: 8, ObservedAt: 1,
		Data: []byte{0xDE, 0xAD}, Health: map[string]string{},
	}}
	_, liveBody := live.processAction(getPriceAction())
	liveResult := mustResult(t, http.StatusOK, liveBody, http.StatusOK)

	if bytes.Equal(stubResult.Data, liveResult.Data) {
		t.Error("stub and observer payloads coincidentally matched; the test proves nothing")
	}
	if err := json.Unmarshal(liveResult.Data, &resp); err == nil {
		t.Error("the observer payload parsed as the stub's JSON shape; " +
			"these are meant to be different formats and the difference must stay visible")
	}
}

// doStateRequest performs GET /state against the handler and returns the body.
func doStateRequest(t *testing.T, e *Extension) []byte {
	t.Helper()
	rec := newRecorder()
	e.stateHandler(rec, nil)
	return rec.body
}

type recorder struct {
	body   []byte
	status int
	hdr    http.Header
}

func newRecorder() *recorder { return &recorder{hdr: http.Header{}} }

func (r *recorder) Header() http.Header         { return r.hdr }
func (r *recorder) Write(b []byte) (int, error) { r.body = append(r.body, b...); return len(b), nil }
func (r *recorder) WriteHeader(s int)           { r.status = s }
