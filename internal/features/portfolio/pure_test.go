package portfolio

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/fx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
)

// MARK: - PortfolioPerformanceBuilderTests

var day0 = time.Unix(1_710_460_800, 0).UTC() // 2024-03-15

func dayAt(offset int) time.Time { return AddDays(day0, offset) }

func mkDay(offset int, value float64) PerformanceDay {
	return PerformanceDay{Date: dayAt(offset), TotalValue: value, CostBasis: 1000}
}

func mkSnap(user, list uuid.UUID, offset int, mv, cash float64, source string) portfoliodb.PortfolioValueSnapshot {
	return portfoliodb.PortfolioValueSnapshot{
		ID: uuid.New(), UserID: user, PortfolioListID: list, CapturedOn: dayAt(offset), Currency: "USD",
		MarketValue: mv, CostBasis: 1000, CashBalance: cash, PositionCount: 1, Source: source, PricedSymbols: 1,
	}
}

func near(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestPerformanceSingleOrNoDayHasNoChanges(t *testing.T) {
	if PerformanceChanges([]PerformanceDay{mkDay(0, 1000)}, dayAt(0)) != nil {
		t.Fatal("single day produced changes")
	}
	if PerformanceChanges(nil, dayAt(0)) != nil {
		t.Fatal("no history produced changes")
	}
}

func TestPerformanceLongWindowsOmittedAndDayChangeReal(t *testing.T) {
	days := []PerformanceDay{mkDay(-1, 1000), mkDay(0, 996)}
	c := PerformanceChanges(days, dayAt(0))
	if c == nil || c.Day == nil || c.SinceInception == nil || c.Week != nil || c.Month != nil {
		t.Fatalf("changes %+v", c)
	}
	d := c.Day
	if !near(d.Percent, -0.004, 1e-6) || d.Absolute != -4 || d.Basis != BasisPreviousTradingDay ||
		d.FromDate != "2024-03-14" || d.ToDate != "2024-03-15" {
		t.Fatalf("day %+v", d)
	}
	if again := PerformanceChanges(days, dayAt(0)); *again.Day != *d {
		t.Fatal("not deterministic")
	}
}

func TestPerformanceDayChangeSkipsClosedDays(t *testing.T) {
	c := PerformanceChanges([]PerformanceDay{mkDay(0, 1000), mkDay(3, 1100)}, dayAt(3))
	if c.Day.FromDate != "2024-03-15" || c.Day.ToDate != "2024-03-18" || !near(c.Day.Percent, 0.1, 1e-6) {
		t.Fatalf("%+v", c.Day)
	}
}

func TestPerformanceWeeklyUsesNearestEarlierDay(t *testing.T) {
	days := []PerformanceDay{mkDay(-10, 900), mkDay(-8, 950), mkDay(-3, 1050), mkDay(0, 1000)}
	w := PerformanceChanges(days, dayAt(0)).Week
	if w == nil || w.FromDate != "2024-03-07" || w.ToDate != "2024-03-15" || !near(w.Percent, (1000.0-950)/950, 1e-6) {
		t.Fatalf("%+v", w)
	}
}

func TestPerformanceSinceInceptionAndZeroBaseline(t *testing.T) {
	c := PerformanceChanges([]PerformanceDay{mkDay(-30, 500), mkDay(-10, 800), mkDay(0, 1000)}, dayAt(0))
	i := c.SinceInception
	if i.FromDate != "2024-02-14" || !near(i.Percent, 1, 1e-6) || i.Absolute != 500 {
		t.Fatalf("%+v", i)
	}
	if z := PerformanceChanges([]PerformanceDay{mkDay(-1, 0), mkDay(0, 1000)}, dayAt(0)); z != nil && z.Day != nil {
		t.Fatal("zero baseline produced a day change")
	}
}

func TestPerformanceYTDFallsBackToFirstDayOfYear(t *testing.T) {
	jan := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	days := []PerformanceDay{{Date: jan, TotalValue: 800}, mkDay(-1, 900), mkDay(0, 1000)}
	y := PerformanceChanges(days, dayAt(0)).YTD
	if y == nil || y.FromDate != "2024-01-03" || y.Basis != BasisYTD {
		t.Fatalf("%+v", y)
	}
	dec := time.Date(2023, 12, 29, 0, 0, 0, 0, time.UTC)
	days = append([]PerformanceDay{{Date: dec, TotalValue: 700}}, days...)
	if y := PerformanceChanges(days, dayAt(0)).YTD; y.FromDate != "2023-12-29" {
		t.Fatalf("baseline before January: %+v", y)
	}
}

func TestPerformanceDaysAggregation(t *testing.T) {
	u, a, b := uuid.New(), uuid.New(), uuid.New()
	// Summed across lists.
	days := PerformanceDays([]portfoliodb.PortfolioValueSnapshot{
		mkSnap(u, a, 0, 1000, 100, SourceLive), mkSnap(u, b, 0, 500, 0, SourceLive),
	}, []uuid.UUID{a, b})
	if len(days) != 1 || days[0].TotalValue != 1600 {
		t.Fatalf("sum %+v", days)
	}
	// A day missing one list's row is dropped.
	days = PerformanceDays([]portfoliodb.PortfolioValueSnapshot{
		mkSnap(u, a, -1, 1000, 0, SourceLive), mkSnap(u, b, -1, 500, 0, SourceLive), mkSnap(u, a, 0, 1010, 0, SourceLive),
	}, []uuid.UUID{a, b})
	if len(days) != 1 || days[0].TotalValue != 1500 {
		t.Fatalf("partial day %+v", days)
	}
	// Days before a list existed count the ones that did.
	days = PerformanceDays([]portfoliodb.PortfolioValueSnapshot{
		mkSnap(u, a, -5, 1000, 0, SourceLive), mkSnap(u, a, 0, 1200, 0, SourceLive), mkSnap(u, b, 0, 300, 0, SourceLive),
	}, []uuid.UUID{a, b})
	if len(days) != 2 || days[0].TotalValue != 1000 || days[1].TotalValue != 1500 {
		t.Fatalf("pre-existence %+v", days)
	}
	// A trailing gap is a missing capture.
	days = PerformanceDays([]portfoliodb.PortfolioValueSnapshot{
		mkSnap(u, a, -2, 1000, 0, SourceLive), mkSnap(u, b, -2, 400, 0, SourceLive), mkSnap(u, a, -1, 1010, 0, SourceLive),
	}, []uuid.UUID{a, b})
	if len(days) != 1 || days[0].TotalValue != 1400 {
		t.Fatalf("trailing gap %+v", days)
	}
	// A list outside listIDs does not hold the series back.
	days = PerformanceDays([]portfoliodb.PortfolioValueSnapshot{
		mkSnap(u, a, -2, 1000, 0, SourceLive), mkSnap(u, b, -2, 400, 0, SourceLive),
		mkSnap(u, a, -1, 1010, 0, SourceLive), mkSnap(u, a, 0, 1020, 0, SourceLive),
	}, []uuid.UUID{a})
	if len(days) != 3 || days[2].TotalValue != 1020 {
		t.Fatalf("archived %+v", days)
	}
	// Backfilled when any contributing row is.
	days = PerformanceDays([]portfoliodb.PortfolioValueSnapshot{
		mkSnap(u, a, 0, 1000, 0, SourceLive), mkSnap(u, b, 0, 500, 0, SourceBackfill),
	}, []uuid.UUID{a, b})
	if !days[0].IsBackfilled {
		t.Fatal("not marked backfilled")
	}
}

func TestPerformanceResponseWindowsPointsOnly(t *testing.T) {
	var days []PerformanceDay
	for o := 0; o <= 60; o++ {
		days = append(days, mkDay(-60+o, 1000+float64(o)))
	}
	r := BuildPerformanceResponse(days, RangeOneWeek, "USD", dayAt(0))
	if len(r.Points) > 8 || *r.Range != "1W" || r.Changes == nil || r.Changes.Month == nil {
		t.Fatalf("%d points, %+v", len(r.Points), r.Changes)
	}
	days = nil
	for o := 0; o <= 40; o++ {
		days = append(days, mkDay(-40+o, 1000))
	}
	if r := BuildPerformanceResponse(days, RangeAll, "USD", dayAt(0)); len(r.Points) != 41 {
		t.Fatalf("ALL %d", len(r.Points))
	}
}

func TestParsePerformanceRange(t *testing.T) {
	s := func(v string) *string { return &v }
	if ParsePerformanceRange(s("banana")) != RangeDefault || ParsePerformanceRange(nil) != RangeDefault ||
		ParsePerformanceRange(s("1w")) != RangeOneWeek || ParsePerformanceRange(s(" 3M ")) != RangeThreeMonths {
		t.Fatal("range parsing")
	}
}

func TestPerformanceEmptyAndProvenance(t *testing.T) {
	r := BuildPerformanceResponse(nil, RangeOneMonth, "USD", dayAt(0))
	if len(r.Points) != 0 || r.Changes != nil || r.AsOf != nil || r.BaseCurrency != "USD" || r.Points == nil {
		t.Fatalf("%+v", r)
	}
	days := []PerformanceDay{
		{Date: dayAt(-1), TotalValue: 1000, CostBasis: 900, IsBackfilled: true},
		{Date: dayAt(0), TotalValue: 1100, CostBasis: 900},
	}
	r = BuildPerformanceResponse(days, RangeAll, "USD", dayAt(0))
	if *r.Points[0].CostBasis != 900 || *r.Points[0].Source != SourceBackfill || *r.Points[1].Source != SourceLive || *r.AsOf != "2024-03-15" {
		t.Fatalf("%+v", r)
	}
}

// MARK: - PortfolioSnapshotValuatorTests

func stock(sym string, shares, price float64) portfoliodb.Stock {
	return portfoliodb.Stock{ID: uuid.New(), Symbol: sym, Shares: shares, BuyPrice: price}
}

// usd wraps raw prices as USD quotes.
func usd(m map[string]float64) map[string]Price {
	out := make(map[string]Price, len(m))
	for k, v := range m {
		out[k] = Price{Value: v, Currency: "USD", Factor: 1}
	}
	return out
}

// mustValuate is the flag-off Valuate: base USD, Passthrough rates.
func mustValuate(t *testing.T, stocks []portfoliodb.Stock, prices map[string]Price, cash float64) SnapshotValuation {
	t.Helper()
	v, err := Valuate(context.Background(), stocks, prices, cash, "USD", fx.Passthrough{}, time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func ccy(s string) *string { return &s }

func fxStock(sym string, shares, buy float64, currency *string) portfoliodb.Stock {
	st := stock(sym, shares, buy)
	st.Currency = currency
	return st
}

var fxFixture = []fx.DailyRate{
	{Date: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), Quote: "USD", Rate: "1.05"},
	{Date: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), Quote: "DKK", Rate: "7.46"},
}

func multiCurrencyFixture() ([]portfoliodb.Stock, map[string]Price) {
	stocks := []portfoliodb.Stock{
		fxStock("AAPL", 10, 100, nil),
		fxStock("NOVO", 5, 500, ccy("DKK")),
		fxStock("NOVO", 5, 500, nil),
		fxStock("SHOP", 1, 50, ccy("USD")),
		fxStock("VOD.L", 100, 1, nil),
	}
	prices := map[string]Price{
		"AAPL":  {200, "USD", 1},
		"NOVO":  {700, "DKK", 1},
		"SHOP":  {90, "CAD", 1},
		"VOD.L": {500, "GBP", 0.01},
	}
	return stocks, prices
}

func TestValuateMultiCurrency(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	stocks, prices := multiCurrencyFixture()

	v, err := Valuate(ctx, stocks, prices, 0, "USD", fx.Static(fxFixture), day)
	if err != nil {
		t.Fatal(err)
	}
	wantMV := 2000 + 10*700*(1.05/7.46)
	if v.MarketValue != round2(wantMV) {
		t.Errorf("market value %v want %v", v.MarketValue, round2(wantMV))
	}
	// Rows count: AAPL, NOVO x2 priced; SHOP and VOD.L missing.
	if v.PositionCount != 5 || v.PricedSymbols != 3 || v.MissingSymbols != 2 {
		t.Errorf("counts %+v", v)
	}
	if v.Currency != "USD" {
		t.Errorf("currency %q", v.Currency)
	}
	if v.FxAsOf == nil || !v.FxAsOf.Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("fx as of %v", v.FxAsOf)
	}
	if len(v.FxMissing) != 1 || v.FxMissing[0] != "GBP" {
		t.Errorf("fx missing %v", v.FxMissing)
	}
	if len(v.ListingMismatches) != 1 || v.ListingMismatches[0] != (ListingMismatch{Symbol: "SHOP", HoldingCurrency: "USD", QuoteCurrency: "CAD"}) {
		t.Errorf("mismatches %+v", v.ListingMismatches)
	}
	// Cost: AAPL 1000, both NOVO rows (5000 DKK, the merged holding currency)
	// converted, SHOP 50; VOD.L has no rate, so its cost is left out.
	wantCost := 1000 + 5000*(1.05/7.46) + 50
	if v.CostBasis != round2(wantCost) {
		t.Errorf("cost basis %v want %v", v.CostBasis, round2(wantCost))
	}

	// Flag off: raw sums, no guard, no factor, no conversion.
	off, err := Valuate(ctx, stocks, prices, 0, "USD", fx.Passthrough{}, day)
	if err != nil {
		t.Fatal(err)
	}
	if off.MarketValue != 2000+7000+90+50000 || off.PricedSymbols != 5 || off.MissingSymbols != 0 ||
		len(off.FxMissing) != 0 || off.FxAsOf != nil || off.Currency != "USD" || len(off.ListingMismatches) != 0 {
		t.Errorf("flag off %+v", off)
	}
}

func TestValuateRule(t *testing.T) {
	v := mustValuate(t, []portfoliodb.Stock{stock("AAPL", 10, 150), stock("NVDA", 4, 500)}, usd(map[string]float64{"AAPL": 200}), 0)
	if v.MarketValue != 2000 || v.CostBasis != 3500 || v.MissingSymbols != 1 || v.PricedSymbols != 1 || v.IsFullyPriced() {
		t.Fatalf("unpriced %+v", v)
	}
	v = mustValuate(t, []portfoliodb.Stock{stock("AAPL", 10, 150), stock("MSFT", 5, 300)}, usd(map[string]float64{"AAPL": 200, "MSFT": 400}), 0)
	if !v.IsFullyPriced() || v.MarketValue != 4000 {
		t.Fatalf("priced %+v", v)
	}
	v = mustValuate(t, []portfoliodb.Stock{stock("AAPL", 10, 150)}, usd(map[string]float64{"AAPL": 0}), 0)
	if v.MissingSymbols != 1 || v.MarketValue != 0 {
		t.Fatalf("zero price %+v", v)
	}
	v = mustValuate(t, []portfoliodb.Stock{stock(" aapl\n", 1, 1)}, usd(map[string]float64{"AAPL": 2}), 0)
	if v.PricedSymbols != 1 {
		t.Fatal("symbol normalization")
	}
	v = mustValuate(t, []portfoliodb.Stock{stock("AAPL", 1, 100), stock("AAPL", 2, 100)}, usd(map[string]float64{"AAPL": 110}), 0)
	if v.PositionCount != 2 || v.PricedSymbols != 2 || v.MarketValue != 330 {
		t.Fatalf("two rows %+v", v)
	}
	v = mustValuate(t, []portfoliodb.Stock{stock("AAPL", 3, 33.333)}, usd(map[string]float64{"AAPL": 10.004}), 12.345)
	if v.CostBasis != 100 || v.MarketValue != 30.01 || v.CashBalance != 12.35 {
		t.Fatalf("rounding %+v", v)
	}
	if !(SnapshotValuation{}).IsEmpty() || (SnapshotValuation{CashBalance: 5}).IsEmpty() {
		t.Fatal("isEmpty")
	}
}

func TestSnapshotHelpers(t *testing.T) {
	got := UniqueSymbols([]string{" aapl", "AAPL", "", "  ", "msft"})
	if strings.Join(got, ",") != "AAPL,MSFT" {
		t.Fatalf("%v", got)
	}
	tz := time.FixedZone("x", 14*3600)
	in := time.Date(2024, 3, 16, 5, 0, 0, 0, tz) // 2024-03-15T15:00Z
	if !StartOfDay(in).Equal(time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(StartOfDay(in))
	}
	if !AddDays(time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC), 1).Equal(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("month boundary")
	}
	if SnapshotInterval("") != time.Hour || SnapshotInterval("60") != 300*time.Second || SnapshotInterval("7200") != 2*time.Hour || SnapshotInterval(" 7200") != time.Hour {
		t.Fatal("interval")
	}
}

// MARK: - PortfolioValuationServiceTests

type stubQuotes struct {
	quotes map[string]marketdata.QuoteResponse
	fail   bool
}

func (s stubQuotes) QuoteBatch(_ context.Context, symbols []string) (marketdata.QuoteBatchResponse, error) {
	if s.fail {
		return marketdata.QuoteBatchResponse{}, errors.New("provider down")
	}
	out := marketdata.QuoteBatchResponse{Quotes: []marketdata.QuoteResponse{}}
	for _, sym := range symbols {
		if q, ok := s.quotes[strings.ToUpper(sym)]; ok {
			out.Quotes = append(out.Quotes, q)
		}
	}
	return out, nil
}

func fp(v float64) *float64 { return &v }

func TestValuationService(t *testing.T) {
	ctx := context.Background()
	svc := ValuationService{Quotes: stubQuotes{quotes: map[string]marketdata.QuoteResponse{
		"AAPL": {Symbol: "AAPL", Currency: "USD", CurrentPrice: 200, Change: fp(2), PercentChange: fp(1)},
		"MSFT": {Symbol: "MSFT", Currency: "USD", CurrentPrice: 400, Change: fp(0), PercentChange: fp(0)},
	}}}
	v := svc.Value(ctx, []portfoliodb.Stock{stock("AAPL", 10, 150)}, 0, time.Now())
	h := v.Holdings[0]
	if len(v.Holdings) != 1 || h.Symbol != "AAPL" || h.Shares != 10 || h.CostBasis != 1500 || h.AverageBuyPrice != 150 ||
		*h.CurrentPrice != 200 || h.MarketValue != 2000 || h.UnrealizedPnl != 500 || !near(*h.UnrealizedPnlPercent, 33.33, 0.01) ||
		*h.DayChange != 20 || *h.DayChangePercent != 1 || !h.HasLiveQuote || v.TotalValue != 2000 {
		t.Fatalf("live %+v", h)
	}

	v = svc.Value(ctx, []portfoliodb.Stock{stock("TSLA", 10, 200)}, 0, time.Now())
	h = v.Holdings[0]
	if h.CurrentPrice != nil || h.HasLiveQuote || h.CostBasis != 2000 || h.MarketValue != 2000 || h.UnrealizedPnl != 0 || h.DayChange != nil || h.DayChangePercent != nil {
		t.Fatalf("no quote %+v", h)
	}

	v = svc.Value(ctx, []portfoliodb.Stock{stock("AAPL", 10, 100), stock("aapl", 10, 200)}, 0, time.Now())
	if len(v.Holdings) != 1 || v.Holdings[0].Shares != 20 || v.Holdings[0].CostBasis != 3000 || v.Holdings[0].AverageBuyPrice != 150 || v.Holdings[0].MarketValue != 4000 {
		t.Fatalf("merge %+v", v.Holdings)
	}

	v = svc.Value(ctx, nil, 500, time.Now())
	if len(v.Holdings) != 0 || v.HoldingsMarketValue != 0 || v.TotalCost != 0 || v.UnrealizedPnl != 0 || v.UnrealizedPnlPercent != nil ||
		v.DayChange != 0 || v.CashBalance != 500 || v.TotalValue != 500 || *v.DayChangePercent != 0 {
		t.Fatalf("empty %+v", v)
	}

	v = svc.Value(ctx, []portfoliodb.Stock{stock("AAPL", 10, 150), stock("MSFT", 10, 300)}, 0, time.Now())
	if v.DayChange != 20 || v.TotalValue != 6000 || !near(*v.DayChangePercent, 20.0/5980*100, 1e-4) || v.Holdings[0].Symbol != "MSFT" {
		t.Fatalf("day change %+v", v)
	}

	down := ValuationService{Quotes: stubQuotes{fail: true}}
	v = down.Value(ctx, []portfoliodb.Stock{stock("NVDA", 5, 150)}, 100, time.Now())
	h = v.Holdings[0]
	if h.CurrentPrice != nil || h.HasLiveQuote || h.MarketValue != 750 || h.UnrealizedPnl != 0 || v.TotalValue != 850 {
		t.Fatalf("outage %+v", v)
	}
}

// MARK: - share helpers

func TestShareSlugAndBaseURL(t *testing.T) {
	s := NewShareSlug()
	if !strings.HasPrefix(s, "p") || len(s) != 23 || strings.ContainsAny(s, "+/=") {
		t.Fatalf("slug %q", s)
	}
	t.Setenv("SHARE_PORTFOLIO_BASE_URL", " https://norviq.test/ ")
	if ShareBaseURL() != "https://norviq.test" {
		t.Fatal(ShareBaseURL())
	}
	t.Setenv("SHARE_PORTFOLIO_BASE_URL", "")
	if ShareBaseURL() != "https://norviq.org" {
		t.Fatal(ShareBaseURL())
	}
}

type errRates struct{ err error }

func (e errRates) Rate(context.Context, string, string, time.Time) (fx.Rate, error) {
	return fx.Rate{}, e.err
}

func TestValuateRateErrors(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	stocks := []portfoliodb.Stock{fxStock("NOVO", 1, 10, ccy("DKK"))}
	prices := map[string]Price{"NOVO": {700, "DKK", 1}}

	boom := errors.New("db down")
	if _, err := Valuate(ctx, stocks, prices, 0, "USD", errRates{boom}, day); !errors.Is(err, boom) {
		t.Fatalf("non-ErrMissing error must abort, got %v", err)
	}
	// An unpriced foreign holding's cost also needs a rate; a hard error aborts there too.
	if _, err := Valuate(ctx, stocks, map[string]Price{}, 0, "USD", errRates{boom}, day); !errors.Is(err, boom) {
		t.Fatalf("cost-side error must abort, got %v", err)
	}
	v, err := Valuate(ctx, stocks, prices, 0, "USD", errRates{fx.ErrMissing}, day)
	if err != nil || v.MissingSymbols != 1 || v.MarketValue != 0 || v.CostBasis != 0 ||
		len(v.FxMissing) != 1 || v.FxMissing[0] != "DKK" || v.FxAsOf != nil {
		t.Fatalf("ErrMissing: %+v %v", v, err)
	}
}

func TestValuateMinorUnitFactor(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	rates := fx.Static([]fx.DailyRate{
		{Date: day, Quote: "USD", Rate: "1.25"},
		{Date: day, Quote: "GBP", Rate: "0.85"},
	})
	stocks := []portfoliodb.Stock{fxStock("VOD.L", 100, 1, ccy("GBP"))}
	// Quoted in pence: 500p = GBP 5.00.
	v, err := Valuate(ctx, stocks, map[string]Price{"VOD.L": {500, "GBP", 0.01}}, 0, "USD", rates, day)
	if err != nil {
		t.Fatal(err)
	}
	if want := round2(100 * 500 * 0.01 * (1.25 / 0.85)); v.MarketValue != want {
		t.Errorf("pence %v want %v", v.MarketValue, want)
	}
	// A Price literal without a factor counts as 1 (R20).
	v, err = Valuate(ctx, []portfoliodb.Stock{stock("AAPL", 10, 100)}, map[string]Price{"AAPL": {200, "USD", 0}}, 0, "USD", rates, day)
	if err != nil || v.MarketValue != 2000 {
		t.Errorf("zero factor %+v %v", v, err)
	}
}

func TestValuateFxMissingSorted(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	stocks := []portfoliodb.Stock{fxStock("VOD.L", 1, 1, ccy("GBP")), fxStock("NESN", 1, 1, ccy("CHF"))}
	prices := map[string]Price{"VOD.L": {5, "GBP", 1}, "NESN": {90, "CHF", 1}}
	v, err := Valuate(ctx, stocks, prices, 0, "USD", fx.Static(fxFixture), day)
	if err != nil || len(v.FxMissing) != 2 || v.FxMissing[0] != "CHF" || v.FxMissing[1] != "GBP" {
		t.Fatalf("%+v %v", v.FxMissing, err)
	}
}

// A stored minor-unit currency means buy_price is in minor units: cost is
// scaled to major units before converting.
func TestValuateCostScalesMinorUnitStockCurrency(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	rates := fx.Static([]fx.DailyRate{
		{Date: day, Quote: "USD", Rate: "1.25"},
		{Date: day, Quote: "GBP", Rate: "0.85"},
	})
	stocks := []portfoliodb.Stock{fxStock("VOD.L", 100, 250, ccy("GBX"))}
	for _, tc := range []struct {
		base string
		want float64
	}{{"USD", 100 * 250 * 0.01 * (1.25 / 0.85)}, {"GBP", 100 * 250 * 0.01}} {
		v, err := Valuate(ctx, stocks, map[string]Price{}, 0, tc.base, rates, day)
		if err != nil || v.CostBasis != round2(tc.want) {
			t.Errorf("%s: cost %v want %v (%v)", tc.base, v.CostBasis, round2(tc.want), err)
		}
	}
}
