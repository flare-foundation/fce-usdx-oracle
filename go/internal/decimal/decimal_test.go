package decimal

import (
	"math/big"
	"strings"
	"testing"
)

func mustRat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("test setup: %q is not a rational", s)
	}
	return r
}

func TestParseAmount_Accepts(t *testing.T) {
	cases := []struct{ in, want string }{
		// The two shapes Hex actually returns, measured 31 Jul 2026.
		{"25,452,544.52", "25452544.52"}, // public endpoint
		{"205304.47", "205304.47"},       // private endpoint

		{"0", "0"},
		{"0.00", "0"},
		{"1,000", "1000"},
		{"999", "999"},
		{"1,000,000", "1000000"},
		{"12,345,678.90", "12345678.90"},
		{"100", "100"},
		{".5", "0.5"},
		{"123.", "123"},
		{"1", "1"},
		{"12", "12"},
		{"123", "123"},
		{"1234567", "1234567"}, // ungrouped is fine
		{"0.000000000000000001", "0.000000000000000001"}, // 18 decimals, Ethereum scale
	}
	for _, c := range cases {
		got, err := ParseAmount(c.in)
		if err != nil {
			t.Errorf("ParseAmount(%q): unexpected error %v", c.in, err)
			continue
		}
		if want := mustRat(t, c.want); got.Cmp(want) != 0 {
			t.Errorf("ParseAmount(%q) = %s, want %s", c.in, got.FloatString(20), want.FloatString(20))
		}
	}
}

func TestParseAmount_Rejects(t *testing.T) {
	cases := []struct{ in, why string }{
		{"1,50", "comma-as-decimal-separator must not be read as a thousands separator"},
		{"1,5", "short trailing group"},
		{"1,0000", "over-long trailing group"},
		{"12,34,567", "inconsistent grouping"},
		{",100", "leading comma"},
		{"1,", "trailing comma"},
		{"1234,567", "leading group longer than three digits"},
		{"-1", "negative"},
		{"+1", "signed"},
		{"1e5", "exponent"},
		{"1.2.3", "two decimal points"},
		{"1.234,56", "grouped fractional part"},
		{"", "empty"},
		{".", "no digits"},
		{"abc", "not a number"},
		{"1 000", "space separator"},
		{"1_000", "underscore separator"},
		{" 1", "leading space"},
		{"1 ", "trailing space"},
		{"0x10", "hex"},
		{"1/2", "big.Rat fraction syntax must not be reachable"},
		{strings.Repeat("9", 100), "over-long input"},
	}
	for _, c := range cases {
		if got, err := ParseAmount(c.in); err == nil {
			t.Errorf("ParseAmount(%q) = %s, want error (%s)", c.in, got.FloatString(4), c.why)
		}
	}
}

// The headline trap, called out on its own because getting it wrong overstates
// reserves a hundredfold and would mask a real depeg.
func TestParseAmount_LocaleCommaIsNotSilentlyStripped(t *testing.T) {
	if _, err := ParseAmount("1,50"); err == nil {
		t.Fatal(`"1,50" parsed successfully; blind comma-stripping would read it as 150`)
	}
}

// big.Rat.SetString accepts "1/2" and "1e5". Neither is a valid API amount, and
// both must be rejected before reaching it.
func TestParseAmount_DoesNotInheritBigRatSyntax(t *testing.T) {
	for _, s := range []string{"1/2", "3/4", "1e5", "1E5", "0b101"} {
		if _, err := ParseAmount(s); err == nil {
			t.Errorf("ParseAmount(%q) succeeded; big.Rat syntax must not leak through", s)
		}
	}
}

// Errors must never quote the input: for this package the input IS the reserve
// figure, which spec §4.1 keeps off every public surface. Even though these
// errors are internal-only today, a future caller might wrap one outward.
func TestParseAmount_ErrorsDoNotEchoInput(t *testing.T) {
	const secret = "25452544.52x"
	_, err := ParseAmount(secret)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "25452544") {
		t.Errorf("error %q echoes the parsed value", err)
	}
}

func TestParseAmount_ExactnessBeyondFloat64(t *testing.T) {
	// 9007199254740993 = 2^53 + 1, the first integer float64 cannot represent.
	const s = "9007199254740993"
	got, err := ParseAmount(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.FloatString(0) != s {
		t.Errorf("got %s, want %s — parsing must not round-trip through float64", got.FloatString(0), s)
	}
}

// A parse must never panic, whatever the upstream returns.
func FuzzParseAmount(f *testing.F) {
	seeds := []string{
		"25,452,544.52", "205304.47", "1,50", "0", "", ".", "-1", "1e5",
		"1,000,000.00", ",", ",,,", "999999999999999999999999", "0.0000000001",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseAmount(s)
		if err != nil {
			return
		}
		// Whatever parses must satisfy the invariants the price formula relies on.
		if got == nil {
			t.Fatalf("ParseAmount(%q) returned nil with no error", s)
		}
		if got.Sign() < 0 {
			t.Errorf("ParseAmount(%q) produced a negative amount", s)
		}
		if strings.ContainsAny(s, "eE/") {
			t.Errorf("ParseAmount(%q) accepted exponent or fraction syntax", s)
		}
	})
}
