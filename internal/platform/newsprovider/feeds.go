package newsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/httpx"
)

// MaxFeedsPerCall is HTTPFeedsClient.maxFeedsPerCall: the aggregator caps
// one call at 25 feeds.
const MaxFeedsPerCall = 25

// Document is JSONFeedDocument (JSON Feed 1.1 plus the aggregator's
// `_feeds` extension). Only headline fields are kept.
type Document struct {
	Title       string
	HomePageURL *string
	FeedURL     *string
	Items       []FeedItem
	Feeds       *DocumentExtension
}

// FeedItem is JSONFeedItem.
type FeedItem struct {
	ID            string
	URL           *string
	Title         string
	DatePublished time.Time
	Image         *string
	Feeds         ItemExtension
}

// ItemExtension is JSONFeedItemExtension.
type ItemExtension struct {
	SourceName    string
	SourceURL     *string
	FeedURL       string
	DateEstimated *bool
}

// DocumentExtension is JSONFeedExtension.
type DocumentExtension struct {
	Warming     []string
	Stale       []string
	GeneratedAt *time.Time
}

// FeedCandidate is one feed the aggregator found behind a site URL.
type FeedCandidate struct {
	URL   string `json:"url"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// ParseFeedDate is JSONFeedDates.parse: ISO 8601 internet date-time, with or
// without fractional seconds.
func ParseFeedDate(raw string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// errDecode is a JSONFeedDocument decoding failure.
var errDecode = errors.New("feed document")

type wireDocument struct {
	Title       *string          `json:"title"`
	HomePageURL *string          `json:"home_page_url"`
	FeedURL     *string          `json:"feed_url"`
	Items       *[]wireItem      `json:"items"`
	Feeds       *json.RawMessage `json:"_feeds"`
}

type wireItem struct {
	ID            *string  `json:"id"`
	URL           *string  `json:"url"`
	Title         *string  `json:"title"`
	DatePublished *string  `json:"date_published"`
	Image         *string  `json:"image"`
	Feeds         *wireIEx `json:"_feeds"`
}

type wireIEx struct {
	SourceName    *string `json:"source_name"`
	SourceURL     *string `json:"source_url"`
	FeedURL       *string `json:"feed_url"`
	DateEstimated *bool   `json:"date_estimated"`
}

type wireDEx struct {
	Warming     *[]string `json:"warming"`
	Stale       *[]string `json:"stale"`
	GeneratedAt *string   `json:"generated_at"`
}

// DecodeDocument is JSONFeedDocument.decode(from:): required fields must be
// present, dates must parse.
func DecodeDocument(data []byte) (Document, error) {
	var w wireDocument
	if err := json.Unmarshal(data, &w); err != nil {
		return Document{}, fmt.Errorf("%w: %w", errDecode, err)
	}
	if w.Title == nil || w.Items == nil {
		return Document{}, fmt.Errorf("%w: missing title or items", errDecode)
	}
	doc := Document{Title: *w.Title, HomePageURL: w.HomePageURL, FeedURL: w.FeedURL, Items: make([]FeedItem, 0, len(*w.Items))}
	for i, it := range *w.Items {
		if it.ID == nil || it.Title == nil || it.DatePublished == nil || it.Feeds == nil ||
			it.Feeds.SourceName == nil || it.Feeds.FeedURL == nil {
			return Document{}, fmt.Errorf("%w: item %d is missing a required field", errDecode, i)
		}
		published, ok := ParseFeedDate(*it.DatePublished)
		if !ok {
			return Document{}, fmt.Errorf("%w: unparseable date %s", errDecode, *it.DatePublished)
		}
		doc.Items = append(doc.Items, FeedItem{
			ID: *it.ID, URL: it.URL, Title: *it.Title, DatePublished: published, Image: it.Image,
			Feeds: ItemExtension{
				SourceName: *it.Feeds.SourceName, SourceURL: it.Feeds.SourceURL,
				FeedURL: *it.Feeds.FeedURL, DateEstimated: it.Feeds.DateEstimated,
			},
		})
	}
	if w.Feeds != nil && !bytes.Equal(bytes.TrimSpace(*w.Feeds), []byte("null")) {
		var x wireDEx
		if err := json.Unmarshal(*w.Feeds, &x); err != nil {
			return Document{}, fmt.Errorf("%w: %w", errDecode, err)
		}
		if x.Warming == nil || x.Stale == nil {
			return Document{}, fmt.Errorf("%w: _feeds is missing warming or stale", errDecode)
		}
		ext := &DocumentExtension{Warming: *x.Warming, Stale: *x.Stale}
		if x.GeneratedAt != nil {
			t, ok := ParseFeedDate(*x.GeneratedAt)
			if !ok {
				return Document{}, fmt.Errorf("%w: unparseable date %s", errDecode, *x.GeneratedAt)
			}
			ext.GeneratedAt = &t
		}
		doc.Feeds = ext
	}
	return doc, nil
}

// FeedsClient talks to the shared feed aggregator.
type FeedsClient interface {
	// Items is the merged newest-first timeline across feeds.
	Items(ctx context.Context, feeds []string, limit int) (Document, error)
	// Discover returns the feed candidates behind a site or feed URL.
	Discover(ctx context.Context, rawURL string) ([]FeedCandidate, error)
}

// HTTPFeedsClient is HTTPFeedsClient.
type HTTPFeedsClient struct {
	BaseURL string
	Client  *http.Client
}

func (c *HTTPFeedsClient) base() string { return strings.TrimSuffix(c.BaseURL, "/") }

func (c *HTTPFeedsClient) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

// Items implements FeedsClient.
func (c *HTTPFeedsClient) Items(ctx context.Context, feeds []string, limit int) (Document, error) {
	if len(feeds) == 0 {
		return Document{Items: []FeedItem{}}, nil
	}
	if len(feeds) > MaxFeedsPerCall {
		feeds = feeds[:MaxFeedsPerCall]
	}
	u, err := url.Parse(c.base() + "/v1/items")
	if err != nil {
		return Document{}, httpx.Abort(http.StatusInternalServerError, "Invalid FEEDS_BASE_URL configuration.")
	}
	u.RawQuery = "feeds=" + queryEscape(strings.Join(feeds, ",")) + "&limit=" + strconv.Itoa(limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Document{}, httpx.Abort(http.StatusInternalServerError, "Invalid FEEDS_BASE_URL configuration.")
	}
	req.Header.Set("Accept", "application/feed+json, application/json")
	status, body, err := c.do(req)
	if err != nil {
		return Document{}, err
	}
	if status != http.StatusOK {
		return Document{}, httpx.Abort(http.StatusBadGateway,
			fmt.Sprintf("Feed aggregator returned %d for /v1/items: %s", status, bodyText(body)))
	}
	doc, err := DecodeDocument(body)
	if err != nil {
		// A DecodingError was an AbortError (400) in Vapor.
		return Document{}, httpx.BadRequest(err.Error())
	}
	return doc, nil
}

// Discover implements FeedsClient.
func (c *HTTPFeedsClient) Discover(ctx context.Context, rawURL string) ([]FeedCandidate, error) {
	payload, _ := json.Marshal(map[string]string{"url": rawURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/v1/discover", bytes.NewReader(payload))
	if err != nil {
		return nil, httpx.Abort(http.StatusInternalServerError, "Invalid FEEDS_BASE_URL configuration.")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	status, body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		var reply struct {
			Candidates *[]FeedCandidate `json:"candidates"`
		}
		if err := json.Unmarshal(body, &reply); err != nil || reply.Candidates == nil {
			return nil, httpx.BadRequest("Feed discovery returned an unreadable reply.")
		}
		return *reply.Candidates, nil
	case http.StatusBadRequest:
		return nil, httpx.BadRequest("That URL cannot be used as a feed: " + bodyText(body))
	default:
		return nil, httpx.Abort(http.StatusBadGateway,
			fmt.Sprintf("Feed discovery failed with status %d: %s", status, bodyText(body)))
	}
}

func (c *HTTPFeedsClient) do(req *http.Request) (int, []byte, error) {
	resp, err := c.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// bodyText is the first 300 bytes of body, trimmed (empty when not UTF-8).
func bodyText(body []byte) string {
	if len(body) > 300 {
		body = body[:300]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(body), ""))
}

// queryEscape escapes a query value as URLComponents.queryItems did: commas
// stay literal, spaces become %20.
func queryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "+", "%20"), "%2C", ",")
}

// JSONFeed is JSONFeedNewsProvider.
type JSONFeed struct {
	Client             FeedsClient
	GeneralFeeds       []string
	SymbolFeedTemplate string
	// MaxPerSymbol nil means 25 for the batch limit and no per-symbol cap.
	MaxPerSymbol *int
	GeneralLimit int
}

// Name implements Provider.
func (*JSONFeed) Name() string { return "jsonfeed" }

// Fetch implements Provider: per-symbol feeds from SymbolFeedTemplate, in
// batches of MaxFeedsPerCall.
func (p *JSONFeed) Fetch(ctx context.Context, symbols []string) ([]Item, error) {
	if p.SymbolFeedTemplate == "" {
		return []Item{}, nil
	}
	set := map[string]struct{}{}
	for _, s := range symbols {
		set[strings.ToUpper(strings.TrimSpace(s))] = struct{}{}
	}
	sorted := make([]string, 0, len(set))
	for s := range set {
		sorted = append(sorted, s)
	}
	sort.Strings(sorted)
	symbolByFeed := map[string]string{}
	for _, s := range sorted {
		if u, ok := FeedURL(p.SymbolFeedTemplate, s); ok {
			symbolByFeed[u] = s
		}
	}
	if len(symbolByFeed) == 0 {
		return []Item{}, nil
	}
	urls := make([]string, 0, len(symbolByFeed))
	for u := range symbolByFeed {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	per := 25
	if p.MaxPerSymbol != nil {
		per = *p.MaxPerSymbol
	}
	items := []Item{}
	for start := 0; start < len(urls); start += MaxFeedsPerCall {
		batch := urls[start:min(start+MaxFeedsPerCall, len(urls))]
		doc, err := p.Client.Items(ctx, batch, min(200, len(batch)*per))
		if err != nil {
			return nil, err
		}
		items = append(items, MapItems(doc, symbolByFeed, nil, p.MaxPerSymbol)...)
	}
	return items, nil
}

// FetchGeneral implements Provider: the curated market-wide feeds.
func (p *JSONFeed) FetchGeneral(ctx context.Context) ([]Item, error) {
	if len(p.GeneralFeeds) == 0 {
		return []Item{}, nil
	}
	doc, err := p.Client.Items(ctx, p.GeneralFeeds, p.GeneralLimit)
	if err != nil {
		return nil, err
	}
	general := "GENERAL"
	return MapItems(doc, map[string]string{}, &general, nil), nil
}

// FeedURL is JSONFeedNewsProvider.feedURL(template:symbol:): expands
// `{symbol}`, rejecting symbols with characters unsafe in a query value.
func FeedURL(template, symbol string) (string, bool) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if template == "" || symbol == "" || !strings.Contains(template, "{symbol}") {
		return "", false
	}
	for _, r := range symbol {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || r == '.' || r == '-' || r == '=') {
			return "", false
		}
	}
	return strings.ReplaceAll(template, "{symbol}", symbol), true
}

// FeedList splits a comma-separated env value into a trimmed, deduped,
// ordered list.
func FeedList(raw string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// MapItems is JSONFeedMapping.providerItems: an item's symbol is looked up by
// the feed that produced it; items from unmapped feeds get defaultSymbol or
// are dropped when it is nil. maxPerSymbol caps each symbol.
func MapItems(doc Document, symbolByFeed map[string]string, defaultSymbol *string, maxPerSymbol *int) []Item {
	counts := map[string]int{}
	out := []Item{}
	for _, it := range doc.Items {
		symbol, ok := symbolByFeed[it.Feeds.FeedURL]
		if !ok {
			if defaultSymbol == nil {
				continue
			}
			symbol = *defaultSymbol
		}
		if maxPerSymbol != nil && *maxPerSymbol > 0 && counts[symbol] >= *maxPerSymbol {
			continue
		}
		counts[symbol]++
		source := it.Feeds.SourceName
		out = append(out, Item{
			Symbol:      symbol,
			Headline:    it.Title,
			Source:      &source,
			URL:         it.URL,
			Image:       it.Image,
			PublishedAt: it.DatePublished,
		})
	}
	return out
}
