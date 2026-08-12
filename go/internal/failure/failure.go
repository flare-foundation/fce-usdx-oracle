// Package failure defines the extension's complete public error vocabulary.
//
// ActionResult.Log and GET /state are both world-readable, and ordinary Go
// error strings carry exactly what must not appear there — API keys inside
// *url.Error, reserve figures inside parse errors. A Failure has unexported
// fields and no exported constructor, so it can only be one of the values
// declared below: the compiler, not review, keeps runtime data out.
//
// Debug detail does not belong in a Failure. Log it separately, and only
// values known to be safe — source name, HTTP status, duration.
package failure

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

	// Not a fault in a data source but an operational state, so monitoring can route it differently.
	ClassNotConfigured Class = "not_configured"
)

// Failure is a fault that is safe to describe publicly. Error() text is always
// a compile-time constant, never formatted from runtime data.
type Failure struct {
	class  Class
	reason string
}

func (f Failure) Error() string { return f.reason }

func (f Failure) Class() Class { return f.class }

func (f Failure) IsZero() bool { return f.class == ClassNone && f.reason == "" }

// The complete public error vocabulary. Wording rule: name the subsystem and
// the nature of the fault, never the value, the endpoint, or the threshold.
var (
	// --- instruction handling ---

	UnexpectedPayload = Failure{ClassSourceInvalid, "instruction carries an unexpected payload"}

	// SET_SECRET is direct-only and SET_ENDPOINTS instruction-only; accepting
	// either over the other channel would bypass its authorisation path.
	WrongActionType = Failure{ClassSourceInvalid, "command arrived over the wrong action type"}

	// --- provisioning ---

	// The ONLY failure a provisioning command may report. One reason for every
	// cause, so the endpoint cannot be used to probe the admin set or the
	// freshness window.
	ProvisionRejected = Failure{ClassSourceInvalid, "provisioning envelope rejected"}

	// Credentials are memory-only, so this is the expected state after every restart.
	NotConfigured = Failure{ClassNotConfigured, "extension is not provisioned"}

	// --- backing sources (Hex APIs) ---

	BackingUnavailable = Failure{ClassSourceDown, "backing source unavailable"}
	BackingTimeout     = Failure{ClassSourceDown, "backing source timed out"}
	BackingMalformed   = Failure{ClassSourceInvalid, "backing source returned a malformed response"}
	BackingStale       = Failure{ClassSourceInvalid, "backing source data is stale"}
	BackingCurrency    = Failure{ClassSourceInvalid, "backing source reported an unexpected currency"}
	BackingImplausible = Failure{ClassImplausible, "backing source returned an implausible value"}

	// --- supply reads (RPC) ---

	SupplyUnavailable = Failure{ClassSourceDown, "supply source unavailable"}
	SupplyTimeout     = Failure{ClassSourceDown, "supply source timed out"}
	SupplyMalformed   = Failure{ClassSourceInvalid, "supply source returned a malformed response"}
	SupplyNoQuorum    = Failure{ClassQuorumFailed, "supply providers did not reach quorum"}
	SupplyImplausible = Failure{ClassImplausible, "supply read produced an implausible value"}

	// The block every provider was asked about is too far behind wall-clock.
	//
	// Distinct from the quorum failures because the reads all SUCCEED and all
	// AGREE — a read at a fixed block is deterministic, so honest providers
	// corroborate an old block perfectly. This is the only check that catches a
	// provider lying about which block to read rather than about what is in it,
	// and the only one that works at all when a chain has a single provider.
	SupplyBlockStale = Failure{ClassSourceInvalid, "supply block is too far behind"}

	// --- observation consistency ---

	WindowExceeded = Failure{ClassSourceInvalid, "observation window exceeded"}
	Deadline       = Failure{ClassSourceDown, "observation deadline exceeded"}

	// Distinct from NotConfigured: provisioning may be complete and the first
	// observation simply not finished yet.
	NoObservation = Failure{ClassSourceDown, "no observation available yet"}

	// Signing a stale observation would publish a price whose timestamp says it
	// is fresh; failing closed lets the on-chain value age visibly instead.
	ObservationStale = Failure{ClassSourceDown, "no recent observation"}

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
	BackingStale, BackingCurrency, BackingImplausible,
	SupplyUnavailable, SupplyTimeout, SupplyMalformed,
	SupplyNoQuorum, SupplyImplausible, SupplyBlockStale,
	WindowExceeded, Deadline,
	NoObservation, ObservationStale,
	SupplyZero, PriceImplausible,
	SignUnavailable, SignRejected,
	Internal,
}
