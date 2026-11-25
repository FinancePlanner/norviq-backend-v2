package fx

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in     string
		iso    string
		factor float64
		ok     bool
	}{
		{" usd ", "USD", 1, true},
		{"GBX", "GBP", 0.01, true},
		{"GBp", "GBP", 0.01, true},
		{"ZAc", "ZAR", 0.01, true},
		{"ILA", "ILS", 0.01, true},
		{"eur", "EUR", 1, true},
		{"", "", 0, false},
		{"N/A", "", 0, false},
		{"US$", "", 0, false},
	}
	for _, c := range cases {
		iso, factor, ok := Normalize(c.in)
		if iso != c.iso || factor != c.factor || ok != c.ok {
			t.Errorf("Normalize(%q) = %q, %v, %v; want %q, %v, %v", c.in, iso, factor, ok, c.iso, c.factor, c.ok)
		}
	}
}
