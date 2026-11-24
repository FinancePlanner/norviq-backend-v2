package marketbrief

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/yahoochart"
)

// eveningAt is 22:40 Lisbon on Friday 20 November 2026.
var eveningAt = time.Date(2026, 11, 20, 22, 40, 0, 0, lisbon)

func eveningQuote(symbol string, price, previous float64) yahoochart.Quote {
	usd := "USD"
	return yahoochart.Quote{Symbol: symbol, Price: price, PreviousClose: previous, MarketTime: eveningAt.Add(-90 * time.Minute), Currency: &usd}
}

func TestEditionID(t *testing.T) {
	if got := EditionID("2026-11-20", SlotEvening); got != "MI-2026-11-20-C" {
		t.Fatal(got)
	}
	if got := EditionID("2026-11-20", SlotMorning); got != "MI-2026-11-20-O" {
		t.Fatal(got)
	}
	if editionKind(SlotMorning) != EditionKindOpen || editionKind(SlotEvening) != EditionKindClose {
		t.Fatal("kinds")
	}
}

// The live brief of 2026-11-20 narrated the 10-year as "down 2.77%": the
// yield's relative change. Its move is about -14 basis points.
func TestYieldMovesInBasisPoints(t *testing.T) {
	q := eveningQuote("^TNX", 4.910, 5.050)
	if got := changeBps(q); got != -14 {
		t.Fatalf("bps %v", got)
	}
	f := buildFacts(SlotEvening, "2026-11-20", []yahoochart.Quote{q, eveningQuote("^NDX", 25029.33, 26519.65)}, nil, nil, eveningAt)
	if f.Quotes[0].ChangeBps == nil || *f.Quotes[0].ChangeBps != -14 {
		t.Fatalf("facts %+v", f.Quotes[0])
	}
	if f.Quotes[1].ChangeBps != nil {
		t.Fatal("an index has no basis points")
	}
	grounded := f.GroundedNumbers()
	for _, want := range []float64{-14, 14} {
		found := false
		for _, g := range grounded {
			found = found || g == want
		}
		if !found {
			t.Errorf("%v not grounded in %v", want, grounded)
		}
	}
	for _, g := range grounded {
		if g == 2.77 || g == -2.77 {
			t.Fatalf("a yield's relative change must not ground a line: %v", grounded)
		}
	}
	raw, _ := json.Marshal(f.Quotes)
	if !strings.Contains(string(raw), `"changeBps":-14`) || strings.Count(string(raw), "changeBps") != 1 {
		t.Fatal(string(raw))
	}
	if !strings.Contains(systemPrompt, `"changeBps" in basis points`) {
		t.Fatal("the prompt must tell the model to narrate yields in basis points")
	}
}

func TestBuildEdition(t *testing.T) {
	due := Due{TradingDate: "2026-11-20", Slot: SlotEvening}
	quotes := []yahoochart.Quote{
		eveningQuote("^NDX", 25029.334, 26519.651),
		eveningQuote("^GSPC", 6910.12, 7175.51),
		eveningQuote("^TNX", 4.9104, 5.0498),
		eveningQuote("BZ=F", 89.87, 89.44),
	}
	series := map[string]*yahoochart.Intraday{
		"^NDX": {Symbol: "^NDX", Interval: "5m", Timezone: "America/New_York", Points: []yahoochart.Point{
			{Time: eveningAt.Add(-7 * time.Hour), Value: 26500.123},
			{Time: eveningAt.Add(-90 * time.Minute), Value: 25029.334},
		}},
	}
	e := buildEdition(due, quotes, series, "claude+web_search", false, eveningAt)
	if e.ID != "MI-2026-11-20-C" || e.Kind != EditionKindClose || e.SchemaVersion != 2 || e.Status != "published" || e.Timezone != "Europe/Lisbon" {
		t.Fatalf("%+v", e)
	}
	if e.Revision != 0 {
		t.Fatal("revision is stamped by the repository")
	}
	if e.PublishedAt != "2026-11-20T22:40:00Z" || e.MarketDataAsOf == nil || *e.MarketDataAsOf != "2026-11-20T21:10:00Z" {
		t.Fatalf("times %s %v", e.PublishedAt, e.MarketDataAsOf)
	}
	bySymbol := map[string]EditionInstrument{}
	order := []string{}
	for _, in := range e.Instruments {
		bySymbol[in.Symbol] = in
		order = append(order, in.Symbol)
	}
	// Catalog order: displayed rows first, context after.
	if !reflect.DeepEqual(order, []string{"^NDX", "^GSPC", "BZ=F", "^TNX"}) {
		t.Fatal(order)
	}
	ndx := bySymbol["^NDX"]
	if ndx.Last != 25029.33 || ndx.PreviousClose != 26519.65 || ndx.Change != -1490.32 || ndx.ChangePct != -5.62 ||
		ndx.AssetClass != AssetIndex || ndx.Region != "US" || ndx.Unit != UnitPoints || ndx.Group == nil || *ndx.Group != "us_close" ||
		ndx.Currency == nil || *ndx.Currency != "USD" || ndx.ChangeBps != nil || ndx.Source != "yahoo" {
		t.Fatalf("ndx %+v", ndx)
	}
	tnx := bySymbol["^TNX"]
	if tnx.Group != nil || tnx.Unit != UnitPercent || tnx.Precision != 3 || tnx.Last != 4.91 || tnx.ChangeBps == nil || *tnx.ChangeBps != -13.9 {
		t.Fatalf("tnx %+v bps %v", tnx, tnx.ChangeBps)
	}
	if brent := bySymbol["BZ=F"]; brent.AssetClass != AssetCommodity || brent.Unit != UnitUSDBbl {
		t.Fatalf("brent %+v", brent)
	}
	if len(e.Series) != 1 || e.Series[0].Symbol != "^NDX" || e.Series[0].Timezone != "America/New_York" ||
		!reflect.DeepEqual(e.Series[0].Points[0], [2]float64{float64(eveningAt.Add(-7 * time.Hour).Unix()), 26500.12}) {
		t.Fatalf("series %+v", e.Series)
	}
	wantMissing := []string{"quote:^GDAXI", "quote:^FCHI", "quote:^STOXX50E", "quote:^DJI", "quote:^N225", "quote:^HSI", "series:^GSPC", "series:^DJI"}
	if !reflect.DeepEqual(e.Quality.Missing, wantMissing) || e.Quality.Model != "claude+web_search" || e.Quality.Degraded {
		t.Fatalf("quality %+v", e.Quality)
	}
}

func TestEditionRoundTripAndLegacy(t *testing.T) {
	e := buildEdition(Due{TradingDate: "2026-11-20", Slot: SlotEvening}, []yahoochart.Quote{eveningQuote("^NDX", 2, 1)}, nil, "m", true, eveningAt)
	e.Revision = 3
	raw, err := editionJSON(&e)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeEdition(raw); got == nil || !reflect.DeepEqual(*got, e) {
		t.Fatalf("round trip %+v", got)
	}
	if raw, _ := editionJSON(nil); raw != nil {
		t.Fatal("nil edition stores NULL")
	}
	for _, legacy := range []string{"", "null", `{"broken":`, `{}`} {
		if decodeEdition(json.RawMessage(legacy)) != nil {
			t.Fatalf("%q decoded", legacy)
		}
	}
}

func TestContentHashIsStable(t *testing.T) {
	a := contentHash([]string{"en", "pt"}, json.RawMessage(`{"id":"x"}`))
	if a != contentHash([]string{"en", "pt"}, json.RawMessage(`{"id":"x"}`)) || len(a) != 16 {
		t.Fatal(a)
	}
	if a == contentHash([]string{"en", "pt"}, nil) || a == contentHash([]string{"pt", "en"}, json.RawMessage(`{"id":"x"}`)) {
		t.Fatal("hash ignores content")
	}
}

type fakeIntraday map[string]*yahoochart.Intraday

func (f fakeIntraday) Intraday(_ context.Context, symbol, _ string, _ time.Time) *yahoochart.Intraday {
	return f[symbol]
}

func TestGeneratorSeriesKeepsTheTradingDayOnly(t *testing.T) {
	today := &yahoochart.Intraday{Symbol: "^NDX", Points: []yahoochart.Point{{Time: eveningAt.Add(-2 * time.Hour), Value: 1}}}
	yesterday := &yahoochart.Intraday{Symbol: "^GSPC", Points: []yahoochart.Point{{Time: eveningAt.Add(-26 * time.Hour), Value: 1}}}
	g := &LLMGenerator{Intraday: fakeIntraday{"^NDX": today, "^GSPC": yesterday}}
	got := g.series(context.Background(), Due{TradingDate: "2026-11-20", Slot: SlotEvening}, eveningAt)
	if len(got) != 1 || got["^NDX"] != today {
		t.Fatalf("%+v", got)
	}
	if len((&LLMGenerator{}).series(context.Background(), Due{TradingDate: "2026-11-20", Slot: SlotEvening}, eveningAt)) != 0 {
		t.Fatal("no provider, no series")
	}
}

func TestIncludes(t *testing.T) {
	for raw, want := range map[string]bool{"edition": true, "x, edition": true, "editions": false, "": false} {
		r := raw
		if includes(&r, "edition") != want {
			t.Errorf("%q", raw)
		}
	}
	if includes(nil, "edition") {
		t.Fatal("nil")
	}
}
