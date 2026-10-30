// Package budgeting ports Vapor's BudgetingEngineController (drift
// dashboards, alert policy, bulk targets, reallocations, templates,
// spend-to-units and discipline under /v1/budget), BudgetingEngineService and
// BudgetDriftEvaluator. The snapshot/item CRUD under the same prefix is
// BudgetController, ported with ExpensesService in package expenses.
package budgeting

import (
	"context"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/telegram"
	"norviq-api/internal/server"
	"norviq-api/internal/store/notificationsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Settings are the variables BudgetDriftEvaluator read.
type Settings struct {
	DriftAlertsEnabled string `env:"BUDGET_DRIFT_ALERTS_ENABLED"`
}

// PremiumGate is usageCounterService.requirePremium.
type PremiumGate interface {
	RequirePremium(ctx context.Context, feature entitlements.Feature, userID uuid.UUID) error
}

// Feature mounts the budgeting engine routes. Zero values build everything
// from the environment; tests override them.
type Feature struct {
	Premium   PremiumGate
	Quotes    Quoter
	Publisher Publisher
	Now       func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "budgeting" }

// NewEngine builds the engine and its drift evaluator from the environment,
// for other domains that reuse Dashboard or Evaluate (ExpensesService, the
// bulk importer, digests, wealth automation).
func NewEngine(d *server.Deps) (*Engine, error) {
	return Feature{}.engine(d)
}

func (f Feature) engine(d *server.Deps) (*Engine, error) {
	s, err := config.Load[Settings]()
	if err != nil {
		return nil, err
	}
	quotes := f.Quotes
	if quotes == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return nil, err
		}
		quotes = svc
	}
	pub := f.Publisher
	if pub == nil {
		p, err := push.From(d)
		if err != nil {
			return nil, err
		}
		// Vapor offered the event to APNS then Telegram; the Telegram
		// channel arrives with the messaging port.
		ip := inbox.NewPublisher(notificationsdb.New(d.DB), p, d.Logger, telegram.Channels(d)...)
		if f.Now != nil {
			ip.Now = f.Now
		}
		pub = ip
	}
	e := &Engine{DB: d.DB, Quotes: quotes, Now: f.Now, Logger: d.Logger}
	e.Evaluator = &Evaluator{Engine: e, Publisher: pub, AlertsEnabled: config.Bool(s.DriftAlertsEnabled, true)}
	return e, nil
}

// Register implements server.Feature, mirroring BudgetingEngineController.boot:
// routes.grouped(ScopedBearerAuthenticator(), SessionToken.guardMiddleware())
// then ScopeRequirementMiddleware(.budgetRead / .budgetWrite). Middleware is
// per route: /v1/budget is shared with package expenses.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	e, err := f.engine(d)
	if err != nil {
		return err
	}
	premium := f.Premium
	if premium == nil {
		premium = entitlements.NewFromEnv(d.DB)
	}
	h := &handlers{engine: e, premium: premium}
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeBudgetRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeBudgetWrite)}

	b := r.V1
	b.GET("/budget/snapshots/:snapshotId/drift", h.drift, read...)
	b.GET("/budget/dca-capacity", h.dcaCapacity, read...)
	b.PUT("/budget/dca-capacity", h.updateDcaCapacity, write...)
	b.GET("/budget/discipline", h.discipline, read...)
	b.PUT("/budget/snapshots/:snapshotId/alert-policy", h.updatePolicy, write...)
	b.PATCH("/budget/snapshots/:snapshotId/items", h.bulkUpdate, write...)
	b.POST("/budget/reallocations/preview", h.preview, write...)
	b.POST("/budget/reallocations", h.commit, write...)
	b.GET("/budget/reallocations", h.history, read...)
	b.GET("/budget/templates", h.templates, read...)
	b.POST("/budget/templates", h.createTemplate, write...)
	b.PUT("/budget/templates/:templateId", h.updateTemplate, write...)
	b.DELETE("/budget/templates/:templateId", h.deleteTemplate, write...)
	b.POST("/budget/templates/:templateId/apply", h.applyTemplate, write...)
	return nil
}
