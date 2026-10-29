// Package retirement ports RetirementController
// (Retirement/RetirementController.swift), RetirementProjectionEngine and
// RetirementRuleRegistry.
package retirement

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/retirementdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/retirement/rules/:jurisdiction and
// /v1/portfolios/:portfolioId/retirement*.
type Feature struct {
	// Now overrides the clock (tests).
	Now func() time.Time
	// ScheduleAchievementCheck is req.scheduleAchievementCheck(for:), run
	// for the portfolio owner after a plan is saved. nil is a no-op (wire
	// achievementhooks.Schedule).
	ScheduleAchievementCheck func(userID uuid.UUID)
}

func (Feature) Name() string { return "retirement" }

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{d: d, access: portfolios.NewAccess(d.DB), now: f.Now, achievements: f.ScheduleAchievementCheck}
	if h.now == nil {
		h.now = portfolios.Now
	}
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningWrite)}
	r.V1.GET("/retirement/rules/:jurisdiction", h.rules, read...)
	r.V1.GET("/portfolios/:portfolioId/retirement", h.getPlan, read...)
	r.V1.PUT("/portfolios/:portfolioId/retirement", h.upsertPlan, write...)
	r.V1.POST("/portfolios/:portfolioId/retirement/refresh-rules", h.refreshRules, write...)
	r.V1.POST("/portfolios/:portfolioId/retirement/projection", h.projection, write...)
	return nil
}

// rules is GET /v1/retirement/rules/:jurisdiction.
func (h *handler) rules(c *echo.Context) error {
	j, ok := ParseJurisdiction(strings.ToUpper(c.Param("jurisdiction")))
	if !ok {
		return httpx.BadRequest("Unsupported tax jurisdiction.")
	}
	pack := RulePackFor(j, httpx.QueryString(c, "version"))
	if pack == nil {
		return httpx.NotFound("Retirement rule version not found.")
	}
	return httpx.OK(c, pack)
}

type handler struct {
	d            *server.Deps
	access       portfolios.Access
	now          func() time.Time
	achievements func(uuid.UUID)
}

// Plan is RetirementPlan.
type Plan struct {
	ID               string    `json:"id"`
	PortfolioID      string    `json:"portfolioId"`
	RuleVersion      string    `json:"ruleVersion"`
	Input            PlanInput `json:"input"`
	NewerRuleVersion *string   `json:"newerRuleVersion,omitempty"`
	CreatedAt        string    `json:"createdAt"`
	UpdatedAt        *string   `json:"updatedAt,omitempty"`
}

// planUpsertRequest is RetirementPlanUpsertRequest.
type planUpsertRequest struct {
	Input       PlanInput `json:"input"`
	RuleVersion *string   `json:"ruleVersion,omitempty"`
}

func (h *handler) portfolioID(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("portfolioId"))
	if !ok {
		return uuid.Nil, httpx.BadRequest("Invalid portfolioId.")
	}
	return id, nil
}

// requirePlan is requirePlan(_:editing:).
func (h *handler) requirePlan(c *echo.Context, editing bool) (portfolios.AccessContext, retirementdb.RetirementPlan, error) {
	s, err := auth.Require(c)
	if err != nil {
		return portfolios.AccessContext{}, retirementdb.RetirementPlan{}, err
	}
	id, err := h.portfolioID(c)
	if err != nil {
		return portfolios.AccessContext{}, retirementdb.RetirementPlan{}, err
	}
	ctx := c.Request().Context()
	access, err := h.access.Require(ctx, h.d.DB, id, s.UserID, portfolios.RequireOptions{Editing: editing})
	if err != nil {
		return portfolios.AccessContext{}, retirementdb.RetirementPlan{}, err
	}
	rec, err := retirementdb.New(h.d.DB).GetRetirementPlanByPortfolio(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return portfolios.AccessContext{}, retirementdb.RetirementPlan{}, httpx.NotFound("Retirement plan not found.")
	}
	if err != nil {
		return portfolios.AccessContext{}, retirementdb.RetirementPlan{}, err
	}
	return access, rec, nil
}

func (h *handler) getPlan(c *echo.Context) error {
	access, rec, err := h.requirePlan(c, false)
	if err != nil {
		return err
	}
	plan, err := makePlan(rec, access.Portfolio.ID)
	if err != nil {
		return err
	}
	return httpx.OK(c, plan)
}

func (h *handler) upsertPlan(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := h.portfolioID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	access, err := h.access.Require(ctx, h.d.DB, id, s.UserID, portfolios.RequireOptions{Editing: true})
	if err != nil {
		return err
	}
	if access.Portfolio.Purpose != portfolios.PurposeRetirement {
		return httpx.BadRequest("Retirement planning requires a retirement portfolio.")
	}
	var payload planUpsertRequest
	if err := portfolios.DecodeSwiftBody(c, &payload); err != nil {
		return err
	}
	if RulePackFor(payload.Input.Jurisdiction, payload.RuleVersion) == nil {
		return httpx.BadRequest("Unsupported retirement rule version.")
	}
	inputJSON, err := encodeInput(payload.Input)
	if err != nil {
		return err
	}
	ruleVersion := CurrentRuleVersion
	if payload.RuleVersion != nil {
		ruleVersion = *payload.RuleVersion
	}
	q := retirementdb.New(h.d.DB)
	now := h.now()
	rec, err := q.GetRetirementPlanByPortfolio(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// A new record: @Timestamp(.create) and (.update) both stamp now.
		rec = retirementdb.RetirementPlan{
			ID: uuid.New(), PortfolioID: id, RuleVersion: ruleVersion, InputJson: inputJSON,
			CreatedAt: &now, UpdatedAt: &now,
		}
		if err := q.InsertRetirementPlan(ctx, retirementdb.InsertRetirementPlanParams{
			ID: rec.ID, PortfolioID: id, RuleVersion: ruleVersion, InputJson: inputJSON, CreatedAt: &now, UpdatedAt: &now,
		}); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		// Assigning a Fluent field always marks it changed, so save()
		// updates (and bumps updated_at) even when the values are equal.
		rec.RuleVersion, rec.InputJson, rec.UpdatedAt = ruleVersion, inputJSON, &now
		if err := q.UpdateRetirementPlan(ctx, retirementdb.UpdateRetirementPlanParams{
			ID: rec.ID, RuleVersion: ruleVersion, InputJson: inputJSON, UpdatedAt: &now,
		}); err != nil {
			return err
		}
	}
	// The metric counts plans by portfolio owner, who may not be the editor.
	if h.achievements != nil {
		h.achievements(access.Portfolio.UserID)
	}
	plan, err := makePlan(rec, id)
	if err != nil {
		return err
	}
	return httpx.OK(c, plan)
}

func (h *handler) refreshRules(c *echo.Context) error {
	access, rec, err := h.requirePlan(c, true)
	if err != nil {
		return err
	}
	now := h.now()
	rec.RuleVersion, rec.UpdatedAt = CurrentRuleVersion, &now
	if err := retirementdb.New(h.d.DB).UpdateRetirementPlanRuleVersion(c.Request().Context(), retirementdb.UpdateRetirementPlanRuleVersionParams{
		ID: rec.ID, RuleVersion: CurrentRuleVersion, UpdatedAt: &now,
	}); err != nil {
		return err
	}
	plan, err := makePlan(rec, access.Portfolio.ID)
	if err != nil {
		return err
	}
	return httpx.OK(c, plan)
}

func (h *handler) projection(c *echo.Context) error {
	access, rec, err := h.requirePlan(c, false)
	if err != nil {
		return err
	}
	var request ProjectionRequest
	if err := portfolios.DecodeSwiftBody(c, &request); err != nil {
		return err
	}
	input, err := decodeInput(rec.InputJson)
	if err != nil {
		return err
	}
	if request.RuleVersion == nil {
		v := rec.RuleVersion
		request.RuleVersion = &v
	}
	pid := access.Portfolio.ID
	out, err := Engine{Now: h.now}.Project(&pid, input, request)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func makePlan(rec retirementdb.RetirementPlan, portfolioID uuid.UUID) (Plan, error) {
	input, err := decodeInput(rec.InputJson)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		ID:          apijson.FormatUUID(rec.ID),
		PortfolioID: apijson.FormatUUID(portfolioID),
		RuleVersion: rec.RuleVersion,
		Input:       input,
	}
	if rec.RuleVersion != CurrentRuleVersion {
		v := CurrentRuleVersion
		plan.NewerRuleVersion = &v
	}
	if rec.CreatedAt != nil {
		plan.CreatedAt = formatISO(*rec.CreatedAt)
	}
	if rec.UpdatedAt != nil {
		s := formatISO(*rec.UpdatedAt)
		plan.UpdatedAt = &s
	}
	return plan, nil
}

// encodeInput is JSONEncoder.backendAPI.encode(input): declaration-order
// keys, nil optionals omitted.
func encodeInput(input PlanInput) (string, error) {
	if input.Accounts == nil {
		input.Accounts = []AccountPlan{}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// decodeInput is JSONDecoder.backendAPI.decode(RetirementPlanInput.self):
// a stored document that no longer decodes is a 400 DecodingError.
func decodeInput(raw string) (PlanInput, error) {
	var input PlanInput
	if err := portfolios.DecodeSwift([]byte(raw), &input, portfolios.KeysBackendAPI); err != nil {
		return PlanInput{}, err
	}
	if input.Accounts == nil {
		input.Accounts = []AccountPlan{}
	}
	return input, nil
}

// formatISO is formatISODateTime: ISO 8601 with milliseconds, which
// ISO8601DateFormatter rounds (.669583 -> .670) rather than truncates.
func formatISO(t time.Time) string {
	return portfolios.FormatISODateTime(t.Round(time.Millisecond))
}

// DecodeInput decodes a stored retirement_plans.input_json document the way
// JSONDecoder.backendAPI decodes RetirementPlanInput.
func DecodeInput(raw string) (PlanInput, error) { return decodeInput(raw) }
