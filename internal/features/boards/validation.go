package boards

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"norviq-api/internal/httpx"
)

// CommunityValidation.swift: input rules for boards, posts and comments.
const (
	maxBoardsPerDay    = 3
	maxTitleLength     = 200
	maxPostBodyLength  = 10000
	maxCommentLength   = 5000
	maxTags            = 5
	maxTagLength       = 24
	maxCommentDepth    = 8
	maxReportNoteChars = 1000
)

// reservedSlugs are paths the web app or an admin screen owns, or that read
// as official.
var reservedSlugs = map[string]bool{
	"admin": true, "api": true, "new": true, "create": true, "edit": true, "settings": true, "submit": true, "search": true,
	"norviq": true, "official": true, "support": true, "help": true, "mod": true, "mods": true, "moderator": true, "staff": true,
	"daily": true,
	// Static web routes under /boards/.
	"guidelines": true, "block": true, "report": true, "activity": true,
}

// trim is `trimmingCharacters(in: .whitespacesAndNewlines)`.
func trim(s string) string { return strings.TrimSpace(s) }

// collapse is `split(whereSeparator: \.isWhitespace).joined(separator: " ")`.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// slugChars is `($0.isASCII && ($0.isLetter || $0.isNumber)) || $0 == "-"`.
func slugChars(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func validateSlug(raw string) (string, error) {
	slug := strings.ToLower(trim(raw))
	n := charCount(slug)
	if n < 3 || n > 32 || !slugChars(slug) || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return "", httpx.BadRequest("Board address must be 3–32 lowercase letters, numbers or dashes.")
	}
	if reservedSlugs[slug] {
		return "", httpx.BadRequest("That board address is reserved.")
	}
	return slug, nil
}

func validateBoardName(raw string) (string, error) {
	name := collapse(raw)
	if n := charCount(name); n < 3 || n > 60 {
		return "", httpx.BadRequest("Board name must be 3–60 characters.")
	}
	return name, nil
}

func validateBoardDescription(raw string) (string, error) {
	d := trim(raw)
	if charCount(d) > 500 {
		return "", httpx.BadRequest("Keep the description under 500 characters.")
	}
	return d, nil
}

func validateTitle(raw string) (string, error) {
	title := collapse(raw)
	if n := charCount(title); n < 3 || n > maxTitleLength {
		return "", httpx.BadRequest(fmt.Sprintf("Title must be 3–%d characters.", maxTitleLength))
	}
	return title, nil
}

func validatePostBody(raw *string) (*string, error) {
	body := ""
	if raw != nil {
		body = trim(*raw)
	}
	if charCount(body) > maxPostBodyLength {
		return nil, httpx.BadRequest(fmt.Sprintf("Keep the post under %d characters.", maxPostBodyLength))
	}
	if body == "" {
		return nil, nil
	}
	return &body, nil
}

func validateCommentBody(raw string) (string, error) {
	body := trim(raw)
	if n := charCount(body); n == 0 || n > maxCommentLength {
		return "", httpx.BadRequest(fmt.Sprintf("Comment must be 1–%d characters.", maxCommentLength))
	}
	return body, nil
}

// validateTags lowercases, deduplicates and keeps order. A leading `#` is
// dropped.
func validateTags(raw []string) ([]string, error) {
	seen := map[string]bool{}
	tags := []string{}
	for _, value := range raw {
		tag := strings.ToLower(trim(value))
		tag = strings.TrimPrefix(tag, "#")
		if tag == "" {
			continue
		}
		if !slugChars(tag) || charCount(tag) > maxTagLength {
			return nil, httpx.BadRequest(fmt.Sprintf("Tags are up to %d letters, numbers or dashes.", maxTagLength))
		}
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	if len(tags) > maxTags {
		return nil, httpx.BadRequest(fmt.Sprintf("Use at most %d tags.", maxTags))
	}
	return tags, nil
}

// validateLink accepts only absolute http(s) URLs with a dotted host and no
// user info. The server never fetches it; the domain is for display.
func validateLink(raw *string) (link, domain string, err error) {
	bad := httpx.BadRequest("Link posts need a full http(s) URL.")
	trimmed := ""
	if raw != nil {
		trimmed = trim(*raw)
	}
	if charCount(trimmed) > 2000 || !rfc3986(trimmed) {
		return "", "", bad
	}
	u, perr := url.Parse(trimmed)
	if perr != nil {
		return "", "", bad
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if (scheme != "http" && scheme != "https") || u.Opaque != "" || host == "" || !strings.Contains(host, ".") || u.User != nil {
		return "", "", bad
	}
	return trimmed, strings.TrimPrefix(host, "www."), nil
}

// rfc3986 approximates URLComponents(string:), which refuses a string with
// characters outside RFC 3986 (spaces, quotes, angle brackets, non-ASCII) or
// a malformed percent escape.
func rfc3986(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-._~:/?#[]@!$&'()*+,;=", c) >= 0:
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// charCount approximates Swift's String.count (extended grapheme clusters):
// combining marks, variation selectors, emoji modifiers and ZWJ-joined
// sequences attach to the character before them, a regional-indicator pair
// is one flag, and CRLF is one character.
func charCount(s string) int {
	return len(graphemes(s))
}

// CharCount is charCount for other domains that port a Swift `String.count`
// check (terminal positions, research).
func CharCount(s string) int { return charCount(s) }

// prefixChars is Swift's String(s.prefix(n)).
func prefixChars(s string, n int) string {
	g := graphemes(s)
	if len(g) <= n {
		return s
	}
	return strings.Join(g[:n], "")
}

func graphemes(s string) []string {
	var out []string
	var cur []rune
	joinNext := false
	riOpen := false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		extends := len(cur) > 0 && (joinNext || isExtender(r) ||
			(r == '\n' && cur[len(cur)-1] == '\r' && len(cur) == 1) ||
			(isRegionalIndicator(r) && riOpen))
		if !extends {
			flush()
			riOpen = false
		}
		switch {
		case isRegionalIndicator(r) && !extends:
			riOpen = true
		case isRegionalIndicator(r):
			riOpen = false
		}
		joinNext = r == 0x200D
		cur = append(cur, r)
	}
	flush()
	return out
}

func isExtender(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) ||
		(r >= 0xFE00 && r <= 0xFE0F) || (r >= 0x1F3FB && r <= 0x1F3FF) || r == 0x200D ||
		(r >= 0xE0020 && r <= 0xE007F)
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }
