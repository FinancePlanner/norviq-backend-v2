package expenseimport

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Ports SpreadsheetRowNormalizerTests.swift.

func TestInfersDecimalSeparator(t *testing.T) {
	cases := []struct {
		values []string
		want   DecimalSeparator
	}{
		{[]string{"1.234,56", "1.234"}, SeparatorComma},
		{[]string{"1,234.56", "99.90"}, SeparatorDot},
		{[]string{"84,20", "13,99"}, SeparatorComma},
		{[]string{"84.20", "13.99"}, SeparatorDot},
	}
	for _, c := range cases {
		if got := InferDecimalSeparator(c.values); got != c.want {
			t.Errorf("%v -> %s, want %s", c.values, got, c.want)
		}
	}
}

func TestParsesAmounts(t *testing.T) {
	cases := []struct {
		raw  string
		sep  DecimalSeparator
		want float64
	}{
		{"1.234,56", SeparatorComma, 1234.56},
		{"1,234.56", SeparatorDot, 1234.56},
		{"84,20 €", SeparatorComma, 84.20},
		{"€ 84.20", SeparatorDot, 84.20},
		{"1.234.567,89", SeparatorComma, 1_234_567.89},
		{"(123.45)", SeparatorDot, -123.45},
		{"-123.45", SeparatorDot, -123.45},
	}
	for _, c := range cases {
		if got, ok := ParseAmount(c.raw, c.sep); !ok || got != c.want {
			t.Errorf("%q -> %v %v, want %v", c.raw, got, ok, c.want)
		}
	}
	for _, raw := range []string{"", "n/a"} {
		if _, ok := ParseAmount(raw, SeparatorDot); ok {
			t.Errorf("%q should not parse", raw)
		}
	}
}

func TestInfersDateOrder(t *testing.T) {
	if got := InferDateOrder([]string{"13/04/2026", "03/04/2026"}); got != DayFirst {
		t.Errorf("got %s", got)
	}
	if got := InferDateOrder([]string{"04/13/2026", "04/03/2026"}); got != MonthFirst {
		t.Errorf("got %s", got)
	}
	if got := InferDateOrder([]string{"03/04/2026", "05/06/2026"}); got != Ambiguous {
		t.Errorf("got %s", got)
	}
}

func mustDate(t *testing.T, raw string, order DateOrder) string {
	t.Helper()
	d, ok := ParseDate(raw, order)
	if !ok {
		t.Fatalf("%q (%s) did not parse", raw, order)
	}
	return isoString(d)
}

func TestParsesDatesByOrder(t *testing.T) {
	if got := mustDate(t, "03/04/2026", DayFirst); got != "2026-04-03" {
		t.Errorf("dayFirst %s", got)
	}
	if got := mustDate(t, "03/04/2026", MonthFirst); got != "2026-03-04" {
		t.Errorf("monthFirst %s", got)
	}
}

func TestParsesISORegardlessOfOrder(t *testing.T) {
	for _, o := range []DateOrder{DayFirst, MonthFirst, Ambiguous} {
		if got := mustDate(t, "2026-01-03", o); got != "2026-01-03" {
			t.Errorf("%s: %s", o, got)
		}
	}
}

func TestParsesSpelledOutMonths(t *testing.T) {
	if got := mustDate(t, "3 April 2026", DayFirst); got != "2026-04-03" {
		t.Errorf("got %s", got)
	}
}

func TestRejectsInvalidDates(t *testing.T) {
	for _, raw := range []string{"not a date", "", "32/13/2026"} {
		if _, ok := ParseDate(raw, DayFirst); ok {
			t.Errorf("%q should not parse", raw)
		}
	}
}

func normRow(cells map[ColumnRole]Cell, s NormalizerSettings, aggregate bool) NormalizedRow {
	return Normalize(6, cells, s, aggregate)
}

func jan3(t *testing.T) Cell {
	t.Helper()
	d, ok := ParseDate("2026-01-03", DayFirst)
	if !ok {
		t.Fatal("parse")
	}
	return dateCell(d)
}

func issueOf(r NormalizedRow) RowIssue {
	if r.Issue == nil {
		return ""
	}
	return *r.Issue
}

func TestNormalizesCompleteRow(t *testing.T) {
	r := normRow(map[ColumnRole]Cell{
		RoleDate: jan3(t), RoleTitle: textCell("Continente"), RoleAmount: numberCell(84.20), RoleCategory: textCell("Supermercado"),
	}, DefaultSettings(), false)
	if !r.IsImportable() || *r.Title != "Continente" || *r.Amount != 84.20 || *r.OccurredOn != "2026-01-03" || *r.SourceCategoryValue != "Supermercado" {
		t.Fatalf("%+v", r)
	}
}

func TestReportsMissingFields(t *testing.T) {
	s := DefaultSettings()
	if got := issueOf(normRow(map[ColumnRole]Cell{RoleTitle: textCell("x"), RoleAmount: numberCell(1)}, s, false)); got != IssueInvalidDate {
		t.Errorf("got %s", got)
	}
	if got := issueOf(normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleTitle: textCell("x")}, s, false)); got != IssueInvalidAmount {
		t.Errorf("got %s", got)
	}
	if got := issueOf(normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleAmount: numberCell(1)}, s, false)); got != IssueMissingTitle {
		t.Errorf("got %s", got)
	}
}

func TestReportsAggregateRow(t *testing.T) {
	r := normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleTitle: textCell("TOTAL"), RoleAmount: numberCell(954.59)}, DefaultSettings(), true)
	if issueOf(r) != IssueAggregateRow || r.IsImportable() {
		t.Fatalf("%+v", r)
	}
}

func TestHonoursNegativeIsExpense(t *testing.T) {
	s := DefaultSettings()
	s.NegativeIsExpense = true
	r := normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleTitle: textCell("Card payment"), RoleAmount: numberCell(-42.50)}, s, false)
	if r.Amount == nil || *r.Amount != 42.50 {
		t.Fatalf("%+v", r)
	}
}

func eurSettings() NormalizerSettings {
	s := DefaultSettings()
	eur := "EUR"
	s.BaseCurrency = &eur
	return s
}

func TestWithholdsRowsNeedingRate(t *testing.T) {
	r := normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleTitle: textCell("London"), RoleAmount: numberCell(100), RoleCurrency: textCell("GBP")}, eurSettings(), false)
	if issueOf(r) != IssueNeedsExchangeRate || r.ForeignAmount == nil || *r.ForeignAmount != 100 || r.ExchangeRate != nil {
		t.Fatalf("%+v", r)
	}
}

func TestAppliesExchangeRate(t *testing.T) {
	s := eurSettings()
	s.ExchangeRates = map[string]float64{"GBP": 1.17}
	r := normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleTitle: textCell("London"), RoleAmount: numberCell(100), RoleCurrency: textCell("GBP")}, s, false)
	if !r.IsImportable() || *r.Amount != 117.0 || *r.ForeignAmount != 100 || *r.ExchangeRate != 1.17 {
		t.Fatalf("%+v", r)
	}
}

func TestBaseCurrencyNeedsNoRate(t *testing.T) {
	r := normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleTitle: textCell("Lisboa"), RoleAmount: numberCell(50), RoleCurrency: textCell("eur")}, eurSettings(), false)
	if !r.IsImportable() || *r.Amount != 50 || r.ExchangeRate != nil {
		t.Fatalf("%+v", r)
	}
}

func TestTruncatesLongTitles(t *testing.T) {
	r := normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleTitle: textCell(strings.Repeat("a", 500)), RoleAmount: numberCell(1)}, DefaultSettings(), false)
	if !r.IsImportable() || utf8.RuneCountInString(*r.Title) != 200 {
		t.Fatalf("%+v", r)
	}
}

func TestRejectsZeroAmounts(t *testing.T) {
	r := normRow(map[ColumnRole]Cell{RoleDate: jan3(t), RoleTitle: textCell("x"), RoleAmount: numberCell(0)}, DefaultSettings(), false)
	if issueOf(r) != IssueInvalidAmount {
		t.Fatalf("%+v", r)
	}
}

// ICU quirks of the non-lenient DateFormatter the normalizer relied on,
// probed on macOS.
func TestICUDateQuirks(t *testing.T) {
	if got := mustDate(t, "3 April 2026", DayFirst); got != "2026-04-03" {
		t.Errorf("got %s", got)
	}
	if got := mustDate(t, "April 3, 2026", MonthFirst); got != "2026-04-03" {
		t.Errorf("got %s", got)
	}
	for _, raw := range []string{"31/02/2026", "20260103"} {
		if _, ok := ParseDate(raw, DayFirst); ok {
			t.Errorf("%q should not parse", raw)
		}
	}
}
