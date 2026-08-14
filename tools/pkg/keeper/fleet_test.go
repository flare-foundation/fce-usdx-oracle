package keeper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"fce-usdx-oracle/tools/pkg/pricereport"

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

// Rotation has to reach every machine, or the machines it skips fall out of
// commission the moment their config is re-pushed and nobody notices.
func TestFleetRotationCoversEveryMachineAndWraps(t *testing.T) {
	f := fleet{machines: []Machine{
		{TeeID: machineA, Proxy: "https://a"},
		{TeeID: machineB, Proxy: "https://b"},
		{TeeID: machineC, Proxy: "https://c"},
	}}

	var got []common.Address
	for range 7 {
		m, ok := f.take()
		if !ok {
			t.Fatal("take() reported no machine from a non-empty fleet")
		}
		got = append(got, m.TeeID)
	}

	want := []common.Address{machineA, machineB, machineC, machineA, machineB, machineC, machineA}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("turn %d went to %s, want %s", i, got[i].Hex(), want[i].Hex())
		}
	}
}

func TestEmptyFleetTakesNothing(t *testing.T) {
	var f fleet
	if _, ok := f.take(); ok {
		t.Fatal("an empty fleet must report no machine, so the caller falls back to the configured proxy")
	}
}

// A direct cycle addresses one named machine; an on-chain cycle addresses none
// and has to look for its result on every proxy, because the registry chose the
// machine.
func TestCycleConfigPicksChannelAndTarget(t *testing.T) {
	cfg := LoopConfig{
		Config: Config{Proxy: "https://configured", Direct: true, DirectAPIKey: "k"},
	}
	f := fleet{machines: []Machine{
		{TeeID: machineA, Proxy: "https://a"},
		{TeeID: machineB, Proxy: "https://b"},
	}}

	direct := cfg.cycleConfig(&f, false)
	if !direct.Direct {
		t.Error("a direct cycle must use the direct channel")
	}
	if direct.Proxy != "https://a" || direct.TeeID != machineA {
		t.Errorf("direct cycle went to %s/%s, want the machine whose turn it is",
			direct.TeeID.Hex(), direct.Proxy)
	}
	if len(direct.ResultProxies) != 0 {
		t.Errorf("direct cycle fans out to %v — a direct result exists only on the machine it was sent to",
			direct.ResultProxies)
	}

	onChain := cfg.cycleConfig(&f, true)
	if onChain.Direct {
		t.Error("an on-chain cycle must use the instruction channel")
	}
	if onChain.TeeID != (common.Address{}) {
		t.Errorf("on-chain cycle claims to address %s — the registry picks the machine",
			onChain.TeeID.Hex())
	}
	if got := onChain.pollTargets(); len(got) != 3 {
		t.Errorf("on-chain poll targets = %v, want the configured proxy plus both machines", got)
	}
}

// The rotation must not be consumed by an on-chain cycle: the next direct cycle
// should continue where the last one left off rather than skip a machine.
func TestOnChainCycleDoesNotAdvanceTheRotation(t *testing.T) {
	cfg := LoopConfig{Config: Config{Proxy: "https://configured", Direct: true}}
	f := fleet{machines: []Machine{
		{TeeID: machineA, Proxy: "https://a"},
		{TeeID: machineB, Proxy: "https://b"},
	}}

	cfg.cycleConfig(&f, true)
	if got := cfg.cycleConfig(&f, false); got.TeeID != machineA {
		t.Errorf("next direct cycle went to %s, want %s — the on-chain cycle took a turn",
			got.TeeID.Hex(), machineA.Hex())
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

// The property the whole sweep rests on: observedAt must strictly increase, so
// the oldest report has to go first. Submitting in registry order lands one and
// rejects the rest as NotNewer, commissioning one machine per pass forever.
func TestSubmissionOrderIsOldestObservationFirst(t *testing.T) {
	held := []swept{
		{machine: Machine{TeeID: machineA}, report: pricereport.Report{ObservedAt: 300}, ok: true},
		{machine: Machine{TeeID: machineB}, report: pricereport.Report{ObservedAt: 100}, ok: true},
		{machine: Machine{TeeID: machineC}, report: pricereport.Report{ObservedAt: 200}, ok: true},
	}

	got := submissionOrder(held)
	want := []int{1, 2, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (ascending observedAt)", got, want)
		}
	}
}

// A machine that produced no report has nothing to submit. Including it would
// relay a zero-value report, which is the one thing the keeper must never do.
func TestSubmissionOrderSkipsMachinesThatCollectedNothing(t *testing.T) {
	held := []swept{
		{machine: Machine{TeeID: machineA}, report: pricereport.Report{ObservedAt: 300}, ok: true},
		{machine: Machine{TeeID: machineB}, ok: false},
	}

	got := submissionOrder(held)
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("order = %v, want only the machine that produced a report", got)
	}
}
