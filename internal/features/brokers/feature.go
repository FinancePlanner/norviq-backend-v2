// Package brokers ports Broker/*.swift: BrokerController (/v1/brokers: list,
// holdings, IBKR connect/sync/disconnect, CSV and screenshot imports),
// AuthController.brokerIBKRCallback, DefaultBrokersService with the IBKR
// gateway, OAuth2 and Web Service (SOD) clients, CsvPortfolioImportService,
// ScreenshotPortfolioImportService and IBKRSyncJob.
package brokers

import (
	"context"
	"net/http"
	"os"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// defaultGatewayBaseURL is the Client Portal gateway Vapor falls back to.
const defaultGatewayBaseURL = "http://localhost:5000/v1/api"

// Feature mounts BrokerController and the IBKR OAuth callback.
type Feature struct {
	// Market overrides the shared market data service's symbol search (tests).
	Market symbolSearch
	// HTTP overrides the client for IBKR and AI calls (tests).
	HTTP *http.Client
	// Entitlements overrides the environment-built billing service (tests).
	Entitlements *entitlements.Service
	// Extractor overrides the screenshot extractor (tests).
	Extractor Extractor
	// Consent overrides the AI consent check (tests).
	Consent func(c *echo.Context, userID uuid.UUID) error
	// Now overrides the clock (tests).
	Now func() time.Time
	// Env overrides os.Getenv (tests).
	Env func(string) string
	// Sleep overrides retry waits (tests).
	Sleep func(ctx context.Context, d time.Duration) error
}

// Name implements server.Feature.
func (Feature) Name() string { return "brokers" }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// service builds DefaultBrokersService as configure.swift did; a bad
// IBKR_CONNECT_MODE or a write-capable IBKR_OAUTH_SCOPE is a startup error.
func (f Feature) service(d *server.Deps) (*service, error) {
	env := f.Env
	if env == nil {
		env = os.Getenv
	}
	now := f.Now
	if now == nil {
		now = portfolios.Now
	}
	sleep := f.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	vault, err := crypto.VaultFromEnv(d.Config.IsProduction(), d.Logger)
	if err != nil {
		return nil, err
	}
	oauthCfg, err := oauthConfigFromEnv(env)
	if err != nil {
		return nil, err
	}
	mode, err := connectModeFromEnv(env("IBKR_CONNECT_MODE"), oauthCfg != nil)
	if err != nil {
		return nil, err
	}
	baseURL := defaultGatewayBaseURL
	if v := portfolios.TrimSwift(env("IBKR_API_BASE_URL")); v != "" {
		baseURL = v
	}
	currency := env("MARKET_DEFAULT_CURRENCY")
	if currency == "" {
		currency = "USD"
	}
	s := &service{
		db:      d.DB,
		logger:  d.Logger,
		vault:   vault,
		gateway: gatewayClient{BaseURL: baseURL, DefaultCurrency: currency, HTTP: client, Sleep: sleep},
		oauthOn: mode == connectOAuth2,
		http:    client,
		search:  f.Market,
		env:     env,
		now:     now,
		sleep:   sleep,

		entitlements: f.Entitlements,
		extractor:    f.Extractor,
	}
	if oauthCfg != nil {
		s.oauth = &oauthClient{config: *oauthCfg, http: client, logger: d.Logger, now: now}
	}
	if s.search == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return nil, err
		}
		s.search = svc
	}
	if s.entitlements == nil {
		s.entitlements = entitlements.NewFromEnv(d.DB)
	}
	return s, nil
}

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s, err := f.service(d)
	if err != nil {
		return err
	}
	h := &handlers{s: s, postCommit: newPostCommit(d)}
	if f.Consent != nil {
		h.consent = f.Consent
	}
	if s.extractor == nil || h.consent == nil {
		p, err := aiplatform.Platform(d)
		if err != nil {
			return err
		}
		if s.extractor == nil {
			s.extractor = ExtractorFromEnv(env(f), p.Config, s.http, d.Logger)
		}
		if h.consent == nil {
			h.consent = platformConsent(p)
		}
	}

	// routes.swift: api.grouped(ScopedBearerAuthenticator(), RateLimit(30/60s,
	// "ratelimit:broker"), Idempotency("idempotency:broker")); the
	// controller then adds its own authenticator, the session guard and a
	// scope per group. The authenticator runs first so buckets are per user.
	outer := []echo.MiddlewareFunc{
		d.Auth.Scoped(),
		d.Limiter.Limit(30, time.Minute, "ratelimit:broker"),
		d.Limiter.Idempotency("idempotency:broker"),
		auth.Guard(),
	}
	with := func(extra ...echo.MiddlewareFunc) []echo.MiddlewareFunc {
		return append(append([]echo.MiddlewareFunc{}, outer...), extra...)
	}
	read := with(auth.RequireScope(auth.ScopeIntegrationsRead))
	write := with(auth.RequireScope(auth.ScopeIntegrationsWrite))
	holdingsRead := with(auth.RequireScope(auth.ScopeHoldingsRead))
	holdingsWrite := with(auth.RequireScope(auth.ScopeHoldingsWrite))
	// Screenshot import spends Norviq's AI budget: first-party only, with
	// its own 10/min limit.
	screenshot := with(auth.RequireScope(auth.ScopeHoldingsWrite), auth.FirstPartyOnly(),
		d.Limiter.Limit(10, time.Minute, "ratelimit:portfolio-screenshot"))

	r.V1.GET("/brokers", h.listBrokers, read...)
	r.V1.GET("/brokers/holdings", h.listHoldings, holdingsRead...)
	// Vapor keeps one trie per method and never falls back from a constant
	// segment to a parameter. GET /v1/brokers/ibkr enters the "ibkr" node
	// (from GET ibkr/sync/status), which has no handler of its own: Vapor's
	// route-not-found 404, before any group middleware (no 401 unsigned).
	r.V1.GET("/brokers/:provider", h.getBroker, append([]echo.MiddlewareFunc{shadowedByConstant("ibkr")}, read...)...)
	r.V1.POST("/brokers/import/csv", h.csvPreview, holdingsWrite...)
	r.V1.POST("/brokers/import/csv/commit", h.csvCommit, holdingsWrite...)
	// Three 8 MB screenshots outrun the app-wide body cap.
	r.AllowLargeBody(http.MethodPost, "/v1/brokers/import/screenshot", screenshotMaxBody)
	r.V1.POST("/brokers/import/screenshot", h.screenshotPreview, screenshot...)
	r.V1.POST("/brokers/import/screenshot/commit", h.screenshotCommit, screenshot...)
	r.V1.POST("/brokers/ibkr/connect/start", h.startConnect, write...)
	r.V1.POST("/brokers/ibkr/connect/credentials", h.connectCredentials, write...)
	r.V1.POST("/brokers/ibkr/sync", h.syncIBKR, write...)
	r.V1.GET("/brokers/ibkr/sync/status", h.syncStatus, read...)
	r.V1.DELETE("/brokers/ibkr/connection", h.disconnect, write...)

	// AuthController: the provider redirects the browser here, unauthenticated.
	r.V1.GET("/auth/brokers/ibkr/callback", h.callback)
	return nil
}

// shadowedByConstant answers Vapor's route-not-found 404 when :provider is
// a constant segment Vapor's router would have taken instead.
func shadowedByConstant(segment string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if c.Param("provider") == segment {
				return httpx.Abort(http.StatusNotFound, "Not Found")
			}
			return next(c)
		}
	}
}

func env(f Feature) func(string) string {
	if f.Env != nil {
		return f.Env
	}
	return os.Getenv
}
