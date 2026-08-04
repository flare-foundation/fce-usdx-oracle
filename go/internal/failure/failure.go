// Package failure defines the extension's complete public error vocabulary.
//
// # Why this package exists
//
// Two of the extension's outputs are world-readable:
//
//   - ActionResult.Log — returned to the TEE proxy and served from its
//     action-result endpoint (docs/extension-contract.md §4.6).
//   - GET /state — polled by tee-node, folded into TEEState.state, attested,
//     and republished by the proxy at GET /info (spec §4.1).
//
// Go error strings routinely carry exactly what must not appear on either.
// Both of the following are real, and both are among the likeliest failures in
// this extension:
//
//	// *url.Error embeds the full request URL, credentials included:
//	Get "https://api.htdigitalassets.com/...?apikey=SECRET": dial tcp: ...
//
//	// A decimal parse error embeds the value it could not parse — and the
//	// public endpoint really does return commas, so this path WILL be taken:
//	parsing reserves "25,452,544.52": not a decimal
//
// The second is the sharper one: spec §4.1 excludes reserve figures from every
// public surface, and a naive `fmt.Errorf("parsing %q: %w", raw, err)` would
// publish the exact number the design is built to withhold.
//
// # How it is enforced
//
// A Failure carries unexported fields and there is no exported constructor, so
// a Failure can only be one of the package-level values declared below. There
// is no code path that puts a runtime value into a public string — not by
// convention or review, but because the compiler rejects it.
//
// Adding a failure mode means adding a value here, which is the point: the
// entire public error surface is one reviewable file.
//
// Detail for debugging does not belong in a Failure. Log it separately, and
// keep to values known to be safe — source name, HTTP status, duration. Never
// the upstream error, its response body, or any parsed figure. Spec §4.1 leaves
// where Confidential Space logs land, and who can read them, unresolved; until
// that is answered, treat logs as disclosing too.
package failure

// Class is the coarse taxonomy reported by GET /state as lastErrorClass.
// Deliberately coarse: it tells an operator which subsystem failed without
// describing the data that caused it.
type Class string

const (
	ClassNone          Class = ""
	ClassSourceDown    Class = "source_unavailable"
	ClassSourceInvalid Class = "source_invalid"
	ClassQuorumFailed  Class = "supply_quorum_failed"
	ClassImplausible   Class = "implausible_value"
	ClassSignFailed    Class = "sign_failed"
	ClassInternal      Class = "internal"

	// ClassNotConfigured means the extension has not been provisioned. Distinct
	// from every other class on purpose: it is an operational state to be
	// resolved by re-provisioning, not a fault in a data source, and monitoring
	// must be able to route it differently.
	ClassNotConfigured Class = "not_configured"
)

// Failure is a fault that is safe to describe publicly.
//
// It implements error so it can flow through ordinary Go control flow, but its
// Error() text is a compile-time constant, never formatted from runtime data.
type Failure struct {
	class  Class
	reason string
}

// Error implements the error interface. The returned string is a constant
// declared in this package and is safe to publish.
func (f Failure) Error() string { return f.reason }

// Class returns the taxonomy bucket for GET /state.
func (f Failure) Class() Class { return f.class }

// IsZero reports whether f is the zero Failure, which is not a real failure.
func (f Failure) IsZero() bool { return f.class == ClassNone && f.reason == "" }

// The complete public error vocabulary.
//
// Wording rule: name the subsystem and the nature of the fault, never the
// value, the endpoint, or the threshold that was crossed. "backing source
// returned an implausible value" is publishable; "backing 25452544.52 exceeds
// cap 30000000" is not — it discloses both a reserve figure and a bound worth
// knowing to anyone probing the oracle.
var (
	// --- instruction handling ---

	UnexpectedPayload = Failure{ClassSourceInvalid, "instruction carries an unexpected payload"}

	// WrongActionType is returned when a command arrives over the wrong channel.
	// SET_SECRET is direct-only and SET_ENDPOINTS is instruction-only; accepting
	// either over the other channel would bypass its authorisation path.
	WrongActionType = Failure{ClassSourceInvalid, "command arrived over the wrong action type"}

	// --- provisioning ---

	// ProvisionRejected is the ONLY failure a provisioning command may report.
	//
	// Deliberately one reason for every cause — bad signature, too few distinct
	// signers, unknown signer, stale issuedAt, replayed version, wrong teeId,
	// wrong chain, wrong domain, undecryptable ciphertext, disallowed host. A
	// caller learns whether provisioning succeeded, never which check failed;
	// distinguishing them would turn the endpoint into an oracle for probing the
	// admin set and the freshness window.
	ProvisionRejected = Failure{ClassSourceInvalid, "provisioning envelope rejected"}

	// NotConfigured means a price was requested before provisioning completed.
	// Credentials are memory-only, so this is the expected state after every
	// restart until the keeper re-submits.
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

	// --- observation consistency ---

	WindowExceeded = Failure{ClassSourceInvalid, "observation window exceeded"}
	Deadline       = Failure{ClassSourceDown, "observation deadline exceeded"}

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
	SupplyNoQuorum, SupplyImplausible,
	WindowExceeded, Deadline,
	SupplyZero, PriceImplausible,
	SignUnavailable, SignRejected,
	Internal,
}
