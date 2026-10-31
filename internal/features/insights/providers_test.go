package insights

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/httpx"
)

// stocktwitsRedditStub serves one StockTwits stream, the Reddit token and a
// Reddit search, with the providers' own snake_case keys.
func stocktwitsRedditStub(t *testing.T) (*httptest.Server, func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/st/streams/symbol/"):
			if r.URL.Query().Get("limit") != "30" {
				t.Errorf("stocktwits limit %q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"messages":[
				{"id":11,"body":"bullish into earnings","created_at":"2020-01-01T00:00:00Z","user":{"name":"T","username":"trader"},"entities":{"sentiment":{"basic":"Bullish"}}},
				{"id":12,"body":"   "}]}`)
		case r.URL.Path == "/reddit/token":
			_, _ = io.WriteString(w, `{"access_token":"tok","token_type":"bearer"}`)
		case strings.HasPrefix(r.URL.Path, "/reddit/r/") && strings.HasSuffix(r.URL.Path, "/search"):
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("reddit auth %q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"data":{"children":[
				{"data":{"id":"abc","title":"TSLA to the moon","selftext":"rally","author":"u1","subreddit":"stocks","permalink":"/r/stocks/abc","created_utc":1577836800}}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range hits {
			out[k] = v
		}
		return out
	}
}

func directFor(srv *httptest.Server, now time.Time) DirectProvider {
	id, secret := "id", "secret"
	return DirectProvider{
		StocktwitsBaseURL: srv.URL + "/st", RedditAuthURL: srv.URL + "/reddit/token", RedditSearchBase: srv.URL + "/reddit",
		RedditClientID: &id, RedditClientSecret: &secret, RedditUserAgent: "test",
		HTTP: srv.Client(), Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}
}

// StockTwits and Reddit are decoded with their own key spellings. Vapor's
// backendAPI decoder rewrote snake_case keys, so StockTwits created_at never
// decoded (postedAt was now) and the Reddit token never decoded (Reddit was
// skipped after the auth POST).
func TestDirectProviderDecodesProviderKeys(t *testing.T) {
	fixed := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	srv, hits := stocktwitsRedditStub(t)
	d := directFor(srv, fixed)
	if !d.Enabled() {
		t.Fatal("direct must always be enabled")
	}
	resp, err := d.FetchTickerPosts(context.Background(), "TSLA", 7, 100)
	if err != nil {
		t.Fatal(err)
	}
	posts := resp.posts()
	if len(posts) != 2 {
		t.Fatalf("posts %+v", posts)
	}
	st, rd := posts[0], posts[1]
	if *st.EventID != "direct:stocktwits:TSLA:11" || *st.PostedAt != "2020-01-01T00:00:00Z" || *st.Sentiment != positiveLabel ||
		*st.URL != "https://stocktwits.com/trader/message/11" {
		t.Fatalf("stocktwits %+v posted=%s", st, *st.PostedAt)
	}
	if *rd.EventID != "direct:reddit:TSLA:abc" || *rd.PostedAt != "2020-01-01T00:00:00Z" || *rd.URL != "https://www.reddit.com/r/stocks/abc" ||
		*rd.AuthorHandle != "r/stocks" {
		t.Fatalf("reddit %+v posted=%s", rd, *rd.PostedAt)
	}
	h := hits()
	if h["/reddit/token"] != 1 || h["/reddit/r/"+strings.Join(equitySubreddits, "+")+"/search"] != 1 {
		t.Fatalf("hits %v", h)
	}

	// The other Hermes endpoints are 501 on Direct.
	_, err = d.FetchEvents(context.Background(), 7, 10)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusNotImplemented {
		t.Fatalf("events %v", err)
	}
}

// xOnly is a Hermes-like provider: the default x-only symbol posts.
type xOnly struct {
	DisabledProvider
	posts []hermesTickerPost
	calls *int
}

func (xOnly) Enabled() bool { return true }
func (xOnly) Label() string { return "HermesInsightsProvider" }
func (p xOnly) FetchTickerPosts(_ context.Context, symbol string, _, _ int) (hermesTickerPostsResponse, error) {
	*p.calls++
	posts := append([]hermesTickerPost(nil), p.posts...)
	return hermesTickerPostsResponse{Symbol: &symbol, Posts: &posts}, nil
}

// The aggregation reaches each provider's own fetchSymbolPosts. In Vapor it
// always ran the protocol default (source x only), so the sp500 and
// trending tiers, whose sources leave out x, fetched nothing, and Direct's
// StockTwits/Reddit/news never fed the roll-up.
func TestFallbackSymbolPostsDispatch(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	srv, hits := stocktwitsRedditStub(t)
	log := slog.New(slog.DiscardHandler)
	var hermesCalls int
	id, text, label, at := "h1", "breakout", "positive", "2026-10-29T10:00:00Z"
	hermes := xOnly{posts: []hermesTickerPost{{EventID: &id, Text: &text, Sentiment: &label, PostedAt: &at}}, calls: &hermesCalls}
	chain := FallbackProvider{Providers: []Provider{hermes, directFor(srv, now)}, Logger: log, Cooldowns: NewCooldownRegistry(time.Hour)}
	clock := func() time.Time { return now }

	if got := strings.Join(sentimentSources(chain), ","); got != "x,stocktwits,reddit" {
		t.Fatalf("sources %s", got)
	}

	// sp500 tier: Hermes serves none of the cheap sources, Direct answers.
	batches, err := fetchSymbolPosts(context.Background(), chain, log, []string{"TSLA"}, tierSources(TierSP500), 7, 100, clock)
	if err != nil || len(batches) != 1 || len(batches[0].Posts) != 2 {
		t.Fatalf("sp500 %+v %v", batches, err)
	}
	if batches[0].Posts[0].Source != SourceStocktwits || batches[0].Posts[1].Source != SourceReddit || hermesCalls != 0 {
		t.Fatalf("sp500 sources %+v hermes=%d", batches[0].Posts, hermesCalls)
	}

	// user tier: Hermes has posts, so Direct is not asked.
	before := hits()["/st/streams/symbol/TSLA.json"]
	batches, err = fetchSymbolPosts(context.Background(), chain, log, []string{"TSLA"}, tierSources(TierUser), 7, 100, clock)
	if err != nil || len(batches) != 1 || len(batches[0].Posts) != 1 || batches[0].Posts[0].Source != SourceX || hermesCalls != 1 {
		t.Fatalf("user %+v %v hermes=%d", batches, err, hermesCalls)
	}
	if hits()["/st/streams/symbol/TSLA.json"] != before {
		t.Fatal("direct asked although Hermes answered")
	}

	// A Hermes that answers with nothing falls through to Direct.
	quiet := xOnly{calls: &hermesCalls}
	chain.Providers[0] = quiet
	batches, err = fetchSymbolPosts(context.Background(), chain, log, []string{"TSLA"}, tierSources(TierUser), 7, 100, clock)
	if err != nil || len(batches) != 1 || len(batches[0].Posts) != 2 || batches[0].Posts[0].Source != SourceStocktwits {
		t.Fatalf("quiet hermes %+v %v", batches, err)
	}

	// Every provider cooling: an error, counted as failed by the caller.
	cooling := FallbackProvider{Providers: []Provider{quiet}, Logger: log, Cooldowns: NewCooldownRegistry(time.Hour)}
	cooling.Cooldowns.BeginCooldown("HermesInsightsProvider")
	if _, err := fetchSymbolPosts(context.Background(), cooling, log, []string{"TSLA"}, tierSources(TierUser), 7, 100, clock); err == nil {
		t.Fatal("all cooling must fail")
	}
}

// A 501 from the Direct provider (last in the chain) no longer replaces a
// real upstream failure as the error the chain reports.
func TestFallbackNotImplementedDoesNotHideRealError(t *testing.T) {
	srv, _ := stocktwitsRedditStub(t)
	down := fixedProvider{label: "A", err: errors.New("hermes down"), calls: new(int)}
	chain := FallbackProvider{Providers: []Provider{down, directFor(srv, time.Now())}, Logger: slog.New(slog.DiscardHandler), Cooldowns: NewCooldownRegistry(time.Hour)}
	if _, err := chain.FetchSummary(context.Background(), 7); err == nil || err.Error() != "hermes down" {
		t.Fatalf("summary %v", err)
	}
	// Only Direct ran: its 501 is the answer.
	only := FallbackProvider{Providers: []Provider{directFor(srv, time.Now())}, Logger: chain.Logger, Cooldowns: NewCooldownRegistry(time.Hour)}
	if _, err := only.FetchSummary(context.Background(), 7); !errNotImplemented(err) {
		t.Fatalf("direct only %v", err)
	}
}

func TestDeepAPIPollingAndCredits(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/search/web":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"maxResults":10`) {
				t.Errorf("body %s", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"requestId":"r1","status":"running","next":{"method":"GET","path":"/v1/requests/r1","afterSecs":0}}`)
		case r.URL.Path == "/v1/requests/r1":
			_, _ = io.WriteString(w, `{"requestId":"r1","status":"succeeded","output":{"results":[
				{"title":"Stocks rally on strong earnings beat","url":"https://n.test/1","snippet":"bullish surge"},
				{"title":"Quiet day","url":"https://n.test/2","snippet":"nothing"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	var slept []time.Duration
	d := DeepAPIProvider{
		APIKey: "test-key", BaseURL: srv.URL, MaxCostUSD: "0.05", HTTP: srv.Client(), Logger: slog.New(slog.DiscardHandler),
		Now:   func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) },
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
	}
	resp, err := d.FetchEvents(context.Background(), 7, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(*resp.Events) != 1 || *(*resp.Events)[0].Topic != "earnings" || len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("events %+v slept %v", resp, slept)
	}
	if len(auth) != 2 || !strings.HasSuffix(auth[1], "Bearer test-key") {
		t.Fatalf("calls %v", auth)
	}

	credits := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"status":"failed","error":{"code":"INSUFFICIENT_CREDITS","message":"balance is zero"}}`)
	}))
	defer credits.Close()
	d.BaseURL, d.HTTP = credits.URL, credits.Client()
	_, err = d.FetchEvents(context.Background(), 7, 10)
	var ce *creditsExhaustedError
	if !errors.As(err, &ce) || ce.err.Status != http.StatusBadGateway {
		t.Fatalf("credits %v", err)
	}
}
