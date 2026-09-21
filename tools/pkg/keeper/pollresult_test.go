package keeper

import (
	"context"
	"encoding/json"
	"fce-usdx-oracle/tools/pkg/direct"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// The submission tag has to reach the proxy as a query parameter. A cycle asks
// for "threshold" and a readiness query for "submit"; the wrong one 404s forever
// and reports a timeout that looks like a dropped instruction.
func TestPollResult_SendsTheSubmissionTag(t *testing.T) {
	var gotTag string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTag = r.URL.Query().Get("submissionTag")
		encodeJSON(t, w, teetypes.ActionResponse{
			Result: teetypes.ActionResult{Status: direct.StatusSucceeded},
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

// encodeJSON writes v as a test server's response. The fixtures are the
// package's own types, so a failure is a broken test rather than a case.
func encodeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding the response: %v", err)
	}
}
