package protocol

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// ToHash copies into a fixed array, so a name longer than 32 bytes is silently
// truncated — and the truncated form matches nothing the sender dispatches, so
// the action falls through to "unsupported op" with no clue why.
func TestOpHashesAreWellFormed(t *testing.T) {
	ops := map[string]common.Hash{
		"TEE_ORACLE":    OPTypeTeeOracle,
		"GET_FEED":      OPCommandGetFeed,
		"GET_READINESS": OPCommandGetReadiness,
		"SET_ENDPOINTS": OPCommandSetEndpoints,
		"SET_ADMINS":    OPCommandSetAdmins,
		"SET_SECRET":    OPCommandSetSecret,
	}

	seen := make(map[common.Hash]string, len(ops))
	for name, h := range ops {
		if len(name) > 32 {
			t.Errorf("%q is %d bytes and would be truncated", name, len(name))
		}
		// Right-padded ASCII, not keccak: the sender declares these as
		// bytes32("TEE_ORACLE"), so a hashed form would match nothing.
		if got := OpName(h); got != name {
			t.Errorf("OpName(%x) = %q, want %q", h, got, name)
		}
		if prev, dup := seen[h]; dup {
			t.Errorf("%q and %q collide on %x", name, prev, h)
		}
		seen[h] = name
	}
}
