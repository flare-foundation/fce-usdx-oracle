// Package decimal parses monetary amounts exactly.
//
// # Why not float64
//
// Every amount here feeds a price that is published on-chain as a uint256.
// float64 has 53 bits of mantissa; a reserve figure like 25,452,544.52 is
// representable only approximately, and the error compounds through the
// division in package price. Amounts are therefore parsed into *big.Rat, which
// represents any decimal input exactly and divides without loss.
//
// # Why not shopspring/decimal
//
// big.Rat is standard library. For an extension whose whole value proposition
// is a reproducible, auditable code hash, a dependency carrying its own
// rounding semantics is a liability: it is one more thing an auditor must read
// and one more thing that can change under a version bump. big.Rat's semantics
// are fixed by the Go compatibility promise.
//
// # The separator trap
//
// Hex's two endpoints disagree on formatting. Measured 31 Jul 2026:
//
//	public:  {"value":"25,452,544.52"}   commas, no currency field
//	private: {"value":"205304.47"}       no commas, currency: "USD"
//
// So a parser must accept both. The obvious implementation — delete every comma
// then parse — is wrong and dangerously so: comma-as-decimal-separator is a
// real convention (much of Europe writes 1,50 for one and a half), and blind
// stripping silently turns 1,50 into 150. A hundredfold overstatement of
// reserves would publish $1.00 through a genuine depeg, which is precisely the
// event this oracle exists to detect.
//
// This package therefore validates grouping structure rather than stripping:
// a comma is accepted only where a thousands separator can legally appear, and
// anything else is rejected. Fail closed; never guess what the operator meant.
package decimal

import (
	"errors"
	"math/big"
	"strings"
)

// maxLen bounds input before any parsing work. Upstream responses are attacker-
// influenced in the limit, and big.Rat on a megabyte of digits is a cheap way to
// burn the observation deadline.
const maxLen = 64

var (
	errEmpty     = errors.New("empty")
	errTooLong   = errors.New("too long")
	errSyntax    = errors.New("invalid decimal syntax")
	errGrouping  = errors.New("invalid digit grouping")
	errNegative  = errors.New("negative")
	errNotNumber = errors.New("not a number")
)

// ParseAmount parses a non-negative decimal amount, accepting ',' only as a
// thousands separator in a well-formed grouping.
//
// Accepted:   "0"  "25452544.52"  "25,452,544.52"  "1,000"  ".5"  "123."
// Rejected:   "1,50"  "1,0000"  "12,34,567"  "-1"  "1e5"  "1.2.3"  ",100"
//
// The returned value is exact. Callers must not convert it to float64.
//
// Errors are for internal logging only and deliberately never include the input
// — see internal/failure: the input here IS the reserve figure, and it must not
// reach any public surface.
func ParseAmount(s string) (*big.Rat, error) {
	if s == "" {
		return nil, errEmpty
	}
	if len(s) > maxLen {
		return nil, errTooLong
	}

	// No sign is permitted at all. Reserves and supply are non-negative by
	// definition, and accepting "-0" or "+1" would mean trusting big.Rat's
	// tolerance rather than our own validation.
	if s[0] == '+' || s[0] == '-' {
		return nil, errNegative
	}

	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if strings.Contains(fracPart, ".") {
		return nil, errSyntax // more than one decimal point
	}

	if err := validateGrouping(intPart); err != nil {
		return nil, err
	}

	// The fractional part may not be grouped: "1.234,56" is not a number in any
	// convention this accepts.
	if !isDigits(fracPart) {
		return nil, errSyntax
	}

	// At least one digit must be present overall, so "." alone is rejected.
	if len(intPart) == 0 && len(fracPart) == 0 {
		return nil, errSyntax
	}

	canonical := strings.ReplaceAll(intPart, ",", "")
	if hasDot && len(fracPart) > 0 {
		canonical += "." + fracPart
	}
	if canonical == "" {
		return nil, errSyntax
	}

	r, ok := new(big.Rat).SetString(canonical)
	if !ok {
		return nil, errNotNumber
	}
	if r.Sign() < 0 {
		return nil, errNegative
	}
	return r, nil
}

// validateGrouping accepts either an ungrouped run of digits, or digits grouped
// in threes by commas with a leading group of one to three digits.
//
// This is the check that makes "1,50" an error rather than a hundredfold
// overstatement.
func validateGrouping(intPart string) error {
	if intPart == "" {
		return nil // ".5" is allowed; the caller checks that some digit exists
	}
	if !strings.Contains(intPart, ",") {
		if !isDigits(intPart) {
			return errSyntax
		}
		return nil
	}

	groups := strings.Split(intPart, ",")
	for i, g := range groups {
		if !isDigits(g) || g == "" {
			return errSyntax
		}
		if i == 0 {
			// Leading group: 1-3 digits. Rejects ",100" (empty) and "1234,567".
			if len(g) > 3 {
				return errGrouping
			}
		} else if len(g) != 3 {
			// Every later group must be exactly 3. This is what rejects "1,50".
			return errGrouping
		}
	}
	return nil
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
