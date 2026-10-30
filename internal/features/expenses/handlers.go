package expenses

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/onboarding"
	"norviq-api/internal/server"
	"norviq-api/internal/store/badgesdb"
	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type handlers struct {
	d        *server.Deps
	svc      *Service
	ent      *entitlements.Service
	overview OverviewSource
	now      func() time.Time
}

// uuidParam is requireUUIDParameter: "Invalid <name>." on a bad id.
func uuidParam(c *echo.Context, name string) (uuid.UUID, error) {
	id, ok := parseUUIDString(c.Param(name))
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Invalid " + name + ".")
	}
	return id, nil
}

func queryDay(c *echo.Context, name string) *time.Time {
	s := httpx.QueryString(c, name)
	if s == nil {
		return nil
	}
	d, ok := ParseDay(*s)
	if !ok {
		return nil
	}
	return &d
}

// --- household partner (Pro: household_partner) ----------------------------

func (h *handlers) getHouseholdPartner(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureHouseholdPartner, s.UserID); err != nil {
		return err
	}
	out, err := h.svc.GetHouseholdPartner(ctx, h.d.DB, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) updateHouseholdPartner(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureHouseholdPartner, s.UserID); err != nil {
		return err
	}
	req, err := decodeHouseholdRequest(c)
	if err != nil {
		return err
	}
	out, err := h.svc.UpdateHouseholdPartner(ctx, h.d.DB, s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// --- expenses ---------------------------------------------------------------

func (h *handlers) getExpenses(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	limit := max(1, min(httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 50), 200))
	f := ExpenseSearchFilters{From: queryDay(c, "from"), To: queryDay(c, "to"), Limit: limit, Query: httpx.QueryString(c, "q")}
	if raw := httpx.QueryString(c, "cursor"); raw != nil {
		if t, ok := ParseISOMillis(*raw); ok {
			f.Cursor = &t
		}
	}
	if raw := httpx.QueryString(c, "category_id"); raw != nil {
		f.CategoryID = uuidPtrFromString(raw)
	}
	if f.CategoryID == nil {
		if raw := httpx.QueryString(c, "categoryId"); raw != nil {
			f.CategoryID = uuidPtrFromString(raw)
		}
	}
	if raw := httpx.QueryString(c, "pillar"); raw != nil {
		if p, ok := ParsePillar(*raw); ok {
			f.Pillar = &p
		}
	}
	items, next, err := h.svc.SearchExpenses(c.Request().Context(), h.d.DB, s.UserID, f)
	if err != nil {
		return err
	}
	if next != nil {
		c.Response().Header().Add("X-Next-Cursor", *next)
	}
	return httpx.OK(c, items)
}

func (h *handlers) createExpense(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	req, err := decodeExpensePayload(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	created, err := h.svc.CreateExpense(ctx, h.d.DB, s.UserID, req)
	if err != nil {
		return err
	}
	// One small XP award per UTC day for logging spending.
	now := h.now()
	badges.AwardXP(ctx, badgesdb.New(h.d.DB), s.UserID, "expense_logged", 5,
		"expense_logged:"+now.UTC().Format("2006-01-02"), now, h.logWarn)
	return httpx.Created(c, created)
}

func (h *handlers) logWarn(msg string, args ...any) { h.d.Logger.Warn(msg, args...) }

func (h *handlers) importCSV(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return httpx.Abort(http.StatusRequestEntityTooLarge, "")
		}
		return err
	}
	if len(raw) == 0 {
		return httpx.BadRequest("Missing CSV body.")
	}
	csv := ""
	if utf8.Valid(raw) {
		csv = string(raw)
	}
	// The documented default is a dry run: only an explicit dry_run=false
	// writes. (Vapor's URL-form decoder read an absent key as false, so a
	// request without dry_run imported for real; norviq-mcp relies on the
	// documented default for its preview call.)
	dryRun := true
	if v := httpx.QueryOptionalBool(c, "dry_run"); v != nil {
		dryRun = *v
	}
	ctx := c.Request().Context()
	cats, err := h.svc.GetCategories(ctx, h.d.DB, s.UserID)
	if err != nil {
		return err
	}
	byName := map[string]string{}
	for _, cat := range cats {
		k := strings.ToLower(cat.Name)
		if _, ok := byName[k]; !ok {
			byName[k] = cat.ID
		}
	}
	out, err := CSVService{Service: h.svc, EvaluateDrift: true}.ImportCSV(ctx, h.d.DB, csv, s.UserID, dryRun, byName)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) exportCSV(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	csv, err := CSVService{Service: h.svc}.ExportCSV(c.Request().Context(), h.d.DB, s.UserID, queryDay(c, "from"), queryDay(c, "to"))
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="expenses.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", []byte(csv))
}

func (h *handlers) updateExpense(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "expenseId")
	if err != nil {
		return err
	}
	req, err := decodeExpensePayload(c)
	if err != nil {
		return err
	}
	out, err := h.svc.UpdateExpense(c.Request().Context(), h.d.DB, s.UserID, id, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) deleteExpense(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "expenseId")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteExpense(c.Request().Context(), h.d.DB, s.UserID, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// --- categories -------------------------------------------------------------

func (h *handlers) getCategories(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.svc.GetCategories(c.Request().Context(), h.d.DB, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) createCategory(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	req, err := decodeCategoryRequest(c)
	if err != nil {
		return err
	}
	out, err := h.svc.CreateCategory(c.Request().Context(), h.d.DB, s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

func (h *handlers) deleteCategory(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "categoryId")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteCategory(c.Request().Context(), h.d.DB, s.UserID, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// --- recurring templates (Pro: recurring_templates) -------------------------

func (h *handlers) requireRecurring(c *echo.Context) (*auth.Session, error) {
	s, err := auth.Require(c)
	if err != nil {
		return nil, err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureRecurringTemplates, s.UserID); err != nil {
		return nil, err
	}
	return s, nil
}

func (h *handlers) getRecurringTemplates(c *echo.Context) error {
	s, err := h.requireRecurring(c)
	if err != nil {
		return err
	}
	out, err := h.svc.GetRecurringTemplates(c.Request().Context(), h.d.DB, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) createRecurringTemplate(c *echo.Context) error {
	s, err := h.requireRecurring(c)
	if err != nil {
		return err
	}
	req, err := decodeRecurringRequest(c)
	if err != nil {
		return err
	}
	out, err := h.svc.CreateRecurringTemplate(c.Request().Context(), h.d.DB, s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

func (h *handlers) updateRecurringTemplate(c *echo.Context) error {
	s, err := h.requireRecurring(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "templateId")
	if err != nil {
		return err
	}
	req, err := decodeRecurringRequest(c)
	if err != nil {
		return err
	}
	out, err := h.svc.UpdateRecurringTemplate(c.Request().Context(), h.d.DB, s.UserID, id, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) deleteRecurringTemplate(c *echo.Context) error {
	s, err := h.requireRecurring(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "templateId")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteRecurringTemplate(c.Request().Context(), h.d.DB, s.UserID, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// --- budget snapshots and plan items ----------------------------------------

func (h *handlers) getSnapshots(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	// Listing is where a new calendar month gets its plan. Idempotent; a
	// failure must not break reads.
	if _, err := h.svc.EnsureCurrentMonthRollover(ctx, h.d.DB, s.UserID, h.now()); err != nil {
		h.d.Logger.Warn("budget month rollover failed", "error", err)
	}
	out, err := h.svc.GetSnapshots(ctx, h.d.DB, s.UserID, httpx.QueryOptionalInt(c, "year"), httpx.QueryOptionalInt(c, "month"))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) latchBudget(c *echo.Context, userID uuid.UUID, salary float64) {
	if salary > 0 {
		onboarding.StampBestEffort(c.Request().Context(), h.d.DB, h.d.Logger, onboarding.LatchBudget, userID)
	}
}

func (h *handlers) createSnapshot(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	req, err := decodeSnapshotPayload(c)
	if err != nil {
		return err
	}
	out, err := h.svc.CreateBudgetSnapshot(c.Request().Context(), h.d.DB, s.UserID, req)
	if err != nil {
		return err
	}
	h.latchBudget(c, s.UserID, out.NetSalary)
	return httpx.Created(c, out)
}

func (h *handlers) updateSnapshot(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "snapshotId")
	if err != nil {
		return err
	}
	req, err := decodeSnapshotPayload(c)
	if err != nil {
		return err
	}
	out, err := h.svc.UpdateSnapshot(c.Request().Context(), h.d.DB, s.UserID, id, req)
	if err != nil {
		return err
	}
	h.latchBudget(c, s.UserID, out.NetSalary)
	return httpx.OK(c, out)
}

func (h *handlers) deleteSnapshot(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "snapshotId")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteSnapshot(c.Request().Context(), h.d.DB, s.UserID, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) getAllPlanItems(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.svc.GetAllPlanItems(c.Request().Context(), h.d.DB, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) getSnapshotItems(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "snapshotId")
	if err != nil {
		return err
	}
	out, err := h.svc.GetPlanItems(c.Request().Context(), h.d.DB, s.UserID, id)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) createPlanItem(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	req, err := decodePlanItemPayload(c)
	if err != nil {
		return err
	}
	out, err := h.svc.CreatePlanItem(c.Request().Context(), h.d.DB, s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

func (h *handlers) updatePlanItem(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "itemId")
	if err != nil {
		return err
	}
	req, err := decodePlanItemPayload(c)
	if err != nil {
		return err
	}
	out, err := h.svc.UpdatePlanItem(c.Request().Context(), h.d.DB, s.UserID, id, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) deletePlanItem(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "itemId")
	if err != nil {
		return err
	}
	if err := h.svc.DeletePlanItem(c.Request().Context(), h.d.DB, s.UserID, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// --- reports ----------------------------------------------------------------

func (h *handlers) countReport(c *echo.Context) (*auth.Session, error) {
	s, err := auth.Require(c)
	if err != nil {
		return nil, err
	}
	if err := h.ent.IncrementUsage(c.Request().Context(), entitlements.FeatureReportGenerations, s.UserID, 1); err != nil {
		return nil, err
	}
	return s, nil
}

func (h *handlers) getExpenseReports(c *echo.Context) error {
	s, err := h.countReport(c)
	if err != nil {
		return err
	}
	from, to := queryDay(c, "from"), queryDay(c, "to")
	ctx := c.Request().Context()
	granularity := "month"
	if g := httpx.QueryString(c, "granularity"); g != nil {
		granularity = *g
	}
	if granularity == "year" {
		out, err := h.svc.GetYearlyReports(ctx, h.d.DB, s.UserID, from, to)
		if err != nil {
			return err
		}
		return httpx.OK(c, out)
	}
	out, err := h.svc.GetMonthlyReports(ctx, h.d.DB, s.UserID, from, to)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// reportsOverviewResponse is ReportsOverviewResponse.
type reportsOverviewResponse struct {
	GeneratedAt           string                          `json:"generatedAt"`
	PortfolioStatistics   json.RawMessage                 `json:"portfolioStatistics"`
	MonthlySummaries      []BudgetMonthSummaryResponse    `json:"monthlySummaries"`
	YearlySummaries       []BudgetYearSummaryResponse     `json:"yearlySummaries"`
	LatestMonthSummary    *BudgetMonthSummaryResponse     `json:"latestMonthSummary,omitempty"`
	LatestPillarSummaries []PillarPlanningSummaryResponse `json:"latestPillarSummaries"`
	CashFlow              []ReportsCashFlowPointResponse  `json:"cashFlow"`
}

func (h *handlers) getReportsOverview(c *echo.Context) error {
	s, err := h.countReport(c)
	if err != nil {
		return err
	}
	from, to := queryDay(c, "from"), queryDay(c, "to")
	ctx := c.Request().Context()
	generatedAt, portfolio, err := h.overview.PortfolioOverview(ctx, h.d.DB, s.UserID)
	if err != nil {
		return err
	}
	monthly, err := h.svc.GetMonthlyReports(ctx, h.d.DB, s.UserID, from, to)
	if err != nil {
		return err
	}
	yearly, err := h.svc.GetYearlyReports(ctx, h.d.DB, s.UserID, from, to)
	if err != nil {
		return err
	}
	currentMonth := FormatDay(h.now())[:7] + "-01"
	var latest *BudgetMonthSummaryResponse
	for i := range monthly {
		if monthly[i].MonthStart == currentMonth {
			latest = &monthly[i]
			break
		}
	}
	if latest == nil {
		for i := len(monthly) - 1; i >= 0; i-- {
			if monthly[i].Actual > 0 {
				latest = &monthly[i]
				break
			}
		}
	}
	if latest == nil && len(monthly) > 0 {
		latest = &monthly[len(monthly)-1]
	}
	pillars := []PillarPlanningSummaryResponse{}
	if latest != nil {
		if ms, ok := ParseDay(latest.MonthStart); ok {
			if pillars, err = h.svc.GetPillarPlanningSummaries(ctx, h.d.DB, s.UserID, ms); err != nil {
				return err
			}
		}
	}
	cash := make([]ReportsCashFlowPointResponse, len(monthly))
	for i := range monthly {
		r := &monthly[i]
		net := r.Salary - r.Actual
		rate := 0.0
		if r.Salary > 0 {
			rate = net / r.Salary * 100
		}
		cash[i] = ReportsCashFlowPointResponse{MonthStart: r.MonthStart, Income: r.Salary, Expenses: r.Actual, Net: net, SavingsRate: rate}
	}
	return httpx.OK(c, reportsOverviewResponse{
		GeneratedAt: generatedAt, PortfolioStatistics: portfolio, MonthlySummaries: monthly,
		YearlySummaries: yearly, LatestMonthSummary: latest, LatestPillarSummaries: pillars, CashFlow: cash,
	})
}

func (h *handlers) getSuggestions(c *echo.Context) error {
	s, err := h.countReport(c)
	if err != nil {
		return err
	}
	from, to := queryDay(c, "from"), queryDay(c, "to")
	ctx := c.Request().Context()
	monthly, err := h.svc.GetMonthlyReports(ctx, h.d.DB, s.UserID, from, to)
	if err != nil {
		return err
	}
	items, err := h.svc.GetAllPlanItems(ctx, h.d.DB, s.UserID)
	if err != nil {
		return err
	}
	generatedAt := FormatISOMillis(h.now())
	suggestions := BuildSuggestions(monthly, items)
	if len(suggestions) == 0 {
		return httpx.OK(c, ReportSuggestionsResponse{GeneratedAt: generatedAt, Suggestions: []ReportSuggestionResponse{}})
	}
	ids := make([]string, len(suggestions))
	for i := range suggestions {
		ids[i] = suggestions[i].ID
	}
	dismissed, err := q(h.d.DB).ListSuggestionDismissals(ctx, expensesdb.ListSuggestionDismissalsParams{UserID: s.UserID, SuggestionIds: ids})
	if err != nil {
		return err
	}
	gone := map[string]bool{}
	for _, id := range dismissed {
		gone[id] = true
	}
	visible := []ReportSuggestionResponse{}
	for i := range suggestions {
		if !gone[suggestions[i].ID] {
			visible = append(visible, suggestions[i])
		}
	}
	return httpx.OK(c, ReportSuggestionsResponse{GeneratedAt: generatedAt, Suggestions: visible})
}

func (h *handlers) dismissSuggestion(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id := strings.TrimFunc(c.Param("id"), unicode.IsSpace)
	if id == "" {
		return httpx.BadRequest("Invalid suggestion id.")
	}
	ctx := c.Request().Context()
	exists, err := q(h.d.DB).SuggestionDismissalExists(ctx, expensesdb.SuggestionDismissalExistsParams{UserID: s.UserID, SuggestionID: id})
	if err != nil {
		return err
	}
	if !exists {
		// A concurrent dismiss of the same suggestion may win the unique
		// (user, suggestion) race; the suggestion is dismissed either way.
		if err := q(h.d.DB).InsertSuggestionDismissal(ctx, expensesdb.InsertSuggestionDismissalParams{
			ID: uuid.New(), UserID: s.UserID, SuggestionID: id, DismissedAt: h.now(),
		}); err != nil && !isUniqueViolation(err) {
			return err
		}
	}
	return httpx.OK(c, map[string]bool{"success": true})
}
