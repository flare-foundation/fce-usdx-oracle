package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/observation"
	"fce-usdx-oracle/pkg/types"
	"fce-usdx-oracle/pkg/wire/feedupdate"

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
// listener.
// feedRequest is the GET_FEED instruction message for the feed this build
// serves — abi.encode(FeedUpdateRequest{feedId}).
func feedRequest(t *testing.T) []byte {
	t.Helper()
	return encodeFeedRequest(t, config.FeedID)
}

func encodeFeedRequest(t *testing.T, feedID [21]byte) []byte {
	t.Helper()
	// The zero id has no valid encoding through report, and is wanted verbatim
	// by one of the rejection cases.
	if feedID == ([21]byte{}) {
		return make([]byte, feedupdate.RequestLength)
	}
	message, err := feedupdate.EncodeFeedRequest(feedID)
	if err != nil {
		t.Fatalf("EncodeFeedRequest: %v", err)
	}
	return message
}

// newTestExtension builds an extension answering with one fixed observation, so
// a test that is not about the observer does not have to set one up.
func newTestExtension() *Extension {
	return &Extension{
		observer: &fakeObserver{obs: observation.Observation{
			Value:      100_000_000,
			Decimals:   config.PriceDecimals,
			ObservedAt: testTimestamp,
			Data:       []byte{0xAB, 0xCD},
			Health:     map[string]string{},
		}},
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
	msg := mustJSON(df)

	return teetypes.Action{
		Data: teetypes.ActionData{
			// The same id as the payload's: tee-node builds an instruction action
			// with Data.ID set to the instruction id, and processAction refuses a
			// pair that disagrees.
			ID:            testInstructionID,
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
	raw := mustJSON(di)

	return teetypes.Action{
		Data: teetypes.ActionData{
			ID:            testDirectActionID,
			Type:          teetypes.Direct,
			SubmissionTag: "submit",
			Message:       raw,
		},
	}
}

// getFeedAction builds a well-formed GET_FEED action carrying the encoded
// FeedUpdateRequest for this build's feed, exactly as requestFeedUpdate() emits it.
func getFeedAction(t *testing.T) teetypes.Action {
	t.Helper()
	return buildTestAction(config.OPTypeTeeOracle, config.OPCommandGetFeed,
		feedRequest(t))
}

// mustJSON encodes a test fixture; the fixtures are this package's own types, so
// a failure is a broken test rather than a case to report.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// mustResult unwraps an ActionResult from a handler body, failing the test if it
// does not decode or the HTTP status is not 200: every action, accepted or
// refused, is answered with a result envelope.
func mustResult(t *testing.T, status int, body []byte) teetypes.ActionResult {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("expected HTTP status %d, got %d: %s", http.StatusOK, status, body)
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
	action := buildTestAction(toHash("UNKNOWN_TYPE"), config.OPCommandGetFeed, nil)

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
	if expectedHash := config.OPTypeTeeOracle.Hex(); !contains(bodyStr, expectedHash) {
		t.Errorf("expected body to contain expected hash %s", expectedHash)
	}
	if !contains(bodyStr, config.OpName(config.OPTypeTeeOracle)) {
		t.Errorf("expected body to contain %q", config.OPTypeTeeOracle)
	}
}

func TestProcessAction_UnknownOPCommand(t *testing.T) {
	e := newTestExtension()
	action := buildTestAction(config.OPTypeTeeOracle, toHash("UNKNOWN_COMMAND"), nil)

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
	if cmdHash := config.OPCommandGetFeed.Hex(); !contains(bodyStr, cmdHash) {
		t.Errorf("expected body to contain hash for %s: %s", config.OPCommandGetFeed, cmdHash)
	}
	if !contains(bodyStr, config.OpName(config.OPCommandGetFeed)) {
		t.Errorf("expected body to contain command name %q", config.OPCommandGetFeed)
	}
}

// --- GET_FEED ---

// GET_FEED carries exactly the ABI-encoded FeedUpdateRequest naming the feed.
// A caller that supplies anything else must be rejected rather than silently
// ignored, so nobody builds on a field the extension drops.
func TestProcessGetFeed_RejectsPayload(t *testing.T) {
	e := newTestExtension()

	// A sentinel that cannot collide with the vocabulary's own wording, so a hit
	// below is unambiguously an echo of caller-supplied bytes.
	const sentinel = "Zq7CanaryTokenXyz"
	payload := []byte(`{"` + sentinel + `":"` + sentinel + `"}`)

	action := buildTestAction(
		config.OPTypeTeeOracle,
		config.OPCommandGetFeed,
		payload,
	)

	status, body := e.processAction(action)
	result := mustResult(t, status, body)

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
}

// The request envelope is exactly one byte and exactly this value; anything else is
// refused rather than ignored. Empty is included deliberately: the registry rejects
// it first, but the handler must not rely on that — direct is not the registry.
func TestProcessGetFeed_RequiresTheRequestEnvelope(t *testing.T) {
	// A request for a DIFFERENT feed of the same extension: well-formed, and
	// refused. One sender serves every feed now, so this is the case the old
	// one-byte envelope could not even express.
	otherFeed := config.FeedID
	otherFeed[20] ^= 0x01

	cases := []struct {
		name    string
		payload []byte
		accept  bool
	}{
		{"this feed", feedRequest(t), true},
		{"empty", nil, false},
		{"the old one-byte envelope", []byte{0x01}, false},
		{"another feed on this extension", encodeFeedRequest(t, otherFeed), false},
		{"the zero feed id", encodeFeedRequest(t, [21]byte{}), false},
		{"a short word", feedRequest(t)[:31], false},
		{"a trailing word", append(feedRequest(t), make([]byte, 32)...), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestExtension()
			action := buildTestAction(
				config.OPTypeTeeOracle, config.OPCommandGetFeed, c.payload)

			status, body := e.processAction(action)
			result := mustResult(t, status, body)

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
			Type:    teetypes.Instruction,
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

// An action whose type is neither of the two known ones must be refused, not
// treated as an instruction. The instruction router is the privileged one — it
// is where SET_ENDPOINTS and SET_ADMINS live, authorised by nothing but having
// arrived on that channel — so an unrecognised type falling through to it would
// hand governance writes to whatever produced it.
// An action whose envelope and payload name different instructions is refused.
// tee-node sets Data.ID to the instruction id, so a mismatch is not something
// the honest path produces, and reconciling it would mean choosing which of two
// disagreeing sources to believe.
func TestProcessAction_ActionIDMustMatchInstructionID(t *testing.T) {
	e := newTestExtension()

	action := getFeedAction(t)
	action.Data.ID = common.HexToHash("0xdead")

	status, body := e.processAction(action)
	if status != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, status, body)
	}
	if !contains(string(body), "does not match the instruction id") {
		t.Errorf("expected the body to name the cause, got %q", string(body))
	}
}

func TestProcessAction_UnknownActionTypeIsRefused(t *testing.T) {
	e := newTestExtension()

	action := teetypes.Action{
		Data: teetypes.ActionData{
			ID:      common.HexToHash("0xabcd"),
			Type:    teetypes.ActionType("neither"),
			Message: []byte(`{}`),
		},
	}

	status, body := e.processAction(action)
	if status != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, status, body)
	}
	if !contains(string(body), "unknown action type") {
		t.Errorf("expected the body to name the cause, got %q", string(body))
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// --- observer path ---

// fakeObserver stands in for the oracle so the handler can be tested without
// RPC endpoints, credentials or a signing service.
type fakeObserver struct {
	obs observation.Observation
	err error

	// readiness is returned verbatim as the signed payload would be; the
	// handler's job is routing and envelope checking, not encoding, which
	// pkg/wire/readiness covers on its own.
	readiness    []byte
	readinessErr error

	mu    sync.Mutex
	calls int

	// Written by ReadinessReport only. The feed path no longer takes an
	// instruction id — the store dropped the field — so nothing request-specific
	// reaches the observer there. See readiness_test.go for the binding that
	// remains.
	lastID           common.Hash
	readinessCalls   int
	lastProvisioning types.Readiness

	// The requesting block's timestamp, as forwarded by processGetFeed.
	lastRequestedAt uint64
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

func (f *fakeObserver) Report(ctx context.Context, requestedAt uint64) (observation.Observation, error) {
	f.mu.Lock()
	f.calls++
	f.lastRequestedAt = requestedAt
	f.mu.Unlock()
	return f.obs, f.err
}

func TestProcessGetFeed_UsesObserverResult(t *testing.T) {
	obs := observation.Observation{
		Value:      95_374_751,
		Decimals:   8,
		ObservedAt: 1_700_000_123,
		Data:       []byte{0xAB, 0xCD},
		Health:     map[string]string{"hex_bonds": config.HealthOK},
	}
	o := &fakeObserver{obs: obs}
	e := newTestExtension()
	e.observer = o

	status, body := e.processAction(getFeedAction(t))
	result := mustResult(t, status, body)

	if result.Status != 1 {
		t.Fatalf("expected status 1, got %d: %s", result.Status, result.Log)
	}
	// The payload must be the observer's signed bytes, verbatim.
	if !bytes.Equal(result.Data, obs.Data) {
		t.Errorf("data = %x, want %x", result.Data, obs.Data)
	}
}

// A failed observation must publish nothing and surface only the vocabulary.
func TestProcessGetFeed_ObserverFailureFailsClosed(t *testing.T) {
	o := &fakeObserver{
		err: failure.BackingUnavailable,
		obs: observation.Observation{Health: map[string]string{"hex_bonds": config.HealthDown}},
	}
	e := newTestExtension()
	e.observer = o

	status, body := e.processAction(getFeedAction(t))
	result := mustResult(t, status, body)

	if result.Status != 0 {
		t.Fatalf("expected status 0, got %d", result.Status)
	}
	if len(result.Data) != 0 {
		t.Error("a failed observation must publish no data")
	}
	if result.Log != "error: "+failure.BackingUnavailable.Error() {
		t.Errorf("log = %q", result.Log)
	}
}

// A non-vocabulary error must not reach the public Log.
func TestProcessGetFeed_ForeignErrorIsNotPublished(t *testing.T) {
	o := &fakeObserver{err: errors.New("dial tcp 10.0.0.1:8545?apikey=SECRET: refused")}
	e := newTestExtension()
	e.observer = o

	status, body := e.processAction(getFeedAction(t))
	result := mustResult(t, status, body)

	if strings.Contains(string(body), "SECRET") || strings.Contains(string(body), "10.0.0.1") {
		t.Errorf("a raw error reached the public result: %s", body)
	}
	if result.Log != "error: "+failure.Internal.Error() {
		t.Errorf("log = %q, want the generic internal reason", result.Log)
	}
}

// A nil observer must panic rather than build something that answers anyway. A
// hard-coded par price produces no errors, no alerts and no stale timestamp, so
// nothing downstream would notice one.
func TestNilObserverPanics(t *testing.T) {
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: a nil observer must panic, not build a stub", name)
			}
		}()
		f()
	}
	mustPanic("nil observer", func() { NewExtension(0, 0, nil, nil) })
	mustPanic("nil observer, live provisioner", func() {
		NewExtension(0, 0, nil, &fakeProvisioner{})
	})
}

// The observer's staleness bound is only as good as the clock it is measured
// against, and the only trustworthy one available is the requesting block's.
// Dropping it on the floor here would leave the machine checking itself.
func TestProcessGetFeed_ForwardsTheRequestingBlockTimestamp(t *testing.T) {
	o := &fakeObserver{obs: observation.Observation{
		Value: 100_000_000, Decimals: 8, ObservedAt: testTimestamp, Data: []byte{0x01},
	}}
	e := newTestExtension()
	e.observer = o

	status, body := e.processAction(getFeedAction(t))
	mustResult(t, status, body)

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lastRequestedAt != testTimestamp {
		t.Errorf("requestedAt = %d, want the instruction's block timestamp %d",
			o.lastRequestedAt, testTimestamp)
	}
}

// A clock refusal is ordinary vocabulary: it reaches the caller as a status-0
// result with a publishable reason, and is counted as a failure like any other.
func TestProcessGetFeed_ClockRefusalIsPublishable(t *testing.T) {
	o := &fakeObserver{err: failure.ClockBehindChain}
	e := newTestExtension()
	e.observer = o

	status, body := e.processAction(getFeedAction(t))
	result := mustResult(t, status, body)

	if result.Status != 0 {
		t.Fatalf("expected status 0, got %d", result.Status)
	}
	if result.Log != "error: "+failure.ClockBehindChain.Error() {
		t.Errorf("log = %q, want %q", result.Log, failure.ClockBehindChain.Error())
	}
}
