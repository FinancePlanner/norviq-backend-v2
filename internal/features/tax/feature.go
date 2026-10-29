// Package tax ports Vapor's TaxController (/v1/tax/*), DefaultTaxService and
// the tax domain it calls: the rule registry, replacement and efficiency
// catalogs, the Portugal and Germany loss ledgers, the German fund advance
// lump sum, tax drag and asset-location analytics, and the report pipeline
// and its jobs.
package tax

import (
	"os"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/taxdb"

	"github.com/labstack/echo/v5"
)

// Settings are configureTaxOptimization's variables.
type Settings struct {
	// Validated is TAX_VALIDATED_JURISDICTIONS (comma list, uppercased;
	// unknown codes dropped).
	Validated map[Jurisdiction]bool
	// V2 is TAX_OPTIMIZATION_V2 (envBool, default true).
	V2 bool
}

// LoadSettings reads the tax optimization variables.
func LoadSettings() Settings {
	validated := map[Jurisdiction]bool{}
	for _, raw := range strings.Split(os.Getenv("TAX_VALIDATED_JURISDICTIONS"), ",") {
		j := Jurisdiction(strings.ToUpper(strings.TrimSpace(raw)))
		if j.valid() {
			validated[j] = true
		}
	}
	v2, ok := os.LookupEnv("TAX_OPTIMIZATION_V2")
	return Settings{Validated: validated, V2: !ok || config.Bool(v2, true)}
}

// Feature mounts the tax routes. Zero values read the environment; tests
// override them.
type Feature struct {
	Settings     *Settings
	Entitlements *entitlements.Config
	Now          func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "tax" }

func (f Feature) service(d *server.Deps) (*service, error) {
	settings := LoadSettings()
	if f.Settings != nil {
		settings = *f.Settings
	}
	cat, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	cfg := entitlements.LoadConfig()
	if f.Entitlements != nil {
		cfg = *f.Entitlements
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	ent := entitlements.New(d.DB, cfg).WithClock(now)
	return &service{
		db: d.DB, q: taxdb.New(d.DB), catalog: cat, rules: newRuleRegistry(settings.Validated),
		ent: ent, v2: settings.V2, now: now,
	}, nil
}

// Register implements server.Feature, mirroring TaxController.boot.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	svc, err := f.service(d)
	if err != nil {
		return err
	}
	h := &handlers{svc: svc, storage: reportStorageFromEnv()}

	// Per-route middleware: a group's Use registers catch-all routes.
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeTaxRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeTaxWrite)}
	tax := r.V1.Group("/tax")

	tax.GET("/capabilities", h.capabilities, read...)
	tax.GET("/dashboard", h.dashboard, read...)
	tax.GET("/loss-carryforwards", h.lossCarryforwards, read...)
	tax.GET("/profile", h.getProfile, read...)
	tax.GET("/profile/context", h.getProfileContext, read...)
	tax.GET("/funds/annual-inputs", h.getFundAnnualInput, read...)
	tax.GET("/scenarios/:scenarioId", h.getScenario, read...)
	tax.GET("/action-plans", h.listActionPlans, read...)
	tax.GET("/action-plans/:actionPlanId", h.getActionPlan, read...)
	tax.GET("/notifications", h.getNotificationPreferences, read...)
	h.registerReportReads(tax, read)

	tax.PUT("/profile", h.saveProfile, write...)
	tax.PUT("/instruments/:instrumentId/market-admission", h.saveMarketAdmission, write...)
	tax.PUT("/instruments/:instrumentId/fund-classification", h.saveFundClassification, write...)
	tax.PUT("/funds/annual-inputs", h.saveFundAnnualInput, write...)
	tax.POST("/scenarios", h.createScenario, write...)
	tax.POST("/action-plans", h.createActionPlan, write...)
	tax.PATCH("/action-plans/:actionPlanId", h.transitionActionPlan, write...)
	tax.POST("/location-scenarios", h.createLocationScenario, write...)
	tax.POST("/placement-plans", h.createPlacementPlan, write...)
	tax.POST("/opportunities/:opportunityId/dismiss", h.dismissOpportunity, write...)
	tax.DELETE("/opportunities/:opportunityId/dismiss", h.restoreOpportunity, write...)
	tax.PUT("/notifications", h.saveNotificationPreferences, write...)
	h.registerReportWrites(tax, write)
	return nil
}
