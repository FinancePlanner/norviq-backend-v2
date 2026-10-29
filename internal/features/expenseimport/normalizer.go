package expenseimport

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// SpreadsheetRowNormalizer: detected cells into expense-shaped values.

// RowIssue is SpreadsheetRowNormalizer.RowIssue.
type RowIssue string

// Row issues.
const (
	IssueInvalidDate       RowIssue = "invalidDate"
	IssueInvalidAmount     RowIssue = "invalidAmount"
	IssueMissingTitle      RowIssue = "missingTitle"
	IssueNeedsExchangeRate RowIssue = "needsExchangeRate"
	IssueAggregateRow      RowIssue = "aggregateRow"
)

// NormalizedRow is SpreadsheetRowNormalizer.NormalizedRow.
type NormalizedRow struct {
	Row                 int
	Title               *string
	Amount              *float64
	OccurredOn          *string
	SourceCategoryValue *string
	Currency            *string
	ForeignAmount       *float64
	ExchangeRate        *float64
	Notes               *string
	Issue               *RowIssue
}

// IsImportable is issue == nil.
func (r NormalizedRow) IsImportable() bool { return r.Issue == nil }

// DateOrder is SpreadsheetRowNormalizer.DateOrder.
type DateOrder string

// Date orders.
const (
	DayFirst   DateOrder = "dayFirst"
	MonthFirst DateOrder = "monthFirst"
	Ambiguous  DateOrder = "ambiguous"
)

func parseDateOrder(raw string) (DateOrder, bool) {
	switch d := DateOrder(raw); d {
	case DayFirst, MonthFirst, Ambiguous:
		return d, true
	}
	return "", false
}

// DecimalSeparator is SpreadsheetRowNormalizer.DecimalSeparator.
type DecimalSeparator string

// Separators.
const (
	SeparatorDot   DecimalSeparator = "dot"
	SeparatorComma DecimalSeparator = "comma"
)

func parseSeparator(raw string) (DecimalSeparator, bool) {
	switch s := DecimalSeparator(raw); s {
	case SeparatorDot, SeparatorComma:
		return s, true
	}
	return "", false
}

// NormalizerSettings are SpreadsheetRowNormalizer.Settings.
type NormalizerSettings struct {
	DateOrder         DateOrder
	DecimalSeparator  DecimalSeparator
	NegativeIsExpense bool
	ExchangeRates     map[string]float64
	BaseCurrency      *string
	DefaultCurrency   *string
}

// DefaultSettings is Settings().
func DefaultSettings() NormalizerSettings {
	return NormalizerSettings{DateOrder: DayFirst, DecimalSeparator: SeparatorDot, ExchangeRates: map[string]float64{}}
}

// InferDateOrder settles day-first versus month-first from the values that
// can only read one way.
func InferDateOrder(values []string) DateOrder {
	firstOver, secondOver := false, false
	for _, v := range values {
		var parts []int
		for _, piece := range strings.FieldsFunc(strings.TrimSpace(v), func(r rune) bool { return r == '/' || r == '-' || r == '.' }) {
			if n, ok := swiftInt(piece); ok {
				parts = append(parts, n)
			}
		}
		if len(parts) < 3 {
			continue
		}
		if parts[0] > 31 {
			continue
		}
		if parts[0] > 12 {
			firstOver = true
		}
		if parts[1] > 12 {
			secondOver = true
		}
	}
	if firstOver && !secondOver {
		return DayFirst
	}
	if secondOver && !firstOver {
		return MonthFirst
	}
	return Ambiguous
}

// swiftInt is Int(String): optional sign, ASCII digits only.
func swiftInt(s string) (int, bool) {
	if s == "" || strings.ContainsRune(s, '_') {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// InferDecimalSeparator decides the decimal point for a column of amounts.
func InferDecimalSeparator(values []string) DecimalSeparator {
	comma, dot := 0, 0
	for _, v := range values {
		d := nonDigitsRe.ReplaceAllString(v, "")
		if d == "" {
			continue
		}
		lc, ld := strings.LastIndexByte(d, ','), strings.LastIndexByte(d, '.')
		switch {
		case lc >= 0 && ld >= 0:
			if lc > ld {
				comma++
			} else {
				dot++
			}
		case lc >= 0:
			switch len(d) - lc - 1 {
			case 2:
				comma++
			case 3:
				dot++
			}
		case ld >= 0:
			switch len(d) - ld - 1 {
			case 2:
				dot++
			case 3:
				comma++
			}
		}
	}
	if comma > dot {
		return SeparatorComma
	}
	return SeparatorDot
}

// ParseAmount reads an amount under the given decimal separator.
func ParseAmount(raw string, sep DecimalSeparator) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	negative := false
	if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") && len(text) >= 2 {
		negative = true
		text = text[1 : len(text)-1]
	}
	if strings.HasPrefix(text, "-") {
		negative = true
	}
	digits := nonDigitsRe.ReplaceAllString(text, "")
	if digits == "" {
		return 0, false
	}
	switch sep {
	case SeparatorComma:
		digits = strings.ReplaceAll(digits, ".", "")
		digits = strings.ReplaceAll(digits, ",", ".")
	default:
		digits = strings.ReplaceAll(digits, ",", "")
	}
	if strings.Count(digits, ".") > 1 {
		digits = keepLastDot(digits)
	}
	v, ok := swiftDouble(digits)
	if !ok {
		return 0, false
	}
	if negative {
		return -math.Abs(v), true
	}
	return v, true
}

var (
	numericDayFirst   = []string{"dd/MM/yyyy", "dd-MM-yyyy", "dd.MM.yyyy"}
	numericMonthFirst = []string{"MM/dd/yyyy", "MM-dd-yyyy", "MM.dd.yyyy"}
	shortDayFirst     = []string{"dd/MM/yy", "dd-MM-yy", "dd.MM.yy"}
	shortMonthFirst   = []string{"MM/dd/yy", "MM-dd-yy", "MM.dd.yy"}
	spelledPatterns   = []string{"d MMMM yyyy", "d MMM yyyy", "MMMM d, yyyy", "MMM d, yyyy"}
	nowFunc           = time.Now
)

// ParseDate parses a text date in the column's order.
func ParseDate(raw string, order DateOrder) (time.Time, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return time.Time{}, false
	}
	now := nowFunc()
	for _, p := range []string{"yyyy-MM-dd", "yyyy/MM/dd"} {
		if t, ok := icuParse(p, text, now); ok {
			return t, true
		}
	}
	var ordered []string
	if order == MonthFirst {
		ordered = concat(numericMonthFirst, shortMonthFirst, numericDayFirst, shortDayFirst)
	} else {
		ordered = concat(numericDayFirst, shortDayFirst, numericMonthFirst, shortMonthFirst)
	}
	for _, p := range append(ordered, spelledPatterns...) {
		if t, ok := icuParse(p, text, now); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// Normalize is SpreadsheetRowNormalizer.normalize.
func Normalize(row int, cells map[ColumnRole]Cell, s NormalizerSettings, isAggregate bool) NormalizedRow {
	text := func(role ColumnRole) *string {
		c, ok := cells[role]
		if !ok || c.IsEmpty() {
			return nil
		}
		v := c.DisplayText()
		return &v
	}
	trimmed := func(p *string) *string {
		if p == nil {
			return nil
		}
		v := strings.TrimSpace(*p)
		return &v
	}
	title := trimmed(text(RoleTitle))
	sourceCategory := trimmed(text(RoleCategory))
	notes := text(RoleNotes)
	currency := text(RoleCurrency)
	if currency == nil {
		currency = s.DefaultCurrency
	}
	if currency != nil {
		up := strings.ToUpper(*currency)
		currency = &up
	}
	issue := func(i RowIssue) *RowIssue { return &i }

	if isAggregate {
		return NormalizedRow{Row: row, Title: title, SourceCategoryValue: sourceCategory, Currency: currency, Notes: notes, Issue: issue(IssueAggregateRow)}
	}

	var occurredOn *string
	if c, ok := cells[RoleDate]; ok && !c.IsEmpty() {
		if c.Kind == CellDate {
			v := isoString(c.Date)
			occurredOn = &v
		} else if t, ok := ParseDate(c.DisplayText(), s.DateOrder); ok {
			v := isoString(t)
			occurredOn = &v
		}
	}

	var amount *float64
	if c, ok := cells[RoleAmount]; ok && !c.IsEmpty() {
		if c.Kind == CellNumber {
			v := c.Number
			amount = &v
		} else if v, ok := ParseAmount(c.DisplayText(), s.DecimalSeparator); ok {
			amount = &v
		}
	}
	if amount != nil {
		v := *amount
		if s.NegativeIsExpense {
			v = -v
		}
		if math.Abs(v) < 0.0000001 {
			amount = nil
		} else {
			a := math.Abs(v)
			amount = &a
		}
	}

	failing := func(i RowIssue) NormalizedRow {
		return NormalizedRow{Row: row, Title: title, Amount: amount, OccurredOn: occurredOn, SourceCategoryValue: sourceCategory, Currency: currency, Notes: notes, Issue: issue(i)}
	}
	if occurredOn == nil {
		return failing(IssueInvalidDate)
	}
	if amount == nil {
		return failing(IssueInvalidAmount)
	}
	if title == nil || *title == "" {
		return failing(IssueMissingTitle)
	}

	final := *amount
	var foreign, applied *float64
	if currency != nil && s.BaseCurrency != nil && *currency != strings.ToUpper(*s.BaseCurrency) { //nolint:gocritic // only the base is upper-cased, as in Swift
		rate, ok := s.ExchangeRates[*currency]
		if !ok || rate <= 0 {
			f := *amount
			return NormalizedRow{
				Row: row, Title: title, Amount: amount, OccurredOn: occurredOn, SourceCategoryValue: sourceCategory,
				Currency: currency, ForeignAmount: &f, Notes: notes, Issue: issue(IssueNeedsExchangeRate),
			}
		}
		f := *amount
		foreign, applied = &f, &rate
		final = math.Round(*amount*rate*100) / 100
	}
	t := prefixRunes(*title, 200)
	return NormalizedRow{
		Row: row, Title: &t, Amount: &final, OccurredOn: occurredOn, SourceCategoryValue: sourceCategory,
		Currency: currency, ForeignAmount: foreign, ExchangeRate: applied, Notes: notes,
	}
}

func prefixRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
