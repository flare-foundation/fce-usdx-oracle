package keeper

import (
	"context"
	"encoding/json"
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

// Every cycle has to poll every machine's proxy: the registry picks which one
// serves the instruction, and only that machine's own proxy holds the result.
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

// The on-chain fan-out: the instruction was routed to the second machine, so the
// result exists only there. Polling only the first would expire and report a
// timeout indistinguishable from a dropped instruction.
func TestPollResultAnyFindsTheResultOnAnotherProxy(t *testing.T) {
	var emptyHits atomic.Int32

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emptyHits.Add(1)
		http.NotFound(w, r)
	}))
	defer empty.Close()

	holder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(teetypes.ActionResponse{
			Result: teetypes.ActionResult{Status: statusSucceeded, Log: "from-the-holder"},
		})
	}))
	defer holder.Close()

	res, from, err := PollResultAny(context.Background(),
		[]string{empty.URL, holder.URL}, common.HexToHash("0x1"), teetypes.Threshold, 5*time.Second)
	if err != nil {
		t.Fatalf("PollResultAny: %v", err)
	}
	if res.Log != "from-the-holder" {
		t.Errorf("got result %+v, want the one the second proxy holds", res)
	}
	if from != holder.URL {
		t.Errorf("answered by %q, want %q — the record has to name the machine that produced it",
			from, holder.URL)
	}
	if emptyHits.Load() == 0 {
		t.Error("the first proxy was never asked")
	}
}

func TestPollResultAnyWithoutProxiesFailsImmediately(t *testing.T) {
	_, _, err := PollResultAny(context.Background(), nil,
		common.HexToHash("0x1"), teetypes.Submit, time.Minute)
	if err == nil {
		t.Fatal("expected an error rather than a poll that cannot succeed")
	}
}
