package marketbrief

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/features/earnings"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/platform/yahoochart"
)

// MarketBriefError cases.
var (
	errUnparseableDraft = errors.New("unparseableDraft")
	errIncompleteBrief  = errors.New("incompleteBrief")
)

// tooFewItemsError is MarketBriefError.tooFewItems.
type tooFewItemsError struct {
	Language string
	Kept     int
}

func (e *tooFewItemsError) Error() string {
	return fmt.Sprintf("tooFewItems(language: %q, kept: %d)", e.Language, e.Kept)
}

// FactQuote is MarketBriefFacts.Quote.
type FactQuote struct {
	Symbol        string  `json:"symbol"`
	Name          string  `json:"name"`
	Price         float64 `json:"price"`
	PreviousClose float64 `json:"previousClose"`
	ChangePercent float64 `json:"changePercent"`
}

// FactHeadline is MarketBriefFacts.Headline.
type FactHeadline struct {
	Title       string  `json:"title"`
	Source      *string `json:"source,omitempty"`
	URL         *string `json:"url,omitempty"`
	PublishedAt string  `json:"publishedAt"`
}

// FactEarnings is MarketBriefFacts.Earnings.
type FactEarnings struct {
	Symbol          string   `json:"symbol"`
	Hour            *string  `json:"hour,omitempty"`
	EpsEstimate     *float64 `json:"epsEstimate,omitempty"`
	EpsActual       *float64 `json:"epsActual,omitempty"`
	RevenueEstimate *float64 `json:"revenueEstimate,omitempty"`
	RevenueActual   *float64 `json:"revenueActual,omitempty"`
}

// Facts is MarketBriefFacts: the "SERVER-SELECTED FACTS" block, everything
// the model may quote without a source.
type Facts struct {
	Slot        string         `json:"slot"`
	TradingDate string         `json:"tradingDate"`
	Quotes      []FactQuote    `json:"quotes"`
	Headlines   []FactHeadline `json:"headlines"`
	Earnings    []FactEarnings `json:"earnings"`
}

const (
	maxHeadlines   = 25
	maxEarnings    = 20
	headlineMaxAge = 24 * time.Hour
)

// buildFacts is MarketBriefFacts.build.
func buildFacts(slot, tradingDate string, quotes []yahoochart.Quote, news []newsprovider.Item, cal []earnings.ItemResponse, now time.Time) Facts {
	f := Facts{Slot: slot, TradingDate: tradingDate, Quotes: []FactQuote{}, Headlines: []FactHeadline{}, Earnings: []FactEarnings{}}
	for _, q := range quotes {
		name := q.Symbol
		if in, ok := catalogInstrument(q.Symbol); ok {
			name = in.Name
		}
		f.Quotes = append(f.Quotes, FactQuote{
			Symbol: q.Symbol, Name: name,
			Price: round2(q.Price), PreviousClose: round2(q.PreviousClose), ChangePercent: round2(q.ChangePercent()),
		})
	}
	recent := []newsprovider.Item{}
	for _, n := range news {
		if now.Sub(n.PublishedAt) <= headlineMaxAge {
			recent = append(recent, n)
		}
	}
	// min(count:sortedBy: >): the newest 25, newest first.
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].PublishedAt.After(recent[j].PublishedAt) })
	if len(recent) > maxHeadlines {
		recent = recent[:maxHeadlines]
	}
	for _, n := range recent {
		f.Headlines = append(f.Headlines, FactHeadline{Title: n.Headline, Source: n.Source, URL: n.URL, PublishedAt: formatISOSeconds(n.PublishedAt)})
	}
	if len(cal) > maxEarnings {
		cal = cal[:maxEarnings]
	}
	for _, item := range cal {
		if item.Symbol == nil || *item.Symbol == "" {
			continue
		}
		f.Earnings = append(f.Earnings, FactEarnings{
			Symbol: *item.Symbol, Hour: item.Hour,
			EpsEstimate: item.EpsEstimate, EpsActual: item.EpsActual,
			RevenueEstimate: item.RevenueEstimate, RevenueActual: item.RevenueActual,
		})
	}
	return f
}

// GroundedNumbers is MarketBriefFacts.groundedNumbers: every number an
// unsourced line may contain.
func (f Facts) GroundedNumbers() []float64 {
	var out []float64
	for _, q := range f.Quotes {
		out = append(out, q.Price, q.PreviousClose, q.ChangePercent, math.Abs(q.ChangePercent))
	}
	for _, e := range f.Earnings {
		for _, v := range []*float64{e.EpsEstimate, e.EpsActual} {
			if v != nil {
				out = append(out, *v)
			}
		}
		// Revenue is written as "$40.5B" or "40 500 M", so ground it at
		// every scale a sentence might use.
		for _, v := range []*float64{e.RevenueEstimate, e.RevenueActual} {
			if v != nil {
				out = append(out, *v, round2(*v/1e9), round2(*v/1e6))
			}
		}
	}
	for _, h := range f.Headlines {
		for _, t := range numberTokens(h.Title) {
			out = append(out, t.readings...)
		}
	}
	return out
}

// round2 is MarketBriefFacts.round2 (.toNearestOrAwayFromZero).
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// formatISOSeconds is ISO8601DateFormatter's default output.
func formatISOSeconds(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// numberToken is MarketBriefNumbers.Token.
type numberToken struct {
	// readings are every value the token could mean.
	readings []float64
	// tolerance is how far a reading may sit from a fact and still match.
	tolerance float64
}

// isGroupSeparator is the regex's [ \u{00A0}\u{202F}].
func isGroupSeparator(r rune) bool { return r == ' ' || r == ' ' || r == ' ' }

// isCurrency is the regex's [$€£].
func isCurrency(r rune) bool { return r == '$' || r == '€' || r == '£' }

// numberTokens is MarketBriefNumbers.tokens: the matches of
//
//	(?<currency>[$€£])?(?<number>\d{1,3}(?:[ \u{00A0}\u{202F}]\d{3})+(?!\d)|\d+(?:[.,]\d+)+|\d+)(?<percent>\s?%)?
//
// walked by hand because RE2 has no lookahead. Swift's \d and \s are
// Unicode-aware, hence unicode.IsDigit and unicode.IsSpace.
func numberTokens(text string) []numberToken {
	rs := []rune(text)
	var out []numberToken
	for i := 0; i < len(rs); {
		start := i
		currency := false
		if isCurrency(rs[i]) && i+1 < len(rs) && unicode.IsDigit(rs[i+1]) {
			currency = true
			start = i + 1
		}
		numEnd, ok := matchNumber(rs, start)
		if !ok {
			i++
			continue
		}
		end := numEnd
		percent := false
		if end < len(rs) && rs[end] == '%' {
			percent, end = true, end+1
		} else if end+1 < len(rs) && unicode.IsSpace(rs[end]) && rs[end+1] == '%' {
			percent, end = true, end+2
		}
		if t, ok := tokenFor(string(rs[start:numEnd]), percent, currency); ok {
			out = append(out, t)
		}
		i = end
	}
	return out
}

// matchNumber tries the three alternatives of <number> in order at i and
// returns where the match ends.
func matchNumber(rs []rune, i int) (int, bool) {
	digitsFrom := func(j int) int {
		for j < len(rs) && unicode.IsDigit(rs[j]) {
			j++
		}
		return j
	}
	runEnd := digitsFrom(i)
	if runEnd == i {
		return 0, false
	}
	// \d{1,3}(?:[ NBSP NNBSP]\d{3})+(?!\d): the lead run must be 1-3
	// digits (a longer run leaves a digit where a separator is needed);
	// take as many groups as possible, backing off until no digit follows.
	if runEnd-i <= 3 {
		var ends []int
		j := runEnd
		for j+3 < len(rs) && isGroupSeparator(rs[j]) &&
			unicode.IsDigit(rs[j+1]) && unicode.IsDigit(rs[j+2]) && unicode.IsDigit(rs[j+3]) {
			j += 4
			ends = append(ends, j)
		}
		for k := len(ends) - 1; k >= 0; k-- {
			if e := ends[k]; e >= len(rs) || !unicode.IsDigit(rs[e]) {
				return e, true
			}
		}
	}
	// \d+(?:[.,]\d+)+
	j, groups := runEnd, 0
	for j+1 < len(rs) && (rs[j] == '.' || rs[j] == ',') && unicode.IsDigit(rs[j+1]) {
		j = digitsFrom(j + 1)
		groups++
	}
	if groups > 0 {
		return j, true
	}
	// \d+
	return runEnd, true
}

// tokenFor is the compactMap body of tokens(in:).
func tokenFor(raw string, percent, currency bool) (numberToken, bool) {
	if strings.ContainsAny(raw, ".,") {
		return numberToken{readings: numberReadings(raw), tolerance: 0.051}, true
	}
	var digits strings.Builder
	for _, r := range raw {
		if unicode.IsNumber(r) {
			digits.WriteRune(r)
		}
	}
	value, ok := swiftDouble(digits.String())
	if !ok {
		return numberToken{}, false
	}
	spaceGrouped := digits.Len() != len(raw)
	isLevel := value >= 1000 && (value < 1900 || value > 2100)
	if !spaceGrouped && !isLevel && !percent && !currency {
		return numberToken{}, false
	}
	return numberToken{readings: []float64{value}, tolerance: 0.5}, true
}

// numberReadings is MarketBriefNumbers.readings: "25.032" is 25.032 in en
// and 25032 in pt-PT.
func numberReadings(token string) []float64 {
	var out []float64
	if v, ok := swiftDouble(strings.ReplaceAll(token, ",", "")); ok {
		out = append(out, v)
	}
	if v, ok := swiftDouble(strings.ReplaceAll(strings.ReplaceAll(token, ".", ""), ",", ".")); ok {
		out = append(out, v)
	}
	return out
}

// swiftDouble is Double(String) over the digit-and-dot strings tokens
// produce: ASCII digits only; out-of-range values are ±infinity.
func swiftDouble(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return 0, false
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return v, true
		}
		return 0, false
	}
	return v, true
}
