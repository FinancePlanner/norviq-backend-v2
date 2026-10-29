package assistant

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// memoAsk is PositionMemoAsk: a due-diligence ask pulled out of a chat
// message.
type memoAsk struct {
	askedSymbol     string
	companionSymbol *string
	cost            *float64
	costCurrency    *string
	// statedPercent is signed: "down 34%" is -34.
	statedPercent *float64
}

var (
	memoDDCommand   = regexp.MustCompile(`(?i)(?:^|\s)/dd(?:\s|$)`)
	memoMakeDD      = regexp.MustCompile(`(?i)\bmake\s+(?:a\s+)?dd\b`)
	memoParenthesis = regexp.MustCompile(`\(([A-Za-z][A-Za-z0-9]{0,5}(?:\.[A-Za-z]{1,3})?)\)`)
	memoDollar      = regexp.MustCompile(`\$([A-Za-z][A-Za-z0-9]{0,5}(?:\.[A-Za-z]{1,4})?)`)
	memoContextual  = regexp.MustCompile(`(?i)\b(?:about|on|for|sell|hold)\s+(?:my\s+)?\$?([A-Za-z][A-Za-z0-9]{0,5}(?:\.[A-Za-z]{1,3})?)\b`)
	memoBare        = regexp.MustCompile(`\b([A-Z][A-Z0-9]{0,5}(?:\.[A-Z]{1,3})?)\b`)
	memoCostSymbol  = regexp.MustCompile(`(?i)([€$£])\s*([0-9]+(?:[.,][0-9]+)?)`)
	memoCostWord    = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(euros?|eur|dollars?|usd|pounds?|gbp)\b`)
	memoDown        = regexp.MustCompile(`(?i)down\s+([0-9]+(?:[.,][0-9]+)?)\s*(?:%|percent)`)
	memoUp          = regexp.MustCompile(`(?i)up\s+([0-9]+(?:[.,][0-9]+)?)\s*(?:%|percent)`)
	memoSigned      = regexp.MustCompile(`([+-])\s*([0-9]+(?:[.,][0-9]+)?)\s*%`)
)

var memoStopwords = func() map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(`A I DD AI Q EUR USD GBP OK MY THE AND OR FOR TO OF
		IN ON IS IT BE AT BY AN AS IF SO UP DO WE US EU UK
		IMO FYI ETF CEO COO EPS PE YOY TTM SEC IPO GDP CPI NOT
		BUT ARE WAS HAS HAD HIS HER OUR YOU YOUR DOWN PERCENT EURO
		EUROS DOLLAR DOLLARS MAKE ABOUT HOLD SELL MEMO WRITE`) {
		out[w] = true
	}
	return out
}()

// foldText is String.folding(options: .caseInsensitive[, .diacriticInsensitive]).
func foldText(s string, diacritics bool) string {
	if diacritics {
		t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
		if out, _, err := transform.String(t, s); err == nil {
			s = out
		}
	}
	return strings.ToLower(s)
}

// parseMemoAsk is PositionMemoAsk.parse; nil for an ordinary turn.
func parseMemoAsk(raw string) *memoAsk {
	text := trimSwiftSpace(raw)
	if text == "" {
		return nil
	}
	asked, companion := extractMemoSymbols(text)
	if !isMemoAsk(text, asked != "") || asked == "" {
		return nil
	}
	ask := &memoAsk{askedSymbol: asked, companionSymbol: companion, statedPercent: extractStatedPercent(text)}
	if amount, currency, ok := extractMemoCost(text); ok {
		ask.cost, ask.costCurrency = &amount, &currency
	}
	return ask
}

func isMemoAsk(text string, hasTicker bool) bool {
	folded := foldText(text, true)
	if memoDDCommand.MatchString(folded) || strings.Contains(folded, "due diligence") || memoMakeDD.MatchString(folded) {
		return true
	}
	if strings.Contains(folded, "write a memo") || strings.Contains(folded, "write me a memo") || strings.Contains(folded, "position memo") {
		return true
	}
	stance := strings.Contains(folded, "should i sell") || strings.Contains(folded, "should i hold") || strings.Contains(folded, "average down")
	return stance && hasTicker
}

func memoMatches(re *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		sym := strings.ToUpper(trimSwiftSpace(m[1]))
		if isMemoTicker(sym) {
			out = append(out, sym)
		}
	}
	return out
}

func isMemoTicker(symbol string) bool {
	core := symbol
	if i := strings.IndexByte(symbol, '.'); i >= 0 {
		core = symbol[:i]
	}
	if n := graphemeLen(core); n < 1 || n > 6 || memoStopwords[core] || memoStopwords[symbol] {
		return false
	}
	for _, r := range core {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
			return false
		}
	}
	return true
}

func extractMemoSymbols(text string) (asked string, companion *string) {
	parenthetical := memoMatches(memoParenthesis, text)
	dollar := memoMatches(memoDollar, text)
	contextual := memoMatches(memoContextual, text)
	bare := memoMatches(memoBare, text)

	var ordered []string
	seen := map[string]bool{}
	for _, group := range [][]string{parenthetical, dollar, contextual, bare} {
		for _, s := range group {
			if !seen[s] {
				seen[s] = true
				ordered = append(ordered, s)
			}
		}
	}
	if len(ordered) == 0 {
		return "", nil
	}
	firstOther := func(list []string, not string) *string {
		for _, s := range list {
			if s != not {
				v := s
				return &v
			}
		}
		return nil
	}
	if len(parenthetical) > 0 {
		listing := parenthetical[0]
		rest := append(append(append([]string{}, dollar...), contextual...), bare...)
		if c := firstOther(rest, listing); c != nil {
			return listing, c
		}
		return listing, firstOther(ordered, listing)
	}
	if len(ordered) > 1 {
		v := ordered[1]
		return ordered[0], &v
	}
	return ordered[0], nil
}

func parseMemoNumber(raw string) (float64, bool) {
	return swiftDouble(strings.ReplaceAll(raw, ",", "."))
}

func memoCurrencyCode(raw string) (string, bool) {
	switch foldText(raw, false) {
	case "€", "euro", "euros", "eur":
		return "EUR", true
	case "$", "dollar", "dollars", "usd":
		return "USD", true
	case "£", "pound", "pounds", "gbp":
		return "GBP", true
	}
	return "", false
}

func firstMemoCost(re *regexp.Regexp, text string, currencyGroup, amountGroup int) (float64, string, bool) {
	m := re.FindStringSubmatch(text)
	if m == nil {
		return 0, "", false
	}
	amount, ok := parseMemoNumber(m[amountGroup])
	if !ok {
		return 0, "", false
	}
	currency, ok := memoCurrencyCode(m[currencyGroup])
	return amount, currency, ok
}

func extractMemoCost(text string) (float64, string, bool) {
	if a, c, ok := firstMemoCost(memoCostSymbol, text, 1, 2); ok {
		return a, c, true
	}
	return firstMemoCost(memoCostWord, text, 2, 1)
}

func extractStatedPercent(text string) *float64 {
	folded := foldText(text, false)
	if m := memoDown.FindStringSubmatch(folded); m != nil {
		if v, ok := parseMemoNumber(m[1]); ok {
			v = -v
			return &v
		}
	}
	if m := memoUp.FindStringSubmatch(folded); m != nil {
		if v, ok := parseMemoNumber(m[1]); ok {
			return &v
		}
	}
	m := memoSigned.FindStringSubmatch(folded)
	if m == nil {
		return nil
	}
	v, ok := parseMemoNumber(m[2])
	if !ok {
		return nil
	}
	if m[1] == "-" {
		v = -v
	}
	return &v
}
