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

// Direct provider quirks, ported faithfully from Vapor's backendAPI
// decoding: StockTwits created_at never decodes (postedAt = now), and the
// Reddit token's access_token never decodes (Reddit is skipped after the
// auth POST).
func TestDirectProviderDecoderQuirks(t *testing.T) {
	fixed := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/st/streams/symbol/TSLA.json":
			if r.URL.Query().Get("limit") != "30" {
				t.Errorf("stocktwits limit %q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"messages":[
				{"id":11,"body":"bullish into earnings","created_at":"2020-01-01T00:00:00Z","user":{"name":"T","username":"trader"},"entities":{"sentiment":{"basic":"Bullish"}}},
				{"id":12,"body":"   "}]}`)
		case "/reddit/token":
			_, _ = io.WriteString(w, `{"access_token":"tok","token_type":"bearer"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	id, secret := "id", "secret"
	d := DirectProvider{
		StocktwitsBaseURL: srv.URL + "/st", RedditAuthURL: srv.URL + "/reddit/token", RedditSearchBase: srv.URL + "/reddit",
		RedditClientID: &id, RedditClientSecret: &secret, RedditUserAgent: "test",
		HTTP: srv.Client(), Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return fixed },
	}
	if !d.Enabled() {
		t.Fatal("direct must always be enabled")
	}
	resp, err := d.FetchTickerPosts(context.Background(), "TSLA", 7, 100)
	if err != nil {
		t.Fatal(err)
	}
	posts := resp.posts()
	if len(posts) != 1 {
		t.Fatalf("posts %+v", posts)
	}
	p := posts[0]
	if *p.EventID != "direct:stocktwits:TSLA:11" || *p.PostedAt != "2026-10-29T12:00:00Z" || *p.Sentiment != positiveLabel ||
		*p.URL != "https://stocktwits.com/trader/message/11" {
		t.Fatalf("post %+v posted=%s", p, *p.PostedAt)
	}
	if hits["/reddit/token"] != 1 {
		t.Fatalf("reddit hits %v", hits)
	}
	for path := range hits {
		if strings.HasPrefix(path, "/reddit/") && path != "/reddit/token" {
			t.Fatalf("reddit search reached: %v", hits)
		}
	}

	// The other Hermes endpoints are 501 on Direct.
	_, err = d.FetchEvents(context.Background(), 7, 10)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusNotImplemented {
		t.Fatalf("events %v", err)
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
