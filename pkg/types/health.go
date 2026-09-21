package types

import "fmt"

// Source health per upstream in the readiness report. Unknown is the zero value: a source
// that has never been polled must not read as healthy.
const (
	HealthUnknown = "unknown"
	HealthOK      = "ok"
	HealthDown    = "down"

	// Answered but lost the agreement vote: a WRONG value is a different problem
	// from no value.
	HealthDisagree = "disagree"

	// Head below the pinned block. Tolerated, but recorded: it is also how a
	// rewind looks.
	HealthLagging = "lagging"

	// Never provisioned, as opposed to broken: "down" pages someone,
	// "not_configured" tells them to send a SET_SECRET.
	HealthNotConfigured = "not_configured"
)

// ChainHealthKey and ProviderHealthKey name a chain and one of its RPC
// providers in the sourceHealth map the readiness report carries.
//
// Both live beside the report because both are public surface and have to
// agree: the chain reader and the observation each build keys, and one prefix
// defined once keeps the map to one naming scheme.
//
// Positional, never operator-supplied: a provider is "rpc_flare_1", so nothing
// written into a SET_ENDPOINTS payload can reach a world-readable surface.
func ChainHealthKey(chain string) string { return "rpc_" + chain }

func ProviderHealthKey(chain string, index int) string {
	return fmt.Sprintf("rpc_%s_%d", chain, index)
}
