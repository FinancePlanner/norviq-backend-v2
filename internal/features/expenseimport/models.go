package expenseimport

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ColumnRole is SpreadsheetColumnRole: what a column is understood to hold.
type ColumnRole string

// Column roles.
const (
	RoleTitle      ColumnRole = "title"
	RoleAmount     ColumnRole = "amount"
	RoleDate       ColumnRole = "date"
	RoleCategory   ColumnRole = "category"
	RolePillar     ColumnRole = "pillar"
	RoleNotes      ColumnRole = "notes"
	RoleCurrency   ColumnRole = "currency"
	RoleExternalID ColumnRole = "externalId"
	RoleIgnore     ColumnRole = "ignore"
)

// parseRole is SpreadsheetColumnRole(rawValue:).
func parseRole(raw string) (ColumnRole, bool) {
	switch r := ColumnRole(raw); r {
	case RoleTitle, RoleAmount, RoleDate, RoleCategory, RolePillar, RoleNotes, RoleCurrency, RoleExternalID, RoleIgnore:
		return r, true
	}
	return "", false
}

// CellKind tags a Cell.
type CellKind int

// Cell kinds.
const (
	CellEmpty CellKind = iota
	CellText
	CellNumber
	CellDate
	CellBool
)

// Cell is SpreadsheetCell: one resolved cell.
type Cell struct {
	Kind   CellKind
	Text   string
	Number float64
	Date   time.Time
	Bool   bool
}

// Cell constructors.
func textCell(s string) Cell     { return Cell{Kind: CellText, Text: s} }
func numberCell(v float64) Cell  { return Cell{Kind: CellNumber, Number: v} }
func dateCell(t time.Time) Cell  { return Cell{Kind: CellDate, Date: t} }
func boolCell(b bool) Cell       { return Cell{Kind: CellBool, Bool: b} }
func (c Cell) Equal(o Cell) bool { return c == o }

// IsEmpty is SpreadsheetCell.isEmpty.
func (c Cell) IsEmpty() bool {
	switch c.Kind {
	case CellEmpty:
		return true
	case CellText:
		return strings.TrimSpace(c.Text) == ""
	}
	return false
}

// DisplayText is displayText(dateFormatter:) with the yyyy-MM-dd UTC
// formatter, the only one the importer used.
func (c Cell) DisplayText() string {
	switch c.Kind {
	case CellText:
		return c.Text
	case CellNumber:
		return formatNumber(c.Number)
	case CellDate:
		return isoString(c.Date)
	case CellBool:
		if c.Bool {
			return "TRUE"
		}
		return "FALSE"
	}
	return ""
}

func (c Cell) String() string {
	switch c.Kind {
	case CellText:
		return fmt.Sprintf("text(%q)", c.Text)
	case CellNumber:
		return fmt.Sprintf("number(%v)", c.Number)
	case CellDate:
		return "date(" + isoString(c.Date) + ")"
	case CellBool:
		return fmt.Sprintf("boolean(%v)", c.Bool)
	}
	return "empty"
}

// formatNumber trims the float noise xlsx stores: whole numbers print as
// integers, everything else as C's %g.
func formatNumber(v float64) string {
	if v == math.Round(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return fmt.Sprintf("%.6g", v)
}

// isoString is the yyyy-MM-dd UTC rendering of a date.
func isoString(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Sheet is SpreadsheetSheet: a dense grid, rows[r][c] 0-based.
type Sheet struct {
	Name                     string
	Index                    int
	Rows                     [][]Cell
	RowsWithUncachedFormulas []int
}

// RowCount is rows.count.
func (s Sheet) RowCount() int { return len(s.Rows) }

// Cell returns the cell or empty out of range.
func (s Sheet) Cell(row, column int) Cell {
	if row < 0 || row >= len(s.Rows) || column < 0 || column >= len(s.Rows[row]) {
		return Cell{}
	}
	return s.Rows[row][column]
}

// Workbook is SpreadsheetWorkbook.
type Workbook struct {
	Sheets       []Sheet
	UsesDate1904 bool
	Warnings     []string
}

// Limits are SpreadsheetLimits.
type Limits struct {
	MaxBytes         int
	MaxInflatedBytes int
	MaxSheets        int
	MaxRowsPerSheet  int
	MaxColumns       int
}

// DefaultLimits is SpreadsheetLimits.default.
var DefaultLimits = Limits{
	MaxBytes:         8 * 1024 * 1024,
	MaxInflatedBytes: 64 * 1024 * 1024,
	MaxSheets:        12,
	MaxRowsPerSheet:  2000,
	MaxColumns:       64,
}

// ReadErrorKind is SpreadsheetReadError's case.
type ReadErrorKind int

// Read error kinds.
const (
	ErrLegacyBinaryFormat ReadErrorKind = iota + 1
	ErrPasswordProtected
	ErrNotASpreadsheet
	ErrTooLarge
	ErrTooManyRows
	ErrNoReadableSheets
)

// ReadError is SpreadsheetReadError.
type ReadError struct {
	Kind  ReadErrorKind
	Bytes int
	Limit int
	Sheet string
	Rows  int
}

func (e *ReadError) Error() string { return e.Reason() }

// Reason is the user-facing message.
func (e *ReadError) Reason() string {
	switch e.Kind {
	case ErrLegacyBinaryFormat:
		return "That's an older .xls file. Open it in Excel and save it as .xlsx, then try again."
	case ErrPasswordProtected:
		return "That file is password-protected. Remove the password and try again."
	case ErrNotASpreadsheet:
		return "We couldn't read that file. Make sure it's a valid .xlsx spreadsheet."
	case ErrTooLarge:
		return fmt.Sprintf("Spreadsheet must be %d MB or smaller.", e.Limit/(1024*1024))
	case ErrTooManyRows:
		return "Sheet \"" + e.Sheet + "\" has more than " + strconv.Itoa(e.Limit) + " rows. Split it and try again."
	case ErrNoReadableSheets:
		return "We couldn't find any readable sheets in that file."
	}
	return ""
}
