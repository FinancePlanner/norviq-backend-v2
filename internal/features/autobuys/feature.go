// Package autobuys ports the /v1/autobuys half of
// TerminalPositionsController (TerminalPositions/TerminalPositionsController
// .swift, TerminalPositionsService.swift, Models/AutobuyRecord.swift and
// StockPlanShared.AutobuyMath). Autobuys are planning records only: nothing
// executes them, so there is no job.
package autobuys

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/autobuysdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/autobuys.
type Feature struct {
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "autobuys" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{q: autobuysdb.New(d.DB), now: f.Now}
	if h.now == nil {
		h.now = time.Now
	}
	g := r.V1.Group("/autobuys", d.Auth.Scoped(), auth.Guard())
	g.GET("", h.list, auth.RequireScope(auth.ScopePlanningRead))
	write := auth.RequireScope(auth.ScopePlanningWrite)
	g.POST("", h.create, write)
	g.PATCH("/:id", h.update, write)
	g.DELETE("/:id", h.delete, write)
	return nil
}

type handler struct {
	q   autobuysdb.Querier
	now func() time.Time
}

// Cadences (AutobuyCadence). Any other string decodes as "unknown".
const (
	CadenceWeekly                = "weekly"
	CadenceBiweekly              = "biweekly"
	CadenceBimonthly             = "bimonthly"
	CadenceMonthly               = "monthly"
	CadencePercentOfContribution = "percentOfContribution"
	CadenceUnknown               = "unknown"
)

// cadenceValue is AutobuyCadence(rawValue:) ?? .unknown.
func cadenceValue(raw string) string {
	switch raw {
	case CadenceWeekly, CadenceBiweekly, CadenceBimonthly, CadenceMonthly, CadencePercentOfContribution:
		return raw
	}
	return CadenceUnknown
}

// MonthlyEquivalent is AutobuyMath.monthlyEquivalent.
func MonthlyEquivalent(amount float64, cadence string, percent *float64) *float64 {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return nil
	}
	var monthly float64
	switch cadence {
	case CadenceWeekly:
		monthly = amount * 52 / 12
	case CadenceBiweekly:
		monthly = amount * 26 / 12
	case CadenceBimonthly:
		monthly = amount * 6 / 12
	case CadenceMonthly:
		monthly = amount
	case CadencePercentOfContribution:
		if percent == nil || math.IsNaN(*percent) || math.IsInf(*percent, 0) || *percent <= 0 || amount <= 0 {
			return nil
		}
		monthly = amount * *percent
	default:
		return nil
	}
	if math.IsInf(monthly, 0) || math.IsNaN(monthly) {
		return nil
	}
	return &monthly
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// MonthlyTotal is TerminalPositionsService.monthlyTotal: active rows only,
// a finite sum.
func MonthlyTotal(rows []autobuysdb.Autobuy) float64 {
	total := 0.0
	for i := range rows {
		r := &rows[i]
		v := 0.0
		if r.Active {
			if m := MonthlyEquivalent(r.Amount, cadenceValue(r.Cadence), r.Percent); m != nil {
				v = *m
			}
		}
		if next := total + v; finite(v) && finite(next) {
			total = next
		}
	}
	return total
}

// Response is AutobuyResponse.
type Response struct {
	ID                string   `json:"id"`
	Ticker            *string  `json:"ticker,omitempty"`
	Label             string   `json:"label"`
	Amount            float64  `json:"amount"`
	Cadence           string   `json:"cadence"`
	Percent           *float64 `json:"percent,omitempty"`
	Active            bool     `json:"active"`
	MonthlyEquivalent *float64 `json:"monthlyEquivalent,omitempty"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
}

// ListResponse is AutobuysListResponse.
type ListResponse struct {
	Currency     string     `json:"currency"`
	Autobuys     []Response `json:"autobuys"`
	MonthlyTotal float64    `json:"monthlyTotal"`
}

// toResponse is AutobuyRecord.toResponse(): timestamps fall back to now.
func toResponse(r *autobuysdb.Autobuy, now time.Time) Response {
	created, updated := now, now
	if r.CreatedAt != nil {
		created, updated = *r.CreatedAt, *r.CreatedAt
	}
	if r.UpdatedAt != nil {
		updated = *r.UpdatedAt
	}
	cadence := cadenceValue(r.Cadence)
	return Response{
		ID: apijson.FormatUUID(r.ID), Ticker: r.Ticker, Label: r.Label, Amount: r.Amount, Cadence: cadence,
		Percent: r.Percent, Active: r.Active, MonthlyEquivalent: MonthlyEquivalent(r.Amount, cadence, r.Percent),
		CreatedAt: apijson.FormatTime(created), UpdatedAt: apijson.FormatTime(updated),
	}
}

// ---- validation (TerminalPositionsService statics) ----

const (
	maxLabelLength = 80
	maxInput       = 1e18
)

var tickerPattern = regexp.MustCompile(`^[A-Z0-9.\-]{1,12}$`)

var clearableFields = []string{"percent", "ticker"}

func normalisedTicker(raw string) (string, error) {
	t := strings.ToUpper(strings.TrimSpace(raw))
	if !tickerPattern.MatchString(t) {
		return "", httpx.Unprocessable("ticker must be 1-12 letters, digits, '.' or '-'")
	}
	return t, nil
}

func nonNegative(v float64, field string) (float64, error) {
	if !finite(v) {
		return 0, httpx.Unprocessable(field + " must be a number")
	}
	if math.Abs(v) > maxInput {
		return 0, httpx.Unprocessable(field + " is too large")
	}
	if v < 0 {
		return 0, httpx.Unprocessable(field + " must be zero or more")
	}
	return v, nil
}

func label(raw string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" || utf8.RuneCountInString(t) > maxLabelLength {
		return "", httpx.Unprocessable(fmt.Sprintf("label must be 1-%d characters", maxLabelLength))
	}
	return t, nil
}

func clearFields(raw []string) (map[string]bool, error) {
	requested := map[string]bool{}
	var unknown []string
	for _, f := range raw {
		if requested[f] {
			continue
		}
		requested[f] = true
		if !slices.Contains(clearableFields, f) {
			unknown = append(unknown, f)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, httpx.Unprocessable("cannot clear: " + strings.Join(unknown, ", "))
	}
	return requested, nil
}

func validatedPercent(percent *float64, cadence string) (*float64, error) {
	if cadence == CadenceUnknown {
		return nil, httpx.Unprocessable("unknown cadence")
	}
	if percent != nil && (!finite(*percent) || *percent <= 0 || *percent > 1) {
		return nil, httpx.Unprocessable("percent must be between 0 and 1 (0.04 = 4%)")
	}
	if cadence == CadencePercentOfContribution && percent == nil {
		return nil, httpx.Unprocessable("percentOfContribution needs a percent")
	}
	return percent, nil
}

// ---- handlers ----

func (h *handler) userAndID(c *echo.Context) (uuid.UUID, uuid.UUID, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return uuid.Nil, uuid.Nil, httpx.BadRequest("invalid id")
	}
	return s.UserID, id, nil
}

// currency is TerminalPositionsService.currency(userId:on:): the default
// portfolio's, then any portfolio's, then MARKET_DEFAULT_CURRENCY ?? "USD".
func (h *handler) currency(ctx context.Context, userID uuid.UUID) (string, error) {
	rows, err := h.q.DefaultPortfolioCurrency(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(rows) > 0 {
		return rows[0], nil
	}
	if rows, err = h.q.AnyPortfolioCurrency(ctx, userID); err != nil {
		return "", err
	}
	if len(rows) > 0 {
		return rows[0], nil
	}
	if v, ok := os.LookupEnv("MARKET_DEFAULT_CURRENCY"); ok {
		return v, nil
	}
	return "USD", nil
}

func (h *handler) find(ctx context.Context, userID, id uuid.UUID) (*autobuysdb.Autobuy, error) {
	rows, err := h.q.GetAutobuy(ctx, autobuysdb.GetAutobuyParams{UserID: userID, ID: id})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, httpx.NotFound("Autobuy not found.")
	}
	return &rows[0], nil
}

func (h *handler) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rows, err := h.q.ListAutobuys(ctx, s.UserID)
	if err != nil {
		return err
	}
	currency, err := h.currency(ctx, s.UserID)
	if err != nil {
		return err
	}
	now := h.now()
	out := ListResponse{Currency: currency, Autobuys: make([]Response, 0, len(rows)), MonthlyTotal: MonthlyTotal(rows)}
	for i := range rows {
		out.Autobuys = append(out.Autobuys, toResponse(&rows[i], now))
	}
	return httpx.OK(c, out)
}

// createRequest is AutobuyCreateRequest.
type createRequest struct {
	Ticker  *string  `json:"ticker"`
	Label   string   `json:"label"`
	Amount  float64  `json:"amount"`
	Cadence string   `json:"cadence"`
	Percent *float64 `json:"percent"`
	Active  *bool    `json:"active"`
}

// updateRequest is AutobuyUpdateRequest.
type updateRequest struct {
	Ticker  *string   `json:"ticker"`
	Label   *string   `json:"label"`
	Amount  *float64  `json:"amount"`
	Cadence *string   `json:"cadence"`
	Percent *float64  `json:"percent"`
	Active  *bool     `json:"active"`
	Clear   *[]string `json:"clear"`
}

func (h *handler) create(c *echo.Context) error {
	var in createRequest
	if err := portfolios.DecodeSwiftBody(c, &in); err != nil {
		return err
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rec := autobuysdb.Autobuy{ID: uuid.New(), UserID: s.UserID, Active: true}
	if in.Ticker != nil {
		t, err := normalisedTicker(*in.Ticker)
		if err != nil {
			return err
		}
		rec.Ticker = &t
	}
	if rec.Label, err = label(in.Label); err != nil {
		return err
	}
	if rec.Amount, err = nonNegative(in.Amount, "amount"); err != nil {
		return err
	}
	rec.Cadence = cadenceValue(in.Cadence)
	if rec.Percent, err = validatedPercent(in.Percent, rec.Cadence); err != nil {
		return err
	}
	if in.Active != nil {
		rec.Active = *in.Active
	}
	now := h.now()
	if err := h.q.InsertAutobuy(c.Request().Context(), autobuysdb.InsertAutobuyParams{
		ID: rec.ID, UserID: rec.UserID, Ticker: rec.Ticker, Label: rec.Label, Amount: rec.Amount,
		Cadence: rec.Cadence, Percent: rec.Percent, Active: rec.Active, Now: &now,
	}); err != nil {
		return err
	}
	rec.CreatedAt, rec.UpdatedAt = &now, &now
	return httpx.Created(c, toResponse(&rec, now))
}

func (h *handler) update(c *echo.Context) error {
	var in updateRequest
	if err := portfolios.DecodeSwiftBody(c, &in); err != nil {
		return err
	}
	user, id, err := h.userAndID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rec, err := h.find(ctx, user, id)
	if err != nil {
		return err
	}
	var requested []string
	if in.Clear != nil {
		requested = *in.Clear
	}
	clear, err := clearFields(requested)
	if err != nil {
		return err
	}
	if in.Ticker != nil {
		t, err := normalisedTicker(*in.Ticker)
		if err != nil {
			return err
		}
		rec.Ticker = &t
	}
	if in.Label != nil {
		if rec.Label, err = label(*in.Label); err != nil {
			return err
		}
	}
	if in.Amount != nil {
		if rec.Amount, err = nonNegative(*in.Amount, "amount"); err != nil {
			return err
		}
	}
	if in.Cadence != nil {
		rec.Cadence = cadenceValue(*in.Cadence)
	}
	if in.Percent != nil {
		rec.Percent = in.Percent
	}
	if in.Active != nil {
		rec.Active = *in.Active
	}
	if clear["ticker"] {
		rec.Ticker = nil
	}
	if clear["percent"] {
		rec.Percent = nil
	}
	if rec.Percent, err = validatedPercent(rec.Percent, cadenceValue(rec.Cadence)); err != nil {
		return err
	}
	now := h.now()
	if err := h.q.UpdateAutobuy(ctx, autobuysdb.UpdateAutobuyParams{
		ID: rec.ID, Ticker: rec.Ticker, Label: rec.Label, Amount: rec.Amount, Cadence: rec.Cadence,
		Percent: rec.Percent, Active: rec.Active, Now: &now,
	}); err != nil {
		return err
	}
	rec.UpdatedAt = &now
	return httpx.OK(c, toResponse(rec, now))
}

func (h *handler) delete(c *echo.Context) error {
	user, id, err := h.userAndID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if _, err := h.find(ctx, user, id); err != nil {
		return err
	}
	if err := h.q.DeleteAutobuy(ctx, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
