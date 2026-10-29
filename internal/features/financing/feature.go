// Package financing ports FinancingController (/v1/financing/*): offer
// simulations, affordability assumptions, financing plans with revisions and
// expense matching, and offer imports from a public URL or an uploaded file.
package financing

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/financingdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// PremiumGate is `usageCounterService.requirePremium(_:userId:on:)`: the
// billing upgrade error for a free user.
type PremiumGate interface {
	RequirePremium(ctx context.Context, feature string, userID uuid.UUID) error
}

// featureScenarioPlanning is BillingFeature.scenarioPlanning, the gate on
// every paid financing route.
const featureScenarioPlanning = string(entitlements.FeatureScenarioPlanning)

// maxImportBytes is FinancingController.maxImportBytes and the 8mb body
// limit of POST /imports/file.
const maxImportBytes = 8 * 1024 * 1024

// Feature mounts /v1/financing/*.
type Feature struct {
	// Premium builds the billing gate (default: entitlements from env).
	Premium func(d *server.Deps) PremiumGate
	// HTTPClient fetches offer pages for POST /imports/url (tests).
	HTTPClient *http.Client
	// Now is the clock (tests).
	Now func() time.Time
}

func (Feature) Name() string { return "financing" }

type handlers struct {
	svc     *service
	premium PremiumGate
	client  *http.Client
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	now := f.Now
	if now == nil {
		now = time.Now
	}
	h := &handlers{
		svc: &service{db: d.DB, q: financingdb.New(d.DB), calc: calculator{now: now}, now: now},
	}
	if f.Premium != nil {
		h.premium = f.Premium(d)
	} else {
		h.premium = entitlements.NamedGate{Service: entitlements.NewFromEnv(d.DB)}
	}
	h.client = f.HTTPClient
	if h.client == nil {
		h.client = importClient()
	}

	// routes.swift registers the controller inside
	// RateLimitMiddleware(limit: 30, interval: 60, keyPrefix: "ratelimit:financing"),
	// so the limiter runs before the authenticator.
	limit := d.Limiter.Limit(30, 60*time.Second, "ratelimit:financing")
	read := []echo.MiddlewareFunc{limit, d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningRead)}
	write := []echo.MiddlewareFunc{limit, d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningWrite)}

	g := r.V1
	g.GET("/financing/assumptions", h.getAssumptions, read...)
	g.GET("/financing/projections", h.projections, read...)
	g.GET("/financing/match-candidates/:expenseId", h.matchCandidates, read...)
	g.GET("/financing/plans", h.plans, read...)
	g.GET("/financing/plans/:planId/schedule", h.schedule, read...)

	g.POST("/financing/simulations", h.simulate, write...)
	g.PUT("/financing/assumptions", h.putAssumptions, write...)
	g.POST("/financing/plans", h.createPlan, write...)
	g.POST("/financing/imports/url", h.importURL, write...)
	g.POST("/financing/imports/file", h.importFile, write...)
	g.PATCH("/financing/plans/:planId", h.updateStatus, write...)
	g.POST("/financing/plans/:planId/revisions", h.revise, write...)
	g.POST("/financing/plans/:planId/installments/:installment/match", h.match, write...)
	g.DELETE("/financing/plans/:planId/installments/:installment/match", h.unmatch, write...)
	return nil
}

func (h *handlers) requirePro(c *echo.Context, userID uuid.UUID) error {
	return h.premium.RequirePremium(c.Request().Context(), featureScenarioPlanning, userID)
}

// parseUUID is Foundation's UUID(uuidString:): exactly 8-4-4-4-12 hex.
func parseUUID(raw string) (uuid.UUID, bool) {
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
}

func planID(c *echo.Context) (uuid.UUID, error) {
	id, ok := parseUUID(c.Param("planId"))
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("")
	}
	return id, nil
}

func installment(c *echo.Context) (int, error) {
	v, err := strconv.Atoi(c.Param("installment"))
	if err != nil || v <= 0 {
		return 0, httpx.BadRequest("")
	}
	return v, nil
}

func (h *handlers) simulate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var req SimulationRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if len(req.Offers) > 1 {
		if err := h.requirePro(c, s.UserID); err != nil {
			return err
		}
	}
	res, err := h.svc.simulation(c.Request().Context(), s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

func (h *handlers) getAssumptions(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	a, err := h.svc.assumptions(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, a)
}

func (h *handlers) putAssumptions(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var req Assumptions
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	a, err := h.svc.updateAssumptions(c.Request().Context(), s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, a)
}

func (h *handlers) plans(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.svc.plans(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) createPlan(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	var req PlanRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	created, err := h.svc.createPlan(c.Request().Context(), s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.Created(c, created)
}

func (h *handlers) updateStatus(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	id, err := planID(c)
	if err != nil {
		return err
	}
	var req StatusRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.updateStatus(c.Request().Context(), s.UserID, id, req.Status)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) revise(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	id, err := planID(c)
	if err != nil {
		return err
	}
	var req RevisionRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.revise(c.Request().Context(), s.UserID, id, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) schedule(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := planID(c)
	if err != nil {
		return err
	}
	out, err := h.svc.schedule(c.Request().Context(), s.UserID, id)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) projections(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	// An unparseable from/to is ignored, not a 400.
	var from, to *time.Time
	if raw, ok := queryValue(c, "from"); ok {
		if d, ok := parseDay(raw); ok {
			from = &d
		}
	}
	if raw, ok := queryValue(c, "to"); ok {
		if d, ok := parseDay(raw); ok {
			to = &d
		}
	}
	out, err := h.svc.projections(c.Request().Context(), s.UserID, from, to)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func queryValue(c *echo.Context, name string) (string, bool) {
	vs, ok := c.Request().URL.Query()[name]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

func (h *handlers) match(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	n, err := installment(c)
	if err != nil {
		return err
	}
	var req MatchRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	expenseID, ok := parseUUID(req.ExpenseID)
	if !ok {
		return httpx.BadRequest("")
	}
	id, err := planID(c)
	if err != nil {
		return err
	}
	out, err := h.svc.match(c.Request().Context(), s.UserID, id, n, expenseID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) matchCandidates(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	expenseID, ok := parseUUID(c.Param("expenseId"))
	if !ok {
		return httpx.BadRequest("")
	}
	out, err := h.svc.matchCandidates(c.Request().Context(), s.UserID, expenseID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) unmatch(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := planID(c)
	if err != nil {
		return err
	}
	n, err := installment(c)
	if err != nil {
		return err
	}
	if err := h.svc.unmatch(c.Request().Context(), s.UserID, id, n); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (r *MatchRequest) validate() error     { return nil }
func (r *URLImportRequest) validate() error { return nil }
