package expenseimport

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// StandardPillars are BudgetPillar.allCases.
var StandardPillars = []string{"fundamentals", "futureYou", "fun"}

var (
	nonAlnumRe  = regexp.MustCompile(`[^A-Za-z0-9]`)
	nonAlnumsRe = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// CanonicalPillar is BudgetPillar(rawValue:): false only when the canonical
// key is empty. Any other string mints a pillar.
func CanonicalPillar(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false
	}
	var normalized string
	if !nonAlnumRe.MatchString(trimmed) {
		r, size := utf8.DecodeRuneInString(trimmed)
		normalized = strings.ToLower(string(r)) + trimmed[size:]
	} else {
		words := strings.Fields(nonAlnumsRe.ReplaceAllString(trimmed, " "))
		if len(words) == 0 {
			return "", false
		}
		var b strings.Builder
		b.WriteString(strings.ToLower(words[0]))
		for _, w := range words[1:] {
			b.WriteString(strings.ToUpper(w[:1]) + strings.ToLower(w[1:]))
		}
		normalized = b.String()
	}
	switch strings.ToLower(normalized) {
	case "fundamentals":
		return "fundamentals", true
	case "futureyou":
		return "futureYou", true
	case "fun":
		return "fun", true
	}
	if normalized == "" {
		return "", false
	}
	return normalized, true
}
