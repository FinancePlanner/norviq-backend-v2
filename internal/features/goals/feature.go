// Package goals ports the dashboard GoalsController (/v1/goals,
// Dashboard/GoalsController.swift and Models/Goal.swift): manual to-do goals
// plus three system goals evaluated on every list.
package goals

import (
	"errors"
	"strconv"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/server"
	"norviq-api/internal/store/goalsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// GoalStatus / GoalStatusSource raw values.
const (
	statusPending   = "pending"
	statusCompleted = "completed"
	sourceManual    = "manual"
	sourceSystem    = "system"
)

// Feature mounts GoalsController.
type Feature struct {
	Now func() time.Time
	// ScheduleAchievementCheck is req.scheduleAchievementCheck(for:); nil
	// is a no-op.
	ScheduleAchievementCheck func(userID uuid.UUID)
}

// Name implements server.Feature.
func (Feature) Name() string { return "goals" }

// Register implements server.Feature, mirroring GoalsController.boot:
// ScopedBearerAuthenticator + SessionToken guard, then goals:read or
// goals:write per route.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{q: goalsdb.New(d.DB), now: f.Now, achievements: f.ScheduleAchievementCheck}
	if h.now == nil {
		h.now = time.Now
	}
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeGoalsRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeGoalsWrite)}
	r.V1.GET("/goals", h.index, read...)
	r.V1.POST("/goals", h.create, write...)
	r.V1.GET("/goals/:id", h.show, read...)
	r.V1.PATCH("/goals/:id", h.update, write...)
	r.V1.PATCH("/goals/:id/status", h.updateStatus, write...)
	r.V1.DELETE("/goals/:id", h.delete, write...)
	return nil
}

type handler struct {
	q            *goalsdb.Queries
	now          func() time.Time
	achievements func(uuid.UUID)
}

// Response is GoalResponse.
type Response struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Status          string  `json:"status"`
	StatusUpdatedBy *string `json:"statusUpdatedBy,omitempty"`
	CompletedAt     *string `json:"completedAt,omitempty"`
	CreatedAt       *string `json:"createdAt,omitempty"`
	UpdatedAt       *string `json:"updatedAt,omitempty"`
}

// request is GoalRequest.
type request struct {
	Title string `json:"title"`
}

// goalStatus is GoalStatus.
type goalStatus string

func (goalStatus) SwiftEnum() (string, []string, bool) {
	return "GoalStatus", []string{statusPending, statusCompleted}, false
}

// statusSource is GoalStatusSource.
type statusSource string

func (statusSource) SwiftEnum() (string, []string, bool) {
	return "GoalStatusSource", []string{sourceManual, sourceSystem}, false
}

// statusRequest is GoalStatusUpdateRequest (synthesized Codable: both keys
// required).
type statusRequest struct {
	Status goalStatus   `json:"status"`
	Source statusSource `json:"source"`
}

func formatPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := apijson.FormatTime(*t)
	return &s
}

// toDTO is Goal.toDTO().
func toDTO(g *goalsdb.Goal) Response {
	status := g.Status
	if status != statusPending && status != statusCompleted {
		status = statusPending
	}
	var by *string
	if g.StatusUpdatedBy != nil && (*g.StatusUpdatedBy == sourceManual || *g.StatusUpdatedBy == sourceSystem) {
		by = g.StatusUpdatedBy
	}
	return Response{
		ID: apijson.FormatUUID(g.ID), Title: g.Title, Status: status, StatusUpdatedBy: by,
		CompletedAt: formatPtr(g.CompletedAt), CreatedAt: formatPtr(g.CreatedAt), UpdatedAt: formatPtr(g.UpdatedAt),
	}
}

func (h *handler) index(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	manual, err := h.q.ListGoalsByUser(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := make([]Response, 0, len(manual)+3)
	nowString := apijson.FormatTime(h.now())
	system := func(id, title string, done bool) Response {
		by := sourceSystem
		r := Response{ID: id, Title: title, Status: statusPending, StatusUpdatedBy: &by, UpdatedAt: &nowString}
		if done {
			r.Status = statusCompleted
			r.CompletedAt = &nowString
		}
		return r
	}
	watchlist, err := h.q.CountActiveWatchlistItems(ctx, s.UserID)
	if err != nil {
		return err
	}
	out = append(out, system("00000000-0000-0000-0000-000000000001",
		"Add 3 stocks to your Watchlist ("+itoa(watchlist)+"/3)", watchlist >= 3))
	budgets, err := h.q.CountBudgetSnapshots(ctx, s.UserID)
	if err != nil {
		return err
	}
	out = append(out, system("00000000-0000-0000-0000-000000000002", "Set up your first monthly budget", budgets >= 1))
	expenses, err := h.q.CountExpenses(ctx, s.UserID)
	if err != nil {
		return err
	}
	out = append(out, system("00000000-0000-0000-0000-000000000003", "Log your first expense", expenses >= 1))
	for i := range manual {
		out = append(out, toDTO(&manual[i]))
	}
	return httpx.OK(c, out)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (h *handler) create(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var in request
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	now := h.now()
	by := sourceManual
	g := goalsdb.Goal{
		ID: uuid.New(), UserID: s.UserID, Title: in.Title, Status: statusPending, StatusUpdatedBy: &by,
		CreatedAt: &now, UpdatedAt: &now,
	}
	if err := h.q.InsertGoal(c.Request().Context(), goalsdb.InsertGoalParams{
		ID: g.ID, UserID: g.UserID, Title: g.Title, Status: g.Status, StatusUpdatedBy: g.StatusUpdatedBy,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}); err != nil {
		return err
	}
	return httpx.OK(c, toDTO(&g))
}

// find is Goal.find(_:userId:) behind `req.parameters.get("id", as:
// UUID.self)`: both failures are a bare 404.
func (h *handler) find(c *echo.Context, userID uuid.UUID) (*goalsdb.Goal, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return nil, httpx.NotFound("")
	}
	g, err := h.q.GetOwnedGoal(c.Request().Context(), goalsdb.GetOwnedGoalParams{UserID: userID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("")
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (h *handler) show(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	g, err := h.find(c, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, toDTO(g))
}

func (h *handler) update(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var in request
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	g, err := h.find(c, s.UserID)
	if err != nil {
		return err
	}
	now := h.now()
	g.Title = in.Title
	g.UpdatedAt = &now
	if err := h.q.UpdateGoalTitle(c.Request().Context(), goalsdb.UpdateGoalTitleParams{ID: g.ID, Title: g.Title, UpdatedAt: &now}); err != nil {
		return err
	}
	return httpx.OK(c, toDTO(g))
}

func (h *handler) updateStatus(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var in statusRequest
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	g, err := h.find(c, s.UserID)
	if err != nil {
		return err
	}
	now := h.now()
	g.Status = string(in.Status)
	source := string(in.Source)
	g.StatusUpdatedBy = &source
	g.CompletedAt = nil
	if in.Status == statusCompleted {
		// completed_at is `timestamp without time zone`: store the UTC wall
		// clock, as Fluent's UTC session did.
		completed := now.UTC()
		g.CompletedAt = &completed
	}
	g.UpdatedAt = &now
	if err := h.q.UpdateGoalStatus(c.Request().Context(), goalsdb.UpdateGoalStatusParams{
		ID: g.ID, Status: g.Status, StatusUpdatedBy: g.StatusUpdatedBy, CompletedAt: g.CompletedAt, UpdatedAt: &now,
	}); err != nil {
		return err
	}
	if g.CompletedAt != nil && h.achievements != nil {
		h.achievements(s.UserID)
	}
	return httpx.OK(c, toDTO(g))
}

func (h *handler) delete(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	g, err := h.find(c, s.UserID)
	if err != nil {
		return err
	}
	if err := h.q.DeleteGoal(c.Request().Context(), g.ID); err != nil {
		return err
	}
	// EmptyAPIResponse encodes as {}.
	return httpx.OK(c, struct{}{})
}
