package marketbrief

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"norviq-api/internal/platform/ai"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// promptMessages is MarketBriefPrompt.messages: the fixed system prompt and
// a facts turn. One call writes both languages, so en and pt-PT carry the
// same facts in the same order. No assistant prefill, no sampling knobs:
// the chain's first rung is Haiku 5.5, which rejects both.
func promptMessages(f Facts, webSearch bool) ([]ai.Message, error) {
	factsJSON, err := swiftSortedJSON(f)
	if err != nil {
		return nil, err
	}
	research := "Web search is unavailable. Use only FACTS; sourceUrl is null or a headline url from FACTS. " +
		"Keep it short: at most 5 items per language, each under 300 characters."
	if webSearch {
		research = "Use web search for today's market news and cite each source in sourceUrl."
	}
	return []ai.Message{
		ai.Text("system", systemPrompt),
		ai.Text("user", research+"\n\nSERVER-SELECTED FACTS:\n"+string(factsJSON)),
	}, nil
}

// draftItem is MarketBriefDraft.Item.
type draftItem struct {
	Kind      string
	Text      string
	Tickers   []string // nil when absent or null
	SourceURL *string
}

// draftSection is MarketBriefDraft.Section.
type draftSection struct {
	Greeting *string
	Items    []draftItem
}

// draft is MarketBriefDraft: the model's reply, before validation.
type draft struct {
	En   draftSection
	PtPT draftSection
}

func (d draft) section(language string) draftSection {
	if language == LanguageEn {
		return d.En
	}
	return d.PtPT
}

// parseDraft is MarketBriefDraft.parse: models wrap JSON in fences or a
// sentence even under json_object, so take the outermost object. Plain
// JSONDecoder semantics: exact keys, so "pt-PT" is read as written.
func parseDraft(content string) (draft, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end < 0 || start >= end {
		return draft{}, errUnparseableDraft
	}
	d, err := decodeDraft([]byte(content[start : end+1]))
	if err != nil {
		return draft{}, errUnparseableDraft
	}
	return d, nil
}

func decodeDraft(raw []byte) (draft, error) {
	fields, err := keyed(raw)
	if err != nil {
		return draft{}, err
	}
	var d draft
	for _, s := range []struct {
		key string
		dst *draftSection
	}{{"en", &d.En}, {"pt-PT", &d.PtPT}} {
		r, ok := fields[s.key]
		if !ok || isNull(r) {
			return d, errUndecodable
		}
		sec, err := decodeSection(r)
		if err != nil {
			return d, err
		}
		*s.dst = sec
	}
	return d, nil
}

func decodeSection(raw json.RawMessage) (draftSection, error) {
	fields, err := keyed(raw)
	if err != nil {
		return draftSection{}, err
	}
	var s draftSection
	if err := optionalValue(fields, "greeting", &s.Greeting); err != nil {
		return s, err
	}
	var items []json.RawMessage
	if err := requireValue(fields, "items", &items); err != nil {
		return s, err
	}
	for _, r := range items {
		f, err := keyed(r)
		if err != nil {
			return s, err
		}
		var it draftItem
		if err := requireValue(f, "kind", &it.Kind); err != nil {
			return s, err
		}
		if err := requireValue(f, "text", &it.Text); err != nil {
			return s, err
		}
		var tickers *[]string
		if err := optionalValue(f, "tickers", &tickers); err != nil {
			return s, err
		}
		if tickers != nil {
			it.Tickers = *tickers
		}
		if err := optionalValue(f, "sourceUrl", &it.SourceURL); err != nil {
			return s, err
		}
		s.Items = append(s.Items, it)
	}
	return s, nil
}

// Validator limits (MarketBriefValidator).
const (
	minItems          = 3
	maxGreetingLength = 120
)

type limits struct {
	maxItems, maxLength int
	kinds               map[string]bool
}

func limitsFor(slot string) limits {
	if slot == SlotMorning {
		return limits{10, 400, map[string]bool{KindHighlight: true, KindEarnings: true}}
	}
	return limits{8, 1000, map[string]bool{KindStory: true}}
}

// validatorOutput is MarketBriefValidator.Output.
type validatorOutput struct {
	Greeting *string
	Items    []Item
	Dropped  int
}

// swiftCount is String.count, approximated by runes (grapheme clusters in
// Swift; the two differ only for combining marks and emoji sequences).
func swiftCount(s string) int { return utf8.RuneCountInString(s) }

// validateSection is MarketBriefValidator.validate. allowedSources nil
// means any https source counts; otherwise only those URLs do.
func validateSection(sec draftSection, slot, language string, grounded []float64, allowedSources map[string]bool) (validatorOutput, error) {
	lim := limitsFor(slot)
	kept := []Item{}
	dropped := 0
	for _, it := range sec.Items {
		if len(kept) >= lim.maxItems {
			break
		}
		text := trimWhitespaceAndNewlines(it.Text)
		if !validKind(it.Kind) || !lim.kinds[it.Kind] || text == "" || swiftCount(text) > lim.maxLength {
			dropped++
			continue
		}
		// Without web search the model saw no page but the headlines, so
		// a link is only a source if it is one of theirs.
		source := httpsURL(it.SourceURL)
		if source != nil && allowedSources != nil && !allowedSources[*source] {
			source = nil
		}
		if source == nil && !isGrounded(text, grounded) {
			dropped++
			continue
		}
		raw := it.Tickers
		if raw == nil {
			raw = []string{}
		}
		kept = append(kept, Item{Kind: it.Kind, Text: text, Tickers: normalizeTickers(raw), SourceURL: source})
	}
	if len(kept) < minItems {
		return validatorOutput{}, &tooFewItemsError{Language: language, Kept: len(kept)}
	}
	var greeting *string
	if sec.Greeting != nil {
		g := trimWhitespaceAndNewlines(*sec.Greeting)
		if g != "" && swiftCount(g) <= maxGreetingLength {
			greeting = &g
		}
	}
	return validatorOutput{Greeting: greeting, Items: kept, Dropped: dropped}, nil
}

// isGrounded is MarketBriefValidator.isGrounded: every checked number
// matches some fact under one of its readings, allowing for the token's
// rounding tolerance or ±0.1% of a level.
func isGrounded(text string, grounded []float64) bool {
	for _, t := range numberTokens(text) {
		matched := false
		for _, r := range t.readings {
			for _, f := range grounded {
				if abs(f-r) <= max(t.tolerance, abs(f)*0.001) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

var upper = cases.Upper(language.Und)

// normalizeTickers is MarketBriefValidator.tickers: trimmed, "$" removed,
// uppercased (Unicode full mapping, as String.uppercased()), matching
// [A-Z][A-Z.]{0,5}, de-duplicated.
func normalizeTickers(raw []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range raw {
		t := upper.String(strings.ReplaceAll(trimWhitespaces(v), "$", ""))
		if !isTicker(t) || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func isTicker(t string) bool {
	if len(t) == 0 || len(t) > 6 || t[0] < 'A' || t[0] > 'Z' {
		return false
	}
	for i := 1; i < len(t); i++ {
		if (t[i] < 'A' || t[i] > 'Z') && t[i] != '.' {
			return false
		}
	}
	return true
}

// httpsURL is MarketBriefValidator.httpsURL: the trimmed string when it
// parses as a URL whose scheme is exactly "https" and has a host.
func httpsURL(raw *string) *string {
	if raw == nil {
		return nil
	}
	trimmed := trimWhitespaceAndNewlines(*raw)
	scheme, _, ok := strings.Cut(trimmed, ":")
	if !ok || scheme != "https" {
		return nil
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	return &trimmed
}
