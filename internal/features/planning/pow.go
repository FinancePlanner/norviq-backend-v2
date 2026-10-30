package planning

import (
	"math"
	"math/big"
)

// Pow is Foundation's pow(x, y), correctly rounded. Darwin's and glibc's
// libm return pow correctly rounded for every argument the planning math
// feeds it; math.Pow is a few ULP off, which would show in the JSON. The
// result is computed in 256-bit arithmetic and rounded once.
func Pow(x, y float64) float64 {
	switch {
	case y == 0 || x == 1:
		return 1
	case x <= 0 || math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0):
		return math.Pow(x, y)
	}
	if y == math.Trunc(y) && math.Abs(y) <= 1<<20 {
		return powInt(x, int64(y))
	}
	lx := bigLog(new(big.Float).SetPrec(powPrec).SetFloat64(x))
	t := lx.Mul(lx, new(big.Float).SetPrec(powPrec).SetFloat64(y))
	f, _ := bigExp(t).Float64()
	return f
}

const powPrec = 256

// powInt is pow(x, n) for an integral n: exact repeated squaring, one
// rounding.
func powInt(x float64, n int64) float64 {
	neg := n < 0
	if neg {
		n = -n
	}
	base := new(big.Float).SetPrec(powPrec * 4).SetFloat64(x)
	result := new(big.Float).SetPrec(powPrec * 4).SetInt64(1)
	for e := n; e > 0; e >>= 1 {
		if e&1 == 1 {
			result.Mul(result, base)
		}
		base.Mul(base, base)
	}
	if neg {
		result.Quo(new(big.Float).SetPrec(powPrec*4).SetInt64(1), result)
	}
	f, _ := result.Float64()
	return f
}

var ln2 = func() *big.Float {
	// ln 2 = 2·atanh(1/3)
	third := new(big.Float).SetPrec(powPrec+64).Quo(big.NewFloat(1).SetPrec(powPrec+64), big.NewFloat(3).SetPrec(powPrec+64))
	return atanh2(third)
}()

// atanh2 is 2·atanh(z) = 2·Σ z^(2k+1)/(2k+1) for |z| ≤ 1/3.
func atanh2(z *big.Float) *big.Float {
	prec := z.Prec()
	sum := new(big.Float).SetPrec(prec).Set(z)
	z2 := new(big.Float).SetPrec(prec).Mul(z, z)
	term := new(big.Float).SetPrec(prec).Set(z)
	eps := new(big.Float).SetPrec(prec).SetMantExp(big.NewFloat(1), -int(prec)-8)
	for k := int64(1); ; k++ {
		term.Mul(term, z2)
		t := new(big.Float).SetPrec(prec).Quo(term, new(big.Float).SetPrec(prec).SetInt64(2*k+1))
		sum.Add(sum, t)
		if new(big.Float).Abs(t).Cmp(eps) < 0 {
			break
		}
	}
	return sum.Mul(sum, big.NewFloat(2))
}

// bigLog is ln(x) for x > 0: x = m·2^e with m in [0.5, 1), ln x = e·ln2 + ln m.
func bigLog(x *big.Float) *big.Float {
	prec := uint(powPrec + 64)
	m := new(big.Float).SetPrec(prec)
	e := x.MantExp(m)
	one := new(big.Float).SetPrec(prec).SetInt64(1)
	num := new(big.Float).SetPrec(prec).Sub(m, one)
	den := new(big.Float).SetPrec(prec).Add(m, one)
	lnm := atanh2(num.Quo(num, den))
	return lnm.Add(lnm, new(big.Float).SetPrec(prec).Mul(ln2, new(big.Float).SetPrec(prec).SetInt64(int64(e))))
}

// bigExp is e^t: t = k·ln2 + r, e^r by Taylor on r/2^16 squared back up.
func bigExp(t *big.Float) *big.Float {
	prec := uint(powPrec + 64)
	q := new(big.Float).SetPrec(prec).Quo(t, ln2)
	kf, _ := q.Float64()
	k := int64(math.Round(kf))
	r := new(big.Float).SetPrec(prec).Sub(t, new(big.Float).SetPrec(prec).Mul(ln2, new(big.Float).SetPrec(prec).SetInt64(k)))
	const halvings = 16
	r.SetMantExp(r, -halvings)
	sum := new(big.Float).SetPrec(prec).SetInt64(1)
	term := new(big.Float).SetPrec(prec).SetInt64(1)
	eps := new(big.Float).SetPrec(prec).SetMantExp(big.NewFloat(1), -int(prec)-8)
	for n := int64(1); ; n++ {
		term.Mul(term, r)
		term.Quo(term, new(big.Float).SetPrec(prec).SetInt64(n))
		sum.Add(sum, term)
		if new(big.Float).Abs(term).Cmp(eps) < 0 {
			break
		}
	}
	for range halvings {
		sum.Mul(sum, sum)
	}
	return sum.SetMantExp(sum, int(k))
}
