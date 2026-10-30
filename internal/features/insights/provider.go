package insights

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
)

// Hermes wire DTOs (InsightsDTOs.swift). Vapor decoded them with the global
// backendAPI decoder, which rewrites snake_case keys to camelCase
// (event_id -> eventId, source_url -> sourceURL), so they are decoded here
// with apijson.Unmarshal. Timestamps stay strings: Hermes emits
// non-standard ISO variants.

type hermesEventPayload struct {
	Title   *string `json:"title"`
	Summary *string `json:"summary"`
	URL     *string `json:"url"`
	Author  *string `json:"author"`
}

type hermesEventSentiment struct {
	Label *string  `json:"label"`
	Score *float64 `json:"score"`
}

type hermesEvent struct {
	EventID    *string               `json:"eventId"`
	Source     *string               `json:"source"`
	SourceID   *string               `json:"sourceId"`
	Topic      *string               `json:"topic"`
	ObservedAt *string               `json:"observedAt"`
	IngestedAt *string               `json:"ingestedAt"`
	Payload    *hermesEventPayload   `json:"payload"`
	Sentiment  *hermesEventSentiment `json:"sentiment"`
}

type hermesEventsResponse struct {
	Count  *int           `json:"count"`
	Events *[]hermesEvent `json:"events"`
}

func (r hermesEventsResponse) validate() error {
	if r.Events == nil {
		return errors.New("events missing")
	}
	for _, e := range *r.Events {
		if e.EventID == nil {
			return errors.New("eventId missing")
		}
	}
	return nil
}

type hermesSummaryResponse struct {
	WindowDays  *int           `json:"windowDays"`
	TotalEvents *int           `json:"totalEvents"`
	ByTopic     map[string]int `json:"byTopic"`
}

type hermesSentimentResponse struct {
	Topic        *string        `json:"topic"`
	WindowDays   *int           `json:"windowDays"`
	Count        *int           `json:"count"`
	LabelCounts  map[string]int `json:"labelCounts"`
	AverageScore *float64       `json:"averageScore"`
	Sampled      *int           `json:"sampled"`
}

type hermesNetWorthEntry struct {
	EventID    *string  `json:"eventId"`
	IngestedAt *string  `json:"ingestedAt"`
	Value      *float64 `json:"value"`
}

type hermesNetWorthResponse struct {
	Latest  *hermesNetWorthEntry  `json:"latest"`
	History []hermesNetWorthEntry `json:"history"`
}

type hermesTickerPost struct {
	EventID        *string  `json:"eventId"`
	Author         *string  `json:"author"`
	AuthorHandle   *string  `json:"authorHandle"`
	Text           *string  `json:"text"`
	URL            *string  `json:"url"`
	Sentiment      *string  `json:"sentiment"`
	SentimentScore *float64 `json:"sentimentScore"`
	Confidence     *float64 `json:"confidence"`
	PostedAt       *string  `json:"postedAt"`
}

type hermesTickerPostsResponse struct {
	Symbol *string             `json:"symbol"`
	Days   *int                `json:"days"`
	Count  *int                `json:"count"`
	Posts  *[]hermesTickerPost `json:"posts"`
}

func (r hermesTickerPostsResponse) validate() error {
	if r.Posts == nil {
		return errors.New("posts missing")
	}
	for _, p := range *r.Posts {
		if p.EventID == nil {
			return errors.New("eventId missing")
		}
	}
	return nil
}

func (r hermesTickerPostsResponse) posts() []hermesTickerPost {
	if r.Posts == nil {
		return nil
	}
	return *r.Posts
}

type validator interface{ validate() error }

// Provider is the InsightsProvider protocol's requirements. Swift's
// protocol extension members (supportedSentimentSources, fetchSymbolPosts)
// were not requirements, so every call through `any InsightsProvider`
// dispatched statically to the extension's defaults; see fetchSymbolPosts.
type Provider interface {
	Enabled() bool
	// Label is providerLabel: the Swift type name, used for logs and
	// cooldown keys.
	Label() string
	FetchEvents(ctx context.Context, days, limit int) (hermesEventsResponse, error)
	FetchSummary(ctx context.Context, days int) (hermesSummaryResponse, error)
	FetchSentiment(ctx context.Context, topic *string, days int) (hermesSentimentResponse, error)
	FetchNetWorth(ctx context.Context) (hermesNetWorthResponse, error)
	FetchTickerPosts(ctx context.Context, symbol string, days, limit int) (hermesTickerPostsResponse, error)
}

// fetchSymbolPosts is the InsightsProvider extension's default
// fetchSymbolPosts with the extension's supportedSentimentSources ([.x]).
// Vapor's aggregation service called it on `any InsightsProvider`, so this
// default ran for every provider and chain, never the Direct/DeepAPI/
// Fallback overrides: only tiers whose sources include x (the user tier)
// fetch anything, one fetchTickerPosts per symbol, filed under source x.
func fetchSymbolPosts(ctx context.Context, p Provider, logger *slog.Logger, symbols, sources []string, days, limit int, now func() time.Time) []symbolPostBatch {
	usable := intersect(sources, []string{SourceX})
	if len(usable) == 0 {
		return []symbolPostBatch{}
	}
	batches := []symbolPostBatch{}
	for _, symbol := range symbols {
		resp, err := p.FetchTickerPosts(ctx, symbol, days, limit)
		if err != nil {
			logger.Warn(fmt.Sprintf("sentiment.ingest symbol failed symbol=%s error=%s", symbol, describe(err)))
			continue
		}
		posts := []ingestedPost{}
		for _, dto := range resp.posts() {
			if dto.Text == nil {
				continue
			}
			text := strings.TrimSpace(*dto.Text)
			if text == "" {
				continue
			}
			postedAt := now()
			if t := parseHermesTimestamp(dto.PostedAt); t != nil {
				postedAt = *t
			}
			posts = append(posts, ingestedPost{
				DedupeKey:          *dto.EventID,
				Symbol:             symbol,
				Author:             dto.Author,
				AuthorHandle:       dto.AuthorHandle,
				Text:               text,
				URL:                dto.URL,
				PostedAt:           postedAt,
				Source:             usable[0],
				ProvidedScore:      dto.SentimentScore,
				ProvidedLabel:      dto.Sentiment,
				ProvidedConfidence: dto.Confidence,
			})
		}
		batches = append(batches, symbolPostBatch{Symbol: symbol, Posts: posts})
	}
	return batches
}

func intersect(sources, supported []string) []string {
	out := []string{}
	for _, s := range sources {
		for _, t := range supported {
			if s == t {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// describe is String(describing: error) closely enough for log lines.
func describe(err error) string { return err.Error() }

// DisabledProvider is DisabledInsightsProvider.
type DisabledProvider struct{}

func disabledError() error {
	return httpx.Abort(http.StatusServiceUnavailable, "Insights provider not configured. Set HERMES_BASE_URL to enable Hermes sync.")
}

func (DisabledProvider) Enabled() bool { return false }
func (DisabledProvider) Label() string { return "DisabledInsightsProvider" }
func (DisabledProvider) FetchEvents(context.Context, int, int) (hermesEventsResponse, error) {
	return hermesEventsResponse{}, disabledError()
}

func (DisabledProvider) FetchSummary(context.Context, int) (hermesSummaryResponse, error) {
	return hermesSummaryResponse{}, disabledError()
}

func (DisabledProvider) FetchSentiment(context.Context, *string, int) (hermesSentimentResponse, error) {
	return hermesSentimentResponse{}, disabledError()
}

func (DisabledProvider) FetchNetWorth(context.Context) (hermesNetWorthResponse, error) {
	return hermesNetWorthResponse{}, disabledError()
}

func (DisabledProvider) FetchTickerPosts(context.Context, string, int, int) (hermesTickerPostsResponse, error) {
	return hermesTickerPostsResponse{}, disabledError()
}

// HermesProvider is HermesInsightsProvider: the self-hosted Hermes finance
// API over the private tailnet.
type HermesProvider struct {
	BaseURL  string
	APIToken string
	HTTP     *http.Client
}

func (HermesProvider) Enabled() bool { return true }
func (HermesProvider) Label() string { return "HermesInsightsProvider" }

func (h HermesProvider) FetchEvents(ctx context.Context, days, limit int) (hermesEventsResponse, error) {
	var out hermesEventsResponse
	err := h.fetchJSON(ctx, "/finance/events", [][2]string{{"days", itoa(days)}, {"limit", itoa(limit)}}, &out)
	return out, err
}

func (h HermesProvider) FetchSummary(ctx context.Context, days int) (hermesSummaryResponse, error) {
	var out hermesSummaryResponse
	err := h.fetchJSON(ctx, "/finance/summary", [][2]string{{"days", itoa(days)}}, &out)
	return out, err
}

func (h HermesProvider) FetchSentiment(ctx context.Context, topic *string, days int) (hermesSentimentResponse, error) {
	query := [][2]string{{"days", itoa(days)}}
	if topic != nil && *topic != "" {
		query = append(query, [2]string{"topic", *topic})
	}
	var out hermesSentimentResponse
	err := h.fetchJSON(ctx, "/finance/sentiment", query, &out)
	return out, err
}

func (h HermesProvider) FetchNetWorth(ctx context.Context) (hermesNetWorthResponse, error) {
	var out hermesNetWorthResponse
	err := h.fetchJSON(ctx, "/finance/net-worth", nil, &out)
	return out, err
}

func (h HermesProvider) FetchTickerPosts(ctx context.Context, symbol string, days, limit int) (hermesTickerPostsResponse, error) {
	var out hermesTickerPostsResponse
	err := h.fetchJSON(ctx, "/finance/ticker/"+symbol+"/posts", [][2]string{{"days", itoa(days)}, {"limit", itoa(limit)}}, &out)
	return out, err
}

func (h HermesProvider) fetchJSON(ctx context.Context, path string, query [][2]string, out any) error {
	base := strings.TrimSuffix(h.BaseURL, "/")
	u, err := url.Parse(base + path)
	if err != nil {
		return httpx.Abort(http.StatusInternalServerError, "Invalid Hermes base URL configuration.")
	}
	if len(query) > 0 {
		u.RawQuery = encodeQueryItems(query)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return httpx.Abort(http.StatusInternalServerError, "Unable to build Hermes request URL.")
	}
	if h.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+h.APIToken)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient(h.HTTP).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if err := apijson.Unmarshal(body, out); err != nil {
			return httpx.Abort(http.StatusBadGateway, "Failed to decode Hermes response for "+path+".")
		}
		if v, ok := out.(validator); ok {
			if err := v.validate(); err != nil {
				return httpx.Abort(http.StatusBadGateway, "Failed to decode Hermes response for "+path+".")
			}
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return httpx.Abort(http.StatusBadGateway, "Hermes rejected the request. Check HERMES_API_TOKEN.")
	case http.StatusNotFound:
		return httpx.Abort(http.StatusNotFound, "Hermes resource not found for "+path+".")
	default:
		return httpx.Abort(http.StatusBadGateway, fmt.Sprintf("Hermes request failed for %s with status %d.", path, resp.StatusCode))
	}
}

// encodeQueryItems is URLComponents.queryItems: a space is %20 and "+"
// stays literal, unlike url.Values.Encode.
func encodeQueryItems(items [][2]string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, queryItemEscape(it[0])+"="+queryItemEscape(it[1]))
	}
	return strings.Join(parts, "&")
}

func queryItemEscape(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	return strings.ReplaceAll(e, "%2B", "+")
}

func httpClient(c *http.Client) *http.Client {
	if c == nil {
		return http.DefaultClient
	}
	return c
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// creditsExhaustedError is InsightsProviderError.creditsExhausted: a 502
// that also puts the provider in cooldown.
type creditsExhaustedError struct{ err *httpx.Error }

func newCreditsExhausted(provider, detail string) *creditsExhaustedError {
	return &creditsExhaustedError{err: httpx.Abort(http.StatusBadGateway, provider+" is out of credit: "+detail)}
}

func (e *creditsExhaustedError) Error() string { return e.err.Reason }
func (e *creditsExhaustedError) Unwrap() error { return e.err }

// CooldownRegistry is ProviderCooldownRegistry: in-process memory of which
// providers are out of credit.
type CooldownRegistry struct {
	mu           sync.Mutex
	coolingUntil map[string]time.Time
	window       time.Duration
	now          func() time.Time
}

// NewCooldownRegistry builds a registry with the given window.
func NewCooldownRegistry(window time.Duration) *CooldownRegistry {
	return &CooldownRegistry{coolingUntil: map[string]time.Time{}, window: window, now: time.Now}
}

// IsCoolingDown reports whether provider is suppressed.
func (r *CooldownRegistry) IsCoolingDown(provider string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.coolingUntil[provider]
	if !ok {
		return false
	}
	if !until.After(r.now()) {
		delete(r.coolingUntil, provider)
		return false
	}
	return true
}

// BeginCooldown suppresses provider for the window.
func (r *CooldownRegistry) BeginCooldown(provider string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	until := r.now().Add(r.window)
	r.coolingUntil[provider] = until
	return until
}

// Clear ends provider's cooldown.
func (r *CooldownRegistry) Clear(provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.coolingUntil, provider)
}

// ActiveCooldowns lists the providers currently skipped, sorted.
func (r *CooldownRegistry) ActiveCooldowns() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := []string{}
	for k, v := range r.coolingUntil {
		if v.After(now) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// FallbackProvider is FallbackInsightsProvider: tries each provider in
// order and returns the first success.
type FallbackProvider struct {
	Providers []Provider
	Logger    *slog.Logger
	Cooldowns *CooldownRegistry
}

// Enabled is true when any provider in the chain is.
func (f FallbackProvider) Enabled() bool {
	for _, p := range f.Providers {
		if p.Enabled() {
			return true
		}
	}
	return false
}

func (FallbackProvider) Label() string { return "FallbackInsightsProvider" }

func (f FallbackProvider) FetchEvents(ctx context.Context, days, limit int) (hermesEventsResponse, error) {
	return firstSuccess(f, "events", func(p Provider) (hermesEventsResponse, error) { return p.FetchEvents(ctx, days, limit) })
}

func (f FallbackProvider) FetchSummary(ctx context.Context, days int) (hermesSummaryResponse, error) {
	return firstSuccess(f, "summary", func(p Provider) (hermesSummaryResponse, error) { return p.FetchSummary(ctx, days) })
}

func (f FallbackProvider) FetchSentiment(ctx context.Context, topic *string, days int) (hermesSentimentResponse, error) {
	return firstSuccess(f, "sentiment", func(p Provider) (hermesSentimentResponse, error) { return p.FetchSentiment(ctx, topic, days) })
}

func (f FallbackProvider) FetchNetWorth(ctx context.Context) (hermesNetWorthResponse, error) {
	return firstSuccess(f, "net-worth", func(p Provider) (hermesNetWorthResponse, error) { return p.FetchNetWorth(ctx) })
}

func (f FallbackProvider) FetchTickerPosts(ctx context.Context, symbol string, days, limit int) (hermesTickerPostsResponse, error) {
	return firstSuccess(f, "ticker-posts", func(p Provider) (hermesTickerPostsResponse, error) {
		return p.FetchTickerPosts(ctx, symbol, days, limit)
	})
}

// firstSuccess runs op against each provider in turn; the first success
// wins, a credit-exhaustion error starts that provider's cooldown, and when
// every provider failed the last error is returned. (Swift's isUsable hook
// only ever ran for fetchSymbolPosts, which no caller reached; see
// fetchSymbolPosts.)
func firstSuccess[T any](f FallbackProvider, label string, op func(Provider) (T, error)) (T, error) {
	var zero T
	var lastErr error
	skipped := 0
	for i, p := range f.Providers {
		name := p.Label()
		if f.Cooldowns.IsCoolingDown(name) {
			skipped++
			continue
		}
		v, err := op(p)
		if err == nil {
			f.Cooldowns.Clear(name)
			return v, nil
		}
		lastErr = err
		var credits *creditsExhaustedError
		if errors.As(err, &credits) {
			until := f.Cooldowns.BeginCooldown(name)
			f.Logger.Error(fmt.Sprintf("Insights provider %s is out of credit for %s; suppressed until %s. %s", name, label, until.UTC().Format(time.RFC3339), credits.Error()))
			continue
		}
		f.Logger.Warn(fmt.Sprintf("Insights provider %d/%d failed for %s, falling through: %s", i+1, len(f.Providers), label, describe(err)))
	}
	if lastErr != nil {
		return zero, lastErr
	}
	if skipped > 0 {
		return zero, httpx.Abort(http.StatusServiceUnavailable, "Every insights provider is in credit cooldown.")
	}
	return zero, httpx.Abort(http.StatusServiceUnavailable, "No insights provider configured.")
}
