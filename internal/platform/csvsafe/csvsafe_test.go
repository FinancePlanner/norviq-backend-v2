package csvsafe

import "testing"

func TestExportEscapesFormulaCell(t *testing.T) {
	cases := map[string]string{
		`=HYPERLINK("x")`: `'=HYPERLINK("x")`,
		"+1":              "'+1",
		"-1":              "'-1",
		"@SUM(A1)":        "'@SUM(A1)",
		"\tx":             "'\tx",
		"\rx":             "'\rx",
		"AAPL":            "AAPL",
		"":                "",
		"a=b":             "a=b",
	}
	for in, want := range cases {
		if got := EscapeFormulaCell(in); got != want {
			t.Errorf("EscapeFormulaCell(%q) = %q, want %q", in, got, want)
		}
	}
}
