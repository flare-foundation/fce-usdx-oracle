// Package price computes the published USDX price from backing and supply.
//
// This package is deliberately pure: no I/O, no clock, no configuration, no
// policy. Given the same inputs it returns the same output, always. That is
// what makes it the one component an auditor can verify by reading alone, and
// what makes exhaustive fuzzing meaningful.
//
// Policy decisions that look like they belong here do not:
//
//   - Plausibility bounds ("reject a price below 0.5") are enforced by the
//     caller and, authoritatively, by UsdxPriceStore on-chain. Encoding a bound
//     here would hide a governance parameter inside arithmetic.
//   - The deviation guard (spec §6.2) is a risk decision with an owner, and is
//     applied on-chain where it can be changed without a new code hash.
//   - Staleness is the consumer's concern, via the feed timestamp.
//
// # The formula
//
//	B     = R_cash + R_bonds                total backing, USD
//	S     = ΣtotalSupply                    circulating supply, USD-denominated
//	price = min(1, B / S)
//	value = ⌊price · 10^decimals⌋
//
// Two properties are load-bearing and both are tested:
//
// **Capped at par.** USDX redeems at $1.00, so over-collateralisation does not
// make it worth more. Publishing 1.02 because reserves happen to exceed supply
// would be wrong in a way that costs someone money.
//
// **Truncated, never rounded.** ⌊⌋ biases every published price downward by at
// most one unit in the last place. Rounding would let a 0.999999995 backing
// ratio publish as exactly 1.00000000, reporting full backing for a token that
// is not fully backed. Under-reporting is a conservative error; over-reporting
// is the failure this oracle exists to prevent.
package price

import (
	"math/big"

	"fce-usdx-oracle/internal/failure"
)

// maxDecimals bounds the fixed-point scale. The published feed uses 8; this
// only exists so a misconfiguration cannot ask for 10^127 and exhaust memory.
const maxDecimals = 36

// Inputs are the measured quantities the price is derived from.
//
// All amounts are USD-denominated and must be non-negative. Supply is the sum
// across chains, already normalised for each chain's decimals() — this package
// does no unit conversion, because a package that silently rescales is a
// package that can silently rescale wrongly.
type Inputs struct {
	// Bonds is R_bonds, the bond reserve figure.
	Bonds *big.Rat

	// Cash is R_cash, cash held for redemptions.
	Cash *big.Rat

	// Supply is S, circulating supply.
	Supply *big.Rat
}

// Compute returns ⌊min(1, B/S) · 10^decimals⌋.
//
// It returns a failure.Failure, so callers may surface the error publicly
// without inspecting it — no input value can reach the returned message.
func Compute(in Inputs, decimals int8) (*big.Int, error) {
	if decimals < 0 || decimals > maxDecimals {
		return nil, failure.Internal
	}
	if in.Bonds == nil || in.Cash == nil || in.Supply == nil {
		return nil, failure.Internal
	}
	if in.Bonds.Sign() < 0 || in.Cash.Sign() < 0 {
		return nil, failure.BackingImplausible
	}
	if in.Supply.Sign() < 0 {
		return nil, failure.SupplyImplausible
	}

	// Division by zero is the guard that matters most here: an empty supply
	// makes the ratio undefined, not infinite, and there is no defensible price
	// to publish. Fail closed and let the feed's timestamp age.
	if in.Supply.Sign() == 0 {
		return nil, failure.SupplyZero
	}

	backing := Backing(in)

	// price = B/S, exact — big.Rat division loses nothing.
	ratio := new(big.Rat).Quo(backing, in.Supply)

	scale := Scale(decimals)
	scaled := new(big.Rat).Mul(ratio, new(big.Rat).SetInt(scale))

	// Truncate toward zero. Inputs are non-negative here, so this is the floor.
	value := new(big.Int).Quo(scaled.Num(), scaled.Denom())

	// Cap at par. Applied after scaling rather than to the ratio, which is
	// equivalent and keeps the exact arithmetic in one place.
	if value.Cmp(scale) > 0 {
		return scale, nil
	}
	return value, nil
}

// Backing returns B = R_cash + R_bonds, exactly.
//
// Spec §3.4 floated a haircut factor on the bond component. Deliberately not
// implemented: it was only ever a proposal, it would always have been zero, and
// applying one changes the published price — which means a new code hash
// regardless. A configurable value buys nothing and adds a knob that silently
// moves the feed.
func Backing(in Inputs) *big.Rat {
	return new(big.Rat).Add(in.Cash, in.Bonds)
}

// Scale returns 10^decimals. Callers treat it as the on-chain representation of
// $1.00, which is what the par cap means.
func Scale(decimals int8) *big.Int {
	if decimals < 0 || decimals > maxDecimals {
		return big.NewInt(1)
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
}
