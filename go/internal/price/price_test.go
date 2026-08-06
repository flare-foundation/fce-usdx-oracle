package price

import (
	"errors"
	"math/big"
	"testing"

	"fce-usdx-oracle/internal/failure"
)

const decimals int8 = 8

func rat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("test setup: %q is not a rational", s)
	}
	return r
}

func in(t *testing.T, bonds, cash, supply string) Inputs {
	t.Helper()
	return Inputs{Bonds: rat(t, bonds), Cash: rat(t, cash), Supply: rat(t, supply)}
}

func TestCompute(t *testing.T) {
	cases := []struct {
		name                string
		bonds, cash, supply string
		want                string
	}{
		{"exactly backed", "100", "0", "100", "100000000"},
		{"over-backed is capped at par", "150", "0", "100", "100000000"},
		{"massively over-backed still caps", "1000000", "0", "1", "100000000"},
		{"half backed", "50", "0", "100", "50000000"},
		{"cash counts toward backing", "50", "50", "100", "100000000"},
		{"under-backed inside half a ULP rounds to par", "999999999", "0", "1000000000", "100000000"},
		{"under-backed beyond half a ULP prints below par", "99999994", "0", "100000000", "99999994"},
		{"zero backing", "0", "0", "100", "0"},

		// The real figures from spec §3.0, measured 31 Jul 2026. Backing equals
		// supply to the cent, so the feed prints exactly par — this is the
		// blocker in §3.0 reproduced as an assertion.
		{"observed production values", "25452544.52", "205304.47", "25452544.524018", "100000000"},

		// A 1% depeg must be visible, not rounded away.
		{"one percent depeg", "99", "0", "100", "99000000"},
		{"one basis point depeg", "9999", "0", "10000", "99990000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Compute(in(t, c.bonds, c.cash, c.supply), decimals)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != c.want {
				t.Errorf("Compute() = %s, want %s", got, c.want)
			}
		})
	}
}

// Rounding, not truncation — reversed 5 Aug 2026. The reasoning is in the
// package doc: Hex quotes reserves to the cent, so truncation turns a $0.004
// quantisation artifact into a permanent sub-par print.
//
// This pins the boundary in both directions, because the whole safety argument
// is that the shortfall rounding can hide is bounded at exactly half a unit in
// the last place.
func TestCompute_RoundsHalfUp(t *testing.T) {
	cases := []struct {
		name        string
		bonds, want string
		supply      string
	}{
		{"exactly half a ULP short rounds up to par", "999999995", "100000000", "1000000000"},
		{"a hair beyond half a ULP stays below par", "999999994", "99999999", "1000000000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Compute(in(t, c.bonds, "0", c.supply), decimals)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != c.want {
				t.Errorf("Compute() = %s, want %s", got, c.want)
			}
		})
	}
}

// The bound that makes rounding acceptable: it can conceal a shortfall of at
// most half a ULP, so nothing an operator would care about survives it. A
// material depeg must print identically to how it printed under truncation.
func TestCompute_RoundingCannotHideAMaterialShortfall(t *testing.T) {
	cases := []struct{ name, bonds, want string }{
		{"one percent", "99000000", "99000000"},
		{"one basis point", "99990000", "99990000"},
		{"the cash buffer, if it were ever dropped", "99193400", "99193400"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Compute(in(t, c.bonds, "0", "100000000"), decimals)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != c.want {
				t.Errorf("Compute() = %s, want %s — rounding must not move a real shortfall", got, c.want)
			}
		})
	}
}

// The ½ is exact because both operands are big.Rat. This ratio is
// 0.999999994999999999 — a float64 rounds it to 0.999999995 and publishes par,
// exact arithmetic keeps it one unit below.
func TestCompute_RoundingBoundaryIsExact(t *testing.T) {
	got, err := Compute(in(t, "99999999.4999999999", "0", "100000000"), decimals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "99999999" {
		t.Errorf("Compute() = %s, want 99999999 — a float64 would land on par", got)
	}
}

func TestCompute_SupplyZeroFailsClosed(t *testing.T) {
	_, err := Compute(in(t, "100", "0", "0"), decimals)
	if !errors.Is(err, failure.SupplyZero) {
		t.Fatalf("expected failure.SupplyZero, got %v", err)
	}
}

func TestCompute_NegativeInputsRejected(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want failure.Failure
	}{
		{"negative bonds", in(t, "-1", "0", "100"), failure.BackingImplausible},
		{"negative cash", in(t, "0", "-1", "100"), failure.BackingImplausible},
		{"negative supply", in(t, "100", "0", "-1"), failure.SupplyImplausible},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Compute(c.in, decimals); !errors.Is(err, c.want) {
				t.Errorf("expected %v, got %v", c.want, err)
			}
		})
	}
}

func TestCompute_NilInputsRejected(t *testing.T) {
	for _, c := range []Inputs{
		{Bonds: nil, Cash: rat(t, "1"), Supply: rat(t, "1")},
		{Bonds: rat(t, "1"), Cash: nil, Supply: rat(t, "1")},
		{Bonds: rat(t, "1"), Cash: rat(t, "1"), Supply: nil},
	} {
		if _, err := Compute(c, decimals); !errors.Is(err, failure.Internal) {
			t.Errorf("expected failure.Internal for a nil input, got %v", err)
		}
	}
}

func TestCompute_BadDecimalsRejected(t *testing.T) {
	for _, d := range []int8{-1, -128, maxDecimals + 1, 127} {
		if _, err := Compute(in(t, "1", "0", "1"), d); !errors.Is(err, failure.Internal) {
			t.Errorf("decimals=%d: expected failure.Internal, got %v", d, err)
		}
	}
}

// Precision beyond float64. Supply at Ethereum's 18 decimals against a backing
// figure at 2 decimals is where a float-based implementation drifts.
//
// One wei of extra supply is 5e-26 of the float — far inside half a ULP, so par
// is the correct answer under rounding. What is asserted is that the arithmetic
// gets there without overflowing or losing the low digits.
func TestCompute_ExactBeyondFloat64(t *testing.T) {
	got, err := Compute(in(t,
		"20620459.685024", "0",
		"20620459.685024000000000001", // one wei more supply
	), decimals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "100000000" {
		t.Errorf("Compute() = %s, want 100000000 — one wei is far inside half a ULP", got)
	}
}

// ComputeWithoutCash publishes only when the missing figure cannot matter, and
// fails closed otherwise. The failing half is the important one: it is what
// keeps this from becoming the graceful degradation the availability doc
// argues against.
func TestComputeWithoutCash(t *testing.T) {
	cases := []struct {
		name          string
		bonds, supply string
		want          string // "" means expect failure.BackingUnavailable
	}{
		{"bonds exceed supply", "150", "100", "100000000"},
		{"bonds equal supply", "100", "100", "100000000"},

		// The measured 5 Aug 2026 figures: bonds is $0.004018 short of supply
		// purely because Hex quotes to the cent. This is the case the rule
		// exists for.
		{"production figures, cent quantisation", "25452544.52", "25452544.524018", "100000000"},

		// Half a ULP short is still par; beyond it, cash decides and nothing
		// may be published.
		{"exactly half a ULP short", "999999995", "1000000000", "100000000"},
		{"a hair beyond half a ULP", "999999994", "1000000000", ""},

		// The availability doc's decisive case. With cash the true price is
		// 0.998; degradation would print 0.990. This prints nothing.
		{"one percent short fails closed", "99", "100", ""},
		{"the cash buffer itself fails closed", "25452544.52", "25657848.99", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ComputeWithoutCash(rat(t, c.bonds), rat(t, c.supply), decimals)
			if c.want == "" {
				if !errors.Is(err, failure.BackingUnavailable) {
					t.Fatalf("expected failure.BackingUnavailable, got value=%v err=%v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != c.want {
				t.Errorf("ComputeWithoutCash() = %s, want %s", got, c.want)
			}
		})
	}
}

// Whatever ComputeWithoutCash returns must equal what a complete observation
// would have returned, for every non-negative cash figure. That equality is the
// entire justification for publishing without the source.
func TestComputeWithoutCash_MatchesTheFullComputation(t *testing.T) {
	bonds, supply := rat(t, "25452544.52"), rat(t, "25452544.524018")

	cashless, err := ComputeWithoutCash(bonds, supply, decimals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, cash := range []string{"0", "0.004018", "1", "205304.47", "999999999"} {
		full, err := Compute(Inputs{Bonds: bonds, Cash: rat(t, cash), Supply: supply}, decimals)
		if err != nil {
			t.Fatalf("cash=%s: unexpected error: %v", cash, err)
		}
		if full.Cmp(cashless) != 0 {
			t.Errorf("cash=%s: full computation gives %s, cashless path published %s",
				cash, full, cashless)
		}
	}
}

func TestComputeWithoutCash_RejectsBadInputs(t *testing.T) {
	if _, err := ComputeWithoutCash(rat(t, "100"), rat(t, "0"), decimals); !errors.Is(err, failure.SupplyZero) {
		t.Errorf("expected failure.SupplyZero, got %v", err)
	}
	if _, err := ComputeWithoutCash(rat(t, "-1"), rat(t, "100"), decimals); !errors.Is(err, failure.BackingImplausible) {
		t.Errorf("expected failure.BackingImplausible, got %v", err)
	}
	if _, err := ComputeWithoutCash(nil, rat(t, "100"), decimals); !errors.Is(err, failure.Internal) {
		t.Errorf("expected failure.Internal for a nil bonds figure, got %v", err)
	}
}

// Errors must be publishable: Compute's failures flow to ActionResult.Log.
func TestCompute_ErrorsAreVocabulary(t *testing.T) {
	_, err := Compute(in(t, "100", "0", "0"), decimals)
	if err == nil {
		t.Fatal("expected an error")
	}
	var f failure.Failure
	if !errors.As(err, &f) {
		t.Fatalf("error %v is not a failure.Failure; it could leak input values", err)
	}
}

// The two invariants everything downstream relies on, over arbitrary inputs:
// the result is never negative, and never exceeds par.
func FuzzCompute(f *testing.F) {
	f.Add(int64(100), int64(0), int64(100))
	f.Add(int64(50), int64(50), int64(100))
	f.Add(int64(1), int64(0), int64(3))
	f.Add(int64(0), int64(0), int64(1))

	f.Fuzz(func(t *testing.T, bonds, cash, supply int64) {
		if bonds < 0 || cash < 0 || supply <= 0 {
			t.Skip()
		}
		got, err := Compute(Inputs{
			Bonds:  new(big.Rat).SetInt64(bonds),
			Cash:   new(big.Rat).SetInt64(cash),
			Supply: new(big.Rat).SetInt64(supply),
		}, decimals)
		if err != nil {
			t.Fatalf("unexpected error for valid inputs: %v", err)
		}
		if got.Sign() < 0 {
			t.Errorf("negative price %s", got)
		}
		if got.Cmp(Scale(decimals)) > 0 {
			t.Errorf("price %s exceeds par — USDX redeems at $1.00", got)
		}
	})
}
