package marketbrief

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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
	e := buildEdition(due, quotes, nil, series, "claude+web_search", false, eveningAt)
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
	e := buildEdition(Due{TradingDate: "2026-11-20", Slot: SlotEvening}, []yahoochart.Quote{eveningQuote("^NDX", 2, 1)}, nil, nil, "m", true, eveningAt)
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

// fakeIntraday serves canned bars and records the window it was asked for.
type fakeIntraday struct {
	bars     map[string]*yahoochart.Intraday
	from, to time.Time
}

func (f *fakeIntraday) Intraday(_ context.Context, symbol, _ string, from, to time.Time) *yahoochart.Intraday {
	f.from, f.to = from, to
	return f.bars[symbol]
}

func bars(symbol string, times ...time.Time) *yahoochart.Intraday {
	d := &yahoochart.Intraday{Symbol: symbol, Interval: "5m"}
	for i, at := range times {
		d.Points = append(d.Points, yahoochart.Point{Time: at, Value: float64(100 + i)})
	}
	return d
}

func TestGeneratorSeriesKeepsTheTradingDayOnly(t *testing.T) {
	due := Due{TradingDate: "2026-11-20", Slot: SlotEvening}
	quote := eveningQuote("^NDX", 105, 100) // 21:10 Lisbon, 16:10 New York
	// The session's last bar is 15:55 New York; the series ends at the close.
	f := &fakeIntraday{bars: map[string]*yahoochart.Intraday{"^NDX": bars("^NDX", quote.MarketTime.Add(-15*time.Minute))}}
	g := &LLMGenerator{Intraday: f}
	got := g.series(context.Background(), due, eveningAt, []yahoochart.Quote{quote})
	if len(got) != 1 || got["^NDX"] == nil {
		t.Fatalf("%+v", got)
	}
	ny, _ := time.LoadLocation("America/New_York")
	if want := time.Date(2026, 11, 20, 0, 0, 0, 0, ny); !f.from.Equal(want) || !f.to.Equal(eveningAt) {
		t.Fatalf("window %v..%v", f.from, f.to)
	}
	// An evening with no bars in the window is not charted.
	g = &LLMGenerator{Intraday: &fakeIntraday{bars: map[string]*yahoochart.Intraday{}}}
	if got := g.series(context.Background(), due, eveningAt, []yahoochart.Quote{quote}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if len((&LLMGenerator{}).series(context.Background(), due, eveningAt, nil)) != 0 {
		t.Fatal("no provider, no series")
	}
}

func TestFuturesSessionStart(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	for _, c := range []struct{ at, want string }{
		{"2026-11-26T08:25:00Z", "2026-11-25T18:00"}, // Thanksgiving Thursday: Wednesday's reopen
		{"2026-11-30T08:25:00Z", "2026-11-29T18:00"}, // Monday: Sunday's reopen, not Friday's
		{"2026-03-10T08:25:00Z", "2026-03-09T18:00"}, // US on EDT, Lisbon still on WET
		{"2026-11-25T23:30:00Z", "2026-11-25T18:00"}, // after the reopen, same ET day
	} {
		at, _ := time.Parse(time.RFC3339, c.at)
		got, ok := seriesWindowStart("NQ=F", at)
		if !ok || got.In(ny).Format("2006-01-02T15:04") != c.want {
			t.Errorf("%s: %v", c.at, got.In(ny))
		}
	}
	if _, ok := seriesWindowStart("^N225", time.Now()); ok {
		t.Fatal("uncharted symbol has no window")
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	at, _ := time.Parse(time.RFC3339, "2026-11-26T08:25:00Z")
	if got, ok := seriesWindowStart("^GDAXI", at); !ok || !got.Equal(time.Date(2026, 11, 26, 0, 0, 0, 0, berlin)) {
		t.Fatalf("DAX window %v", got)
	}
}

func TestSeriesEndsAtQuote(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	at := time.Date(2026, 11, 26, 8, 25, 0, 0, time.UTC)
	start := time.Date(2026, 11, 25, 18, 0, 0, 0, ny)
	nq := func(marketTime time.Time) yahoochart.Quote {
		return yahoochart.Quote{Symbol: "NQ=F", Price: 25100, PreviousClose: 25000, MarketTime: marketTime}
	}
	due := Due{TradingDate: "2026-11-26", Slot: SlotMorning}
	run := func(q []yahoochart.Quote, d *yahoochart.Intraday) (map[string]*yahoochart.Intraday, *fakeIntraday) {
		f := &fakeIntraday{bars: map[string]*yahoochart.Intraday{"NQ=F": d}}
		return (&LLMGenerator{Intraday: f}).series(context.Background(), due, at, q), f
	}

	t.Run("quote newer than last bar", func(t *testing.T) {
		q := nq(at.Add(-3 * time.Minute))
		got, f := run([]yahoochart.Quote{q}, bars("NQ=F", at.Add(-15*time.Minute), at.Add(-10*time.Minute), at.Add(-5*time.Minute)))
		s := got["NQ=F"]
		if s == nil || len(s.Points) != 4 {
			t.Fatalf("%+v", s)
		}
		last := s.Points[3]
		if !last.Time.Equal(q.MarketTime) || last.Value != q.Price {
			t.Fatalf("last %+v", last)
		}
		if pct := (last.Value/q.PreviousClose - 1) * 100; pct != q.ChangePercent() {
			t.Fatalf("legend %v scorecard %v", pct, q.ChangePercent())
		}
		if !f.from.Equal(start) || !f.to.Equal(at) {
			t.Fatalf("window %v..%v", f.from, f.to)
		}
	})
	t.Run("quote older than last bar", func(t *testing.T) {
		q := nq(at.Add(-12 * time.Minute))
		got, _ := run([]yahoochart.Quote{q}, bars("NQ=F", at.Add(-20*time.Minute), at.Add(-15*time.Minute), at.Add(-12*time.Minute), at.Add(-10*time.Minute)))
		s := got["NQ=F"]
		if s == nil || len(s.Points) != 3 {
			t.Fatalf("%+v", s)
		}
		if last := s.Points[2]; !last.Time.Equal(q.MarketTime) || last.Value != q.Price || !s.Points[1].Time.Equal(at.Add(-15*time.Minute)) {
			t.Fatalf("%+v", s.Points)
		}
	})
	t.Run("last bar 7h before quote", func(t *testing.T) {
		got, _ := run([]yahoochart.Quote{nq(at)}, bars("NQ=F", at.Add(-8*time.Hour), at.Add(-7*time.Hour)))
		if len(got) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("gap of exactly maxSeriesGap is kept", func(t *testing.T) {
		got, _ := run([]yahoochart.Quote{nq(at)}, bars("NQ=F", at.Add(-maxSeriesGap)))
		if got["NQ=F"] == nil {
			t.Fatal("dropped")
		}
	})
	t.Run("no bars before the quote", func(t *testing.T) {
		got, _ := run([]yahoochart.Quote{nq(at.Add(-time.Hour))}, bars("NQ=F", at.Add(-time.Hour)))
		if len(got) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no quote for the symbol", func(t *testing.T) {
		got, _ := run(nil, bars("NQ=F", at.Add(-5*time.Minute)))
		if len(got) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("bars before the window", func(t *testing.T) {
		q := nq(at.Add(-3 * time.Minute))
		got, _ := run([]yahoochart.Quote{q}, bars("NQ=F", start.Add(-5*time.Minute), at.Add(-10*time.Minute), at.Add(-5*time.Minute)))
		s := got["NQ=F"]
		if s == nil || len(s.Points) != 3 {
			t.Fatalf("%+v", s)
		}
		for _, p := range s.Points {
			if p.Time.Before(start) {
				t.Fatalf("bar %v before the window %v", p.Time, start)
			}
		}
	})
}

// session is 5-minute bars from first to last inclusive, valued 100, 101…
func session(symbol string, first, last time.Time) *yahoochart.Intraday {
	var times []time.Time
	for ts := first; !ts.After(last); ts = ts.Add(5 * time.Minute) {
		times = append(times, ts)
	}
	return bars(symbol, times...)
}

// Real 2026-11-25 Close stamps (norviq.org/brief/2026-11-25/evening): S&P 500
// and Dow 21:32 Lisbon (16:32 ET), Nasdaq 100 22:15 (17:15 ET). The bars stop
// at the 16:00 ET close, so a gap measured from the quote dropped all three.
func TestEveningSeriesEndsAtSessionClose(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	at := time.Date(2026, 11, 25, 22, 31, 0, 0, time.UTC)
	closeAt := time.Date(2026, 11, 25, 16, 0, 0, 0, ny)
	quotes := []yahoochart.Quote{
		{Symbol: "^NDX", Price: 25200.5, PreviousClose: 25000, MarketTime: time.Date(2026, 11, 25, 22, 15, 0, 0, time.UTC)},
		{Symbol: "^GSPC", Price: 6990.25, PreviousClose: 7000, MarketTime: time.Date(2026, 11, 25, 21, 32, 6, 0, time.UTC)},
		{Symbol: "^DJI", Price: 47100, PreviousClose: 47000, MarketTime: time.Date(2026, 11, 25, 21, 32, 17, 0, time.UTC)},
	}
	f := &fakeIntraday{bars: map[string]*yahoochart.Intraday{}}
	for _, q := range quotes {
		d := session(q.Symbol, time.Date(2026, 11, 25, 9, 30, 0, 0, ny), time.Date(2026, 11, 25, 15, 55, 0, 0, ny))
		d.SessionEnd = closeAt
		f.bars[q.Symbol] = d
	}
	got := (&LLMGenerator{Intraday: f}).series(context.Background(), Due{TradingDate: "2026-11-25", Slot: SlotEvening}, at, quotes)
	for _, q := range quotes {
		s := got[q.Symbol]
		if s == nil {
			t.Errorf("%s dropped", q.Symbol)
			continue
		}
		last := s.Last()
		if !last.Time.Equal(closeAt) || last.Value != q.Price {
			t.Errorf("%s last %v %v, want %v %v", q.Symbol, last.Time.In(ny), last.Value, closeAt, q.Price)
		}
		for _, p := range s.Points {
			if p.Time.After(closeAt) {
				t.Errorf("%s point %v after the close", q.Symbol, p.Time.In(ny))
			}
		}
		if pct := (last.Value/q.PreviousClose - 1) * 100; pct != q.ChangePercent() {
			t.Errorf("%s legend %v scorecard %v", q.Symbol, pct, q.ChangePercent())
		}
	}
}

func TestCashSeriesSessionEnd(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	evening := func(t *testing.T, at time.Time, q yahoochart.Quote, d *yahoochart.Intraday) *yahoochart.Intraday {
		t.Helper()
		due := Due{TradingDate: LocalDate(at), Slot: SlotEvening}
		f := &fakeIntraday{bars: map[string]*yahoochart.Intraday{q.Symbol: d}}
		return (&LLMGenerator{Intraday: f}).series(context.Background(), due, at, []yahoochart.Quote{q})[q.Symbol]
	}
	ndx := func(stamped time.Time) yahoochart.Quote {
		return yahoochart.Quote{Symbol: "^NDX", Price: 25100, PreviousClose: 25000, MarketTime: stamped}
	}

	t.Run("half day ends at Yahoo's session end", func(t *testing.T) {
		at := time.Date(2026, 11, 27, 22, 31, 0, 0, time.UTC)
		halfClose := time.Date(2026, 11, 27, 13, 0, 0, 0, ny)
		d := session("^NDX", time.Date(2026, 11, 27, 9, 30, 0, 0, ny), time.Date(2026, 11, 27, 12, 55, 0, 0, ny))
		d.SessionEnd = halfClose
		s := evening(t, at, ndx(time.Date(2026, 11, 27, 13, 20, 0, 0, ny)), d)
		if s == nil {
			t.Fatal("dropped")
		}
		if last := s.Last(); !last.Time.Equal(halfClose) || last.Value != 25100 {
			t.Fatalf("last %v %v", last.Time.In(ny), last.Value)
		}
	})
	t.Run("static close without a session end", func(t *testing.T) {
		at := time.Date(2026, 11, 25, 22, 31, 0, 0, time.UTC)
		closeAt := time.Date(2026, 11, 25, 16, 0, 0, 0, ny)
		for name, sessionEnd := range map[string]time.Time{
			"absent":                 {},
			"before the first bar":   time.Date(2026, 11, 24, 16, 0, 0, 0, ny),
			"after the brief's time": at.Add(time.Hour),
		} {
			d := session("^NDX", time.Date(2026, 11, 25, 9, 30, 0, 0, ny), time.Date(2026, 11, 25, 15, 55, 0, 0, ny))
			d.SessionEnd = sessionEnd
			s := evening(t, at, ndx(time.Date(2026, 11, 25, 17, 15, 0, 0, ny)), d)
			if s == nil {
				t.Errorf("%s: dropped", name)
				continue
			}
			if last := s.Last(); !last.Time.Equal(closeAt) {
				t.Errorf("%s: last %v", name, last.Time.In(ny))
			}
		}
	})
	t.Run("cash quote before the close ends at the quote", func(t *testing.T) {
		berlin, _ := time.LoadLocation("Europe/Berlin")
		at := time.Date(2026, 11, 26, 8, 25, 0, 0, time.UTC)
		q := yahoochart.Quote{Symbol: "^GDAXI", Price: 25200, PreviousClose: 25000, MarketTime: time.Date(2026, 11, 26, 9, 25, 0, 0, berlin)}
		d := session("^GDAXI", time.Date(2026, 11, 26, 9, 0, 0, 0, berlin), time.Date(2026, 11, 26, 9, 20, 0, 0, berlin))
		d.SessionEnd = time.Date(2026, 11, 26, 17, 30, 0, 0, berlin)
		f := &fakeIntraday{bars: map[string]*yahoochart.Intraday{"^GDAXI": d}}
		s := (&LLMGenerator{Intraday: f}).series(context.Background(), Due{TradingDate: "2026-11-26", Slot: SlotMorning}, at, []yahoochart.Quote{q})["^GDAXI"]
		if s == nil || len(s.Points) != 6 {
			t.Fatalf("%+v", s)
		}
		if last := s.Last(); !last.Time.Equal(q.MarketTime) || last.Value != q.Price {
			t.Fatalf("last %v %v", last.Time.In(berlin), last.Value)
		}
	})
}

// Every skipped symbol is logged with why.
func TestSeriesDropsAreLogged(t *testing.T) {
	at := time.Date(2026, 11, 25, 22, 31, 0, 0, time.UTC)
	ny, _ := time.LoadLocation("America/New_York")
	stamped := time.Date(2026, 11, 25, 16, 32, 0, 0, ny)
	quotes := []yahoochart.Quote{
		{Symbol: "^GSPC", Price: 2, PreviousClose: 1, MarketTime: stamped},
		{Symbol: "^DJI", Price: 2, PreviousClose: 1, MarketTime: stamped},
	}
	f := &fakeIntraday{bars: map[string]*yahoochart.Intraday{
		"^NDX":  bars("^NDX", time.Date(2026, 11, 25, 15, 0, 0, 0, ny)),
		"^GSPC": bars("^GSPC", time.Date(2026, 11, 25, 14, 0, 0, 0, ny)), // two hours short of the close
	}}
	var buf bytes.Buffer
	g := &LLMGenerator{Intraday: f, Logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	if got := g.series(context.Background(), Due{TradingDate: "2026-11-25", Slot: SlotEvening}, at, quotes); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	reasons := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if rec["msg"] == "market_brief_series_dropped" {
			reasons[rec["symbol"].(string)] = rec
		}
	}
	for symbol, want := range map[string]string{"^NDX": "no_quote", "^GSPC": "gap", "^DJI": "no_bars"} {
		if rec := reasons[symbol]; rec == nil || rec["reason"] != want {
			t.Errorf("%s: %v, want reason %s (log %s)", symbol, rec, want, buf.String())
		}
	}
	if gap := reasons["^GSPC"]["gap"]; gap != float64(2*time.Hour) {
		t.Errorf("gap %v", gap)
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

func TestMissingReasons(t *testing.T) {
	// The Open at 08:25Z (Lisbon is on WET in November).
	at := time.Date(2026, 11, 26, 8, 25, 0, 0, time.UTC)
	due := Due{TradingDate: "2026-11-26", Slot: SlotMorning}
	usd := "USD"
	q := func(symbol string, stamped time.Time) yahoochart.Quote {
		return yahoochart.Quote{Symbol: symbol, Price: 2, PreviousClose: 1, MarketTime: stamped, Currency: &usd}
	}
	provided := []yahoochart.Quote{
		q("^GDAXI", time.Date(2026, 11, 26, 7, 59, 0, 0, time.UTC)),
		q("NQ=F", at.Add(-time.Minute)),
		q("^STOXX50E", time.Date(2026, 11, 25, 16, 30, 0, 0, time.UTC)),
	}
	kept, dropped := currentQuotes(provided, due)
	e := buildEdition(due, kept, dropped, nil, "m", false, at)
	want := map[string]string{
		"quote:^GDAXI":    ReasonPreOpen,
		"quote:^FCHI":     ReasonUnavailable,
		"quote:^STOXX50E": ReasonPreviousSession,
		"series:NQ=F":     ReasonNoBars,
	}
	for key, reason := range want {
		if e.Quality.MissingReasons[key] != reason {
			t.Errorf("%s: %q, want %q (%v)", key, e.Quality.MissingReasons[key], reason, e.Quality.MissingReasons)
		}
	}
	if len(e.Quality.MissingReasons) != len(e.Quality.Missing) {
		t.Fatalf("%v vs %v", e.Quality.Missing, e.Quality.MissingReasons)
	}
	for _, key := range e.Quality.Missing {
		if e.Quality.MissingReasons[key] == "" {
			t.Errorf("%s has no reason", key)
		}
	}
	// A series that arrived without points has no bars either.
	empty := buildEdition(due, kept, dropped, map[string]*yahoochart.Intraday{"ES=F": {Symbol: "ES=F", Interval: "5m"}}, "m", false, at)
	if r := empty.Quality.MissingReasons["series:ES=F"]; r != ReasonNoBars {
		t.Errorf("empty series: %q", r)
	}
	// Nothing missing serializes as before.
	raw, _ := json.Marshal(EditionQuality{Missing: []string{}})
	if strings.Contains(string(raw), "missingReasons") {
		t.Fatal(string(raw))
	}
}
