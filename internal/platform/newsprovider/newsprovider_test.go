package newsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/httpx"
)

func fixture(t *testing.T) Document {
	t.Helper()
	data, err := os.ReadFile("testdata/jsonfeed-ticker.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := DecodeDocument(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return doc
}

func statusOf(t *testing.T, err error) (int, string) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) {
		t.Fatalf("want httpx.Error, got %v", err)
	}
	return he.Status, he.Reason
}

// JSONFeedNewsProviderTests.decodesDocument.
func TestDecodeDocument(t *testing.T) {
	doc := fixture(t)
	if doc.Title != "Merged timeline" || len(doc.Items) != 4 {
		t.Fatalf("doc = %q, %d items", doc.Title, len(doc.Items))
	}
	if doc.Items[0].Feeds.SourceName != "CNBC" || *doc.Items[0].Feeds.SourceURL != "https://www.cnbc.com" {
		t.Fatalf("item 0 feeds = %+v", doc.Items[0].Feeds)
	}
	if *doc.Items[0].Image != "https://pub.example/a.jpg" {
		t.Fatalf("image = %v", *doc.Items[0].Image)
	}
	if got := float64(doc.Items[3].DatePublished.UnixNano()) / 1e9; got != 1_789_633_800.5 {
		t.Fatalf("fractional date = %v", got)
	}
	if doc.Feeds == nil || len(doc.Feeds.Warming) != 1 {
		t.Fatalf("feeds ext = %+v", doc.Feeds)
	}
}

func TestDecodeDocumentRequiresFields(t *testing.T) {
	for _, raw := range []string{
		`{"items":[]}`,
		`{"title":"t"}`,
		`{"title":"t","items":[{"id":"a","title":"x","_feeds":{"source_name":"s","feed_url":"f"}}]}`,
		`{"title":"t","items":[{"id":"a","title":"x","date_published":"nope","_feeds":{"source_name":"s","feed_url":"f"}}]}`,
	} {
		if _, err := DecodeDocument([]byte(raw)); err == nil {
			t.Errorf("decoded %s", raw)
		}
	}
}

// JSONFeedNewsProviderTests.mapsGeneral.
func TestMapItemsGeneral(t *testing.T) {
	general := "GENERAL"
	items := MapItems(fixture(t), map[string]string{}, &general, nil)
	if len(items) != 4 {
		t.Fatalf("len = %d", len(items))
	}
	it := items[0]
	if it.Symbol != "GENERAL" || it.Headline != "Fed holds rates" || *it.Source != "CNBC" ||
		*it.URL != "https://pub.example/story" || *it.Image != "https://pub.example/a.jpg" || it.Summary != nil {
		t.Fatalf("item = %+v", it)
	}
	if !it.PublishedAt.Equal(time.Unix(1_789_642_800, 0)) {
		t.Fatalf("publishedAt = %v", it.PublishedAt)
	}
}

// JSONFeedNewsProviderTests.mapsSymbolsByFeedURL.
func TestMapItemsBySymbolFeed(t *testing.T) {
	bySymbol := map[string]string{
		"https://feeds.finance.yahoo.com/rss/2.0/headline?s=AAPL&region=US&lang=en-US": "AAPL",
		"https://feeds.finance.yahoo.com/rss/2.0/headline?s=MSFT&region=US&lang=en-US": "MSFT",
	}
	one := 1
	items := MapItems(fixture(t), bySymbol, nil, &one)
	got := []string{}
	for _, it := range items {
		got = append(got, it.Symbol)
	}
	if !reflect.DeepEqual(got, []string{"AAPL", "MSFT"}) || items[0].Headline != "Apple beats" {
		t.Fatalf("items = %+v", items)
	}
}

// JSONFeedNewsProviderTests.symbolFeedURLs.
func TestFeedURL(t *testing.T) {
	tpl := "https://feeds.finance.yahoo.com/rss/2.0/headline?s={symbol}&region=US"
	if u, ok := FeedURL(tpl, "aapl"); !ok || u != "https://feeds.finance.yahoo.com/rss/2.0/headline?s=AAPL&region=US" {
		t.Fatalf("aapl = %q %v", u, ok)
	}
	if u, ok := FeedURL(tpl, "BRK.B"); !ok || u != "https://feeds.finance.yahoo.com/rss/2.0/headline?s=BRK.B&region=US" {
		t.Fatalf("BRK.B = %q %v", u, ok)
	}
	if _, ok := FeedURL(tpl, "a b/c"); ok {
		t.Fatal("a b/c accepted")
	}
	if _, ok := FeedURL("", "AAPL"); ok {
		t.Fatal("empty template accepted")
	}
}

// JSONFeedNewsProviderTests.curatedList.
func TestFeedList(t *testing.T) {
	if got := FeedList(" https://a/rss , https://b/rss,https://a/rss,, "); !reflect.DeepEqual(got, []string{"https://a/rss", "https://b/rss"}) {
		t.Fatalf("got %v", got)
	}
	if got := FeedList(""); len(got) != 0 || got == nil {
		t.Fatalf("empty = %#v", got)
	}
}

// NewsProviderSelectionTests.parse.
func TestParseNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{"finnhub"}},
		{" Finnhub , jsonfeed ,finnhub", []string{"finnhub", "jsonfeed"}},
		{"rss", []string{"jsonfeed"}},
		{"yahoo_rss,finnhub", []string{"jsonfeed", "finnhub"}},
		{"bogus", []string{}},
	}
	for _, c := range cases {
		if got := ParseNames(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseNames(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

type stubProvider struct {
	name  string
	items []Item
	err   error
}

func (s stubProvider) Name() string { return s.name }
func (s stubProvider) Fetch(context.Context, []string) ([]Item, error) {
	return s.items, s.err
}

func (s stubProvider) FetchGeneral(context.Context) ([]Item, error) {
	return s.items, s.err
}

// NewsProviderSelectionTests.build.
func TestBuildChain(t *testing.T) {
	fh := stubProvider{name: "finnhub"}
	jf := stubProvider{name: "jsonfeed"}
	if p := BuildChain([]string{"finnhub"}, nil, nil, nil); p != nil {
		t.Fatalf("want nil, got %v", p)
	}
	if p := BuildChain([]string{"finnhub"}, fh, jf, nil); p.Name() != "finnhub" {
		t.Fatalf("got %s", p.Name())
	}
	if p := BuildChain([]string{"jsonfeed", "finnhub"}, nil, jf, nil); p.Name() != "jsonfeed" {
		t.Fatalf("got %s", p.Name())
	}
	if p := BuildChain([]string{"jsonfeed", "finnhub"}, fh, jf, nil); p.Name() != "composite(jsonfeed,finnhub)" {
		t.Fatalf("got %s", p.Name())
	}
}

func str(s string) *string { return &s }

func TestCompositeDedupesAndFails(t *testing.T) {
	a := stubProvider{name: "a", items: []Item{{Symbol: "X", Headline: "one", URL: str("https://Same")}, {Symbol: "X", Headline: "no url"}}}
	b := stubProvider{name: "b", items: []Item{{Symbol: "X", Headline: "dup", URL: str("https://same")}, {Symbol: "X", Headline: "two", URL: str("https://other")}}}
	bad := stubProvider{name: "bad", err: errors.New("boom")}
	c := &Composite{Providers: []Provider{a, bad, b}}
	items, err := c.Fetch(context.Background(), []string{"X"})
	if err != nil {
		t.Fatal(err)
	}
	heads := []string{}
	for _, it := range items {
		heads = append(heads, it.Headline)
	}
	if !reflect.DeepEqual(heads, []string{"one", "no url", "two"}) {
		t.Fatalf("heads = %v", heads)
	}
	_, err = (&Composite{Providers: []Provider{bad, bad}}).FetchGeneral(context.Background())
	if code, reason := statusOf(t, err); code != http.StatusBadGateway || reason != "All news providers failed." {
		t.Fatalf("got %d %q", code, reason)
	}
}

func TestBuildDefaults(t *testing.T) {
	c := Build(Settings{}, nil, nil)
	if c.Provider != nil || c.Feeds != nil || len(c.TickerFeeds) != 0 {
		t.Fatalf("empty settings chain = %+v", c)
	}
	c = Build(Settings{FinnhubAPIKey: "k"}, nil, nil)
	fh, ok := c.Provider.(*Finnhub)
	if !ok || fh.BaseURL != DefaultFinnhubBaseURL {
		t.Fatalf("finnhub default = %+v", c.Provider)
	}
	c = Build(Settings{FinnhubAPIKey: "k", FinnhubBaseURL: "http://stub", FeedsBaseURL: "http://feeds", NewsProviders: "jsonfeed,finnhub", NewsTickerFeeds: "https://a"}, nil, nil)
	if c.Provider.Name() != "composite(jsonfeed,finnhub)" || c.Feeds == nil || !reflect.DeepEqual(c.TickerFeeds, []string{"https://a"}) {
		t.Fatalf("chain = %+v", c)
	}
	if c.Provider.(*Composite).Providers[1].(*Finnhub).BaseURL != "http://stub" {
		t.Fatal("FINNHUB_BASE_URL override ignored")
	}
}

func TestFinnhubFetch(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.Header.Get("X-Finnhub-Token") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/company-news":
			sym := r.URL.Query().Get("symbol")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"headline": sym + " 1", "url": "https://x/" + sym + "1", "datetime": 1_774_884_000, "source": "Reuters"},
				{"headline": sym + " 2", "url": "https://x/" + sym + "2", "datetime": 1_774_884_001},
			})
		case "/news":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"headline": "Macro", "url": "https://x/m", "datetime": 1_774_884_000, "related": "aapl, msft,TOOLONGX,brk.b"},
			})
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("nope"))
		}
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	f := &Finnhub{BaseURL: srv.URL, APIKey: "key", LookbackDays: 7, MaxArticlesPerSymbol: 1, Client: srv.Client(), Now: func() time.Time { return now }}
	items, err := f.Fetch(context.Background(), []string{" msft", "aapl", "AAPL", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Symbol != "AAPL" || items[1].Symbol != "MSFT" || items[0].Headline != "AAPL 1" {
		t.Fatalf("items = %+v", items)
	}
	if !items[0].PublishedAt.Equal(time.Unix(1_774_884_000, 0)) || *items[0].Source != "Reuters" {
		t.Fatalf("item 0 = %+v", items[0])
	}
	if paths[0] != "/company-news?symbol=AAPL&from=2026-10-04&to=2026-10-10" {
		t.Fatalf("path = %s", paths[0])
	}
	general, err := f.FetchGeneral(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if general[0].Symbol != "GENERAL" || !reflect.DeepEqual(general[0].RelatedSymbols, []string{"AAPL", "MSFT", "BRK.B"}) {
		t.Fatalf("general = %+v", general[0])
	}

	f.APIKey = "wrong"
	_, err = f.FetchGeneral(context.Background())
	if code, reason := statusOf(t, err); code != http.StatusBadGateway || reason != "Finnhub rejected the request. Check FINNHUB_API_KEY." {
		t.Fatalf("401 -> %d %q", code, reason)
	}
	f.APIKey = " "
	_, err = f.FetchGeneral(context.Background())
	if code, reason := statusOf(t, err); code != http.StatusServiceUnavailable || reason != "FINNHUB_API_KEY is not configured." {
		t.Fatalf("no key -> %d %q", code, reason)
	}
	f.APIKey = "key"
	f.BaseURL = srv.URL + "/other"
	_, err = f.FetchGeneral(context.Background())
	if code, reason := statusOf(t, err); code != http.StatusBadGateway || !strings.HasSuffix(reason, "with status 418: nope") {
		t.Fatalf("418 -> %d %q", code, reason)
	}
}

func TestHTTPFeedsClientAndJSONFeedProvider(t *testing.T) {
	data, err := os.ReadFile("testdata/jsonfeed-ticker.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotFeeds []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/items":
			gotFeeds = append(gotFeeds, r.URL.Query().Get("feeds"))
			_, _ = w.Write(data)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/discover":
			_, _ = w.Write([]byte(`{"candidates":[{"url":"https://site/feed.xml","type":"rss","title":"Site"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := &HTTPFeedsClient{BaseURL: srv.URL + "/", Client: srv.Client()}
	cands, err := client.Discover(context.Background(), "https://site")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].URL != "https://site/feed.xml" {
		t.Fatalf("candidates = %+v", cands)
	}
	two := 2
	p := &JSONFeed{
		Client:             client,
		GeneralFeeds:       []string{"https://www.cnbc.com/id/100003114/device/rss/rss.html"},
		SymbolFeedTemplate: "https://feeds.finance.yahoo.com/rss/2.0/headline?s={symbol}&region=US&lang=en-US",
		MaxPerSymbol:       &two,
		GeneralLimit:       60,
	}
	items, err := p.Fetch(context.Background(), []string{"msft", "aapl"})
	if err != nil {
		t.Fatal(err)
	}
	syms := []string{}
	for _, it := range items {
		syms = append(syms, it.Symbol)
	}
	if !reflect.DeepEqual(syms, []string{"AAPL", "AAPL", "MSFT"}) {
		t.Fatalf("symbols = %v", syms)
	}
	general, err := p.FetchGeneral(context.Background())
	if err != nil || len(general) != 4 || general[0].Symbol != "GENERAL" {
		t.Fatalf("general = %+v, %v", general, err)
	}
	if len(gotFeeds) != 2 {
		t.Fatalf("aggregator calls = %v", gotFeeds)
	}
}
