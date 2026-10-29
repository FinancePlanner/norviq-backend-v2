package expenseimport

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Ports SpreadsheetStructureDetectorTests.swift.

func detectFixture(t *testing.T, file string, index int) DetectedSheet {
	t.Helper()
	return Detect(read(t, file, DefaultLimits).Sheets[index])
}

func fieldOf(d DetectedSheet, letter string) ColumnRole {
	for _, c := range d.Columns {
		if c.Letter == letter {
			return c.Field
		}
	}
	return ""
}

func sheetOf(rows ...[]Cell) Sheet { return Sheet{Name: "Sheet1", Rows: rows} }

func TestFindsOffsetHeaderRow(t *testing.T) {
	d := detectFixture(t, "messy.xlsx", 0)
	if d.HeaderRow == nil || *d.HeaderRow != 5 || d.DataStartRow != 6 || d.DataEndRow != 14 {
		t.Fatalf("header %v start %d end %d", d.HeaderRow, d.DataStartRow, d.DataEndRow)
	}
}

func TestFindsOffsetColumns(t *testing.T) {
	d := detectFixture(t, "messy.xlsx", 0)
	if d.FirstColumn != 2 || d.LastColumn != 12 {
		t.Fatalf("columns %d..%d", d.FirstColumn, d.LastColumn)
	}
	if !slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(n, "row 5") }) {
		t.Fatalf("notes %v", d.Notes)
	}
}

func TestHandlesCleanSheet(t *testing.T) {
	d := detectFixture(t, "messy.xlsx", 2)
	if d.HeaderRow == nil || *d.HeaderRow != 1 || d.DataStartRow != 2 || d.FirstColumn != 1 {
		t.Fatalf("%+v", d)
	}
}

func TestExcludesLabelledTotalRow(t *testing.T) {
	d := detectFixture(t, "messy.xlsx", 0)
	if !slices.Contains(d.ExcludedRows, 14) {
		t.Fatalf("excluded %v", d.ExcludedRows)
	}
	if !slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(strings.ToLower(n), "totals") }) {
		t.Fatalf("notes %v", d.Notes)
	}
}

func TestExcludesArithmeticTotalRow(t *testing.T) {
	d := Detect(sheetOf(
		[]Cell{textCell("date"), textCell("item"), textCell("amount")},
		[]Cell{textCell("2026-01-01"), textCell("a"), numberCell(10)},
		[]Cell{textCell("2026-01-02"), textCell("b"), numberCell(20)},
		[]Cell{textCell("2026-01-03"), textCell("c"), numberCell(30)},
		[]Cell{{}, {}, numberCell(60)},
	))
	if !slices.Contains(d.ExcludedRows, 5) {
		t.Fatalf("excluded %v", d.ExcludedRows)
	}
}

func TestDoesNotOverFlagAggregates(t *testing.T) {
	d := Detect(sheetOf(
		[]Cell{textCell("date"), textCell("item"), textCell("amount")},
		[]Cell{textCell("2026-01-01"), textCell("a"), numberCell(10)},
		[]Cell{textCell("2026-01-02"), textCell("b"), numberCell(20)},
		[]Cell{textCell("2026-01-03"), textCell("c"), numberCell(35)},
	))
	if len(d.ExcludedRows) != 0 {
		t.Fatalf("excluded %v", d.ExcludedRows)
	}
}

func TestRecognisesTotalLabels(t *testing.T) {
	for _, l := range []string{"TOTAL", "Total:", " subtotal ", "Soma", "Saldo", "Balance", "Gesamt"} {
		if !IsAggregateLabel(l) {
			t.Errorf("%q should be a total", l)
		}
	}
	for _, l := range []string{"Totally Awesome Cafe", "Continente", "Rent", ""} {
		if IsAggregateLabel(l) {
			t.Errorf("%q should not be a total", l)
		}
	}
}

func TestMapsColumns(t *testing.T) {
	d := detectFixture(t, "messy.xlsx", 0)
	for letter, want := range map[string]ColumnRole{"G": RoleDate, "H": RoleTitle, "I": RoleCategory, "J": RoleAmount, "K": RoleNotes} {
		if got := fieldOf(d, letter); got != want {
			t.Errorf("%s = %q, want %q", letter, got, want)
		}
	}
}

func TestResolvesCompetingAmountColumns(t *testing.T) {
	d := detectFixture(t, "messy.xlsx", 0)
	var amounts []string
	for _, c := range d.Columns {
		if c.Field == RoleAmount {
			amounts = append(amounts, c.Letter)
		}
	}
	if !slices.Equal(amounts, []string{"J"}) {
		t.Fatalf("amount columns %v", amounts)
	}
}

func TestMapsEnglishHeaders(t *testing.T) {
	d := detectFixture(t, "messy.xlsx", 2)
	for letter, want := range map[string]ColumnRole{"A": RoleDate, "B": RoleTitle, "C": RoleAmount, "D": RolePillar} {
		if got := fieldOf(d, letter); got != want {
			t.Errorf("%s = %q, want %q", letter, got, want)
		}
	}
}

func TestHeaderContradictedByContentLosesConfidence(t *testing.T) {
	d := Detect(sheetOf(
		[]Cell{textCell("Date"), textCell("Amount")},
		[]Cell{textCell("not a date"), numberCell(10)},
		[]Cell{textCell("also not"), numberCell(20)},
		[]Cell{textCell("still not"), numberCell(30)},
	))
	for _, c := range d.Columns {
		if c.Letter == "A" && c.Confidence > 0.4 {
			t.Fatalf("confidence %v", c.Confidence)
		}
	}
}

func TestScoresExpenseSheetsHigher(t *testing.T) {
	wb := read(t, "messy.xlsx", DefaultLimits)
	if e, s := Detect(wb.Sheets[0]).Score, Detect(wb.Sheets[1]).Score; e <= s {
		t.Fatalf("expenses %v <= summary %v", e, s)
	}
}

func TestSniffsAmounts(t *testing.T) {
	if a, ok := LooksLikeAmount("84,20 €"); !ok || !a.HadCurrencySymbol {
		t.Errorf("84,20 € -> %+v %v", a, ok)
	}
	for raw, want := range map[string]float64{"1.234,56": 1234.56, "1,234.56": 1234.56, "(123.45)": -123.45} {
		if a, ok := LooksLikeAmount(raw); !ok || a.Value != want {
			t.Errorf("%s -> %+v %v", raw, a, ok)
		}
	}
	for _, raw := range []string{"", "Continente"} {
		if _, ok := LooksLikeAmount(raw); ok {
			t.Errorf("%q should not be an amount", raw)
		}
	}
}

func TestSniffsDates(t *testing.T) {
	for _, v := range []string{"2026-01-03", "03/04/2026", "3.4.2026", "12-11-2025", "3 April 2026"} {
		if !LooksLikeDate(v) {
			t.Errorf("%q should look like a date", v)
		}
	}
	for _, v := range []string{"Continente", "84.20", "", "2026"} {
		if LooksLikeDate(v) {
			t.Errorf("%q should not look like a date", v)
		}
	}
}

func TestSamplesSpanTheColumn(t *testing.T) {
	var values []string
	for i := 1; i <= 100; i++ {
		values = append(values, strconv.Itoa(i))
	}
	s := Sampled(values, 20)
	if len(s) > 20 || s[0] != "1" {
		t.Fatalf("sampled %v", s)
	}
	if !slices.ContainsFunc(s, func(v string) bool { n, _ := strconv.Atoi(v); return n > 50 }) {
		t.Fatalf("sampled %v", s)
	}
}
