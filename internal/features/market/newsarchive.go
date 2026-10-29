package market

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/server"
	"norviq-api/internal/store/marketnewsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// NewsArchiveSettings is MarketNewsArchiveConfig.fromEnvironment.
type NewsArchiveSettings struct {
	TTLSeconds   string `env:"MARKET_TTL_NEWS_SECONDS"`
	DefaultLimit string `env:"MARKET_NEWS_DEFAULT_LIMIT"`
	MaxLimit     string `env:"MARKET_NEWS_MAX_LIMIT"`
}

func envIntOr(raw string, def int) int {
	if v, err := strconv.Atoi(raw); err == nil {
		return v
	}
	return def
}

// StockNews is StockPlanShared.StockNews.
type StockNews struct {
	Title    string  `json:"title"`
	URL      string  `json:"url"`
	Date     string  `json:"date"`
	ImageURL *string `json:"imageURL,omitempty"`
	Source   *string `json:"source,omitempty"`
	Summary  *string `json:"summary,omitempty"`
	NewsID   *string `json:"newsId,omitempty"`
	Symbol   *string `json:"symbol,omitempty"`
}

// NewsArchive is DefaultMarketNewsArchiveService.
type NewsArchive struct {
	Provider newsprovider.Provider // nil when no news provider is configured
	FMP      *marketdata.FMP       // nil without FMP_API_KEY
	Q        marketnewsdb.Querier
	TTL      int
	Default  int
	Max      int
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewNewsArchive builds the archive from the environment.
func NewNewsArchive(q marketnewsdb.Querier, provider newsprovider.Provider, fmp *marketdata.FMP, logger *slog.Logger) (*NewsArchive, error) {
	s, err := config.Load[NewsArchiveSettings]()
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &NewsArchive{
		Provider: provider, FMP: fmp, Q: q, Logger: logger,
		TTL:     max(60, envIntOr(s.TTLSeconds, 900)),
		Default: max(1, envIntOr(s.DefaultLimit, 50)),
		Max:     max(1, envIntOr(s.MaxLimit, 200)),
	}, nil
}

var sharedArchive sync.Map // *server.Deps -> *NewsArchive

// SharedNewsArchive returns d's environment-configured archive, built once
// over the shared news chain and market data service.
func SharedNewsArchive(d *server.Deps) (*NewsArchive, error) {
	if v, ok := sharedArchive.Load(d); ok {
		return v.(*NewsArchive), nil
	}
	chain, err := newsprovider.Shared(d)
	if err != nil {
		return nil, err
	}
	svc, err := SharedService(d)
	if err != nil {
		return nil, err
	}
	a, err := NewNewsArchive(marketnewsdb.New(d.DB), chain.Provider, svc.FMP, d.Logger)
	if err != nil {
		return nil, err
	}
	v, _ := sharedArchive.LoadOrStore(d, a)
	return v.(*NewsArchive), nil
}

// SetSharedNewsArchive installs a as d's archive (tests).
func SetSharedNewsArchive(d *server.Deps, a *NewsArchive) { sharedArchive.Store(d, a) }

func (a *NewsArchive) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *NewsArchive) resolveLimit(raw *int) int {
	v := a.Default
	if raw != nil {
		v = *raw
	}
	return min(max(v, 1), a.Max)
}

func archiveSymbol(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", httpx.BadRequest("Symbol is required.")
	}
	return s, nil
}

func archiveTrim(raw *string) *string {
	if raw == nil {
		return nil
	}
	t := strings.TrimSpace(*raw)
	if t == "" {
		return nil
	}
	return &t
}

func archiveURL(raw *string) (*string, error) {
	v := archiveTrim(raw)
	if v == nil {
		return nil, nil
	}
	u, err := url.Parse(*v)
	scheme := ""
	if err == nil {
		scheme = strings.ToLower(u.Scheme)
	}
	if err != nil || strings.ContainsAny(*v, " \t\n\"<>\\^`{|}") || (scheme != "http" && scheme != "https") || u.Host == "" {
		return nil, httpx.BadRequest("Invalid provider news URL.")
	}
	return v, nil
}

// formatISOMillis is ISO8601DateFormatter with fractional seconds.
func formatISOMillis(t time.Time) string {
	return t.UTC().Round(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func makeStockNews(r *marketnewsdb.MarketNewsArchive) (StockNews, bool) {
	u := archiveTrim(r.Url)
	if u == nil {
		return StockNews{}, false
	}
	return StockNews{
		Title: r.Headline, URL: *u, Date: formatISOMillis(r.PublishedAt),
		ImageURL: r.ImageUrl, Source: r.Source, Summary: r.Summary,
	}, true
}

func stockNewsList(rows []marketnewsdb.MarketNewsArchive) []StockNews {
	out := []StockNews{}
	for i := range rows {
		if n, ok := makeStockNews(&rows[i]); ok {
			out = append(out, n)
		}
	}
	return out
}

func (a *NewsArchive) archivedRows(ctx context.Context, symbol string, limit int) ([]marketnewsdb.MarketNewsArchive, error) {
	return a.Q.ArchivedNews(ctx, marketnewsdb.ArchivedNewsParams{Symbol: symbol, Lim: int32(limit)}) //nolint:gosec // limit is clamped to MARKET_NEWS_MAX_LIMIT
}

func (a *NewsArchive) isFresh(rows []marketnewsdb.MarketNewsArchive, now time.Time) bool {
	if len(rows) == 0 {
		return false
	}
	last := rows[0].FetchedAt
	for i := range rows {
		if rows[i].FetchedAt.After(last) {
			last = rows[i].FetchedAt
		}
	}
	return now.Sub(last).Seconds() <= float64(a.TTL)
}

// mapProviderError lets 404/400/501 aborts through and turns everything
// else into the 503.
func mapProviderError(err error) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusNotFound, http.StatusBadRequest, http.StatusNotImplemented:
			return he
		}
	}
	return httpx.Abort(http.StatusServiceUnavailable,
		"Market news provider unavailable. Check FINNHUB_API_KEY and upstream provider health.")
}

// News is news(symbol:limit:): the archive while fresh, else a provider
// refresh, falling back to stale rows.
func (a *NewsArchive) News(ctx context.Context, rawSymbol string, rawLimit *int) ([]StockNews, error) {
	symbol, err := archiveSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	limit := a.resolveLimit(rawLimit)
	archived, err := a.archivedRows(ctx, symbol, limit)
	if err != nil {
		return nil, err
	}
	if a.isFresh(archived, a.now()) || a.Provider == nil {
		return stockNewsList(archived), nil
	}
	fresh, err := a.RefreshNews(ctx, symbol, &limit)
	if err != nil {
		if len(archived) > 0 {
			a.Logger.Warn("market.news stale fallback", "symbol", symbol)
			return stockNewsList(archived), nil
		}
		return nil, mapProviderError(err)
	}
	return fresh, nil
}

// GeneralNews is generalNews(limit:): FMP first, then the news chain.
func (a *NewsArchive) GeneralNews(ctx context.Context, rawLimit *int) ([]StockNews, error) {
	const symbol = "GENERAL"
	limit := a.resolveLimit(rawLimit)
	archived, err := a.archivedRows(ctx, symbol, limit)
	if err != nil {
		return nil, err
	}
	if a.isFresh(archived, a.now()) {
		return stockNewsList(archived), nil
	}
	fetchedAt := a.now()
	if a.FMP != nil {
		if out, err := a.generalFromFMP(ctx, symbol, limit, fetchedAt); err == nil {
			return out, nil
		} else {
			a.Logger.Error("market.generalNews FMP failed", "error", err)
		}
	}
	if a.Provider == nil {
		return stockNewsList(archived), nil
	}
	out, err := a.generalFromProvider(ctx, symbol, limit, fetchedAt)
	if err != nil {
		if len(archived) > 0 {
			a.Logger.Warn("market.generalNews legacy stale fallback", "error", err)
			return stockNewsList(archived), nil
		}
		return nil, mapProviderError(err)
	}
	return out, nil
}

func (a *NewsArchive) generalFromFMP(ctx context.Context, symbol string, limit int, fetchedAt time.Time) ([]StockNews, error) {
	page := 0
	articles, err := a.FMP.GeneralMarketNews(ctx, &page, &limit, nil, nil)
	if err != nil {
		return nil, err
	}
	for _, raw := range articles {
		n, err := a.normalizeFMP(raw, symbol)
		if err != nil {
			return nil, err
		}
		if n == nil {
			continue
		}
		if err := a.upsert(ctx, *n, fetchedAt); err != nil {
			return nil, err
		}
	}
	rows, err := a.archivedRows(ctx, symbol, limit)
	if err != nil {
		return nil, err
	}
	return stockNewsList(rows), nil
}

func (a *NewsArchive) generalFromProvider(ctx context.Context, symbol string, limit int, fetchedAt time.Time) ([]StockNews, error) {
	articles, err := a.Provider.FetchGeneral(ctx)
	if err != nil {
		return nil, err
	}
	for _, raw := range articles {
		n, err := normalizeArchive(raw, symbol, a.Provider.Name())
		if err != nil {
			return nil, err
		}
		if n == nil {
			continue
		}
		if err := a.upsert(ctx, *n, fetchedAt); err != nil {
			return nil, err
		}
	}
	rows, err := a.archivedRows(ctx, symbol, limit)
	if err != nil {
		return nil, err
	}
	return stockNewsList(rows), nil
}

// ArchivedNews is archivedNews(symbol:limit:): the archive only.
func (a *NewsArchive) ArchivedNews(ctx context.Context, rawSymbol string, rawLimit *int) ([]StockNews, error) {
	symbol, err := archiveSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	rows, err := a.archivedRows(ctx, symbol, a.resolveLimit(rawLimit))
	if err != nil {
		return nil, err
	}
	return stockNewsList(rows), nil
}

// RefreshNews is refreshNews(symbol:limit:): fetch from the chain, upsert,
// return the archive.
func (a *NewsArchive) RefreshNews(ctx context.Context, rawSymbol string, rawLimit *int) ([]StockNews, error) {
	if a.Provider == nil {
		return nil, httpx.Abort(http.StatusNotImplemented,
			"News provider not configured. Configure FINNHUB_API_KEY to enable archived market news sync.")
	}
	symbol, err := archiveSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	limit := a.resolveLimit(rawLimit)
	fetchedAt := a.now()
	articles, err := a.Provider.Fetch(ctx, []string{symbol})
	if err != nil {
		return nil, err
	}
	for _, raw := range articles {
		n, err := normalizeArchive(raw, symbol, a.Provider.Name())
		if err != nil {
			return nil, err
		}
		if n == nil {
			continue
		}
		if err := a.upsert(ctx, *n, fetchedAt); err != nil {
			return nil, err
		}
	}
	rows, err := a.archivedRows(ctx, symbol, limit)
	if err != nil {
		return nil, err
	}
	return stockNewsList(rows), nil
}

// RefreshTrackedNews is refreshTrackedNews(symbols:): one chain fetch for
// all symbols (the Thesis Watch ingestion job).
func (a *NewsArchive) RefreshTrackedNews(ctx context.Context, rawSymbols []string) error {
	if a.Provider == nil {
		return nil
	}
	set := map[string]bool{}
	symbols := []string{}
	for _, raw := range rawSymbols {
		s, err := archiveSymbol(raw)
		if err != nil {
			return err
		}
		if !set[s] {
			set[s] = true
			symbols = append(symbols, s)
		}
	}
	if len(symbols) == 0 {
		return nil
	}
	sortStrings(symbols)
	fetchedAt := a.now()
	articles, err := a.Provider.Fetch(ctx, symbols)
	if err != nil {
		return err
	}
	for _, raw := range articles {
		symbol := strings.ToUpper(raw.Symbol)
		if !set[symbol] {
			continue
		}
		n, err := normalizeArchive(raw, symbol, a.Provider.Name())
		if err != nil {
			return err
		}
		if n == nil {
			continue
		}
		if err := a.upsert(ctx, *n, fetchedAt); err != nil {
			return err
		}
	}
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// archiveArticle is NormalizedArchiveArticle.
type archiveArticle struct {
	provider, symbol, headline string
	source                     *string
	url                        string
	summary, imageURL          *string
	publishedAt                time.Time
}

func normalizeArchive(raw newsprovider.Item, expected, provider string) (*archiveArticle, error) {
	symbol := strings.ToUpper(raw.Symbol)
	if symbol != expected {
		return nil, nil
	}
	headline := strings.TrimSpace(raw.Headline)
	if headline == "" {
		return nil, nil
	}
	u, err := archiveURL(raw.URL)
	if err != nil || u == nil {
		return nil, err
	}
	return &archiveArticle{
		provider: provider, symbol: symbol, headline: headline, source: archiveTrim(raw.Source),
		url: *u, summary: archiveTrim(raw.Summary), imageURL: archiveTrim(raw.Image), publishedAt: raw.PublishedAt,
	}, nil
}

func (a *NewsArchive) normalizeFMP(raw marketdata.FMPMarketNewsItem, expected string) (*archiveArticle, error) {
	headline := ""
	if raw.Title != nil {
		headline = strings.TrimSpace(*raw.Title)
	}
	if headline == "" {
		return nil, nil
	}
	u, err := archiveURL(raw.URL)
	if err != nil || u == nil {
		return nil, err
	}
	published := a.now()
	if raw.PublishedDate != nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", *raw.PublishedDate, time.UTC); err == nil {
			published = t
		}
	}
	source := raw.Publisher
	if source == nil {
		source = raw.Site
	}
	return &archiveArticle{
		provider: a.FMP.Name(), symbol: expected, headline: headline, source: archiveTrim(source),
		url: *u, summary: archiveTrim(raw.Text), imageURL: archiveTrim(raw.Image), publishedAt: published,
	}, nil
}

func (a *NewsArchive) findDuplicate(ctx context.Context, n archiveArticle) (*marketnewsdb.MarketNewsArchive, error) {
	u := n.url
	row, err := a.Q.FindArchiveByURL(ctx, marketnewsdb.FindArchiveByURLParams{Provider: n.provider, Symbol: n.symbol, Url: &u})
	if err == nil {
		return &row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	candidates, err := a.Q.FindArchiveByHeadline(ctx, marketnewsdb.FindArchiveByHeadlineParams{
		Provider: n.provider, Symbol: n.symbol, Headline: n.headline,
	})
	if err != nil {
		return nil, err
	}
	for i := range candidates {
		if math.Abs(candidates[i].PublishedAt.Sub(n.publishedAt).Seconds()) < 1 {
			return &candidates[i], nil
		}
	}
	return nil, nil
}

func (a *NewsArchive) upsert(ctx context.Context, n archiveArticle, fetchedAt time.Time) error {
	existing, err := a.findDuplicate(ctx, n)
	if err != nil {
		return err
	}
	now := a.now()
	u := n.url
	if existing != nil {
		return a.Q.UpdateArchiveNews(ctx, marketnewsdb.UpdateArchiveNewsParams{
			Headline: n.headline, Source: n.source, Url: &u, Summary: n.summary, ImageUrl: n.imageURL,
			PublishedAt: n.publishedAt, FetchedAt: fetchedAt, Now: &now, ID: existing.ID,
		})
	}
	return a.Q.InsertArchiveNews(ctx, marketnewsdb.InsertArchiveNewsParams{
		ID: uuid.New(), Provider: n.provider, Symbol: n.symbol, Headline: n.headline, Source: n.source,
		Url: &u, Summary: n.summary, ImageUrl: n.imageURL, PublishedAt: n.publishedAt, FetchedAt: fetchedAt, Now: now,
	})
}

// GET /v1/market/news?symbol=&limit=
func (h *handlers) stockNews(c *echo.Context) error {
	symbol, err := requireSymbolQuery(c)
	if err != nil {
		return err
	}
	out, err := h.archive.News(ctx(c), symbol, queryInt(c, "limit"))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// GET /v1/market/news/general?limit=
func (h *handlers) generalMarketNews(c *echo.Context) error {
	out, err := h.archive.GeneralNews(ctx(c), queryInt(c, "limit"))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// GET /v1/market/news/archive?symbol=&limit=
func (h *handlers) archivedStockNews(c *echo.Context) error {
	symbol, err := requireSymbolQuery(c)
	if err != nil {
		return err
	}
	out, err := h.archive.ArchivedNews(ctx(c), symbol, queryInt(c, "limit"))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// POST /v1/market/news/archive/sync?symbol=&limit=
func (h *handlers) syncArchivedStockNews(c *echo.Context) error {
	symbol, err := requireSymbolQuery(c)
	if err != nil {
		return err
	}
	out, err := h.archive.RefreshNews(ctx(c), symbol, queryInt(c, "limit"))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
