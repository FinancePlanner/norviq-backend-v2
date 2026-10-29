package news

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/store/newsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewsServiceError reasons (status 404 / 400).
var (
	errNewsNotFound       = httpx.NotFound("News item not found.")
	errInvalidSymbol      = httpx.BadRequest("Invalid symbol.")
	errInvalidHeadline    = httpx.BadRequest("Invalid headline.")
	errInvalidURL         = httpx.BadRequest("Invalid url.")
	errInvalidPublishedAt = httpx.BadRequest("Invalid publishedAt. Expected ISO8601 datetime.")
)

// Service is DefaultNewsService plus the ticker and Thesis Watch services:
// everything NewsController and the webhook call.
type Service struct {
	db      *pgxpool.Pool
	q       *newsdb.Queries
	chain   *newsprovider.Chain
	archive *market.NewsArchive
	pro     ProGate
	logger  *slog.Logger
	clock   func() time.Time
	ticker  *tickerCache
}

// NewService builds the service.
func NewService(db *pgxpool.Pool, chain *newsprovider.Chain, archive *market.NewsArchive, pro ProGate, logger *slog.Logger, now func() time.Time) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db: db, q: newsdb.New(db), chain: chain, archive: archive, pro: pro,
		logger: logger, clock: now, ticker: newTickerCache(60 * time.Second),
	}
}

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

func (s *Service) provider() newsprovider.Provider {
	if s.chain == nil {
		return nil
	}
	return s.chain.Provider
}

// ItemResponse is NewsItemResponse.
type ItemResponse struct {
	ID          string  `json:"id"`
	Symbol      string  `json:"symbol"`
	Headline    string  `json:"headline"`
	Source      *string `json:"source,omitempty"`
	URL         *string `json:"url,omitempty"`
	ImageURL    *string `json:"imageUrl,omitempty"`
	Summary     *string `json:"summary,omitempty"`
	PublishedAt string  `json:"publishedAt"`
	CreatedAt   *string `json:"createdAt,omitempty"`
	UpdatedAt   *string `json:"updatedAt,omitempty"`
}

// ItemRequest is NewsItemRequest.
type ItemRequest struct {
	Symbol      string  `json:"symbol"`
	Headline    string  `json:"headline"`
	Source      *string `json:"source"`
	URL         *string `json:"url"`
	Summary     *string `json:"summary"`
	PublishedAt *string `json:"publishedAt"`
}

// SyncResponse is NewsSyncResponse.
type SyncResponse struct {
	Provider      string `json:"provider"`
	SymbolsCount  int    `json:"symbolsCount"`
	FetchedCount  int    `json:"fetchedCount"`
	InsertedCount int    `json:"insertedCount"`
	UpdatedCount  int    `json:"updatedCount"`
	SkippedCount  int    `json:"skippedCount"`
}

// WebhookResponse is FinnhubNewsWebhookResponse.
type WebhookResponse struct {
	Provider            string `json:"provider"`
	ReceivedCount       int    `json:"receivedCount"`
	MatchedSymbolsCount int    `json:"matchedSymbolsCount"`
	MatchedUsersCount   int    `json:"matchedUsersCount"`
	InsertedCount       int    `json:"insertedCount"`
	SkippedCount        int    `json:"skippedCount"`
}

func timePtrString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatISOMillis(*t)
	return &s
}

func makeItemResponse(m *newsdb.NewsItem) ItemResponse {
	return ItemResponse{
		ID:          strings.ToUpper(m.ID.String()),
		Symbol:      m.Symbol,
		Headline:    m.Headline,
		Source:      m.Source,
		URL:         m.Url,
		Summary:     m.Summary,
		PublishedAt: formatISOMillis(m.PublishedAt),
		CreatedAt:   timePtrString(&m.CreatedAt),
		UpdatedAt:   timePtrString(m.UpdatedAt),
	}
}

func trimToNil(raw *string) *string {
	if raw == nil {
		return nil
	}
	t := strings.TrimSpace(*raw)
	if t == "" {
		return nil
	}
	return &t
}

func normalizeSymbol(raw string) (string, error) {
	n := strings.ToUpper(strings.TrimSpace(raw))
	if n == "" {
		return "", errInvalidSymbol
	}
	return n, nil
}

func normalizedSymbolValue(raw *string) *string {
	if raw == nil {
		return nil
	}
	n := strings.ToUpper(strings.TrimSpace(*raw))
	if n == "" {
		return nil
	}
	return &n
}

func normalizeHeadline(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" {
		return "", errInvalidHeadline
	}
	return n, nil
}

// validHTTPURL is `URL(string:)` with an http(s) scheme and a host.
func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || strings.ContainsAny(raw, " \t\n\"<>\\^`{|}") {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != ""
}

func normalizeURL(raw *string) (*string, error) {
	t := trimToNil(raw)
	if t == nil {
		return nil, nil
	}
	if !validHTTPURL(*t) {
		return nil, errInvalidURL
	}
	return t, nil
}

func (s *Service) parsePublishedAt(raw *string) (time.Time, error) {
	t := trimToNil(raw)
	if t == nil {
		return s.now(), nil
	}
	if parsed, ok := parseISO8601(*t); ok {
		return bindDate(parsed), nil
	}
	return time.Time{}, errInvalidPublishedAt
}

// List is DefaultNewsService.list: one page plus the next cursor.
func (s *Service) List(ctx context.Context, userID uuid.UUID, symbol *string, limit int, cursor *newsCursor) ([]ItemResponse, *string, error) {
	params := newsdb.ListNewsItemsParams{UserID: userID, Symbol: normalizedSymbolValue(symbol), Lim: int32(limit + 1)} //nolint:gosec // limit is clamped to 1...200
	if cursor != nil {
		at := cursor.publishedAt
		params.CursorAt = &at
		params.CursorID = cursor.id
	}
	rows, err := s.q.ListNewsItems(ctx, params)
	if err != nil {
		return nil, nil, err
	}
	page := rows
	if len(page) > limit {
		page = page[:limit]
	}
	items := make([]ItemResponse, 0, len(page))
	for i := range page {
		items = append(items, makeItemResponse(&page[i]))
	}
	if len(rows) > limit && limit < 200 && len(page) > 0 {
		last := page[len(page)-1]
		next := encodeNewsCursor(last.PublishedAt, last.ID)
		return items, &next, nil
	}
	return items, nil, nil
}

func (s *Service) trackedSymbols(ctx context.Context, userID uuid.UUID) ([]string, error) {
	stocks, err := s.q.TrackedStockSymbols(ctx, userID)
	if err != nil {
		return nil, err
	}
	watch, err := s.q.TrackedWatchlistSymbols(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Array(Set(stocks + watchlist)), normalized.
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range append(stocks, watch...) {
		if seen[raw] {
			continue
		}
		seen[raw] = true
		if n := normalizedSymbolValue(&raw); n != nil {
			out = append(out, *n)
		}
	}
	return out, nil
}

// Feed is DefaultNewsService.feed.
func (s *Service) Feed(ctx context.Context, userID uuid.UUID, limit int) ([]ItemResponse, error) {
	clamped := max(1, min(limit, 100))
	tracked, err := s.trackedSymbols(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []ItemResponse{}
	if len(tracked) == 0 {
		return out, nil
	}
	rows, err := s.q.ListNewsFeed(ctx, newsdb.ListNewsFeedParams{UserID: userID, Symbols: tracked, Lim: int32(max(1, clamped))})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		out = append(out, makeItemResponse(&rows[i]))
	}
	return out, nil
}

func (s *Service) find(ctx context.Context, id, userID uuid.UUID) (*newsdb.NewsItem, error) {
	row, err := s.q.GetNewsItem(ctx, newsdb.GetNewsItemParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNewsNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Get is DefaultNewsService.get.
func (s *Service) Get(ctx context.Context, id, userID uuid.UUID) (ItemResponse, error) {
	m, err := s.find(ctx, id, userID)
	if err != nil {
		return ItemResponse{}, err
	}
	return makeItemResponse(m), nil
}

func (s *Service) applyPayload(m *newsdb.NewsItem, p ItemRequest) error {
	symbol, err := normalizeSymbol(p.Symbol)
	if err != nil {
		return err
	}
	headline, err := normalizeHeadline(p.Headline)
	if err != nil {
		return err
	}
	source := trimToNil(p.Source)
	u, err := normalizeURL(p.URL)
	if err != nil {
		return err
	}
	summary := trimToNil(p.Summary)
	published, err := s.parsePublishedAt(p.PublishedAt)
	if err != nil {
		return err
	}
	m.Symbol, m.Headline, m.Source, m.Url, m.Summary, m.PublishedAt = symbol, headline, source, u, summary, published
	return nil
}

// Create is DefaultNewsService.create.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, p ItemRequest) (ItemResponse, error) {
	m := newsdb.NewsItem{ID: uuid.New(), UserID: userID}
	if err := s.applyPayload(&m, p); err != nil {
		return ItemResponse{}, err
	}
	if err := s.insert(ctx, &m); err != nil {
		return ItemResponse{}, err
	}
	return makeItemResponse(&m), nil
}

func (s *Service) insert(ctx context.Context, m *newsdb.NewsItem) error {
	now := s.now()
	m.CreatedAt, m.UpdatedAt = now, &now
	return s.q.InsertNewsItem(ctx, newsdb.InsertNewsItemParams{
		ID: m.ID, UserID: m.UserID, Symbol: m.Symbol, Headline: m.Headline, Source: m.Source,
		Url: m.Url, Summary: m.Summary, PublishedAt: m.PublishedAt, CreatedAt: now, UpdatedAt: &now,
	})
}

func (s *Service) save(ctx context.Context, m *newsdb.NewsItem) error {
	now := s.now()
	m.UpdatedAt = &now
	return s.q.UpdateNewsItem(ctx, newsdb.UpdateNewsItemParams{
		ID: m.ID, Symbol: m.Symbol, Headline: m.Headline, Source: m.Source, Url: m.Url,
		Summary: m.Summary, PublishedAt: m.PublishedAt, UpdatedAt: &now,
	})
}

// Update is DefaultNewsService.update.
func (s *Service) Update(ctx context.Context, id, userID uuid.UUID, p ItemRequest) (ItemResponse, error) {
	m, err := s.find(ctx, id, userID)
	if err != nil {
		return ItemResponse{}, err
	}
	if err := s.applyPayload(m, p); err != nil {
		return ItemResponse{}, err
	}
	if err := s.save(ctx, m); err != nil {
		return ItemResponse{}, err
	}
	return makeItemResponse(m), nil
}

// Delete is DefaultNewsService.delete.
func (s *Service) Delete(ctx context.Context, id, userID uuid.UUID) error {
	m, err := s.find(ctx, id, userID)
	if err != nil {
		return err
	}
	return s.q.DeleteNewsItem(ctx, m.ID)
}

// normalizedArticle is NormalizedNewsArticle.
type normalizedArticle struct {
	symbols     []string
	headline    string
	source      *string
	url         *string
	summary     *string
	publishedAt time.Time
}

// Sync is DefaultNewsService.syncNews.
func (s *Service) Sync(ctx context.Context, userID uuid.UUID) (SyncResponse, error) {
	provider := s.provider()
	if provider == nil {
		return SyncResponse{}, httpx.Abort(http.StatusNotImplemented,
			"News provider not configured. Configure FINNHUB_API_KEY to enable Finnhub sync.")
	}
	symbols, err := s.trackedSymbols(ctx, userID)
	if err != nil {
		return SyncResponse{}, err
	}
	tracked := map[string]bool{}
	for _, sym := range symbols {
		tracked[sym] = true
	}
	fetched, err := provider.Fetch(ctx, symbols)
	if err != nil {
		return SyncResponse{}, err
	}
	articles := []normalizedArticle{}
	for _, raw := range fetched {
		a, err := normalizeProviderArticle(raw)
		if err != nil {
			return SyncResponse{}, err
		}
		if a != nil {
			articles = append(articles, *a)
		}
	}
	resp := SyncResponse{
		Provider: provider.Name(), SymbolsCount: len(symbols), FetchedCount: len(fetched),
		SkippedCount: max(len(fetched)-len(articles), 0),
	}
	for _, a := range articles {
		if len(a.symbols) != 1 || !tracked[a.symbols[0]] {
			resp.SkippedCount++
			continue
		}
		result, err := s.upsertNewsItem(ctx, userID, a.symbols[0], a)
		if err != nil {
			return SyncResponse{}, err
		}
		switch result {
		case upsertInserted:
			resp.InsertedCount++
		case upsertUpdated:
			resp.UpdatedCount++
		default:
			resp.SkippedCount++
		}
	}
	return resp, nil
}

func normalizeProviderArticle(raw newsprovider.Item) (*normalizedArticle, error) {
	symbol := normalizedSymbolValue(&raw.Symbol)
	if symbol == nil {
		return nil, nil
	}
	headline := trimToNil(&raw.Headline)
	if headline == nil {
		return nil, nil
	}
	u, err := normalizeURL(raw.URL)
	if err != nil {
		return nil, err
	}
	return &normalizedArticle{
		symbols: []string{*symbol}, headline: *headline, source: trimToNil(raw.Source),
		url: u, summary: trimToNil(raw.Summary), publishedAt: raw.PublishedAt,
	}, nil
}

type upsertResult int

const (
	upsertInserted upsertResult = iota
	upsertUpdated
	upsertSkipped
)

func (s *Service) upsertNewsItem(ctx context.Context, userID uuid.UUID, symbol string, a normalizedArticle) (upsertResult, error) {
	existing, err := s.findDuplicate(ctx, userID, symbol, a)
	if err != nil {
		return upsertSkipped, err
	}
	if existing != nil {
		if apply(a, existing) {
			if err := s.save(ctx, existing); err != nil {
				return upsertSkipped, err
			}
			return upsertUpdated, nil
		}
		return upsertSkipped, nil
	}
	m := newsdb.NewsItem{
		ID: uuid.New(), UserID: userID, Symbol: symbol, Headline: a.headline, Source: a.source,
		Url: a.url, Summary: a.summary, PublishedAt: bindDate(a.publishedAt),
	}
	if err := s.insert(ctx, &m); err != nil {
		return upsertSkipped, err
	}
	return upsertInserted, nil
}

func strEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func secondsBetween(a, b time.Time) float64 {
	return math.Abs(a.Sub(b).Seconds())
}

func apply(a normalizedArticle, existing *newsdb.NewsItem) bool {
	needsUpdate := existing.Headline != a.headline ||
		!strEq(trimToNil(existing.Source), a.source) ||
		!strEq(trimToNil(existing.Url), a.url) ||
		!strEq(trimToNil(existing.Summary), a.summary) ||
		secondsBetween(existing.PublishedAt, a.publishedAt) >= 1
	if !needsUpdate {
		return false
	}
	existing.Headline, existing.Source, existing.Url, existing.Summary = a.headline, a.source, a.url, a.summary
	existing.PublishedAt = bindDate(a.publishedAt)
	return true
}

func dupValue(raw *string) string {
	if t := trimToNil(raw); t != nil {
		return *t
	}
	return ""
}

func (s *Service) findDuplicate(ctx context.Context, userID uuid.UUID, symbol string, a normalizedArticle) (*newsdb.NewsItem, error) {
	var urlParam *string
	if a.url != nil && *a.url != "" {
		urlParam = a.url
	}
	candidates, err := s.q.FindPotentialNewsDuplicates(ctx, newsdb.FindPotentialNewsDuplicatesParams{
		UserID: userID, Symbol: symbol, Headline: a.headline, Url: urlParam,
	})
	if err != nil {
		return nil, err
	}
	if candidate := trimToNil(a.url); candidate != nil {
		for i := range candidates {
			if dupValue(candidates[i].Url) == *candidate {
				return &candidates[i], nil
			}
		}
	}
	for i := range candidates {
		c := &candidates[i]
		existingURL, candidateURL := dupValue(c.Url), dupValue(a.url)
		if existingURL == candidateURL && candidateURL != "" {
			return c, nil
		}
		if c.Headline == a.headline && secondsBetween(c.PublishedAt, a.publishedAt) < 1 {
			return c, nil
		}
	}
	return nil, nil
}
