package expenseimport

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
)

// Ports SpreadsheetReaderTests.swift. Fixtures come from two writers that
// disagree in ways that matter: XlsxWriter (shared strings, cached formula
// results) and openpyxl (inline strings, no cached results).

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func read(t *testing.T, name string, limits Limits) *Workbook {
	t.Helper()
	wb, err := XLSXReader{}.Read(fixture(t, name), limits)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return wb
}

func readErr(t *testing.T, data []byte, limits Limits) *ReadError {
	t.Helper()
	_, err := XLSXReader{}.Read(data, limits)
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("want ReadError, got %v", err)
	}
	return re
}

func expectCell(t *testing.T, s Sheet, row, col int, want Cell) {
	t.Helper()
	if got := s.Cell(row, col); !got.Equal(want) {
		t.Errorf("cell(%d,%d) = %v, want %v", row, col, got, want)
	}
}

func TestReadsMessyWorkbook(t *testing.T) {
	wb := read(t, "messy.xlsx", DefaultLimits)
	var names []string
	for _, s := range wb.Sheets {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"Gastos 2026", "Resumo", "2025 (antigo)"}) {
		t.Fatalf("sheets %v", names)
	}
	s := wb.Sheets[0]
	// Header sits on row 5, data begins at column G.
	expectCell(t, s, 4, 6, textCell("Data"))
	expectCell(t, s, 4, 7, textCell("Descrição"))
	expectCell(t, s, 5, 0, Cell{})
	expectCell(t, s, 5, 7, textCell("Continente"))
	expectCell(t, s, 5, 9, numberCell(84.2))
}

func TestDistinguishesDatesFromNumbers(t *testing.T) {
	s := read(t, "messy.xlsx", DefaultLimits).Sheets[0]
	c := s.Cell(5, 6)
	if c.Kind != CellDate || isoString(c.Date) != "2026-01-03" {
		t.Fatalf("G6 = %v, want date 2026-01-03", c)
	}
	expectCell(t, s, 5, 9, numberCell(84.2))
}

func TestHandlesBothDateSystems(t *testing.T) {
	e1900 := read(t, "excel-like.xlsx", DefaultLimits)
	e1904 := read(t, "excel-like-1904.xlsx", DefaultLimits)
	if e1900.UsesDate1904 || !e1904.UsesDate1904 {
		t.Fatalf("usesDate1904 = %v / %v", e1900.UsesDate1904, e1904.UsesDate1904)
	}
	for _, wb := range []*Workbook{e1900, e1904} {
		c := wb.Sheets[0].Cell(4, 3)
		if c.Kind != CellDate || isoString(c.Date) != "2026-01-03" {
			t.Fatalf("D5 = %v", c)
		}
	}
}

func TestReadsSharedStringsAndCachedFormulas(t *testing.T) {
	s := read(t, "excel-like.xlsx", DefaultLimits).Sheets[0]
	expectCell(t, s, 4, 4, textCell("Continente"))
	// =SUM(G5:G9), cached as 954.59. We read the cache, never evaluate.
	expectCell(t, s, 9, 6, numberCell(954.59))
	if len(s.RowsWithUncachedFormulas) != 0 {
		t.Fatalf("uncached %v", s.RowsWithUncachedFormulas)
	}
}

func TestFlagsUncachedFormulas(t *testing.T) {
	wb := read(t, "no-cached-formula.xlsx", DefaultLimits)
	s := wb.Sheets[0]
	expectCell(t, s, 2, 2, Cell{})
	if !slices.Equal(s.RowsWithUncachedFormulas, []int{3}) {
		t.Fatalf("uncached %v", s.RowsWithUncachedFormulas)
	}
	if !slices.ContainsFunc(wb.Warnings, func(w string) bool { return strings.Contains(w, "no saved result") }) {
		t.Fatalf("warnings %v", wb.Warnings)
	}
}

func TestRoundsFloatNoise(t *testing.T) {
	// Stored as 9.300000000000001.
	expectCell(t, read(t, "messy.xlsx", DefaultLimits).Sheets[2], 1, 2, numberCell(9.3))
}

func TestRejectsLegacyBinary(t *testing.T) {
	ole2 := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...)
	if re := readErr(t, ole2, DefaultLimits); re.Kind != ErrLegacyBinaryFormat {
		t.Fatalf("kind %v", re.Kind)
	}
}

func TestRejectsGarbage(t *testing.T) {
	if re := readErr(t, []byte("this is not a spreadsheet, it is prose"), DefaultLimits); re.Kind != ErrNotASpreadsheet {
		t.Fatalf("kind %v", re.Kind)
	}
}

func TestRejectsOversizeUploads(t *testing.T) {
	limits := DefaultLimits
	limits.MaxBytes = 128
	if _, err := (XLSXReader{}).Read(fixture(t, "messy.xlsx"), limits); err == nil {
		t.Fatal("want error")
	}
}

func TestRejectsTooManyRows(t *testing.T) {
	limits := DefaultLimits
	limits.MaxRowsPerSheet = 3
	re := readErr(t, fixture(t, "messy.xlsx"), limits)
	if re.Kind != ErrTooManyRows || re.Sheet != "Gastos 2026" || re.Rows != 12 || re.Limit != 3 {
		t.Fatalf("got %+v", re)
	}
}

func TestColumnLetterRoundTrip(t *testing.T) {
	for letters, index := range map[string]int{"A": 1, "G": 7, "Z": 26, "AA": 27, "AZ": 52, "BA": 53} {
		if got := ColumnIndex(letters); got != index {
			t.Errorf("ColumnIndex(%s) = %d", letters, got)
		}
		if got := ColumnLetters(index); got != letters {
			t.Errorf("ColumnLetters(%d) = %s", index, got)
		}
	}
}

// A pretty-printed styles.xml (newlines inside cellXfs and after tag names)
// keeps its date styles. Vapor's `.` did not cross newlines, so every date
// cell read as a number.
func TestPrettyPrintedStylesKeepDates(t *testing.T) {
	src := fixture(t, "messy.xlsx")
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == "xl/styles.xml" {
			pretty := strings.ReplaceAll(string(data), "><", ">\n  <")
			pretty = strings.ReplaceAll(pretty, "<xf ", "<xf\n    ")
			data = []byte(pretty)
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	wb, err := XLSXReader{}.Read(buf.Bytes(), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if c := wb.Sheets[0].Cell(5, 6); c.Kind != CellDate || isoString(c.Date) != "2026-01-03" {
		t.Fatalf("G6 = %v, want date 2026-01-03", c)
	}
}
