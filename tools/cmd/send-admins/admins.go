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
	Version   uint64   `json:"version"`
	Threshold int      `json:"threshold"`
	Admins    []string `json:"admins"`
}

// Mirrors provision.MinThreshold and provision.MaxAdmins.
const (
	minThreshold = 2
	maxAdmins    = 32
)

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
	if len(a.Admins) == 0 || len(a.Admins) > maxAdmins {
		return errors.Errorf("admin count %d is not between 1 and %d", len(a.Admins), maxAdmins)
	}
	// M=1 is not a threshold scheme: one key would be the whole root of trust.
	if a.Threshold < minThreshold || a.Threshold > len(a.Admins) {
		return errors.Errorf("threshold %d is not between %d and %d",
			a.Threshold, minThreshold, len(a.Admins))
	}

	seen := make(map[common.Address]bool, len(a.Admins))
	for _, raw := range a.Admins {
		text := strings.TrimSpace(raw)
		// common.HexToAddress silently yields the zero address for anything it
		// cannot parse, so a typo would install a signer nobody holds and
		// quietly shrink the effective set.
		if !common.IsHexAddress(text) {
			return errors.Errorf("%q is not a hex address", raw)
		}
		addr := common.HexToAddress(text)
		if addr == (common.Address{}) {
			return errors.New("admin address is zero")
		}
		// A repeated address inflates the apparent set size while the
		// distinct-signer check still counts it once, so a 2-of-3 naming the
		// same key twice is really 2-of-2 with one key unusable.
		if seen[addr] {
			return errors.Errorf("%s appears more than once", addr.Hex())
		}
		seen[addr] = true
	}
	return nil
}

// summarise renders what was actually parsed, so the operator sees the set the
// extension will see rather than the bytes they thought they wrote.
func summarise(a admins) string {
	var b strings.Builder
	b.WriteString("version " + strconv.FormatUint(a.Version, 10) + "\n")
	b.WriteString(strconv.Itoa(a.Threshold) + "-of-" + strconv.Itoa(len(a.Admins)) + "\n")
	for _, raw := range a.Admins {
		// Checksummed, so an operator comparing against a hardware wallet is
		// comparing the same rendering.
		b.WriteString("  " + common.HexToAddress(strings.TrimSpace(raw)).Hex() + "\n")
	}
	return b.String()
}
