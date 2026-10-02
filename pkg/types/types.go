// Package types holds the JSON shapes the extension shares with its tooling:
// the provisioning readiness block, the signed readiness report with its
// source-health vocabulary, and the envelope tee-node reads from GET /state.
//
// Everything here is public on the wire. Nothing may carry raw supply figures,
// reserve components, API responses, credentials or endpoint URLs.
package types

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// Readiness is the provisioning half of a machine's account of itself: which
// configuration generation it runs and which secrets it still lacks.
type Readiness struct {
	EndpointsSet bool `json:"endpointsSet"`
	// keccak256 of the active SET_ENDPOINTS payload. Compare against the
	// EndpointsSet event to confirm this machine runs what was published.
	EndpointsHash string `json:"endpointsHash"`
	AdminsSet     bool   `json:"adminsSet"`
	// keccak256 of the active SET_ADMINS payload. Compare against the AdminsSet
	// event the same way. Addresses, never keys — nothing here is secret.
	AdminsHash string `json:"adminsHash"`
	Ready      bool   `json:"ready"`
	// Names secret refs, never their values. Credentials are memory-only, so
	// this is what distinguishes "never configured" from "source is down".
	MissingRefs []string `json:"missingSecretRefs"`
}

// ReadinessReport is the JSON payload of a signed GET_READINESS answer — see
// pkg/wire/readiness. It is the machine's one account of itself: the
// provisioning block from the runtime configuration store and the observation
// side from the poller, signed by the enclave and bound to the request.
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

	// WouldSign reports whether this machine would sign a GET_FEED for the
	// newest observation
	WouldSign bool `json:"wouldSign"`

	// Observations and Failures are the poller's cumulative counters.
	Observations int `json:"observations"`
	Failures     int `json:"failures"`

	// LastErrorClass is a failure.Class constant, empty when the newest
	// observation succeeded. A closed taxonomy, never a raw error.
	LastErrorClass string `json:"lastErrorClass"`

	// SourceHealth maps each positional source name to a Health* value.
	// Positional so nothing an endpoint author supplies reaches this surface.
	SourceHealth map[string]string `json:"sourceHealth"`

	// ChainID is the chain the machine is configured for, so a report cannot be
	// mistaken for another deployment's.
	ChainID uint64 `json:"chainId"`
}

// StateResponse is the body of GET /state, in the shape tee-node decodes as its
// own TeeState and carries into the attested TEE info. The route exists because
// tee-node reads it on every info request and the registry's availability check
// verifies what it carries; this extension carries nothing — the zero version
// and no bytes — because its account of itself is the signed readiness report.
type StateResponse struct {
	StateVersion common.Hash   `json:"stateVersion"`
	State        hexutil.Bytes `json:"state"`
}
