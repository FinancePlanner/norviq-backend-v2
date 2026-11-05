// Package marketbrief ports Vapor's MarketBrief module: the two shared daily
// briefs (08:15 and 22:30 Lisbon), GET /v1/market/brief, the push opt-in at
// /v1/market/brief/notifications, and MarketBriefJob.
package marketbrief

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/platform/yahoochart"
	"norviq-api/internal/server"

	"github.com/labstack/echo/v5"
)

// Settings are configure.swift's market brief environment variables.
type Settings struct {
	// Enabled gates the scheduled job and what the route serves.
	Enabled string `env:"MARKET_BRIEF_ENABLED"`
	// PushEnabled gates the job's pushes, independently of Enabled.
	PushEnabled string `env:"MARKET_BRIEF_PUSH_ENABLED"`
	// Model is the web-search model (default DefaultModel).
	Model string `env:"MARKET_BRIEF_MODEL"`
	// YahooBaseURL is v2-only (Vapor hard-coded Yahoo's chart URL): test
	// stubs and parity runs. Unset means the real endpoint.
	YahooBaseURL string `env:"YAHOO_CHART_BASE_URL"`
}

// IsEnabled is envBool("MARKET_BRIEF_ENABLED", default: false).
func (s Settings) IsEnabled() bool { return config.Bool(s.Enabled, false) }

// IsPushEnabled is envBool("MARKET_BRIEF_PUSH_ENABLED", default: false).
func (s Settings) IsPushEnabled() bool { return config.Bool(s.PushEnabled, false) }

func (f Feature) settings() (Settings, error) {
	if f.Settings != nil {
		return *f.Settings, nil
	}
	return config.Load[Settings]()
}

// Feature mounts the market brief routes.
type Feature struct {
	// Settings overrides the environment (tests).
	Settings *Settings
	// Repository overrides the database repository (tests).
	Repository Repository
	// Generator overrides the live generator (tests).
	Generator Generator
}

// Name implements server.Feature.
func (Feature) Name() string { return "marketbrief" }

func (f Feature) repository(d *server.Deps) Repository {
	if f.Repository != nil {
		return f.Repository
	}
	return DBRepository{DB: d.DB}
}

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s, err := f.settings()
	if err != nil {
		return err
	}
	h := &handlers{enabled: s.IsEnabled(), repo: f.repository(d), prefs: PreferenceService{DB: d.DB}}

	// routes.swift: api.grouped(marketRateLimit) — RateLimitMiddleware(100/60s,
	// "ratelimit:market"), shared with MarketDataController — outside the
	// controller's ScopedBearerAuthenticator + guard, so the bucket is per
	// client IP. Unlike MarketDataController, there is no per-user limiter.
	g := r.V1.Group("/market/brief",
		d.Limiter.Limit(100, 60*time.Second, "ratelimit:market"),
		d.Auth.Scoped(), auth.Guard(),
	)
	g.GET("", h.show, auth.RequireScope(auth.ScopeMarketRead))
	// The push opt-in is the user's own setting, so it carries the
	// notification scopes rather than market:read.
	g.GET("/notifications", h.notificationPreferences, auth.RequireScope(auth.ScopeNotificationsRead))
	g.PUT("/notifications", h.updateNotificationPreferences, auth.RequireScope(auth.ScopeNotificationsWrite))
	return nil
}

type handlers struct {
	enabled bool
	repo    Repository
	prefs   PreferenceService
}

// dateRe is `#/\d{4}-\d{2}-\d{2}/#` under wholeMatch; Swift's \d is any
// Unicode decimal digit.
var dateRe = regexp.MustCompile(`^\p{Nd}{4}-\p{Nd}{2}-\p{Nd}{2}$`)

// GET /v1/market/brief?lang=&slot=&date=
func (h *handlers) show(c *echo.Context) error {
	ctx := c.Request().Context()
	language := ResolveLanguage(httpx.QueryString(c, "lang"))
	var brief Response
	switch rawSlot := httpx.QueryString(c, "slot"); {
	case !h.enabled:
		brief = emptyResponse(language, false)
	case rawSlot != nil:
		if !validSlot(*rawSlot) {
			return httpx.BadRequest("slot must be morning or evening")
		}
		date := httpx.QueryString(c, "date")
		if date == nil || !dateRe.MatchString(*date) {
			return httpx.BadRequest("date must be yyyy-MM-dd when slot is given")
		}
		found, err := h.repo.Find(ctx, *date, *rawSlot, language)
		if err != nil {
			return err
		}
		if found == nil {
			return httpx.NotFound("No market brief for that date and slot.")
		}
		brief = *found
	default:
		latest, err := h.repo.Latest(ctx, language)
		if err != nil {
			return err
		}
		if latest != nil {
			brief = *latest
		} else {
			brief = emptyResponse(language, true)
		}
	}
	// Stored payloads never carry `previous`; it is worked out per request.
	if brief.TradingDate != nil && brief.Slot != nil {
		prev, err := h.repo.Previous(ctx, *brief.TradingDate, *brief.Slot, language)
		if err != nil {
			return err
		}
		brief.Previous = prev
	}
	c.Response().Header().Set("Cache-Control", "private, max-age=300")
	return httpx.OK(c, brief)
}

// GET /v1/market/brief/notifications
func (h *handlers) notificationPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	prefs, err := h.prefs.Get(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, prefs)
}

// preferencesBody is MarketBriefNotificationPreferences as a request.
type preferencesBody struct {
	Enabled  bool   `json:"enabled"`
	Language string `json:"language"`
}

// PUT /v1/market/brief/notifications
func (h *handlers) updateNotificationPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body preferencesBody
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	prefs, err := h.prefs.Update(c.Request().Context(), s.UserID, body.Enabled, body.Language)
	if err != nil {
		return err
	}
	return httpx.OK(c, prefs)
}

// NewLiveGenerator is MarketBriefGenerator.live: Yahoo quotes, the shared
// news chain, the Finnhub earnings calendar, a web-search client on the
// configured AI key and base URL (its own model, 6000 tokens, 90 s), and
// the platform's default chain as the fallback. No key or base URL means
// no web client, and every run is degraded.
func NewLiveGenerator(d *server.Deps, s Settings) (*LLMGenerator, error) {
	p, err := aiplatform.Platform(d)
	if err != nil {
		return nil, err
	}
	news, err := newsprovider.Shared(d)
	if err != nil {
		return nil, err
	}
	cal, err := earnings.CalendarFromEnv(nil)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(s.Model)
	if model == "" {
		model = DefaultModel
	}
	g := &LLMGenerator{
		Quotes:   yahoochart.New(yahoochart.Settings{BaseURL: s.YahooBaseURL}.ResolveBaseURL(), &http.Client{}, d.Logger),
		News:     news.Provider,
		Earnings: cal,
		WebModel: model,
		Fallback: func() ai.ChatClient { return p.Default },
		Now:      time.Now,
		Logger:   d.Logger,
	}
	if p.Config.APIKey != "" && p.Config.BaseURL != "" {
		g.WebClient = &ai.OpenAIClient{
			APIKey: p.Config.APIKey, Model: model, BaseURL: p.Config.BaseURL,
			MaxTokens: 6000, Timeout: 90 * time.Second, HTTP: p.HTTP, Logger: d.Logger,
		}
	}
	return g, nil
}

// JobName is the V2_JOBS name of MarketBriefJob.
const JobName = "market_brief"

// Job is MarketBriefJob: a five-minute tick (after one minute) asks the
// schedule whether a window is open; the database (unique per slot and
// language) is the record of what is done. Like Vapor's, it runs only with
// MARKET_BRIEF_ENABLED on.
type Job struct {
	Feature Feature
	// InitialDelay and Interval override Vapor's 60 s and 300 s (tests).
	InitialDelay, Interval time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements features.Job.
func (Job) Name() string { return JobName }

// NewTicker wires one MarketBriefJob tick runner from the environment.
func (j Job) NewTicker(d *server.Deps, s Settings) (*Ticker, error) {
	t := &Ticker{Deps: d, Generator: j.Feature.Generator, Repository: j.Feature.repository(d), PushEnabled: s.IsPushEnabled()}
	if t.Generator == nil {
		g, err := NewLiveGenerator(d, s)
		if err != nil {
			return nil, err
		}
		t.Generator = g
	}
	if t.PushEnabled {
		ps, err := push.From(d)
		if err != nil {
			return nil, err
		}
		t.Notifier = &Notifier{Repository: t.Repository, DB: d.DB, Push: ps, Logger: d.Logger}
	}
	return t, nil
}

// Run implements features.Job. Ticks fire at a fixed rate; a tick still
// running when the next is due skips it (BackgroundJobState's overlap
// guard), and shutdown waits for the running tick.
func (j Job) Run(ctx context.Context, d *server.Deps) {
	s, err := j.Feature.settings()
	if err != nil {
		d.Logger.Error("market_brief config", "error", err)
		return
	}
	if !s.IsEnabled() {
		d.Logger.Info("market_brief job not started: MARKET_BRIEF_ENABLED is off")
		return
	}
	t, err := j.NewTicker(d, s)
	if err != nil {
		d.Logger.Error("market_brief setup", "error", err)
		return
	}
	initial, interval := j.InitialDelay, j.Interval
	if initial <= 0 {
		initial = 60 * time.Second
	}
	if interval <= 0 {
		interval = 300 * time.Second
	}
	now := j.Now
	if now == nil {
		now = time.Now
	}
	var running atomic.Bool
	var wg sync.WaitGroup
	defer wg.Wait()
	timer := time.NewTimer(initial)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if running.CompareAndSwap(false, true) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer running.Store(false)
				t.Tick(ctx, now())
			}()
		}
		timer.Reset(interval)
	}
}

// LatestReader reads what GET /v1/market/brief serves by default, for callers
// outside this feature. It leaves out `previous`, which only the share screen
// uses.
func (f Feature) LatestReader(d *server.Deps) (func(ctx context.Context, language string) (Response, error), error) {
	s, err := f.settings()
	if err != nil {
		return nil, err
	}
	enabled, repo := s.IsEnabled(), f.repository(d)
	return func(ctx context.Context, raw string) (Response, error) {
		language := ResolveLanguage(&raw)
		if !enabled {
			return emptyResponse(language, false), nil
		}
		latest, err := repo.Latest(ctx, language)
		if err != nil {
			return Response{}, err
		}
		if latest == nil {
			return emptyResponse(language, true), nil
		}
		return *latest, nil
	}, nil
}
