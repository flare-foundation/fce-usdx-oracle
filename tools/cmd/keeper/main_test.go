package main

import (
	"math/big"
	"testing"
)

// The floor is given in whole coins because that is how an operator thinks
// about a balance, and compared in wei because that is what the chain returns.
func TestParseCoins(t *testing.T) {
	ether := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

	cases := []struct {
		in   string
		want *big.Int
	}{
		{"5", new(big.Int).Mul(big.NewInt(5), ether)},
		{"0", big.NewInt(0)},
		{"0.5", new(big.Int).Div(ether, big.NewInt(2))},
		{" 2 ", new(big.Int).Mul(big.NewInt(2), ether)},
	}
	for _, c := range cases {
		got, err := parseCoins(c.in)
		if err != nil {
			t.Errorf("parseCoins(%q): %v", c.in, err)
			continue
		}
		if got.Cmp(c.want) != 0 {
			t.Errorf("parseCoins(%q) = %s wei, want %s", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "lots", "-1"} {
		if _, err := parseCoins(bad); err == nil {
			t.Errorf("parseCoins(%q) was accepted", bad)
		}
	}
}
