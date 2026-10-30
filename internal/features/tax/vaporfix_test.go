package tax

import "testing"

// Regression tests for Vapor bugs fixed in v2 (docs/vapor-bugs/money.md).

func TestDoubleToDecimalUsesShortestDigits(t *testing.T) {
	for in, want := range map[float64]string{
		0.28:    "0.28",
		88.55:   "88.55",
		0.0253:  "0.0253",
		-1234.5: "-1234.5",
		1e-7:    "0.0000001",
		100:     "100",
	} {
		if got := fd(in).String(); got != want {
			t.Errorf("fd(%v) = %s, want %s", in, got, want)
		}
	}
	if got := portugalAutonomous.String(); got != "0.28" {
		t.Fatalf("PT autonomous rate %s (Vapor: 0.2800000000000000512)", got)
	}
}
