// Package server assembles the echo application: global middleware in the
// order Vapor's configure.swift installed it, health endpoints, every feature's
// routes, and the strangler fallback to the Vapor backend.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/middleware"
	"norviq-api/internal/platform/metrics"
	"norviq-api/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	echomw "github.com/labstack/echo/v5/middleware"
	"github.com/redis/go-redis/v9"
)

// Deps is what every feature may use. Features take what they need from it
// and build their own services in Register.
type Deps struct {
	Config  config.Base
	Logger  *slog.Logger
	DB      *pgxpool.Pool
	Queries *store.Queries
	Redis   redis.UniversalClient // nil when REDIS_URL is unset
	Auth    *auth.Authenticator
	Admin   auth.Admin
	Limiter *middleware.Limiter
	Health  *Health
}

// Routes are the two mount points Vapor had: the root (`app`) and `/v1`
// (`api`).
type Routes struct {
	Root *echo.Group
	V1   *echo.Group

	bodyLimits map[string]int64 // "METHOD /path" -> bytes; see AllowLargeBody
}

// AllowLargeBody lifts the global 10 MB body cap for one route to limit
// bytes: Vapor's `body: .collect(maxSize:)` above defaultMaxBodySize. path
// is the full route path ("/v1/receipts/scan"). The handler still enforces
// its own limit, after its middleware, as Vapor's collect did. Register-time
// only; a no-op on Routes not built by New.
func (r *Routes) AllowLargeBody(method, path string, limit int64) {
	if r.bodyLimits != nil {
		r.bodyLimits[method+" "+path] = limit
	}
}

// Feature is one ported Vapor controller (or a few related ones).
type Feature interface {
	Name() string
	Register(d *Deps, r *Routes) error
}

// Server is the assembled HTTP handler.
type Server struct {
	Echo    *echo.Echo
	Handler http.Handler
}

// New builds the server and mounts features.
func New(d *Deps, features []Feature) (*Server, error) {
	st, err := newStrangler(d.Config.LegacyUpstreamURL, d.Config.ExtraDisabledRoutes, d.Logger)
	if err != nil {
		return nil, err
	}

	e := echo.NewWithConfig(echo.Config{
		Logger:           d.Logger,
		HTTPErrorHandler: httpx.ErrorHandler(d.Logger, d.Config.IsProduction()),
		JSONSerializer:   httpx.Serializer{},
		IPExtractor:      d.Limiter.ClientIP.Resolve,
		OnAddRoute:       st.mirror,
	})

	// HTTPMetricsMiddleware, `at: .beginning` (PROMETHEUS_ENABLED only).
	if metrics.Enabled() {
		e.Use(metrics.HTTPMiddleware(metrics.Shared))
	}

	bodyLimits := map[string]int64{}
	origins, err := allowedOrigins(d.Config.AllowedOrigins, d.Config.IsProduction())
	if err != nil {
		return nil, err
	}
	e.Use(
		echomw.CORSWithConfig(echomw.CORSConfig{
			AllowOrigins: origins,
			AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodOptions, http.MethodDelete, http.MethodPatch},
			AllowHeaders: []string{"Accept", "Authorization", "Content-Type", "Origin", "X-Requested-With", "User-Agent", "Access-Control-Allow-Origin"},
		}),
		middleware.VaryOrigin(),
		middleware.SecurityHeaders(d.Config.IsProduction()),
		echomw.GzipWithConfig(echomw.GzipConfig{MinLength: 1024}),
		middleware.RequestLog(d.Logger),
		bodyLimit(10*1024*1024, bodyLimits),
		echomw.Recover(),
	)

	routes := &Routes{Root: e.Group(""), V1: e.Group("/v1"), bodyLimits: bodyLimits}
	registerHealth(d, routes)
	for _, f := range features {
		if err := f.Register(d, routes); err != nil {
			return nil, err
		}
	}

	st.app = e
	return &Server{Echo: e, Handler: st}, nil
}

// allowedOrigins is ProductionConfiguration.allowedOrigins: the
// comma-separated ALLOWED_ORIGINS, or "*" outside production when unset.
func allowedOrigins(raw string, production bool) ([]string, error) {
	var out []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		if production {
			return nil, configError("ALLOWED_ORIGINS is required in production.")
		}
		return []string{"http://localhost:3000", "http://localhost:8080"}, nil
	}
	if production {
		var unsafe []string
		for _, o := range out {
			l := strings.ToLower(o)
			if l == "*" || strings.Contains(l, "localhost") || strings.Contains(l, "127.0.0.1") || strings.Contains(l, "0.0.0.0") {
				unsafe = append(unsafe, o)
			}
		}
		if len(unsafe) > 0 {
			return nil, configError("ALLOWED_ORIGINS contains unsafe production origins: " + strings.Join(unsafe, ", "))
		}
	}
	return out, nil
}

type configError string

func (e configError) Error() string { return string(e) }

// HealthCheck is one entry of the readiness report.
type HealthCheck struct {
	Status    string   `json:"status"`
	Message   *string  `json:"message,omitempty"`
	LatencyMs *float64 `json:"latencyMs,omitempty"`
}

// Health collects readiness checks; features add their own.
type Health struct {
	mu     sync.Mutex
	checks map[string]func(ctx context.Context) HealthCheck
}

// NewHealth returns an empty registry.
func NewHealth() *Health { return &Health{checks: map[string]func(context.Context) HealthCheck{}} }

// Add registers or replaces a named readiness check.
func (h *Health) Add(name string, check func(ctx context.Context) HealthCheck) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = check
}

func (h *Health) run(ctx context.Context) map[string]HealthCheck {
	h.mu.Lock()
	names := make([]string, 0, len(h.checks))
	for n := range h.checks {
		names = append(names, n)
	}
	h.mu.Unlock()
	sort.Strings(names)
	out := make(map[string]HealthCheck, len(names))
	for _, n := range names {
		h.mu.Lock()
		check := h.checks[n]
		h.mu.Unlock()
		out[n] = check(ctx)
	}
	return out
}

type statusResponse struct {
	Status string `json:"status"`
}

type readinessResponse struct {
	Status      string                 `json:"status"`
	Checks      map[string]HealthCheck `json:"checks"`
	Version     string                 `json:"version"`
	Environment string                 `json:"environment"`
}

func registerHealth(d *Deps, r *Routes) {
	ok := func(c *echo.Context) error { return httpx.OK(c, statusResponse{Status: "ok"}) }
	r.Root.GET("/health", ok)
	r.Root.GET("/health/live", ok)
	r.Root.GET("/health/ready", func(c *echo.Context) error {
		checks := d.Health.run(c.Request().Context())
		status := "ready"
		for _, ch := range checks {
			if ch.Status == "unhealthy" {
				status = "not_ready"
				break
			}
			if ch.Status == "degraded" {
				status = "degraded"
			}
		}
		code := http.StatusOK
		if status == "not_ready" {
			code = http.StatusServiceUnavailable
		}
		return httpx.JSON(c, code, readinessResponse{
			Status:      status,
			Checks:      checks,
			Version:     d.Config.AppVersion(),
			Environment: string(d.Config.Environment),
		})
	})
	r.V1.GET("", func(c *echo.Context) error { return c.String(http.StatusOK, "It works!") })
	r.V1.GET("/hello", func(c *echo.Context) error { return c.String(http.StatusOK, "Hello, world!") })
}

// bodyLimit is echo's BodyLimit at def bytes, except on routes given their
// own cap with AllowLargeBody. Echo routes before global middleware, so
// c.Path() is the matched route here. limits is only written before serving.
func bodyLimit(def int64, limits map[string]int64) echo.MiddlewareFunc {
	std := echomw.BodyLimit(def)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		dflt := std(next)
		return func(c *echo.Context) error {
			if n, ok := limits[c.Request().Method+" "+c.Path()]; ok {
				return echomw.BodyLimit(n)(next)(c)
			}
			return dflt(c)
		}
	}
}
