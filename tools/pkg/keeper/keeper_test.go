package keeper

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/pkg/errors"
)

// A lost race is the designed outcome of the ≥2-keeper topology, not a fault.
// If this ever classifies as a failure the on-call gets paged several times an
// hour for the system working correctly, and the alert gets muted.
func TestOvertakenBatchIsARaceNotAFailure(t *testing.T) {
	// Another keeper's later request landed first: the store is ahead of us.
	if !raceLost(100, 120, true) {
		t.Fatal("a store ahead of the batch should be a lost race")
	}
	// Two keepers whose requests were mined in one block share a stamp, and
	// the second submission of that stamp is refused NotNewer.
	if !raceLost(100, 100, true) {
		t.Fatal("a store at the batch's own stamp should be a lost race")
	}
	if !OutcomeRaceLost.Advanced() {
		t.Fatal("a lost race still advanced the feed — someone else's report landed")
	}
}

// The ratchet only moves forward, so a NotNewer followed by a read showing the
// store BEHIND the batch means the read did not see the refusing state: a node
// lagging behind the one that ran the call, or a store that could not be read.
// Neither proves the feed advanced, so neither may read as a success.
func TestNotNewerWithoutAStoreAheadIsARejection(t *testing.T) {
	if raceLost(100, 80, true) {
		t.Error("a read from a lagging node (store behind the batch) was taken as a lost race")
	}
	if raceLost(100, 0, false) {
		t.Error("an unreadable store was taken as a lost race")
	}
	if OutcomeRejected.Advanced() {
		t.Error("a rejection did not advance the feed")
	}
}

// Only two outcomes leave a newer price on chain. Everything else must read as
// "the feed did not move", because that is what the alerting keys on.
func TestOnlyAcceptedAndRaceLostAdvance(t *testing.T) {
	advancing := map[Outcome]bool{OutcomeAccepted: true, OutcomeRaceLost: true}

	all := []Outcome{
		OutcomeAccepted, OutcomeRaceLost, OutcomeRequestFailed, OutcomeRequestSkipped,
		OutcomeNoResult, OutcomeExtensionFailed, OutcomeRejected, OutcomeDryRun,
		OutcomeNotMined, OutcomeInternal,
	}
	for _, o := range all {
		if o.Advanced() != advancing[o] {
			t.Errorf("%s.Advanced() = %v, want %v", o, o.Advanced(), advancing[o])
		}
	}
}

func TestAsRejectionUnwraps(t *testing.T) {
	wrapped := errors.Wrap(&Rejection{Guard: "TooOld", Msg: "TooOld: too old"}, "submitting")

	rj, ok := AsRejection(wrapped)
	if !ok {
		t.Fatal("a wrapped rejection must still be recognisable — the cycle wraps before classifying")
	}
	if rj.Guard != "TooOld" {
		t.Fatalf("guard = %q, want TooOld", rj.Guard)
	}

	if _, ok := AsRejection(errors.New("rpc down")); ok {
		t.Fatal("a plain error is not a store rejection: it means retry, not reconfigure")
	}
}

// Records are one JSON object per line, which is what a log shipper expects.
// A record is emitted on every cycle including failures, so the fields that
// identify it must survive even when the cycle produced nothing else.
func TestEmitterWritesOneLinePerRecord(t *testing.T) {
	var buf bytes.Buffer
	e := NewEmitter(&buf)

	start := time.Now().UTC()
	e.Emit(Record{
		KeeperID:       "keeper-a",
		CycleStartedAt: start,
		CycleEndedAt:   start.Add(1500 * time.Millisecond),
		Outcome:        OutcomeNoResult,
		ResultStatus:   "timeout",
	})
	e.Emit(Record{KeeperID: "keeper-a", Outcome: OutcomeAccepted})

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}

	var first Record
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("record is not valid JSON: %s", err)
	}
	if first.Outcome != OutcomeNoResult {
		t.Errorf("outcome = %q, want %q", first.Outcome, OutcomeNoResult)
	}
	if first.DurationMS != 1500 {
		t.Errorf("durationMs = %d, want 1500 — the emitter computes it", first.DurationMS)
	}
	if first.KeeperID != "keeper-a" {
		t.Errorf("keeperId = %q: records from ≥2 keepers must be tellable apart", first.KeeperID)
	}
}

// A nil emitter must not panic: it is the legitimate "discard" case in tests.
func TestNilEmitterIsSafe(t *testing.T) {
	var e *Emitter
	e.Emit(Record{Outcome: OutcomeAccepted})
}

// A caller that sets no request timeout still gets one: the wait for a receipt
// is the place a single-flight keeper can stop forever, so the default is part
// of the contract rather than something every call site has to remember.
func TestRequestTimeoutFallsBackToTheDefault(t *testing.T) {
	if got := (Config{}).requestTimeout(); got != DefaultRequestTimeout {
		t.Errorf("unset request timeout = %s, want the %s default", got, DefaultRequestTimeout)
	}
	if got := (Config{RequestTimeout: time.Second}).requestTimeout(); got != time.Second {
		t.Errorf("configured request timeout = %s, want it honoured", got)
	}
}
