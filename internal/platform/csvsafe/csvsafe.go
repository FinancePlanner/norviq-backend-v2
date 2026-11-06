// Package csvsafe keeps spreadsheet apps from running a cell as a formula.
package csvsafe

// EscapeFormulaCell prefixes a single quote to a cell that starts with =, +,
// -, @, a tab or a carriage return, so Excel and Sheets read it as text. The
// stored value is untouched; call this only when writing an export.
func EscapeFormulaCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
