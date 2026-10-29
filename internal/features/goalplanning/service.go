package goalplanning

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/goalplanningdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Quoter is the slice of MarketDataService a goal valuation needs.
type Quoter interface {
	Quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error)
}

// Resolver is EntitlementResolver.
type Resolver interface {
	Resolve(ctx context.Context, userID uuid.UUID) (entitlements.Snapshot, error)
}

// service is GoalPlanningService.
type service struct {
	db     *pgxpool.Pool
	q      *goalplanningdb.Queries
	quotes Quoter
	now    func() time.Time
}

// validationError is a StockPlanShared validation error thrown by
// FinancialGoalInput.validate(). It is no AbortError, so Vapor answered 500
// with String(describing:) ("Internal Server Error" in production).
type validationError string

func (e validationError) Error() string { return string(e) }

func ownedGoalNotFound() *httpx.Error { return httpx.NotFound("Financial goal not found") }

// goalDTO is GoalPlanningService.goalDTO.
func (s *service) goalDTO(ctx context.Context, q *goalplanningdb.Queries, m *goalplanningdb.FinancialGoal) (FinancialGoal, error) {
	allocations, err := q.ListAllocationsByGoalSorted(ctx, m.ID)
	if err != nil {
		return FinancialGoal{}, err
	}
	links, err := q.ListCategoryLinksByGoalSorted(ctx, m.ID)
	if err != nil {
		return FinancialGoal{}, err
	}
	out := FinancialGoal{
		ID:                       apijson.FormatUUID(m.ID),
		Name:                     m.Name,
		PortfolioListID:          apijson.FormatUUID(m.PortfolioListID),
		GoalType:                 orDefault(m.GoalType, goalTypes, "custom"),
		TargetAmount:             m.TargetAmount,
		TargetDate:               dateString(m.TargetDate),
		BaseCurrency:             m.BaseCurrency,
		StartingCapital:          m.StartingCapital,
		MonthlyContribution:      m.MonthlyContribution,
		AnnualContributionGrowth: m.AnnualContributionGrowth,
		InflationAssumption:      m.InflationAssumption,
		RiskProfile:              orDefault(m.RiskProfile, riskProfiles, riskModerate),
		ExpectedAnnualReturn:     m.ExpectedAnnualReturn,
		Status:                   orDefault(m.Status, goalStatuses, statusActive),
		PortfolioAllocations:     make([]PortfolioAllocation, 0, len(allocations)),
		ExpenseCategoryLinks:     make([]CategoryLink, 0, len(links)),
		CreatedAt:                timestampPtr(m.CreatedAt),
		UpdatedAt:                timestampPtr(m.UpdatedAt),
	}
	for _, a := range allocations {
		out.PortfolioAllocations = append(out.PortfolioAllocations, PortfolioAllocation{
			ID: apijson.FormatUUID(a.ID), PortfolioListID: apijson.FormatUUID(a.PortfolioListID),
			AllocationPercentage: a.AllocationPercentage,
		})
	}
	for _, l := range links {
		if !slices.Contains(categoryRoles, l.Role) {
			continue
		}
		out.ExpenseCategoryLinks = append(out.ExpenseCategoryLinks, CategoryLink{
			ID: apijson.FormatUUID(l.ID), CategoryID: apijson.FormatUUID(l.CategoryID), Role: l.Role,
		})
	}
	return out, nil
}

func timestampPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := timestamp(*t)
	return &s
}

// overrides are progress(for:monthlyContribution:expectedAnnualReturn:targetDate:).
type overrides struct {
	monthlyContribution  *float64
	expectedAnnualReturn *float64
	targetDate           *time.Time
}

// progress is GoalPlanningService.progress.
func (s *service) progress(ctx context.Context, goal *goalplanningdb.FinancialGoal, userID uuid.UUID, o overrides) (Progress, error) {
	now := s.now()
	targetDate := goal.TargetDate
	if o.targetDate != nil {
		targetDate = *o.targetDate
	}
	monthsRemaining := max(0, monthsBetween(now, targetDate))
	allocations, err := s.allocationModels(ctx, goal)
	if err != nil {
		return Progress{}, err
	}
	value, warnings, err := s.currentValue(ctx, allocations, goal.StartingCapital, userID)
	if err != nil {
		return Progress{}, err
	}
	observed, err := s.observedMonthlyContribution(ctx, goal.ID, userID)
	if err != nil {
		return Progress{}, err
	}
	planned := goal.MonthlyContribution
	trajectoryContribution := goal.MonthlyContribution
	if observed > 0 {
		trajectoryContribution = observed
	}
	if o.monthlyContribution != nil {
		planned = *o.monthlyContribution
		trajectoryContribution = *o.monthlyContribution
	}
	annualReturn := goal.ExpectedAnnualReturn
	if o.expectedAnnualReturn != nil {
		annualReturn = *o.expectedAnnualReturn
	}
	projected := futureValue(value, trajectoryContribution, annualReturn, monthsRemaining, goal.AnnualContributionGrowth)
	createdAt := now
	if goal.CreatedAt != nil {
		createdAt = *goal.CreatedAt
	}
	monthsElapsed := max(0, monthsBetween(createdAt, now))
	plannedToday := futureValue(goal.StartingCapital, goal.MonthlyContribution, goal.ExpectedAnnualReturn, monthsElapsed, goal.AnnualContributionGrowth)
	targetAtDate := inflatedTarget(goal.TargetAmount, goal.InflationAssumption, monthsRemaining)
	completion := monthsToTarget(value, goal.TargetAmount, trajectoryContribution, annualReturn, goal.AnnualContributionGrowth, goal.InflationAssumption)
	var driftMonths *int
	if completion != nil {
		driftMonths = intPtr(*completion - monthsRemaining)
	}
	state := driftState(value, goal.TargetAmount, targetAtDate, projected, driftMonths)
	if observed == 0 && o.monthlyContribution == nil {
		warnings = append(warnings, "No observed contribution history; projection uses the planned monthly contribution.")
	}
	snapshots, err := s.q.ListSnapshotsByGoal(ctx, goal.ID)
	if err != nil {
		return Progress{}, err
	}
	trajectory := makeTrajectory(goal, targetDate, value, trajectoryContribution, annualReturn, snapshots, now)
	var completionDate *string
	if completion != nil {
		d := dateString(addMonths(now, *completion))
		completionDate = &d
	}
	return Progress{
		GoalID:                      apijson.FormatUUID(goal.ID),
		Currency:                    goal.BaseCurrency,
		CurrentValue:                value,
		TargetAmount:                goal.TargetAmount,
		PercentComplete:             math.Min(1, math.Max(0, value/goal.TargetAmount)),
		PlannedValueToday:           plannedToday,
		ProjectedValueAtTarget:      projected,
		ProjectedCompletionDate:     completionDate,
		DriftAmount:                 projected - goal.TargetAmount,
		DriftMonths:                 driftMonths,
		DriftState:                  state,
		PlannedMonthlyContribution:  planned,
		ObservedMonthlyContribution: observed,
		Trajectory:                  trajectory,
		Warnings:                    warnings,
		CalculatedAt:                timestamp(now),
	}, nil
}

// driftState is GoalPlanningService.driftState.
func driftState(current, target, targetAtDate, projected float64, driftMonths *int) string {
	if current >= target {
		return driftComplete
	}
	if driftMonths == nil {
		return driftInsufficientData
	}
	dm := *driftMonths
	if math.Abs(projected-targetAtDate) <= float64(targetAtDate*0.01) || abs(dm) <= 1 {
		return driftOnTrack
	}
	if dm < 0 {
		return driftAhead
	}
	return driftBehind
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// allocationModels is GoalPlanningService.allocationModels: the stored rows,
// or a synthetic 100% allocation to the goal's portfolio.
func (s *service) allocationModels(ctx context.Context, goal *goalplanningdb.FinancialGoal) ([]goalplanningdb.FinancialGoalPortfolioAllocation, error) {
	rows, err := s.q.ListAllocationsByGoal(ctx, goal.ID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []goalplanningdb.FinancialGoalPortfolioAllocation{{
			GoalID: goal.ID, UserID: goal.UserID, PortfolioListID: goal.PortfolioListID, AllocationPercentage: 100,
		}}, nil
	}
	return rows, nil
}

// currentValue is GoalPlanningService.currentValue.
func (s *service) currentValue(ctx context.Context, allocations []goalplanningdb.FinancialGoalPortfolioAllocation, fallback float64, userID uuid.UUID) (float64, []string, error) {
	warnings := []string{}
	if len(allocations) == 0 {
		return fallback, warnings, nil
	}
	total := 0.0
	for _, a := range allocations {
		holdings, err := s.q.ListHoldingsForPortfolio(ctx, goalplanningdb.ListHoldingsForPortfolioParams{UserID: userID, PortfolioListID: a.PortfolioListID})
		if err != nil {
			return 0, nil, err
		}
		portfolioValue := 0.0
		for _, h := range holdings {
			price := h.BuyPrice
			quote, qerr := s.quote(ctx, h.Symbol)
			if qerr != nil {
				warnings = append(warnings, "Live price unavailable for "+h.Symbol+"; purchase price used.")
			} else {
				price = quote.CurrentPrice
			}
			portfolioValue += float64(h.Shares * price)
		}
		total += float64(portfolioValue*a.AllocationPercentage) / 100
	}
	if total == 0 && fallback > 0 {
		warnings = append(warnings, "Linked portfolios have no valued holdings; starting capital is used.")
		return fallback, warnings, nil
	}
	seen := map[string]bool{}
	unique := []string{}
	for _, w := range warnings {
		if !seen[w] {
			seen[w] = true
			unique = append(unique, w)
		}
	}
	sort.Strings(unique)
	return total, unique, nil
}

func (s *service) quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error) {
	if s.quotes == nil {
		return marketdata.QuoteResponse{}, fmt.Errorf("market data unavailable")
	}
	return s.quotes.Quote(ctx, symbol)
}

// observedMonthlyContribution is GoalPlanningService.observedMonthlyContribution.
func (s *service) observedMonthlyContribution(ctx context.Context, goalID, userID uuid.UUID) (float64, error) {
	start := addMonths(s.now(), -3)
	amounts, err := s.q.ListContributionAmountsSince(ctx, goalplanningdb.ListContributionAmountsSinceParams{GoalID: goalID, UserID: userID, Since: start})
	if err != nil {
		return 0, err
	}
	manual := 0.0
	for _, a := range amounts {
		manual += a
	}
	categoryIDs, err := s.q.ListCategoryIDsByGoalRole(ctx, goalplanningdb.ListCategoryIDsByGoalRoleParams{GoalID: goalID, Role: roleObservedContribution})
	if err != nil {
		return 0, err
	}
	if len(categoryIDs) == 0 {
		return manual / 3, nil
	}
	expenses, err := s.q.ListExpenseAmountsSince(ctx, goalplanningdb.ListExpenseAmountsSinceParams{UserID: userID, Since: start, CategoryIds: categoryIDs})
	if err != nil {
		return 0, err
	}
	spent := 0.0
	for _, e := range expenses {
		spent += e
	}
	return (manual + spent) / 3, nil
}

// reductionCandidate is GoalPlanningService.reductionCandidate.
func (s *service) reductionCandidate(ctx context.Context, goalID, userID uuid.UUID) (*float64, error) {
	categoryIDs, err := s.q.ListCategoryIDsByGoalRole(ctx, goalplanningdb.ListCategoryIDsByGoalRoleParams{GoalID: goalID, Role: roleReductionCandidate})
	if err != nil {
		return nil, err
	}
	if len(categoryIDs) == 0 {
		return nil, nil
	}
	start := addMonths(s.now(), -3)
	expenses, err := s.q.ListExpenseAmountsSince(ctx, goalplanningdb.ListExpenseAmountsSinceParams{UserID: userID, Since: start, CategoryIds: categoryIDs})
	if err != nil {
		return nil, err
	}
	spent := 0.0
	for _, e := range expenses {
		spent += e
	}
	v := spent / 3 * 0.2
	return &v, nil
}

// supportsAllocationAdvice is GoalPlanningService.supportsAllocationAdvice.
func (s *service) supportsAllocationAdvice(ctx context.Context, goal *goalplanningdb.FinancialGoal, userID uuid.UUID) (bool, error) {
	if goal.RiskProfile == riskConservative {
		return false, nil
	}
	allocations, err := s.allocationModels(ctx, goal)
	if err != nil {
		return false, err
	}
	ids := make([]uuid.UUID, 0, len(allocations))
	for _, a := range allocations {
		ids = append(ids, a.PortfolioListID)
	}
	if len(ids) == 0 {
		return false, nil
	}
	categories, err := s.q.ListHoldingCategoriesForPortfolios(ctx, goalplanningdb.ListHoldingCategoriesForPortfoliosParams{UserID: userID, PortfolioIds: ids})
	if err != nil {
		return false, err
	}
	if len(categories) == 0 {
		return false, nil
	}
	for _, c := range categories {
		switch c {
		case "stock", "etf", "mutual_fund", "cash", "bond":
		default:
			return false, nil
		}
	}
	return true, nil
}

// makeTrajectory is GoalPlanningService.makeTrajectory.
func makeTrajectory(goal *goalplanningdb.FinancialGoal, targetDate time.Time, currentValue, monthlyContribution, annualReturn float64,
	snapshots []goalplanningdb.FinancialGoalProgressSnapshot, now time.Time,
) []TrajectoryPoint {
	start := now
	if goal.CreatedAt != nil {
		start = *goal.CreatedAt
	}
	totalMonths := max(1, monthsBetween(start, targetDate))
	elapsed := max(0, monthsBetween(start, now))
	// keyed(by:): the last snapshot of a month wins.
	byMonth := map[string]float64{}
	for _, snap := range snapshots {
		byMonth[monthKey(snap.CalculatedAt)] = snap.CurrentValue
	}
	points := make([]TrajectoryPoint, 0, totalMonths+1)
	for month := 0; month <= totalMonths; month++ {
		date := addMonths(start, month)
		planned := futureValue(goal.StartingCapital, goal.MonthlyContribution, goal.ExpectedAnnualReturn, month, goal.AnnualContributionGrowth)
		projected := planned
		if month >= elapsed {
			projected = futureValue(currentValue, monthlyContribution, annualReturn, max(0, month-elapsed), goal.AnnualContributionGrowth)
		}
		p := TrajectoryPoint{Date: dateString(date), PlannedValue: planned, ProjectedValue: projected}
		if v, ok := byMonth[monthKey(date)]; ok {
			p.ActualValue = &v
		}
		points = append(points, p)
	}
	return points
}

// validateInput is FinancialGoalInput.validate().
func validateInput(in *goalInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return validationError("invalidName")
	}
	if utf8.RuneCountInString(in.BaseCurrency) != 3 {
		return validationError("invalidCurrency")
	}
	if !finite(in.TargetAmount) || !finite(in.StartingCapital) || !finite(in.MonthlyContribution) ||
		!(in.TargetAmount > 0) || !(in.StartingCapital >= 0) || !(in.MonthlyContribution >= 0) {
		return validationError("invalidAmount")
	}
	if !finite(in.AnnualContributionGrowth) || !finite(in.InflationAssumption) ||
		!within(in.AnnualContributionGrowth, -1, 1) || !within(in.InflationAssumption, -1, 1) ||
		(in.ExpectedAnnualReturn != nil && !(finite(*in.ExpectedAnnualReturn) && within(*in.ExpectedAnnualReturn, -0.5, 0.5))) {
		return validationError("invalidAmount")
	}
	for _, a := range in.PortfolioAllocations {
		if !(a.AllocationPercentage > 0 && a.AllocationPercentage <= 100) {
			return validationError("invalidAllocation")
		}
	}
	return nil
}

func finite(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) }

func within(v, lo, hi float64) bool { return v >= lo && v <= hi }

type allocation struct {
	portfolioID uuid.UUID
	percentage  float64
}

// validateLinks is GoalPlanningService.validateLinks.
func (s *service) validateLinks(ctx context.Context, in *goalInput, userID uuid.UUID, excluding *uuid.UUID) ([]allocation, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}
	if math.Abs(in.configuredReturn()-defaultAnnualReturn(string(in.RiskProfile))) > 0.010_001 {
		return nil, httpx.Unprocessable("Expected return must remain within one percentage point of the selected risk profile")
	}
	allocations := []allocation{}
	seen := map[uuid.UUID]bool{}
	for _, a := range in.PortfolioAllocations {
		id, ok := portfolios.ParseSwiftUUID(a.PortfolioListID)
		if !ok || seen[id] {
			return nil, httpx.BadRequest("Portfolio allocations must contain unique valid ids")
		}
		seen[id] = true
		owned, err := s.q.PortfolioOwned(ctx, goalplanningdb.PortfolioOwnedParams{ID: id, UserID: userID})
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, httpx.NotFound("Portfolio not found")
		}
		if err := s.validateAllocationCapacity(ctx, id, a.AllocationPercentage, string(in.Status), userID, excluding); err != nil {
			return nil, err
		}
		allocations = append(allocations, allocation{id, a.AllocationPercentage})
	}
	keys := map[string]bool{}
	for _, link := range in.ExpenseCategoryLinks {
		key := link.CategoryID + ":" + string(link.Role)
		invalid := httpx.BadRequest("Expense category links must be unique and owned by the user")
		if keys[key] {
			return nil, invalid
		}
		keys[key] = true
		id, ok := portfolios.ParseSwiftUUID(link.CategoryID)
		if !ok {
			return nil, invalid
		}
		owned, err := s.q.ExpenseCategoryOwned(ctx, goalplanningdb.ExpenseCategoryOwnedParams{ID: id, UserID: userID})
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, invalid
		}
	}
	return allocations, nil
}

// validateAllocationCapacity is GoalPlanningService.validateAllocationCapacity.
func (s *service) validateAllocationCapacity(ctx context.Context, portfolioID uuid.UUID, percentage float64, status string, userID uuid.UUID, excluding *uuid.UUID) error {
	if status != statusActive {
		return nil
	}
	rows, err := s.q.ListAllocationsForPortfolio(ctx, goalplanningdb.ListAllocationsForPortfolioParams{PortfolioListID: portfolioID, UserID: userID})
	if err != nil {
		return err
	}
	used := 0.0
	for _, r := range rows {
		if excluding != nil && r.GoalID == *excluding {
			continue
		}
		if r.GoalStatus == statusActive {
			used += r.AllocationPercentage
		}
	}
	if used+percentage > 100.000_001 {
		return httpx.Unprocessable("Active goal allocations for a portfolio cannot exceed 100%")
	}
	return nil
}

// validateBulkActivation is GoalPlanningService.validateBulkActivation.
func (s *service) validateBulkActivation(ctx context.Context, goals []goalplanningdb.FinancialGoal, userID uuid.UUID) error {
	included := map[uuid.UUID]bool{}
	active, err := s.q.ListActiveFinancialGoalIDs(ctx, userID)
	if err != nil {
		return err
	}
	for _, id := range active {
		included[id] = true
	}
	for i := range goals {
		included[goals[i].ID] = true
	}
	rows, err := s.q.ListAllocationsByUser(ctx, userID)
	if err != nil {
		return err
	}
	totals := map[uuid.UUID]float64{}
	for _, r := range rows {
		if included[r.GoalID] {
			totals[r.PortfolioListID] += r.AllocationPercentage
		}
	}
	for _, t := range totals {
		if t > 100.000_001 {
			return httpx.Unprocessable("Bulk activation would allocate more than 100% of a portfolio")
		}
	}
	return nil
}

// replaceLinks is GoalPlanningService.replaceLinks.
func replaceLinks(ctx context.Context, q *goalplanningdb.Queries, goalID, userID uuid.UUID, in *goalInput, allocations []allocation, now time.Time) error {
	if err := q.DeleteAllocationsByGoal(ctx, goalID); err != nil {
		return err
	}
	if err := q.DeleteCategoryLinksByGoal(ctx, goalID); err != nil {
		return err
	}
	for _, a := range allocations {
		if err := q.InsertAllocation(ctx, goalplanningdb.InsertAllocationParams{
			ID: uuid.New(), GoalID: goalID, UserID: userID, PortfolioListID: a.portfolioID,
			AllocationPercentage: a.percentage, CreatedAt: &now,
		}); err != nil {
			return err
		}
	}
	for _, link := range in.ExpenseCategoryLinks {
		categoryID, ok := portfolios.ParseSwiftUUID(link.CategoryID)
		if !ok {
			continue
		}
		if err := q.InsertCategoryLink(ctx, goalplanningdb.InsertCategoryLinkParams{
			ID: uuid.New(), GoalID: goalID, UserID: userID, CategoryID: categoryID, Role: string(link.Role), CreatedAt: &now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// suggestions is GoalPlanningService.suggestions.
func (s *service) suggestions(ctx context.Context, goal *goalplanningdb.FinancialGoal, userID uuid.UUID) ([]Suggestion, error) {
	progress, err := s.progress(ctx, goal, userID, overrides{})
	if err != nil {
		return nil, err
	}
	cutoff := s.now().Add(-86400 * time.Second)
	if err := s.q.DeleteStaleProposedSuggestions(ctx, goalplanningdb.DeleteStaleProposedSuggestionsParams{GoalID: goal.ID, Cutoff: cutoff}); err != nil {
		return nil, err
	}
	existing, err := s.q.ListProposedSuggestions(ctx, goal.ID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		out := make([]Suggestion, 0, len(existing))
		for i := range existing {
			out = append(out, s.suggestionDTO(&existing[i]))
		}
		return out, nil
	}

	var generated []goalplanningdb.FinancialGoalSuggestion
	add := func(kind, title, explanation string, monthly, allocationPct *float64, months *int) {
		var m *int64
		if months != nil {
			v := int64(*months)
			m = &v
		}
		generated = append(generated, goalplanningdb.FinancialGoalSuggestion{
			ID: uuid.New(), GoalID: goal.ID, UserID: userID, Kind: kind, Title: title, Explanation: explanation,
			MonthlyAmount: monthly, AllocationPercentage: allocationPct, EstimatedMonthsChanged: m, Status: suggestionProposed,
		})
	}
	switch {
	case progress.DriftState == driftBehind:
		months := max(1, monthsBetween(s.now(), goal.TargetDate))
		required, err := requiredMonthlyContribution(progress.CurrentValue, goal.TargetAmount, goal.ExpectedAnnualReturn, months,
			goal.AnnualContributionGrowth, goal.InflationAssumption)
		if err != nil {
			return nil, err
		}
		increase := math.Max(0, required-progress.ObservedMonthlyContribution)
		if increase > 0.5 {
			var changed *int
			if progress.DriftMonths != nil {
				changed = intPtr(-max(0, *progress.DriftMonths))
			}
			add(kindIncreaseContribution, "Increase monthly contributions",
				"Adding "+money(increase, goal.BaseCurrency)+" per month closes the projected shortfall at the current return assumption.",
				&increase, nil, changed)
		}
		reduction, err := s.reductionCandidate(ctx, goal.ID, userID)
		if err != nil {
			return nil, err
		}
		if reduction != nil && *reduction > 0 {
			add(kindReduceSpending, "Redirect flexible spending",
				"Redirecting up to "+money(*reduction, goal.BaseCurrency)+" per month from linked categories can fund this goal. Review the budget draft before applying it.",
				reduction, nil, nil)
		}
		supported, err := s.supportsAllocationAdvice(ctx, goal, userID)
		if err != nil {
			return nil, err
		}
		if supported {
			one := 1.0
			add(kindRebalanceAllocation, "Review allocation within your risk band",
				"A scenario up to one percentage point above the "+goal.RiskProfile+" return assumption remains inside the configured guardrail. Review a rebalance draft before applying trades.",
				nil, &one, nil)
		}
		extend := 1
		if progress.DriftMonths != nil {
			extend = *progress.DriftMonths
		}
		add(kindExtendTargetDate, "Consider a later target date",
			fmt.Sprintf("Extending the target by %d months preserves the current contribution trajectory.", max(1, extend)),
			nil, nil, progress.DriftMonths)
	case progress.DriftState == driftAhead && goal.MonthlyContribution > 0:
		months := max(1, monthsBetween(s.now(), goal.TargetDate))
		required, err := requiredMonthlyContribution(progress.CurrentValue, goal.TargetAmount, goal.ExpectedAnnualReturn, months,
			goal.AnnualContributionGrowth, goal.InflationAssumption)
		if err != nil {
			return nil, err
		}
		reduction := math.Max(0, goal.MonthlyContribution-required)
		if reduction > 0.5 {
			add(kindReduceContribution, "You have contribution headroom",
				"You could reduce monthly contributions by "+money(reduction, goal.BaseCurrency)+" and remain on plan under current assumptions.",
				&reduction, nil, nil)
		}
	}
	out := make([]Suggestion, 0, len(generated))
	for i := range generated {
		g := &generated[i]
		now := s.now()
		g.CreatedAt = &now
		if err := s.q.InsertSuggestion(ctx, goalplanningdb.InsertSuggestionParams{
			ID: g.ID, GoalID: g.GoalID, UserID: g.UserID, Kind: g.Kind, Title: g.Title, Explanation: g.Explanation,
			MonthlyAmount: g.MonthlyAmount, AllocationPercentage: g.AllocationPercentage,
			EstimatedMonthsChanged: g.EstimatedMonthsChanged, Status: g.Status, CreatedAt: g.CreatedAt,
		}); err != nil {
			return nil, err
		}
	}
	for i := range generated {
		out = append(out, s.suggestionDTO(&generated[i]))
	}
	return out, nil
}

// money is GoalPlanningService.money.
func money(value float64, currency string) string {
	return strings.ToUpper(currency) + " " + fmt.Sprintf("%.0f", value)
}

// suggestionDTO is GoalPlanningService.suggestionDTO.
func (s *service) suggestionDTO(m *goalplanningdb.FinancialGoalSuggestion) Suggestion {
	created := s.now()
	if m.CreatedAt != nil {
		created = *m.CreatedAt
	}
	var months *int
	if m.EstimatedMonthsChanged != nil {
		months = intPtr(int(*m.EstimatedMonthsChanged))
	}
	return Suggestion{
		ID: apijson.FormatUUID(m.ID), GoalID: apijson.FormatUUID(m.GoalID),
		Kind:  orDefault(m.Kind, suggestKinds, kindIncreaseContribution),
		Title: m.Title, Explanation: m.Explanation, MonthlyAmount: m.MonthlyAmount,
		AllocationPercentage: m.AllocationPercentage, EstimatedMonthsChanged: months,
		Status:    orDefault(m.Status, suggestStates, suggestionProposed),
		CreatedAt: timestamp(created),
	}
}

// persistSnapshot is GoalPlanningService.persistSnapshot.
func (s *service) persistSnapshot(ctx context.Context, p *Progress, goal *goalplanningdb.FinancialGoal, userID uuid.UUID) error {
	now := s.now()
	tomorrow := now.UTC().AddDate(0, 0, 1)
	isMonthEnd := tomorrow.Month() != now.UTC().Month()
	return s.q.InsertSnapshot(ctx, goalplanningdb.InsertSnapshotParams{
		ID: uuid.New(), GoalID: goal.ID, UserID: userID, CurrentValue: p.CurrentValue,
		PlannedValue: p.PlannedValueToday, ProjectedValue: p.ProjectedValueAtTarget,
		DriftState: p.DriftState, IsMonthEnd: isMonthEnd, CalculatedAt: now,
	})
}

// draftPayload is ScenarioJSON as Fluent stored it: {"values": {...}}.
type draftPayload struct {
	Values map[string]any `json:"values"`
}

// draftDTO is GoalPlanningController.draftDTO.
func (s *service) draftDTO(m *goalplanningdb.FinancialGoalAdjustmentDraft) AdjustmentDraft {
	payload := map[string]string{}
	var stored draftPayload
	if err := json.Unmarshal(m.Payload, &stored); err == nil {
		for k, v := range stored.Values {
			switch val := v.(type) {
			case string:
				payload[k] = val
			case float64:
				payload[k] = marketdata.SwiftDoubleString(val)
			case bool:
				if val {
					payload[k] = "true"
				} else {
					payload[k] = "false"
				}
			}
		}
	}
	created := s.now()
	if m.CreatedAt != nil {
		created = *m.CreatedAt
	}
	return AdjustmentDraft{
		ID: apijson.FormatUUID(m.ID), SuggestionID: apijson.FormatUUID(m.SuggestionID),
		Destination: orDefault(m.Destination, destinations, destinationGoal),
		Payload:     payload, CreatedAt: timestamp(created),
	}
}

// contributionDTO is GoalPlanningController.contributionDTO.
func (s *service) contributionDTO(m *goalplanningdb.FinancialGoalContribution) Contribution {
	created := s.now()
	if m.CreatedAt != nil {
		created = *m.CreatedAt
	}
	return Contribution{
		ID: apijson.FormatUUID(m.ID), GoalID: apijson.FormatUUID(m.GoalID), Amount: m.Amount,
		OccurredAt: dateString(m.OccurredAt), Note: m.Note, CreatedAt: timestamp(created),
	}
}

// inTx runs fn in a transaction (req.db.transaction).
func (s *service) inTx(ctx context.Context, fn func(q *goalplanningdb.Queries) error) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return fn(s.q.WithTx(tx)) })
}
