// Package watchlist ports the watchlist, watchlist lists, watchlist CSV
// import and price target routes of StockController (/v1/watchlist*,
// /v1/targets*), the smart screen routes of WealthAutomationController
// (/v1/watchlist/screens*), the holding risk profile routes of
// ScenarioController (/v1/holding-risk-profiles*), and the two pollers that
// read those tables: TargetAlertPoller and EarningsNotificationPoller.
package watchlist

import (
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/badgesdb"
	"norviq-api/internal/store/watchlistdb"

	"github.com/labstack/echo/v5"
)

// Settings are the environment variables the routes read.
type Settings struct {
	// ScenarioPlanningEnabled is SCENARIO_PLANNING_ENABLED (default off):
	// without it every holding risk profile route answers 404, as Vapor's
	// ScenarioController.authorize did. Vapor read it per request.
	ScenarioPlanningEnabled string `env:"SCENARIO_PLANNING_ENABLED"`
}

// Feature mounts the routes.
type Feature struct {
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (Feature) Name() string { return "watchlist" }

type handlers struct {
	d                *server.Deps
	q                *watchlistdb.Queries
	ent              *entitlements.Service
	badges           *badges.Service
	scenarioPlanning bool
	clock            func() time.Time
}

// now is Date(), at the microsecond precision Postgres keeps, so a response
// built from memory matches a later read.
func (h *handlers) now() time.Time {
	t := time.Now()
	if h.clock != nil {
		t = h.clock()
	}
	return t.UTC().Truncate(time.Microsecond)
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s, err := config.Load[Settings]()
	if err != nil {
		return err
	}
	h := &handlers{
		d:                d,
		q:                watchlistdb.New(d.DB),
		ent:              entitlements.NewFromEnv(d.DB),
		badges:           &badges.Service{Q: badgesdb.New(d.DB), Logger: d.Logger.Warn},
		scenarioPlanning: config.Bool(s.ScenarioPlanningEnabled, false),
		clock:            f.Now,
	}
	protected := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard()}
	with := func(scope auth.Scope) []echo.MiddlewareFunc {
		return append(append([]echo.MiddlewareFunc{}, protected...), auth.RequireScope(scope))
	}
	watchRead, watchWrite := with(auth.ScopeWatchlistRead), with(auth.ScopeWatchlistWrite)
	targetsRead, targetsWrite := with(auth.ScopeTargetsRead), with(auth.ScopeTargetsWrite)
	planRead, planWrite := with(auth.ScopePlanningRead), with(auth.ScopePlanningWrite)

	// StockController (watchlist, lists, CSV import).
	r.V1.GET("/watchlist", h.listWatchlist, watchRead...)
	r.V1.POST("/watchlist", h.createWatchlistItem, watchWrite...)
	r.V1.POST("/watchlist/import/csv/preview", h.importCSVPreview, watchWrite...)
	r.V1.POST("/watchlist/import/csv/commit", h.importCSVCommit, watchWrite...)
	r.V1.GET("/watchlist/lists", h.listWatchlistLists, watchRead...)
	r.V1.POST("/watchlist/lists", h.createWatchlistList, watchWrite...)
	r.V1.PATCH("/watchlist/lists/:watchlistListId", h.updateWatchlistList, watchWrite...)
	r.V1.DELETE("/watchlist/lists/:watchlistListId", h.deleteWatchlistList, watchWrite...)
	r.V1.PATCH("/watchlist/:watchlistId", h.updateWatchlistItem, watchWrite...)
	r.V1.DELETE("/watchlist/:watchlistId", h.deleteWatchlistItem, watchWrite...)

	// StockController (targets).
	r.V1.GET("/targets", h.listTargets, targetsRead...)
	r.V1.POST("/targets", h.createTarget, targetsWrite...)
	r.V1.PUT("/targets/:targetId", h.updateTarget, targetsWrite...)
	r.V1.DELETE("/targets/:targetId", h.deleteTarget, targetsWrite...)

	// WealthAutomationController (smart screens, watchlist scopes).
	r.V1.GET("/watchlist/screens/catalog", h.screenCatalog, watchRead...)
	r.V1.GET("/watchlist/screens", h.listScreens, watchRead...)
	r.V1.GET("/watchlist/screens/:screenID", h.getScreen, watchRead...)
	r.V1.GET("/watchlist/screens/:screenID/history", h.screenHistory, watchRead...)
	r.V1.POST("/watchlist/screens", h.createScreen, watchWrite...)
	r.V1.PUT("/watchlist/screens/:screenID", h.updateScreen, watchWrite...)
	r.V1.DELETE("/watchlist/screens/:screenID", h.deleteScreen, watchWrite...)
	r.V1.POST("/watchlist/screens/:screenID/evaluate", h.evaluateScreen, watchWrite...)

	// ScenarioController (holding risk profiles).
	r.V1.GET("/holding-risk-profiles", h.listRiskProfiles, planRead...)
	r.V1.POST("/holding-risk-profiles", h.saveRiskProfile, planWrite...)
	r.V1.DELETE("/holding-risk-profiles/:id", h.deleteRiskProfile, planWrite...)
	return nil
}
