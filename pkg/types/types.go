// Package types contains types that could be useful to other apps when interacting with this extension.
package types

import (
	"encoding/json"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// State is PUBLIC: tee-node reads GET /state on every TEE info request, carries
// it as TeeState.State, and the proxy republishes it at GET /info. Nothing here
// may carry raw supply figures, reserve components, API responses, credentials
// or endpoint URLs.
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

// ReadinessReport is the JSON payload of a signed GET_READINESS answer — see
// internal/wire/readiness. Public on the same terms as State, and deliberately not a copy
// of it: State's counters come from the GET_FEED handler, these from the poller.
type ReadinessReport struct {
	// --- provisioning, from the runtime configuration store ---
	Readiness Readiness `json:"readiness"`

	// --- observation, from the poller ---

	// LastObservedAt is when the inputs behind the newest observation were
	// read; zero if there has never been one.
	LastObservedAt uint64 `json:"lastObservedAt"`

	// ObservationAge is seconds since LastObservedAt at the moment of
	// answering. The number to alert on: the feed's designed failure mode is
	// freezing, not erroring.
	ObservationAge uint64 `json:"observationAge"`

	// WouldSign reports whether a GET_FEED issued now would succeed — the
	// newest observation exists and is inside MaxObservationAge. A keeper can
	// skip a cycle it would otherwise pay for.
	WouldSign bool `json:"wouldSign"`

	// Observations and Failures are the poller's cumulative counters.
	Observations int `json:"observations"`
	Failures     int `json:"failures"`

	// LastErrorClass is a failure.Class constant, empty when the newest
	// observation succeeded. A closed taxonomy, never a raw error.
	LastErrorClass string `json:"lastErrorClass"`

	// SourceHealth maps each positional source name to a config.Health* value.
	// Positional so nothing an endpoint author supplies reaches this surface.
	SourceHealth map[string]string `json:"sourceHealth"`

	// --- identity ---

	// ChainID is the network the machine binds its signatures to. The machine
	// itself is identified by recovering the signature, not by a field here.
	ChainID uint64 `json:"chainId"`
}

// --- DO NOT MODIFY below this line: this is the shape tee-node decodes. ---

// StateResponse is the envelope returned by GET /state. It is the extension
// half of tee-node's TeeState: tee-node decodes this body as that type, takes
// State and StateVersion, and carries them opaquely into the attested TEE info.
// State is therefore bytes, not an object; they hold the JSON encoding of State
// (EncodeState), and StateVersion names that schema.
type StateResponse struct {
	StateVersion common.Hash   `json:"stateVersion"`
	State        hexutil.Bytes `json:"state"`
}

// EncodeState returns the bytes StateResponse.State carries for s.
func EncodeState(s State) (hexutil.Bytes, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return hexutil.Bytes(b), nil
}

// DecodeState reverses EncodeState. Consumers read the bytes out of the proxy's
// /info (teeInfo.state.state), where tee-node placed them.
func DecodeState(b hexutil.Bytes) (State, error) {
	if len(b) == 0 {
		return State{}, errors.New("empty state")
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, err
	}
	return s, nil
}
