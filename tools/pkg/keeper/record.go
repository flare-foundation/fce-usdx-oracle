package keeper

import (
	"encoding/json"
	"io"
	"time"
)

// Outcome is the terminal classification of one cycle. "No price landed" has
// several causes, they route to different owners, and one is not a fault at
// all — collapsing them into "failed" produces an alert nobody trusts.
type Outcome string

const (
	// Our submission landed — the only outcome that advanced the feed.
	OutcomeAccepted Outcome = "accepted"

	// A newer price was already stored. A SUCCESS: with ≥2 keepers exactly one
	// submission wins per cycle. Must never alert.
	OutcomeRaceLost Outcome = "race_lost"

	// NotNewer, but the store is BEHIND our report: we relayed something old.
	// A latency problem worth seeing, unlike OutcomeRaceLost.
	OutcomeStaleSubmission Outcome = "stale_submission"

	// The trigger transaction never landed.
	OutcomeRequestFailed Outcome = "request_failed"

	// The machines said they would refuse to sign, so no paid request was made.
	// Distinct from request_failed: a saving, not a fault. What deserves attention
	// is the observation age it names, which is already on the record.
	OutcomeRequestSkipped Outcome = "request_skipped"

	// The poll expired with no terminal status. FCC is fire-and-forget, so this
	// is expected, not exceptional.
	OutcomeNoResult Outcome = "no_result"

	// Status-0: the extension declined to publish and named a class in Log.
	OutcomeExtensionFailed Outcome = "extension_failed"

	// The report is real but the store will not take it — StaleEndpoints,
	// StaleAdmins, NotNewer, TooFarAhead and friends.
	OutcomeRejected Outcome = "rejected"

	// OutcomeDryRun is a cycle that requested, collected and passed the store's
	// rehearsal, then stopped before submitting because it was asked to.
	OutcomeDryRun Outcome = "dry_run"

	// Submitted, but no receipt inside the deadline. Not chased.
	OutcomeNotMined Outcome = "not_mined"

	OutcomeInternal Outcome = "internal"
)

// Advanced reports whether the cycle left a newer price on chain, whether or not
// this keeper is the one that put it there.
func (o Outcome) Advanced() bool {
	return o == OutcomeAccepted || o == OutcomeRaceLost
}

// Record is one cycle's emitted record. Emitted on every tick and write-only:
// nothing reads one back to make a decision, which is what keeps the keeper
// stateless.
type Record struct {
	KeeperID       string    `json:"keeperId"`
	CycleStartedAt time.Time `json:"cycleStartedAt"`
	CycleEndedAt   time.Time `json:"cycleEndedAt"`
	DurationMS     int64     `json:"durationMs"`

	Outcome Outcome `json:"outcome"`

	// Proxy is the first of the fleet's proxies to answer, which on a bad cycle
	// is what says whose failure it was.
	Proxy string `json:"proxy,omitempty"`

	// TeeIDs are the machines whose updates went up together, recovered from
	// their signatures — the only thing that names who contributed. Empty when
	// no usable report was produced.
	TeeIDs []string `json:"teeIds,omitempty"`

	// InstructionID is the on-chain instruction id. Empty when the trigger
	// transaction never landed.
	InstructionID string `json:"instructionId,omitempty"`

	// RequestTxStatus is "mined" or "failed" for the requestFeedUpdate()
	// transaction.
	RequestTxStatus string `json:"requestTxStatus,omitempty"`

	// ResultStatus is the ActionResult status as a string — "1", "0", or
	// "timeout" when no terminal result arrived.
	ResultStatus string `json:"resultStatus,omitempty"`

	// ActionResult.Log on a status-0 result: a constant from the extension's
	// closed vocabulary, which is the alert key.
	ErrorClass string `json:"errorClass,omitempty"`

	// Best-effort: a missing map means the proxy could not be read, never that
	// the sources are healthy.
	SourceHealth map[string]string `json:"sourceHealth,omitempty"`

	// SubmitOutcome is "accepted", a named store guard, or "not_attempted".
	SubmitOutcome string `json:"submitOutcome,omitempty"`
	SubmitTx      string `json:"submitTx,omitempty"`

	// Prices are each contributor's value at its own scale, in TeeIDs order, as
	// strings. The keeper never aggregates them: the store takes the median.
	Prices []string `json:"prices,omitempty"`
	// ObservedAt is the instant the whole batch observed.
	ObservedAt uint64 `json:"observedAt,omitempty"`

	// The store's observedAt at the end of the cycle — the one signal that
	// cannot lie, and what separates a lost race from a real failure.
	StoreObservedAtAfter uint64 `json:"storeObservedAtAfter"`

	// Non-zero when transactions are broadcast but not mined. A stuck one blocks
	// every later transaction behind its nonce.
	PendingNonceGap uint64 `json:"pendingNonceGap,omitempty"`

	// Human-readable. Never parsed; route on Outcome and ErrorClass instead.
	Detail string `json:"detail,omitempty"`
}

// Emitter writes cycle records.
type Emitter struct {
	enc *json.Encoder
}

// NewEmitter writes one JSON object per line, as log shippers and jq expect.
func NewEmitter(w io.Writer) *Emitter {
	return &Emitter{enc: json.NewEncoder(w)}
}

func (e *Emitter) Emit(r Record) {
	if e == nil || e.enc == nil {
		return
	}
	r.DurationMS = r.CycleEndedAt.Sub(r.CycleStartedAt).Milliseconds()
	_ = e.enc.Encode(r)
}
