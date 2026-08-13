package main

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

// The SET_ADMINS payload schema, restated here rather than imported, for the
// same reason send-endpoints restates its own: tools/ is deliberately
// independent of any one language implementation, and importing the Go
// extension's internals would make this tool unusable against the others. The
// cost is that this must be kept in step with internal/runtimecfg by hand.
//
// The check matters more here than for endpoints. Publishing a bad admin set is
// silent in the same way — the transaction succeeds, expectedAdminsHash is
// written, and only then does the extension reject it — but the machine is left
// running the PREVIOUS admin set while the chain expects the new one, so every
// submitPrice reverts StaleAdmins and no SET_SECRET signed by the new admins can
// land either. A rotation that half-happened is worse than one that did not
// start.
type admins struct {
	Version uint64 `json:"version"`

	// Governs the pinned backing credential (hex_api_key) only.
	Backing adminSet `json:"backing"`

	// Governs every other credential — the RPC provider keys named by
	// SET_ENDPOINTS, and any ref the extension does not recognise.
	Providers adminSet `json:"providers"`
}

type adminSet struct {
	Threshold int      `json:"threshold"`
	Admins    []string `json:"admins"`
}

// Mirrors provision.MinBackingThreshold, provision.MinProviderThreshold and
// provision.MaxAdmins.
//
// The two floors differ by decision. The backing credential authenticates a
// PINNED endpoint by header, so a wrong value degrades the feed to cash-less
// rather than moving the price; provider credentials are named by SET_ENDPOINTS
// and a rogue value denies service on every chain that uses it.
const (
	minBackingThreshold  = 1
	minProviderThreshold = 2
	maxAdmins            = 32
)

// hexAPIKeyRef is the one credential the backing set governs. Stated here only
// so the summary can name it; the mapping that matters is pinned in the
// extension build and is NOT carried in this payload — otherwise whoever
// published admins could move a credential under whichever set they hold.
const hexAPIKeyRef = "hex_api_key"

// parseAdmins decodes the payload the way the extension does, rejecting unknown
// fields: an unrecognised field means the publisher and the running build
// disagree about the schema.
func parseAdmins(raw []byte) (admins, error) {
	var a admins

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return admins{}, errors.Errorf("payload is not a known admin set: %s", err)
	}
	return a, nil
}

// validateAdmins mirrors runtimecfg.validateAdmins. Kept in the same order so
// the two can be diffed by eye.
func validateAdmins(a admins) error {
	// Zero is the extension's "never applied" sentinel, so it could never be
	// accepted; the version must also exceed whatever that machine last applied,
	// which only the machine knows.
	if a.Version == 0 {
		return errors.New("version must be non-zero")
	}
	if err := validateSet("backing", a.Backing, minBackingThreshold); err != nil {
		return err
	}
	return validateSet("providers", a.Providers, minProviderThreshold)
}

func validateSet(name string, in adminSet, floor int) error {
	if len(in.Admins) == 0 || len(in.Admins) > maxAdmins {
		return errors.Errorf("%s admin count %d is not between 1 and %d",
			name, len(in.Admins), maxAdmins)
	}
	if in.Threshold < floor || in.Threshold > len(in.Admins) {
		return errors.Errorf("%s threshold %d is not between %d and %d",
			name, in.Threshold, floor, len(in.Admins))
	}

	seen := make(map[common.Address]bool, len(in.Admins))
	for _, raw := range in.Admins {
		text := strings.TrimSpace(raw)
		// common.HexToAddress silently yields the zero address for anything it
		// cannot parse, so a typo would install a signer nobody holds and
		// quietly shrink the effective set.
		if !common.IsHexAddress(text) {
			return errors.Errorf("%s: %q is not a hex address", name, raw)
		}
		addr := common.HexToAddress(text)
		if addr == (common.Address{}) {
			return errors.Errorf("%s: admin address is zero", name)
		}
		// A repeated address inflates the apparent set size while the
		// distinct-signer check still counts it once, so a 2-of-3 naming the
		// same key twice is really 2-of-2 with one key unusable.
		if seen[addr] {
			return errors.Errorf("%s: %s appears more than once", name, addr.Hex())
		}
		seen[addr] = true
	}
	return nil
}

// summarise renders what was actually parsed, so the operator sees the sets the
// extension will see rather than the bytes they thought they wrote — including
// which credentials each set governs, which is the part most easily got wrong.
func summarise(a admins) string {
	var b strings.Builder
	b.WriteString("version " + strconv.FormatUint(a.Version, 10) + "\n\n")
	b.WriteString(setBlock("backing", a.Backing, "governs "+hexAPIKeyRef+" only"))
	b.WriteString(setBlock("providers", a.Providers, "governs every other credential"))
	return b.String()
}

func setBlock(name string, s adminSet, governs string) string {
	var b strings.Builder
	b.WriteString(name + ": " + strconv.Itoa(s.Threshold) + "-of-" +
		strconv.Itoa(len(s.Admins)) + " — " + governs + "\n")
	for _, raw := range s.Admins {
		// Checksummed, so an operator comparing against a hardware wallet is
		// comparing the same rendering.
		b.WriteString("  " + common.HexToAddress(strings.TrimSpace(raw)).Hex() + "\n")
	}
	return b.String()
}
