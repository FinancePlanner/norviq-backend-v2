package news

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/server"
	"norviq-api/internal/store/marketnewsdb"
	"norviq-api/internal/store/notificationsdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- stubs (never a real provider) ---

type stubPro map[uuid.UUID]bool

func (s stubPro) IsPro(_ context.Context, id uuid.UUID) (bool, error) { return s[id], nil }

// stubProvider returns canned items for Fetch (filtered to the requested
// symbols) and nothing for FetchGeneral.
type stubProvider struct {
	name  string
	items []newsprovider.Item
}

func (s stubProvider) Name() string { return s.name }

func (s stubProvider) Fetch(_ context.Context, symbols []string) ([]newsprovider.Item, error) {
	want := map[string]bool{}
	for _, sym := range symbols {
		want[sym] = true
	}
	out := []newsprovider.Item{}
	for _, it := range s.items {
		if want[it.Symbol] {
			out = append(out, it)
		}
	}
	return out, nil
}

func (s stubProvider) FetchGeneral(context.Context) ([]newsprovider.Item, error) {
	return []newsprovider.Item{}, nil
}

// stubFeeds is NewsTickerServiceTests' StubFeedsClient.
type stubFeeds struct {
	mu            sync.Mutex
	doc           newsprovider.Document
	candidates    []newsprovider.FeedCandidate
	itemsCalls    [][]string
	discoverCalls []string
}

func (s *stubFeeds) Items(_ context.Context, feeds []string, _ int) (newsprovider.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.itemsCalls = append(s.itemsCalls, append([]string{}, feeds...))
	return s.doc, nil
}

func (s *stubFeeds) Discover(_ context.Context, rawURL string) ([]newsprovider.FeedCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discoverCalls = append(s.discoverCalls, rawURL)
	return s.candidates, nil
}

// recordingChannel stands in for APNs.
type recordingChannel struct {
	mu   sync.Mutex
	sent []inbox.Message
}

func (*recordingChannel) Name() string { return "test" }

func (c *recordingChannel) Send(_ context.Context, _ uuid.UUID, m inbox.Message) (inbox.Outcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return inbox.Outcome{Status: "delivered"}, nil
}

// --- harness ---

type env struct {
	app     *testutil.App
	feature Feature
}

func newEnv(t *testing.T, chain *newsprovider.Chain, pro ProGate, archiveProvider newsprovider.Provider) *env {
	t.Helper()
	if chain == nil {
		chain = &newsprovider.Chain{}
	}
	if pro == nil {
		pro = stubPro{}
	}
	f := Feature{Chain: chain, Pro: pro}
	app := testutil.NewApp(t, []server.Feature{f}, func(d *server.Deps) {
		newsprovider.SetShared(d, newsprovider.Chain{})
		a, err := market.NewNewsArchive(marketnewsdb.New(d.DB), archiveProvider, nil, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		market.SetSharedNewsArchive(d, a)
	})
	return &env{app: app, feature: f}
}

func (e *env) service(t *testing.T) *Service {
	t.Helper()
	svc, err := e.feature.service(e.app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func (e *env) user(t *testing.T, label string) (uuid.UUID, string) {
	t.Helper()
	id := e.app.CreateUser("news-" + label + "-" + uuid.NewString()[:8] + "@example.com")
	return id, e.app.Token(id)
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func seedStock(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, symbol string, shares, price float64) {
	t.Helper()
	list := uuid.New()
	exec(t, pool, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, list, user, "L"+list.String()[:6])
	exec(t, pool, `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, $4, $5, '2026-01-01', now(), $6)`, uuid.New(), user, symbol, shares, price, list)
}

func seedWatch(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, symbol, status string) {
	t.Helper()
	list := uuid.New()
	exec(t, pool, `INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, $3, false, now())`, list, user, "W"+list.String()[:6])
	exec(t, pool, `INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id)
		VALUES ($1, $2, $3, now(), $4, $5)`, uuid.New(), user, symbol, status, list)
}

type apiErr struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func expectError(t *testing.T, r *testutil.Response, status int, reason string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status = %d, want %d: %s", r.Status, status, r.Body)
	}
	var e apiErr
	r.JSON(t, &e)
	if e.Reason != reason {
		t.Fatalf("reason = %q, want %q", e.Reason, reason)
	}
}

func newsBody(symbol, headline string) map[string]any {
	return map[string]any{
		"symbol": symbol, "headline": headline, "source": "Reuters",
		"url": "https://example.com/news/apple", "summary": "Quarterly earnings beat.",
		"publishedAt": "2026-02-11T12:30:00Z",
	}
}

// --- NewsServiceTests ---

func TestNewsCreateListGetUpdateDelete(t *testing.T) {
	e := newEnv(t, nil, nil, nil)
	owner, ownerTok := e.user(t, "owner")
	_, otherTok := e.user(t, "other")

	r := e.app.Do(http.MethodPost, "/v1/news", ownerTok, newsBody("  aapl  ", "Apple beats estimates"))
	if r.Status != http.StatusCreated {
		t.Fatalf("create = %d %s", r.Status, r.Body)
	}
	var created ItemResponse
	r.JSON(t, &created)
	if created.Symbol != "AAPL" || created.Headline != "Apple beats estimates" || *created.Source != "Reuters" ||
		*created.URL != "https://example.com/news/apple" || *created.Summary != "Quarterly earnings beat." ||
		created.PublishedAt != "2026-02-11T12:30:00.000Z" || created.ID != strings.ToUpper(created.ID) {
		t.Fatalf("created = %+v", created)
	}

	// Defaults publishedAt to now when omitted.
	body := newsBody("MSFT", "Microsoft news")
	delete(body, "publishedAt")
	before := time.Now().Add(-2 * time.Second)
	r = e.app.Do(http.MethodPost, "/v1/news", ownerTok, body)
	var defaulted ItemResponse
	r.JSON(t, &defaulted)
	at, err := time.Parse(time.RFC3339Nano, defaulted.PublishedAt)
	if err != nil || at.Before(before) || at.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("default publishedAt = %q (%v)", defaulted.PublishedAt, err)
	}
	e.app.Do(http.MethodPost, "/v1/news", otherTok, newsBody("AAPL", "Other user"))

	// list is user scoped and filters by symbol.
	var all []ItemResponse
	e.app.Do(http.MethodGet, "/v1/news", ownerTok, nil).JSON(t, &all)
	if len(all) != 2 {
		t.Fatalf("owner list = %+v", all)
	}
	var filtered []ItemResponse
	e.app.Do(http.MethodGet, "/v1/news?symbol=%20%20aapl%20", ownerTok, nil).JSON(t, &filtered)
	if len(filtered) != 1 || filtered[0].Symbol != "AAPL" {
		t.Fatalf("filtered = %+v", filtered)
	}

	// get: missing and foreign ids are 404.
	expectError(t, e.app.Do(http.MethodGet, "/v1/news/"+uuid.NewString(), ownerTok, nil), http.StatusNotFound, "News item not found.")
	expectError(t, e.app.Do(http.MethodGet, "/v1/news/"+created.ID, otherTok, nil), http.StatusNotFound, "News item not found.")
	if r := e.app.Do(http.MethodGet, "/v1/news/"+created.ID, ownerTok, nil); r.Status != http.StatusOK {
		t.Fatalf("get = %d", r.Status)
	}

	// update persists.
	upd := map[string]any{
		"symbol": " msft ", "headline": "Microsoft raises guidance", "source": "Bloomberg",
		"url": "https://example.com/news/msft", "summary": "Guidance improved for next quarter.",
		"publishedAt": "2026-02-10T09:15:00Z",
	}
	var updated ItemResponse
	e.app.Do(http.MethodPut, "/v1/news/"+created.ID, ownerTok, upd).JSON(t, &updated)
	if updated.Symbol != "MSFT" || updated.Headline != "Microsoft raises guidance" || *updated.Source != "Bloomberg" ||
		updated.PublishedAt != "2026-02-10T09:15:00.000Z" {
		t.Fatalf("updated = %+v", updated)
	}
	var symbol string
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT symbol FROM news_items WHERE id = $1 AND user_id = $2`,
		uuid.MustParse(created.ID), owner).Scan(&symbol); err != nil || symbol != "MSFT" {
		t.Fatalf("stored symbol = %q %v", symbol, err)
	}

	// delete is user scoped.
	expectError(t, e.app.Do(http.MethodDelete, "/v1/news/"+created.ID, otherTok, nil), http.StatusNotFound, "News item not found.")
	if r := e.app.Do(http.MethodDelete, "/v1/news/"+created.ID, ownerTok, nil); r.Status != http.StatusNoContent {
		t.Fatalf("delete = %d", r.Status)
	}
	expectError(t, e.app.Do(http.MethodGet, "/v1/news/"+created.ID, ownerTok, nil), http.StatusNotFound, "News item not found.")
}

func TestNewsValidation(t *testing.T) {
	e := newEnv(t, nil, nil, nil)
	_, tok := e.user(t, "validate")
	cases := []struct {
		field, value, reason string
	}{
		{"symbol", "   ", "Invalid symbol."},
		{"headline", "   ", "Invalid headline."},
		{"url", "not a valid url", "Invalid url."},
		{"publishedAt", "2026/02/11", "Invalid publishedAt. Expected ISO8601 datetime."},
	}
	for _, c := range cases {
		body := newsBody("AAPL", "Apple")
		body[c.field] = c.value
		expectError(t, e.app.Do(http.MethodPost, "/v1/news", tok, body), http.StatusBadRequest, c.reason)
	}
	expectError(t, e.app.Do(http.MethodGet, "/v1/news/not-a-uuid", tok, nil), http.StatusBadRequest, "Invalid news ID")
	if r := e.app.Do(http.MethodGet, "/v1/news", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", r.Status)
	}
}

func TestNewsListCursor(t *testing.T) {
	e := newEnv(t, nil, nil, nil)
	_, tok := e.user(t, "cursor")
	for i, at := range []string{"2026-02-01T00:00:00Z", "2026-02-02T00:00:00Z", "2026-02-03T00:00:00Z"} {
		b := newsBody("AAPL", "Item "+string(rune('A'+i)))
		b["publishedAt"] = at
		e.app.Do(http.MethodPost, "/v1/news", tok, b)
	}
	r := e.app.Do(http.MethodGet, "/v1/news?limit=2", tok, nil)
	var page1 []ItemResponse
	r.JSON(t, &page1)
	next := r.Header.Get("X-Next-Cursor")
	if len(page1) != 2 || page1[0].Headline != "Item C" || next == "" {
		t.Fatalf("page1 = %+v cursor %q", page1, next)
	}
	var page2 []ItemResponse
	e.app.Do(http.MethodGet, "/v1/news?limit=2&cursor="+next, tok, nil).JSON(t, &page2)
	if len(page2) != 1 || page2[0].Headline != "Item A" {
		t.Fatalf("page2 = %+v", page2)
	}
}

func TestNewsFeedAndSync(t *testing.T) {
	provider := stubProvider{name: "finnhub", items: []newsprovider.Item{
		{Symbol: "AAPL", Headline: "Apple one", URL: ptr("https://x/a1"), PublishedAt: time.Now().Add(-time.Hour)},
		{Symbol: "MSFT", Headline: "Msft one", URL: ptr("https://x/m1"), PublishedAt: time.Now().Add(-2 * time.Hour)},
		{Symbol: "AAPL", Headline: "   ", PublishedAt: time.Now()},
	}}
	e := newEnv(t, &newsprovider.Chain{Provider: provider}, nil, nil)
	user, tok := e.user(t, "sync")
	seedStock(t, e.app.Deps.DB, user, "AAPL", 1, 100)
	seedWatch(t, e.app.Deps.DB, user, "MSFT", "active")
	seedWatch(t, e.app.Deps.DB, user, "TSLA", "archived")

	var first SyncResponse
	e.app.Do(http.MethodPost, "/v1/news/sync", tok, nil).JSON(t, &first)
	if first.Provider != "finnhub" || first.SymbolsCount != 2 || first.FetchedCount != 3 || first.InsertedCount != 2 || first.SkippedCount != 1 {
		t.Fatalf("first sync = %+v", first)
	}
	var second SyncResponse
	e.app.Do(http.MethodPost, "/v1/news/sync", tok, nil).JSON(t, &second)
	if second.InsertedCount != 0 || second.UpdatedCount+second.SkippedCount != 3 {
		t.Fatalf("second sync = %+v", second)
	}
	var feed []ItemResponse
	e.app.Do(http.MethodGet, "/v1/news/feed", tok, nil).JSON(t, &feed)
	if len(feed) != 2 || feed[0].Headline != "Apple one" {
		t.Fatalf("feed = %+v", feed)
	}

	// Without a provider, sync is 501.
	bare := newEnv(t, nil, nil, nil)
	_, bareTok := bare.user(t, "bare")
	expectError(t, bare.app.Do(http.MethodPost, "/v1/news/sync", bareTok, nil), http.StatusNotImplemented,
		"News provider not configured. Configure FINNHUB_API_KEY to enable Finnhub sync.")
}

func TestRecordNewsView(t *testing.T) {
	e := newEnv(t, nil, nil, nil)
	_, tok := e.user(t, "view")
	expectError(t, e.app.Do(http.MethodPost, "/v1/news/view", tok, map[string]any{"headline": "  "}), http.StatusBadRequest, "headline is required.")
	r := e.app.Do(http.MethodPost, "/v1/news/view", tok, map[string]any{"headline": "Apple beats", "symbol": "AAPL"})
	if r.Status/100 != 2 {
		t.Fatalf("view = %d %s", r.Status, r.Body)
	}
}

// --- Finnhub webhook (StockPlanBackendTests finnhubNewsWebhook*) ---

func TestFinnhubWebhookIngestsTrackedSymbols(t *testing.T) {
	t.Setenv("FINNHUB_WEBHOOK_SECRET", "test-finnhub-secret")
	e := newEnv(t, nil, nil, nil)
	user1, _ := e.user(t, "finnhubnews1")
	user2, _ := e.user(t, "finnhubnews2")
	seedStock(t, e.app.Deps.DB, user1, "AAPL", 5, 100)
	seedWatch(t, e.app.Deps.DB, user1, "MSFT", "active")
	seedWatch(t, e.app.Deps.DB, user2, "AAPL", "active")

	payload := map[string]any{"news": []map[string]any{{
		"category": "company", "datetime": 1_774_884_000, "headline": "Apple launches a new enterprise service",
		"id": 42, "related": "AAPL,MSFT,TSLA", "source": "Reuters", "summary": "Sample webhook article",
		"url": "https://example.com/news/apple-enterprise-service",
	}}}
	post := func(secret string) *testutil.Response {
		raw := mustJSON(t, payload)
		return postWebhook(t, e.app, raw, secret)
	}
	var first, second WebhookResponse
	r := post("test-finnhub-secret")
	if r.Status != http.StatusOK {
		t.Fatalf("first = %d %s", r.Status, r.Body)
	}
	r.JSON(t, &first)
	post("test-finnhub-secret").JSON(t, &second)
	want := WebhookResponse{Provider: "finnhub", ReceivedCount: 1, MatchedSymbolsCount: 2, MatchedUsersCount: 2, InsertedCount: 3}
	if first != want {
		t.Fatalf("first = %+v", first)
	}
	if second.InsertedCount != 0 || second.SkippedCount != 3 {
		t.Fatalf("second = %+v", second)
	}
	symbolsOf := func(id uuid.UUID) []string {
		rows, err := e.app.Deps.DB.Query(context.Background(), `SELECT symbol FROM news_items WHERE user_id = $1 ORDER BY symbol`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	if got := symbolsOf(user1); !reflect.DeepEqual(got, []string{"AAPL", "MSFT"}) {
		t.Fatalf("user1 = %v", got)
	}
	if got := symbolsOf(user2); !reflect.DeepEqual(got, []string{"AAPL"}) {
		t.Fatalf("user2 = %v", got)
	}

	expectError(t, post("wrong"), http.StatusUnauthorized, "Invalid Finnhub webhook secret.")
	expectError(t, post("  "), http.StatusUnauthorized, "Missing Finnhub webhook secret.")
	expectError(t, postWebhook(t, e.app, []byte(`{"other":[]}`), "test-finnhub-secret"),
		http.StatusBadRequest, "Invalid Finnhub webhook payload. Expected a news array under `news`, `data`, `articles`, or a top-level array.")
	// A top-level array is accepted too.
	if r := postWebhook(t, e.app, []byte(`[{"headline":"x","url":"https://example.com/n2","symbol":"ZZZZ"}]`),
		"test-finnhub-secret"); r.Status != http.StatusOK {
		t.Fatalf("array = %d %s", r.Status, r.Body)
	}
}

func TestFinnhubWebhookUnconfigured(t *testing.T) {
	t.Setenv("FINNHUB_WEBHOOK_SECRET", "  ")
	e := newEnv(t, nil, nil, nil)
	expectError(t, postWebhook(t, e.app, []byte(`[]`), "x"),
		http.StatusServiceUnavailable, "FINNHUB_WEBHOOK_SECRET is not configured.")
}

// --- NewsTickerMappingTests / NewsTickerServiceTests ---

func tickerDoc(t *testing.T, stale string) newsprovider.Document {
	t.Helper()
	raw := `{"version":"https://jsonfeed.org/version/1.1","title":"Merged timeline","items":[
	  {"id":"a","url":"https://x/a","title":"A","date_published":"2026-09-17T11:00:00Z","_feeds":{"source_name":"CNBC","source_url":"https://cnbc.com","feed_url":"https://f/a"}},
	  {"id":"b","url":"https://x/b","title":"B","date_published":"2026-09-17T10:00:00Z","_feeds":{"source_name":"Fed","feed_url":"https://f/b"}}],
	 "_feeds":{"warming":[],"stale":[` + stale + `],"generated_at":"2026-09-17T12:00:00Z"}}`
	doc, err := newsprovider.DecodeDocument([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestTickerMapping(t *testing.T) {
	r := tickerResponse(tickerDoc(t, `"https://f/b"`), true, time.Unix(1_789_646_400, 0))
	if !r.Enabled || !r.Stale || len(r.Items) != 2 || r.Items[0].ID != "a" || r.Items[1].ID != "b" {
		t.Fatalf("response = %+v", r)
	}
	if r.Items[0].Source != "CNBC" || *r.Items[0].SourceURL != "https://cnbc.com" ||
		r.Items[0].PublishedAt != "2026-09-17T11:00:00Z" || r.GeneratedAt != "2026-09-17T12:00:00Z" {
		t.Fatalf("item = %+v gen %s", r.Items[0], r.GeneratedAt)
	}
	if got := tickerFeedList([]string{"https://a", "https://b"}, []string{"https://b", "https://c"}); !reflect.DeepEqual(got, []string{"https://a", "https://b", "https://c"}) {
		t.Fatalf("merge = %v", got)
	}
	many := make([]string, 40)
	for i := range many {
		many[i] = "https://f/" + string(rune('A'+i))
	}
	if got := tickerFeedList(many, nil); len(got) != 25 {
		t.Fatalf("cap = %d", len(got))
	}
}

func TestTickerService(t *testing.T) {
	stub := &stubFeeds{doc: tickerDoc(t, "")}
	e := newEnv(t, &newsprovider.Chain{Feeds: stub, TickerFeeds: []string{"https://curated/rss"}}, nil, nil)
	_, tok := e.user(t, "ticker")

	var settings TickerSettings
	e.app.Do(http.MethodGet, "/v1/news/ticker/settings", tok, nil).JSON(t, &settings)
	if !settings.Enabled {
		t.Fatal("default should be enabled")
	}

	// Add a feed: discovered, first candidate stored, duplicates rejected.
	stub.candidates = []newsprovider.FeedCandidate{{URL: "https://mine/feed", Type: "rss", Title: "Mine"}}
	var added TickerFeed
	r := e.app.Do(http.MethodPost, "/v1/news/ticker/feeds", tok, map[string]any{"url": "https://mine"})
	r.JSON(t, &added)
	if added.URL != "https://mine/feed" || *added.Title != "Mine" || !reflect.DeepEqual(stub.discoverCalls, []string{"https://mine"}) {
		t.Fatalf("added = %d %+v", r.Status, added)
	}
	expectError(t, e.app.Do(http.MethodPost, "/v1/news/ticker/feeds", tok, map[string]any{"url": "https://mine"}), http.StatusConflict, "You already follow that feed.")
	expectError(t, e.app.Do(http.MethodPost, "/v1/news/ticker/feeds", tok, map[string]any{"url": "ftp://x"}), http.StatusBadRequest, "Enter a full http(s) URL.")
	stub.candidates = nil
	expectError(t, e.app.Do(http.MethodPost, "/v1/news/ticker/feeds", tok, map[string]any{"url": "https://none"}), http.StatusUnprocessableEntity, "No feed found at that address.")

	// The ticker asks for curated plus the user's own feeds.
	var ticker TickerResponse
	e.app.Do(http.MethodGet, "/v1/news/ticker", tok, nil).JSON(t, &ticker)
	if !ticker.Enabled || len(ticker.Items) != 2 || !reflect.DeepEqual(stub.itemsCalls, [][]string{{"https://curated/rss", "https://mine/feed"}}) {
		t.Fatalf("ticker = %+v calls %v", ticker, stub.itemsCalls)
	}

	// The cap is 10 feeds.
	for i := 1; i < maxUserFeeds; i++ {
		stub.candidates = []newsprovider.FeedCandidate{{URL: "https://f" + string(rune('a'+i)) + "/feed", Title: "F"}}
		if r := e.app.Do(http.MethodPost, "/v1/news/ticker/feeds", tok, map[string]any{"url": "https://site"}); r.Status != http.StatusOK {
			t.Fatalf("add %d = %d %s", i, r.Status, r.Body)
		}
	}
	expectError(t, e.app.Do(http.MethodPost, "/v1/news/ticker/feeds", tok, map[string]any{"url": "https://more"}), http.StatusConflict, "You can follow at most 10 feeds.")
	var list TickerFeedsResponse
	e.app.Do(http.MethodGet, "/v1/news/ticker/feeds", tok, nil).JSON(t, &list)
	if len(list.Feeds) != 10 || list.MaxFeeds != 10 || list.Feeds[0].URL != "https://mine/feed" {
		t.Fatalf("list = %+v", list)
	}
	if r := e.app.Do(http.MethodDelete, "/v1/news/ticker/feeds/"+list.Feeds[0].ID, tok, nil); r.Status != http.StatusNoContent {
		t.Fatalf("remove = %d %s", r.Status, r.Body)
	}
	expectError(t, e.app.Do(http.MethodDelete, "/v1/news/ticker/feeds/"+list.Feeds[0].ID, tok, nil), http.StatusNotFound, "Feed not found.")

	// Disabling short-circuits without calling the aggregator.
	calls := len(stub.itemsCalls)
	e.app.Do(http.MethodPut, "/v1/news/ticker/settings", tok, map[string]any{"enabled": false}).JSON(t, &settings)
	if settings.Enabled {
		t.Fatal("update did not persist")
	}
	e.app.Do(http.MethodGet, "/v1/news/ticker/settings", tok, nil).JSON(t, &settings)
	ticker = TickerResponse{}
	e.app.Do(http.MethodGet, "/v1/news/ticker", tok, nil).JSON(t, &ticker)
	if settings.Enabled || ticker.Enabled || len(ticker.Items) != 0 || len(stub.itemsCalls) != calls {
		t.Fatalf("disabled ticker = %+v settings %+v", ticker, settings)
	}
}

func TestTickerNotConfigured(t *testing.T) {
	e := newEnv(t, nil, nil, nil)
	_, tok := e.user(t, "noticker")
	var settings TickerSettings
	e.app.Do(http.MethodGet, "/v1/news/ticker/settings", tok, nil).JSON(t, &settings)
	var list TickerFeedsResponse
	e.app.Do(http.MethodGet, "/v1/news/ticker/feeds", tok, nil).JSON(t, &list)
	if settings.Enabled || len(list.Feeds) != 0 || list.MaxFeeds != 0 {
		t.Fatalf("settings %+v list %+v", settings, list)
	}
	const reason = "The news ticker is not configured on this server."
	expectError(t, e.app.Do(http.MethodPut, "/v1/news/ticker/settings", tok, map[string]any{"enabled": true}), http.StatusServiceUnavailable, reason)
	expectError(t, e.app.Do(http.MethodPost, "/v1/news/ticker/feeds", tok, map[string]any{"url": "https://x"}), http.StatusServiceUnavailable, reason)
	expectError(t, e.app.Do(http.MethodDelete, "/v1/news/ticker/feeds/"+uuid.NewString(), tok, nil), http.StatusServiceUnavailable, reason)
}

// --- ThesisWatchTests ---

func TestThesisClassifierAndRanker(t *testing.T) {
	event, severity := classify("Company cuts guidance after profit warning", nil)
	if event != "guidance" || severity != severityHigh {
		t.Fatalf("classify = %s/%s", event, severity)
	}
	now := time.Now()
	holding := rank(relHolding, 18, severityHigh, impactChallenges, now, nil, now)
	watch := rank(relWatchlist, 0, severityLow, impactNeutral, now, nil, now)
	if holding <= watch {
		t.Fatalf("holding %v <= watchlist %v", holding, watch)
	}
}

func TestThesisWatchIngestFeedAndNotify(t *testing.T) {
	now := time.Now()
	archive := stubProvider{name: "finnhub", items: []newsprovider.Item{
		{Symbol: "ZZNA", Headline: "ZZNA faces lawsuit over fraud claims", Source: ptr("Desk"), URL: ptr("https://example.com/t1"), PublishedAt: now.Add(-2 * time.Hour)},
		{Symbol: "ZZNB", Headline: "ZZNB launches product", URL: ptr("https://example.com/t2"), Summary: ptr("A new product"), PublishedAt: now.Add(-3 * time.Hour)},
	}}
	pro := stubPro{}
	e := newEnv(t, nil, pro, archive)
	carol, tok := e.user(t, "carol")
	pro[carol] = true
	pool := e.app.Deps.DB
	seedStock(t, pool, carol, "ZZNA", 4, 250)
	seedWatch(t, pool, carol, "ZZNB", "active")
	exec(t, pool, `INSERT INTO research_notes (id, user_id, symbol, title, thesis, risks, catalysts, created_at, updated_at)
		VALUES ($1, $2, 'ZZNA', 'Thesis', 'Grows', 'Regulators could block the merger; lawsuit exposure', 'Strong earnings momentum', now(), now())`, uuid.New(), carol)

	svc := e.service(t)
	svc.IngestOnce(context.Background(), &roundRobin{}, 30)
	var archived int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM market_news_archive WHERE symbol IN ('ZZNA','ZZNB')`).Scan(&archived); err != nil || archived != 2 {
		t.Fatalf("archived = %d %v", archived, err)
	}

	var feed FeedResponse
	e.app.Do(http.MethodGet, "/v1/news/thesis-watch", tok, nil).JSON(t, &feed)
	if !feed.Capabilities.IsPro || len(feed.Items) != 2 {
		t.Fatalf("feed = %+v", feed)
	}
	var lawsuit *Story
	for i := range feed.Items {
		if strings.Contains(feed.Items[i].Headline, "lawsuit") {
			lawsuit = &feed.Items[i]
		}
	}
	if lawsuit == nil || lawsuit.Relationship != relHolding || lawsuit.EventType != "legal" || lawsuit.Exposure == nil {
		t.Fatalf("lawsuit story = %+v", lawsuit)
	}
	expectError(t, e.app.Do(http.MethodGet, "/v1/news/thesis-watch?scope=bogus", tok, nil), http.StatusBadRequest, "Invalid Thesis Watch scope.")

	// Story, feedback and read.
	if r := e.app.Do(http.MethodGet, "/v1/news/thesis-watch/"+lawsuit.ID, tok, nil); r.Status != http.StatusOK {
		t.Fatalf("story = %d %s", r.Status, r.Body)
	}
	if r := e.app.Do(http.MethodPost, "/v1/news/thesis-watch/"+lawsuit.ID+"/feedback", tok, map[string]any{"signal": "relevant"}); r.Status != http.StatusNoContent {
		t.Fatalf("feedback = %d %s", r.Status, r.Body)
	}
	if r := e.app.Do(http.MethodPost, "/v1/news/thesis-watch/"+lawsuit.ID+"/view", tok, nil); r.Status != http.StatusNoContent {
		t.Fatalf("view = %d %s", r.Status, r.Body)
	}
	var story Story
	e.app.Do(http.MethodGet, "/v1/news/thesis-watch/"+lawsuit.ID, tok, nil).JSON(t, &story)
	if !story.IsRead || story.Feedback == nil || *story.Feedback != "relevant" {
		t.Fatalf("story after feedback = %+v", story)
	}

	// Notification preferences; Pro only.
	var prefs NotificationPreferences
	e.app.Do(http.MethodGet, "/v1/news/thesis-watch/notifications", tok, nil).JSON(t, &prefs)
	if prefs.Enabled || prefs.DailyCap != 3 || prefs.Policy != "thesis_challenges_and_major_events" {
		t.Fatalf("prefs = %+v", prefs)
	}
	expectError(t, e.app.Do(http.MethodPut, "/v1/news/thesis-watch/notifications", tok, map[string]any{"enabled": true, "timezone": "Mars/Base"}),
		http.StatusBadRequest, "Invalid timezone identifier.")
	e.app.Do(http.MethodPut, "/v1/news/thesis-watch/notifications", tok, map[string]any{"enabled": true, "timezone": "Europe/Lisbon"}).JSON(t, &prefs)
	if !prefs.Enabled || prefs.Timezone != "Europe/Lisbon" {
		t.Fatalf("updated prefs = %+v", prefs)
	}
	free, freeTok := e.user(t, "free")
	_ = free
	expectError(t, e.app.Do(http.MethodPut, "/v1/news/thesis-watch/notifications", freeTok, map[string]any{"enabled": true, "timezone": "UTC"}),
		http.StatusPaymentRequired, "Thesis Watch push alerts require Pro.")

	// The poller pushes the thesis challenge once.
	channel := &recordingChannel{}
	pub := &inbox.Publisher{Q: notificationsdb.New(pool), Channels: []inbox.Channel{channel}, Logger: slog.New(slog.DiscardHandler)}
	svc.NotifyOnce(context.Background(), pub)
	svc.NotifyOnce(context.Background(), pub)
	if len(channel.sent) == 0 {
		t.Fatal("no thesis notification sent")
	}
	keys := map[string]bool{}
	for _, m := range channel.sent {
		if keys[m.DeduplicationKey] {
			t.Fatalf("duplicate push %s", m.DeduplicationKey)
		}
		keys[m.DeduplicationKey] = true
		if !strings.HasPrefix(m.DeduplicationKey, "thesis-watch:") || m.Kind != inbox.KindThesisWatch {
			t.Fatalf("message = %+v", m)
		}
	}
	if !keys["thesis-watch:"+strings.ToLower(lawsuit.ID)] {
		t.Fatalf("lawsuit story not pushed: %v", keys)
	}
}

func TestRoundRobin(t *testing.T) {
	rr := &roundRobin{}
	syms := []string{"A", "B", "C"}
	if got := rr.next(syms, 2); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatal(got)
	}
	if got := rr.next(syms, 2); !reflect.DeepEqual(got, []string{"C", "A"}) {
		t.Fatal(got)
	}
	if got := rr.next(nil, 2); len(got) != 0 {
		t.Fatal(got)
	}
}

func ptr(s string) *string { return &s }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// postWebhook POSTs a JSON body to the Finnhub webhook with a secret header.
func postWebhook(t *testing.T, app *testutil.App, body []byte, secret string) *testutil.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, app.Server.URL+"/webhooks/finnhub/news", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Finnhub-Secret", secret)
	resp, err := app.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}
