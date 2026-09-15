// Package price computes min(1, backing/supply) as a fixed-point integer.
// Pure: no I/O, no clock, no configuration.
package price

import (
	"math/big"

	"fce-usdx-oracle/internal/failure"
)

const maxDecimals = 36

// Inputs are USD-denominated and non-negative. Supply is already normalised
// for each chain's decimals() — this package does no unit conversion.
type Inputs struct {
	Bonds  *big.Rat
	Cash   *big.Rat
	Supply *big.Rat
}

// Compute returns min(1, B/S) * 10^decimals, rounded half-up.
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

	// Empty supply makes the ratio undefined, not infinite: fail closed.
	if in.Supply.Sign() == 0 {
		return nil, failure.SupplyZero
	}

	ratio := new(big.Rat).Quo(Backing(in), in.Supply)

	scale := Scale(decimals)
	scaled := new(big.Rat).Mul(ratio, new(big.Rat).SetInt(scale))

	// Round half-up, exact in big.Rat: truncating prints a depeg that has not happened.
	scaled.Add(scaled, big.NewRat(1, 2))
	value := new(big.Int).Quo(scaled.Num(), scaled.Denom())

	if value.Cmp(scale) > 0 {
		// scale represents 1
		return scale, nil
	}
	return value, nil
}

// ComputeWithoutCash returns the price when the cash figure is unavailable. Cash is
// non-negative and the price caps at par, so a bonds-only price already at par is the
// full result. Below par the missing figure is load-bearing, so this errors.
func ComputeWithoutCash(bonds, supply *big.Rat, decimals int8) (*big.Int, error) {
	value, err := Compute(Inputs{Bonds: bonds, Cash: new(big.Rat), Supply: supply}, decimals)
	if err != nil {
		return nil, err
	}

	if value.Cmp(Scale(decimals)) < 0 {
		return nil, failure.BackingUnavailable
	}
	return value, nil
}

// Backing returns B = R_cash + R_bonds, exactly.
func Backing(in Inputs) *big.Rat {
	return new(big.Rat).Add(in.Cash, in.Bonds)
}

// Scale returns 10^decimals, the on-chain representation of $1.00.
func Scale(decimals int8) *big.Int {
	if decimals < 0 || decimals > maxDecimals {
		return big.NewInt(1)
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
}
