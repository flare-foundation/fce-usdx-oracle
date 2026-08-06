package keeper

import (
	"encoding/json"
	"io"
	"time"
)

// Outcome is the terminal classification of one cycle.
//
// The taxonomy is the one in docs/usdx-oracle-monitoring.md: "no price landed"
// has several causes, they route to different owners, and one of them is not a
// fault at all. Collapsing them into a single "failed" is what produces an alert
// nobody trusts.
type Outcome string

const (
	// OutcomeAccepted — our submission landed. The only outcome that advanced the feed.
	OutcomeAccepted Outcome = "accepted"

	// OutcomeRaceLost — a newer price was already stored, so submitPrice would
	// revert NotNewer. This is a SUCCESS: with ≥2 keepers exactly one submission
	// wins per cycle and the rest lose the race. It must never alert.
	OutcomeRaceLost Outcome = "race_lost"

	// OutcomeStaleSubmission — NotNewer, but the store is *behind* our report
	// rather than ahead of it, meaning we were relaying something old. A latency
	// problem worth seeing, unlike OutcomeRaceLost.
	OutcomeStaleSubmission Outcome = "stale_submission"

	// OutcomeRequestFailed — the trigger transaction never landed (cause 2).
	OutcomeRequestFailed Outcome = "request_failed"

	// OutcomeNoResult — the poll expired with no terminal status (cause 3). FCC
	// is fire-and-forget, so this is expected, not exceptional.
	OutcomeNoResult Outcome = "no_result"

	// OutcomeExtensionFailed — a status-0 result: the extension declined to
	// publish and named a class in Log (cause 4). Fail closed; relay nothing.
	OutcomeExtensionFailed Outcome = "extension_failed"

	// OutcomeRejected — the report is real but the store will not take it
	// (cause 6): StaleConfig, OutOfBounds, TooOld, UnknownSigner and friends.
	OutcomeRejected Outcome = "rejected"

	// OutcomeNotMined — submitted, but no receipt inside the deadline. Not
	// chased: the next cycle supersedes it, and a replacement landing after
	// maxAge would revert TooOld anyway.
	OutcomeNotMined Outcome = "not_mined"

	// OutcomeInternal — a fault in the keeper itself.
	OutcomeInternal Outcome = "internal"
)

// Advanced reports whether the cycle left a newer price on chain, whether or not
// this keeper is the one that put it there.
func (o Outcome) Advanced() bool {
	return o == OutcomeAccepted || o == OutcomeRaceLost
}

// Record is one cycle's emitted record — the keeper's half of the alerting
// contract in docs/usdx-oracle-monitoring.md.
//
// Emitted on every tick regardless of outcome, and write-only: nothing reads a
// record back to make a decision. That is what keeps the keeper stateless, and
// the reason a crash mid-cycle needs no recovery — see the keeper spec.
type Record struct {
	KeeperID       string    `json:"keeperId"`
	CycleStartedAt time.Time `json:"cycleStartedAt"`
	CycleEndedAt   time.Time `json:"cycleEndedAt"`
	DurationMS     int64     `json:"durationMs"`

	Outcome Outcome `json:"outcome"`

	// InstructionID is empty when the trigger never landed.
	InstructionID   string `json:"instructionId,omitempty"`
	RequestTxStatus string `json:"requestTxStatus,omitempty"`

	// ResultStatus is the ActionResult status as a string — "1", "0", or
	// "timeout" when no terminal result arrived.
	ResultStatus string `json:"resultStatus,omitempty"`

	// ErrorClass is ActionResult.Log on a status-0 result: a constant from the
	// extension's closed vocabulary, which is the alert key.
	ErrorClass string `json:"errorClass,omitempty"`

	// SourceHealth is the extension's per-source map, read from the proxy.
	// Best-effort: a missing map means the proxy could not be read, never that
	// the sources are healthy.
	SourceHealth map[string]string `json:"sourceHealth,omitempty"`

	// SubmitOutcome is "accepted", a named store guard, or "not_attempted".
	SubmitOutcome string `json:"submitOutcome,omitempty"`
	SubmitTx      string `json:"submitTx,omitempty"`

	// Price, Decimals and ObservedAt come from the decoded report, when there
	// was one. Price is the scaled integer as a string: it is a uint256, and a
	// JSON number is a float64 in most parsers.
	Price      string `json:"price,omitempty"`
	Decimals   int8   `json:"decimals,omitempty"`
	ObservedAt uint64 `json:"observedAt,omitempty"`

	// StoreObservedAtAfter is the store's observedAt at the end of the cycle.
	// This is the per-cycle sample of the one signal that cannot lie, and what
	// separates a lost race from a real failure.
	StoreObservedAtAfter uint64 `json:"storeObservedAtAfter"`

	// PendingNonceGap is non-zero when this keeper has transactions broadcast
	// but not mined. A stuck transaction blocks every later one behind its
	// nonce, so a persistent gap is a liveness fault the cycle outcome alone
	// would not show.
	PendingNonceGap uint64 `json:"pendingNonceGap,omitempty"`

	// Detail is a human-readable reason. Never parsed; route on Outcome and
	// ErrorClass instead.
	Detail string `json:"detail,omitempty"`
}

// Emitter writes cycle records.
type Emitter struct {
	enc *json.Encoder
}

// NewEmitter writes one JSON object per line, which is what log shippers and
// `jq` both expect.
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
