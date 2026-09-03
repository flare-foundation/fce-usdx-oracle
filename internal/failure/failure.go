// Package failure defines the extension's complete public error vocabulary.
// ActionResult.Log and GET /state are world-readable, so Failure has unexported
// fields and no constructor: only the values below exist. Log debug detail apart.
//
// Not the only route to that field, though: a non-200 from POST /action reaches
// Log too, because tee-node wraps the response body into a status-0 result. The
// two 501 handlers in internal/extension are the only paths that do it, and the
// comments there say why they may.
package failure

import "errors"

// Coerce returns the Failure err carries, or fallback if it carries none.
//
// The single conversion from an arbitrary error into something publishable, so
// there is one place to look when asking whether a surface can leak. An error
// from outside the vocabulary — a *url.Error holding an API key, a parse error
// holding a reserve figure — becomes fallback and its text is dropped.
//
// A nil err yields fallback rather than a zero Failure, which would read as
// "no failure" on a path that has already decided there was one.
func Coerce(err error, fallback Failure) Failure {
	var f Failure
	if errors.As(err, &f) {
		return f
	}
	return fallback
}

// Class is the coarse taxonomy reported by GET /state as lastErrorClass.
type Class string

const (
	ClassNone          Class = ""
	ClassSourceDown    Class = "source_unavailable"
	ClassSourceInvalid Class = "source_invalid"
	ClassQuorumFailed  Class = "supply_quorum_failed"
	ClassImplausible   Class = "implausible_value"
	ClassSignFailed    Class = "sign_failed"
	ClassInternal      Class = "internal"

	// An operational state rather than a source fault, so monitoring can route it
	// differently.
	ClassNotConfigured Class = "not_configured"

	// Also not a source fault: the fix is on the machine (NTP), not upstream and
	// not in this code.
	ClassClockSkew Class = "clock_skew"
)

// Failure is a fault that is safe to describe publicly. Error() text is always a
// compile-time constant, never formatted from runtime data.
type Failure struct {
	class  Class
	reason string
}

func (f Failure) Error() string { return f.reason }

func (f Failure) Class() Class { return f.class }

func (f Failure) IsZero() bool { return f.class == ClassNone && f.reason == "" }

// The complete public error vocabulary. Wording rule: name the subsystem and the
// nature of the fault, never the value, the endpoint, or the threshold.
var (
	// --- instruction handling ---

	UnexpectedPayload = Failure{ClassSourceInvalid, "instruction carries an unexpected payload"}

	// Accepting a command over the other channel would bypass its authorisation.
	WrongActionType = Failure{ClassSourceInvalid, "command arrived over the wrong action type"}

	// The ONLY failure a provisioning command may report: one reason for every
	// cause, so it cannot be used to probe the admin set or the freshness window.
	ProvisionRejected = Failure{ClassSourceInvalid, "provisioning envelope rejected"}

	// Credentials are memory-only, so this is expected after every restart.
	NotConfigured = Failure{ClassNotConfigured, "extension is not provisioned"}

	// --- backing sources (Hex APIs) ---

	BackingUnavailable = Failure{ClassSourceDown, "backing source unavailable"}
	BackingTimeout     = Failure{ClassSourceDown, "backing source timed out"}
	BackingMalformed   = Failure{ClassSourceInvalid, "backing source returned a malformed response"}
	BackingCurrency    = Failure{ClassSourceInvalid, "backing source reported an unexpected currency"}

	// There is no "backing data is stale" — an old figure is usually the correct
	// one. Only a FUTURE stamp is a fault, and it means a broken upstream or a
	// skewed clock, so it stays distinct from BackingMalformed.
	BackingTimestamp = Failure{ClassSourceInvalid, "backing source reported a timestamp in the future"}

	BackingImplausible = Failure{ClassImplausible, "backing source returned an implausible value"}

	// --- supply reads (RPC) ---

	SupplyUnavailable = Failure{ClassSourceDown, "supply source unavailable"}
	SupplyTimeout     = Failure{ClassSourceDown, "supply source timed out"}
	SupplyMalformed   = Failure{ClassSourceInvalid, "supply source returned a malformed response"}
	SupplyNoQuorum    = Failure{ClassQuorumFailed, "supply providers did not reach quorum"}
	SupplyImplausible = Failure{ClassImplausible, "supply read produced an implausible value"}

	// Distinct from the quorum failures because the reads all SUCCEED and AGREE:
	// this catches a provider lying about which block to read, not about what is in it.
	SupplyBlockStale = Failure{ClassSourceInvalid, "supply block is too far behind"}

	// --- observation consistency ---

	WindowExceeded = Failure{ClassSourceInvalid, "observation window exceeded"}
	Deadline       = Failure{ClassSourceDown, "observation deadline exceeded"}

	// Distinct from NotConfigured: provisioning may be complete and the first
	// observation simply not finished yet.
	NoObservation = Failure{ClassSourceDown, "no observation available yet"}

	// Signing a stale observation would publish a price whose timestamp says it is
	// fresh; failing closed lets the on-chain value age visibly instead.
	ObservationStale = Failure{ClassSourceDown, "no recent observation"}

	// --- clock agreement with the requesting instruction ---

	// Fails closed rather than treating a zero as "no bound".
	MissingRequestTime = Failure{ClassSourceInvalid, "instruction carries no block timestamp"}

	// Observation older than the requesting block by more than MaxObservationAge
	// while the LOCAL bound passed — only a slow clock does that. Bounded this
	// direction only; the store's MAX_FUTURE_SKEW covers the other.
	ClockBehindChain = Failure{ClassClockSkew, "machine clock is behind chain time"}

	// --- price computation ---

	SupplyZero       = Failure{ClassImplausible, "supply is zero"}
	PriceImplausible = Failure{ClassImplausible, "computed price is outside the plausible range"}

	// --- signing ---

	SignUnavailable = Failure{ClassSignFailed, "signing service unavailable"}
	SignRejected    = Failure{ClassSignFailed, "signing service rejected the request"}

	// --- catch-all ---

	Internal = Failure{ClassInternal, "internal error"}
)

// All is every declared Failure. Tests walk it to assert the vocabulary stays
// publishable; nothing in production depends on the order.
var All = []Failure{
	UnexpectedPayload, WrongActionType,
	ProvisionRejected, NotConfigured,
	BackingUnavailable, BackingTimeout, BackingMalformed,
	BackingCurrency, BackingTimestamp, BackingImplausible,
	SupplyUnavailable, SupplyTimeout, SupplyMalformed,
	SupplyNoQuorum, SupplyImplausible, SupplyBlockStale,
	WindowExceeded, Deadline,
	NoObservation, ObservationStale,
	MissingRequestTime, ClockBehindChain,
	SupplyZero, PriceImplausible,
	SignUnavailable, SignRejected,
	Internal,
}
