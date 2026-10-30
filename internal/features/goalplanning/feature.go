// Package goalplanning ports GoalPlanningController (/v1/financial-goals),
// GoalPlanningService and GoalPlanningDailyJob (GoalPlanning/*.swift), plus
// the closed-form goal maths of StockPlanShared's PlanningEngine.
package goalplanning

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/onboarding"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/server"
	"norviq-api/internal/store/goalplanningdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Feature mounts GoalPlanningController. Zero values build everything from
// the environment; tests override them.
type Feature struct {
	Quotes       Quoter
	Entitlements Resolver
	Now          func() time.Time
	// ScheduleAchievementCheck is req.scheduleAchievementCheck(for:); nil
	// is a no-op.
	ScheduleAchievementCheck func(userID uuid.UUID)
}

// Name implements server.Feature.
func (Feature) Name() string { return "goalplanning" }

// RoutesEnabled is routes.swift's goalPlanningRoutesEnabled():
// GOAL_PLANNING_ENABLED unset, or anything but 0/false/no/off (lowercased,
// not trimmed).
func RoutesEnabled() bool {
	v, ok := os.LookupEnv("GOAL_PLANNING_ENABLED")
	if !ok {
		return true
	}
	return !slices.Contains([]string{"0", "false", "no", "off"}, strings.ToLower(v))
}

func (f Feature) service(d *server.Deps) (*service, error) {
	quotes := f.Quotes
	if quotes == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return nil, err
		}
		quotes = svc
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	return &service{db: d.DB, q: goalplanningdb.New(d.DB), quotes: quotes, now: now}, nil
}

// Register implements server.Feature, mirroring GoalPlanningController.boot:
// ScopedBearerAuthenticator + SessionToken guard, then goals:read or
// goals:write. Unregistered when GOAL_PLANNING_ENABLED is off (Vapor does
// not register it either).
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	if !RoutesEnabled() {
		return nil
	}
	svc, err := f.service(d)
	if err != nil {
		return err
	}
	ent := f.Entitlements
	if ent == nil {
		ent = entitlements.NewFromEnv(d.DB)
	}
	h := &handler{d: d, svc: svc, ent: ent, achievements: f.ScheduleAchievementCheck}
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeGoalsRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeGoalsWrite)}

	g := r.V1
	g.GET("/financial-goals", h.list, read...)
	g.GET("/financial-goals/templates", h.templates, read...)
	g.GET("/financial-goals/overview", h.overview, read...)
	g.GET("/financial-goals/:id", h.get, read...)
	g.GET("/financial-goals/:id/progress", h.progress, read...)
	g.GET("/financial-goals/:id/contributions", h.listContributions, read...)
	g.GET("/financial-goals/:id/suggestions", h.suggestions, read...)
	g.GET("/financial-goals/:id/adjustment-drafts", h.adjustmentDrafts, read...)

	g.POST("/financial-goals", h.create, write...)
	g.PATCH("/financial-goals/bulk", h.bulkUpdate, write...)
	g.PUT("/financial-goals/:id", h.update, write...)
	g.DELETE("/financial-goals/:id", h.delete, write...)
	g.POST("/financial-goals/:id/what-if", h.whatIf, write...)
	g.POST("/financial-goals/:id/contributions", h.createContribution, write...)
	g.DELETE("/financial-goals/:id/contributions/:contributionId", h.deleteContribution, write...)
	g.POST("/financial-goals/:id/suggestions/:suggestionId/accept", h.acceptSuggestion, write...)
	g.POST("/financial-goals/:id/suggestions/:suggestionId/dismiss", h.dismissSuggestion, write...)
	return nil
}

type handler struct {
	d            *server.Deps
	svc          *service
	ent          Resolver
	achievements func(uuid.UUID)
}

func (h *handler) templates(c *echo.Context) error {
	return httpx.OK(c, templates)
}

func (h *handler) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	models, err := h.svc.q.ListFinancialGoalsByUser(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := make([]FinancialGoal, 0, len(models))
	for i := range models {
		dto, err := h.svc.goalDTO(ctx, h.svc.q, &models[i])
		if err != nil {
			return err
		}
		out = append(out, dto)
	}
	return httpx.OK(c, out)
}

func (h *handler) overview(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	snap, err := h.ent.Resolve(ctx, s.UserID)
	if err != nil {
		return err
	}
	models, err := h.svc.q.ListActiveFinancialGoalsByTargetDate(ctx, s.UserID)
	if err != nil {
		return err
	}
	items := make([]OverviewItem, 0, len(models))
	for i := range models {
		goal, err := h.svc.goalDTO(ctx, h.svc.q, &models[i])
		if err != nil {
			return err
		}
		progress, err := h.svc.progress(ctx, &models[i], s.UserID, overrides{})
		if err != nil {
			return err
		}
		items = append(items, OverviewItem{ID: goal.ID, Goal: goal, Progress: progress})
	}
	out := Overview{Items: items, ActiveGoalCount: len(items), IsPro: snap.IsPro()}
	for i := range items {
		out.TotalCurrentValue += items[i].Progress.CurrentValue
	}
	for i := range items {
		out.TotalTargetAmount += items[i].Goal.TargetAmount
	}
	if !snap.IsPro() {
		out.ActiveGoalLimit = intPtr(1)
	}
	return httpx.OK(c, out)
}

// primaryPortfolio is GoalPlanningController.primaryPortfolio.
func (h *handler) primaryPortfolio(ctx context.Context, allocations []allocation, userID uuid.UUID) (uuid.UUID, error) {
	if len(allocations) > 0 {
		best := allocations[0]
		for _, a := range allocations[1:] {
			if best.percentage < a.percentage {
				best = a
			}
		}
		return best.portfolioID, nil
	}
	id, err := h.svc.q.FallbackPortfolioID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, httpx.Unprocessable("Create a portfolio before creating a financial goal")
	}
	return id, err
}

// enforceActiveLimit is GoalPlanningController.enforceActiveLimit.
func (h *handler) enforceActiveLimit(ctx context.Context, userID uuid.UUID, activating bool, excluding *uuid.UUID) error {
	if !activating {
		return nil
	}
	snap, err := h.ent.Resolve(ctx, userID)
	if err != nil {
		return err
	}
	if snap.IsPro() {
		return nil
	}
	count, err := h.svc.q.CountActiveFinancialGoals(ctx, goalplanningdb.CountActiveFinancialGoalsParams{UserID: userID, Excluding: excluding})
	if err != nil {
		return err
	}
	if count >= 1 {
		return entitlements.UpgradeLimit(entitlements.FeatureGoalPlanning, snap.Level, 1, int(count))
	}
	return nil
}

// prepare runs the shared validation of create and update, in Vapor's order.
func (h *handler) prepare(ctx context.Context, in *goalInput, userID uuid.UUID, activating bool, excluding *uuid.UUID) ([]allocation, uuid.UUID, time.Time, error) {
	if err := h.enforceActiveLimit(ctx, userID, activating, excluding); err != nil {
		return nil, uuid.Nil, time.Time{}, err
	}
	allocations, err := h.svc.validateLinks(ctx, in, userID, excluding)
	if err != nil {
		return nil, uuid.Nil, time.Time{}, err
	}
	primary, err := h.primaryPortfolio(ctx, allocations, userID)
	if err != nil {
		return nil, uuid.Nil, time.Time{}, err
	}
	if len(allocations) == 0 {
		if err := h.svc.validateAllocationCapacity(ctx, primary, 100, string(in.Status), userID, excluding); err != nil {
			return nil, uuid.Nil, time.Time{}, err
		}
	}
	targetDate, ok := parseDate(in.TargetDate)
	if !ok || !targetDate.After(h.svc.now()) {
		return nil, uuid.Nil, time.Time{}, httpx.BadRequest("Target date must be in the future")
	}
	if len(allocations) == 0 {
		allocations = []allocation{{primary, 100}}
	}
	return allocations, primary, targetDate, nil
}

func (h *handler) create(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var in goalInput
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	ctx := c.Request().Context()
	allocations, primary, targetDate, err := h.prepare(ctx, &in, s.UserID, in.Status == statusActive, nil)
	if err != nil {
		return err
	}
	now := h.svc.now()
	goal := goalplanningdb.FinancialGoal{
		ID: uuid.New(), UserID: s.UserID, PortfolioListID: primary, Name: strings.TrimSpace(in.Name),
		GoalType: string(in.GoalType), TargetAmount: in.TargetAmount, TargetDate: targetDate,
		BaseCurrency: strings.ToUpper(in.BaseCurrency), StartingCapital: in.StartingCapital,
		MonthlyContribution: in.MonthlyContribution, AnnualContributionGrowth: in.AnnualContributionGrowth,
		InflationAssumption: in.InflationAssumption, RiskProfile: string(in.RiskProfile),
		ExpectedAnnualReturn: in.configuredReturn(), Status: string(in.Status), CreatedAt: &now, UpdatedAt: &now,
	}
	err = h.svc.inTx(ctx, func(q *goalplanningdb.Queries) error {
		if err := q.InsertFinancialGoal(ctx, goalplanningdb.InsertFinancialGoalParams{
			ID: goal.ID, UserID: goal.UserID, PortfolioListID: goal.PortfolioListID, Name: goal.Name,
			GoalType: goal.GoalType, TargetAmount: goal.TargetAmount, TargetDate: goal.TargetDate,
			BaseCurrency: goal.BaseCurrency, StartingCapital: goal.StartingCapital,
			MonthlyContribution: goal.MonthlyContribution, AnnualContributionGrowth: goal.AnnualContributionGrowth,
			InflationAssumption: goal.InflationAssumption, RiskProfile: goal.RiskProfile,
			ExpectedAnnualReturn: goal.ExpectedAnnualReturn, Status: goal.Status, CreatedAt: &now, UpdatedAt: &now,
		}); err != nil {
			return err
		}
		return replaceLinks(ctx, q, goal.ID, s.UserID, &in, allocations, now)
	})
	if err != nil {
		return err
	}
	onboarding.StampBestEffort(ctx, h.d.DB, h.d.Logger, onboarding.LatchGoal, s.UserID)
	dto, err := h.svc.goalDTO(ctx, h.svc.q, &goal)
	if err != nil {
		return err
	}
	return httpx.OK(c, dto)
}

// ownedGoal is GoalPlanningController.ownedGoal.
func (h *handler) ownedGoal(c *echo.Context) (uuid.UUID, *goalplanningdb.FinancialGoal, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, nil, err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return uuid.Nil, nil, ownedGoalNotFound()
	}
	goal, err := h.svc.q.GetOwnedFinancialGoal(c.Request().Context(), goalplanningdb.GetOwnedFinancialGoalParams{UserID: s.UserID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, ownedGoalNotFound()
	}
	if err != nil {
		return uuid.Nil, nil, err
	}
	return s.UserID, &goal, nil
}

func (h *handler) get(c *echo.Context) error {
	_, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	dto, err := h.svc.goalDTO(c.Request().Context(), h.svc.q, goal)
	if err != nil {
		return err
	}
	return httpx.OK(c, dto)
}

func (h *handler) update(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	var in goalInput
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	ctx := c.Request().Context()
	activating := in.Status == statusActive && goal.Status != statusActive
	allocations, primary, targetDate, err := h.prepare(ctx, &in, userID, activating, &goal.ID)
	if err != nil {
		return err
	}
	now := h.svc.now()
	goal.PortfolioListID = primary
	goal.Name = strings.TrimSpace(in.Name)
	goal.GoalType = string(in.GoalType)
	goal.TargetAmount = in.TargetAmount
	goal.TargetDate = targetDate
	goal.BaseCurrency = strings.ToUpper(in.BaseCurrency)
	goal.StartingCapital = in.StartingCapital
	goal.MonthlyContribution = in.MonthlyContribution
	goal.AnnualContributionGrowth = in.AnnualContributionGrowth
	goal.InflationAssumption = in.InflationAssumption
	goal.RiskProfile = string(in.RiskProfile)
	goal.ExpectedAnnualReturn = in.configuredReturn()
	goal.Status = string(in.Status)
	goal.UpdatedAt = &now
	err = h.svc.inTx(ctx, func(q *goalplanningdb.Queries) error {
		if err := q.UpdateFinancialGoal(ctx, goalplanningdb.UpdateFinancialGoalParams{
			ID: goal.ID, PortfolioListID: goal.PortfolioListID, Name: goal.Name, GoalType: goal.GoalType,
			TargetAmount: goal.TargetAmount, TargetDate: goal.TargetDate, BaseCurrency: goal.BaseCurrency,
			StartingCapital: goal.StartingCapital, MonthlyContribution: goal.MonthlyContribution,
			AnnualContributionGrowth: goal.AnnualContributionGrowth, InflationAssumption: goal.InflationAssumption,
			RiskProfile: goal.RiskProfile, ExpectedAnnualReturn: goal.ExpectedAnnualReturn, Status: goal.Status,
			UpdatedAt: &now,
		}); err != nil {
			return err
		}
		return replaceLinks(ctx, q, goal.ID, userID, &in, allocations, now)
	})
	if err != nil {
		return err
	}
	dto, err := h.svc.goalDTO(ctx, h.svc.q, goal)
	if err != nil {
		return err
	}
	return httpx.OK(c, dto)
}

func (h *handler) delete(c *echo.Context) error {
	_, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	if err := h.svc.q.DeleteFinancialGoal(c.Request().Context(), goal.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handler) progress(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	p, err := h.svc.progress(c.Request().Context(), goal, userID, overrides{})
	if err != nil {
		return err
	}
	return httpx.OK(c, p)
}

func (h *handler) whatIf(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	var in whatIfInput
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	if (in.MonthlyContribution != nil && !(finite(*in.MonthlyContribution) && *in.MonthlyContribution >= 0)) ||
		(in.ExpectedAnnualReturn != nil && !(finite(*in.ExpectedAnnualReturn) && math.Abs(*in.ExpectedAnnualReturn-goal.ExpectedAnnualReturn) <= 0.010_001)) {
		return httpx.BadRequest("Invalid what-if assumptions")
	}
	var targetDate *time.Time
	if in.TargetDate != nil {
		parsed, ok := parseDate(*in.TargetDate)
		if !ok || !parsed.After(h.svc.now()) {
			return httpx.BadRequest("What-if target date must be in the future")
		}
		targetDate = &parsed
	}
	ctx := c.Request().Context()
	baseline, err := h.svc.progress(ctx, goal, userID, overrides{})
	if err != nil {
		return err
	}
	scenario, err := h.svc.progress(ctx, goal, userID, overrides{
		monthlyContribution: in.MonthlyContribution, expectedAnnualReturn: in.ExpectedAnnualReturn, targetDate: targetDate,
	})
	if err != nil {
		return err
	}
	return httpx.OK(c, WhatIfResponse{Baseline: baseline, Scenario: scenario})
}

func (h *handler) listContributions(c *echo.Context) error {
	_, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	rows, err := h.svc.q.ListContributionsByGoal(c.Request().Context(), goal.ID)
	if err != nil {
		return err
	}
	out := make([]Contribution, 0, len(rows))
	for i := range rows {
		out = append(out, h.svc.contributionDTO(&rows[i]))
	}
	return httpx.OK(c, out)
}

func (h *handler) createContribution(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	var in contributionInput
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	occurredAt, ok := parseDate(in.OccurredAt)
	if !(in.Amount > 0) || !finite(in.Amount) || !ok || occurredAt.After(h.svc.now()) {
		return httpx.BadRequest("Contribution must be positive and cannot be in the future")
	}
	var note *string
	if in.Note != nil {
		n := strings.TrimSpace(*in.Note)
		note = &n
	}
	now := h.svc.now()
	row := goalplanningdb.FinancialGoalContribution{
		ID: uuid.New(), GoalID: goal.ID, UserID: userID, Amount: in.Amount, OccurredAt: occurredAt, Note: note, CreatedAt: &now,
	}
	if err := h.svc.q.InsertContribution(c.Request().Context(), goalplanningdb.InsertContributionParams{
		ID: row.ID, GoalID: row.GoalID, UserID: row.UserID, Amount: row.Amount, OccurredAt: row.OccurredAt, Note: row.Note, CreatedAt: row.CreatedAt,
	}); err != nil {
		return err
	}
	if h.achievements != nil {
		h.achievements(userID)
	}
	return httpx.OK(c, h.svc.contributionDTO(&row))
}

func (h *handler) deleteContribution(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("contributionId"))
	if !ok {
		return httpx.NotFound("Contribution not found")
	}
	n, err := h.svc.q.DeleteOwnedContribution(c.Request().Context(), goalplanningdb.DeleteOwnedContributionParams{ID: id, GoalID: goal.ID, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return httpx.NotFound("Contribution not found")
	}
	return httpx.NoContent(c)
}

func (h *handler) suggestions(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	out, err := h.svc.suggestions(c.Request().Context(), goal, userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// ownedSuggestion is GoalPlanningController.ownedSuggestion.
func (h *handler) ownedSuggestion(c *echo.Context, goal *goalplanningdb.FinancialGoal, userID uuid.UUID) (*goalplanningdb.FinancialGoalSuggestion, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("suggestionId"))
	if !ok {
		return nil, httpx.NotFound("Suggestion not found")
	}
	row, err := h.svc.q.GetOwnedSuggestion(c.Request().Context(), goalplanningdb.GetOwnedSuggestionParams{ID: id, GoalID: goal.ID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("Suggestion not found")
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (h *handler) acceptSuggestion(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	suggestion, err := h.ownedSuggestion(c, goal, userID)
	if err != nil {
		return err
	}
	// Only a proposed suggestion can be accepted. Vapor accepted accepted
	// and dismissed ones again, creating another draft each time.
	if suggestion.Status != suggestionProposed {
		return httpx.Conflict("Suggestion is no longer open")
	}
	ctx := c.Request().Context()
	destination := destinationGoal
	switch suggestion.Kind {
	case kindReduceSpending:
		destination = destinationBudget
	case kindRebalanceAllocation:
		destination = destinationRebalancing
	}
	if destination != destinationGoal {
		snap, err := h.ent.Resolve(ctx, userID)
		if err != nil {
			return err
		}
		if !snap.IsPro() {
			return entitlements.UpgradeRequired(entitlements.FeatureGoalPlanning, snap.Level)
		}
	}
	if err := h.svc.q.UpdateSuggestionStatus(ctx, goalplanningdb.UpdateSuggestionStatusParams{ID: suggestion.ID, Status: suggestionAccepted}); err != nil {
		return err
	}
	values := map[string]any{"kind": suggestion.Kind}
	if suggestion.MonthlyAmount != nil {
		values["monthly_amount"] = *suggestion.MonthlyAmount
	}
	if suggestion.AllocationPercentage != nil {
		values["allocation_percentage"] = *suggestion.AllocationPercentage
	}
	payload, err := json.Marshal(draftPayload{Values: values})
	if err != nil {
		return err
	}
	now := h.svc.now()
	draft := goalplanningdb.FinancialGoalAdjustmentDraft{
		ID: uuid.New(), SuggestionID: suggestion.ID, UserID: userID, Destination: destination, Payload: payload, CreatedAt: &now,
	}
	if err := h.svc.q.InsertAdjustmentDraft(ctx, goalplanningdb.InsertAdjustmentDraftParams{
		ID: draft.ID, SuggestionID: draft.SuggestionID, UserID: draft.UserID, Destination: draft.Destination,
		Payload: draft.Payload, CreatedAt: draft.CreatedAt,
	}); err != nil {
		return err
	}
	return httpx.OK(c, h.svc.draftDTO(&draft))
}

func (h *handler) dismissSuggestion(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	suggestion, err := h.ownedSuggestion(c, goal, userID)
	if err != nil {
		return err
	}
	if err := h.svc.q.UpdateSuggestionStatus(c.Request().Context(), goalplanningdb.UpdateSuggestionStatusParams{ID: suggestion.ID, Status: suggestionDismissed}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handler) adjustmentDrafts(c *echo.Context) error {
	userID, goal, err := h.ownedGoal(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	ids, err := h.svc.q.ListSuggestionIDsForGoal(ctx, goalplanningdb.ListSuggestionIDsForGoalParams{GoalID: goal.ID, UserID: userID})
	if err != nil {
		return err
	}
	out := []AdjustmentDraft{}
	if len(ids) == 0 {
		return httpx.OK(c, out)
	}
	rows, err := h.svc.q.ListDraftsForSuggestions(ctx, ids)
	if err != nil {
		return err
	}
	for i := range rows {
		out = append(out, h.svc.draftDTO(&rows[i]))
	}
	return httpx.OK(c, out)
}

func (h *handler) bulkUpdate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var in bulkInput
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	// A repeated id counts once (Vapor answered 404 "Financial goal not
	// found" for it); the response follows the request's order.
	ids := make([]uuid.UUID, 0, len(in.GoalIDs))
	position := map[uuid.UUID]int{}
	for _, raw := range in.GoalIDs {
		id, ok := portfolios.ParseSwiftUUID(raw)
		if !ok {
			return httpx.BadRequest("Invalid goal ids")
		}
		if _, seen := position[id]; !seen {
			position[id] = len(ids)
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return httpx.BadRequest("Invalid goal ids")
	}
	ctx := c.Request().Context()
	goals, err := h.svc.q.ListOwnedFinancialGoalsByIDs(ctx, goalplanningdb.ListOwnedFinancialGoalsByIDsParams{UserID: s.UserID, Ids: ids})
	if err != nil {
		return err
	}
	if len(goals) != len(ids) {
		return ownedGoalNotFound()
	}
	sort.Slice(goals, func(i, j int) bool { return position[goals[i].ID] < position[goals[j].ID] })
	if in.Status == statusActive {
		currentlyActive, err := h.svc.q.CountActiveFinancialGoalsNotIn(ctx, goalplanningdb.CountActiveFinancialGoalsNotInParams{UserID: s.UserID, Ids: ids})
		if err != nil {
			return err
		}
		snap, err := h.ent.Resolve(ctx, s.UserID)
		if err != nil {
			return err
		}
		total := int(currentlyActive) + len(goals)
		if !snap.IsPro() && total > 1 {
			return entitlements.UpgradeLimit(entitlements.FeatureGoalPlanning, snap.Level, 1, total)
		}
		if err := h.svc.validateBulkActivation(ctx, goals, s.UserID); err != nil {
			return err
		}
	}
	now := h.svc.now()
	err = h.svc.inTx(ctx, func(q *goalplanningdb.Queries) error {
		for i := range goals {
			goals[i].Status = string(in.Status)
			goals[i].UpdatedAt = &now
			if err := q.UpdateFinancialGoalStatus(ctx, goalplanningdb.UpdateFinancialGoalStatusParams{ID: goals[i].ID, Status: goals[i].Status, UpdatedAt: &now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	out := make([]FinancialGoal, 0, len(goals))
	for i := range goals {
		dto, err := h.svc.goalDTO(ctx, h.svc.q, &goals[i])
		if err != nil {
			return err
		}
		out = append(out, dto)
	}
	return httpx.OK(c, out)
}
