package insights

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Ported from SentimentClassifierTests.swift.

func score(r classifierResult) float64 {
	if r.Score == nil {
		return 0
	}
	return *r.Score
}

func TestClassifierDirectionality(t *testing.T) {
	bull := classify("Absolutely bullish on this, loading calls")
	if bull.Label != positiveLabel || score(bull) <= 0 {
		t.Fatalf("bullish: %+v", bull)
	}
	bear := classify("This is a bubble, puts printing, total crash incoming")
	if bear.Label != negativeLabel || score(bear) >= 0 {
		t.Fatalf("bearish: %+v", bear)
	}
}

func TestClassifierSubstringCollisions(t *testing.T) {
	for _, text := range []string{"supply chain disruption", "download the report", "the upset was notable"} {
		r := classify(text)
		if r.MatchCount != 0 || r.Score != nil {
			t.Fatalf("%q: %+v", text, r)
		}
	}
	if r := classify("shares are up today"); r.MatchCount != 1 {
		t.Fatalf("up: %+v", r)
	}
}

func TestClassifierNegation(t *testing.T) {
	if score(classify("bullish")) <= 0 || score(classify("not bullish")) >= 0 {
		t.Fatal("negation did not flip")
	}
	if score(classify("not the report I wanted but honestly bullish")) <= 0 {
		t.Fatal("distant negator applied")
	}
}

func TestClassifierAbsenceAndPhrases(t *testing.T) {
	r := classify("The company filed its quarterly report on Tuesday.")
	if r.Score != nil || r.Confidence != 0 || r.Label != neutralLabel {
		t.Fatalf("absence: %+v", r)
	}
	rocket := classify("🚀🚀 diamond hands")
	if score(rocket) <= 0 || rocket.MatchCount < 2 {
		t.Fatalf("rocket: %+v", rocket)
	}
	if score(classify("🐻 paper hands everywhere")) >= 0 {
		t.Fatal("bear emoji")
	}
}

func TestClassifierTokensConfidenceSaturation(t *testing.T) {
	if got := tokenize("$tsla and #tsla and tsla"); !reflect.DeepEqual(got, []string{"tsla", "and", "tsla", "and", "tsla"}) {
		t.Fatalf("tokens %v", got)
	}
	if classify("bullish rally breakout squeeze moon").Confidence <= classify("bullish").Confidence {
		t.Fatal("confidence does not track evidence")
	}
	ext := score(classify(strings.Repeat("bullish rally squeeze moon breakout ", 20)))
	if ext > 1 || ext <= 0.8 {
		t.Fatalf("saturation %v", ext)
	}
	mixed := classify("bullish long term but bearish short term")
	if math.Abs(score(mixed)) > labelThreshold || mixed.Label != neutralLabel {
		t.Fatalf("mixed %+v", mixed)
	}
	for s, want := range map[float64]string{0.2: positiveLabel, -0.2: negativeLabel, 0.1: neutralLabel, 0.15: neutralLabel} {
		if labelForScore(s) != want {
			t.Fatalf("label(%v)", s)
		}
	}
}

// Ported from SentimentAggregatorTests.swift.

func in(score *float64, label string, conf *float64, source string) aggregateInput {
	return aggregateInput{Scored: scored{Score: score, Label: label, Confidence: conf}, Source: source}
}

func f(v float64) *float64 { return &v }

func TestAggregatorWeightsAndAbsence(t *testing.T) {
	r := aggregate([]aggregateInput{in(f(1), "positive", f(1), SourceX), in(f(-1), "negative", f(0.1), SourceX)})
	if *r.Score <= 0.5 || r.Label != positiveLabel {
		t.Fatalf("weighting %+v", r)
	}
	r = aggregate([]aggregateInput{in(nil, "neutral", nil, SourceX), in(nil, "neutral", nil, SourceX), in(f(0.8), "positive", f(1), SourceX)})
	if r.PostCount != 3 || *r.Score <= 0 {
		t.Fatalf("unscored %+v", r)
	}
	r = aggregate([]aggregateInput{in(nil, "neutral", nil, SourceX), in(nil, "neutral", nil, SourceX)})
	if r.Score != nil || r.Confidence != nil || r.PostCount != 2 {
		t.Fatalf("absence %+v", r)
	}
	if r = aggregate(nil); r.Score != nil || r.PostCount != 0 || r.SourceCounts.Total() != 0 {
		t.Fatalf("empty %+v", r)
	}
	r = aggregate([]aggregateInput{
		in(f(0.5), "positive", f(0.5), SourceReddit), in(f(0.5), "positive", f(0.5), SourceReddit),
		in(f(-0.5), "negative", f(0.5), SourceStocktwits), in(f(0.1), "neutral", f(0.5), SourceNews),
	})
	if r.SourceCounts != (SourceCounts{Reddit: 2, Stocktwits: 1, News: 1}) {
		t.Fatalf("counts %+v", r.SourceCounts)
	}
	thin := aggregate([]aggregateInput{in(f(0.8), "positive", f(1), SourceX)})
	var many []aggregateInput
	for range 10 {
		many = append(many, in(f(0.8), "positive", f(1), SourceX))
	}
	if *thin.Confidence >= *aggregate(many).Confidence {
		t.Fatal("thin sample not discounted")
	}
	if *aggregate([]aggregateInput{in(f(0.9), "positive", nil, SourceX)}).Score <= 0 {
		t.Fatal("provider score without confidence")
	}
}

func TestVolumeZScoreAndDelta(t *testing.T) {
	if volumeZScore(100, []int{1, 2}) != nil || volumeZScore(100, nil) != nil {
		t.Fatal("short history")
	}
	if z := volumeZScore(200, []int{10, 12, 9, 11, 10, 13}); z == nil || *z <= 3 {
		t.Fatalf("spike %v", z)
	}
	mega := volumeZScore(5000, []int{5000, 4980, 5020, 4990, 5010, 5000})
	small := volumeZScore(90, []int{3, 4, 2, 5, 3, 4})
	if math.Abs(*mega) >= 1 || *small <= *mega {
		t.Fatalf("mega %v small %v", *mega, *small)
	}
	if z := volumeZScore(20, []int{5, 5, 5, 5, 5, 5}); z == nil || *z <= 0 {
		t.Fatal("flat")
	}
	if delta(f(0.5), nil) != nil || delta(nil, f(0.5)) != nil {
		t.Fatal("delta needs both")
	}
	if d := delta(f(0.5), f(0.2)); math.Abs(*d-0.3) > 1e-12 {
		t.Fatalf("delta %v", *d)
	}
}

func TestParseHermesTimestamp(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, v := range []string{"2026-07-03T22:13:45+00:00", "2026-07-03T22:13:45+00:00Z", "2026-07-03T22:13:45Z", "2026-07-03T22:13:45.123Z"} {
		if parseHermesTimestamp(s(v)) == nil {
			t.Fatalf("%q did not parse", v)
		}
	}
	if parseHermesTimestamp(s("not-a-date")) != nil || parseHermesTimestamp(nil) != nil {
		t.Fatal("garbage parsed")
	}
	got := parseHermesTimestamp(s("2026-07-02T09:00:00+00:00Z"))
	if !got.Equal(time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v", got)
	}
}

func TestCooldownRegistry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := NewCooldownRegistry(time.Minute)
	r.now = func() time.Time { return now }
	if r.IsCoolingDown("DeepAPIInsightsProvider") {
		t.Fatal("fresh registry cools")
	}
	r.BeginCooldown("DeepAPIInsightsProvider")
	if !r.IsCoolingDown("DeepAPIInsightsProvider") || !reflect.DeepEqual(r.ActiveCooldowns(), []string{"DeepAPIInsightsProvider"}) {
		t.Fatal("cooldown not active")
	}
	now = now.Add(2 * time.Minute)
	if r.IsCoolingDown("DeepAPIInsightsProvider") {
		t.Fatal("cooldown did not expire")
	}
	r.BeginCooldown("X")
	r.Clear("X")
	if r.IsCoolingDown("X") {
		t.Fatal("clear")
	}
}
