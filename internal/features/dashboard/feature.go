// Package dashboard ports Vapor's DashboardController (/v1/dashboard,
// /v1/dashboard/insights, /v1/dashboard/why-moved), DefaultDashboardService
// and DefaultWhyMovedService. GoalsController, which lives in the same Vapor
// directory, is not part of it.
//
// Vapor quirk ported as is: why-moved asks InsightsService.tickerSentiment
// for limit 0, and the repository applies it as SQL `LIMIT 0`, so no post is
// ever read: every mover's sentiment is nil and sentimentSource is absent.
package dashboard

import (
	"context"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/statistics"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/server"
	"norviq-api/internal/store/dashboarddb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/dashboard.
type Feature struct {
	// Stats overrides the statistics repository (tests).
	Stats OverviewSource
	// Insights overrides the Postgres insights reads (tests).
	Insights Insights
	// Market overrides the market overview source (tests); nil uses the
	// shared market service.
	Market MarketOverview
	// AI overrides the AI platform and cache (tests).
	AI *AI
}

// Name implements server.Feature.
func (Feature) Name() string { return "dashboard" }

type handlers struct {
	svc      *Service
	whyMoved *WhyMoved
}

// Register mirrors DashboardController.boot: ScopedBearerAuthenticator +
// guard + portfolio:read.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	q := dashboarddb.New(d.DB)
	stats := f.Stats
	if stats == nil {
		stats = statistics.NewRepository(d.DB)
	}
	insights := f.Insights
	if insights == nil {
		insights = DBInsights{Q: q}
	}
	mkt := f.Market
	if mkt == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return err
		}
		mkt = svc
	}
	var aiDeps AI
	if f.AI != nil {
		aiDeps = *f.AI
	} else {
		p, err := aiplatform.Platform(d)
		if err != nil {
			return err
		}
		aiDeps = AI{Platform: p, Cache: cache.New(d.Redis, "ai", d.Logger)}
	}
	exp, err := expenses.SharedService(d)
	if err != nil {
		return err
	}
	h := &handlers{
		svc: &Service{
			Stats: stats,
			Q:     q,
			Reports: func(ctx context.Context, userID uuid.UUID) ([]expenses.BudgetMonthSummaryResponse, error) {
				return exp.GetMonthlyReports(ctx, d.DB, userID, nil, nil)
			},
		},
		whyMoved: &WhyMoved{Stats: stats, Insights: insights, Market: mkt, Q: q, AI: aiDeps, Logger: d.Logger},
	}

	mw := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePortfolioRead)}
	r.V1.GET("/dashboard", h.dashboard, mw...)
	r.V1.GET("/dashboard/insights", h.insights, mw...)
	r.V1.GET("/dashboard/why-moved", h.whyMovedRoute, mw...)
	return nil
}

func (h *handlers) dashboard(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Dashboard(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) insights(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Insights(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) whyMovedRoute(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.whyMoved.WhyMoved(c, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
