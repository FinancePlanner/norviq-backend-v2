// Package insights ports InsightsController (/v1/insights/*): the Hermes
// insights reads (summary, topics, sentiment snapshots, net worth, ticker
// posts), the retail-sentiment aggregates (symbol batch, portfolio,
// watchlist, trending, history), the admin sync triggers and the Hermes
// machine endpoint; plus the provider chain (Hermes -> DeepAPI -> Direct)
// and the HermesSyncJob, SentimentAggregationJob and SentimentRetentionJob
// loops.
//
// Other domains take the insights service from SharedService (the AI
// assistant's get_insights tool and Telegram replies call Summary; the
// dashboard's why-moved card calls Summary and TickerSentiment).
package insights

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/insightsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Settings are the environment variables configure.swift read for
// insights and sentiment, with Vapor's defaults.
type Settings struct {
	HermesBaseURL      string
	HermesAPIToken     string
	DeepAPIKey         string
	DeepAPIBaseURL     string
	DeepAPIMaxCostUSD  string
	DeepAPISocial      bool
	RedditClientID     *string
	RedditClientSecret *string
	RedditUserAgent    string
	FinnhubAPIKey      *string
	CooldownSeconds    int

	TrackedTickersLimit int
	PinnedTickers       []string
	HermesSyncSeconds   int64

	SentimentWindowDays int
	SP500Symbols        []string
	FMPAPIKey           *string
	UserTierLimit       int
	TrendingTierLimit   int
	ThemePosts          int
	BatchSize           int
	PostsPerSymbol      int
	MaxThemeSymbols     int
	AggregationHourUTC  int
	PostRetentionDays   int
	DailyRetentionDays  int
}

// envInt is `Environment.get(name).flatMap(Int.init(_:)) ?? def`.
func envInt(name string, def int) int {
	if v, ok := os.LookupEnv(name); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envTrimmed(name string) *string {
	v, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	v = strings.TrimSpace(v)
	return &v
}

func orDefault(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// symbolList is the `split(",") + trim(.whitespaces) + uppercased +
// drop-empty` the pin and override lists used.
func symbolList(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if s := strings.ToUpper(strings.Trim(part, " \t")); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// LoadSettings reads Settings from the environment.
func LoadSettings() Settings {
	s := Settings{
		HermesBaseURL:       orDefault(envTrimmed("HERMES_BASE_URL"), ""),
		HermesAPIToken:      orDefault(envTrimmed("HERMES_API_TOKEN"), ""),
		DeepAPIKey:          orDefault(envTrimmed("DEEPAPI_API_KEY"), ""),
		DeepAPIBaseURL:      orDefault(envTrimmed("DEEPAPI_API_BASE_URL"), "https://deepapi.co"),
		DeepAPIMaxCostUSD:   orDefault(envTrimmed("DEEPAPI_MAX_COST_USD"), "0.05"),
		RedditClientID:      envTrimmed("REDDIT_CLIENT_ID"),
		RedditClientSecret:  envTrimmed("REDDIT_CLIENT_SECRET"),
		RedditUserAgent:     orDefault(envTrimmed("REDDIT_USER_AGENT"), "norviq-sentiment/1.0"),
		CooldownSeconds:     envInt("PROVIDER_CREDIT_COOLDOWN_SECONDS", 6*60*60),
		TrackedTickersLimit: envInt("HERMES_TRACKED_TICKERS_LIMIT", 25),
		PinnedTickers:       symbolList(os.Getenv("HERMES_TRACKED_TICKERS")),
		HermesSyncSeconds:   900,
		SentimentWindowDays: envInt("SENTIMENT_WINDOW_DAYS", 7),
		SP500Symbols:        symbolList(os.Getenv("SENTIMENT_SP500_SYMBOLS")),
		UserTierLimit:       envInt("SENTIMENT_USER_TIER_LIMIT", 500),
		TrendingTierLimit:   envInt("SENTIMENT_TRENDING_TIER_LIMIT", 50),
		ThemePosts:          envInt("SENTIMENT_THEME_POSTS", 40),
		BatchSize:           envInt("SENTIMENT_BATCH_SIZE", 20),
		PostsPerSymbol:      envInt("SENTIMENT_POSTS_PER_SYMBOL", 100),
		MaxThemeSymbols:     envInt("SENTIMENT_MAX_THEME_SYMBOLS", 150),
		AggregationHourUTC:  envInt("SENTIMENT_AGGREGATION_HOUR_UTC", 5),
		PostRetentionDays:   envInt("SENTIMENT_POST_RETENTION_DAYS", 90),
		DailyRetentionDays:  envInt("SENTIMENT_DAILY_RETENTION_DAYS", 730),
	}
	// Bool(String): only the exact spellings "true" and "false".
	s.DeepAPISocial = os.Getenv("DEEPAPI_SOCIAL_SCRAPING_ENABLED") == "true"
	if v, ok := os.LookupEnv("FINNHUB_API_KEY"); ok {
		s.FinnhubAPIKey = &v
	}
	if v, ok := os.LookupEnv("FMP_API_KEY"); ok {
		s.FMPAPIKey = &v
	}
	if v, err := strconv.ParseInt(os.Getenv("HERMES_SYNC_INTERVAL_SECONDS"), 10, 64); err == nil {
		s.HermesSyncSeconds = v
	}
	return s
}

// Options override the environment-built services (tests).
type Options struct {
	Settings *Settings
	// Provider replaces the chain makeInsightsProvider builds.
	Provider Provider
	// Quotes replaces market.SharedService for the portfolio roll-up.
	Quotes QuoteSource
	// Chat replaces the AI platform's default client for themes; set
	// NoThemes to run without a theme service.
	Chat     ai.ChatClient
	NoThemes bool
	// HTTP is the client for every provider call (stub servers in tests).
	HTTP *http.Client
	// FMPBaseURL replaces https://financialmodelingprep.com for the seeder.
	FMPBaseURL string
	Now        func() time.Time
}

// bundle is everything the controller and jobs share for one Deps.
type bundle struct {
	settings   Settings
	insights   *Service
	queries    *SentimentQueries
	aggregator *Aggregator
	seeder     *IndexSeeder
	cooldowns  *CooldownRegistry
}

var bundles sync.Map // *server.Deps -> *bundle

// MakeProvider is makeInsightsProvider: Hermes (when HERMES_BASE_URL is
// set), then DeepAPI (when DEEPAPI_API_KEY is set), then the always-enabled
// Direct provider. A single link is returned bare, more are wrapped in a
// FallbackProvider sharing cooldowns.
func MakeProvider(d *server.Deps, s Settings, httpc *http.Client, cooldowns *CooldownRegistry, now func() time.Time) Provider {
	var chain []Provider
	var names []string
	if s.HermesBaseURL != "" {
		chain = append(chain, HermesProvider{BaseURL: s.HermesBaseURL, APIToken: s.HermesAPIToken, HTTP: httpc})
		names = append(names, "hermes")
	}
	if s.DeepAPIKey != "" {
		chain = append(chain, DeepAPIProvider{
			APIKey: s.DeepAPIKey, BaseURL: s.DeepAPIBaseURL, MaxCostUSD: s.DeepAPIMaxCostUSD,
			SocialScrapingEnabled: s.DeepAPISocial, HTTP: httpc, Logger: d.Logger, Now: now,
		})
		names = append(names, "deepapi")
	}
	direct := DirectProvider{
		StocktwitsBaseURL:  "https://api.stocktwits.com/api/2",
		RedditAuthURL:      "https://www.reddit.com/api/v1/access_token",
		RedditSearchBase:   "https://oauth.reddit.com",
		RedditClientID:     s.RedditClientID,
		RedditClientSecret: s.RedditClientSecret,
		RedditUserAgent:    s.RedditUserAgent,
		HTTP:               httpc,
		Logger:             d.Logger,
		Now:                now,
	}
	if s.FinnhubAPIKey != nil && *s.FinnhubAPIKey != "" {
		direct.News = &FinnhubNews{BaseURL: "https://finnhub.io/api/v1", APIKey: *s.FinnhubAPIKey, HTTP: httpc, Now: now}
	}
	if direct.Enabled() {
		chain = append(chain, direct)
		names = append(names, "direct")
	}
	if len(chain) == 0 {
		d.Logger.Warn("No insights provider configured (set HERMES_BASE_URL or DEEPAPI_API_KEY)")
		return DisabledProvider{}
	}
	d.Logger.Info("Insights providers: " + strings.Join(names, " -> "))
	if len(chain) == 1 {
		return chain[0]
	}
	return FallbackProvider{Providers: chain, Logger: d.Logger, Cooldowns: cooldowns}
}

func buildBundle(d *server.Deps, o Options) (*bundle, error) {
	s := LoadSettings()
	if o.Settings != nil {
		s = *o.Settings
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	httpc := o.HTTP
	if httpc == nil {
		httpc = &http.Client{}
	}
	cooldowns := NewCooldownRegistry(time.Duration(s.CooldownSeconds) * time.Second)
	cooldowns.now = now
	provider := o.Provider
	if provider == nil {
		provider = MakeProvider(d, s, httpc, cooldowns, now)
	}
	svc := NewService(d.DB, d.Logger, provider, s.TrackedTickersLimit, s.PinnedTickers, now)

	quotes := o.Quotes
	if quotes == nil {
		m, err := market.SharedService(d)
		if err != nil {
			return nil, err
		}
		quotes = m
	}
	q := insightsdb.New(d.DB)

	var themes *ThemeService
	if !o.NoThemes {
		client := o.Chat
		if client == nil {
			p, err := aiplatform.Platform(d)
			if err != nil {
				return nil, err
			}
			client = p.Default
		}
		if _, disabled := client.(ai.DisabledClient); client != nil && !disabled {
			themes = &ThemeService{Client: client, MaxPosts: s.ThemePosts, MaxThemes: 5, Logger: d.Logger}
		}
	}
	fmpBase := o.FMPBaseURL
	if fmpBase == "" {
		fmpBase = "https://financialmodelingprep.com"
	}
	return &bundle{
		settings:  s,
		insights:  svc,
		cooldowns: cooldowns,
		queries:   &SentimentQueries{q: q, quotes: quotes, windowDays: s.SentimentWindowDays, now: now},
		aggregator: &Aggregator{
			insights: svc, q: q,
			resolver: &UniverseResolver{
				insights: svc, q: q, pinned: s.PinnedTickers,
				userTierLimit: s.UserTierLimit, trendingTierLimit: s.TrendingTierLimit, now: now,
			},
			themes: themes, windowDays: s.SentimentWindowDays, batchSize: s.BatchSize,
			postsPerSymbol: s.PostsPerSymbol, maxThemeSymbols: s.MaxThemeSymbols, logger: d.Logger, now: now,
		},
		seeder: &IndexSeeder{
			q: q, fmpAPIKey: s.FMPAPIKey, fmpBaseURL: fmpBase, overrideSymbol: s.SP500Symbols,
			http: httpc, logger: d.Logger, now: now,
		},
	}, nil
}

// bundleFor returns d's shared bundle, building it from the environment on
// first use.
func bundleFor(d *server.Deps) (*bundle, error) {
	if v, ok := bundles.Load(d); ok {
		return v.(*bundle), nil
	}
	b, err := buildBundle(d, Options{})
	if err != nil {
		return nil, err
	}
	v, _ := bundles.LoadOrStore(d, b)
	return v.(*bundle), nil
}

// SharedService returns the insights service for d, built once per Deps,
// so the AI assistant, dashboard and messaging ports share the controller's
// instance (Vapor's app.insightsService).
func SharedService(d *server.Deps) (*Service, error) {
	b, err := bundleFor(d)
	if err != nil {
		return nil, err
	}
	return b.insights, nil
}

// Install builds d's bundle with overrides and makes it the shared one
// (tests, and wiring that must replace the provider chain).
func Install(d *server.Deps, o Options) error {
	b, err := buildBundle(d, o)
	if err != nil {
		return err
	}
	bundles.Store(d, b)
	return nil
}

// Feature mounts the /v1/insights routes.
type Feature struct {
	// Options, when set, replace the environment-built services.
	Options *Options
}

// Name implements server.Feature.
func (Feature) Name() string { return "insights" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	if f.Options != nil {
		if err := Install(d, *f.Options); err != nil {
			return err
		}
	}
	b, err := bundleFor(d)
	if err != nil {
		return err
	}
	h := &handlers{d: d, b: b, ent: entitlements.NewFromEnv(d.DB)}
	d.Health.Add("hermes", func(context.Context) server.HealthCheck {
		return b.insights.HermesHealthCheck(os.Getenv("HERMES_SYNC_INTERVAL_SECONDS"))
	})

	// routes.swift: RateLimitMiddleware(60/60s, "ratelimit:insights")
	// outside the controller, so it is per client IP and also covers the
	// machine endpoint.
	g := r.V1.Group("/insights", d.Limiter.Limit(60, 60*time.Second, "ratelimit:insights"))
	g.GET("/tracked-symbols", h.trackedSymbols)

	protected := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeInsightsRead)}
	g.GET("/summary", h.summary, protected...)
	g.GET("/topics/:topic", h.topic, protected...)
	g.GET("/sentiment", h.sentiment, protected...)
	g.GET("/net-worth", h.netWorth, protected...)
	g.GET("/tickers/:symbol/sentiment", h.tickerSentiment, protected...)
	g.GET("/sentiment/symbols", h.symbolBatch, protected...)
	g.GET("/sentiment/portfolio", h.portfolio, protected...)
	g.GET("/sentiment/watchlist", h.watchlist, protected...)
	g.GET("/sentiment/trending", h.trending, protected...)
	g.GET("/sentiment/history/:symbol", h.history, protected...)
	g.POST("/sync", h.syncNow, protected...)
	g.POST("/sentiment/sync", h.sentimentSyncNow, protected...)
	g.POST("/sentiment/seed-index", h.seedIndexNow, protected...)
	return nil
}

type handlers struct {
	d   *server.Deps
	b   *bundle
	ent *entitlements.Service
}

const maxBatchSymbols = 100

var symbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.\-]{0,9}$`)

// validatedSymbol trims, uppercases and drops a leading "$".
func validatedSymbol(raw string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(raw))
	symbol := strings.TrimPrefix(normalized, "$")
	if !symbolPattern.MatchString(symbol) {
		return "", httpx.BadRequest("Invalid symbol.")
	}
	return symbol, nil
}

func clampedDays(raw *int, def int) int { return max(1, min(httpx.IntOr(raw, def), 90)) }

func clampedLimit(raw *int, def, maxValue int) int {
	return max(1, min(httpx.IntOr(raw, def), maxValue))
}

// pathParam is req.parameters.get(name), percent-decoded.
func pathParam(c *echo.Context, name string) string {
	raw := c.Param(name)
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}

func (h *handlers) premium(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	return h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureAIInsights, s.UserID)
}

// trackedSymbols is the Hermes machine endpoint: fail-closed on
// INSIGHTS_SYMBOLS_TOKEN, then the top-N biggest positions across users.
func (h *handlers) trackedSymbols(c *echo.Context) error {
	expected := strings.Trim(os.Getenv("INSIGHTS_SYMBOLS_TOKEN"), " \t")
	if expected == "" {
		return httpx.Forbidden("tracked-symbols is disabled (INSIGHTS_SYMBOLS_TOKEN unset).")
	}
	presented, _ := auth.BearerToken(c.Request().Header.Get("Authorization"))
	if presented != expected {
		return httpx.Forbidden("Invalid machine token.")
	}
	limit := envInt("HERMES_TRACKED_TICKERS_LIMIT", 25)
	perUser := envInt("HERMES_TRACKED_TICKERS_PER_USER", 10)
	symbols, err := h.b.insights.TopPositionSymbols(c.Request().Context(), perUser, limit)
	if err != nil {
		return err
	}
	return httpx.OK(c, TrackedSymbolsResponse{Symbols: symbols, Limit: limit})
}

func (h *handlers) summary(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	out, err := h.b.insights.Summary(c.Request().Context(), clampedDays(httpx.QueryOptionalInt(c, "days"), 7))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) topic(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	topic := strings.TrimSpace(pathParam(c, "topic"))
	if topic == "" || utf8.RuneCountInString(topic) > 40 {
		return httpx.BadRequest("Invalid topic.")
	}
	days := clampedDays(httpx.QueryOptionalInt(c, "days"), 7)
	limit := clampedLimit(httpx.QueryOptionalInt(c, "limit"), 50, 200)
	out, err := h.b.insights.Topic(c.Request().Context(), topic, days, limit)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) sentiment(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	var topic *string
	if raw := httpx.QueryString(c, "topic"); raw != nil {
		if t := strings.TrimSpace(*raw); t != "" {
			topic = &t
		}
	}
	out, err := h.b.insights.Sentiment(c.Request().Context(), topic)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) netWorth(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	out, err := h.b.insights.NetWorth(c.Request().Context())
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) tickerSentiment(c *echo.Context) error {
	if err := h.premium(c); err != nil {
		return err
	}
	symbol, err := validatedSymbol(pathParam(c, "symbol"))
	if err != nil {
		return err
	}
	days := clampedDays(httpx.QueryOptionalInt(c, "days"), 14)
	limit := clampedLimit(httpx.QueryOptionalInt(c, "limit"), 20, 100)
	out, err := h.b.insights.TickerSentiment(c.Request().Context(), symbol, days, limit)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) symbolBatch(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	raw := ""
	if v := httpx.QueryString(c, "symbols"); v != nil {
		raw = *v
	}
	var requested []string
	for _, part := range strings.Split(raw, ",") {
		if part == "" {
			continue
		}
		if s, err := validatedSymbol(part); err == nil {
			requested = append(requested, s)
		}
	}
	if len(requested) == 0 {
		return httpx.BadRequest("Provide at least one symbol.")
	}
	if len(requested) > maxBatchSymbols {
		return httpx.BadRequest("At most 100 symbols per request.")
	}
	out, err := h.b.queries.Batch(c.Request().Context(), requested, false)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// queryUUID is req.query[UUID.self, at: name]: nil when absent or malformed.
func queryUUID(c *echo.Context, name string) *uuid.UUID {
	raw := httpx.QueryString(c, name)
	if raw == nil {
		return nil
	}
	id, ok := portfolios.ParseSwiftUUID(*raw)
	if !ok {
		return nil
	}
	return &id
}

func (h *handlers) portfolio(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.b.queries.Portfolio(c.Request().Context(), s.UserID, queryUUID(c, "portfolioListId"))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) watchlist(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.b.queries.Watchlist(c.Request().Context(), s.UserID, queryUUID(c, "watchlistListId"))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) trending(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	out, err := h.b.queries.Trending(c.Request().Context(), clampedLimit(httpx.QueryOptionalInt(c, "limit"), 20, 50))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) history(c *echo.Context) error {
	if err := h.premium(c); err != nil {
		return err
	}
	symbol, err := validatedSymbol(pathParam(c, "symbol"))
	if err != nil {
		return err
	}
	out, err := h.b.queries.History(c.Request().Context(), symbol, clampedLimit(httpx.QueryOptionalInt(c, "limit"), 30, 90))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) syncNow(c *echo.Context) error {
	if _, err := h.d.Admin.Require(c); err != nil {
		return err
	}
	out, err := h.b.insights.SyncFromHermes(c.Request().Context())
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) sentimentSyncNow(c *echo.Context) error {
	if _, err := h.d.Admin.Require(c); err != nil {
		return err
	}
	out, err := h.b.aggregator.RunDailyAggregation(c.Request().Context())
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) seedIndexNow(c *echo.Context) error {
	if _, err := h.d.Admin.Require(c); err != nil {
		return err
	}
	n, err := h.b.seeder.Seed(c.Request().Context())
	if err != nil {
		return err
	}
	return httpx.OK(c, SeedIndexResponse{Seeded: n})
}
