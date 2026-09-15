package keeper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

var (
	machineA = common.HexToAddress("0xaa")
	machineB = common.HexToAddress("0xbb")
	machineC = common.HexToAddress("0xcc")
)

// A machine the registry lists without a URL cannot be reached directly. It has
// to be dropped rather than given a neighbour's URL, which would send a request
// meant for one enclave to another and record the wrong machine as commissioned.
func TestMachineListDropsMachinesWithNoURL(t *testing.T) {
	got := machineList(
		[]common.Address{machineA, machineB, machineC},
		[]string{"https://a/", "", "  "},
		"",
	)

	if len(got) != 1 {
		t.Fatalf("got %d machines (%v), want only the one with a url", len(got), got)
	}
	if got[0].TeeID != machineA {
		t.Errorf("kept %s, want %s", got[0].TeeID.Hex(), machineA.Hex())
	}
	// Trailing slash trimmed, or every URL is built with a double slash.
	if got[0].Proxy != "https://a" {
		t.Errorf("proxy = %q, want the trimmed url", got[0].Proxy)
	}
}

func TestMachineListOverrideReplacesEveryURL(t *testing.T) {
	got := machineList(
		[]common.Address{machineA, machineB},
		[]string{"https://a", ""},
		"http://localhost:6674",
	)

	if len(got) != 2 {
		t.Fatalf("got %d machines, want both — an override also reaches the one with no url", len(got))
	}
	for _, m := range got {
		if m.Proxy != "http://localhost:6674" {
			t.Errorf("%s kept %q, want the override", m.TeeID.Hex(), m.Proxy)
		}
	}
}

// Every cycle has to poll every machine's proxy: each addressed machine files
// its result on its own.
func TestCycleConfigPollsTheWholeFleet(t *testing.T) {
	cfg := LoopConfig{Config: Config{Proxy: "https://configured", DirectAPIKey: "k"}}
	f := fleet{machines: []Machine{
		{TeeID: machineA, Proxy: "https://a"},
		{TeeID: machineB, Proxy: "https://b"},
	}}

	c := cfg.cycleConfig(&f)
	if got := c.pollTargets(); len(got) != 3 {
		t.Errorf("poll targets = %v, want the configured proxy plus both machines", got)
	}
}

// An unreadable registry must not stop the feed: the cycle falls back to the one
// proxy it was told about by hand.
func TestCycleConfigFallsBackToTheConfiguredProxy(t *testing.T) {
	cfg := LoopConfig{Config: Config{Proxy: "https://configured"}}

	var f fleet
	got := cfg.cycleConfig(&f).pollTargets()
	if len(got) != 1 || got[0] != "https://configured" {
		t.Errorf("poll targets = %v, want just the configured proxy", got)
	}
}

func TestPollTargetsDeduplicatesAndKeepsThePrimaryFirst(t *testing.T) {
	cfg := Config{
		Proxy:         "https://a/",
		ResultProxies: []string{"https://b", "https://a", "", "  ", "https://b/"},
	}

	got := cfg.pollTargets()
	want := []string{"https://a", "https://b"}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("targets = %v, want %v", got, want)
		}
	}
}

// Each addressed machine files its result on its own proxy, so a proxy that has
// nothing must not end the round: the result exists on the other one.
func TestPollResultsFindsTheResultOnAnotherProxy(t *testing.T) {
	var emptyHits atomic.Int32

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emptyHits.Add(1)
		http.NotFound(w, r)
	}))
	defer empty.Close()

	holder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encodeJSON(t, w, teetypes.ActionResponse{
			Result: teetypes.ActionResult{Status: statusSucceeded, Log: "from-the-holder"},
		})
	}))
	defer holder.Close()

	got, err := pollResults(context.Background(),
		[]string{empty.URL, holder.URL}, 1, common.HexToHash("0x1"), teetypes.Threshold, 5*time.Second)
	if err != nil {
		t.Fatalf("pollResults: %v", err)
	}
	if len(got) != 1 || got[0].result.Log != "from-the-holder" {
		t.Errorf("got %+v, want the one result the second proxy holds", got)
	}
	if got[0].proxy != holder.URL {
		t.Errorf("answered by %q, want %q — the record has to name the machine that produced it",
			got[0].proxy, holder.URL)
	}
	if emptyHits.Load() == 0 {
		t.Error("the first proxy was never asked")
	}
}

// A round is complete when every addressed machine has answered, and the results
// come back in proxy order so the batch is stable across cycles.
func TestPollResultsReturnsEveryAnswerInProxyOrder(t *testing.T) {
	serve := func(log string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			encodeJSON(t, w, teetypes.ActionResponse{
				Result: teetypes.ActionResult{Status: statusSucceeded, Log: log},
			})
		}))
	}
	a, b := serve("a"), serve("b")
	defer a.Close()
	defer b.Close()

	started := time.Now()
	got, err := pollResults(context.Background(),
		[]string{a.URL, b.URL}, 2, common.HexToHash("0x1"), teetypes.Threshold, time.Minute)
	if err != nil {
		t.Fatalf("pollResults: %v", err)
	}
	if len(got) != 2 || got[0].result.Log != "a" || got[1].result.Log != "b" {
		t.Errorf("got %+v, want both results in proxy order", got)
	}
	if time.Since(started) > 5*time.Second {
		t.Error("a complete round must not wait for the deadline")
	}
}

// A machine that never answers costs the timeout, not the cycle: what did arrive
// is returned, and whether it is enough is the store's threshold to judge.
func TestPollResultsReturnsAPartialRoundAtTheDeadline(t *testing.T) {
	answers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encodeJSON(t, w, teetypes.ActionResponse{
			Result: teetypes.ActionResult{Status: statusSucceeded, Log: "only-one"},
		})
	}))
	defer answers.Close()
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer silent.Close()

	got, err := pollResults(context.Background(),
		[]string{answers.URL, silent.URL}, 2, common.HexToHash("0x1"), teetypes.Threshold, 1200*time.Millisecond)
	if err != nil {
		t.Fatalf("a partial round is not an error: %v", err)
	}
	if len(got) != 1 || got[0].result.Log != "only-one" {
		t.Errorf("got %+v, want the one answer that arrived", got)
	}
}

func TestPollResultsWithoutProxiesFailsImmediately(t *testing.T) {
	_, err := pollResults(context.Background(), nil, 1,
		common.HexToHash("0x1"), teetypes.Submit, time.Minute)
	if err == nil {
		t.Fatal("expected an error rather than a poll that cannot succeed")
	}
}
