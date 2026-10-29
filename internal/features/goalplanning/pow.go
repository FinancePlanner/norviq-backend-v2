package goalplanning

import "math"

// pow is a correctly rounded x^y for the finite, positive bases the goal
// maths uses. Vapor's Foundation pow (Darwin libm, glibc) is correctly
// rounded in practice, while math.Pow can be a few ULP off; projections are
// compared bit for bit with Vapor's, so the result is computed in
// double-double (~100 bits) and rounded once.
//
// Every product below is wrapped in float64(...): the Go spec lets the
// compiler fuse x*y+z into an FMA (arm64 does), which would change bits.
func pow(x, y float64) float64 {
	switch {
	case y == 0 || x == 1:
		return 1
	case y == 1:
		return x
	case !(x > 0) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0):
		return math.Pow(x, y)
	}
	l := ddLog(x)
	p := ddMulFloat(l, y)
	if p.hi > 709 || p.hi < -745 {
		return math.Pow(x, y)
	}
	return ddExp(p).hi
}

// dd is an unevaluated sum hi + lo with |lo| <= ulp(hi)/2.
type dd struct{ hi, lo float64 }

func twoSum(a, b float64) dd {
	s := a + b
	bb := s - a
	return dd{s, (a - (s - bb)) + (b - bb)}
}

func quickTwoSum(a, b float64) dd {
	s := a + b
	return dd{s, b - (s - a)}
}

func twoProd(a, b float64) dd {
	p := float64(a * b)
	return dd{p, math.FMA(a, b, -p)}
}

func ddAdd(a, b dd) dd {
	s := twoSum(a.hi, b.hi)
	t := twoSum(a.lo, b.lo)
	s.lo += t.hi
	s = quickTwoSum(s.hi, s.lo)
	s.lo += t.lo
	return quickTwoSum(s.hi, s.lo)
}

func ddMul(a, b dd) dd {
	p := twoProd(a.hi, b.hi)
	p.lo += float64(a.hi*b.lo) + float64(a.lo*b.hi)
	return quickTwoSum(p.hi, p.lo)
}

func ddMulFloat(a dd, b float64) dd {
	p := twoProd(a.hi, b)
	p.lo += float64(a.lo * b)
	return quickTwoSum(p.hi, p.lo)
}

func ddDivFloat(a dd, b float64) dd {
	q1 := a.hi / b
	p := twoProd(q1, b)
	s := twoSum(a.hi, -p.hi)
	s.lo -= p.lo
	s.lo += a.lo
	q2 := (s.hi + s.lo) / b
	return quickTwoSum(q1, q2)
}

var ddLn2 = dd{0.6931471805599453, 2.3190468138462996e-17}

// ddExp is e^a: a = k·ln2 + r, then the Taylor series of r/1024 squared ten
// times.
func ddExp(a dd) dd {
	k := math.Round(a.hi / ddLn2.hi)
	r := ddAdd(a, ddMulFloat(ddLn2, -k))
	r = dd{math.Ldexp(r.hi, -10), math.Ldexp(r.lo, -10)}
	s := dd{1, 0}
	for i := 16; i >= 1; i-- {
		s = ddAdd(dd{1, 0}, ddDivFloat(ddMul(s, r), float64(i)))
	}
	for range 10 {
		s = ddMul(s, s)
	}
	e := int(k)
	return dd{math.Ldexp(s.hi, e), math.Ldexp(s.lo, e)}
}

// ddLog is ln x by Newton's method on ddExp, from math.Log's estimate.
func ddLog(x float64) dd {
	y := dd{math.Log(x), 0}
	for range 2 {
		e := ddExp(dd{-y.hi, -y.lo})
		t := ddAdd(ddMulFloat(e, x), dd{-1, 0})
		y = ddAdd(y, t)
	}
	return y
}
