package portfolio

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

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

func TestValuateRule(t *testing.T) {
	v := Valuate([]portfoliodb.Stock{stock("AAPL", 10, 150), stock("NVDA", 4, 500)}, map[string]float64{"AAPL": 200}, 0, "USD")
	if v.MarketValue != 2000 || v.CostBasis != 3500 || v.MissingSymbols != 1 || v.PricedSymbols != 1 || v.IsFullyPriced() {
		t.Fatalf("unpriced %+v", v)
	}
	v = Valuate([]portfoliodb.Stock{stock("AAPL", 10, 150), stock("MSFT", 5, 300)}, map[string]float64{"AAPL": 200, "MSFT": 400}, 0, "USD")
	if !v.IsFullyPriced() || v.MarketValue != 4000 {
		t.Fatalf("priced %+v", v)
	}
	v = Valuate([]portfoliodb.Stock{stock("AAPL", 10, 150)}, map[string]float64{"AAPL": 0}, 0, "USD")
	if v.MissingSymbols != 1 || v.MarketValue != 0 {
		t.Fatalf("zero price %+v", v)
	}
	v = Valuate([]portfoliodb.Stock{stock(" aapl\n", 1, 1)}, map[string]float64{"AAPL": 2}, 0, "USD")
	if v.PricedSymbols != 1 {
		t.Fatal("symbol normalization")
	}
	v = Valuate([]portfoliodb.Stock{stock("AAPL", 1, 100), stock("AAPL", 2, 100)}, map[string]float64{"AAPL": 110}, 0, "USD")
	if v.PositionCount != 2 || v.PricedSymbols != 2 || v.MarketValue != 330 {
		t.Fatalf("two rows %+v", v)
	}
	v = Valuate([]portfoliodb.Stock{stock("AAPL", 3, 33.333)}, map[string]float64{"AAPL": 10.004}, 12.345, "USD")
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
