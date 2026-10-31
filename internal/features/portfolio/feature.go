// Package portfolio ports PortfolioController (/v1/portfolio/*, /v1/lots,
// /v1/pnl), the authenticated routes of PortfolioShareController
// (/v1/portfolio/share-links) and PortfolioSnapshotJob, plus the services
// they share: the live valuation (PortfolioValuationService), the stored
// valuation (PortfolioSnapshotValuator), the performance builder and the
// filter resolver. Those are exported for the achievements, leaderboards,
// reports and pilots ports.
package portfolio

import (
	"github.com/labstack/echo/v5"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
)

// Feature mounts the routes.
type Feature struct {
	// Quotes overrides the environment-built market data service (tests).
	Quotes QuoteSource
	// Fx overrides the FX source of the realized P&L (tests). Nil uses
	// Quotes when it is one (the market data service is).
	Fx FxSource
}

// Name implements server.Feature.
func (Feature) Name() string { return "portfolio" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	quotes := f.Quotes
	if quotes == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return err
		}
		quotes = svc
	}
	fx := f.Fx
	if fx == nil {
		fx, _ = quotes.(FxSource)
	}
	h := &handlers{
		d:            d,
		filters:      FilterResolver{Access: portfolios.NewAccess(d.DB)},
		valuation:    ValuationService{Quotes: quotes, Logger: d.Logger},
		fx:           fx,
		entitlements: entitlements.NewFromEnv(d.DB),
	}

	// PortfolioController.boot: ScopedBearerAuthenticator + guard, then a
	// scope per group. No rate limit: routes.swift registers both
	// controllers on the bare api group.
	read := chain(d, auth.RequireScope(auth.ScopePortfolioRead))
	write := chain(d, auth.RequireScope(auth.ScopePortfolioWrite))
	txRead := chain(d, auth.RequireScope(auth.ScopeTransactionsRead))
	summary := chain(d, auth.RequireScope(auth.ScopePortfolioRead, auth.ScopeMarketRead))

	v1 := r.V1
	v1.GET("/portfolio/performance", h.performance, read...)
	v1.GET("/portfolio/sector-exposure", h.sectorExposure, read...)
	v1.GET("/portfolio/dividends", h.dividends, read...)
	v1.GET("/portfolio/lists", h.listPortfolioLists, read...)
	v1.POST("/portfolio/lists", h.createPortfolioList, write...)
	v1.PATCH("/portfolio/lists/:portfolioListId", h.updatePortfolioList, write...)
	v1.DELETE("/portfolio/lists/:portfolioListId", h.deletePortfolioList, write...)
	v1.GET("/lots", h.lots, txRead...)
	v1.GET("/pnl", h.pnl, txRead...)
	v1.GET("/portfolio/summary", h.summary, summary...)

	// PortfolioShareController.boot, minus the public slug route (still on
	// Vapor).
	v1.GET("/portfolio/share-links", h.listShareLinks, read...)
	v1.GET("/portfolio/share-links/:scope", h.shareLinkStatus, read...)
	v1.POST("/portfolio/share-links/:scope", h.createShareLink, write...)
	v1.DELETE("/portfolio/share-links/:scope", h.revokeShareLinks, write...)
	return nil
}

// chain is ScopedBearerAuthenticator, SessionToken.guardMiddleware and the
// group's ScopeRequirementMiddleware, in that order.
func chain(d *server.Deps, scope echo.MiddlewareFunc) []echo.MiddlewareFunc {
	return []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), scope}
}
