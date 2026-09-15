package keeper

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"fce-usdx-oracle/tools/pkg/feedupdate"

	"github.com/pkg/errors"
)

// A lost race is the designed outcome of the ≥2-keeper topology, not a fault.
// If this ever classifies as a failure the on-call gets paged several times an
// hour for the system working correctly, and the alert gets muted.
func TestNotNewerIsARaceNotAFailure(t *testing.T) {
	report := feedupdate.Update{ObservedAt: 100}

	rj := &Rejection{Guard: "NotNewer", StoreObservedAt: 120}
	if got := classifyRejection(rj, report.ObservedAt); got != OutcomeRaceLost {
		t.Fatalf("store ahead of us should be a lost race, got %q", got)
	}
	if !OutcomeRaceLost.Advanced() {
		t.Fatal("a lost race still advanced the feed — someone else's report landed")
	}
}

// The same guard fires when our own report was stale, which is a latency problem
// worth seeing. Collapsing the two is what would hide it.
func TestNotNewerWithStaleReportIsDistinct(t *testing.T) {
	report := feedupdate.Update{ObservedAt: 100}

	rj := &Rejection{Guard: "NotNewer", StoreObservedAt: 80}
	got := classifyRejection(rj, report.ObservedAt)
	if got != OutcomeStaleSubmission {
		t.Fatalf("store behind us should be a stale submission, got %q", got)
	}
	if got.Advanced() {
		t.Fatal("a stale submission did not advance the feed")
	}
}

func TestOtherGuardsAreRejections(t *testing.T) {
	report := feedupdate.Update{ObservedAt: 100}

	for _, guard := range []string{"WrongExtensionId", "WrongFeedId", "StaleEndpoints", "NoEndpointsPublished", "StaleAdmins", "NoAdminsPublished", "BadSignature", "TooFarAhead", "NotEnoughSignatures", "TooManySignatures", "SpreadTooBig"} {
		rj := &Rejection{Guard: guard}
		if got := classifyRejection(rj, report.ObservedAt); got != OutcomeRejected {
			t.Errorf("%s: got %q, want %q", guard, got, OutcomeRejected)
		}
	}
}

// Only two outcomes leave a newer price on chain. Everything else must read as
// "the feed did not move", because that is what the alerting keys on.
func TestOnlyAcceptedAndRaceLostAdvance(t *testing.T) {
	advancing := map[Outcome]bool{OutcomeAccepted: true, OutcomeRaceLost: true}

	all := []Outcome{
		OutcomeAccepted, OutcomeRaceLost, OutcomeStaleSubmission, OutcomeRequestFailed,
		OutcomeNoResult, OutcomeExtensionFailed, OutcomeRejected, OutcomeNotMined, OutcomeInternal,
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
