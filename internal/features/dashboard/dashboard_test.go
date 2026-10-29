package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"norviq-api/internal/features/statistics"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/store/dashboarddb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// --- stubs (WhyMovedTests.WhyMovedStubState) ---

type stubState struct {
	mu              sync.Mutex
	summaries       []statistics.StockStatisticsSummaryDTO
	sectors         []statistics.SectorAllocationDTO
	sentiment       map[string]SentimentAggregate
	sentimentThrows bool
	topics          map[string]int
	chatCalls       int
	chatResponse    string
}

func newState() *stubState {
	return &stubState{chatResponse: `{"text": "AAPL drove the gain on bullish chatter."}`}
}

func (s *stubState) Overview(context.Context, uuid.UUID, statistics.QueryOptions) (statistics.ViewModel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0.0
	for _, x := range s.summaries {
		total += x.MarketValue
	}
	return statistics.ViewModel{ImportedStocks: statistics.ImportedStocksView{
		TotalPositions: len(s.summaries), TotalMarketValue: total,
		StockSummaries: s.summaries, SectorAllocations: s.sectors,
	}}, nil
}

func (s *stubState) TickerSentiment(_ context.Context, symbol string, _, _ int) (SentimentAggregate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sentimentThrows {
		return SentimentAggregate{}, errors.New("stub")
	}
	if a, ok := s.sentiment[strings.ToUpper(symbol)]; ok {
		return a, nil
	}
	return SentimentAggregate{Label: "neutral"}, nil
}

func (s *stubState) TopicCounts(context.Context, int) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.topics, nil
}

func (s *stubState) Chat(context.Context, []ai.Message, []ai.Tool, string) (ai.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatCalls++
	return ai.Text("assistant", s.chatResponse), nil
}

func (s *stubState) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatCalls
}

type noMarket struct{}

func (noMarket) Overview(context.Context) (marketdata.MarketOverviewResponse, error) {
	return marketdata.MarketOverviewResponse{}, errors.New("no market data in tests")
}

type fixedChat string

func (f fixedChat) Chat(context.Context, []ai.Message, []ai.Tool, string) (ai.Message, error) {
	return ai.Text("assistant", fmt.Sprintf(`{"text": %q}`, string(f))), nil
}

type fixedPlan bool

func (f fixedPlan) IsPro(context.Context, uuid.UUID) (bool, error) { return bool(f), nil }

func summary(symbol string, value float64, dayPct *float64) statistics.StockStatisticsSummaryDTO {
	return statistics.StockStatisticsSummaryDTO{Symbol: symbol, MarketValue: value, WeightPercent: 10, DailyChangePercent: dayPct}
}

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func pct(v float64) *float64 { return &v }

type dashApp struct {
	*testutil.App
	state *stubState
	token string
	user  uuid.UUID
}

// newDashApp mounts the feature with stubbed statistics, sentiment and AI;
// insights reads the real Postgres tables.
func newDashApp(t *testing.T, platform func(*stubState) *ai.Platform) *dashApp {
	t.Helper()
	st := newState()
	p := &ai.Platform{Logger: testLogger(), Default: st}
	if platform != nil {
		p = platform(st)
	}
	f := Feature{Stats: st, Insights: st, Market: noMarket{}, AI: &AI{Platform: p, Cache: cache.NewMemory()}}
	app := testutil.NewApp(t, []server.Feature{f})
	user := app.CreateUser("dash-" + uuid.NewString() + "@example.com")
	return &dashApp{App: app, state: st, token: app.Token(user), user: user}
}

func (a *dashApp) whyMoved(t *testing.T) WhyMovedResponse {
	t.Helper()
	r := a.Do(http.MethodGet, "/v1/dashboard/why-moved", a.token, nil)
	if r.Status != http.StatusOK {
		t.Fatalf("why-moved: %d %s", r.Status, r.Body)
	}
	var out WhyMovedResponse
	r.JSON(t, &out)
	return out
}

func TestWhyMovedHappyPath(t *testing.T) {
	a := newDashApp(t, nil)
	a.state.summaries = []statistics.StockStatisticsSummaryDTO{
		summary("AAPL", 50000, pct(3)),
		summary("MSFT", 20000, pct(1)),
		summary("XOM", 10000, pct(-2)),
		summary("FLAT", 5000, pct(0)),
	}
	a.state.sentiment = map[string]SentimentAggregate{"AAPL": {Label: "bullish", Score: pct(0.6), PostCount: 12}}
	a.state.topics = map[string]int{"Stocks": 9, "Housing": 4}

	body := a.whyMoved(t)
	if body.Movers[0].Symbol != "AAPL" || body.Movers[0].Sentiment == nil || body.Movers[0].Sentiment.Label != "bullish" {
		t.Fatalf("movers: %+v", body.Movers)
	}
	syms := map[string]bool{}
	for _, m := range body.Movers {
		syms[m.Symbol] = true
	}
	if !syms["XOM"] || syms["FLAT"] {
		t.Fatalf("movers: %v", syms)
	}
	if body.Context.Topics[0].Topic != "Stocks" {
		t.Fatalf("topics: %+v", body.Context.Topics)
	}
	src := body.SentimentSource
	if src == nil || src.PostsAnalyzed != 12 || src.SymbolsCovered != 1 || src.WindowDays != 7 {
		t.Fatalf("source: %+v", src)
	}
	if body.AISummary == nil || !strings.Contains(body.AISummary.Text, "AAPL") || body.PortfolioChangePercent == nil {
		t.Fatalf("summary: %+v", body)
	}
}

func TestWhyMovedSentimentDegrades(t *testing.T) {
	a := newDashApp(t, nil)
	a.state.summaries = []statistics.StockStatisticsSummaryDTO{summary("AAPL", 50000, pct(2))}
	a.state.sentimentThrows = true
	body := a.whyMoved(t)
	if len(body.Movers) != 1 || body.Movers[0].Sentiment != nil || body.SentimentSource != nil {
		t.Fatalf("%+v", body)
	}
}

func TestWhyMovedEmptyPortfolio(t *testing.T) {
	a := newDashApp(t, nil)
	body := a.whyMoved(t)
	if len(body.Movers) != 0 || body.AISummary != nil || a.state.calls() != 0 {
		t.Fatalf("%+v calls=%d", body, a.state.calls())
	}
	r := a.Do(http.MethodGet, "/v1/dashboard/why-moved", a.token, nil)
	if !strings.Contains(string(r.Body), `"movers":[]`) || !strings.Contains(string(r.Body), `"indices":[]`) {
		t.Fatalf("arrays must never be null: %s", r.Body)
	}
}

func TestWhyMovedAICacheHit(t *testing.T) {
	a := newDashApp(t, nil)
	a.state.summaries = []statistics.StockStatisticsSummaryDTO{summary("AAPL", 50000, pct(2))}
	first := a.whyMoved(t)
	second := a.whyMoved(t)
	if a.state.calls() != 1 || first.AISummary == nil || second.AISummary == nil || *first.AISummary != *second.AISummary {
		t.Fatalf("calls=%d", a.state.calls())
	}
}

func TestWhyMovedInvalidAIResponseIsDropped(t *testing.T) {
	a := newDashApp(t, nil)
	a.state.summaries = []statistics.StockStatisticsSummaryDTO{summary("AAPL", 50000, pct(2))}
	// The system prompt asks for {"title","body"}; only {"text"} is read.
	a.state.chatResponse = `{"title": "Up", "body": "AAPL"}`
	if body := a.whyMoved(t); body.AISummary != nil {
		t.Fatalf("%+v", body.AISummary)
	}
}

func TestWhyMovedRequiresAuth(t *testing.T) {
	a := newDashApp(t, nil)
	for _, p := range []string{"/v1/dashboard", "/v1/dashboard/insights", "/v1/dashboard/why-moved"} {
		if r := a.Do(http.MethodGet, p, "", nil); r.Status != http.StatusUnauthorized {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
}

func twoChains(pro bool) func(*stubState) *ai.Platform {
	return func(st *stubState) *ai.Platform {
		return &ai.Platform{
			Logger: testLogger(), Default: st, Plans: fixedPlan(pro),
			Router: &ai.Router{Free: fixedChat("served by the free chain"), Pro: fixedChat("served by the pro chain")},
		}
	}
}

func TestWhyMovedFreeUserStaysOnFreeChain(t *testing.T) {
	a := newDashApp(t, twoChains(false))
	a.state.summaries = []statistics.StockStatisticsSummaryDTO{summary("AAPL", 50000, pct(3))}
	body := a.whyMoved(t)
	if body.AISummary == nil || body.AISummary.Text != "served by the free chain" || a.state.calls() != 0 {
		t.Fatalf("%+v calls=%d", body.AISummary, a.state.calls())
	}
}

func TestWhyMovedProUserUsesProChain(t *testing.T) {
	a := newDashApp(t, twoChains(true))
	a.state.summaries = []statistics.StockStatisticsSummaryDTO{summary("AAPL", 50000, pct(3))}
	if body := a.whyMoved(t); body.AISummary == nil || body.AISummary.Text != "served by the pro chain" {
		t.Fatalf("%+v", body.AISummary)
	}
}

// --- /v1/dashboard ---

func TestDashboardPerformersAndAllocation(t *testing.T) {
	a := newDashApp(t, nil)
	a.state.summaries = []statistics.StockStatisticsSummaryDTO{
		summary("AAPL", 1100, pct(10)),
		summary("MSFT", 1000, nil),
	}
	a.state.sectors = []statistics.SectorAllocationDTO{{Sector: "Unknown", Value: 2100, WeightPercent: 100}}
	var body Response
	r := a.Do(http.MethodGet, "/v1/dashboard", a.token, nil)
	r.JSON(t, &body)
	// AAPL: 1100 - 1100/1.1 = 100; previous total 2000 -> 5%.
	if body.TotalValue != 2100 || body.DailyChange != 100 || body.DailyChangePercent != 5 {
		t.Fatalf("%s", r.Body)
	}
	if len(body.TopPerformers) != 2 || body.TopPerformers[0].Symbol != "AAPL" || body.TopPerformers[0].Change != 100 ||
		body.BottomPerformers[0].Symbol != "MSFT" {
		t.Fatalf("%s", r.Body)
	}
	if len(body.SectorAllocation) != 1 || body.SectorAllocation[0].Sector != "Unknown" || body.SectorAllocation[0].Percent != 100 {
		t.Fatalf("%s", r.Body)
	}
}

func TestDashboardEmpty(t *testing.T) {
	a := newDashApp(t, nil)
	r := a.Do(http.MethodGet, "/v1/dashboard", a.token, nil)
	want := `{"totalValue":0,"dailyChange":0,"dailyChangePercent":0,"topPerformers":[],"bottomPerformers":[],"sectorAllocation":[]}`
	if r.Status != 200 || string(r.Body) != want {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// --- /v1/dashboard/insights (StockPlanBackendTests dashboardInsights*) ---

func (a *dashApp) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := a.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (a *dashApp) seedMonth(t *testing.T, month int, salary, planned, actual float64) {
	t.Helper()
	snap := uuid.New()
	start := fmt.Sprintf("2026-%02d-01", month)
	a.exec(t, `INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, created_at, updated_at,
		currency_code, category_drift_threshold, total_drift_threshold, alerts_enabled, alert_on_unbudgeted, revision)
		VALUES ($1, $2, $3::date, $4, '{"fundamentals":0.5}', now(), now(), 'EUR', 15, 10, true, true, 0)`, snap, a.user, start, salary)
	if planned > 0 {
		a.exec(t, `INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, created_at, updated_at)
			VALUES ($1, $2, $3, 'Planned', $4, 'fundamentals', now(), now())`, uuid.New(), snap, a.user, planned)
	}
	if actual > 0 {
		a.exec(t, `INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at)
			VALUES ($1, $2, 'Actual', $3, 'fundamentals', $4::date, now(), now())`, uuid.New(), a.user, actual, start)
	}
}

func (a *dashApp) seedCash(t *testing.T, balance float64) {
	t.Helper()
	acct := uuid.New()
	a.exec(t, `INSERT INTO accounts (id, user_id, external_id, broker, base_currency, created_at) VALUES ($1, $2, $3, 'manual', 'USD', now())`,
		acct, a.user, "acct-"+acct.String())
	a.exec(t, `INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, 'USD', $3, current_date, now())`,
		uuid.New(), acct, balance)
}

func (a *dashApp) insights(t *testing.T) InsightsResponse {
	t.Helper()
	r := a.Do(http.MethodGet, "/v1/dashboard/insights", a.token, nil)
	if r.Status != http.StatusOK {
		t.Fatalf("insights: %d %s", r.Status, r.Body)
	}
	var out InsightsResponse
	r.JSON(t, &out)
	return out
}

func TestInsightsPayload(t *testing.T) {
	a := newDashApp(t, nil)
	a.seedMonth(t, 1, 5000, 1500, 1000)
	a.seedCash(t, 2000)
	got := a.insights(t)
	// savings 80% -> 40, streak 1 -> 5, coverage 2000/1000 capped -> 30.
	if got.FinancialHealth.MaxScore != 100 || got.FinancialHealth.Score != 75 || got.SavingsRate != 80 ||
		got.BudgetStreak != 1 || got.CashBuffer != 2000 || got.FinancialHealth.Status != Healthy {
		t.Fatalf("%+v", got)
	}
}

func TestInsightsStrongProfile(t *testing.T) {
	a := newDashApp(t, nil)
	for m := 1; m <= 6; m++ {
		a.seedMonth(t, m, 10000, 4000, 1000)
	}
	a.seedCash(t, 10000)
	if got := a.insights(t).FinancialHealth; got.Score < 90 || got.Status != Excellent {
		t.Fatalf("%+v", got)
	}
}

func TestInsightsMediumProfile(t *testing.T) {
	a := newDashApp(t, nil)
	for m := 1; m <= 3; m++ {
		a.seedMonth(t, m, 10000, 3000, 2000)
	}
	a.seedCash(t, 1000)
	if got := a.insights(t).FinancialHealth; got.Score < 70 || got.Score > 89 || got.Status != Healthy {
		t.Fatalf("%+v", got)
	}
}

func TestInsightsEmpty(t *testing.T) {
	a := newDashApp(t, nil)
	r := a.Do(http.MethodGet, "/v1/dashboard/insights", a.token, nil)
	want := `{"savingsRate":0,"budgetStreak":0,"watchlistCount":0,"cashBuffer":0,"financialHealth":{"score":0,"maxScore":100,"status":"atRisk"}}`
	if r.Status != 200 || string(r.Body) != want {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestFinancialHealthBands(t *testing.T) {
	cases := []struct {
		savings float64
		streak  int
		cash    float64
		actual  *float64
		score   int
		status  FinancialHealthStatus
	}{
		{0, 0, 0, nil, 0, AtRisk},
		{30, 0, 0, nil, 40, NeedsAttention},
		{30, 6, 0, nil, 70, Healthy},
		{30, 6, 500, pct(1000), 85, Healthy},
		{50, 12, 5000, pct(1000), 100, Excellent},
		{-10, -1, -5, pct(1000), 0, AtRisk},
	}
	for _, c := range cases {
		got := financialHealth(c.savings, c.streak, c.cash, c.actual)
		if got.Score != c.score || got.Status != c.status || got.MaxScore != 100 {
			t.Errorf("%+v -> %+v", c, got)
		}
	}
}

func TestDBInsightsSentimentLimitZero(t *testing.T) {
	// Vapor bug ported: why-moved asks for limit 0, which reads no posts.
	pool := testutil.DB(t)
	sym := "QT" + strings.ToUpper(uuid.NewString()[:6])
	if _, err := pool.Exec(context.Background(), `INSERT INTO ticker_sentiment_posts
		(id, dedupe_key, symbol, author, text, sentiment_label, sentiment_score, posted_at, created_at)
		VALUES ($1, $2, $3, 'a', 'up only', 'bullish', 0.8, now() - interval '1 day', now())`, uuid.New(), "test-"+sym, sym); err != nil {
		t.Fatal(err)
	}
	q := dashboarddb.New(pool)
	agg, err := DBInsights{Q: q}.TickerSentiment(context.Background(), sym, 7, 0)
	if err != nil || agg.PostCount != 0 || agg.Score != nil {
		t.Fatalf("%+v %v", agg, err)
	}
	agg, err = DBInsights{Q: q}.TickerSentiment(context.Background(), sym, 7, 50)
	if err != nil || agg.PostCount != 1 {
		t.Fatalf("%+v %v", agg, err)
	}
}
