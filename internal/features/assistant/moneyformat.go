package assistant

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// moneyNumber is MoneyFormat.number: a NumberFormatter pinned to en_US_POSIX,
// .decimal style, "," grouping, exactly two fraction digits. NumberFormatter
// (ICU underneath) rounds the shortest round-trip decimal of the double with
// its default half-even mode, so 0.125 is "0.12" and 2.675 is "2.68". The sign
// survives rounding to zero ("-0.00"), as ICU keeps it.
func moneyNumber(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "∞"
	case math.IsInf(v, -1):
		return "-∞"
	}
	digits := strconv.FormatFloat(math.Abs(v), 'f', -1, 64)
	intPart, frac, _ := strings.Cut(digits, ".")
	intPart, frac = moneyRoundHalfEven(intPart, frac, 2)
	var b strings.Builder
	if math.Signbit(v) {
		b.WriteByte('-')
	}
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	b.WriteByte('.')
	b.WriteString(frac)
	return b.String()
}

// moneyRoundHalfEven rounds the decimal intPart.frac to scale fraction digits,
// half to even, and returns frac padded to exactly scale digits.
func moneyRoundHalfEven(intPart, frac string, scale int) (roundedInt, roundedFrac string) {
	if len(frac) <= scale {
		return intPart, frac + strings.Repeat("0", scale-len(frac))
	}
	kept, rest := frac[:scale], frac[scale:]
	up := false
	switch {
	case rest[0] > '5':
		up = true
	case rest[0] == '5':
		if strings.Trim(rest[1:], "0") != "" {
			up = true
		} else {
			all := intPart + kept
			up = (all[len(all)-1]-'0')%2 == 1
		}
	}
	if !up {
		return intPart, kept
	}
	all := []byte(intPart + kept)
	i := len(all) - 1
	for ; i >= 0; i-- {
		if all[i] == '9' {
			all[i] = '0'
			continue
		}
		all[i]++
		break
	}
	s := string(all)
	if i < 0 {
		s = "1" + s
	}
	return s[:len(s)-scale], s[len(s)-scale:]
}

// moneyPercent is MoneyFormat.percent.
func moneyPercent(v float64) string { return moneyNumber(v) + "%" }

// moneyTrend is MoneyFormat.trend.
func moneyTrend(v float64) string {
	switch {
	case v > 0:
		return "up"
	case v < 0:
		return "down"
	default:
		return "flat"
	}
}

// moneyMonthStart is MoneyFormat.currentMonthStart: the first instant of
// now's month in UTC.
func moneyMonthStart(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}
