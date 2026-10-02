package keeper

import (
	"encoding/json"
	"io"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// Outcome is the terminal classification of one cycle. "No price landed" has
// several causes, they route to different owners, and one is not a fault at
// all — collapsing them into "failed" produces an alert nobody trusts.
type Outcome string

const (
	// Our submission landed.
	OutcomeAccepted Outcome = "accepted"

	// The store refused our batch NotNewer (in the preflight, the rehearsal or
	// gas estimation), or our submission was mined and reverted, and the store,
	// read afterwards, holds an observedAt at least as new as ours: another
	// submission landed first, almost always another keeper's later request.
	// The feed advanced, so this is a success and must never alert.
	OutcomeRaceLost Outcome = "race_lost"

	// The trigger transaction never landed.
	OutcomeRequestFailed Outcome = "request_failed"

	// The machines said they would refuse to sign, so no paid request was made.
	// Distinct from request_failed: a saving, not a fault. What deserves attention
	// is the observation age it names, which is already on the record.
	OutcomeRequestSkipped Outcome = "request_skipped"

	// The poll expired with no terminal status. FCC is fire-and-forget, so this
	// is expected, not exceptional.
	OutcomeNoResult Outcome = "no_result"

	// No answering machine published a usable update: each declined (status 0)
	// or sent a result that does not decode. ErrorClass carries the first
	// refusal's text, and is empty when every answer failed to decode.
	OutcomeExtensionFailed Outcome = "extension_failed"

	// The answers are real but the store will not take them: StaleEndpoints,
	// StaleAdmins, SpreadTooBig, TooFarAhead and friends, named in
	// SubmitOutcome. Also a submission that was mined and reverted while the
	// store stayed behind it, under the guard its replay names (Reverted when
	// none), and a NotNewer while the store reads lower than our batch, which
	// only a lagging RPC node or an unreadable store can produce.
	OutcomeRejected Outcome = "rejected"

	// OutcomeDryRun is a cycle that requested, collected and passed the store's
	// rehearsal, then stopped before submitting because it was asked to.
	OutcomeDryRun Outcome = "dry_run"

	// Submitted, but no receipt inside the submit deadline. Not chased. A
	// submission that was mined and reverted is race_lost or rejected instead.
	OutcomeNotMined Outcome = "not_mined"

	OutcomeInternal Outcome = "internal"
)

// Advanced reports whether the feed holds a price at least as new as this
// cycle's, whether or not this keeper is the one that put it there.
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

	// Proxy is the first proxy, in poll order (the configured one, then the
	// fleet's), that returned a result; the configured proxy when the cycle
	// ended before any did.
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

	// ErrorClass is the first status-0 result's ActionResult.Log, copied
	// verbatim: the extension's public refusal text, "error: <reason>". The
	// reasons form a closed set (internal/failure), each with a class, so
	// monitoring routes on the reason text through a reason → class table. A
	// command the build does not serve reaches Log as the extension's 501 body
	// instead, which is outside that set.
	ErrorClass string `json:"errorClass,omitempty"`

	// Every machine's own view of its sources, from the readiness preflight,
	// keyed by teeId: health is per machine, and two machines reading the same
	// provider can disagree about it. A machine present with an empty map
	// answered but has observed nothing yet; one that is absent could not be
	// asked. Best-effort throughout — a missing map means no machine could be
	// asked (no key, none reachable), never that the sources are healthy.
	SourceHealth map[string]map[string]string `json:"sourceHealth,omitempty"`

	// SubmitOutcome is how the submission step ended: "accepted"; "not_attempted"
	// when the cycle ended before it; "dry_run"; "send_failed" when the
	// transaction could not be sent for a reason that names no store guard;
	// "not_mined"; or the store guard that refused the batch (NotNewer,
	// StaleEndpoints, SpreadTooBig, …, or Reverted when the revert names none),
	// whether the refusal came in the preflight, the rehearsal, gas estimation or
	// a mined submission's replay.
	SubmitOutcome string `json:"submitOutcome,omitempty"`
	// SubmitTx is the submitFeedUpdates transaction, once one was broadcast.
	SubmitTx string `json:"submitTx,omitempty"`

	// Prices are each contributor's value at its own scale, in TeeIDs order, as
	// strings. The keeper never aggregates them: the store takes the median.
	Prices []string `json:"prices,omitempty"`
	// ObservedAt is the batch's common observedAt: the requesting instruction's
	// block timestamp, which every machine answering it signs.
	ObservedAt uint64 `json:"observedAt,omitempty"`

	// The store's observedAt at the end of the cycle, read from the chain rather
	// than taken from any machine. Zero when it could not be read, or before the
	// first update.
	StoreObservedAtAfter uint64 `json:"storeObservedAtAfter"`

	// Dropped names the answers that arrived but did not go up, each with the
	// guard that would have rejected it: a signature recovering to nobody or to
	// a machine this cycle did not ask, a wrong extension or feed, a stale
	// configuration commitment, a repeated signer, an observedAt outside the
	// batch. One bad answer costs its machine the round, not the fleet.
	Dropped []Dropped `json:"dropped,omitempty"`

	// Non-zero when transactions are broadcast but not mined. A stuck one blocks
	// every later transaction behind its nonce.
	PendingNonceGap uint64 `json:"pendingNonceGap,omitempty"`

	// KeeperBalance is what the paying account held at the start of the cycle,
	// in wei as a decimal string. Emitted so the monitoring layer can see the
	// wallet draining before it empties; the keeper itself only warns.
	KeeperBalance string `json:"keeperBalance,omitempty"`

	// Human-readable. Never parsed; route on Outcome and ErrorClass instead.
	Detail string `json:"detail,omitempty"`
}

// Dropped is one answer left out of the submission and the store guard that
// would have rejected it. TeeID is empty when the signature recovered to nobody.
type Dropped struct {
	TeeID  string `json:"teeId,omitempty"`
	Guard  string `json:"guard"`
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
	if err := e.enc.Encode(r); err != nil {
		logger.Warnf("writing the cycle record: %s", err)
	}
}
