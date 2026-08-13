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

	// The poll expired with no terminal status. FCC is fire-and-forget, so this
	// is expected, not exceptional.
	OutcomeNoResult Outcome = "no_result"

	// Status-0: the extension declined to publish and named a class in Log.
	OutcomeExtensionFailed Outcome = "extension_failed"

	// The report is real but the store will not take it — StaleConfig,
	// OutOfBounds, TooOld, UnknownSigner and friends.
	OutcomeRejected Outcome = "rejected"

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

	// Channel is "instruction" or "direct". Worth recording because the two have
	// different failure modes at the same outcome: a direct no_result points at
	// one machine's proxy, an instruction no_result could be any machine the
	// registry happened to pick.
	Channel string `json:"channel,omitempty"`

	// Phase is "bootstrap" for the startup sweep and empty for a timed cycle.
	// The two have different success criteria — a sweep entry that reports
	// race_lost did not commission its machine, while a cycle that reports it
	// worked exactly as designed — so alerting has to be able to tell them apart.
	Phase string `json:"phase,omitempty"`

	// Proxy is the machine's proxy this cycle used: the direct target, or on the
	// instruction channel whichever of the fleet's proxies actually held the
	// result. With a rotating keeper this is what says which machine a failure
	// belongs to.
	Proxy string `json:"proxy,omitempty"`

	// TeeID is the machine that signed the report, recovered from the signature.
	// On the instruction channel it is the only thing that names the machine the
	// registry routed to. Falls back to the machine the cycle addressed when no
	// report was produced.
	TeeID string `json:"teeId,omitempty"`

	// InstructionID is the on-chain instruction id, or the proxy's action id on
	// the direct channel. Empty when the trigger never landed.
	InstructionID string `json:"instructionId,omitempty"`

	// RequestTxStatus is "mined", "failed", or "not_applicable" on the direct
	// channel, which sends no trigger transaction at all.
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

	// Price is the scaled integer as a string: it is a uint256, and a JSON
	// number is a float64 in most parsers.
	Price      string `json:"price,omitempty"`
	Decimals   int8   `json:"decimals,omitempty"`
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
