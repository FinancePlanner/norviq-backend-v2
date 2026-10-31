// Package expenses ports Vapor's DefaultExpensesService (the hub other
// expense domains build on), ExpenseBulkImporter, ExpenseCsvService and the
// routes of ExpensesController (/v1/expenses), BudgetController
// (/v1/budget/snapshots, /v1/budget/items) and ReportsController
// (/v1/reports). The spreadsheet/receipt imports and the budgeting engine
// live in their own packages and take this package's Service from
// SharedService.
package expenses

import (
	"context"
	"encoding/json"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	echomw "github.com/labstack/echo/v5/middleware"
)

// OverviewSource supplies the parts of GET /v1/reports/overview that live in
// other domains: StatisticsService.overviewStatistics (its generatedAt) and
// ReportsController.resolvePortfolioStatistics (the importedStocks
// statistics, falling back to live-priced holdings via
// PortfolioSnapshotValuator). PortfolioStatistics is the encoded
// ImportedStocksStatisticsDTO.
type OverviewSource interface {
	PortfolioOverview(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID) (generatedAt string, portfolioStatistics json.RawMessage, err error)
}

// Feature mounts the expenses, budget snapshot/item and reports routes.
type Feature struct {
	// Drift wires BudgetDriftEvaluator (budgeting engine). Nil: drift is not
	// evaluated after expense writes.
	Drift func(d *server.Deps) DriftEvaluator
	// Overview wires GET /v1/reports/overview. Nil: the route is not
	// registered (404).
	Overview func(d *server.Deps) OverviewSource
	// Service overrides the shared service (tests).
	Service *Service
	// Entitlements overrides the environment's billing config (tests).
	Entitlements *entitlements.Config
	Now          func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "expenses" }

// Register implements server.Feature, mirroring ExpensesController.boot,
// BudgetController.boot and ReportsController.boot. Routes are registered
// one by one (a group's Use would add catch-all routes under prefixes that
// sibling packages share).
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	svc := f.Service
	if svc == nil {
		var err error
		if svc, err = SharedService(d); err != nil {
			return err
		}
	}
	if f.Drift != nil {
		svc.SetDriftEvaluator(f.Drift(d))
	}
	ent := entitlements.NewFromEnv(d.DB)
	if f.Entitlements != nil {
		ent = entitlements.New(d.DB, *f.Entitlements)
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	h := &handlers{d: d, svc: svc, ent: ent, now: now}
	if f.Overview != nil {
		h.overview = f.Overview(d)
	}

	mw := func(extra ...echo.MiddlewareFunc) []echo.MiddlewareFunc {
		return append([]echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard()}, extra...)
	}
	expRead := mw(auth.RequireScope(auth.ScopeExpensesRead))
	expWrite := mw(auth.RequireScope(auth.ScopeExpensesWrite))
	firstParty := mw(auth.FirstPartyOnly())

	v1 := r.V1
	v1.GET("/expenses/partner", h.getHouseholdPartner, firstParty...)
	v1.PUT("/expenses/partner", h.updateHouseholdPartner, firstParty...)
	v1.GET("/expenses/categories", h.getCategories, expRead...)
	v1.POST("/expenses/categories", h.createCategory, expWrite...)
	v1.DELETE("/expenses/categories/:categoryId", h.deleteCategory, expWrite...)
	// body: .collect(maxSize: "1mb"), applied after the middleware.
	v1.POST("/expenses/import", h.importCSV, append(mw(auth.RequireScope(auth.ScopeExpensesWrite)), echomw.BodyLimit(CSVMaxBytes))...)
	v1.GET("/expenses/export.csv", h.exportCSV, expRead...)
	v1.GET("/expenses/recurring", h.getRecurringTemplates, firstParty...)
	v1.POST("/expenses/recurring", h.createRecurringTemplate, firstParty...)
	v1.PATCH("/expenses/recurring/:templateId", h.updateRecurringTemplate, firstParty...)
	v1.DELETE("/expenses/recurring/:templateId", h.deleteRecurringTemplate, firstParty...)
	v1.GET("/expenses", h.getExpenses, expRead...)
	v1.POST("/expenses", h.createExpense, expWrite...)
	v1.PATCH("/expenses/:expenseId", h.updateExpense, expWrite...)
	v1.DELETE("/expenses/:expenseId", h.deleteExpense, expWrite...)

	budRead := mw(auth.RequireScope(auth.ScopeBudgetRead))
	budWrite := mw(auth.RequireScope(auth.ScopeBudgetWrite))
	v1.GET("/budget/snapshots", h.getSnapshots, budRead...)
	v1.POST("/budget/snapshots", h.createSnapshot, budWrite...)
	v1.PATCH("/budget/snapshots/:snapshotId", h.updateSnapshot, budWrite...)
	v1.DELETE("/budget/snapshots/:snapshotId", h.deleteSnapshot, budWrite...)
	v1.GET("/budget/snapshots/:snapshotId/items", h.getSnapshotItems, budRead...)
	v1.GET("/budget/items", h.getAllPlanItems, budRead...)
	v1.POST("/budget/items", h.createPlanItem, budWrite...)
	v1.PATCH("/budget/items/:itemId", h.updatePlanItem, budWrite...)
	v1.DELETE("/budget/items/:itemId", h.deletePlanItem, budWrite...)

	repRead := mw(auth.RequireScope(auth.ScopeReportsRead))
	if h.overview != nil {
		v1.GET("/reports/overview", h.getReportsOverview, repRead...)
	}
	v1.GET("/reports/suggestions", h.getSuggestions, repRead...)
	v1.POST("/reports/suggestions/:id/dismiss", h.dismissSuggestion, firstParty...)
	v1.GET("/reports/expenses", h.getExpenseReports, repRead...)
	return nil
}
