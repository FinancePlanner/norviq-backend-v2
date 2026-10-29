// Package crypto ports Vapor's CryptoController (/v1/crypto/*),
// DefaultCryptoService with its FMP / mock / disabled data providers, the
// crypto markets view (CoinGecko universe, FMP reference data, assembler,
// DefaultCryptoMarketsService with its "crypto_markets" Redis cache) and
// CryptoMarketsRefreshJob.
//
// Vapor quirk ported as is: CryptoPortfolioItemRequest's CodingKeys name its
// last key "average_buy_price", but the global backendAPI JSON decoder
// rewrites snake_case body keys to camelCase before CodingKeys are matched.
// No JSON body can therefore supply it, and POST/PUT /v1/crypto/portfolio
// answer 400 keyNotFound for every JSON body (form bodies still work).
package crypto

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/cryptodb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Settings are the crypto environment variables configure.swift read. Unset
// and empty behave the same, as Swift's `Environment.get(...).flatMap(Int.init)`
// did.
type Settings struct {
	MarketsRefreshSeconds string `env:"CRYPTO_MARKETS_REFRESH_SECONDS"`
	MinMarketCapUSD       string `env:"CRYPTO_MOVERS_MIN_MARKET_CAP_USD"`
	MinVolumeUSD          string `env:"CRYPTO_MOVERS_MIN_VOLUME_USD"`
	UniverseSize          string `env:"CRYPTO_MARKETS_UNIVERSE_SIZE"`
	YTDCoins              string `env:"CRYPTO_MARKETS_YTD_COINS"`
	YTDFillBudget         string `env:"CRYPTO_MARKETS_YTD_FILL_BUDGET"`
	CoinGeckoAPIKey       string `env:"COINGECKO_API_KEY"`
	CoinGeckoAPIPlan      string `env:"COINGECKO_API_PLAN"`
}

// intOr is `Environment.get(name).flatMap(Int.init) ?? def`.
func intOr(raw string, def int) int {
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	return def
}

func floatOr(raw string, def float64) float64 {
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		return n
	}
	return def
}

// RefreshSeconds is CRYPTO_MARKETS_REFRESH_SECONDS (default 360).
func (s Settings) RefreshSeconds() int { return intOr(s.MarketsRefreshSeconds, 360) }

var (
	providers sync.Map // *server.Deps -> DataProvider
	markets   sync.Map // *server.Deps -> *MarketsService
)

// SharedProvider is app.cryptoService's provider for d, built once:
// makeCryptoDataProvider over the market platform's FMP client.
func SharedProvider(d *server.Deps) (DataProvider, error) {
	if v, ok := providers.Load(d); ok {
		return v.(DataProvider), nil
	}
	svc, err := market.SharedService(d)
	if err != nil {
		return nil, err
	}
	p := SelectProvider(svc.FMP, d.Config.Environment)
	switch p.(type) {
	case DisabledProvider:
		d.Logger.Error("FMP_API_KEY is not configured; crypto market data disabled.")
	case MockProvider:
		d.Logger.Warn("FMP_API_KEY is not configured; using MockCryptoDataProvider for crypto market data.")
	}
	v, _ := providers.LoadOrStore(d, p)
	return v.(DataProvider), nil
}

// SharedMarkets is app.cryptoMarketsService for d, built once so requests
// and the refresh job share the in-process snapshot.
func SharedMarkets(d *server.Deps) (*MarketsService, error) {
	if v, ok := markets.Load(d); ok {
		return v.(*MarketsService), nil
	}
	s, err := config.Load[Settings]()
	if err != nil {
		return nil, err
	}
	svc, err := market.SharedService(d)
	if err != nil {
		return nil, err
	}
	var reference ReferenceSource
	if svc.FMP != nil {
		reference = FMPReference{Provider: FMPProvider{FMP: svc.FMP}}
	}
	var key *string
	if s.CoinGeckoAPIKey != "" {
		key = &s.CoinGeckoAPIKey
	}
	var plan *string
	if s.CoinGeckoAPIPlan != "" {
		plan = &s.CoinGeckoAPIPlan
	}
	cg := NewCoinGecko(key, ParseCoinGeckoPlan(plan))
	cg.Client = &http.Client{}
	cg.Logger = d.Logger
	def := DefaultMarketsFilter()
	m := NewMarketsService(MarketsOptions{
		Provider:  cg,
		Reference: reference,
		Cache:     cache.New(d.Redis, "crypto_markets", d.Logger),
		Filter: MarketsFilter{
			MinMarketCap: floatOr(s.MinMarketCapUSD, def.MinMarketCap),
			MinVolume24h: floatOr(s.MinVolumeUSD, def.MinVolume24h),
			ListSize:     def.ListSize,
		},
		UniverseSize: intOr(s.UniverseSize, 250),
		YTDCoverage:  intOr(s.YTDCoins, 50),
		// Three missed ticks before the fresh copy lapses to the stale one.
		FreshTTLSecond: s.RefreshSeconds() * 3,
		Logger:         d.Logger,
	})
	v, _ := markets.LoadOrStore(d, m)
	return v.(*MarketsService), nil
}

// Feature mounts /v1/crypto.
type Feature struct {
	// Provider overrides the environment-selected data provider (tests).
	Provider DataProvider
	// Markets overrides the shared markets service (tests).
	Markets *MarketsService
	// Entitlements overrides the environment-built billing service (tests).
	Entitlements func(d *server.Deps) *entitlements.Service
	// Now overrides the portfolio clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "crypto" }

type handlers struct {
	provider  DataProvider
	markets   *MarketsService
	ent       *entitlements.Service
	portfolio PortfolioStore
}

// Register mirrors CryptoController.boot: ScopedBearerAuthenticator + guard,
// a per-user 120/min limit, then crypto:read or crypto:write.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handlers{provider: f.Provider, markets: f.Markets}
	var err error
	if h.provider == nil {
		if h.provider, err = SharedProvider(d); err != nil {
			return err
		}
	}
	if h.markets == nil {
		if h.markets, err = SharedMarkets(d); err != nil {
			return err
		}
	}
	h.ent = entitlements.NewFromEnv(d.DB)
	if f.Entitlements != nil {
		h.ent = f.Entitlements(d)
	}
	h.portfolio = PortfolioStore{Q: cryptodb.New(d.DB), Now: f.Now}

	protected := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), d.Limiter.Limit(120, 60*time.Second, "ratelimit:crypto")}
	read := append(append([]echo.MiddlewareFunc{}, protected...), auth.RequireScope(auth.ScopeCryptoRead))
	write := append(append([]echo.MiddlewareFunc{}, protected...), auth.RequireScope(auth.ScopeCryptoWrite))

	g := r.V1.Group("/crypto")
	g.GET("/list", h.list, read...)
	g.GET("/quote/:symbol", h.quote, read...)
	g.GET("/quote-short/:symbol", h.quoteShort, read...)
	g.GET("/batch-quotes", h.batchQuotes, read...)
	g.GET("/history/:resolution/:symbol", h.history, read...)
	g.GET("/news", h.generalNews, read...)
	g.GET("/news/:symbol", h.news, read...)
	g.GET("/markets", h.marketsView, read...)
	g.GET("/portfolio", h.listPortfolio, read...)
	g.POST("/portfolio", h.addToPortfolio, write...)
	g.PUT("/portfolio/:itemId", h.updatePortfolioItem, write...)
	g.DELETE("/portfolio/:itemId", h.removeFromPortfolio, write...)
	return nil
}

// requireEntitlement is requireCryptoEntitlement.
func (h *handlers) requireEntitlement(c *echo.Context) (uuid.UUID, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureCrypto, s.UserID); err != nil {
		return uuid.Nil, err
	}
	return s.UserID, nil
}

// requireSymbol prefers `?symbol=` over the path parameter, trimmed.
func requireSymbol(c *echo.Context) (string, error) {
	if q := httpx.QueryString(c, "symbol"); q != nil {
		return strings.TrimSpace(*q), nil
	}
	if p := c.Param("symbol"); p != "" {
		return strings.TrimSpace(p), nil
	}
	return "", httpx.BadRequest("Symbol is required.")
}

func (h *handlers) list(c *echo.Context) error {
	if _, err := h.requireEntitlement(c); err != nil {
		return err
	}
	out, err := h.provider.CryptocurrencyList(c.Request().Context())
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) marketsView(c *echo.Context) error {
	if _, err := h.requireEntitlement(c); err != nil {
		return err
	}
	raw := string(OneDay)
	if q := httpx.QueryString(c, "timeframe"); q != nil {
		raw = *q
	}
	timeframe, ok := ParseTimeframe(strings.ToLower(raw))
	if !ok {
		names := make([]string, len(AllTimeframes))
		for i, t := range AllTimeframes {
			names[i] = string(t)
		}
		return httpx.BadRequest("timeframe must be one of: " + strings.Join(names, ", ") + ".")
	}
	limit := min(max(httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 100), 1), 250)
	out, err := h.markets.Markets(c.Request().Context(), timeframe, limit)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) quote(c *echo.Context) error {
	if _, err := h.requireEntitlement(c); err != nil {
		return err
	}
	symbol, err := requireSymbol(c)
	if err != nil {
		return err
	}
	out, err := h.provider.Quote(c.Request().Context(), symbol)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) quoteShort(c *echo.Context) error {
	if _, err := h.requireEntitlement(c); err != nil {
		return err
	}
	symbol, err := requireSymbol(c)
	if err != nil {
		return err
	}
	out, err := h.provider.QuoteShort(c.Request().Context(), symbol)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) batchQuotes(c *echo.Context) error {
	if _, err := h.requireEntitlement(c); err != nil {
		return err
	}
	short := false
	if b := httpx.QueryOptionalBool(c, "short"); b != nil {
		short = *b
	}
	out, err := h.provider.BatchQuotes(c.Request().Context(), short)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) history(c *echo.Context) error {
	if _, err := h.requireEntitlement(c); err != nil {
		return err
	}
	symbol, err := requireSymbol(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	from, to := httpx.QueryString(c, "from"), httpx.QueryString(c, "to")
	var out []HistoricalPoint
	switch resolution := c.Param("resolution"); resolution {
	case "1min", "5min", "1hour":
		if out, err = h.provider.Intraday(ctx, resolution, symbol, from, to); err != nil {
			return err
		}
	case "light":
		points, err := h.provider.HistoricalLight(ctx, symbol, from, to)
		if err != nil {
			return err
		}
		out = make([]HistoricalPoint, len(points))
		for i, p := range points {
			out[i] = HistoricalPoint{Date: p.Date, Close: p.Price, Volume: p.Volume}
		}
	case "full":
		points, err := h.provider.HistoricalFull(ctx, symbol, from, to)
		if err != nil {
			return err
		}
		out = make([]HistoricalPoint, len(points))
		for i, p := range points {
			out[i] = HistoricalPoint{Date: p.Date, Open: f64(p.Open), Low: f64(p.Low), High: f64(p.High), Close: p.Close, Volume: p.Volume}
		}
	default:
		return httpx.BadRequest("Invalid resolution.")
	}
	return httpx.OK(c, out)
}

func newsResponse(items []NewsItem) []StockNews {
	out := make([]StockNews, len(items))
	for i, it := range items {
		n := StockNews{Title: "No Title", ImageURL: it.Image, Summary: it.Text}
		if it.Title != nil {
			n.Title = *it.Title
		}
		if it.URL != nil {
			n.URL = *it.URL
		}
		if it.PublishedDate != nil {
			n.Date = *it.PublishedDate
		}
		n.Source = it.Publisher
		if n.Source == nil {
			n.Source = it.Site
		}
		out[i] = n
	}
	return out
}

func (h *handlers) fetchNews(c *echo.Context, symbol *string) error {
	items, err := h.provider.News(c.Request().Context(), symbol,
		httpx.QueryOptionalInt(c, "page"), httpx.QueryOptionalInt(c, "limit"),
		httpx.QueryString(c, "from"), httpx.QueryString(c, "to"))
	if err != nil {
		return err
	}
	return httpx.OK(c, newsResponse(items))
}

func (h *handlers) generalNews(c *echo.Context) error {
	if _, err := h.requireEntitlement(c); err != nil {
		return err
	}
	return h.fetchNews(c, nil)
}

func (h *handlers) news(c *echo.Context) error {
	if _, err := h.requireEntitlement(c); err != nil {
		return err
	}
	symbol, err := requireSymbol(c)
	if err != nil {
		return err
	}
	return h.fetchNews(c, &symbol)
}

func (h *handlers) listPortfolio(c *echo.Context) error {
	userID, err := h.requireEntitlement(c)
	if err != nil {
		return err
	}
	out, err := h.portfolio.List(c.Request().Context(), userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) addToPortfolio(c *echo.Context) error {
	userID, err := h.requireEntitlement(c)
	if err != nil {
		return err
	}
	p, err := decodePortfolioRequest(c)
	if err != nil {
		return err
	}
	out, err := h.portfolio.Add(c.Request().Context(), p, userID)
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

func itemID(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("itemId"))
	if !ok {
		return uuid.Nil, httpx.BadRequest("Invalid itemId.")
	}
	return id, nil
}

func (h *handlers) updatePortfolioItem(c *echo.Context) error {
	userID, err := h.requireEntitlement(c)
	if err != nil {
		return err
	}
	id, err := itemID(c)
	if err != nil {
		return err
	}
	p, err := decodePortfolioRequest(c)
	if err != nil {
		return err
	}
	out, err := h.portfolio.Update(c.Request().Context(), id, p, userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) removeFromPortfolio(c *echo.Context) error {
	userID, err := h.requireEntitlement(c)
	if err != nil {
		return err
	}
	id, err := itemID(c)
	if err != nil {
		return err
	}
	if err := h.portfolio.Remove(c.Request().Context(), id, userID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
