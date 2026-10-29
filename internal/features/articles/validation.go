package articles

import (
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"norviq-api/internal/httpx"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// ArticleValidation: input rules for articles. Pure, so they test without a
// database. Lengths are Swift Character counts (graphemeLen).
const (
	maxPerDay          = 3
	maxImagesPerDay    = 10
	titleMin, titleMax = 8, 140
	bodyMin, bodyMax   = 300, 20000
	maxBullets         = 3
	bulletMin          = 10
	bulletMax          = 240
	maxTickers         = 5
	disclosureMin      = 10
	disclosureMax      = 500
	maxSlugLength      = 80
)

// codeAlphabet has no i, l, o, 0 or 1: a code is read off an image card and
// typed back in.
const codeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

var (
	upper = cases.Upper(language.Und)
	lower = cases.Lower(language.Und)
)

// swiftUpper and swiftLower are String.uppercased()/lowercased(): full
// Unicode case mapping, locale-independent ("ß" uppercases to "SS").
func swiftUpper(s string) string { return upper.String(s) }
func swiftLower(s string) string { return lower.String(s) }

// collapse is `split(whereSeparator: \.isWhitespace).joined(separator: " ")`.
func collapse(raw string) string { return strings.Join(strings.Fields(raw), " ") }

// trimWS is trimmingCharacters(in: .whitespacesAndNewlines).
func trimWS(raw string) string { return strings.TrimFunc(raw, unicode.IsSpace) }

func validateTitle(raw string) (string, error) {
	title := collapse(raw)
	if n := graphemeLen(title); n < titleMin || n > titleMax {
		return "", httpx.BadRequest("Title must be 8–140 characters.")
	}
	return title, nil
}

func validateBody(raw string) (string, error) {
	body := trimWS(raw)
	if n := graphemeLen(body); n < bodyMin || n > bodyMax {
		return "", httpx.BadRequest("The article must be 300–20,000 characters.")
	}
	return body, nil
}

func validateBullets(raw []string) ([]string, error) {
	items := make([]string, 0, len(raw))
	for _, r := range raw {
		if c := collapse(r); c != "" {
			items = append(items, c)
		}
	}
	if len(items) < 1 || len(items) > maxBullets {
		return nil, httpx.BadRequest("Add one to three key points.")
	}
	for _, it := range items {
		if n := graphemeLen(it); n < bulletMin || n > bulletMax {
			return nil, httpx.BadRequest("Each key point must be 10–240 characters.")
		}
	}
	return items, nil
}

// validateTickers drops a leading $, uppercases, dedupes keeping order and
// caps at five.
func validateTickers(raw []string) ([]string, error) {
	seen := map[string]bool{}
	tickers := []string{}
	for _, item := range raw {
		ticker := swiftUpper(trimWS(item))
		if strings.HasPrefix(ticker, "$") {
			ticker = strings.Join(graphemes(ticker)[1:], "")
		}
		if ticker == "" {
			continue
		}
		if !isTicker(ticker) {
			return nil, httpx.BadRequest(`"` + graphemePrefix(ticker, 12) + `" isn't a ticker symbol.`)
		}
		if !seen[ticker] {
			seen[ticker] = true
			tickers = append(tickers, ticker)
		}
	}
	if len(tickers) < 1 || len(tickers) > maxTickers {
		return nil, httpx.BadRequest("Tag one to five tickers.")
	}
	return tickers, nil
}

func validateDisclosure(raw string) (string, error) {
	disclosure := collapse(raw)
	if n := graphemeLen(disclosure); n < disclosureMin || n > disclosureMax {
		return "", httpx.BadRequest(`Add a disclosure of 10–500 characters, e.g. "No position".`)
	}
	return disclosure, nil
}

// isTicker: 1–10 characters, an ASCII letter first, then ASCII uppercase
// letters, digits, "." or "-".
func isTicker(v string) bool {
	if n := graphemeLen(v); n < 1 || n > 10 {
		return false
	}
	first := v[0]
	if first >= 0x80 || !(first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z') {
		return false
	}
	for _, r := range v {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// slugFrom keeps lowercased ASCII alphanumerics and collapses every other run
// into one dash.
func slugFrom(title string) string {
	var b strings.Builder
	lastWasDash := true
	for _, r := range swiftLower(title) {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastWasDash = false
		} else if !lastWasDash {
			b.WriteByte('-')
			lastWasDash = true
		}
	}
	slug := b.String()
	if len(slug) > maxSlugLength {
		slug = slug[:maxSlugLength]
	}
	slug = strings.TrimRight(slug, "-")
	if slug == "" {
		return "article"
	}
	return slug
}

var markdownLink = regexp.MustCompile(`\]\([^)]*\)`)

// wordCount ignores markdown punctuation and link targets.
func wordCount(markdown string) int {
	stripped := markdownLink.ReplaceAllString(markdown, " ")
	words := strings.FieldsFunc(stripped, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("#*_>`[]()!~|-", r)
	})
	n := 0
	for _, w := range words {
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r) {
				n++
				break
			}
		}
	}
	return n
}

func makeCode() string {
	b := make([]byte, 8)
	for i := range b {
		b[i] = codeAlphabet[rand.IntN(len(codeAlphabet))]
	}
	return string(b)
}

// tooMany is a 429 CodedAbort.
func tooMany(code, reason string) error {
	return httpx.CodedAbort(http.StatusTooManyRequests, code, reason)
}
