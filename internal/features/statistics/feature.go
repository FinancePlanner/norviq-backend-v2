// Package statistics ports Vapor's StatisticsController (/v1/statistics/*),
// DefaultStatisticsService and DatabaseStatisticsRepository. The repository
// is exported because Vapor's dashboard, why-moved and achievements code read
// it too.
package statistics

import (
	"context"
	"strconv"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts the statistics routes.
type Feature struct {
	// Entitlements overrides the environment-built billing service (tests).
	Entitlements func(d *server.Deps) *entitlements.Service
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "statistics" }

type handlers struct {
	repo *Repository
	ent  *entitlements.Service
	now  func() time.Time
}

// Register implements server.Feature, mirroring StatisticsController.boot:
// ScopedBearerAuthenticator + guard + portfolio:read on every route.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	ent := entitlements.NewFromEnv(d.DB)
	if f.Entitlements != nil {
		ent = f.Entitlements(d)
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	h := &handlers{repo: NewRepository(d.DB).WithClock(now), ent: ent, now: now}

	mw := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePortfolioRead)}
	st := r.V1.Group("/statistics")
	view := func(build func(*Repository, context.Context, uuid.UUID, QueryOptions) (ViewModel, error)) echo.HandlerFunc {
		return func(c *echo.Context) error {
			userID, opts, err := h.prepare(c)
			if err != nil {
				return err
			}
			m, err := build(h.repo, c.Request().Context(), userID, opts)
			if err != nil {
				return err
			}
			return httpx.OK(c, m.DTO())
		}
	}
	st.GET("/stocks/scorecard", view((*Repository).Overview), mw...)
	st.GET("/stocks/allocation", view((*Repository).StockAllocation), mw...)
	st.GET("/stocks/sector-allocation", view((*Repository).SectorAllocation), mw...)
	st.GET("/stocks/sector-gains", h.sectorGains, mw...)
	st.GET("/stocks/calendar-performance", view((*Repository).CalendarPerformance), mw...)
	st.GET("/stocks/contribution-analysis", view((*Repository).ContributionAnalysis), mw...)
	st.GET("/stocks/winners-losers", view((*Repository).WinnersVsLosers), mw...)
	st.GET("/stocks/volatility-snapshot", view((*Repository).VolatilitySnapshot), mw...)
	st.GET("/stocks/currency-split", view((*Repository).CurrencySplit), mw...)
	st.GET("/stocks/scenario-tracking", view((*Repository).ScenarioTracking), mw...)
	st.GET("/stocks/notes-quality", view((*Repository).NotesQualityMetrics), mw...)

	st.GET("/imported-stocks", view((*Repository).Overview), mw...)
	st.GET("/watchlist", view((*Repository).Overview), mw...)
	st.GET("/looklist", view((*Repository).Overview), mw...)
	st.GET("/market", view((*Repository).Overview), mw...)
	st.GET("/overview", view((*Repository).Overview), mw...)
	return nil
}

func (h *handlers) sectorGains(c *echo.Context) error {
	userID, opts, err := h.prepare(c)
	if err != nil {
		return err
	}
	out, err := h.repo.SectorGains(c.Request().Context(), userID, opts)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// prepare is the shared head of every handler: session, the statistics
// premium gate, then the query (decoded, then validated), in Vapor's order.
func (h *handlers) prepare(c *echo.Context) (uuid.UUID, QueryOptions, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, QueryOptions{}, err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureStatistics, s.UserID); err != nil {
		return uuid.Nil, QueryOptions{}, err
	}
	top, err := queryTop(c)
	if err != nil {
		return uuid.Nil, QueryOptions{}, err
	}
	in := QueryInput{
		Period:    httpx.QueryString(c, "period"),
		Top:       top,
		Benchmark: httpx.QueryString(c, "benchmark"),
		AsOf:      httpx.QueryString(c, "asOf"),
	}
	opts, err := BuildOptions(in, h.now())
	if err != nil {
		return uuid.Nil, QueryOptions{}, err
	}
	return s.UserID, opts, nil
}

// queryTop decodes `top` as `req.query.decode(StatisticsQueryParams.self)`
// did: present but not an Int (empty included) is a 400 with the
// URLEncodedFormDecoder's reason.
func queryTop(c *echo.Context) (*int, error) {
	raw := httpx.QueryString(c, "top")
	if raw == nil {
		return nil, nil
	}
	n, err := strconv.Atoi(*raw)
	if err != nil {
		return nil, httpx.BadRequest("Value was not of type 'Int' at path 'top'. Data found at 'top' was not Int.")
	}
	return &n, nil
}
