package recaps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Money is RecapMoney.
type Money struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// TickerStat is RecapTickerStat.
type TickerStat struct {
	Symbol  string `json:"symbol"`
	Touches int    `json:"touches"`
}

// Badge is RecapBadge.
type Badge struct {
	Type string `json:"type"`
	Tier string `json:"tier"`
}

// Growth is RecapGrowth.
type Growth struct {
	Currency   string  `json:"currency"`
	StartValue float64 `json:"startValue"`
	EndValue   float64 `json:"endValue"`
}

// IntroSlide is RecapIntroSlide.
type IntroSlide struct {
	PeriodLabel string `json:"periodLabel"`
	ActiveDays  int    `json:"activeDays"`
}

// StreakSlide is RecapStreakSlide.
type StreakSlide struct {
	LongestStreakDays int `json:"longestStreakDays"`
	ActiveDays        int `json:"activeDays"`
}

// TopTickersSlide is RecapTopTickersSlide.
type TopTickersSlide struct {
	Tickers []TickerStat `json:"tickers"`
}

// TradesSlide is RecapTradesSlide.
type TradesSlide struct {
	TradeCount       int     `json:"tradeCount"`
	BuyCount         int     `json:"buyCount"`
	SellCount        int     `json:"sellCount"`
	MostTradedSymbol *string `json:"mostTradedSymbol,omitempty"`
}

// ResearchSlide is RecapResearchSlide.
type ResearchSlide struct {
	NotesWritten   int `json:"notesWritten"`
	WatchlistAdded int `json:"watchlistAdded"`
	TargetsSet     int `json:"targetsSet"`
	TargetsHit     int `json:"targetsHit"`
}

// GrowthSlide is RecapGrowthSlide.
type GrowthSlide struct {
	Growth []Growth `json:"growth"`
}

// DividendsSlide is RecapDividendsSlide.
type DividendsSlide struct {
	Received     []Money `json:"received"`
	PaymentCount int     `json:"paymentCount"`
	PayerCount   int     `json:"payerCount"`
}

// GoalsSlide is RecapGoalsSlide.
type GoalsSlide struct {
	Contributed       []Money `json:"contributed"`
	ContributionCount int     `json:"contributionCount"`
}

// BadgesSlide is RecapBadgesSlide.
type BadgesSlide struct {
	Badges   []Badge `json:"badges"`
	XPEarned int     `json:"xpEarned"`
}

// PersonalitySlide is RecapPersonalitySlide.
type PersonalitySlide struct {
	Archetype string `json:"archetype"`
}

// MissedYouSlide is RecapMissedYouSlide.
type MissedYouSlide struct {
	BestStreakDays int `json:"bestStreakDays"`
	TotalBadges    int `json:"totalBadges"`
}

// OutroSlide is RecapOutroSlide.
type OutroSlide struct {
	IsFirstChapter  bool   `json:"isFirstChapter"`
	NextPeriodLabel string `json:"nextPeriodLabel"`
}

// Slide is RecapSlide: {"type": "<case>", "payload": {...}}.
type Slide struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// slidePayload returns an empty payload for a slide type, nil for an
// unknown one (a Swift decoding error).
func slidePayload(kind string) any {
	switch kind {
	case "intro":
		return &IntroSlide{}
	case "streak":
		return &StreakSlide{}
	case "top_tickers":
		return &TopTickersSlide{}
	case "trades":
		return &TradesSlide{}
	case "research":
		return &ResearchSlide{}
	case "growth":
		return &GrowthSlide{}
	case "dividends":
		return &DividendsSlide{}
	case "goals":
		return &GoalsSlide{}
	case "badges":
		return &BadgesSlide{}
	case "personality":
		return &PersonalitySlide{}
	case "missed_you":
		return &MissedYouSlide{}
	case "outro":
		return &OutroSlide{}
	}
	return nil
}

// UnmarshalJSON decodes the payload into the struct its type names.
func (s *Slide) UnmarshalJSON(raw []byte) error {
	var w struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	p := slidePayload(w.Type)
	if p == nil {
		return fmt.Errorf("recap: unknown slide type %q", w.Type)
	}
	if err := strictDecode(w.Payload, p); err != nil {
		return err
	}
	s.Type, s.Payload = w.Type, p
	return nil
}

// Deck is RecapDeck.
type Deck struct {
	Kind           string  `json:"kind"`
	Key            string  `json:"key"`
	PeriodLabel    string  `json:"periodLabel"`
	Archetype      *string `json:"archetype,omitempty"`
	IsFirstChapter bool    `json:"isFirstChapter"`
	IsMissedYou    bool    `json:"isMissedYou"`
	Slides         []Slide `json:"slides"`
	SchemaVersion  int     `json:"schemaVersion"`
}

// strictDecode decodes JSON the way Swift's synthesized Decodable does for
// the fields that matter here: a missing or null non-optional field is an
// error. Only top-level presence is checked; slices and nested structs are
// validated by their own decoders.
func strictDecode(raw []byte, v any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("recap: missing payload")
	}
	return json.Unmarshal(raw, v)
}

// encodePayload is RecapRecord's encoder: JSONEncoder with .sortedKeys, so
// a row v2 writes is byte-identical to one Vapor writes (Foundation also
// escapes "/" as "\/" and leaves <, > and & alone).
func encodePayload(d Deck) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return "", err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return "", err
	}
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), "/", `\/`), nil
}

// decodePayload is RecapRecord.deck().
func decodePayload(payload string) (Deck, error) {
	var d Deck
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		return Deck{}, err
	}
	if d.Slides == nil {
		return Deck{}, fmt.Errorf("recap: deck without slides")
	}
	return d, nil
}

// Deck builder constants (RecapDeckBuilder).
const (
	schemaVersion          = 1
	minFullDeckSlides      = 4
	minFullDeckActiveDays  = 7
	maxTopTickers          = 5
	archetypeSteadyBuilder = "steady_builder"
)

type archetypeRule struct {
	archetype string
	matches   func(f *Facts, k int) bool
}

// archetypeRules is PersonalityClassifier.rules: the first match wins.
var archetypeRules = []archetypeRule{
	{"copycat_strategist", func(f *Facts, k int) bool { return f.PilotFollows >= 2*k }},
	{"dividend_farmer", func(f *Facts, k int) bool { return f.DividendPayments >= 3*k }},
	{"saver", func(f *Facts, k int) bool { return f.GoalContributionCount >= 3*k }},
	{"comeback_kid", func(f *Facts, k int) bool {
		return f.ActiveDaysFirstHalf >= 1 && f.ActiveDaysSecondHalf >= 5*k && f.ActiveDaysSecondHalf >= 2*f.ActiveDaysFirstHalf
	}},
	{"ticker_hunter", func(f *Facts, k int) bool { return len(f.ResearchTouchesBySymbol) >= 10*k }},
	{"researcher", func(f *Facts, k int) bool { return f.NotesWritten+f.TargetsSet >= 5*k }},
	{"disciplined_sizer", func(f *Facts, k int) bool { return f.TargetsSet >= 3*k }},
	{"patient_planner", func(f *Facts, k int) bool { return f.ActiveDays >= 10*k && f.TradeCount <= 2*k }},
}

// Classify is PersonalityClassifier.classify(_:kind:).
func Classify(f *Facts, kind string) string {
	k := 1
	if kind == KindYear {
		k = 4
	}
	for _, r := range archetypeRules {
		if r.matches(f, k) {
			return r.archetype
		}
	}
	return archetypeSteadyBuilder
}

// BuildDeck is RecapDeckBuilder.build(facts:period:).
func BuildDeck(f *Facts, p Period) Deck {
	intro := Slide{"intro", &IntroSlide{PeriodLabel: p.Label(), ActiveDays: f.ActiveDays}}
	nextLabel := p.Next().Label()
	deck := func(archetype *string, firstChapter, missedYou bool, slides []Slide) Deck {
		return Deck{
			Kind: p.Kind, Key: p.Key(), PeriodLabel: p.Label(), Archetype: archetype,
			IsFirstChapter: firstChapter, IsMissedYou: missedYou, Slides: slides, SchemaVersion: schemaVersion,
		}
	}
	var middle []Slide
	if f.HasAnyActivity() {
		middle = middleSlides(f)
	}
	if len(middle) == 0 {
		return deck(nil, false, true, []Slide{
			intro,
			{"missed_you", &MissedYouSlide{BestStreakDays: f.BestStreakEverDays, TotalBadges: f.TotalBadgesEver}},
			{"outro", &OutroSlide{IsFirstChapter: false, NextPeriodLabel: nextLabel}},
		})
	}
	if len(middle) >= minFullDeckSlides && f.ActiveDays >= minFullDeckActiveDays {
		a := Classify(f, p.Kind)
		slides := append([]Slide{intro}, middle...)
		slides = append(slides,
			Slide{"personality", &PersonalitySlide{Archetype: a}},
			Slide{"outro", &OutroSlide{IsFirstChapter: false, NextPeriodLabel: nextLabel}})
		return deck(&a, false, false, slides)
	}
	slides := append([]Slide{intro}, middle[:min(2, len(middle))]...)
	slides = append(slides, Slide{"outro", &OutroSlide{IsFirstChapter: true, NextPeriodLabel: nextLabel}})
	return deck(nil, true, false, slides)
}

func middleSlides(f *Facts) []Slide {
	slides := []Slide{}
	if f.LongestStreakDays >= 2 {
		slides = append(slides, Slide{"streak", &StreakSlide{LongestStreakDays: f.LongestStreakDays, ActiveDays: f.ActiveDays}})
	}
	if len(f.ResearchTouchesBySymbol) >= 3 {
		stats := make([]TickerStat, 0, len(f.ResearchTouchesBySymbol))
		for k, v := range f.ResearchTouchesBySymbol {
			stats = append(stats, TickerStat{Symbol: k, Touches: v})
		}
		sort.Slice(stats, func(i, j int) bool {
			if stats[i].Touches != stats[j].Touches {
				return stats[i].Touches > stats[j].Touches
			}
			return stats[i].Symbol < stats[j].Symbol
		})
		slides = append(slides, Slide{"top_tickers", &TopTickersSlide{Tickers: stats[:min(maxTopTickers, len(stats))]}})
	}
	if f.TradeCount >= 1 {
		var most *string
		bestCount := 0
		for sym, n := range f.TradesBySymbol {
			if most == nil || n > bestCount || (n == bestCount && sym < *most) {
				s := sym
				most, bestCount = &s, n
			}
		}
		slides = append(slides, Slide{"trades", &TradesSlide{
			TradeCount: f.TradeCount, BuyCount: f.BuyCount, SellCount: f.SellCount, MostTradedSymbol: most,
		}})
	}
	if f.NotesWritten+f.WatchlistAdded+f.TargetsSet >= 1 {
		slides = append(slides, Slide{"research", &ResearchSlide{
			NotesWritten: f.NotesWritten, WatchlistAdded: f.WatchlistAdded, TargetsSet: f.TargetsSet, TargetsHit: f.TargetsHit,
		}})
	}
	if len(f.Growth) > 0 {
		slides = append(slides, Slide{"growth", &GrowthSlide{Growth: f.Growth}})
	}
	if f.DividendPayments >= 1 {
		slides = append(slides, Slide{"dividends", &DividendsSlide{
			Received: nonNil(f.DividendsReceived), PaymentCount: f.DividendPayments, PayerCount: f.DividendPayers,
		}})
	}
	if f.GoalContributionCount >= 1 {
		slides = append(slides, Slide{"goals", &GoalsSlide{Contributed: nonNil(f.GoalContributions), ContributionCount: f.GoalContributionCount}})
	}
	if len(f.BadgesEarned) > 0 {
		slides = append(slides, Slide{"badges", &BadgesSlide{Badges: f.BadgesEarned, XPEarned: f.XPEarned}})
	}
	return slides
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
