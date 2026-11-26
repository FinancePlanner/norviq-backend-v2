package marketbrief

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
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
	f := buildFacts(SlotEvening, "2026-11-20", []yahoochart.Quote{q, eveningQuote("^NDX", 25029.33, 26519.65)}, nil, nil, nil, eveningAt)
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
	e := buildEdition(due, quotes, nil, series, nil, "claude+web_search", false, nil, eveningAt)
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
	e := buildEdition(Due{TradingDate: "2026-11-20", Slot: SlotEvening}, []yahoochart.Quote{eveningQuote("^NDX", 2, 1)}, nil, nil, nil, "m", true, nil, eveningAt)
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

// The real ^GSPC session of 25 Nov: its last bar is stamped 16:00 ET, the
// quote 16:32 ET. The series ends once at the close, on the quote's price.
func TestCloseSeriesAgainstRealGSPCSession(t *testing.T) {
	raw, err := os.ReadFile("../../platform/yahoochart/testdata/intraday_1d_5m_GSPC_close_2026-11-25.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := yahoochart.ParseIntraday(raw, yahoochart.IntradayInterval)
	if err != nil || d == nil {
		t.Fatalf("%v %v", d, err)
	}
	var env struct {
		Chart struct {
			Result []struct {
				Meta struct {
					RegularMarketPrice float64 `json:"regularMarketPrice"`
					ChartPreviousClose float64 `json:"chartPreviousClose"`
				} `json:"meta"`
			} `json:"result"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Chart.Result) != 1 {
		t.Fatalf("meta: %v", err)
	}
	meta := env.Chart.Result[0].Meta
	q := yahoochart.Quote{
		Symbol: "^GSPC", Price: meta.RegularMarketPrice, PreviousClose: meta.ChartPreviousClose,
		MarketTime: time.Date(2026, 11, 25, 21, 32, 6, 0, time.UTC),
	}
	if q.Price != 7182.78 || q.PreviousClose != 7201.09 {
		t.Fatalf("fixture meta %+v", meta)
	}
	ny, _ := time.LoadLocation("America/New_York")
	closeAt := time.Date(2026, 11, 25, 16, 0, 0, 0, ny)
	at := time.Date(2026, 11, 25, 22, 31, 0, 0, time.UTC)
	f := &fakeIntraday{bars: map[string]*yahoochart.Intraday{"^GSPC": d}}
	s := (&LLMGenerator{Intraday: f}).series(context.Background(), Due{TradingDate: "2026-11-25", Slot: SlotEvening}, at, []yahoochart.Quote{q})["^GSPC"]
	if s == nil {
		t.Fatal("dropped")
	}
	if last := s.Last(); !last.Time.Equal(closeAt) || last.Value != q.Price {
		t.Fatalf("last %v %v, want %v %v", last.Time.In(ny), last.Value, closeAt, q.Price)
	}
	atClose := 0
	for _, p := range s.Points {
		if p.Time.After(closeAt) {
			t.Errorf("point %v after the close", p.Time.In(ny))
		}
		if p.Time.Equal(closeAt) {
			atClose++
		}
	}
	if atClose != 1 {
		t.Fatalf("%d points at 16:00 ET, want 1", atClose)
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
	t.Run("verified close beats a later reported end", func(t *testing.T) {
		// Yahoo's regular.end wrongly says 16:00 on the NYSE half day; NYSE
		// published 13:00. From 16:00 the 14:15 quote trails the 12:55 bar by
		// 80 minutes and the series would be dropped.
		at := time.Date(2026, 11, 27, 22, 31, 0, 0, time.UTC)
		halfClose := time.Date(2026, 11, 27, 13, 0, 0, 0, ny)
		d := session("^NDX", time.Date(2026, 11, 27, 9, 30, 0, 0, ny), time.Date(2026, 11, 27, 12, 55, 0, 0, ny))
		d.SessionEnd = time.Date(2026, 11, 27, 16, 0, 0, 0, ny)
		s := evening(t, at, ndx(time.Date(2026, 11, 27, 14, 15, 0, 0, ny)), d)
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
	e := buildEdition(due, kept, dropped, nil, nil, "m", false, nil, at)
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
	empty := buildEdition(due, kept, dropped, map[string]*yahoochart.Intraday{"ES=F": {Symbol: "ES=F", Interval: "5m"}}, nil, "m", false, nil, at)
	if r := empty.Quality.MissingReasons["series:ES=F"]; r != ReasonNoBars {
		t.Errorf("empty series: %q", r)
	}
	// Nothing missing serializes as before.
	raw, _ := json.Marshal(EditionQuality{Missing: []string{}})
	if strings.Contains(string(raw), "missingReasons") {
		t.Fatal(string(raw))
	}
}

func usQuote(symbol string, stamped time.Time) yahoochart.Quote {
	usd := "USD"
	return yahoochart.Quote{Symbol: symbol, Price: 2, PreviousClose: 1, MarketTime: stamped, Currency: &usd}
}

func TestThanksgivingOpen(t *testing.T) {
	at := time.Date(2026, 11, 26, 8, 25, 0, 0, time.UTC)
	due := Due{TradingDate: "2026-11-26", Slot: SlotMorning}
	today := func(symbol string) yahoochart.Quote { return usQuote(symbol, at.Add(-15*time.Minute)) }
	// Context rows keep the 18 h rule, so ^TNX and Brent still carry
	// Wednesday's session: its day range and volume must not ride along.
	vol := int64(5000)
	wednesday := func(symbol string) yahoochart.Quote {
		q := usQuote(symbol, time.Date(2026, 11, 25, 19, 59, 0, 0, time.UTC))
		q.DayLow, q.DayHigh, q.YearLow, q.YearHigh, q.Volume = ptrF(1.9), ptrF(2.1), ptrF(1), ptrF(5), &vol
		return q
	}
	dax := today("^GDAXI")
	dax.DayLow, dax.DayHigh = ptrF(1.9), ptrF(2.1)
	provided := []yahoochart.Quote{
		dax, today("^FCHI"), today("NQ=F"), today("ES=F"), today("^N225"), today("^HSI"),
		wednesday("^TNX"), wednesday("BZ=F"),
	}
	kept, dropped := currentQuotes(provided, due)
	sessions := sessionsFor(due.Slot, due.TradingDate)
	e := buildEdition(due, kept, dropped, nil, sessions, "m", false, nil, at)
	want := []MarketSession{
		{Market: "nyse", Status: SessionClosed, Name: "Thanksgiving Day"},
		{Market: "sifma", Status: SessionClosed, Name: "Thanksgiving Day"},
	}
	if !reflect.DeepEqual(e.MarketSessions, want) {
		t.Fatalf("sessions %+v", e.MarketSessions)
	}
	rows := map[string]EditionInstrument{}
	for _, row := range e.Instruments {
		rows[row.Symbol] = row
	}
	tnx, foundTNX := rows["^TNX"]
	if !foundTNX {
		t.Fatal("^TNX row dropped, the 18 h rule keeps it")
	}
	if tnx.DayLow != nil || tnx.DayHigh != nil || tnx.Volume != nil {
		t.Errorf("^TNX carries the previous session's day fields: %+v", tnx)
	}
	if tnx.YearLow == nil || tnx.YearHigh == nil {
		t.Errorf("^TNX 52-week range is not session-bound: %+v", tnx)
	}
	if tnx.Session == nil || *tnx.Session != SessionClosed {
		t.Errorf("^TNX session %v, want closed", tnx.Session)
	}
	if brent := rows["BZ=F"]; brent.Volume != nil || brent.DayLow != nil {
		t.Errorf("BZ=F carries the previous session's day fields: %+v", brent)
	}
	if d := rows["^GDAXI"]; d.DayLow == nil || d.DayHigh == nil {
		t.Errorf("same-day ^GDAXI keeps its day range: %+v", d)
	}
	for key, reason := range e.Quality.MissingReasons {
		if reason == ReasonHoliday {
			t.Errorf("%s: unexpected holiday reason", key)
		}
	}
	if r := e.Quality.MissingReasons["series:NQ=F"]; r != ReasonNoBars {
		t.Errorf("NQ=F series: %q (contextMarket must not produce holiday)", r)
	}
	facts := buildFacts(due.Slot, due.TradingDate, kept, nil, nil, sessions, at)
	if !reflect.DeepEqual(facts.Sessions, want) {
		t.Fatalf("facts sessions %+v", facts.Sessions)
	}
}

func TestThanksgivingClose(t *testing.T) {
	at := time.Date(2026, 11, 26, 22, 31, 0, 0, time.UTC)
	due := Due{TradingDate: "2026-11-26", Slot: SlotEvening}
	wednesday := time.Date(2026, 11, 25, 21, 15, 0, 0, time.UTC)
	today := at.Add(-time.Hour)
	provided := []yahoochart.Quote{
		usQuote("^NDX", wednesday), usQuote("^GSPC", wednesday), usQuote("^DJI", wednesday),
		usQuote("^GDAXI", today), usQuote("^FCHI", today), usQuote("^STOXX50E", today),
	}
	kept, dropped := currentQuotes(provided, due)
	if dropped["^NDX"] != ReasonPreviousSession {
		t.Fatalf("dropped %v", dropped)
	}
	e := buildEdition(due, kept, dropped, nil, sessionsFor(due.Slot, due.TradingDate), "m", false, nil, at)
	for _, symbol := range []string{"^NDX", "^GSPC", "^DJI"} {
		for _, key := range []string{"quote:" + symbol, "series:" + symbol} {
			if r := e.Quality.MissingReasons[key]; r != ReasonHoliday {
				t.Errorf("%s: %q, want holiday", key, r)
			}
		}
	}
	for _, key := range []string{"quote:^GDAXI", "quote:^FCHI", "series:^GDAXI"} {
		if r := e.Quality.MissingReasons[key]; r == ReasonHoliday {
			t.Errorf("%s: EU rows are unaffected", key)
		}
	}
}

func TestHalfDaySeriesEndsAtVerifiedClose(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	at := time.Date(2026, 11, 27, 22, 31, 0, 0, time.UTC)
	// The quote is 14:15 ET, so only the verified 13:00 close keeps the
	// series: from the static 16:00 close the 12:55 bar trails it by 80
	// minutes, past maxSeriesGap.
	series := func(day int) *yahoochart.Intraday {
		dayAt := time.Date(2026, 11, day, 22, 31, 0, 0, time.UTC)
		q := yahoochart.Quote{Symbol: "^GSPC", Price: 6900, PreviousClose: 6800, MarketTime: time.Date(2026, 11, day, 14, 15, 0, 0, ny)}
		d := session("^GSPC", time.Date(2026, 11, day, 9, 30, 0, 0, ny), time.Date(2026, 11, day, 12, 55, 0, 0, ny))
		f := &fakeIntraday{bars: map[string]*yahoochart.Intraday{"^GSPC": d}}
		due := Due{TradingDate: dayAt.Format(time.DateOnly), Slot: SlotEvening}
		return (&LLMGenerator{Intraday: f}).series(context.Background(), due, dayAt, []yahoochart.Quote{q})["^GSPC"]
	}
	s := series(27)
	if s == nil {
		t.Fatal("dropped")
	}
	if last := s.Last(); !last.Time.Equal(time.Date(2026, 11, 27, 13, 0, 0, 0, ny)) || last.Value != 6900 {
		t.Fatalf("last %v %v", last.Time.In(ny), last.Value)
	}
	// Without a verified time the static 16:00 close applies, and the gap
	// rule drops the same bars.
	end, ok := seriesSessionEnd("^GSPC", time.Time{}, time.Date(2026, 11, 25, 9, 30, 0, 0, ny), at)
	if !ok || !end.Equal(time.Date(2026, 11, 25, 16, 0, 0, 0, ny)) {
		t.Fatalf("static end %v %v", end, ok)
	}
	if s := series(25); s != nil {
		t.Fatalf("regular day kept a series 80 minutes short: last %v", s.Last().Time.In(ny))
	}
}

func TestRegularDayHasNoSessions(t *testing.T) {
	if got := sessionsFor(SlotEvening, "2026-11-25"); got != nil {
		t.Fatalf("%+v", got)
	}
	e := buildEdition(Due{TradingDate: "2026-11-25", Slot: SlotEvening}, nil, nil, nil, nil, "m", false, nil, eveningAt)
	raw, _ := json.Marshal(e)
	f, _ := json.Marshal(buildFacts(SlotEvening, "2026-11-25", nil, nil, nil, nil, eveningAt))
	if strings.Contains(string(raw), "marketSessions") || strings.Contains(string(f), "sessions") {
		t.Fatalf("%s %s", raw, f)
	}
}

func TestCalendarExpiringIsLoggedOncePerGenerate(t *testing.T) {
	var buf bytes.Buffer
	g := newGenerator(nil, nil, &scriptedClient{replies: []any{draftJSON(3)}})
	g.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	g.Now = func() time.Time { return time.Date(2026, 11, 26, 8, 25, 0, 0, time.UTC) }
	brief, err := g.Generate(context.Background(), Due{TradingDate: "2026-11-26", Slot: SlotMorning})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), `"msg":"market_brief_calendar_expiring"`); n != len(calendarExpiring("2026-11-26")) || n == 0 {
		t.Fatalf("%d lines: %s", n, buf.String())
	}
	if !strings.Contains(buf.String(), `"market":"sifma"`) || !strings.Contains(buf.String(), `"date":"2026-11-26"`) {
		t.Fatal(buf.String())
	}
	if len(brief.Edition.MarketSessions) != 2 {
		t.Fatalf("%+v", brief.Edition.MarketSessions)
	}
}

func ptrF(v float64) *float64 { return &v }

func rangeRow(t *testing.T, q yahoochart.Quote) EditionInstrument {
	t.Helper()
	e := buildEdition(Due{TradingDate: "2026-11-20", Slot: SlotEvening}, []yahoochart.Quote{q}, nil, nil, nil, "m", false, nil, eveningAt)
	if len(e.Instruments) != 1 {
		t.Fatalf("rows %+v", e.Instruments)
	}
	return e.Instruments[0]
}

func TestInstrumentRangesDroppedWhenInconsistent(t *testing.T) {
	base := func() yahoochart.Quote { return eveningQuote("^NDX", 100, 99) }

	q := base()
	q.DayLow, q.DayHigh, q.YearLow, q.YearHigh = ptrF(90.004), ptrF(110.006), ptrF(50), ptrF(150)
	row := rangeRow(t, q)
	if row.DayLow == nil || *row.DayLow != 90 || row.DayHigh == nil || *row.DayHigh != 110.01 {
		t.Fatalf("day range kept and rounded: %+v", row)
	}
	if row.YearLow == nil || *row.YearLow != 50 || row.YearHigh == nil || *row.YearHigh != 150 {
		t.Fatalf("year range: %+v", row)
	}

	// last outside the day range: it belongs to another session. The 52w
	// range is judged on its own.
	q = base()
	q.DayLow, q.DayHigh, q.YearLow, q.YearHigh = ptrF(101), ptrF(110), ptrF(50), ptrF(150)
	row = rangeRow(t, q)
	if row.DayLow != nil || row.DayHigh != nil {
		t.Fatalf("day range must drop: %+v", row)
	}
	if row.YearLow == nil || row.YearHigh == nil {
		t.Fatalf("year range must stay: %+v", row)
	}

	// last above the day range
	q = base()
	q.DayLow, q.DayHigh = ptrF(80), ptrF(95)
	if row = rangeRow(t, q); row.DayLow != nil || row.DayHigh != nil {
		t.Fatalf("last above the day range: %+v", row)
	}

	// low above high
	q = base()
	q.DayLow, q.DayHigh = ptrF(110), ptrF(90)
	if row = rangeRow(t, q); row.DayLow != nil || row.DayHigh != nil {
		t.Fatalf("inverted range: %+v", row)
	}

	// one end only
	q = base()
	q.DayHigh, q.YearLow = ptrF(110), ptrF(50)
	if row = rangeRow(t, q); row.DayHigh != nil || row.YearLow != nil {
		t.Fatalf("half range: %+v", row)
	}

	// 52w range excluding last
	q = base()
	q.YearLow, q.YearHigh = ptrF(120), ptrF(150)
	if row = rangeRow(t, q); row.YearLow != nil || row.YearHigh != nil {
		t.Fatalf("year range excluding last: %+v", row)
	}

	// volume
	q = base()
	raw, _ := json.Marshal(rangeRow(t, q))
	for _, k := range []string{"volume", "dayLow", "dayHigh", "yearLow", "yearHigh", "session"} {
		if strings.Contains(string(raw), `"`+k+`"`) {
			t.Errorf("%s must be absent: %s", k, raw)
		}
	}
	// An index's volume is opaque (and zero for EU indices): never shown.
	vol := int64(8281)
	q.Volume = &vol
	if row = rangeRow(t, q); row.Volume != nil {
		t.Fatalf("index volume: %+v", row)
	}
	brent := eveningQuote("BZ=F", 100, 99)
	brent.Volume = &vol
	if row = rangeRow(t, brent); row.Volume == nil || *row.Volume != 8281 {
		t.Fatalf("commodity volume: %+v", row)
	}
}

// Volume is contracts traded: kept for futures and commodities only.
func TestVolumeOnlyForFuturesAndCommodities(t *testing.T) {
	vol := int64(3_465_619_000)
	gspc := eveningQuote("^GSPC", 100, 99)
	gspc.Volume = &vol
	if r := rangeRow(t, gspc); r.Volume != nil {
		t.Errorf("^GSPC volume: %+v", r)
	}

	at := time.Date(2026, 11, 20, 8, 25, 0, 0, time.UTC)
	nq := usQuote("NQ=F", at.Add(-15*time.Minute))
	nq.Volume = &vol
	e := buildEdition(Due{TradingDate: "2026-11-20", Slot: SlotMorning}, []yahoochart.Quote{nq}, nil, nil, nil, "m", false, nil, at)
	if len(e.Instruments) != 1 || e.Instruments[0].Volume == nil || *e.Instruments[0].Volume != vol {
		t.Errorf("NQ=F volume: %+v", e.Instruments)
	}
}

func TestInstrumentSessionTag(t *testing.T) {
	quotes := func(at time.Time, symbols ...string) []yahoochart.Quote {
		var out []yahoochart.Quote
		for _, s := range symbols {
			out = append(out, usQuote(s, at.Add(-90*time.Minute)))
		}
		return out
	}
	byRow := func(e Edition) map[string]*string {
		m := map[string]*string{}
		for _, r := range e.Instruments {
			m[r.Symbol] = r.Session
		}
		return m
	}

	at := time.Date(2026, 11, 27, 22, 0, 0, 0, time.UTC)
	due := Due{TradingDate: "2026-11-27", Slot: SlotEvening}
	e := buildEdition(due, quotes(at, "^GSPC", "^GDAXI"), nil, nil, sessionsFor(due.Slot, due.TradingDate), "m", false, nil, at)
	got := byRow(e)
	if got["^GSPC"] == nil || *got["^GSPC"] != SessionEarlyClose {
		t.Errorf("^GSPC: %v", got["^GSPC"])
	}
	if got["^GDAXI"] != nil {
		t.Errorf("^GDAXI: %v", *got["^GDAXI"])
	}

	// NQ=F has only a contextMarket (nyse), which never tags a row.
	at = time.Date(2026, 11, 26, 8, 25, 0, 0, time.UTC)
	due = Due{TradingDate: "2026-11-26", Slot: SlotMorning}
	e = buildEdition(due, quotes(at, "NQ=F"), nil, nil, sessionsFor(due.Slot, due.TradingDate), "m", false, nil, at)
	if got = byRow(e); got["NQ=F"] != nil {
		t.Errorf("NQ=F: %v", *got["NQ=F"])
	}

	// a regular day tags nothing
	e = buildEdition(Due{TradingDate: "2026-11-20", Slot: SlotEvening}, quotes(eveningAt, "^GSPC"), nil, nil, nil, "m", false, nil, eveningAt)
	if got = byRow(e); got["^GSPC"] != nil {
		t.Errorf("regular day: %v", *got["^GSPC"])
	}
}
