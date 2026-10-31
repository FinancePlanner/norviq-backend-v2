package insights

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
)

// equitySubreddits is DeepAPIInsightsProvider.equitySubreddits.
var equitySubreddits = []string{
	"wallstreetbets", "stocks", "investing", "SecurityAnalysis", "ValueInvesting", "StockMarket", "options",
}

// DirectProvider is DirectAPIInsightsProvider: the last link of the chain,
// talking to StockTwits, Reddit and Finnhub news directly. It serves
// per-symbol posts only (through FetchTickerPosts); the legacy topic
// endpoints answer 501.
type DirectProvider struct {
	StocktwitsBaseURL  string // "https://api.stocktwits.com/api/2"
	RedditAuthURL      string // "https://www.reddit.com/api/v1/access_token"
	RedditSearchBase   string // "https://oauth.reddit.com"
	RedditClientID     *string
	RedditClientSecret *string
	RedditUserAgent    string
	News               *FinnhubNews // nil when FINNHUB_API_KEY is empty
	HTTP               *http.Client
	Logger             *slog.Logger
	Now                func() time.Time
}

func (d DirectProvider) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Enabled is !supportedSentimentSources.isEmpty, which StockTwits makes
// always true.
func (d DirectProvider) Enabled() bool { return len(d.supportedSources()) > 0 }
func (DirectProvider) Label() string   { return "DirectAPIInsightsProvider" }

func (d DirectProvider) supportedSources() []string {
	sources := []string{SourceStocktwits}
	if d.News != nil {
		sources = append(sources, SourceNews)
	}
	if d.RedditClientID != nil && d.RedditClientSecret != nil && *d.RedditClientID != "" && *d.RedditClientSecret != "" {
		sources = append(sources, SourceReddit)
	}
	return sources
}

func notImplementedDirect() error {
	return httpx.Abort(http.StatusNotImplemented, "Direct provider serves per-symbol posts only.")
}

func (DirectProvider) FetchEvents(context.Context, int, int) (hermesEventsResponse, error) {
	return hermesEventsResponse{}, notImplementedDirect()
}

func (DirectProvider) FetchSummary(context.Context, int) (hermesSummaryResponse, error) {
	return hermesSummaryResponse{}, notImplementedDirect()
}

func (DirectProvider) FetchSentiment(context.Context, *string, int) (hermesSentimentResponse, error) {
	return hermesSentimentResponse{}, notImplementedDirect()
}

func (DirectProvider) FetchNetWorth(context.Context) (hermesNetWorthResponse, error) {
	return hermesNetWorthResponse{History: []hermesNetWorthEntry{}}, nil
}

// FetchTickerPosts runs the provider's own symbol-post fetch for one symbol
// and reshapes it as Hermes ticker posts, scored.
func (d DirectProvider) FetchTickerPosts(ctx context.Context, symbol string, days, limit int) (hermesTickerPostsResponse, error) {
	batches := d.fetchSymbolPosts(ctx, []string{symbol}, d.supportedSources(), limit)
	var posts []ingestedPost
	if len(batches) > 0 {
		posts = batches[0].Posts
	}
	out := make([]hermesTickerPost, 0, len(posts))
	for i := range posts {
		p := &posts[i]
		s := scorePost(*p)
		id, text, label := p.DedupeKey, p.Text, s.Label
		posted := formatISO(p.PostedAt)
		out = append(out, hermesTickerPost{
			EventID: &id, Author: p.Author, AuthorHandle: p.AuthorHandle, Text: &text, URL: p.URL,
			Sentiment: &label, SentimentScore: s.Score, Confidence: s.Confidence, PostedAt: &posted,
		})
	}
	n := len(out)
	return hermesTickerPostsResponse{Symbol: &symbol, Days: &days, Count: &n, Posts: &out}, nil
}

// sentimentSources and symbolPosts make the Direct provider's own
// supportedSentimentSources and fetchSymbolPosts the ones the aggregation
// reaches (symbolPostProvider).
func (d DirectProvider) sentimentSources() []string { return d.supportedSources() }

//nolint:unparam // the error is the interface's; Direct skips what fails.
func (d DirectProvider) symbolPosts(ctx context.Context, _ *slog.Logger, symbols, sources []string, _, limit int, _ func() time.Time) ([]symbolPostBatch, error) {
	return d.fetchSymbolPosts(ctx, symbols, sources, limit), nil
}

// fetchSymbolPosts is DirectAPIInsightsProvider.fetchSymbolPosts.
func (d DirectProvider) fetchSymbolPosts(ctx context.Context, symbols, sources []string, limit int) []symbolPostBatch {
	usable := intersect(sources, d.supportedSources())
	if len(usable) == 0 {
		return []symbolPostBatch{}
	}
	has := func(s string) bool {
		for _, u := range usable {
			if u == s {
				return true
			}
		}
		return false
	}

	// One Reddit token per run (try?: a failure just skips Reddit).
	var redditToken *string
	if has(SourceReddit) {
		if tok, err := d.redditAccessToken(ctx); err == nil {
			redditToken = &tok
		}
	}

	newsBySymbol := map[string][]newsItem{}
	if has(SourceNews) && d.News != nil {
		items, err := d.News.Fetch(ctx, symbols)
		if err != nil {
			d.Logger.Warn("direct.symbol-posts news failed error=" + describe(err))
		} else {
			for _, it := range items {
				k := strings.ToUpper(it.Symbol)
				newsBySymbol[k] = append(newsBySymbol[k], it)
			}
		}
	}

	batches := []symbolPostBatch{}
	for _, symbol := range symbols {
		posts := []ingestedPost{}
		if has(SourceStocktwits) {
			got, err := d.fetchStocktwits(ctx, symbol, limit)
			if err != nil {
				d.Logger.Warn(fmt.Sprintf("direct.stocktwits failed symbol=%s error=%s", symbol, describe(err)))
			} else {
				posts = append(posts, got...)
			}
		}
		if has(SourceReddit) && redditToken != nil {
			got, err := d.fetchReddit(ctx, symbol, *redditToken, limit)
			if err != nil {
				d.Logger.Warn(fmt.Sprintf("direct.reddit failed symbol=%s error=%s", symbol, describe(err)))
			} else {
				posts = append(posts, got...)
			}
		}
		if items, ok := newsBySymbol[strings.ToUpper(symbol)]; ok {
			if len(items) > limit {
				items = items[:max(limit, 0)]
			}
			for _, it := range items {
				key := it.Headline
				if it.URL != nil {
					key = *it.URL
				}
				var parts []string
				parts = append(parts, it.Headline)
				if it.Summary != nil {
					parts = append(parts, *it.Summary)
				}
				posts = append(posts, ingestedPost{
					DedupeKey: "direct:news:" + symbol + ":" + key,
					Symbol:    symbol,
					Author:    it.Source,
					Text:      strings.Join(parts, "\n"),
					URL:       it.URL,
					PostedAt:  it.PublishedAt,
					Source:    SourceNews,
				})
			}
		}
		batches = append(batches, symbolPostBatch{Symbol: symbol, Posts: posts})
	}
	return batches
}

// Wire formats, decoded as the providers spell them (encoding/json). Vapor
// ran them through the backendAPI decoder, which rewrites snake_case keys,
// so created_at, access_token and created_utc never matched: StockTwits
// posts lost their timestamp and the Reddit token never decoded.

type stocktwitsStream struct {
	Messages *[]stocktwitsMessage `json:"messages"`
}

type stocktwitsMessage struct {
	ID        *int64  `json:"id"`
	Body      *string `json:"body"`
	CreatedAt *string `json:"created_at"`
	User      *struct {
		Username *string `json:"username"`
		Name     *string `json:"name"`
	} `json:"user"`
	Entities *struct {
		Sentiment *struct {
			Basic *string `json:"basic"`
		} `json:"sentiment"`
	} `json:"entities"`
}

func (s stocktwitsStream) validate() error {
	if s.Messages == nil {
		return errors.New("messages missing")
	}
	for _, m := range *s.Messages {
		if m.ID == nil || m.Body == nil {
			return errors.New("message missing id/body")
		}
	}
	return nil
}

func (d DirectProvider) get(ctx context.Context, rawURL string, header http.Header) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := httpClient(d.HTTP).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// decodeChecked decodes a third-party body with its own key spellings.
func decodeChecked(body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return err
	}
	if v, ok := out.(validator); ok {
		return v.validate()
	}
	return nil
}

func (d DirectProvider) fetchStocktwits(ctx context.Context, symbol string, limit int) ([]ingestedPost, error) {
	u := fmt.Sprintf("%s/streams/symbol/%s.json?limit=%d", d.StocktwitsBaseURL, symbol, min(limit, 30))
	status, body, err := d.get(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		if status == http.StatusTooManyRequests || status == http.StatusNotFound {
			return []ingestedPost{}, nil
		}
		return nil, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("StockTwits returned HTTP %d for %s.", status, symbol))
	}
	var stream stocktwitsStream
	if err := decodeChecked(body, &stream); err != nil {
		return nil, err
	}
	out := []ingestedPost{}
	for _, m := range *stream.Messages {
		text := strings.TrimSpace(*m.Body)
		if text == "" {
			continue
		}
		var stated string
		if m.Entities != nil && m.Entities.Sentiment != nil && m.Entities.Sentiment.Basic != nil {
			stated = strings.ToLower(*m.Entities.Sentiment.Basic)
		}
		var provided *float64
		switch stated {
		case "bullish":
			v := 1.0
			provided = &v
		case "bearish":
			v := -1.0
			provided = &v
		}
		post := ingestedPost{
			DedupeKey: fmt.Sprintf("direct:stocktwits:%s:%d", symbol, *m.ID),
			Symbol:    symbol,
			Text:      text,
			PostedAt:  d.now(),
			Source:    SourceStocktwits,
		}
		if t := parseHermesTimestamp(m.CreatedAt); t != nil {
			post.PostedAt = *t
		}
		if m.User != nil {
			post.Author, post.AuthorHandle = m.User.Name, m.User.Username
			if m.User.Username != nil {
				link := fmt.Sprintf("https://stocktwits.com/%s/message/%d", *m.User.Username, *m.ID)
				post.URL = &link
			}
		}
		if provided != nil {
			label := labelForScore(*provided)
			conf := 0.8
			post.ProvidedScore, post.ProvidedLabel, post.ProvidedConfidence = provided, &label, &conf
		}
		out = append(out, post)
	}
	return out, nil
}

type redditToken struct {
	AccessToken *string `json:"access_token"`
}

func (d DirectProvider) redditAccessToken(ctx context.Context) (string, error) {
	if d.RedditClientID == nil || d.RedditClientSecret == nil {
		return "", httpx.Abort(http.StatusServiceUnavailable, "Reddit credentials are not configured.")
	}
	creds := base64.StdEncoding.EncodeToString([]byte(*d.RedditClientID + ":" + *d.RedditClientSecret))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.RedditAuthURL, strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic "+creds)
	req.Header.Set("User-Agent", d.RedditUserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	resp, err := httpClient(d.HTTP).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", httpx.Abort(http.StatusBadGateway, fmt.Sprintf("Reddit auth returned HTTP %d.", resp.StatusCode))
	}
	var tok redditToken
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == nil {
		return "", errors.New(`keyNotFound("access_token")`)
	}
	return *tok.AccessToken, nil
}

type redditListing struct {
	Data *struct {
		Children *[]struct {
			Data *struct {
				ID         *string  `json:"id"`
				Title      *string  `json:"title"`
				Selftext   *string  `json:"selftext"`
				Author     *string  `json:"author"`
				Subreddit  *string  `json:"subreddit"`
				Permalink  *string  `json:"permalink"`
				CreatedUTC *float64 `json:"created_utc"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

func (l redditListing) validate() error {
	if l.Data == nil || l.Data.Children == nil {
		return errors.New("listing missing data")
	}
	for _, c := range *l.Data.Children {
		if c.Data == nil || c.Data.ID == nil || c.Data.Title == nil {
			return errors.New("post missing id/title")
		}
	}
	return nil
}

// fetchReddit searches the equity subreddits for symbol.
func (d DirectProvider) fetchReddit(ctx context.Context, symbol, token string, limit int) ([]ingestedPost, error) {
	query := alphanumericEscape(symbol + " stock")
	u := fmt.Sprintf("%s/r/%s/search?q=%s&restrict_sr=1&sort=new&t=week&limit=%d",
		d.RedditSearchBase, strings.Join(equitySubreddits, "+"), query, min(limit, 50))
	status, body, err := d.get(ctx, u, http.Header{"Authorization": {"Bearer " + token}, "User-Agent": {d.RedditUserAgent}})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		if status == http.StatusTooManyRequests {
			return []ingestedPost{}, nil
		}
		return nil, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("Reddit returned HTTP %d for %s.", status, symbol))
	}
	var listing redditListing
	if err := decodeChecked(body, &listing); err != nil {
		return nil, err
	}
	out := []ingestedPost{}
	for _, c := range *listing.Data.Children {
		p := c.Data
		parts := []string{*p.Title}
		if p.Selftext != nil {
			parts = append(parts, *p.Selftext)
		}
		combined := strings.TrimSpace(strings.Join(parts, "\n"))
		if combined == "" {
			continue
		}
		post := ingestedPost{
			DedupeKey: "direct:reddit:" + symbol + ":" + *p.ID,
			Symbol:    symbol,
			Author:    p.Author,
			Text:      combined,
			PostedAt:  d.now(),
			Source:    SourceReddit,
		}
		if p.Subreddit != nil {
			h := "r/" + *p.Subreddit
			post.AuthorHandle = &h
		}
		if p.Permalink != nil {
			link := "https://www.reddit.com" + *p.Permalink
			post.URL = &link
		}
		if p.CreatedUTC != nil {
			post.PostedAt = time.Unix(0, int64(*p.CreatedUTC*1e9))
		}
		out = append(out, post)
	}
	return out, nil
}

// alphanumericEscape is addingPercentEncoding(withAllowedCharacters:
// .alphanumerics) for ASCII input.
func alphanumericEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// newsItem is ProviderNewsItem.
type newsItem struct {
	Symbol      string
	Headline    string
	Source      *string
	URL         *string
	Summary     *string
	PublishedAt time.Time
}

// FinnhubNews is the company-news half of FinnhubNewsProvider (News
// domain), which the direct provider used for the news source.
type FinnhubNews struct {
	BaseURL string // "https://finnhub.io/api/v1"
	APIKey  string
	HTTP    *http.Client
	Now     func() time.Time
}

type finnhubArticle struct {
	Datetime *float64 `json:"datetime"`
	Headline *string  `json:"headline"`
	Source   *string  `json:"source"`
	Summary  *string  `json:"summary"`
	URL      *string  `json:"url"`
}

// Fetch is FinnhubNewsProvider.fetch(symbols:): /company-news for each
// distinct symbol over the last 7 UTC days, at most 25 articles each.
func (f *FinnhubNews) Fetch(ctx context.Context, symbols []string) ([]newsItem, error) {
	set := map[string]struct{}{}
	for _, s := range symbols {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			set[s] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(set))
	for s := range set {
		normalized = append(normalized, s)
	}
	sort.Strings(normalized)
	if len(normalized) == 0 {
		return []newsItem{}, nil
	}
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	today := now().UTC()
	from, to := today.AddDate(0, 0, -6).Format("2006-01-02"), today.Format("2006-01-02")

	items := []newsItem{}
	for _, symbol := range normalized {
		if strings.TrimSpace(f.APIKey) == "" {
			return nil, httpx.Abort(http.StatusServiceUnavailable, "FINNHUB_API_KEY is not configured.")
		}
		path := "/company-news"
		u := strings.TrimSuffix(f.BaseURL, "/") + path + "?" + encodeQueryItems([][2]string{{"symbol", symbol}, {"from", from}, {"to", to}})
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, httpx.Abort(http.StatusInternalServerError, "Invalid Finnhub base URL configuration.")
		}
		req.Header.Set("X-Finnhub-Token", f.APIKey)
		req.Header.Set("Accept", "application/json")
		resp, err := httpClient(f.HTTP).Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusOK:
		case http.StatusNotFound:
			return nil, httpx.Abort(http.StatusNotFound, "Finnhub resource not found for "+path+".")
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, httpx.Abort(http.StatusBadGateway, "Finnhub rejected the request. Check FINNHUB_API_KEY.")
		default:
			text := strings.TrimSpace(string(body))
			reason := fmt.Sprintf("Finnhub request failed for %s with status %d.", path, resp.StatusCode)
			if text != "" {
				reason = fmt.Sprintf("Finnhub request failed for %s with status %d: %s", path, resp.StatusCode, text)
			}
			return nil, httpx.Abort(http.StatusBadGateway, reason)
		}
		var payload []finnhubArticle
		if err := apijson.Unmarshal(body, &payload); err != nil {
			return nil, httpx.Abort(http.StatusBadGateway, "Failed to decode Finnhub response for "+path+".")
		}
		for i, a := range payload {
			if i >= 25 {
				break
			}
			it := newsItem{Symbol: symbol, Source: a.Source, URL: a.URL, Summary: a.Summary, PublishedAt: now()}
			if a.Headline != nil {
				it.Headline = *a.Headline
			}
			if a.Datetime != nil {
				it.PublishedAt = time.Unix(0, int64(*a.Datetime*1e9))
			}
			items = append(items, it)
		}
	}
	return items, nil
}
