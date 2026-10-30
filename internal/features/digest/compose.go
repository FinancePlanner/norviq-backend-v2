package digest

import (
	"math"
	"strconv"
	"strings"
)

// Headline is DigestComposer.headline: "Your week: +€412.00 (+1.80%). NVDA
// did most of it."
func Headline(p *Payload) string {
	if len(p.Holdings) == 0 {
		return "Your week in Norviq"
	}
	primary := p.Holdings[0]
	line := "Your week: " + signedMoney(primary.Change, primary.Currency)
	if primary.ChangePercent != nil {
		line += " (" + signedPercent(*primary.ChangePercent) + ")"
	}
	line += "."
	if lead := leadMover(p, primary); lead != nil {
		line += " " + lead.Symbol + " did most of it."
	}
	return line
}

// Subject is DigestComposer.subject.
func Subject(p *Payload) string {
	if len(p.Holdings) == 0 {
		return "Your week in Norviq"
	}
	primary := p.Holdings[0]
	subject := "Your week: " + signedMoney(primary.Change, primary.Currency)
	if primary.ChangePercent != nil {
		subject += " (" + signedPercent(*primary.ChangePercent) + ")"
	}
	return subject
}

// leadMover is the top mover in move's currency when it alone accounts for
// at least half the week's move, in the same direction.
func leadMover(p *Payload, move CurrencyMove) *Mover {
	if move.Change == 0 {
		return nil
	}
	for i := range p.Movers {
		top := &p.Movers[i]
		if top.Currency != move.Currency {
			continue
		}
		if math.Signbit(top.Contribution) != math.Signbit(move.Change) ||
			math.Abs(top.Contribution) < math.Abs(move.Change)/2 {
			return nil
		}
		return top
	}
	return nil
}

// ---- Formatting -------------------------------------------------------------

// money is DigestComposer.money: a one-character symbol before the amount,
// otherwise "amount CODE".
func money(value float64, currency string) string {
	symbol := currencySymbol(currency)
	amount := number(math.Abs(value))
	sign := ""
	if value < 0 {
		sign = "-"
	}
	if len([]rune(symbol)) == 1 {
		return sign + symbol + amount
	}
	return sign + amount + " " + symbol
}

func signedMoney(value float64, currency string) string {
	if value > 0 {
		return "+" + money(value, currency)
	}
	return money(value, currency)
}

func signedPercent(value float64) string {
	if value > 0 {
		return "+" + percent(value)
	}
	return percent(value)
}

func currencySymbol(code string) string {
	switch strings.ToUpper(code) {
	case "EUR":
		return "€"
	case "USD":
		return "$"
	case "GBP":
		return "£"
	}
	return strings.ToUpper(code)
}

// percent is MoneyFormat.percent.
func percent(value float64) string { return number(value) + "%" }

// number is MoneyFormat.number: NumberFormatter in en_US_POSIX, "," grouping,
// exactly two decimals. ICU rounds the shortest decimal form of the double
// half-even (1.015 is "1.02", 0.125 is "0.12") and keeps the sign of a value
// that rounds to zero.
func number(value float64) string {
	neg := math.Signbit(value)
	digits := strconv.FormatFloat(math.Abs(value), 'f', -1, 64)
	whole, frac, _ := strings.Cut(digits, ".")
	if len(frac) > 2 {
		rest := frac[2:]
		frac = frac[:2]
		up := rest[0] > '5' || (rest[0] == '5' && (strings.TrimRight(rest[1:], "0") != "" || (frac[1]-'0')%2 == 1))
		if up {
			whole, frac = increment(whole, frac)
		}
	}
	for len(frac) < 2 {
		frac += "0"
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	b.WriteByte('.')
	b.WriteString(frac)
	return b.String()
}

// increment adds one unit in the last place of whole.frac (two digits).
func increment(whole, frac string) (string, string) {
	d := []byte(whole + frac)
	i := len(d) - 1
	for ; i >= 0; i-- {
		if d[i] == '9' {
			d[i] = '0'
			continue
		}
		d[i]++
		break
	}
	if i < 0 {
		d = append([]byte{'1'}, d...)
	}
	n := len(d) - 2
	return string(d[:n]), string(d[n:])
}
