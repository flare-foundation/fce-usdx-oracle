package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"fce-usdx-oracle/pkg/protocol"
	"fce-usdx-oracle/pkg/wire/configpayload"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

// The SET_ADMINS payload schema, restated for the same reason send-endpoints
// restates its own. Worse to get wrong: a rejected rotation leaves the machine on
// the previous set, so updates revert StaleAdmins and no new SET_SECRET lands.
//
// The on-chain payload is ABI-encoded by the sender into Admins{version, feedId,
// roles}; the contract assigns the version. This tool takes JSON for convenience
// and builds the typed AdminRole list. The operator does NOT pick the version:
// the JSON carries none, and a top-level "version" number does not parse.
//
// Each role is a tagged signer set:
//
//	{
//	  "backing":   { "threshold": 1, "admins": ["0x…"] },
//	  "providers": { "threshold": 2, "admins": ["0x…", "0x…"] }
//	}
type adminSetConfig struct {
	Threshold int      `json:"threshold"`
	Admins    []string `json:"admins"`
}

// Mirrors the floors and ceilings pkg/protocol pins and the enclave's verifier
// enforces (MinBackingThreshold, MinProviderThreshold, MaxAdmins, MaxThreshold).
// The floors differ by decision: a wrong backing credential degrades the feed
// to cash-less, a rogue provider credential denies service on every chain using
// it. maxThreshold is the extension's signature cap: a set asking for more
// signatures than it will look at can never be satisfied.
const (
	minBackingThreshold  = 1
	minProviderThreshold = 2
	maxAdmins            = 32
	maxThreshold         = 16
)

// knownRoles must both be present. Which credentials each governs is pinned in
// the extension build (protocol.BackingRefs), not here; this tool only checks
// the shape.
var knownRoles = []string{"backing", "providers"}

// parseAdmins decodes the operator's JSON into the typed role list.
func parseAdmins(raw []byte) ([]configpayload.Role, error) {
	var a map[string]adminSetConfig
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, errors.Errorf("payload is not a known admin set: %s", err)
	}

	roles := make([]configpayload.Role, 0, len(a))
	for _, name := range sortedRoleKeys(a) {
		role, err := buildRole(name, a[name])
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// buildRole turns one config entry into a typed AdminRole, validating it.
func buildRole(name string, sc adminSetConfig) (configpayload.Role, error) {
	tag, err := configpayload.Tag(name)
	if err != nil {
		return configpayload.Role{}, errors.Errorf("role %q: %s", name, err)
	}
	threshold, err := configpayload.Threshold(sc.Threshold)
	if err != nil {
		return configpayload.Role{}, errors.Errorf("role %q: %s", name, err)
	}

	admins := make([]common.Address, 0, len(sc.Admins))
	for _, raw := range sc.Admins {
		text := strings.TrimSpace(raw)
		// common.HexToAddress silently yields the zero address for anything it
		// cannot parse, so a typo would install a signer nobody holds and quietly
		// shrink the effective set.
		if !common.IsHexAddress(text) {
			return configpayload.Role{}, errors.Errorf("role %q: %q is not a hex address", name, raw)
		}
		admins = append(admins, common.HexToAddress(text))
	}

	return configpayload.Role{Role: tag, Threshold: threshold, Admins: admins}, nil
}

// validateAdmins checks the role set against the rules the extension build pins.
func validateAdmins(roles []configpayload.Role) error {
	if len(roles) == 0 {
		return errors.New("no roles configured")
	}

	seen := make(map[string]bool, len(roles))
	for _, r := range roles {
		name := configpayload.TagName(r.Role)
		if seen[name] {
			return errors.Errorf("role %q appears more than once", name)
		}
		seen[name] = true

		floor, ok := floorFor(name)
		if !ok {
			return errors.Errorf("unknown role %q (expected one of %s)",
				name, strings.Join(knownRoles, ", "))
		}
		if err := validateSet(name, r, floor); err != nil {
			return err
		}
	}

	for _, name := range knownRoles {
		if !seen[name] {
			return errors.Errorf("role %q is missing", name)
		}
	}
	return nil
}

func validateSet(name string, r configpayload.Role, floor int) error {
	if len(r.Admins) == 0 || len(r.Admins) > maxAdmins {
		return errors.Errorf("role %q admin count %d is not between 1 and %d",
			name, len(r.Admins), maxAdmins)
	}
	// The set size is not the only ceiling: past maxThreshold the extension drops
	// the envelope before counting signers, so the rotation would land and every
	// SET_SECRET after it would bounce with no reason given.
	ceiling := min(len(r.Admins), maxThreshold)
	if int(r.Threshold) < floor || int(r.Threshold) > ceiling {
		return errors.Errorf("role %q threshold %d is not between %d and %d",
			name, r.Threshold, floor, ceiling)
	}

	seen := make(map[common.Address]bool, len(r.Admins))
	for _, addr := range r.Admins {
		if addr == (common.Address{}) {
			return errors.Errorf("role %q: admin address is zero", name)
		}
		// A repeated address inflates the apparent set size while the
		// distinct-signer check still counts it once, so a 2-of-3 naming the same
		// key twice is really 2-of-2 with one key unusable.
		if seen[addr] {
			return errors.Errorf("role %q: %s appears more than once", name, addr.Hex())
		}
		seen[addr] = true
	}
	return nil
}

// summarise renders what was actually parsed, so the operator sees the sets the
// extension will see rather than the bytes they thought they wrote — including
// which credentials each set governs, which is the part most easily got wrong.
func summarise(roles []configpayload.Role) string {
	var b strings.Builder
	for _, r := range roles {
		name := configpayload.TagName(r.Role)
		governs := "governs every other credential"
		if name == protocol.RoleBacking {
			governs = "governs " + joinAnd(protocol.BackingRefs())
		}
		b.WriteString(name + ": " + strconv.FormatUint(r.Threshold, 10) + "-of-" +
			strconv.Itoa(len(r.Admins)) + " — " + governs + "\n")
		for _, addr := range r.Admins {
			// Checksummed, so an operator comparing against a hardware wallet is
			// comparing the same rendering.
			b.WriteString("  " + addr.Hex() + "\n")
		}
	}
	return b.String()
}

// joinAnd lists names in prose: "a", "a and b", "a, b and c".
func joinAnd(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func floorFor(name string) (int, bool) {
	switch name {
	case "backing":
		return minBackingThreshold, true
	case "providers":
		return minProviderThreshold, true
	default:
		return 0, false
	}
}

func sortedRoleKeys(a map[string]adminSetConfig) []string {
	names := make([]string, 0, len(a))
	for name := range a {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
