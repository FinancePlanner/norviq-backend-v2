// Package stocks ports StockController's holdings routes (/v1/stocks...),
// StockService and DatabaseStocksRepository, TransactionController and
// TransactionService (/v1/transactions), LedgerTradeRecorder, and
// SharingController's stock share page (/share/stock/:symbol).
//
// The watchlist, research and targets routes of StockController live in
// their own packages.
package stocks

import (
	"context"
	"log/slog"
	"net/url"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/activity"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/onboarding"
	"norviq-api/internal/server"
	"norviq-api/internal/store/activitydb"
	"norviq-api/internal/store/badgesdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

// MarketData is the slice of the market data service the stock routes use;
// *marketdata.Service satisfies it.
type MarketData interface {
	Analysis(ctx context.Context, symbol string, params marketdata.AnalysisParams) (marketdata.StockAnalysisMetricsResponse, error)
	Profile(ctx context.Context, symbol string) (marketdata.CompanyProfileResponse, error)
	Search(ctx context.Context, q string) ([]marketdata.SearchResultResponse, error)
}

// Feature mounts the holdings, transactions and stock share routes.
type Feature struct {
	// Market overrides the shared market data service (tests).
	Market MarketData
	// Entitlements overrides the environment-built billing service (tests).
	Entitlements *entitlements.Service
}

// Name implements server.Feature.
func (Feature) Name() string { return "stocks" }

type handlers struct {
	db      *pgxpool.Pool
	market  MarketData
	ent     *entitlements.Service
	badges  *badges.Service
	logger  *slog.Logger
	sharing sharingConfig
}

func (h *handlers) search() SymbolSearch { return h.market }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handlers{
		db:      d.DB,
		market:  f.Market,
		ent:     f.Entitlements,
		badges:  &badges.Service{Q: badgesdb.New(d.DB), Logger: d.Logger.Warn},
		logger:  d.Logger,
		sharing: loadSharingConfig(),
	}
	if h.market == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return err
		}
		h.market = svc
	}
	if h.ent == nil {
		h.ent = entitlements.NewFromEnv(d.DB)
	}

	// StockController.boot: ScopedBearerAuthenticator + guard, then the
	// holdings scope per group.
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeHoldingsRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeHoldingsWrite)}
	r.V1.GET("/stocks", h.listStocks, read...)
	r.V1.POST("/stocks", h.createStock, write...)
	r.V1.POST("/stocks/bulk", h.bulkCreateStocks, write...)
	r.V1.GET("/stocks/symbol/:symbol/insights", h.getStockInsights, read...)
	r.V1.GET("/stocks/:symbol/insights", h.getStockInsightsLegacy, read...)
	r.V1.GET("/stocks/symbol/:symbol/valuation", h.getStockValuation, read...)
	r.V1.POST("/stocks/symbol/:symbol/valuation", h.createStockValuation, write...)
	r.V1.PUT("/stocks/symbol/:symbol/valuation", h.updateStockValuation, write...)
	r.V1.GET("/stocks/id/:stockId", h.getStock, read...)
	r.V1.PUT("/stocks/id/:stockId", h.updateStock, write...)
	r.V1.POST("/stocks/id/:stockId/sell", h.sellStock, write...)
	r.V1.DELETE("/stocks/id/:stockId", h.deleteStock, write...)

	// TransactionController.boot.
	t := transactionHandlers{h: h}
	txRead := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeTransactionsRead)}
	txWrite := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeTransactionsWrite)}
	r.V1.GET("/transactions", t.list, txRead...)
	r.V1.POST("/transactions", t.create, txWrite...)
	r.V1.PATCH("/transactions/:transactionId", t.update, txWrite...)
	r.V1.DELETE("/transactions/:transactionId", t.delete, txWrite...)

	// SharingController: public, at the root. /share/app is not ported here.
	r.Root.GET("/share/stock/:symbol", h.shareStock)
	return nil
}

// pathParam is req.parameters.get(name), percent-decoded.
func pathParam(c *echo.Context, name string) string {
	raw := c.Param(name)
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}

// recordActivity is `try? userActivityService.recordActivity(...)`.
func (h *handlers) recordActivity(ctx context.Context, db activitydb.DBTX, rec activity.Record) {
	if err := activity.NewRecorder(db).Record(ctx, rec); err != nil {
		h.logger.Warn("stock activity record failed", "user", rec.UserID.String(), "error", err)
	}
}

// reconcileBadges is Request.reconcileBadges: evaluate badges, log a
// failure. (Vapor also queued a live achievement check here; v2 has no
// achievements engine yet.)
func (h *handlers) reconcileBadges(ctx context.Context, userID uuid.UUID) {
	if _, err := h.badges.Evaluate(ctx, userID); err != nil {
		h.logger.Warn("badge reconciliation failed", "user", userID.String(), "error", err)
	}
}

// latchHolding is Request.latchOnboarding(.holding, ...).
func (h *handlers) latchHolding(ctx context.Context, userID uuid.UUID) {
	onboarding.StampBestEffort(ctx, h.db, h.logger, onboarding.LatchHolding, userID)
}
