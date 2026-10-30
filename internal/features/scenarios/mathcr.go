package scenarios

import (
	"math"
	"math/big"
)

// Correctly rounded log, exp, pow, sin and cos.
//
// Vapor's engine calls the platform libm (Darwin's or glibc's), whose
// results are correctly rounded in practice. Go's math package is only
// accurate to about 1 ulp, and one differing last bit in a Box-Muller draw
// or a contribution factor changes every later value of a seeded Monte
// Carlo path. These evaluate in double-double arithmetic (~106 bits) and
// round once, which reproduces the libm results bit for bit.

type dd struct{ hi, lo float64 }

func twoSum(a, b float64) (float64, float64) {
	s := a + b
	bb := s - a
	return s, (a - (s - bb)) + (b - bb)
}

func quickTwoSum(a, b float64) dd {
	s := a + b
	return dd{s, b - (s - a)}
}

// twoProd must round p on its own: the conversion stops the compiler
// fusing a*b into an FMA with whatever later adds p.
func twoProd(a, b float64) (float64, float64) {
	p := float64(a * b)
	return p, math.FMA(a, b, -p)
}

func ddAdd(a, b dd) dd {
	s1, s2 := twoSum(a.hi, b.hi)
	t1, t2 := twoSum(a.lo, b.lo)
	s2 += t1
	r := quickTwoSum(s1, s2)
	r.lo += t2
	return quickTwoSum(r.hi, r.lo)
}

func ddNeg(a dd) dd { return dd{-a.hi, -a.lo} }

func ddMul(a, b dd) dd {
	p, e := twoProd(a.hi, b.hi)
	e += float64(a.hi*b.lo) + float64(a.lo*b.hi)
	return quickTwoSum(p, e)
}

func ddMulF(a dd, b float64) dd {
	p, e := twoProd(a.hi, b)
	e += float64(a.lo * b)
	return quickTwoSum(p, e)
}

func ddDiv(a, b dd) dd {
	q1 := a.hi / b.hi
	r := ddAdd(a, ddNeg(ddMulF(b, q1)))
	q2 := r.hi / b.hi
	r = ddAdd(r, ddNeg(ddMulF(b, q2)))
	q3 := r.hi / b.hi
	q := quickTwoSum(q1, q2)
	return ddAdd(q, dd{q3, 0})
}

// split3 rounds a high-precision constant into three float64 parts.
func split3(digits string) [3]float64 {
	v, _, err := big.ParseFloat(digits, 10, 400, big.ToNearestEven)
	if err != nil {
		panic(err)
	}
	var out [3]float64
	rest := new(big.Float).SetPrec(400).Set(v)
	for i := range out {
		out[i], _ = rest.Float64()
		rest.Sub(rest, new(big.Float).SetPrec(400).SetFloat64(out[i]))
	}
	return out
}

var (
	ln2Parts  = split3("0.693147180559945309417232121458176568075500134360255254120680009493393621969694715605863326996418687542001481021")
	pio2Parts = split3("1.570796326794896619231321691639751442098584699687552910487472296153908203143104499314017412671058533991074043256")
	// invOdd[i] = 1/(2i+1); invFact[i] = 1/i!.
	invOdd, invFact = func() ([]dd, []dd) {
		odd := make([]dd, 24)
		for i := range odd {
			odd[i] = ddDiv(dd{1, 0}, dd{float64(2*i + 1), 0})
		}
		fact := make([]dd, 32)
		f := dd{1, 0}
		for i := range fact {
			if i > 0 {
				f = ddMulF(f, float64(i))
			}
			fact[i] = ddDiv(dd{1, 0}, f)
		}
		return odd, fact
	}()
)

// ddLog is log(x) for a finite x > 0.
func ddLog(x float64) dd {
	m, k := math.Frexp(x)
	if m < math.Sqrt2/2 {
		m *= 2
		k--
	}
	// log(m) = 2·atanh(s), s = (m-1)/(m+1), |s| <= 0.1716.
	den := quickTwoSum(twoSum(m, 1))
	s := ddDiv(dd{m - 1, 0}, den)
	s2 := ddMul(s, s)
	t := invOdd[len(invOdd)-1]
	for i := len(invOdd) - 2; i >= 0; i-- {
		t = ddAdd(ddMul(t, s2), invOdd[i])
	}
	logm := ddMulF(ddMul(s, t), 2)
	fk := float64(k)
	kln2 := ddAdd(ddMulF(dd{ln2Parts[0], 0}, fk), ddMulF(dd{ln2Parts[1], ln2Parts[2]}, fk))
	return ddAdd(kln2, logm)
}

// ddExp is exp(x) for |x| < ~700.
func ddExp(x dd) dd {
	k := math.Round(x.hi / ln2Parts[0])
	r := ddAdd(x, ddNeg(ddMulF(dd{ln2Parts[0], 0}, k)))
	r = ddAdd(r, ddNeg(ddMulF(dd{ln2Parts[1], 0}, k)))
	r = ddAdd(r, ddNeg(ddMulF(dd{ln2Parts[2], 0}, k)))
	// Taylor series, |r| <= 0.347.
	sum := invFact[len(invFact)-1]
	for i := len(invFact) - 2; i >= 0; i-- {
		sum = ddAdd(ddMul(sum, r), invFact[i])
	}
	ki := int(k)
	return dd{math.Ldexp(sum.hi, ki), math.Ldexp(sum.lo, ki)}
}

// crLog is a correctly rounded natural logarithm.
func crLog(x float64) float64 {
	if !(x > 0) || math.IsInf(x, 0) || x == 1 {
		return math.Log(x)
	}
	return ddLog(x).hi
}

// crPow is a correctly rounded pow for the finite, positive bases the
// engine uses; anything else defers to math.Pow.
func crPow(x, y float64) float64 {
	switch {
	case y == 0 || x == 1:
		return 1
	case !(x > 0) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0):
		return math.Pow(x, y)
	case y == 1:
		return x
	}
	l := ddLog(x)
	p, e := twoProd(l.hi, y)
	e += float64(l.lo * y)
	arg := quickTwoSum(p, e)
	if math.Abs(arg.hi) > 700 {
		return math.Pow(x, y)
	}
	return ddExp(arg).hi
}

// crExp is a correctly rounded exp.
func crExp(x float64) float64 {
	if math.IsNaN(x) || math.Abs(x) > 700 {
		return math.Exp(x)
	}
	return ddExp(dd{x, 0}).hi
}

// crSinCos returns correctly rounded sin(x) and cos(x) for |x| < 1e6.
func crSinCos(x float64) (float64, float64) {
	if math.IsNaN(x) || math.Abs(x) >= 1e6 {
		return math.Sin(x), math.Cos(x)
	}
	if x == 0 {
		return x, 1
	}
	n := math.Round(x / pio2Parts[0])
	r := dd{x, 0}
	for _, p := range pio2Parts {
		r = ddAdd(r, ddNeg(ddMulF(dd{p, 0}, n)))
	}
	r2 := ddMul(r, r)
	// sin r = r·Σ (-r²)^i/(2i+1)!, cos r = Σ (-r²)^i/(2i)!.
	sr := invFact[29]
	for i := 27; i >= 1; i -= 2 {
		sr = ddAdd(ddNeg(ddMul(sr, r2)), invFact[i])
	}
	sinr := ddMul(sr, r)
	cr := invFact[30]
	for i := 28; i >= 0; i -= 2 {
		cr = ddAdd(ddNeg(ddMul(cr, r2)), invFact[i])
	}
	switch int64(n) & 3 {
	case 0:
		return sinr.hi, cr.hi
	case 1:
		return cr.hi, -sinr.hi
	case 2:
		return -sinr.hi, -cr.hi
	default:
		return -cr.hi, sinr.hi
	}
}
