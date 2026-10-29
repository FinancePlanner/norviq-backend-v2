package articles

import (
	"strings"
	"unicode"
)

// Swift's String.count and prefix(_:) count Characters (extended grapheme
// clusters), not runes. graphemes approximates UAX #29 closely enough for the
// length rules: combining marks, variation selectors, emoji modifiers, tags
// and ZWJ sequences extend the previous character; regional indicators pair
// into one flag; CR LF is one character. (Hangul jamo sequences are counted
// per jamo, a deviation no real title hits.)
func graphemes(s string) []string {
	var out []string
	var cur strings.Builder
	var prev rune = -1
	riRun := 0
	for _, r := range s {
		join := false
		switch {
		case prev == -1:
		case prev == '\r' && r == '\n':
			join = true
		case isExtend(r):
			join = prev != '\r' && prev != '\n'
		case prev == 0x200D && isPictographic(r):
			join = true
		case isRegionalIndicator(r) && isRegionalIndicator(prev) && riRun%2 == 1:
			join = true
		}
		if !join && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		if isRegionalIndicator(r) {
			if join {
				riRun++
			} else {
				riRun = 1
			}
		} else if !isExtend(r) {
			riRun = 0
		}
		cur.WriteRune(r)
		prev = r
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func graphemeLen(s string) int {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 || s[i] == '\r' {
			ascii = false
			break
		}
	}
	if ascii {
		return len(s)
	}
	return len(graphemes(s))
}

// graphemePrefix is String.prefix(n).
func graphemePrefix(s string, n int) string {
	g := graphemes(s)
	if len(g) <= n {
		return s
	}
	return strings.Join(g[:n], "")
}

func isExtend(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc) ||
		r == 0x200D || r == 0x200C ||
		(r >= 0x1F3FB && r <= 0x1F3FF) || // emoji modifiers
		(r >= 0xE0020 && r <= 0xE007F) // tags
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func isPictographic(r rune) bool {
	return unicode.Is(unicode.So, r) || (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF)
}
