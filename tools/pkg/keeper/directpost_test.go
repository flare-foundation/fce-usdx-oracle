package keeper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
)

// postDirectAction carries GET_READINESS and nothing else — a price is always an
// on-chain instruction — so the readiness op pair is what these exercise.
var (
	testOPType    = teeutils.ToHash(opTypeTeeOracle)
	testOPCommand = teeutils.ToHash(opCommandGetReadiness)
)

// The direct wire shape is a DirectInstruction: three fields, payload under
// "message". Sending a DataFixed does not fail — the extension just sees an empty
// payload — so the shape is asserted here rather than in an integration run.
func TestPostDirectAction_SendsTheDirectInstructionShape(t *testing.T) {
	var (
		gotKey  string
		gotBody map[string]any
		gotPath string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		_ = json.NewEncoder(w).Encode(teetypes.Action{
			Data: teetypes.ActionData{
				ID:            common.HexToHash("0xabc"),
				Type:          teetypes.Direct,
				SubmissionTag: teetypes.Submit,
			},
		})
	}))
	defer srv.Close()

	id, err := postDirectAction(context.Background(), srv.URL, "the-key",
		testOPType, testOPCommand, readinessRequestV1)
	if err != nil {
		t.Fatalf("postDirectAction: %v", err)
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
func TestPostDirectAction_ReportsProxyRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := postDirectAction(context.Background(), srv.URL, "wrong",
		testOPType, testOPCommand, readinessRequestV1)
	if err == nil {
		t.Fatal("expected an error for a 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, want it to name the status", err)
	}
}

// A 2xx with no action id is not success: there is nothing to poll for, and
// treating it as an id would poll a zero hash until the deadline.
func TestPostDirectAction_RejectsMissingActionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	if _, err := postDirectAction(context.Background(), srv.URL, "k",
		testOPType, testOPCommand, readinessRequestV1); err == nil {
		t.Fatal("expected an error when the proxy returns no action id")
	}
}

// The submission tag has to reach the proxy as a query parameter. A cycle asks
// for "threshold" and a readiness query for "submit"; the wrong one 404s forever
// and reports a timeout that looks like a dropped instruction.
func TestPollResult_SendsTheSubmissionTag(t *testing.T) {
	var gotTag string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTag = r.URL.Query().Get("submissionTag")
		_ = json.NewEncoder(w).Encode(teetypes.ActionResponse{
			Result: teetypes.ActionResult{Status: statusSucceeded},
		})
	}))
	defer srv.Close()

	for _, tag := range []teetypes.SubmissionTag{teetypes.Submit, teetypes.Threshold} {
		_, err := PollResult(context.Background(), srv.URL, common.HexToHash("0x1"), tag, time.Second)
		if err != nil {
			t.Fatalf("PollResult(%s): %v", tag, err)
		}
		if gotTag != string(tag) {
			t.Errorf("submissionTag = %q, want %q", gotTag, tag)
		}
	}
}

// A readiness query without a proxy or a key is a configuration error, caught
// before any request goes out.
func TestQueryReadiness_RequiresProxyAndKey(t *testing.T) {
	cases := []struct {
		name          string
		proxy, apiKey string
	}{
		{"no proxy", "", "k"},
		{"no key", "http://localhost:1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := QueryReadiness(context.Background(), c.proxy, c.apiKey, time.Second)
			if err == nil {
				t.Fatal("expected a configuration error")
			}
		})
	}
}
