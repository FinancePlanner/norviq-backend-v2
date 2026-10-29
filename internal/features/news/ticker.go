package news

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/newsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// maxUserFeeds is DefaultNewsTickerService.maxUserFeeds.
const maxUserFeeds = 10

// TickerItem is NewsTickerItem.
type TickerItem struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	URL         *string `json:"url,omitempty"`
	Source      string  `json:"source"`
	SourceURL   *string `json:"sourceUrl,omitempty"`
	PublishedAt string  `json:"publishedAt"`
}

// TickerResponse is NewsTickerResponse.
type TickerResponse struct {
	Enabled     bool         `json:"enabled"`
	Items       []TickerItem `json:"items"`
	GeneratedAt string       `json:"generatedAt"`
	Stale       bool         `json:"stale"`
}

// TickerSettings is NewsTickerSettings (and UpdateNewsTickerSettingsRequest).
type TickerSettings struct {
	Enabled bool `json:"enabled"`
}

// TickerFeed is NewsTickerFeed.
type TickerFeed struct {
	ID    string  `json:"id"`
	URL   string  `json:"url"`
	Title *string `json:"title,omitempty"`
}

// TickerFeedsResponse is NewsTickerFeedsResponse.
type TickerFeedsResponse struct {
	Feeds    []TickerFeed `json:"feeds"`
	MaxFeeds int          `json:"maxFeeds"`
}

type addFeedRequest struct {
	URL string `json:"url"`
}

var errTickerNotConfigured = httpx.Abort(http.StatusServiceUnavailable, "The news ticker is not configured on this server.")

// tickerCache is NewsTickerCache: a tiny TTL cache keyed by feed set.
type tickerCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]tickerEntry
}

type tickerEntry struct {
	response TickerResponse
	expires  time.Time
}

func newTickerCache(ttl time.Duration) *tickerCache {
	return &tickerCache{ttl: ttl, entries: map[string]tickerEntry{}}
}

func (c *tickerCache) get(key string, now time.Time) (TickerResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !e.expires.After(now) {
		return TickerResponse{}, false
	}
	return e.response, true
}

func (c *tickerCache) set(key string, r TickerResponse, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= 256 {
		for k, e := range c.entries {
			if !e.expires.After(now) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= 256 {
			c.entries = map[string]tickerEntry{}
		}
	}
	c.entries[key] = tickerEntry{response: r, expires: now.Add(c.ttl)}
}

func (s *Service) feeds() newsprovider.FeedsClient {
	if s.chain == nil {
		return nil
	}
	return s.chain.Feeds
}

// tickerFeedList is NewsTickerMapping.feedList: curated first, then the
// user's, deduped, capped at one aggregator call.
func tickerFeedList(curated, user []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, f := range append(append([]string{}, curated...), user...) {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
		if len(out) == newsprovider.MaxFeedsPerCall {
			break
		}
	}
	return out
}

// tickerResponse is NewsTickerMapping.response.
func tickerResponse(doc newsprovider.Document, enabled bool, now time.Time) TickerResponse {
	items := make([]TickerItem, 0, len(doc.Items))
	for _, it := range doc.Items {
		items = append(items, TickerItem{
			ID: it.ID, Title: it.Title, URL: it.URL, Source: it.Feeds.SourceName,
			SourceURL: it.Feeds.SourceURL, PublishedAt: formatISOSeconds(it.DatePublished),
		})
	}
	generated := now
	stale := false
	if doc.Feeds != nil {
		if doc.Feeds.GeneratedAt != nil {
			generated = *doc.Feeds.GeneratedAt
		}
		stale = len(doc.Feeds.Stale) > 0
	}
	return TickerResponse{Enabled: enabled, Items: items, GeneratedAt: formatISOSeconds(generated), Stale: stale}
}

func (s *Service) tickerPreference(ctx context.Context, userID uuid.UUID) (*newsdb.NewsTickerPreference, error) {
	p, err := s.q.GetNewsTickerPreference(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// TickerSettings is the ticker service's settings(userId:).
func (s *Service) TickerSettings(ctx context.Context, userID uuid.UUID) (TickerSettings, error) {
	if s.feeds() == nil {
		return TickerSettings{Enabled: false}, nil
	}
	p, err := s.tickerPreference(ctx, userID)
	if err != nil {
		return TickerSettings{}, err
	}
	return TickerSettings{Enabled: p == nil || p.Enabled}, nil
}

// Ticker is the ticker service's ticker(userId:limit:).
func (s *Service) Ticker(ctx context.Context, userID uuid.UUID, limit int) (TickerResponse, error) {
	now := s.now()
	client := s.feeds()
	if client == nil {
		return TickerResponse{Enabled: false, Items: []TickerItem{}, GeneratedAt: formatISOSeconds(now)}, nil
	}
	settings, err := s.TickerSettings(ctx, userID)
	if err != nil {
		return TickerResponse{}, err
	}
	if !settings.Enabled {
		return TickerResponse{Enabled: false, Items: []TickerItem{}, GeneratedAt: formatISOSeconds(now)}, nil
	}
	rows, err := s.q.ListNewsTickerFeeds(ctx, userID)
	if err != nil {
		return TickerResponse{}, err
	}
	user := make([]string, 0, len(rows))
	for _, r := range rows {
		user = append(user, r.FeedUrl)
	}
	feeds := tickerFeedList(s.chain.TickerFeeds, user)
	if len(feeds) == 0 {
		return TickerResponse{Enabled: true, Items: []TickerItem{}, GeneratedAt: formatISOSeconds(now)}, nil
	}
	key := strings.Join(feeds, "\n") + "|" + strconv.Itoa(limit)
	if cached, ok := s.ticker.get(key, now); ok {
		return cached, nil
	}
	doc, err := client.Items(ctx, feeds, limit)
	if err != nil {
		return TickerResponse{}, err
	}
	resp := tickerResponse(doc, true, now)
	s.ticker.set(key, resp, now)
	return resp, nil
}

// UpdateTickerSettings is updateSettings(userId:enabled:).
func (s *Service) UpdateTickerSettings(ctx context.Context, userID uuid.UUID, enabled bool) (TickerSettings, error) {
	if s.feeds() == nil {
		return TickerSettings{}, errTickerNotConfigured
	}
	p, err := s.tickerPreference(ctx, userID)
	if err != nil {
		return TickerSettings{}, err
	}
	now := s.now()
	if p != nil {
		err = s.q.UpdateNewsTickerPreference(ctx, newsdb.UpdateNewsTickerPreferenceParams{Enabled: enabled, Now: &now, ID: p.ID})
	} else {
		err = s.q.InsertNewsTickerPreference(ctx, newsdb.InsertNewsTickerPreferenceParams{ID: uuid.New(), UserID: userID, Enabled: enabled, Now: &now})
	}
	if err != nil {
		return TickerSettings{}, err
	}
	return TickerSettings{Enabled: enabled}, nil
}

func tickerFeed(r newsdb.NewsTickerFeedSubscription) TickerFeed {
	return TickerFeed{ID: apijson.FormatUUID(r.ID), URL: r.FeedUrl, Title: r.Title}
}

// ListTickerFeeds is listFeeds(userId:).
func (s *Service) ListTickerFeeds(ctx context.Context, userID uuid.UUID) (TickerFeedsResponse, error) {
	if s.feeds() == nil {
		return TickerFeedsResponse{Feeds: []TickerFeed{}, MaxFeeds: 0}, nil
	}
	rows, err := s.q.ListNewsTickerFeeds(ctx, userID)
	if err != nil {
		return TickerFeedsResponse{}, err
	}
	feeds := make([]TickerFeed, 0, len(rows))
	for _, r := range rows {
		feeds = append(feeds, tickerFeed(r))
	}
	return TickerFeedsResponse{Feeds: feeds, MaxFeeds: maxUserFeeds}, nil
}

// AddTickerFeed is addFeed(userId:url:).
func (s *Service) AddTickerFeed(ctx context.Context, userID uuid.UUID, rawURL string) (TickerFeed, error) {
	client := s.feeds()
	if client == nil {
		return TickerFeed{}, errTickerNotConfigured
	}
	u := strings.TrimSpace(rawURL)
	if !validHTTPURL(u) {
		return TickerFeed{}, httpx.BadRequest("Enter a full http(s) URL.")
	}
	count, err := s.q.CountNewsTickerFeeds(ctx, userID)
	if err != nil {
		return TickerFeed{}, err
	}
	if count >= maxUserFeeds {
		return TickerFeed{}, httpx.Conflict("You can follow at most " + strconv.Itoa(maxUserFeeds) + " feeds.")
	}
	candidates, err := client.Discover(ctx, u)
	if err != nil {
		return TickerFeed{}, err
	}
	if len(candidates) == 0 {
		return TickerFeed{}, httpx.Unprocessable("No feed found at that address.")
	}
	chosen := candidates[0]
	dup, err := s.q.NewsTickerFeedExists(ctx, newsdb.NewsTickerFeedExistsParams{UserID: userID, FeedUrl: chosen.URL})
	if err != nil {
		return TickerFeed{}, err
	}
	if dup {
		return TickerFeed{}, httpx.Conflict("You already follow that feed.")
	}
	var title *string
	if chosen.Title != "" {
		t := chosen.Title
		title = &t
	}
	now := s.now()
	row := newsdb.NewsTickerFeedSubscription{ID: uuid.New(), UserID: userID, FeedUrl: chosen.URL, Title: title, CreatedAt: &now}
	if err := s.q.InsertNewsTickerFeed(ctx, newsdb.InsertNewsTickerFeedParams{
		ID: row.ID, UserID: userID, FeedUrl: row.FeedUrl, Title: title, CreatedAt: &now,
	}); err != nil {
		return TickerFeed{}, err
	}
	return tickerFeed(row), nil
}

// RemoveTickerFeed is removeFeed(userId:feedId:).
func (s *Service) RemoveTickerFeed(ctx context.Context, userID, feedID uuid.UUID) error {
	if s.feeds() == nil {
		return errTickerNotConfigured
	}
	n, err := s.q.DeleteNewsTickerFeed(ctx, newsdb.DeleteNewsTickerFeedParams{ID: feedID, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return httpx.NotFound("Feed not found.")
	}
	return nil
}

// GET /v1/news/ticker?limit=
func (h *handlers) newsTicker(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	limit := clampedLimit(httpx.QueryOptionalInt(c, "limit"), 30, 100)
	resp, err := h.svc.Ticker(c.Request().Context(), s.UserID, limit)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// GET /v1/news/ticker/settings
func (h *handlers) newsTickerSettings(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.TickerSettings(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// PUT /v1/news/ticker/settings
func (h *handlers) updateNewsTickerSettings(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p TickerSettings
	if err := swiftjson.DecodeRequest(c, &p); err != nil {
		return err
	}
	resp, err := h.svc.UpdateTickerSettings(c.Request().Context(), s.UserID, p.Enabled)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// GET /v1/news/ticker/feeds
func (h *handlers) listNewsTickerFeeds(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.ListTickerFeeds(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// POST /v1/news/ticker/feeds
func (h *handlers) addNewsTickerFeed(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p addFeedRequest
	if err := swiftjson.DecodeRequest(c, &p); err != nil {
		return err
	}
	resp, err := h.svc.AddTickerFeed(c.Request().Context(), s.UserID, p.URL)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// DELETE /v1/news/ticker/feeds/:feedId
func (h *handlers) deleteNewsTickerFeed(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathUUID(c, "feedId", "Invalid feed ID")
	if err != nil {
		return err
	}
	if err := h.svc.RemoveTickerFeed(c.Request().Context(), s.UserID, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
