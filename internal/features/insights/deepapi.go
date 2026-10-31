package insights

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
)

// DeepAPIProvider is DeepAPIInsightsProvider: web search (and, when
// DEEPAPI_SOCIAL_SCRAPING_ENABLED, X/Reddit scrapes) through DeepAPI.
//
// Its per-symbol multi-source ingest (fetchSymbolPosts override, X/Reddit/
// page scrapes keyed on stable ids) is not ported: Vapor only ever reached
// the protocol-extension default (see fetchSymbolPosts), so that code never
// ran outside unit tests. DeepAPI therefore keeps the default here too
// (source x through FetchTickerPosts); porting the scrapes would add paid
// DeepAPI calls for the sp500 and trending tiers (docs/vapor-bugs/social.md).
type DeepAPIProvider struct {
	APIKey                string
	BaseURL               string
	MaxCostUSD            string
	SocialScrapingEnabled bool
	HTTP                  *http.Client
	Logger                *slog.Logger
	Now                   func() time.Time
	// Sleep waits between polls; nil uses a context-aware timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (DeepAPIProvider) Enabled() bool { return true }
func (DeepAPIProvider) Label() string { return "DeepAPIInsightsProvider" }

func (d DeepAPIProvider) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// swiftTimestamp is Date().timeIntervalSince1970 interpolated into a string.
func swiftTimestamp(t time.Time) string {
	secs := float64(t.UnixNano()) / 1e9
	return strconv.FormatFloat(secs, 'f', -1, 64)
}

func (d DeepAPIProvider) FetchEvents(ctx context.Context, days, limit int) (hermesEventsResponse, error) {
	resp, err := d.searchWeb(ctx, buildFinancialEventsQuery(days), limit)
	if err != nil {
		return hermesEventsResponse{}, err
	}
	events := []hermesEvent{}
	for i, r := range resp.results() {
		s := extractSentiment(r)
		if s == nil {
			continue
		}
		now := d.now()
		id := fmt.Sprintf("deepapi-web-%d-%s", i, swiftTimestamp(now))
		source := "deepapi-web"
		topic := categorizeFinancialTopic(*r.Title + " " + r.snippetText())
		observed := formatISO(now)
		if r.DateText != nil {
			observed = *r.DateText
		}
		ingested := formatISO(now)
		events = append(events, hermesEvent{
			EventID:    &id,
			Source:     &source,
			Topic:      &topic,
			ObservedAt: &observed,
			IngestedAt: &ingested,
			Payload:    &hermesEventPayload{Title: r.Title, Summary: r.Snippet, URL: r.URL},
			Sentiment:  s,
		})
	}
	n := len(events)
	return hermesEventsResponse{Count: &n, Events: &events}, nil
}

func (d DeepAPIProvider) FetchSummary(ctx context.Context, days int) (hermesSummaryResponse, error) {
	resp, err := d.searchWeb(ctx, buildFinancialEventsQuery(days), 10)
	if err != nil {
		return hermesSummaryResponse{}, err
	}
	byTopic := map[string]int{}
	for _, r := range resp.results() {
		byTopic[categorizeFinancialTopic(*r.Title+" "+r.snippetText())]++
	}
	total := 0
	for _, v := range byTopic {
		total += v
	}
	return hermesSummaryResponse{WindowDays: &days, TotalEvents: &total, ByTopic: byTopic}, nil
}

func (d DeepAPIProvider) FetchSentiment(ctx context.Context, topic *string, days int) (hermesSentimentResponse, error) {
	searchTopic := "global financial markets"
	if topic != nil {
		searchTopic = *topic
	}
	var scores []float64
	labelCounts := map[string]int{positiveLabel: 0, neutralLabel: 0, negativeLabel: 0}
	total := 0

	resp, err := d.searchWeb(ctx, searchTopic+" bullish bearish sentiment market outlook forecast", 10)
	if err != nil {
		return hermesSentimentResponse{}, err
	}
	total += len(resp.results())
	for _, r := range resp.results() {
		if s := extractSentiment(r); s != nil {
			scores = append(scores, *s.Score)
			labelCounts[*s.Label]++
		}
	}

	if d.SocialScrapingEnabled {
		tw, err := d.twitterSearch(ctx, searchTopic+" stock market OR crypto OR forex lang:en", 50)
		if err != nil {
			return hermesSentimentResponse{}, err
		}
		for _, p := range tw {
			s := analyzeTweetSentiment(p.Text)
			scores = append(scores, *s.Score)
			labelCounts[*s.Label]++
		}
		total += len(tw)

		rd, err := d.redditSearch(ctx, deepAPIRedditSearchRequest{
			Query:             searchTopic,
			Subreddits:        []string{"wallstreetbets", "stocks", "investing", "SecurityAnalysis", "ValueInvesting", "CryptoCurrency", "Forex"},
			Sort:              "hot",
			Since:             "week",
			MaxItems:          50,
			MaxCostUSD:        d.MaxCostUSD,
			WaitForFinishSecs: 60,
		})
		if err != nil {
			return hermesSentimentResponse{}, err
		}
		for _, p := range rd {
			score, label := analyzeRedditPost(p)
			scores = append(scores, score)
			labelCounts[label]++
		}
		total += len(rd)
	}

	avg := 0.0
	if len(scores) > 0 {
		sum := 0.0
		for _, s := range scores {
			sum += s
		}
		avg = sum / float64(len(scores))
	}
	return hermesSentimentResponse{
		Topic: topic, WindowDays: &days, Count: &total, LabelCounts: labelCounts,
		AverageScore: &avg, Sampled: &total,
	}, nil
}

// FetchNetWorth: DeepAPI has no net-worth data.
func (DeepAPIProvider) FetchNetWorth(context.Context) (hermesNetWorthResponse, error) {
	return hermesNetWorthResponse{History: []hermesNetWorthEntry{}}, nil
}

func (d DeepAPIProvider) FetchTickerPosts(ctx context.Context, symbol string, days, limit int) (hermesTickerPostsResponse, error) {
	resp, err := d.searchWeb(ctx, symbol+" stock sentiment analyst price target outlook", min(limit, 10))
	if err != nil {
		return hermesTickerPostsResponse{}, err
	}
	posts := []hermesTickerPost{}
	for i, r := range resp.results() {
		s := extractSentiment(r)
		if s == nil {
			continue
		}
		confidence := math.Min(1.0, math.Max(0.0, math.Abs(*s.Score)*2.0))
		id := fmt.Sprintf("deepapi-ticker-%s-%d-%s", symbol, i, swiftTimestamp(d.now()))
		text := r.snippetText()
		posted := formatISO(d.now())
		if r.DateText != nil {
			posted = *r.DateText
		}
		posts = append(posts, hermesTickerPost{
			EventID: &id, Text: &text, URL: r.URL, Sentiment: s.Label,
			SentimentScore: s.Score, Confidence: &confidence, PostedAt: &posted,
		})
	}
	if d.SocialScrapingEnabled {
		query := fmt.Sprintf("$%s OR #%s OR \"%s stock\" lang:en", symbol, symbol, symbol)
		tw, err := d.twitterSearch(ctx, query, limit)
		if err != nil {
			return hermesTickerPostsResponse{}, err
		}
		for i, p := range tw {
			s := analyzeTweetSentiment(p.Text)
			confidence := math.Min(1.0, math.Max(0.0, math.Abs(*s.Score)*2.0))
			id := fmt.Sprintf("deepapi-twitter-%s-%d-%s", symbol, i, swiftTimestamp(d.now()))
			text := p.Text
			post := hermesTickerPost{
				EventID: &id, Text: &text, URL: p.URL, Sentiment: s.Label,
				SentimentScore: s.Score, Confidence: &confidence,
			}
			if p.Author != nil {
				post.Author, post.AuthorHandle = p.Author.Name, p.Author.Handle
			}
			posted := formatISO(d.now())
			if p.CreatedAt != nil {
				posted = *p.CreatedAt
			}
			post.PostedAt = &posted
			posts = append(posts, post)
		}
	}
	n := len(posts)
	return hermesTickerPostsResponse{Symbol: &symbol, Days: &days, Count: &n, Posts: &posts}, nil
}

// MARK: - DeepAPI wire types

type deepAPIWebSearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"maxResults"`
	MaxCostUSD string `json:"maxCostUsd"`
	DryRun     *bool  `json:"dryRun,omitempty"`
}

type deepAPISearchResult struct {
	Title    *string `json:"title"`
	URL      *string `json:"url"`
	Snippet  *string `json:"snippet"`
	DateText *string `json:"dateText"`
}

func (r deepAPISearchResult) snippetText() string {
	if r.Snippet == nil {
		return ""
	}
	return *r.Snippet
}

type deepAPIWebSearchOutput struct {
	Results *[]deepAPISearchResult `json:"results"`
}

func (o deepAPIWebSearchOutput) results() []deepAPISearchResult {
	if o.Results == nil {
		return nil
	}
	return *o.Results
}

func (o deepAPIWebSearchOutput) validate() error {
	if o.Results == nil {
		return errors.New("results missing")
	}
	for _, r := range *o.Results {
		if r.Title == nil || r.URL == nil {
			return errors.New("result missing title/url")
		}
	}
	return nil
}

type deepAPITwitterSearchRequest struct {
	Query             string `json:"query"`
	MaxItems          int    `json:"maxItems"`
	Sort              string `json:"sort"`
	MaxCostUSD        string `json:"maxCostUsd"`
	WaitForFinishSecs int    `json:"waitForFinishSecs"`
}

type deepAPITwitterAuthor struct {
	Handle    *string `json:"handle"`
	Name      *string `json:"name"`
	Followers *int    `json:"followers"`
}

type deepAPITwitterPost struct {
	ID        *string               `json:"id"`
	TextPtr   *string               `json:"text"`
	URL       *string               `json:"url"`
	Author    *deepAPITwitterAuthor `json:"author"`
	CreatedAt *string               `json:"createdAt"`
	Text      string                `json:"-"`
}

type deepAPIRedditSearchRequest struct {
	Query             string   `json:"query"`
	Subreddits        []string `json:"subreddits"`
	Sort              string   `json:"sort"`
	Since             string   `json:"since"`
	MaxItems          int      `json:"maxItems"`
	MaxCostUSD        string   `json:"maxCostUsd"`
	WaitForFinishSecs int      `json:"waitForFinishSecs"`
}

type deepAPIRedditPost struct {
	ID    *string `json:"id"`
	Title *string `json:"title"`
	Text  *string `json:"text"`
}

type deepAPINextAction struct {
	Method    *string  `json:"method"`
	Path      *string  `json:"path"`
	AfterSecs *float64 `json:"afterSecs"`
}

type deepAPIErrorBody struct {
	Code    *string `json:"code"`
	Message *string `json:"message"`
}

type deepAPIEnvelope struct {
	RequestID *string            `json:"requestId"`
	Status    *string            `json:"status"`
	Output    json.RawMessage    `json:"output"`
	Next      *deepAPINextAction `json:"next"`
	Error     *deepAPIErrorBody  `json:"error"`
}

func (e deepAPIEnvelope) validate() error {
	if e.Status == nil {
		return errors.New("status missing")
	}
	if e.Next != nil && (e.Next.Method == nil || e.Next.Path == nil) {
		return errors.New("next incomplete")
	}
	if e.Error != nil && (e.Error.Code == nil || e.Error.Message == nil) {
		return errors.New("error incomplete")
	}
	return nil
}

func (e deepAPIEnvelope) hasOutput() bool {
	trimmed := bytes.TrimSpace(e.Output)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

const deepAPIMaxPolls = 12

// creditExhaustionCodes are the DeepAPI error codes for an exhausted balance.
var creditExhaustionCodes = map[string]bool{
	"insufficient_credits": true,
	"payment_required":     true,
	"quota_exceeded":       true,
}

func (d DeepAPIProvider) searchWeb(ctx context.Context, query string, maxResults int) (deepAPIWebSearchOutput, error) {
	body := deepAPIWebSearchRequest{Query: query, MaxResults: min(max(maxResults, 10), 10), MaxCostUSD: d.MaxCostUSD}
	var out deepAPIWebSearchOutput
	ok, err := d.send(ctx, "/v1/search/web", body, &out)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, httpx.Abort(http.StatusBadGateway, "DeepAPI web search returned no output.")
	}
	return out, nil
}

func (d DeepAPIProvider) twitterSearch(ctx context.Context, query string, maxItems int) ([]deepAPITwitterPost, error) {
	body := deepAPITwitterSearchRequest{Query: query, MaxItems: maxItems, Sort: "latest", MaxCostUSD: d.MaxCostUSD, WaitForFinishSecs: 60}
	var posts twitterPosts
	if _, err := d.send(ctx, "/v1/scrape/twitter/search", body, &posts); err != nil {
		return nil, err
	}
	return posts, nil
}

func (d DeepAPIProvider) redditSearch(ctx context.Context, body deepAPIRedditSearchRequest) ([]deepAPIRedditPost, error) {
	var posts redditPosts
	if _, err := d.send(ctx, "/v1/scrape/reddit/search", body, &posts); err != nil {
		return nil, err
	}
	return posts, nil
}

type twitterPosts []deepAPITwitterPost

func (p twitterPosts) validate() error {
	for i := range p {
		if p[i].ID == nil || p[i].TextPtr == nil {
			return errors.New("post missing id/text")
		}
		p[i].Text = *p[i].TextPtr
	}
	return nil
}

type redditPosts []deepAPIRedditPost

func (p redditPosts) validate() error {
	for _, r := range p {
		if r.ID == nil || r.Title == nil {
			return errors.New("post missing id/title")
		}
	}
	return nil
}

// send POSTs a DeepAPI request, follows the polling `next` action, and
// decodes the envelope's output into out. It reports whether output was
// present (it is legitimately null while a run settles).
func (d DeepAPIProvider) send(ctx context.Context, path string, body, out any) (bool, error) {
	payload, err := apijson.Marshal(body)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+d.APIKey)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Idempotency-Key", strings.ToUpper(uuid.NewString()))
	env, err := d.do(req, path)
	if err != nil {
		return false, err
	}

	polls := 0
	for env.Next != nil && strings.EqualFold(*env.Next.Method, "GET") && polls < deepAPIMaxPolls {
		polls++
		after := 5.0
		if env.Next.AfterSecs != nil {
			after = *env.Next.AfterSecs
		}
		delay := time.Duration(math.Min(math.Max(after, 1), 15) * float64(time.Second))
		if err := d.sleep(ctx, delay); err != nil {
			return false, err
		}
		next := *env.Next.Path
		poll, err := http.NewRequestWithContext(ctx, http.MethodGet, d.BaseURL+next, nil)
		if err != nil {
			return false, err
		}
		poll.Header.Set("Authorization", "Bearer "+d.APIKey)
		if env, err = d.do(poll, next); err != nil {
			return false, err
		}
	}
	if env.Next != nil && polls >= deepAPIMaxPolls {
		return false, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("DeepAPI %s did not finish within %d polls.", path, deepAPIMaxPolls))
	}
	if !env.hasOutput() {
		return false, nil
	}
	if err := apijson.Unmarshal(env.Output, out); err != nil {
		return false, unreadable(path, env.statusCode)
	}
	if v, ok := out.(validator); ok {
		if err := v.validate(); err != nil {
			return false, unreadable(path, env.statusCode)
		}
	}
	return true, nil
}

type deepAPIResult struct {
	deepAPIEnvelope
	statusCode int
}

func unreadable(path string, status int) error {
	return httpx.Abort(http.StatusBadGateway, fmt.Sprintf("DeepAPI %s returned an unreadable response (HTTP %d).", path, status))
}

func (d DeepAPIProvider) do(req *http.Request, path string) (deepAPIResult, error) {
	ctx, cancel := context.WithTimeout(req.Context(), 90*time.Second)
	defer cancel()
	resp, err := httpClient(d.HTTP).Do(req.WithContext(ctx)) //nolint:gosec // DEEPAPI_API_BASE_URL is operator configuration
	if err != nil {
		return deepAPIResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return deepAPIResult{}, err
	}
	var env deepAPIEnvelope
	if err := apijson.Unmarshal(raw, &env); err != nil || env.validate() != nil {
		return deepAPIResult{}, unreadable(path, resp.StatusCode)
	}
	if env.Error != nil {
		if creditExhaustionCodes[strings.ToLower(*env.Error.Code)] {
			return deepAPIResult{}, newCreditsExhausted("DeepAPI", *env.Error.Message)
		}
		return deepAPIResult{}, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("DeepAPI %s failed [%s]: %s", path, *env.Error.Code, *env.Error.Message))
	}
	if resp.StatusCode == http.StatusPaymentRequired {
		return deepAPIResult{}, newCreditsExhausted("DeepAPI", "HTTP 402 on "+path)
	}
	if resp.StatusCode >= 300 {
		return deepAPIResult{}, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("DeepAPI %s failed with HTTP %d.", path, resp.StatusCode))
	}
	if *env.Status == "failed" {
		return deepAPIResult{}, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("DeepAPI %s reported status failed.", path))
	}
	return deepAPIResult{deepAPIEnvelope: env, statusCode: resp.StatusCode}, nil
}

func (d DeepAPIProvider) sleep(ctx context.Context, delay time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, delay)
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func buildFinancialEventsQuery(days int) string {
	topics := []string{"stock market", "crypto", "forex", "commodities", "earnings", "central banks", "macro economics"}
	return strings.Join(topics, " OR ") + fmt.Sprintf(" financial news last %d days", days)
}

func categorizeFinancialTopic(text string) string {
	l := strings.ToLower(text)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(l, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("crypto", "bitcoin", "ethereum"):
		return "crypto"
	case has("forex", "currency", "fx"):
		return "forex"
	case has("commodit", "gold", "oil"):
		return "commodities"
	case has("earning"):
		return "earnings"
	case has("central bank", "fed", "ecb", "rate"):
		return "central-banks"
	case has("macro", "gdp", "inflation", "cpi"):
		return "macro-economics"
	case has("ipo"):
		return "ipo"
	case has("merger", "acquisition", "m&a"):
		return "ma"
	}
	return "general-markets"
}

func countContains(text string, keywords []string) int {
	n := 0
	for _, k := range keywords {
		if strings.Contains(text, k) {
			n++
		}
	}
	return n
}

var (
	webBullish = []string{"bullish", "surge", "rally", "gain", "up", "rise", "soar", "jump", "climb", "strong", "beat", "exceed", "outperform", "buy", "long", "optimistic", "positive", "growth", "profit", "record high"}
	webBearish = []string{"bearish", "crash", "fall", "drop", "down", "decline", "plunge", "tumble", "slump", "weak", "miss", "underperform", "sell", "short", "pessimistic", "negative", "loss", "recession", "correction"}

	tweetBullish = []string{"bullish", "moon", "pump", "buy", "long", "calls", "green", "up", "rally", "breakout", "support", "accumulate", "🚀", "📈", "💎", "🙌"}
	tweetBearish = []string{"bearish", "dump", "sell", "short", "puts", "red", "down", "crash", "breakdown", "resistance", "distribute", "📉", "🐻", "💀", "🚮"}

	redditBullish = []string{"bullish", "buy", "long", "calls", "undervalued", "growth", "moon", "diamond hands", "hold", "accumulate"}
	redditBearish = []string{"bearish", "sell", "short", "puts", "overvalued", "crash", "paper hands", "dump", "bubble"}
)

func labelAt(score, threshold float64) string {
	if score > threshold {
		return positiveLabel
	}
	if score < -threshold {
		return negativeLabel
	}
	return neutralLabel
}

// extractSentiment is the web-snippet keyword scorer; nil when no keyword hit.
func extractSentiment(r deepAPISearchResult) *hermesEventSentiment {
	text := strings.ToLower(*r.Title + " " + r.snippetText())
	bull, bear := countContains(text, webBullish), countContains(text, webBearish)
	total := bull + bear
	if total == 0 {
		return nil
	}
	score := float64(bull-bear) / float64(total)
	label := labelAt(score, 0.2)
	return &hermesEventSentiment{Label: &label, Score: &score}
}

func analyzeTweetSentiment(text string) hermesEventSentiment {
	l := strings.ToLower(text)
	bull, bear := countContains(l, tweetBullish), countContains(l, tweetBearish)
	total := bull + bear
	score := 0.0
	if total > 0 {
		score = float64(bull-bear) / float64(total)
	}
	label := labelAt(score, 0.15)
	return hermesEventSentiment{Label: &label, Score: &score}
}

func analyzeRedditPost(p deepAPIRedditPost) (float64, string) {
	text := ""
	if p.Text != nil {
		text = *p.Text
	}
	l := strings.ToLower(*p.Title + " " + text)
	bull, bear := countContains(l, redditBullish), countContains(l, redditBearish)
	total := bull + bear
	score := 0.0
	if total > 0 {
		score = float64(bull-bear) / float64(total)
	}
	return score, labelAt(score, 0.2)
}
