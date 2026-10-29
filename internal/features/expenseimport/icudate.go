package expenseimport

import (
	"strings"
	"time"
	"unicode"
)

// Vapor parsed text dates with DateFormatter (en_US_POSIX, UTC,
// isLenient = false). ICU is far more forgiving than the patterns suggest,
// and that forgiveness decides which pattern wins, so it is emulated here:
//
//   - a literal run of separators ("-", "/", ".", ",", whitespace) in the
//     pattern matches any non-empty run of separators in the text, so
//     "dd/MM/yyyy" reads "3.4.2026" and "3 April 2026";
//   - numeric fields take every digit available ("yyyy" reads "26" as year
//     26), and "yy" applies the two-digit-year pivot only to exactly two
//     digits;
//   - month fields accept a number or an English month name, full or
//     abbreviated, in any case, whatever their width;
//   - the whole string must be consumed and the date must exist.
//
// Probed against Foundation on macOS (see the port report).

var monthNames = []string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"}

type icuToken struct {
	field byte // 'y', 'M', 'd', or 0 for a literal
	width int
	lit   string
}

func tokenizePattern(p string) []icuToken {
	var out []icuToken
	for i := 0; i < len(p); {
		ch := p[i]
		if ch == 'y' || ch == 'M' || ch == 'd' {
			j := i
			for j < len(p) && p[j] == ch {
				j++
			}
			out = append(out, icuToken{field: ch, width: j - i})
			i = j
			continue
		}
		j := i
		for j < len(p) && p[j] != 'y' && p[j] != 'M' && p[j] != 'd' {
			j++
		}
		out = append(out, icuToken{lit: p[i:j]})
		i = j
	}
	return out
}

func isSeparator(r rune) bool {
	return r == '-' || r == '/' || r == '.' || r == ',' || unicode.IsSpace(r)
}

// icuParse parses text with pattern, or reports false.
func icuParse(pattern, text string, now time.Time) (time.Time, bool) {
	tokens := tokenizePattern(pattern)
	rs := []rune(text)
	pos := 0
	year, month, day := 0, 0, 0
	haveYear, haveMonth, haveDay := false, false, false
	for _, tok := range tokens {
		if tok.field == 0 {
			allSep := true
			for _, r := range tok.lit {
				if !isSeparator(r) {
					allSep = false
				}
			}
			if allSep {
				start := pos
				for pos < len(rs) && isSeparator(rs[pos]) {
					pos++
				}
				if pos == start {
					return time.Time{}, false
				}
				continue
			}
			lit := []rune(tok.lit)
			if pos+len(lit) > len(rs) || string(rs[pos:pos+len(lit)]) != tok.lit {
				return time.Time{}, false
			}
			pos += len(lit)
			continue
		}
		start := pos
		for pos < len(rs) && rs[pos] >= '0' && rs[pos] <= '9' {
			pos++
		}
		digits := string(rs[start:pos])
		if digits == "" {
			if tok.field != 'M' {
				return time.Time{}, false
			}
			m, n := matchMonthName(rs[pos:])
			if m == 0 {
				return time.Time{}, false
			}
			pos += n
			month, haveMonth = m, true
			continue
		}
		v := 0
		for _, d := range digits {
			v = v*10 + int(d-'0')
			if v > 1_000_000 {
				return time.Time{}, false
			}
		}
		switch tok.field {
		case 'y':
			if tok.width == 2 && len(digits) == 2 {
				v = pivotYear(v, now)
			}
			year, haveYear = v, true
		case 'M':
			month, haveMonth = v, true
		case 'd':
			day, haveDay = v, true
		}
	}
	if pos != len(rs) || !haveYear || !haveMonth || !haveDay {
		return time.Time{}, false
	}
	if year < 1 || month < 1 || month > 12 || day < 1 || day > daysIn(year, month) {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

// matchMonthName matches the longest English month name or abbreviation at
// the start of rs, case-insensitively.
func matchMonthName(rs []rune) (month, consumed int) {
	lower := strings.ToLower(string(rs))
	for i, name := range monthNames {
		if strings.HasPrefix(lower, name) && len(name) > consumed {
			month, consumed = i+1, len(name)
		}
	}
	if month == 0 {
		for i, name := range monthNames {
			if strings.HasPrefix(lower, name[:3]) {
				return i + 1, 3
			}
		}
	}
	return month, consumed
}

// pivotYear is ICU's two-digit year: within 80 years before and 20 after now.
func pivotYear(v int, now time.Time) int {
	start := now.Year() - 80
	century := start / 100 * 100
	y := century + v
	if y < start {
		y += 100
	}
	return y
}

func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// ParseICUDate is icuParse for other features porting DateFormatter parses.
func ParseICUDate(pattern, text string, now time.Time) (time.Time, bool) {
	return icuParse(pattern, text, now)
}
