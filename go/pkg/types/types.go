// Package types contains types that could be useful to other apps when interacting with this extension.
package types

import (
	"github.com/ethereum/go-ethereum/common"
)

// GetPriceResponse is the stub shape returned in ActionResult.Data. The real
// wire format is abi.encode(abi.encode(report), signature) — see internal/report.
type GetPriceResponse struct {
	// truncate(price * 10^Decimals) as a decimal string, not a number: the
	// on-chain type is uint256 and JSON numbers are float64 in most parsers.
	Price string `json:"price"`

	Decimals int8 `json:"decimals"`

	// Unix timestamp of the observation; the price store's monotonicity key.
	ObservedAt uint64 `json:"observedAt"`
}

// State is PUBLIC: tee-node polls GET /state, folds it into TEEState.state, and
// the proxy republishes it at GET /info. Nothing here may carry raw supply
// figures, reserve components, API responses, credentials or endpoint URLs.
type State struct {
	LastPrice    string `json:"lastPrice"`
	LastDecimals int8   `json:"lastDecimals"`

	// Consumers use this to detect a frozen feed, which is the designed failure
	// mode rather than an outage.
	LastObservedAt uint64 `json:"lastObservedAt"`

	UpdateCount  int `json:"updateCount"`
	FailureCount int `json:"failureCount"`

	// One of the failure.Class constants — a closed taxonomy, never a raw error
	// string, because this field is world-readable.
	LastErrorClass string `json:"lastErrorClass"`

	// Names secret refs, never their values. Credentials are memory-only, so
	// this is what distinguishes "never configured" from "source is down".
	Readiness Readiness `json:"readiness"`

	// Stable names like "hex_bonds" or "rpc_flare" — never URLs, which would
	// disclose which providers and API keys are in use.
	SourceHealth map[string]string `json:"sourceHealth"`
}

type Readiness struct {
	EndpointsSet bool `json:"endpointsSet"`

	// keccak256 of the active SET_ENDPOINTS payload. Compare against the
	// EndpointsSet event to confirm this machine runs what was published.
	EndpointsHash string `json:"endpointsHash"`

	AdminsSet bool `json:"adminsSet"`

	// keccak256 of the active SET_ADMINS payload. Compare against the AdminsSet
	// event the same way. Addresses, never keys — nothing here is secret.
	AdminsHash string `json:"adminsHash"`

	Ready       bool     `json:"ready"`
	MissingRefs []string `json:"missingSecretRefs"`
}

// --- DO NOT MODIFY below this line. ---

// StateResponse is the envelope returned by GET /state.
type StateResponse struct {
	StateVersion common.Hash `json:"stateVersion"`
	State        State       `json:"state"`
}
