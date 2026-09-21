package envelope_test

import (
	"math/big"
	"strings"
	"testing"

	"fce-usdx-oracle/internal/provisioning/envelope"
	"fce-usdx-oracle/pkg/protocol"

	"github.com/ethereum/go-ethereum/common"
)

// The floors are the only guard on a set's threshold, so a policy that could
// never install a set must be refused where it is built, not at the first
// SET_ADMINS.
func TestNewVerifierRefusesAnUnusablePolicy(t *testing.T) {
	for name, p := range map[string]protocol.Policy{
		"zero backing floor":                {MinBacking: 0, MinProviders: 2},
		"zero providers floor":              {MinBacking: 1, MinProviders: 0},
		"floor above protocol.MaxThreshold": {MinBacking: 1, MinProviders: protocol.MaxThreshold + 1},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				msg, _ := r.(string)
				if r == nil || !strings.Contains(msg, "floor") {
					t.Fatalf("expected a panic naming the floor, got %v", r)
				}
			}()
			envelope.NewVerifier(big.NewInt(114), common.Address{1}, p)
		})
	}
}

// A ref the policy does not name falls under the stronger set, and the floor
// follows the set — the rule usdxprov applies before an envelope is sent.
func TestPolicyRoutesRefsToTheirSet(t *testing.T) {
	p := protocol.Policy{BackingRefs: []string{"hex_api_key"}, MinBacking: 1, MinProviders: 3}
	if got := p.RoleFor("hex_api_key"); got != protocol.RoleBacking {
		t.Errorf("hex_api_key → %q, want %q", got, protocol.RoleBacking)
	}
	if got := p.RoleFor("never_heard_of_it"); got != protocol.RoleProviders {
		t.Errorf("unknown ref → %q, want the stronger set %q", got, protocol.RoleProviders)
	}
	if got := p.MinThresholdFor("hex_api_key"); got != 1 {
		t.Errorf("backing floor = %d, want 1", got)
	}
	if got := p.MinThresholdFor("rpc_flare_1"); got != 3 {
		t.Errorf("providers floor = %d, want 3", got)
	}
	if _, known := p.FloorFor("auditors"); known {
		t.Error("an unknown role must not have a floor")
	}
}
