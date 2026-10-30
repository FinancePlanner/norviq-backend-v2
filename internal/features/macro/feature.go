// Package macro ports the macro/inflation domain: MacroController
// (/v1/macro/*), DefaultMacroService, the FRED / Eurostat / IBGE / BCB / ECB
// / Nowflation providers, and MacroRefreshJob.
package macro

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/store/macrodb"

	"github.com/labstack/echo/v5"
)

// Settings are configure.swift's macro environment variables.
type Settings struct {
	MacroEnabled           string `env:"MACRO_ENABLED"`
	FREDAPIKey             string `env:"FRED_API_KEY"`
	NowflationBaseURL      string `env:"NOWFLATION_BASE_URL"`
	NowflationSnapshotPath string `env:"NOWFLATION_SNAPSHOT_PATH"`
	AllowStubFallback      string `env:"MACRO_ALLOW_STUB_FALLBACK"`
	SeekingAlphaEnabled    string `env:"MACRO_ENRICHMENT_SEEKING_ALPHA_ENABLED"`
	InvestingEnabled       string `env:"MACRO_ENRICHMENT_INVESTING_ENABLED"`
	RefreshIntervalSeconds string `env:"MACRO_REFRESH_INTERVAL_SECONDS"`
	USRefreshSeconds       string `env:"MACRO_US_REFRESH_SECONDS"`
	IntlRefreshSeconds     string `env:"MACRO_INTL_REFRESH_SECONDS"`

	// v2-only overrides for test stubs and parity. Vapor hard-coded these;
	// unset means the real APIs.
	FREDBaseURL     string `env:"FRED_BASE_URL"`
	EurostatBaseURL string `env:"EUROSTAT_BASE_URL"`
	IBGEBaseURL     string `env:"IBGE_BASE_URL"`
	BCBBaseURL      string `env:"BCB_BASE_URL"`
	ECBBaseURL      string `env:"ECB_BASE_URL"`
	BLSCalendarURL  string `env:"BLS_CALENDAR_URL"`
}

// orDefault is v when set, else def.
func orDefault(v, def string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return def
}

// NewService wires the service exactly as configure.swift did.
func NewService(s Settings, d *server.Deps, client *http.Client) *Service {
	fredKey := strings.TrimSpace(s.FREDAPIKey)
	logger := d.Logger
	nowflation := &Nowflation{
		BaseURL:      strings.TrimSpace(s.NowflationBaseURL),
		SnapshotPath: strings.TrimSpace(s.NowflationSnapshotPath),
		Client:       client, Logger: logger,
	}
	plan := Plan(config.Bool(s.MacroEnabled, true), fredKey != "", nowflation.Enabled())
	var fred *FRED
	if fredKey != "" {
		fred = &FRED{
			APIKey: fredKey, BaseURL: orDefault(s.FREDBaseURL, FREDBaseURL), Client: client, Logger: logger,
			BLS: BLSCalendar{URL: orDefault(s.BLSCalendarURL, BLSCalendarURL), Client: client, Logger: logger},
		}
	}
	ecb := NewECB(client, logger)
	ecb.BaseURL = orDefault(s.ECBBaseURL, ECBBaseURL)
	registry := BuildRegistry(plan, Concrete{
		FRED:       fred,
		Eurostat:   &Eurostat{BaseURL: orDefault(s.EurostatBaseURL, EurostatBaseURL), Client: client, Logger: logger},
		IBGE:       &IBGE{BaseURL: orDefault(s.IBGEBaseURL, IBGEBaseURL), Client: client},
		Nowflation: nowflation,
		ECB:        ecb,
		Extra: []Enrichment{
			SeekingAlphaEnrichment(config.Bool(s.SeekingAlphaEnabled, false), logger),
			InvestingComEnrichment(config.Bool(s.InvestingEnabled, false), logger),
		},
	}, logger)
	return &Service{
		Repo:              Repo{Q: macrodb.New(d.DB)},
		Registry:          registry,
		AllowStubFallback: config.Bool(s.AllowStubFallback, !d.Config.IsProduction()),
		FREDHub:           fred,
		BCBHub:            &BCB{BaseURL: orDefault(s.BCBBaseURL, BCBBaseURL), Client: client},
		Redis:             d.Redis,
		Logger:            logger,
	}
}

var shared sync.Map // *server.Deps -> *Service

// SharedService returns the environment-configured macro service for d,
// built once so the routes and the refresh job share it. Other domains
// (the weekly digest's personal inflation) should take it from here.
func SharedService(d *server.Deps) (*Service, error) {
	if v, ok := shared.Load(d); ok {
		return v.(*Service), nil
	}
	s, err := config.Load[Settings]()
	if err != nil {
		return nil, err
	}
	v, _ := shared.LoadOrStore(d, NewService(s, d, &http.Client{}))
	return v.(*Service), nil
}

// Feature mounts /v1/macro.
type Feature struct {
	// Service overrides the environment-built service (tests).
	Service *Service
}

// Name implements server.Feature.
func (Feature) Name() string { return "macro" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	svc := f.Service
	if svc == nil {
		var err error
		if svc, err = SharedService(d); err != nil {
			return err
		}
	}
	h := handlers{svc: svc}
	status := SharedSyncStatus(d)
	d.Health.Add("macro", func(ctx context.Context) server.HealthCheck {
		return svc.HealthCheck(ctx, status, time.Now())
	})
	// routes.swift: api.grouped(RateLimitMiddleware(80/60s, "ratelimit:macro"))
	// wraps the controller outside its authenticator, so the bucket is per
	// client IP.
	g := r.V1.Group("/macro",
		d.Limiter.Limit(80, 60*time.Second, "ratelimit:macro"),
		d.Auth.Scoped(), auth.Guard(),
		auth.RequireScope(auth.ScopeMarketRead),
	)
	g.GET("/inflation/current", h.current)
	g.GET("/inflation/components", h.components)
	g.GET("/top-movers", h.topMovers)
	g.GET("/inflation/series", h.series)
	g.GET("/inflation/personal", h.personal)
	g.GET("/supported-countries", h.supportedCountries)
	g.GET("/fed-watch", h.fedWatch)
	g.GET("/policy-watch", h.policyWatch)
	g.GET("/housing", h.housing)
	g.GET("/economy", h.economy)
	g.GET("/items", h.items)
	g.GET("/items/:itemId/series", h.itemSeries)
	return nil
}

type handlers struct{ svc *Service }

// country falls back to US for an absent or unknown value.
func country(c *echo.Context) Country {
	if v, ok := ParseCountry(httpx.QueryString(c, "country")); ok {
		return v
	}
	return US
}

func (h handlers) current(c *echo.Context) error {
	snap, err := h.svc.CurrentInflation(c.Request().Context(), country(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, snap)
}

func (h handlers) components(c *echo.Context) error {
	snap, err := h.svc.CurrentInflation(c.Request().Context(), country(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, snap.Components)
}

func isSwiftWhitespace(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) }

func (h handlers) topMovers(c *echo.Context) error {
	snap, err := h.svc.CurrentInflation(c.Request().Context(), country(c))
	if err != nil {
		return err
	}
	focus := httpx.QueryString(c, "focus")
	if focus == nil {
		return httpx.OK(c, snap.TopMovers)
	}
	// Swift's split(separator:) drops empty pieces before the trim.
	var wanted []string
	for _, part := range strings.Split(strings.ToLower(*focus), ",") {
		if part != "" {
			wanted = append(wanted, strings.TrimFunc(part, isSwiftWhitespace))
		}
	}
	out := []TopMover{}
	for _, m := range snap.TopMovers {
		category := strings.ToLower(m.Category)
		for _, w := range wanted {
			// Foundation's contains("") is false, so a blank piece
			// matches nothing.
			if w != "" && strings.Contains(category, w) {
				out = append(out, m)
				break
			}
		}
	}
	return httpx.OK(c, out)
}

func (h handlers) series(c *echo.Context) error {
	resp, err := h.svc.Series(c.Request().Context(), country(c),
		httpx.QueryString(c, "series"), httpx.QueryString(c, "from"), httpx.QueryString(c, "to"),
		httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 120))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h handlers) personal(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.PersonalInflation(c.Request().Context(), s.UserID, country(c),
		httpx.IntOr(httpx.QueryOptionalInt(c, "months"), 12))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h handlers) supportedCountries(c *echo.Context) error {
	return httpx.OK(c, h.svc.SupportedCountries())
}

func (h handlers) fedWatch(c *echo.Context) error {
	resp, err := h.svc.FedWatch(c.Request().Context())
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h handlers) policyWatch(c *echo.Context) error {
	resp, err := h.svc.PolicyWatch(c.Request().Context(), country(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h handlers) housing(c *echo.Context) error {
	resp, err := h.svc.Housing(c.Request().Context(), country(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h handlers) economy(c *echo.Context) error {
	resp, err := h.svc.Economy(c.Request().Context(), country(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h handlers) items(c *echo.Context) error {
	resp, err := h.svc.Items(c.Request().Context(), country(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h handlers) itemSeries(c *echo.Context) error {
	itemID := c.Param("itemId")
	if itemID == "" {
		return httpx.BadRequest("Missing item id.")
	}
	resp, err := h.svc.ItemSeries(c.Request().Context(), itemID, country(c),
		httpx.QueryString(c, "from"), httpx.QueryString(c, "to"),
		httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 120))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}
