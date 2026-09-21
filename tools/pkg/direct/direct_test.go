package direct

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fce-usdx-oracle/pkg/protocol"

	"github.com/ethereum/go-ethereum/common"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// The readiness op pair and its one-byte request stand in for every direct
// command here: the channel does not care what it carries.
var (
	testOPType    = protocol.OPTypeTeeOracle
	testOPCommand = protocol.OPCommandGetReadiness
	testMessage   = []byte{protocol.ReadinessRequestV1}
)

// The direct wire shape is a DirectInstruction: three fields, payload under
// "message". Sending a DataFixed does not fail — the extension just sees an empty
// payload — so the shape is asserted here rather than in an integration run.
func TestPost_SendsTheDirectInstructionShape(t *testing.T) {
	var (
		gotKey  string
		gotBody map[string]any
		gotPath string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		encodeJSON(t, w, teetypes.Action{
			Data: teetypes.ActionData{
				ID:            common.HexToHash("0xabc"),
				Type:          teetypes.Direct,
				SubmissionTag: teetypes.Submit,
			},
		})
	}))
	defer srv.Close()

	id, err := Post(context.Background(), srv.URL, "the-key", testOPType, testOPCommand, testMessage)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}

	if id != common.HexToHash("0xabc") {
		t.Errorf("action id = %s, want the id the proxy assigned", id.Hex())
	}
	if gotPath != "/direct" {
		t.Errorf("path = %q, want /direct", gotPath)
	}
	if gotKey != "the-key" {
		t.Errorf("X-API-Key = %q, want the configured key", gotKey)
	}

	// Exactly these three keys. An extra "originalMessage" would mean someone
	// reintroduced the DataFixed shape.
	if len(gotBody) != 3 {
		t.Errorf("body has %d fields (%v), want exactly opType, opCommand, message",
			len(gotBody), gotBody)
	}
	if gotBody["message"] != "0x01" {
		t.Errorf("message = %v, want the one-byte request envelope", gotBody["message"])
	}
	if gotBody["opCommand"] != testOPCommand.Hex() {
		t.Errorf("opCommand = %v, want %s", gotBody["opCommand"], testOPCommand.Hex())
	}
}

// A proxy that rejects the key must surface as an authentication failure, not as
// a missing result five minutes later.
func TestPost_ReportsProxyRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := Post(context.Background(), srv.URL, "wrong", testOPType, testOPCommand, testMessage)
	if err == nil {
		t.Fatal("expected an error for a 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, want it to name the status", err)
	}
}

// A 2xx with no action id is not success: there is nothing to poll for, and
// treating it as an id would poll a zero hash until the deadline.
func TestPost_RejectsMissingActionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	if _, err := Post(context.Background(), srv.URL, "k", testOPType, testOPCommand, testMessage); err == nil {
		t.Fatal("expected an error when the proxy returns no action id")
	}
}

// A proxy streaming a body that never ends must be cut off at MaxBody rather
// than decoded into the caller's memory. Asserted on what the server manages to
// write: uncapped, the decoder drains everything offered.
func TestResult_CapsAnEndlessBody(t *testing.T) {
	const ceiling = 64 << 20 // hard stop so the handler always terminates

	var written atomic.Int64
	endless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Valid JSON that is never closed, so nothing but a cap ends the read.
		_, _ = w.Write([]byte(`{"result":{"log":"`))
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for written.Load() < ceiling {
			n, err := w.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer endless.Close()

	if _, _, err := Result(context.Background(), endless.URL,
		common.HexToHash("0x1"), teetypes.Threshold); err == nil {
		t.Fatal("expected a capped body to fail decoding rather than stream without bound")
	}

	// The cap plus whatever the socket buffered, nowhere near the ceiling.
	if got, limit := written.Load(), int64(8*MaxBody); got > limit {
		t.Errorf("proxy wrote %d bytes into the caller, want at most %d", got, limit)
	}
}

// resultServer files a result for id after `after` polls; before that it 404s,
// as the proxy does until tee-node has answered.
func resultServer(t *testing.T, id common.Hash, after int, result teetypes.ActionResult) (*httptest.Server, *int) {
	t.Helper()
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/action/result/"+id.Hex() || r.URL.Query().Get("submissionTag") != string(teetypes.Submit) {
			t.Errorf("unexpected request %s", r.URL)
		}
		polls++
		if polls < after {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		encodeJSON(t, w, teetypes.ActionResponse{Result: result})
	}))
	return srv, &polls
}

// Await returns the machine's own verdict, not the proxy's acceptance: a 404
// until tee-node has answered, then status 1 or 0 with the extension's reason.
// Returning early on the 404 is exactly the race that made an upload report a
// credential missing that landed a moment later.
func TestAwait_ReturnsTheMachinesVerdict(t *testing.T) {
	initialBackoff = 2 * time.Millisecond
	t.Cleanup(func() { initialBackoff = time.Second })
	id := common.HexToHash("0x11")

	applied, polls := resultServer(t, id, 3, teetypes.ActionResult{ID: id, Status: StatusSucceeded})
	defer applied.Close()
	res, err := Await(context.Background(), applied.URL, id, teetypes.Submit, time.Second)
	if err != nil {
		t.Fatalf("an applied result was reported as: %v", err)
	}
	if res.Status != StatusSucceeded {
		t.Fatalf("status = %d, want %d", res.Status, StatusSucceeded)
	}
	if *polls < 3 {
		t.Fatalf("returned after %d polls, before the result was filed", *polls)
	}

	rejected, _ := resultServer(t, id, 1, teetypes.ActionResult{ID: id, Status: StatusFailed, Log: "provisioning rejected"})
	defer rejected.Close()
	res, err = Await(context.Background(), rejected.URL, id, teetypes.Submit, time.Second)
	if err != nil || res.Status != StatusFailed || res.Log != "provisioning rejected" {
		t.Fatalf("a rejection must come back as the machine's verdict, got %+v, %v", res, err)
	}

	never, _ := resultServer(t, id, 1<<30, teetypes.ActionResult{})
	defer never.Close()
	if _, err := Await(context.Background(), never.URL, id, teetypes.Submit, 20*time.Millisecond); err == nil {
		t.Fatal("a result that never arrives must be reported, not treated as applied")
	}
}

// PostToExtension speaks the action envelope tee-node delivers: type Direct, so
// a command the extension refuses over the instruction channel is refused here
// too, and a DirectInstruction as the message.
func TestPostToExtension_SendsADirectAction(t *testing.T) {
	var got teetypes.Action
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/action" {
			t.Errorf("path = %s, want /action", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		encodeJSON(t, w, teetypes.ActionResult{Status: StatusSucceeded, Log: "ok"})
	}))
	defer srv.Close()

	id, res, err := PostToExtension(context.Background(), srv.URL, testOPType, testOPCommand, testMessage)
	if err != nil {
		t.Fatalf("PostToExtension: %v", err)
	}
	if res.Status != StatusSucceeded || res.Log != "ok" {
		t.Errorf("result = %+v, want the extension's answer", res)
	}
	if got.Data.ID != id || id == (common.Hash{}) {
		t.Errorf("action id sent %s, returned %s; want one fresh id on both", got.Data.ID.Hex(), id.Hex())
	}
	if got.Data.Type != teetypes.Direct || got.Data.SubmissionTag != teetypes.Submit {
		t.Errorf("action typed %s/%s, want direct/submit", got.Data.Type, got.Data.SubmissionTag)
	}
	var inner teetypes.DirectInstruction
	if err := json.Unmarshal(got.Data.Message, &inner); err != nil {
		t.Fatalf("the message is not a DirectInstruction: %v", err)
	}
	if inner.OPCommand != testOPCommand || !bytes.Equal(inner.Message, testMessage) {
		t.Errorf("instruction = %+v, want the op pair and payload given", inner)
	}
}

// encodeJSON writes v as a test server's response. The fixtures are the
// package's own types, so a failure is a broken test rather than a case.
func encodeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding the response: %v", err)
	}
}
