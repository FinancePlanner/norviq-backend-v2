package insights

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ported from InsightsServiceTests / FallbackInsightsProviderTests /
// SentimentAggregationServiceTests, against httptest stubs only.

func testSettings() *Settings {
	s := LoadSettings()
	s.HermesBaseURL, s.DeepAPIKey = "", ""
	s.PinnedTickers = nil
	s.SP500Symbols = nil
	s.FMPAPIKey = nil
	s.TrackedTickersLimit = 25
	s.UserTierLimit = 500
	s.TrendingTierLimit = 50
	s.SentimentWindowDays = 7
	s.BatchSize = 20
	s.PostsPerSymbol = 100
	s.ThemePosts = 40
	s.MaxThemeSymbols = 150
	return &s
}

type stubQuotes struct{}

func (stubQuotes) QuoteBatch(_ context.Context, symbols []string) (marketdata.QuoteBatchResponse, error) {
	out := marketdata.QuoteBatchResponse{Quotes: []marketdata.QuoteResponse{}}
	for _, s := range symbols {
		out.Quotes = append(out.Quotes, marketdata.QuoteResponse{Symbol: s, CurrentPrice: 100, Currency: "USD"})
	}
	return out, nil
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05+00:00") + "Z" }

// hermesStub serves the five Hermes endpoints with fixed data.
func hermesStub(t *testing.T, now time.Time) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer hermes-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body any
		switch {
		case r.URL.Path == "/finance/events":
			body = map[string]any{"count": 2, "events": []any{
				map[string]any{
					"eventId": "e1", "source": "x", "sourceId": "s1", "topic": "stocks",
					"observedAt": iso(now.Add(-time.Hour)), "ingestedAt": iso(now.Add(-time.Hour)),
					"payload":   map[string]any{"title": "TSLA rallies", "summary": "up", "url": "https://x.test/1", "author": "a"},
					"sentiment": map[string]any{"label": "positive", "score": 0.6},
				},
				map[string]any{
					"eventId": "e2", "source": "reddit", "topic": "crypto",
					"observedAt": iso(now.Add(-2 * time.Hour)), "ingestedAt": iso(now.Add(-2 * time.Hour)),
				},
			}}
		case r.URL.Path == "/finance/summary":
			body = map[string]any{"windowDays": 30, "totalEvents": 2, "byTopic": map[string]int{"stocks": 1, "crypto": 1}}
		case r.URL.Path == "/finance/sentiment":
			body = map[string]any{
				"topic": r.URL.Query().Get("topic"), "windowDays": 30, "count": 2,
				"labelCounts": map[string]int{"positive": 1, "neutral": 1}, "averageScore": 0.3, "sampled": 2,
			}
		case r.URL.Path == "/finance/net-worth":
			body = map[string]any{
				"latest": map[string]any{"eventId": "n2", "ingestedAt": iso(now.Add(-time.Hour)), "value": 1200.5},
				"history": []any{
					map[string]any{"eventId": "n1", "ingestedAt": iso(now.Add(-48 * time.Hour)), "value": 1000.0},
					map[string]any{"eventId": "n2", "ingestedAt": iso(now.Add(-time.Hour)), "value": 1200.5},
				},
			}
		case r.URL.Path == "/finance/ticker/TSLA/posts":
			body = map[string]any{"symbol": "TSLA", "days": 30, "count": 1, "posts": []any{map[string]any{
				"eventId": "p1", "author": "Trader", "authorHandle": "@trader", "text": "bullish, loading calls",
				"url": "https://x.test/p1", "sentiment": "positive", "sentimentScore": 0.7, "confidence": 0.8,
				"postedAt": iso(now.Add(-3 * time.Hour)),
			}}}
		case strings.HasPrefix(r.URL.Path, "/finance/ticker/"):
			body = map[string]any{"symbol": "X", "days": 30, "count": 0, "posts": []any{}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func makePro(t *testing.T, app *testutil.App, id uuid.UUID) {
	t.Helper()
	if _, err := app.Deps.DB.Exec(context.Background(),
		`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), id); err != nil {
		t.Fatal(err)
	}
}

func holdStock(t *testing.T, app *testutil.App, userID uuid.UUID, symbol string, shares, price float64) {
	t.Helper()
	ctx := context.Background()
	list := uuid.New()
	if _, err := app.Deps.DB.Exec(ctx, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, list, userID, "List "+symbol); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Deps.DB.Exec(ctx,
		`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES ($1, $2, $3, $4, $5, now(), now(), $6)`,
		uuid.New(), userID, symbol, shares, price, list); err != nil {
		t.Fatal(err)
	}
}

func TestHermesSyncAndReadRoutes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	hermes, _ := hermesStub(t, now)
	s := testSettings()
	s.PinnedTickers = []string{"TSLA"}
	opts := &Options{
		Settings: s, Provider: HermesProvider{BaseURL: hermes.URL, APIToken: "hermes-token", HTTP: hermes.Client()},
		Quotes: stubQuotes{}, NoThemes: true,
	}
	app := testutil.NewApp(t, []server.Feature{Feature{Options: opts}})
	user := app.CreateUser("insights-free@example.com")
	tok := app.Token(user)

	// Before the first sync the snapshot read is a 404.
	if r := app.Do("GET", "/v1/insights/sentiment", tok, nil); r.Status != 404 ||
		!strings.Contains(string(r.Body), "No sentiment snapshot available yet") {
		t.Fatalf("pre-sync sentiment %d %s", r.Status, r.Body)
	}

	// Admin gate.
	if r := app.Do("POST", "/v1/insights/sync", "", nil); r.Status != 401 {
		t.Fatalf("anon sync %d", r.Status)
	}
	if r := app.Do("POST", "/v1/insights/sync", tok, nil); r.Status != 403 {
		t.Fatalf("non-admin sync %d %s", r.Status, r.Body)
	}
	admin := app.CreateUser("insights-admin@example.com")
	app.Deps.Admin.IDs[admin] = struct{}{}
	r := app.Do("POST", "/v1/insights/sync", app.Token(admin), nil)
	if r.Status != 200 {
		t.Fatalf("sync %d %s", r.Status, r.Body)
	}
	var sum SyncSummary
	r.JSON(t, &sum)
	if sum.EventsInserted != 2 || sum.SnapshotsUpserted != 3 || sum.TickerPostsInserted != 1 || sum.NetWorthInserted != 2 ||
		sum.TickerPostsFetched != 1 || sum.TickerSymbolsFailed != 0 || sum.TickerPostsNewestAt == nil {
		t.Fatalf("summary %+v", sum)
	}

	// Idempotent: a second run inserts nothing new but re-upserts snapshots.
	svc, err := SharedService(app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.SyncFromHermes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.EventsInserted != 0 || again.TickerPostsInserted != 0 || again.NetWorthInserted != 0 || again.SnapshotsUpserted != 3 {
		t.Fatalf("rerun %+v", again)
	}

	var summary SummaryResponse
	r = app.Do("GET", "/v1/insights/summary?days=7", tok, nil)
	r.JSON(t, &summary)
	if r.Status != 200 || summary.TotalEvents != 2 || summary.ByTopic["stocks"] != 1 || len(summary.LatestEvents) != 2 {
		t.Fatalf("summary %d %s", r.Status, r.Body)
	}
	direct, err := svc.Summary(context.Background(), 7)
	if err != nil || direct.TotalEvents != 2 {
		t.Fatalf("exported Summary %+v %v", direct, err)
	}

	if r = app.Do("GET", "/v1/insights/topics/stocks", tok, nil); r.Status != 200 || !strings.Contains(string(r.Body), "TSLA rallies") {
		t.Fatalf("topic %d %s", r.Status, r.Body)
	}
	if r = app.Do("GET", "/v1/insights/sentiment?topic=crypto", tok, nil); r.Status != 200 {
		t.Fatalf("topic sentiment %d %s", r.Status, r.Body)
	}
	var nw NetWorthResponse
	r = app.Do("GET", "/v1/insights/net-worth", tok, nil)
	r.JSON(t, &nw)
	if r.Status != 200 || len(nw.History) != 2 {
		t.Fatalf("net worth %d %s", r.Status, r.Body)
	}

	// Ticker sentiment is premium-gated before the symbol is validated.
	r = app.Do("GET", "/v1/insights/tickers/!!/sentiment", tok, nil)
	if r.Status != 403 || !strings.Contains(string(r.Body), "upgrade_required") {
		t.Fatalf("free ticker %d %s", r.Status, r.Body)
	}
	makePro(t, app, user)
	r = app.Do("GET", "/v1/insights/tickers/$tsla/sentiment", tok, nil)
	var ts TickerSentimentResponse
	r.JSON(t, &ts)
	if r.Status != 200 || ts.Symbol != "TSLA" || len(ts.Posts) != 1 {
		t.Fatalf("pro ticker %d %s", r.Status, r.Body)
	}
	if r = app.Do("GET", "/v1/insights/tickers/!!/sentiment", tok, nil); r.Status != 400 {
		t.Fatalf("bad symbol %d %s", r.Status, r.Body)
	}
}

func TestTrackedSymbolsMachineEndpoint(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{Feature{Options: &Options{Settings: testSettings(), Provider: DisabledProvider{}, Quotes: stubQuotes{}, NoThemes: true}}})
	t.Setenv("INSIGHTS_SYMBOLS_TOKEN", "")
	if r := app.Do("GET", "/v1/insights/tracked-symbols", "", nil); r.Status != 403 ||
		!strings.Contains(string(r.Body), "INSIGHTS_SYMBOLS_TOKEN unset") {
		t.Fatalf("unset %d %s", r.Status, r.Body)
	}
	t.Setenv("INSIGHTS_SYMBOLS_TOKEN", "machine")
	t.Setenv("HERMES_TRACKED_TICKERS_PER_USER", "2")
	if r := app.Do("GET", "/v1/insights/tracked-symbols", "wrong", nil); r.Status != 403 {
		t.Fatalf("wrong token %d", r.Status)
	}
	a := app.CreateUser("ranker-a@example.com")
	b := app.CreateUser("ranker-b@example.com")
	holdStock(t, app, a, "BIG", 10, 100) // 1000
	holdStock(t, app, a, "AMD", 50, 100) // 5000
	holdStock(t, app, a, "TINY", 1, 1)   // dropped: a's third
	holdStock(t, app, b, "AMD", 5, 100)
	holdStock(t, app, b, "LOTS", 20, 10)
	r := app.Do("GET", "/v1/insights/tracked-symbols", "machine", nil)
	var out TrackedSymbolsResponse
	r.JSON(t, &out)
	if r.Status != 200 || strings.Join(out.Symbols, ",") != "AMD,BIG,LOTS" || out.Limit != 25 {
		t.Fatalf("tracked %d %s", r.Status, r.Body)
	}
}

func TestDisabledProviderErrors(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{Feature{Options: &Options{Settings: testSettings(), Provider: DisabledProvider{}, Quotes: stubQuotes{}, NoThemes: true}}})
	admin := app.CreateUser("disabled-admin@example.com")
	app.Deps.Admin.IDs[admin] = struct{}{}
	r := app.Do("POST", "/v1/insights/sync", app.Token(admin), nil)
	if r.Status != 503 || !strings.Contains(string(r.Body), "Insights provider is disabled.") {
		t.Fatalf("sync %d %s", r.Status, r.Body)
	}
}

// recordingProvider serves fetchTickerPosts from a map and records calls.
type recordingProvider struct {
	DisabledProvider
	mu    sync.Mutex
	calls []string
	posts map[string][]hermesTickerPost
}

func (*recordingProvider) Enabled() bool { return true }
func (*recordingProvider) Label() string { return "RecordingProvider" }

func (p *recordingProvider) FetchTickerPosts(_ context.Context, symbol string, _, _ int) (hermesTickerPostsResponse, error) {
	p.mu.Lock()
	p.calls = append(p.calls, symbol)
	p.mu.Unlock()
	posts := p.posts[symbol]
	if posts == nil {
		return hermesTickerPostsResponse{}, errors.New("no posts")
	}
	return hermesTickerPostsResponse{Symbol: &symbol, Posts: &posts}, nil
}

type stubChat struct {
	mu       sync.Mutex
	requests [][]ai.Message
	reply    string
}

func (c *stubChat) Chat(_ context.Context, messages []ai.Message, _ []ai.Tool, format string) (ai.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if format != "json_object" {
		return ai.Message{}, errors.New("unexpected format " + format)
	}
	c.requests = append(c.requests, messages)
	reply := c.reply
	return ai.Message{Role: "assistant", Content: &reply}, nil
}

func post(id, text, label string, at time.Time) hermesTickerPost {
	ts := iso(at)
	return hermesTickerPost{EventID: &id, Text: &text, Sentiment: &label, PostedAt: &ts}
}

func TestAggregationIngestsOnlyUserTier(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	prov := &recordingProvider{posts: map[string][]hermesTickerPost{
		"AAPL": {
			post("a1", "very bullish, breakout and rally", "positive", now.Add(-time.Hour)),
			post("a2", "loading calls, moon", "positive", now.Add(-2*time.Hour)),
			post("a3", "filed a report", "neutral", now.Add(-3*time.Hour)),
		},
		"MSFT": {post("m1", "bearish crash", "negative", now.Add(-time.Hour))},
	}}
	chat := &stubChat{reply: `{"summary":"Momentum chatter.","themes":[{"label":"Breakout","stance":"Bullish","evidenceCount":9}],"contrarianFlag":false}`}
	s := testSettings()
	s.SP500Symbols = []string{"MSFT"}
	app := testutil.NewApp(t, []server.Feature{Feature{Options: &Options{Settings: s, Provider: prov, Quotes: stubQuotes{}, Chat: chat}}})
	holder := app.CreateUser("agg-holder@example.com")
	holdStock(t, app, holder, "AAPL", 10, 150)
	admin := app.CreateUser("agg-admin@example.com")
	app.Deps.Admin.IDs[admin] = struct{}{}
	at := app.Token(admin)

	r := app.Do("POST", "/v1/insights/sentiment/seed-index", at, nil)
	if r.Status != 200 || strings.TrimSpace(string(r.Body)) != `{"seeded":1}` {
		t.Fatalf("seed %d %s", r.Status, r.Body)
	}
	r = app.Do("POST", "/v1/insights/sentiment/sync", at, nil)
	var sum SentimentSyncSummary
	r.JSON(t, &sum)
	if r.Status != 200 {
		t.Fatalf("sync %d %s", r.Status, r.Body)
	}
	// An x-only provider (the extension-default fetchSymbolPosts) serves
	// only the user tier (AAPL); the sp500 tier (MSFT) asks for the cheap
	// sources, which it does not serve, so MSFT is considered but not
	// fetched. Direct and Fallback: TestFallbackSymbolPostsDispatch.
	if strings.Join(prov.calls, ",") != "AAPL" || sum.SymbolsConsidered != 2 || sum.SymbolsIngested != 1 ||
		sum.PostsFetched != 3 || sum.PostsInserted != 3 || sum.ThemesGenerated != 1 {
		t.Fatalf("calls %v summary %+v", prov.calls, sum)
	}
	if len(chat.requests) != 1 || chat.requests[0][0].Role != "system" {
		t.Fatalf("chat %v", chat.requests)
	}

	tok := app.Token(holder)
	var batch SymbolSentimentBatchResponse
	r = app.Do("GET", "/v1/insights/sentiment/symbols?symbols=aapl,msft,!!", tok, nil)
	r.JSON(t, &batch)
	if r.Status != 200 || len(batch.Symbols) != 1 || batch.Requested != 2 || batch.Symbols["AAPL"].SourceCounts.X != 3 ||
		batch.Symbols["AAPL"].Label != positiveLabel {
		t.Fatalf("batch %d %s", r.Status, r.Body)
	}
	if r = app.Do("GET", "/v1/insights/sentiment/symbols?symbols=!!", tok, nil); r.Status != 400 ||
		!strings.Contains(string(r.Body), "Provide at least one symbol.") {
		t.Fatalf("empty batch %d %s", r.Status, r.Body)
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = "S" + itoa(i)
	}
	if r = app.Do("GET", "/v1/insights/sentiment/symbols?symbols="+strings.Join(many, ","), tok, nil); r.Status != 400 ||
		!strings.Contains(string(r.Body), "At most 100 symbols") {
		t.Fatalf("big batch %d %s", r.Status, r.Body)
	}

	var pf PortfolioSentimentResponse
	r = app.Do("GET", "/v1/insights/sentiment/portfolio", tok, nil)
	r.JSON(t, &pf)
	if r.Status != 200 || pf.Scope != "portfolio" || pf.SymbolsCovered != 1 || pf.Label != positiveLabel {
		t.Fatalf("portfolio %d %s", r.Status, r.Body)
	}
	var wl PortfolioSentimentResponse
	r = app.Do("GET", "/v1/insights/sentiment/watchlist", tok, nil)
	r.JSON(t, &wl)
	if r.Status != 200 || wl.Scope != "watchlist" || len(wl.BiggestMovers) != 0 || wl.Label != neutralLabel {
		t.Fatalf("watchlist %d %s", r.Status, r.Body)
	}
	if r = app.Do("GET", "/v1/insights/sentiment/trending", tok, nil); r.Status != 200 {
		t.Fatalf("trending %d %s", r.Status, r.Body)
	}
	if r = app.Do("GET", "/v1/insights/sentiment/history/AAPL", tok, nil); r.Status != 403 {
		t.Fatalf("free history %d %s", r.Status, r.Body)
	}
	makePro(t, app, holder)
	var hist []SymbolSentimentResponse
	r = app.Do("GET", "/v1/insights/sentiment/history/AAPL", tok, nil)
	r.JSON(t, &hist)
	// history never includes themes (includeThemes: false in Vapor).
	if r.Status != 200 || len(hist) != 1 || hist[0].Themes != nil || hist[0].PostCount != 3 {
		t.Fatalf("history %d %s", r.Status, r.Body)
	}
	b0, err := bundleFor(app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	withThemes, err := b0.queries.Batch(context.Background(), []string{"AAPL"}, true)
	if err != nil {
		t.Fatal(err)
	}
	th := withThemes.Symbols["AAPL"].Themes
	if th == nil || len(th.Themes) != 1 || th.Themes[0].Stance != "bullish" || th.Themes[0].EvidenceCount != 3 ||
		th.Summary == nil || *th.Summary != "Momentum chatter." {
		t.Fatalf("themes %+v", th)
	}

	// Retention leaves fresh rows alone.
	b, err := bundleFor(app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	SentimentRetentionOnce(context.Background(), app.Deps, b, now.Add(400*24*time.Hour))
	var posts, rows int
	_ = app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM ticker_sentiment_posts`).Scan(&posts)
	_ = app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM symbol_sentiment_daily`).Scan(&rows)
	if posts != 0 || rows != 1 {
		t.Fatalf("retention posts=%d rows=%d", posts, rows)
	}
}

// failing / succeeding providers for the fallback chain.
type fixedProvider struct {
	DisabledProvider
	label string
	err   error
	calls *int
}

func (fixedProvider) Enabled() bool   { return true }
func (p fixedProvider) Label() string { return p.label }
func (p fixedProvider) FetchSummary(context.Context, int) (hermesSummaryResponse, error) {
	*p.calls++
	if p.err != nil {
		return hermesSummaryResponse{}, p.err
	}
	n := 7
	return hermesSummaryResponse{WindowDays: &n}, nil
}

func TestFallbackChain(t *testing.T) {
	var aCalls, bCalls int
	cooldowns := NewCooldownRegistry(time.Hour)
	a := fixedProvider{label: "A", err: newCreditsExhausted("A", "out of credits"), calls: &aCalls}
	b := fixedProvider{label: "B", calls: &bCalls}
	chain := FallbackProvider{Providers: []Provider{a, b}, Logger: testutil.NewApp(t, nil).Deps.Logger, Cooldowns: cooldowns}

	got, err := chain.FetchSummary(context.Background(), 7)
	if err != nil || *got.WindowDays != 7 || aCalls != 1 || bCalls != 1 {
		t.Fatalf("first %v %v a=%d b=%d", got, err, aCalls, bCalls)
	}
	if !cooldowns.IsCoolingDown("A") {
		t.Fatal("credit exhaustion did not start a cooldown")
	}
	if _, err = chain.FetchSummary(context.Background(), 7); err != nil || aCalls != 1 || bCalls != 2 {
		t.Fatalf("cooled provider retried a=%d b=%d %v", aCalls, bCalls, err)
	}

	// Last error wins when everything fails.
	c := fixedProvider{label: "C", err: errors.New("c down"), calls: new(int)}
	d := fixedProvider{label: "D", err: errors.New("d down"), calls: new(int)}
	_, err = FallbackProvider{Providers: []Provider{c, d}, Logger: chain.Logger, Cooldowns: NewCooldownRegistry(time.Hour)}.FetchSummary(context.Background(), 7)
	if err == nil || err.Error() != "d down" {
		t.Fatalf("last error %v", err)
	}

	// Every provider cooling → 503.
	only := FallbackProvider{Providers: []Provider{a}, Logger: chain.Logger, Cooldowns: cooldowns}
	_, err = only.FetchSummary(context.Background(), 7)
	if err == nil || !strings.Contains(err.Error(), "Every insights provider is in credit cooldown.") {
		t.Fatalf("cooldown 503 %v", err)
	}
}

func TestHermesProviderErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/finance/summary":
			w.WriteHeader(http.StatusForbidden)
		case "/finance/net-worth":
			_, _ = w.Write([]byte(`{"latest":`))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	h := HermesProvider{BaseURL: srv.URL + "/", HTTP: srv.Client()}
	if _, err := h.FetchSummary(context.Background(), 7); err == nil || err.Error() != "Hermes rejected the request. Check HERMES_API_TOKEN." {
		t.Fatalf("403: %v", err)
	}
	if _, err := h.FetchNetWorth(context.Background()); err == nil || err.Error() != "Failed to decode Hermes response for /finance/net-worth." {
		t.Fatalf("decode: %v", err)
	}
	if _, err := h.FetchEvents(context.Background(), 7, 10); err == nil || err.Error() != "Hermes request failed for /finance/events with status 418." {
		t.Fatalf("418: %v", err)
	}
}

// With only the Direct provider answering (staging: no Hermes, DeepAPI out
// of credit), the Hermes sync used to fail every tick on Direct's 501 for
// events, so ticker posts never synced. The topic steps now count as nothing
// synced and the run carries on.
func TestHermesSyncSkipsStepsDirectDoesNotServe(t *testing.T) {
	srv, _ := stocktwitsRedditStub(t)
	s := testSettings()
	s.PinnedTickers = []string{"TSLA"}
	credits := fixedProvider{label: "DeepAPIInsightsProvider", err: newCreditsExhausted("DeepAPI", "out of credits"), calls: new(int)}
	chain := FallbackProvider{
		Providers: []Provider{credits, directFor(srv, time.Now())},
		Logger:    slog.New(slog.DiscardHandler), Cooldowns: NewCooldownRegistry(time.Hour),
	}
	chain.Cooldowns.BeginCooldown("DeepAPIInsightsProvider")
	app := testutil.NewApp(t, []server.Feature{Feature{Options: &Options{Settings: s, Provider: chain, Quotes: stubQuotes{}, NoThemes: true}}})
	admin := app.CreateUser("direct-sync-admin@example.com")
	app.Deps.Admin.IDs[admin] = struct{}{}
	r := app.Do("POST", "/v1/insights/sync", app.Token(admin), nil)
	if r.Status != 200 {
		t.Fatalf("sync %d %s", r.Status, r.Body)
	}
	var sum SyncSummary
	r.JSON(t, &sum)
	if sum.EventsInserted != 0 || sum.SnapshotsUpserted != 0 || sum.NetWorthInserted != 0 ||
		sum.TickerPostsFetched != 2 || sum.TickerPostsInserted != 2 || sum.TickerSymbolsFailed != 0 {
		t.Fatalf("summary %+v", sum)
	}

	// A real failure (here the first provider's events error) still fails
	// the run.
	down := fixedProvider{label: "A", err: errors.New("hermes down"), calls: new(int)}
	svc := NewService(app.Deps.DB, app.Deps.Logger, FallbackProvider{
		Providers: []Provider{down, directFor(srv, time.Now())}, Logger: app.Deps.Logger, Cooldowns: NewCooldownRegistry(time.Hour),
	}, 25, []string{"TSLA"}, time.Now)
	if _, err := svc.SyncFromHermes(context.Background()); err == nil || errNotImplemented(err) {
		t.Fatalf("real failure %v", err)
	}
}
