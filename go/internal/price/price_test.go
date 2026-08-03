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
		{"under-backed by a hair", "999999999", "0", "1000000000", "99999999"},
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

// Truncation, not rounding. A ratio a hair under par must not publish as par:
// reporting full backing for a token that is not fully backed is the exact
// failure this oracle exists to prevent.
func TestCompute_TruncatesNeverRounds(t *testing.T) {
	// 0.999999995 would round to 1.00000000 at 8 decimals, but must truncate
	// to 0.99999999.
	got, err := Compute(in(t, "999999995", "0", "1000000000"), decimals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "99999999" {
		t.Errorf("Compute() = %s, want 99999999 (rounding would give 100000000)", got)
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

func TestHaircut(t *testing.T) {
	cases := []struct {
		name       string
		haircutBps uint16
		want       string
	}{
		{"no haircut is the default", 0, "100000000"},
		{"one percent haircut on bonds", 100, "99000000"},
		{"fifty percent haircut", 5000, "50000000"},
		{"full haircut values bonds at zero", 10000, "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i := in(t, "100", "0", "100")
			i.HaircutBps = c.haircutBps
			got, err := Compute(i, decimals)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != c.want {
				t.Errorf("Compute() = %s, want %s", got, c.want)
			}
		})
	}
}

// The haircut discounts bonds only. Cash is cash.
func TestHaircut_DoesNotApplyToCash(t *testing.T) {
	i := in(t, "0", "100", "100")
	i.HaircutBps = 5000
	got, err := Compute(i, decimals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "100000000" {
		t.Errorf("Compute() = %s, want 100000000 — cash must not be haircut", got)
	}
}

func TestCompute_HaircutOverOneHundredPercentRejected(t *testing.T) {
	i := in(t, "100", "0", "100")
	i.HaircutBps = bpsDivisor + 1
	if _, err := Compute(i, decimals); !errors.Is(err, failure.Internal) {
		t.Errorf("expected failure.Internal, got %v", err)
	}
}

// Precision beyond float64. Supply at Ethereum's 18 decimals against a backing
// figure at 2 decimals is where a float-based implementation drifts.
func TestCompute_ExactBeyondFloat64(t *testing.T) {
	got, err := Compute(in(t,
		"20620459.685024", "0",
		"20620459.685024000000000001", // one wei more supply: must be under par
	), decimals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "99999999" {
		t.Errorf("Compute() = %s, want 99999999 — a hair under par must not publish as par", got)
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
	f.Add(int64(100), int64(0), int64(100), uint16(0))
	f.Add(int64(50), int64(50), int64(100), uint16(0))
	f.Add(int64(1), int64(0), int64(3), uint16(100))
	f.Add(int64(0), int64(0), int64(1), uint16(10000))

	f.Fuzz(func(t *testing.T, bonds, cash, supply int64, haircut uint16) {
		if bonds < 0 || cash < 0 || supply <= 0 || haircut > bpsDivisor {
			t.Skip()
		}
		got, err := Compute(Inputs{
			Bonds:      new(big.Rat).SetInt64(bonds),
			Cash:       new(big.Rat).SetInt64(cash),
			Supply:     new(big.Rat).SetInt64(supply),
			HaircutBps: haircut,
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
