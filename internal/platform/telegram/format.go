package telegram

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxMessageCharacters is Telegram's hard limit on one message, in UTF-16
// code units.
const MaxMessageCharacters = 4096

// TelegramFormat's patterns. NSRegularExpression's lookarounds have no RE2
// equivalent, so the two italic patterns are matched by emphasis() instead.
var (
	codeBlockRe  = regexp.MustCompile("(?s)```(?:[a-zA-Z0-9_+-]*)\\n?(.*?)```")
	inlineCodeRe = regexp.MustCompile("`([^`\\n]+)`")
	linkRe       = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	headingRe    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	boldRe       = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	bulletRe     = regexp.MustCompile(`(?m)^\s*[-*]\s+`)
)

// rewrite applies transform to each match's capture groups (an unmatched
// group is "").
func rewrite(re *regexp.Regexp, text string, transform func(groups []string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		groups := make([]string, 0, len(m)/2-1)
		for i := 2; i+1 < len(m); i += 2 {
			if m[i] < 0 {
				groups = append(groups, "")
				continue
			}
			groups = append(groups, text[m[i]:m[i+1]])
		}
		b.WriteString(text[last:m[0]])
		b.WriteString(transform(groups))
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// isICUWord is ICU's \w: letters, marks, decimal numbers, connector
// punctuation (and the joiners).
func isICUWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.Is(unicode.Nd, r) ||
		unicode.Is(unicode.Pc, r) || r == '‌' || r == '‍'
}

// emphasis is `(?<![\w M])M([^M\n]+)M(?![\w M])` for one marker M (`*` or
// `_`): the closing marker can only be the first M after the opening one,
// since the content excludes M.
func emphasis(text string, marker rune, transform func(inner string) string) string {
	blocked := func(r rune) bool { return r == marker || isICUWord(r) }
	var b strings.Builder
	last, i := 0, 0
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r != marker {
			i += size
			continue
		}
		if prev, _ := utf8.DecodeLastRuneInString(text[:i]); i > 0 && blocked(prev) {
			i += size
			continue
		}
		start := i + size
		end := strings.IndexRune(text[start:], marker)
		if end <= 0 {
			i += size
			continue
		}
		inner := text[start : start+end]
		closeAt := start + end + utf8.RuneLen(marker)
		if strings.ContainsRune(inner, '\n') {
			i += size
			continue
		}
		if next, _ := utf8.DecodeRuneInString(text[closeAt:]); closeAt < len(text) && blocked(next) {
			i += size
			continue
		}
		b.WriteString(text[last:i])
		b.WriteString(transform(inner))
		last, i = closeAt, closeAt
	}
	b.WriteString(text[last:])
	return b.String()
}

func placeholder(i int) string { return "\x00CODE" + strconv.Itoa(i) + "\x00" }

// HTML is TelegramFormat.html: markdown to Telegram's HTML subset.
func HTML(markdown string) string {
	var blocks []string
	text := rewrite(codeBlockRe, markdown, func(g []string) string {
		blocks = append(blocks, "<pre>"+Escape(g[0])+"</pre>")
		return placeholder(len(blocks) - 1)
	})
	text = rewrite(inlineCodeRe, text, func(g []string) string {
		blocks = append(blocks, "<code>"+Escape(g[0])+"</code>")
		return placeholder(len(blocks) - 1)
	})
	text = Escape(text)
	text = rewrite(linkRe, text, func(g []string) string { return `<a href="` + g[1] + `">` + g[0] + "</a>" })
	text = rewrite(headingRe, text, func(g []string) string { return "<b>" + g[0] + "</b>" })
	text = rewrite(boldRe, text, func(g []string) string { return "<b>" + g[0] + "</b>" })
	text = emphasis(text, '*', func(s string) string { return "<i>" + s + "</i>" })
	text = emphasis(text, '_', func(s string) string { return "<i>" + s + "</i>" })
	text = rewrite(bulletRe, text, func([]string) string { return "• " })
	for i, block := range blocks {
		text = strings.ReplaceAll(text, placeholder(i), block)
	}
	return text
}

// Plain is TelegramFormat.plain: markers removed, link targets kept.
func Plain(markdown string) string {
	text := rewrite(codeBlockRe, markdown, func(g []string) string { return g[0] })
	text = rewrite(inlineCodeRe, text, func(g []string) string { return g[0] })
	text = rewrite(linkRe, text, func(g []string) string { return g[0] + " (" + g[1] + ")" })
	text = rewrite(headingRe, text, func(g []string) string { return g[0] })
	text = rewrite(boldRe, text, func(g []string) string { return g[0] })
	text = emphasis(text, '*', func(s string) string { return s })
	text = emphasis(text, '_', func(s string) string { return s })
	return rewrite(bulletRe, text, func([]string) string { return "• " })
}

// Escape escapes only & < >.
func Escape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

// width is the length in UTF-16 code units, the unit Telegram counts.
func width(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func trimSwift(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// Split is TelegramFormat.split: on paragraph, line, then word boundaries
// in the second half of the window, else mid-word.
func Split(text string, limit int) []string {
	if text == "" {
		return nil
	}
	if width(text) <= limit {
		return []string{text}
	}
	var parts []string
	remaining := text
	for width(remaining) > limit {
		window := prefix(remaining, limit)
		cut := breakPoint(window, len(characters(window))/2)
		parts = append(parts, trimSwift(remaining[:cut]))
		remaining = strings.TrimLeft(remaining[cut:], "\n ")
	}
	parts = append(parts, trimSwift(remaining))
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// HTMLChunks is TelegramFormat.htmlChunks: parts whose rendered HTML fits.
func HTMLChunks(markdown string, limit int) []string {
	if markdown == "" {
		return nil
	}
	var out []string
	for _, part := range Split(markdown, limit) {
		out = append(out, fit(part, limit)...)
	}
	return out
}

func fit(part string, limit int) []string {
	if width(HTML(part)) <= limit {
		return []string{part}
	}
	budget := limit
	for budget > 16 {
		budget /= 2
		candidates := Split(part, budget)
		ok := true
		for _, c := range candidates {
			if width(HTML(c)) > limit {
				ok = false
				break
			}
		}
		if ok {
			return candidates
		}
	}
	return Split(part, 16)
}

// prefix is the longest run of whole characters within utf16Limit.
func prefix(text string, utf16Limit int) string {
	used, end := 0, 0
	for _, ch := range characters(text) {
		next := used + width(ch)
		if next > utf16Limit {
			break
		}
		used = next
		end += len(ch)
	}
	return text[:end]
}

// breakPoint returns a byte offset into window: the last separator at least
// minimum characters in, else the window's end.
func breakPoint(window string, minimum int) int {
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(window, sep); i >= 0 && len(characters(window[:i])) >= minimum {
			return i
		}
	}
	return len(window)
}
