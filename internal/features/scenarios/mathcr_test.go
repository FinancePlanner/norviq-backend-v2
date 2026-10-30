package scenarios

import (
	"math"
	"testing"
)

// Reference values are the exact results rounded to nearest (checked with
// Python's decimal module at 60 digits). The sin/cos inputs are cases where
// Darwin's libm is off by one ulp; v2 returns the correctly rounded value.
func TestCorrectlyRoundedMath(t *testing.T) {
	if got := crLog(0.3); got != -1.2039728043259361 {
		t.Errorf("log(0.3) = %v", got)
	}
	if got := crPow(1.02, 1.5); got != 1.030149503712932 {
		t.Errorf("pow(1.02, 1.5) = %v", got)
	}
	if got := crPow(1.03, 19.0/12); got != 1.0479139113669043 {
		t.Errorf("pow(1.03, 19/12) = %v", got)
	}
	if got := crExp(-0.5); got != 0.6065306597126334 {
		t.Errorf("exp(-0.5) = %v", got)
	}
	if s, _ := crSinCos(math.Float64frombits(4611972700966069845)); math.Float64bits(s) != 4605823236592984453 {
		t.Errorf("sin = %v", s)
	}
	if _, c := crSinCos(math.Float64frombits(4612503998886995356)); math.Float64bits(c) != 13827961125895933308 {
		t.Errorf("cos = %v", c)
	}
	if crPow(1, 7.5) != 1 || crPow(1.07, 0) != 1 || !math.IsNaN(crPow(-1, 0.5)) {
		t.Error("pow special cases")
	}
}
