// Package decimal parses monetary amounts exactly, into *big.Rat.
//
// Hex's two endpoints disagree on formatting: the public one returns
// "25,452,544.52", the restricted one "205304.47". Stripping every comma before
// parsing would silently turn the European "1,50" into 150 — a hundredfold
// overstatement of reserves, which would publish $1.00 straight through a real
// depeg. So this validates grouping structure instead and fails closed.
package decimal

import (
	"errors"
	"math/big"
	"strings"
)

// maxLen bounds input before any parsing work: big.Rat on a megabyte of digits
// is a cheap way to burn the observation deadline.
const maxLen = 64

var (
	errEmpty     = errors.New("empty")
	errTooLong   = errors.New("too long")
	errSyntax    = errors.New("invalid decimal syntax")
	errGrouping  = errors.New("invalid digit grouping")
	errNegative  = errors.New("negative")
	errNotNumber = errors.New("not a number")
)

// ParseAmount parses a non-negative decimal amount, accepting ',' only as a thousands
// separator in a well-formed grouping. Errors never include the input: it IS the
// reserve figure.
//
//	Accepted:   "0"  "25452544.52"  "25,452,544.52"  "1,000"  ".5"  "123."
//	Rejected:   "1,50"  "1,0000"  "12,34,567"  "-1"  "1e5"  "1.2.3"  ",100"
func ParseAmount(s string) (*big.Rat, error) {
	if s == "" {
		return nil, errEmpty
	}
	if len(s) > maxLen {
		return nil, errTooLong
	}

	// No sign at all: accepting "-0" or "+1" would mean trusting big.Rat's
	// tolerance rather than our own validation.
	if s[0] == '+' || s[0] == '-' {
		return nil, errNegative
	}

	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if strings.Contains(fracPart, ".") {
		return nil, errSyntax
	}

	if err := validateGrouping(intPart); err != nil {
		return nil, err
	}

	// The fractional part may not be grouped: "1.234,56" is not a number here.
	if !isDigits(fracPart) {
		return nil, errSyntax
	}

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
// in threes with a leading group of one to three. This is what makes "1,50" an
// error rather than a hundredfold overstatement.
func validateGrouping(intPart string) error {
	if intPart == "" {
		return nil // ".5"; the caller checks that some digit exists
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
			if len(g) > 3 {
				return errGrouping
			}
		} else if len(g) != 3 {
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
