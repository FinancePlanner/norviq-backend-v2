package stocks

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Swift Foundation behaviors the stock code leans on.

var upper = cases.Upper(language.Und)

// swiftUpper is String.uppercased(): full Unicode case mapping ("ß" -> "SS").
func swiftUpper(s string) string { return upper.String(s) }

// trim is trimmingCharacters(in: .whitespacesAndNewlines).
func trim(s string) string { return portfolios.TrimSwift(s) }

// normalizeSymbol is trim + uppercased.
func normalizeSymbol(s string) string { return swiftUpper(trim(s)) }

var dateFormatterRE = regexp.MustCompile(`^\s*([0-9]+)-([0-9]+)-([0-9]+)$`)

// parseDateFormatter is a non-lenient "yyyy-MM-dd" en_US_POSIX UTC
// DateFormatter's date(from:): each field takes one or more digits (ICU
// accepts "2024-1-5" and "24-01-01"), but the date must exist, year 0 does
// not, and nothing may trail it.
func parseDateFormatter(s string) (time.Time, bool) {
	m := dateFormatterRE.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	y, err1 := strconv.Atoi(m[1])
	mo, err2 := strconv.Atoi(m[2])
	d, err3 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || err3 != nil || y < 1 || mo < 1 || mo > 12 || d < 1 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d || int(t.Month()) != mo {
		return time.Time{}, false
	}
	return t, true
}

var buyDateRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// parseBuyDate is DatabaseStocksRepository.parseISODateOnly: strictly
// four-two-two digits naming a real calendar day. Vapor's
// Calendar.date(from: DateComponents) rolled an out-of-range month or day
// over ("2025-02-30" became March 2nd); v2 rejects it.
func parseBuyDate(raw string) (time.Time, error) {
	trimmed := trim(raw)
	invalid := httpx.BadRequest("Invalid buyDate. Expected YYYY-MM-DD.")
	if !buyDateRE.MatchString(trimmed) {
		return time.Time{}, invalid
	}
	y, _ := strconv.Atoi(trimmed[0:4])
	mo, _ := strconv.Atoi(trimmed[5:7])
	d, _ := strconv.Atoi(trimmed[8:10])
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if y < 1 || t.Year() != y || int(t.Month()) != mo || t.Day() != d {
		return time.Time{}, invalid
	}
	return t, nil
}

// parseRequiredDate is StockService.parseISODateOnly / TransactionService
// .parseDateOnly: blank is "<field> is required.", otherwise the strict
// formatter.
func parseRequiredDate(raw, field string) (time.Time, error) {
	trimmed := trim(raw)
	if trimmed == "" {
		return time.Time{}, httpx.BadRequest(field + " is required.")
	}
	t, ok := parseDateFormatter(trimmed)
	if !ok {
		return time.Time{}, httpx.BadRequest("Invalid " + field + ". Expected YYYY-MM-DD.")
	}
	return t, nil
}

// parseOptionalDate is parseOptionalISODateOnly: nil or blank is nil.
func parseOptionalDate(raw *string, field string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := trim(*raw)
	if trimmed == "" {
		return nil, nil
	}
	t, ok := parseDateFormatter(trimmed)
	if !ok {
		return nil, httpx.BadRequest("Invalid " + field + ". Expected YYYY-MM-DD.")
	}
	return &t, nil
}

// emptyToNil trims and maps "" to nil.
func emptyToNil(raw *string) *string {
	if raw == nil {
		return nil
	}
	t := trim(*raw)
	if t == "" {
		return nil
	}
	return &t
}

// swiftDouble is "\(double)".
func swiftDouble(f float64) string { return marketdata.SwiftDoubleString(f) }

// swiftReflecting is String(reflecting: string): the quoted debug form.
func swiftReflecting(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0:
			b.WriteString(`\0`)
		default:
			if unicode.IsPrint(r) || r == ' ' {
				b.WriteRune(r)
			} else {
				fmt.Fprintf(&b, `\u{%x}`, r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
