// Package nsdecimal reproduces Foundation's Decimal (NSDecimal) as shipped in
// swift-foundation release/6.2, which is what the Vapor backend runs on.
//
// Vapor's tax and financing code does its money arithmetic in Decimal and
// serializes the results with JSONEncoder, so the exact digits a response
// carries depend on Foundation's quirks: a 128-bit mantissa that is
// truncated (never rounded) when a product or quotient does not fit,
// precision lost while aligning exponents in additions, Decimal(Double)'s
// multiply-by-ten conversion (Decimal(3.133) is 3.132999999999999488), and
// doubleValue's divide-by-ten loop. Porting that code with float64 or an
// exact decimal library would change the last digits of many responses.
//
// The algorithms below mirror Decimal+Math.swift and Decimal+Conformances.swift
// step by step; testdata/vectors.txt holds results captured from Swift.
package nsdecimal

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

const (
	maxWords = 8 // NSDecimalMaxSize: eight 16-bit mantissa words
	maxExp   = 127
	minExp   = -128
)

// Decimal is an immutable Foundation Decimal. The zero value is 0.
type Decimal struct {
	mant *big.Int // magnitude; nil or 0 means zero
	exp  int32
	neg  bool
	nan  bool
}

// RoundingMode mirrors NSDecimalNumber.RoundingMode.
type RoundingMode int

const (
	RoundPlain RoundingMode = iota
	RoundDown
	RoundUp
	RoundBankers
)

var (
	ten      = big.NewInt(10)
	tenK     = big.NewInt(10000)
	pow10Tab = func() []*big.Int {
		t := make([]*big.Int, 80)
		t[0] = big.NewInt(1)
		for i := 1; i < len(t); i++ {
			t[i] = new(big.Int).Mul(t[i-1], ten)
		}
		return t
	}()
)

func pow10(n int) *big.Int {
	if n < len(pow10Tab) {
		return pow10Tab[n]
	}
	return new(big.Int).Exp(ten, big.NewInt(int64(n)), nil)
}

// Zero is 0.
var Zero = Decimal{}

// NaN is Decimal.nan.
var NaN = Decimal{nan: true}

func words(v *big.Int) int {
	if v == nil {
		return 0
	}
	return (v.BitLen() + 15) / 16
}

func (d Decimal) isZero() bool { return !d.nan && (d.mant == nil || d.mant.Sign() == 0) }

// IsZero reports d == 0.
func (d Decimal) IsZero() bool { return d.isZero() }

// IsNaN reports whether d is NaN.
func (d Decimal) IsNaN() bool { return d.nan }

func (d Decimal) m() *big.Int {
	if d.mant == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(d.mant)
}

// compact mirrors Decimal.compact(): strip trailing decimal zeros.
func compact(neg bool, mant *big.Int, exp int32) Decimal {
	if mant.Sign() == 0 {
		return Zero
	}
	q, r := new(big.Int), new(big.Int)
	for {
		q.QuoRem(mant, ten, r)
		if r.Sign() != 0 {
			break
		}
		mant = new(big.Int).Set(q)
		exp++
	}
	for exp > maxExp {
		mant = new(big.Int).Mul(mant, ten)
		exp--
	}
	return Decimal{mant: mant, exp: exp, neg: neg}
}

// FromInt is Decimal(Int).
func FromInt(v int64) Decimal {
	if v == 0 {
		return Zero
	}
	neg := v < 0
	u := new(big.Int).Abs(big.NewInt(v))
	return compact(neg, u, 0)
}

// FromFloat64 is Decimal(Double), including its multiply-by-ten drift.
func FromFloat64(value float64) Decimal {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return NaN
	}
	if value == 0 {
		return Zero
	}
	neg := value < 0
	val := value
	if neg {
		val = -1 * value
	}
	var exponent int32
	const two64 = 18446744073709551616.0 // Double(UInt64.max - 1) == Double(UInt64.max)
	for val < two64 {
		if exponent <= -128 {
			return NaN
		}
		val *= 10.0
		exponent--
	}
	for two64 <= val {
		if exponent >= 127 {
			return NaN
		}
		val /= 10.0
		exponent++
	}
	maxMantissa := math.Nextafter(two64, 0)
	var mantissa uint64
	if val > maxMantissa {
		mantissa = uint64(maxMantissa)
	} else {
		mantissa = uint64(val)
	}
	return compact(neg, new(big.Int).SetUint64(mantissa), exponent)
}

// MustParse parses a decimal literal or panics (for constants).
func MustParse(s string) Decimal {
	d, ok := Parse(s)
	if !ok {
		panic("nsdecimal: bad literal " + s)
	}
	return d
}

// Parse is Decimal(string:) with a "." separator. ok is false where Swift
// returns nil.
func Parse(s string) (Decimal, bool) {
	i := 0
	n := len(s)
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' }
	for i < n && isSpace(s[i]) {
		i++
	}
	start := i
	neg := false
	if i < n && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	mant := new(big.Int)
	var exp int32
	tooBig := false
	limit := new(big.Int).Lsh(big.NewInt(1), 16*maxWords)
	digits := 0
	addDigit := func(dgt int64) bool {
		next := new(big.Int).Mul(mant, ten)
		next.Add(next, big.NewInt(dgt))
		if next.Cmp(limit) >= 0 {
			return false
		}
		mant = next
		return true
	}
	for i < n && s[i] >= '0' && s[i] <= '9' {
		digits++
		if tooBig {
			exp++
		} else if !addDigit(int64(s[i] - '0')) {
			tooBig = true
			exp++
		}
		i++
	}
	if i < n && s[i] == '.' {
		i++
		for i < n && s[i] >= '0' && s[i] <= '9' {
			digits++
			if !tooBig {
				if addDigit(int64(s[i] - '0')) {
					exp--
				} else {
					tooBig = true
				}
			}
			i++
		}
	}
	if digits == 0 {
		return Decimal{}, false
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		i++
		eneg := false
		if i < n && (s[i] == '+' || s[i] == '-') {
			eneg = s[i] == '-'
			i++
		}
		e := 0
		for i < n && s[i] >= '0' && s[i] <= '9' {
			e = e*10 + int(s[i]-'0')
			if e > 10000 {
				return Decimal{}, false
			}
			i++
		}
		if eneg {
			e = -e
		}
		e += int(exp)
		if e > maxExp || e < minExp {
			return Decimal{}, false
		}
		exp = int32(e)
	}
	for i < n && isSpace(s[i]) {
		i++
	}
	if i != n || i == start {
		return Decimal{}, false
	}
	return compact(neg, mant, exp), true
}

// String is Decimal.description (also what JSONEncoder writes).
func (d Decimal) String() string {
	if d.nan {
		return "NaN"
	}
	if d.isZero() {
		return "0"
	}
	digits := d.mant.String()
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	switch {
	case d.exp >= 0:
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", int(d.exp)))
	default:
		frac := int(-d.exp)
		if frac >= len(digits) {
			b.WriteString("0.")
			b.WriteString(strings.Repeat("0", frac-len(digits)))
			b.WriteString(digits)
		} else {
			b.WriteString(digits[:len(digits)-frac])
			b.WriteByte('.')
			b.WriteString(digits[len(digits)-frac:])
		}
	}
	return b.String()
}

// Float64 is NSDecimalNumber(decimal:).doubleValue: the mantissa as a
// double, then divided (or multiplied) by ten once per exponent step.
func (d Decimal) Float64() float64 {
	if d.nan {
		return math.NaN()
	}
	if d.isZero() {
		return 0
	}
	// Rebuild 16-bit words from the magnitude.
	b := d.mant.Bytes() // big-endian
	var w []uint16
	for i := len(b); i > 0; i -= 2 {
		lo := b[i-1]
		var hi byte
		if i-2 >= 0 {
			hi = b[i-2]
		}
		w = append(w, uint16(hi)<<8|uint16(lo))
	}
	dv := 0.0
	top := len(w)
	if top > maxWords {
		top = maxWords
	}
	for idx := top - 1; idx >= 0; idx-- {
		dv = dv*65536 + float64(w[idx])
	}
	if d.exp < 0 {
		for e := d.exp; e < 0; e++ {
			dv /= 10.0
		}
	} else {
		for e := int32(0); e < d.exp; e++ {
			dv *= 10.0
		}
	}
	if d.neg {
		return -dv
	}
	return dv
}

// fitMantissa mirrors Decimal._fitMantissa. Its rounding branch computes a
// rounded value and then discards it, so the result is always truncated.
func fitMantissa(v *big.Int) (*big.Int, int32) {
	if words(v) <= maxWords {
		return v, 0
	}
	r := new(big.Int).Set(v)
	var e int32
	for words(r) > maxWords+1 {
		r.Quo(r, tenK)
		e += 4
	}
	for words(r) > maxWords {
		r.Quo(r, ten)
		e++
	}
	return r, e
}

// normalize mirrors Decimal._normalize. a and b get the same exponent;
// the one with the smaller exponent loses digits when the other cannot be
// scaled up far enough.
func normalize(a, b Decimal) (Decimal, Decimal) {
	diff := int(a.exp) - int(b.exp)
	if diff == 0 {
		return a, b
	}
	swapped := false
	aa, bb := a, b // aa has the larger exponent
	if diff < 0 {
		aa, bb = b, a
		diff = -diff
		swapped = true
	}
	prod := new(big.Int).Mul(aa.m(), pow10(diff))
	if words(prod) <= maxWords {
		aa = Decimal{mant: prod, exp: bb.exp, neg: aa.neg}
	} else {
		maxPow := int(math.Floor(float64(maxWords-words(aa.mant)) * 4.81647993))
		q := new(big.Int).Quo(bb.m(), pow10(diff-maxPow))
		bb = Decimal{mant: q, exp: bb.exp + int32(diff-maxPow), neg: bb.neg} //nolint:gosec // exponents are bounded by int8 range
		if q.Sign() != 0 {
			aa = Decimal{mant: new(big.Int).Mul(aa.m(), pow10(maxPow)), exp: aa.exp - int32(maxPow), neg: aa.neg} //nolint:gosec // exponents are bounded by int8 range
		} else {
			bb.exp = aa.exp
		}
	}
	if swapped {
		return bb, aa
	}
	return aa, bb
}

// Add is lhs + rhs.
func (d Decimal) Add(o Decimal) Decimal {
	if d.nan || o.nan {
		return NaN
	}
	if d.isZero() {
		return o
	}
	if o.isZero() {
		return d
	}
	a, b := normalize(d, o)
	if a.isZero() {
		return b
	}
	if b.isZero() {
		return a
	}
	if a.neg == b.neg {
		sum := new(big.Int).Add(a.mant, b.mant)
		exp := a.exp
		if words(sum) > maxWords {
			var e int32
			sum, e = fitMantissa(sum)
			if exp+e > maxExp {
				return NaN
			}
			exp += e
		}
		return compact(a.neg, sum, exp)
	}
	switch a.mant.Cmp(b.mant) {
	case 0:
		return Zero
	case -1:
		return compact(b.neg, new(big.Int).Sub(b.mant, a.mant), a.exp)
	default:
		return compact(a.neg, new(big.Int).Sub(a.mant, b.mant), a.exp)
	}
}

// Neg is -d.
func (d Decimal) Neg() Decimal {
	if d.isZero() || d.nan {
		return d
	}
	d.neg = !d.neg
	return d
}

// Sub is lhs - rhs.
func (d Decimal) Sub(o Decimal) Decimal { return d.Add(o.Neg()) }

// Mul is lhs * rhs.
func (d Decimal) Mul(o Decimal) Decimal {
	if d.nan || o.nan {
		return NaN
	}
	if d.isZero() || o.isZero() {
		return Zero
	}
	prod := new(big.Int).Mul(d.mant, o.mant)
	exp := d.exp + o.exp
	if words(prod) > maxWords {
		var e int32
		prod, e = fitMantissa(prod)
		exp += e
	}
	if exp > maxExp {
		return NaN
	}
	return compact(d.neg != o.neg, prod, exp)
}

// Div is lhs / rhs (NaN on division by zero, like the Swift operator).
func (d Decimal) Div(o Decimal) Decimal {
	if d.nan || o.nan || o.isZero() {
		return NaN
	}
	if d.isZero() {
		return Zero
	}
	a, b := d, o
	if int(a.exp)-int(b.exp) >= 19 {
		a, b = normalize(a, b)
		if a.isZero() || b.isZero() {
			a, b = d, o
		}
	}
	big38 := new(big.Int).Mul(a.mant, pow10(38))
	q := new(big.Int).Quo(big38, b.mant)
	q, e := fitMantissa(q)
	exp := int(a.exp) - int(b.exp) - 38 + int(e)
	if exp < minExp || exp > maxExp {
		return NaN
	}
	return compact(a.neg != b.neg, q, int32(exp))
}

// Cmp mirrors Decimal._compare (which aligns exponents with truncation).
func (d Decimal) Cmp(o Decimal) int {
	if d.nan {
		if o.nan {
			return 0
		}
		return -1
	}
	if o.nan {
		return 1
	}
	if d.neg && !o.neg && !d.isZero() {
		return -1
	}
	if !d.neg && o.neg && !o.isZero() {
		return 1
	}
	if d.isZero() {
		if o.isZero() {
			return 0
		}
		return -1
	}
	if o.isZero() {
		return 1
	}
	a, b := normalize(d, o)
	c := a.m().Cmp(b.m())
	if a.neg {
		return -c
	}
	return c
}

// Equal is ==.
func (d Decimal) Equal(o Decimal) bool {
	if d.nan && o.nan {
		return true
	}
	return d.Cmp(o) == 0
}

// LessThan is <.
func (d Decimal) LessThan(o Decimal) bool { return d.Cmp(o) < 0 }

// Sign returns -1, 0 or 1.
func (d Decimal) Sign() int {
	if d.isZero() || d.nan {
		return 0
	}
	if d.neg {
		return -1
	}
	return 1
}

// Max is Swift's max(a, b): b when b >= a, else a.
func Max(a, b Decimal) Decimal {
	if b.Cmp(a) >= 0 {
		return b
	}
	return a
}

// Min is Swift's min(a, b): b when b < a, else a.
func Min(a, b Decimal) Decimal {
	if b.Cmp(a) < 0 {
		return b
	}
	return a
}

// Abs is the magnitude.
func (d Decimal) Abs() Decimal {
	d.neg = false
	return d
}

// Round is NSDecimalRound(&r, &d, scale, mode).
func (d Decimal) Round(scale int, mode RoundingMode) Decimal {
	if d.nan || d.isZero() {
		return d
	}
	s := scale + int(d.exp)
	if s >= 0 {
		return d
	}
	s = -s
	exponent := int(d.exp) + s
	result := d.m()
	var remainder, premainder int64
	r := new(big.Int)
	for s > 4 {
		if remainder != 0 {
			premainder = 1
		}
		result.QuoRem(result, tenK, r)
		remainder = r.Int64()
		s -= 4
	}
	for s != 0 {
		if remainder != 0 {
			premainder = 1
		}
		result.QuoRem(result, ten, r)
		remainder = r.Int64()
		s--
	}
	// Swift compares remainder (0...9999 after a /10000 step) against 5.
	if premainder != 0 && (remainder == 0 || remainder == 5) {
		remainder++
	}
	neg := d.neg
	if remainder != 0 {
		inc := false
		if neg {
			switch mode {
			case RoundUp:
			case RoundBankers:
				if remainder == 5 && result.Bit(0) == 0 {
					remainder--
				}
				inc = remainder >= 5
			case RoundPlain:
				inc = remainder >= 5
			case RoundDown:
				inc = true
			}
		} else {
			switch mode {
			case RoundDown:
			case RoundBankers:
				if remainder == 5 && result.Bit(0) == 0 {
					remainder--
				}
				inc = remainder >= 5
			case RoundPlain:
				inc = remainder >= 5
			case RoundUp:
				inc = true
			}
		}
		if inc {
			result.Add(result, big.NewInt(1))
		}
	}
	for exponent > maxExp {
		exponent--
		result.Mul(result, ten)
	}
	return compact(neg, result, int32(exponent)) //nolint:gosec // exponent is bounded by int8 range plus a scale
}

// MarshalJSON writes the description as a JSON number, as JSONEncoder does.
func (d Decimal) MarshalJSON() ([]byte, error) {
	if d.nan {
		return nil, errors.New("nsdecimal: NaN is not valid JSON")
	}
	return []byte(d.String()), nil
}

// UnmarshalJSON accepts a JSON number (or a numeric string, which
// JSONDecoder also accepts for Decimal).
func (d *Decimal) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return errors.New("nsdecimal: null is not a Decimal")
	}
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	v, ok := Parse(s)
	if !ok {
		return fmt.Errorf("nsdecimal: invalid number %q", s)
	}
	*d = v
	return nil
}

// Scan reads a numeric or double column (Fluent maps Double columns
// through Decimal(Double) at the call site; this is for numeric).
func (d *Decimal) Scan(src any) error {
	switch v := src.(type) {
	case float64:
		*d = FromFloat64(v)
	case int64:
		*d = FromInt(v)
	case string:
		p, ok := Parse(v)
		if !ok {
			return fmt.Errorf("nsdecimal: scan %q", v)
		}
		*d = p
	case []byte:
		p, ok := Parse(string(v))
		if !ok {
			return fmt.Errorf("nsdecimal: scan %q", v)
		}
		*d = p
	case nil:
		*d = Zero
	default:
		return fmt.Errorf("nsdecimal: cannot scan %T", src)
	}
	return nil
}

// Value writes the description.
func (d Decimal) Value() (driver.Value, error) { return d.String(), nil }

// Sum adds values left to right (Swift's reduce(0, +)).
func Sum(vs ...Decimal) Decimal {
	out := Zero
	for _, v := range vs {
		out = out.Add(v)
	}
	return out
}
