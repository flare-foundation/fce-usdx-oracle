// Package types contains types that could be useful to other apps when interacting with this extension.
package types

import (
	"github.com/ethereum/go-ethereum/common"
)

// GetPriceResponse is the JSON payload returned in ActionResult.Data for a
// GET_PRICE action.
//
// SCAFFOLD: this is the Phase 2 stub shape. The final wire format is the
// ABI-encoded PriceReport plus a tee-node signature (spec §WP3):
//
//	data = abi.encode(abi.encode(report), signature)
//
// That lands with the real oracle logic, together with UsdxPriceStore which has
// to decode it. Anything consuming this shape today should expect it to change.
type GetPriceResponse struct {
	// Price is truncate(price * 10^Decimals), as a decimal string.
	//
	// A string, not a number: the on-chain type is uint256, which does not fit a
	// float64, and JSON numbers are float64 in most parsers. At $1.00 and 8
	// decimals it would fit today — encoding it as a string anyway keeps the
	// contract honest for every consumer that later reads a real uint256.
	Price string `json:"price"`

	// Decimals is the fixed-point scale of Price.
	Decimals int8 `json:"decimals"`

	// ObservedAt is when the TEE observed the inputs, as a Unix timestamp. This
	// is the price store's monotonicity key, so it must be part of the signed
	// body once signing is wired up.
	ObservedAt uint64 `json:"observedAt"`
}

// State holds the extension's observable state, returned by GET /state.
//
// This struct is PUBLIC. tee-node polls GET /state and folds the response into
// TEEState.state, which is attested and then republished by the TEE proxy at
// GET /info (spec §4.1). Nothing here may carry raw supply figures, reserve
// components, API responses, credentials or endpoint URLs — only the derived
// price and non-sensitive operational health.
type State struct {
	// LastPrice is the most recently published price, same encoding as
	// GetPriceResponse.Price. Empty string when nothing has been published yet.
	LastPrice string `json:"lastPrice"`

	// LastDecimals is the scale of LastPrice.
	LastDecimals int8 `json:"lastDecimals"`

	// LastObservedAt is the observation timestamp of LastPrice. Consumers use
	// this to detect a frozen feed: a price that stops advancing is the designed
	// failure mode, not an outage (spec §7.15).
	LastObservedAt uint64 `json:"lastObservedAt"`

	// UpdateCount and FailureCount are cumulative since process start.
	UpdateCount  int `json:"updateCount"`
	FailureCount int `json:"failureCount"`

	// LastErrorClass is one of the config.ErrClass* constants — a closed
	// taxonomy, never a raw error string, because this field is world-readable.
	LastErrorClass string `json:"lastErrorClass"`

	// Readiness reports provisioning state. Credentials are memory-only, so the
	// extension returns to unprovisioned after every restart and this is what
	// distinguishes "never configured" from "configured but a source is down".
	// It names secret refs, never their values.
	Readiness Readiness `json:"readiness"`

	// SourceHealth maps an upstream's stable name to one of config.Health*.
	// Names are identifiers like "hex_bonds" or "rpc_flare" — never URLs, which
	// would disclose which providers and API keys are in use.
	SourceHealth map[string]string `json:"sourceHealth"`
}

// Readiness is the provisioning state reported by GET /state.
type Readiness struct {
	EndpointsSet bool     `json:"endpointsSet"`
	SecretsSet   bool     `json:"secretsSet"`
	Ready        bool     `json:"ready"`
	MissingRefs  []string `json:"missingSecretRefs"`
}

// --- DO NOT MODIFY below this line. ---

// StateResponse is the envelope returned by GET /state.
type StateResponse struct {
	StateVersion common.Hash `json:"stateVersion"`
	State        State       `json:"state"`
}
