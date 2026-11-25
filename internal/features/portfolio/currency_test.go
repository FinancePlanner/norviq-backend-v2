package portfolio

import "testing"

func TestHoldingCurrency(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name     string
		stock    *string
		quote    string
		base     string
		want     string
		mismatch bool
	}{
		{"stock and quote agree", s("DKK"), "DKK", "USD", "DKK", false},
		{"stock differs from quote", s("USD"), "DKK", "USD", "USD", true},
		{"quote only", nil, "CAD", "USD", "CAD", false},
		{"nothing known", nil, "", "EUR", "EUR", false},
		{"invalid quote is unset", nil, "N/A", "EUR", "EUR", false},
		{"invalid stock currency is unset", s(""), "CAD", "USD", "CAD", false},
		{"invalid stock currency N/A", s("N/A"), "", "USD", "USD", false},
		{"invalid quote does not mismatch", s("USD"), "N/A", "EUR", "USD", false},
		{"pence quote matches pound stock", s("GBP"), "GBp", "USD", "GBP", false},
		{"lowercase normalised", s("dkk"), "DKK", "USD", "DKK", false},
	}
	for _, c := range cases {
		got, mm := HoldingCurrency(c.stock, c.quote, c.base)
		if got != c.want || mm != c.mismatch {
			t.Errorf("%s: got %q,%v want %q,%v", c.name, got, mm, c.want, c.mismatch)
		}
	}
}
