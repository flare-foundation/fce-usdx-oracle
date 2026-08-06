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
//	value = ⌊price · 10^decimals + ½⌋
//
// Two properties are load-bearing and both are tested:
//
// **Capped at par.** USDX redeems at $1.00, so over-collateralisation does not
// make it worth more. Publishing 1.02 because reserves happen to exceed supply
// would be wrong in a way that costs someone money.
//
// **Rounded half-up, not truncated.** _Reversed 5 Aug 2026; this package
// previously truncated._ The argument for ⌊⌋ was that it can only ever
// under-report, and over-reporting is the failure this oracle exists to
// prevent. That is true, and it is the wrong trade at this scale.
//
// Hex quotes the reserve figure to the cent while supply carries 6 decimals on
// Flare and Songbird and 18 on Ethereum. The published figure is therefore
// supply with up to $0.01 of quantisation lopped off it, and truncation turns
// that rounding artifact into a permanent sub-par print: measured 5 Aug 2026,
// B − S = −$0.004 on a $25.4M float, which truncates to 0.99999999 and reads as
// a depeg that has not happened.
//
// Half a unit in the last place is $0.127 at that supply — twelve times the
// worst-case cent quantisation, and 5e-9 of the float. So rounding absorbs the
// artifact with margin, and the residual over-report it admits is far below any
// shortfall worth reporting. Anything material is untouched: a 1% depeg prints
// as 0.99000000 either way, because the shortfall that rounding could hide is
// bounded at half a ULP by construction.
//
// The exact arithmetic underneath is what makes that bound trustworthy — see
// TestCompute_RoundingBoundaryIsExact, where a float64 implementation lands on
// the wrong side of the ½ boundary.
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

// Compute returns min(1, B/S) · 10^decimals, rounded half-up.
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

	// Round half-up: ⌊x + ½⌋. Inputs are non-negative here, so Quo's truncation
	// toward zero is the floor, and adding a half before it is exactly the
	// half-up rule. Both operands are big.Rat, so the ½ is exact — a float
	// implementation would misplace values sitting on the boundary.
	scaled.Add(scaled, big.NewRat(1, 2))
	value := new(big.Int).Quo(scaled.Num(), scaled.Denom())

	// Cap at par. Applied after scaling rather than to the ratio, which is
	// equivalent and keeps the exact arithmetic in one place.
	if value.Cmp(scale) > 0 {
		return scale, nil
	}
	return value, nil
}

// ComputeWithoutCash returns the price when the cash figure is unavailable —
// either because no credential for the restricted endpoint has been provisioned
// or because the endpoint failed during the observation.
//
// It publishes only when the answer cannot depend on the missing figure, and
// fails closed otherwise. Cash is non-negative, so the bonds-only price is a
// lower bound on the true one, and the true one is capped at par: if the
// bonds-only price already reaches par, so does the true price, and the value
// returned here is provably the one a complete observation would have produced.
//
// This is deliberately NOT graceful degradation, which
// docs/usdx-oracle-availability.md argues against and which this must not become
// by accident. Degradation would publish min(1, R_bonds/S) whenever cash was
// missing — a different number, indistinguishable on-chain from a full
// observation, and wrong in exactly the case the feed exists for: at
// R_bonds = 0.99·S the true price is 0.998 and the degraded one is 0.990, so the
// cash buffer that decides whether a depeg prints would be silently dropped.
// Here that case returns an error and nothing is published.
//
// The rule has teeth only because Compute rounds: measured 5 Aug 2026 the
// bonds-only ratio is 0.9999999998, which reaches par under rounding and would
// not under truncation. Cash at $205,304 is 806,615 units in the last place —
// six orders of magnitude beyond what rounding can absorb — so this admits the
// quantisation artifact and nothing else.
func ComputeWithoutCash(bonds, supply *big.Rat, decimals int8) (*big.Int, error) {
	value, err := Compute(Inputs{Bonds: bonds, Cash: new(big.Rat), Supply: supply}, decimals)
	if err != nil {
		return nil, err
	}

	// Below par, the missing figure is load-bearing. Report the source that is
	// actually absent rather than a price nobody can stand behind.
	if value.Cmp(Scale(decimals)) < 0 {
		return nil, failure.BackingUnavailable
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
