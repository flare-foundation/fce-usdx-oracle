package keeper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fce-usdx-oracle/tools/pkg/readinessreport"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

var testReadinessID = common.HexToHash("0xabc")

// readinessProxy serves a machine answering GET_READINESS with a genuinely signed
// report. Signed for real because Decode recovers the signer and checks the payload
// hash, so a hand-rolled blob would be rejected before wouldRefuse saw it.
func readinessProxy(t *testing.T, wouldSign bool) *httptest.Server {
	t.Helper()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	payload, err := json.Marshal(map[string]any{
		"wouldSign":      wouldSign,
		"observationAge": 900,
		"lastErrorClass": "source_unavailable",
		"readiness":      map[string]any{"ready": true},
	})
	if err != nil {
		t.Fatalf("encoding the payload: %v", err)
	}

	bodyArgs := mustReadinessArgs(t, "bytes32", "bytes32", "uint64", "bytes32")
	body, err := bodyArgs.Pack(
		readinessreport.Domain(),
		testReadinessID,
		uint64(1786952592),
		crypto.Keccak256Hash(payload),
	)
	if err != nil {
		t.Fatalf("packing the body: %v", err)
	}

	signature, err := crypto.Sign(accounts.TextHash(crypto.Keccak256(body)), key)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	// The extension normalises v on the way out; so must the fixture.
	signature[64] += 27

	data, err := mustReadinessArgs(t, "bytes", "bytes", "bytes").Pack(body, signature, payload)
	if err != nil {
		t.Fatalf("packing the payload: %v", err)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			encodeJSON(t, w, teetypes.Action{
				Data: teetypes.ActionData{
					ID:            testReadinessID,
					Type:          teetypes.Direct,
					SubmissionTag: teetypes.Submit,
				},
			})
			return
		}
		encodeJSON(t, w, teetypes.ActionResponse{
			Result: teetypes.ActionResult{Status: statusSucceeded, Data: data},
		})
	}))
}

func mustReadinessArgs(t *testing.T, types ...string) abi.Arguments {
	t.Helper()
	args := make(abi.Arguments, len(types))
	for i, ty := range types {
		parsed, err := abi.NewType(ty, "", nil)
		if err != nil {
			t.Fatalf("abi type %s: %v", ty, err)
		}
		args[i] = abi.Argument{Type: parsed}
	}
	return args
}

// An on-chain instruction is routed to a machine the registry picks, so one machine's
// staleness must not speak for the fleet. A single willing machine means it goes out.
func TestWouldRefuse_OneWillingMachineIsEnoughToRequest(t *testing.T) {
	stale := readinessProxy(t, false)
	defer stale.Close()
	willing := readinessProxy(t, true)
	defer willing.Close()

	cfg := Config{
		DirectAPIKey:  "k",
		Proxy:         stale.URL,
		ResultProxies: []string{willing.URL},
	}

	if skip, why := wouldRefuse(context.Background(), cfg); skip {
		t.Errorf("skipped the request although a machine would sign: %s", why)
	}
}

// Unanimous refusal is the one case that predicts the outcome, and the only one
// where skipping actually saves a fee.
func TestWouldRefuse_SkipsOnlyWhenEveryMachineRefuses(t *testing.T) {
	a := readinessProxy(t, false)
	defer a.Close()
	b := readinessProxy(t, false)
	defer b.Close()

	cfg := Config{
		DirectAPIKey:  "k",
		Proxy:         a.URL,
		ResultProxies: []string{b.URL},
	}

	skip, why := wouldRefuse(context.Background(), cfg)
	if !skip {
		t.Fatal("expected the request to be skipped when no machine would sign")
	}
	if !strings.Contains(why, "no fee spent") {
		t.Errorf("reason = %q, want it to say the fee was saved", why)
	}
}

// Uncertainty is not refusal. A machine that cannot answer readiness — an older
// build, an unreachable proxy — may still answer a price perfectly well, so the
// keeper requests anyway rather than turning a diagnostic into an outage.
func TestWouldRefuse_UnreadableMachineRequestsAnyway(t *testing.T) {
	stale := readinessProxy(t, false)
	defer stale.Close()

	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}))
	defer silent.Close()

	cfg := Config{
		DirectAPIKey:  "k",
		Proxy:         stale.URL,
		ResultProxies: []string{silent.URL},
	}

	if skip, why := wouldRefuse(context.Background(), cfg); skip {
		t.Errorf("skipped although one machine could not be read: %s", why)
	}
}

// No key and no target are configuration states, not refusals: there is nothing
// to ask, so nothing is predicted.
func TestWouldRefuse_WithoutKeyOrTargetRequestsAnyway(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no key":    {Proxy: "http://example.invalid"},
		"no target": {DirectAPIKey: "k"},
	} {
		if skip, _ := wouldRefuse(context.Background(), cfg); skip {
			t.Errorf("%s: expected the request to go ahead", name)
		}
	}
}
