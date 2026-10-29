package financing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/financingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// service is FinancingService.
type service struct {
	db   *pgxpool.Pool
	q    *financingdb.Queries
	calc calculator
	now  func() time.Time
}

func notFound() error { return httpx.NotFound("") }

// assumptions is FinancingService.assumptions.
//
// Deviation: Vapor's Fluent model aliases external_monthly_debt_payments as
// "financing_affordability_assumptions_external_monthly_debt_payments" (66
// bytes); Postgres truncates aliases to 63, the field is never "fetched", and
// reading it hits FluentKit's fatalError, killing the process. So in Vapor
// any read of a stored row (GET /assumptions, POST /simulations) crashes the
// server. v2 returns the stored row, including
// monthly_savings_target_override (whose alias is truncated too and which
// Vapor would always have read back as nil).
func (s *service) assumptions(ctx context.Context, userID uuid.UUID) (Assumptions, error) {
	row, err := s.q.GetAssumptions(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultAssumptions(), nil
	}
	if err != nil {
		return Assumptions{}, err
	}
	scope := row.IncomeScope
	if scope != "personal" && scope != "household" {
		scope = "personal"
	}
	return Assumptions{
		IncomeScope:                  scope,
		NetMonthlyIncomeOverride:     row.NetMonthlyIncomeOverride,
		GrossMonthlyIncome:           row.GrossMonthlyIncome,
		ExternalMonthlyDebtPayments:  row.ExternalMonthlyDebtPayments,
		SafetyBufferPercent:          row.SafetyBufferPercent,
		MonthlySavingsTargetOverride: row.MonthlySavingsTargetOverride,
	}, nil
}

func (s *service) simulation(ctx context.Context, userID uuid.UUID, req SimulationRequest) (SimulationResponse, error) {
	stored, err := s.assumptions(ctx, userID)
	if err != nil {
		return SimulationResponse{}, err
	}
	applied := stored
	if req.Assumptions != nil {
		applied = *req.Assumptions
	}
	budget, err := s.budgetContext(ctx, userID, applied)
	if err != nil {
		return SimulationResponse{}, err
	}
	return s.calc.simulate(req, applied, budget)
}

func (s *service) updateAssumptions(ctx context.Context, userID uuid.UUID, v Assumptions) (Assumptions, error) {
	deref := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	if !(v.ExternalMonthlyDebtPayments >= 0) ||
		!(v.SafetyBufferPercent >= 0 && v.SafetyBufferPercent <= 30) ||
		!(deref(v.GrossMonthlyIncome) >= 0) ||
		!(deref(v.NetMonthlyIncomeOverride) >= 0) {
		return Assumptions{}, httpx.BadRequest("Invalid affordability assumptions.")
	}
	now := s.now()
	existing, err := s.q.GetAssumptions(ctx, userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = s.q.InsertAssumptions(ctx, financingdb.InsertAssumptionsParams{
			ID: uuid.New(), UserID: userID, IncomeScope: v.IncomeScope,
			NetMonthlyIncomeOverride: v.NetMonthlyIncomeOverride, GrossMonthlyIncome: v.GrossMonthlyIncome,
			ExternalMonthlyDebtPayments: v.ExternalMonthlyDebtPayments, SafetyBufferPercent: v.SafetyBufferPercent,
			MonthlySavingsTargetOverride: v.MonthlySavingsTargetOverride, UpdatedAt: &now,
		})
	case err != nil:
	default:
		// Fluent's update() is a no-op (updated_at untouched) when nothing
		// changed. Its fetched model never held external_monthly_debt_payments
		// or monthly_savings_target_override (see assumptions), so setting them
		// always counted as a change.
		err = s.q.UpdateAssumptions(ctx, financingdb.UpdateAssumptionsParams{
			ID: existing.ID, IncomeScope: v.IncomeScope,
			NetMonthlyIncomeOverride: v.NetMonthlyIncomeOverride, GrossMonthlyIncome: v.GrossMonthlyIncome,
			ExternalMonthlyDebtPayments: v.ExternalMonthlyDebtPayments, SafetyBufferPercent: v.SafetyBufferPercent,
			MonthlySavingsTargetOverride: v.MonthlySavingsTargetOverride, UpdatedAt: &now,
		})
	}
	if err != nil {
		return Assumptions{}, err
	}
	return v, nil
}

func (s *service) createPlan(ctx context.Context, userID uuid.UUID, req PlanRequest) (PlanResponse, error) {
	if err := s.validatePlan(req); err != nil {
		return PlanResponse{}, err
	}
	terms, err := json.Marshal(req.Terms)
	if err != nil {
		return PlanResponse{}, err
	}
	now := s.now()
	plan := financingdb.FinancingPlan{
		ID: uuid.New(), UserID: userID, Title: req.Title, Market: req.Market,
		PurchaseType: req.PurchaseType, Currency: strings.ToUpper(req.Currency), Status: statusActive,
		UserSharePercent: req.UserSharePercent, SourceDomain: req.SourceDomain, CreatedAt: &now, UpdatedAt: &now,
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PlanResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := q.InsertPlan(ctx, financingdb.InsertPlanParams{
		ID: plan.ID, UserID: plan.UserID, Title: plan.Title, Market: plan.Market, PurchaseType: plan.PurchaseType,
		Currency: plan.Currency, Status: plan.Status, UserSharePercent: plan.UserSharePercent,
		SourceDomain: plan.SourceDomain, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt,
	}); err != nil {
		return PlanResponse{}, err
	}
	if err := q.InsertRevision(ctx, financingdb.InsertRevisionParams{
		ID: uuid.New(), PlanID: plan.ID, EffectiveInstallment: 1, Terms: terms, CreatedAt: &now,
	}); err != nil {
		return PlanResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PlanResponse{}, err
	}
	return planResponse(plan, req.Terms), nil
}

func (s *service) plans(ctx context.Context, userID uuid.UUID) ([]PlanResponse, error) {
	rows, err := s.q.ListPlans(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []PlanResponse{}
	for _, p := range rows {
		terms, ok, err := s.latestTerms(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, planResponse(p, terms))
	}
	return out, nil
}

func (s *service) plan(ctx context.Context, userID, planID uuid.UUID) (financingdb.FinancingPlan, error) {
	p, err := s.q.GetPlan(ctx, financingdb.GetPlanParams{ID: planID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return p, notFound()
	}
	return p, err
}

func (s *service) updateStatus(ctx context.Context, userID, planID uuid.UUID, status string) (PlanResponse, error) {
	p, err := s.plan(ctx, userID, planID)
	if err != nil {
		return PlanResponse{}, err
	}
	// Fluent skips the UPDATE (and the updated_at stamp) when the status is
	// unchanged.
	if p.Status != status {
		now := s.now()
		if err := s.q.UpdatePlanStatus(ctx, financingdb.UpdatePlanStatusParams{ID: planID, Status: status, UpdatedAt: &now}); err != nil {
			return PlanResponse{}, err
		}
		p.Status, p.UpdatedAt = status, &now
	}
	terms, ok, err := s.latestTerms(ctx, planID)
	if err != nil {
		return PlanResponse{}, err
	}
	if !ok {
		return PlanResponse{}, notFound()
	}
	return planResponse(p, terms), nil
}

func (s *service) revise(ctx context.Context, userID, planID uuid.UUID, req RevisionRequest) (PlanResponse, error) {
	p, err := s.plan(ctx, userID, planID)
	if err != nil {
		return PlanResponse{}, err
	}
	if err := s.validateTerms(req.Terms); err != nil {
		return PlanResponse{}, err
	}
	lastMatch := int64(0)
	if m, err := s.q.LastMatch(ctx, planID); err == nil {
		lastMatch = m.InstallmentNumber
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return PlanResponse{}, err
	}
	lastRevision := int64(1)
	if r, err := s.q.LatestRevision(ctx, planID); err == nil {
		lastRevision = r.EffectiveInstallment
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return PlanResponse{}, err
	}
	terms, err := json.Marshal(req.Terms)
	if err != nil {
		return PlanResponse{}, err
	}
	now := s.now()
	if err := s.q.InsertRevision(ctx, financingdb.InsertRevisionParams{
		ID: uuid.New(), PlanID: planID, EffectiveInstallment: max(lastMatch+1, lastRevision+1), Terms: terms, CreatedAt: &now,
	}); err != nil {
		return PlanResponse{}, err
	}
	return planResponse(p, req.Terms), nil
}

func (s *service) schedule(ctx context.Context, userID, planID uuid.UUID) ([]Projection, error) {
	p, err := s.plan(ctx, userID, planID)
	if err != nil {
		return nil, err
	}
	terms, ok, err := s.latestTerms(ctx, planID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound()
	}
	matches, err := s.q.ListMatches(ctx, planID)
	if err != nil {
		return nil, err
	}
	byInstallment := make(map[int]string, len(matches))
	for _, m := range matches {
		byInstallment[int(m.InstallmentNumber)] = apijson.FormatUUID(m.ExpenseID)
	}
	id := apijson.FormatUUID(planID)
	projections, err := s.calc.projections(&id, terms, p.Currency, byInstallment)
	if err != nil {
		return nil, err
	}
	if p.Status == statusCancelled {
		for i := range projections {
			if projections[i].Status != installmentMatched {
				projections[i].Status = installmentCancelled
			}
		}
	}
	return projections, nil
}

func (s *service) projections(ctx context.Context, userID uuid.UUID, from, to *time.Time) ([]Projection, error) {
	plans, err := s.q.ListPlansNotCancelled(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []Projection{}
	for _, p := range plans {
		items, err := s.schedule(ctx, userID, p.ID)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			date, ok := parseDay(item.DueDate)
			if !ok {
				continue
			}
			if (from == nil || !date.Before(*from)) && (to == nil || !date.After(*to)) {
				out = append(out, item)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DueDate < out[j].DueDate })
	return out, nil
}

func (s *service) expense(ctx context.Context, userID, expenseID uuid.UUID) (financingdb.GetExpenseRow, error) {
	e, err := s.q.GetExpense(ctx, financingdb.GetExpenseParams{ID: expenseID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return e, notFound()
	}
	return e, err
}

func (s *service) match(ctx context.Context, userID, planID uuid.UUID, installment int, expenseID uuid.UUID) (MatchResponse, error) {
	if _, err := s.plan(ctx, userID, planID); err != nil {
		return MatchResponse{}, err
	}
	if installment <= 0 {
		return MatchResponse{}, notFound()
	}
	e, err := s.expense(ctx, userID, expenseID)
	if err != nil {
		return MatchResponse{}, err
	}
	now := s.now()
	id := uuid.New()
	// A second match for the same installment or expense violates a unique
	// constraint; Vapor let the database error through as a 500.
	if err := s.q.InsertMatch(ctx, financingdb.InsertMatchParams{
		ID: id, PlanID: planID, InstallmentNumber: int64(installment), ExpenseID: e.ID, CreatedAt: &now,
	}); err != nil {
		return MatchResponse{}, err
	}
	return MatchResponse{
		ID: apijson.FormatUUID(id), PlanID: apijson.FormatUUID(planID), InstallmentNumber: installment,
		ExpenseID: apijson.FormatUUID(expenseID), CreatedAt: timestamp(&now),
	}, nil
}

func (s *service) matchCandidates(ctx context.Context, userID, expenseID uuid.UUID) ([]MatchCandidate, error) {
	e, err := s.expense(ctx, userID, expenseID)
	if err != nil {
		return nil, err
	}
	plans, err := s.q.ListPlansWithStatus(ctx, financingdb.ListPlansWithStatusParams{UserID: userID, Status: statusActive})
	if err != nil {
		return nil, err
	}
	out := []MatchCandidate{}
	for _, p := range plans {
		schedule, err := s.schedule(ctx, userID, p.ID)
		if err != nil {
			return nil, err
		}
		for _, projection := range schedule {
			if projection.Status == installmentMatched {
				continue
			}
			due, ok := parseDay(projection.DueDate)
			if !ok {
				continue
			}
			expected := projection.TotalAmount * p.UserSharePercent / 100
			amountDelta := math.Abs(e.Amount - expected)
			amountTolerance := math.Max(1, expected*0.02)
			// Calendar.dateComponents([.day]) between two UTC midnights, in
			// the server's time zone (UTC in production).
			dayDelta := int(e.OccurredOn.Sub(due).Hours() / 24)
			if dayDelta < 0 {
				dayDelta = -dayDelta
			}
			score := 0.0
			reasons := []string{}
			if amountDelta <= amountTolerance {
				score += 0.6
				reasons = append(reasons, "amount")
			}
			if dayDelta <= 7 {
				score += 0.3
				reasons = append(reasons, "date")
			}
			if containsFold(e.Title, p.Title) || containsFold(p.Title, e.Title) {
				score += 0.1
				reasons = append(reasons, "title")
			}
			if score >= 0.5 {
				out = append(out, MatchCandidate{
					PlanID: apijson.FormatUUID(p.ID), PlanTitle: p.Title, InstallmentNumber: projection.InstallmentNumber,
					DueDate: projection.DueDate, ExpenseID: apijson.FormatUUID(expenseID), Score: score, Reasons: reasons,
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// containsFold is localizedCaseInsensitiveContains; Foundation finds no
// range for an empty needle.
func containsFold(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func (s *service) unmatch(ctx context.Context, userID, planID uuid.UUID, installment int) error {
	if _, err := s.plan(ctx, userID, planID); err != nil {
		return err
	}
	m, err := s.q.GetMatch(ctx, financingdb.GetMatchParams{PlanID: planID, InstallmentNumber: int64(installment)})
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	return s.q.DeleteMatch(ctx, m.ID)
}

// budgetContext is FinancingService.budgetContext.
func (s *service) budgetContext(ctx context.Context, userID uuid.UUID, a Assumptions) (budgetContext, error) {
	var netSalary *float64
	baseline, plannedSavings := 0.0, 0.0
	snap, err := s.q.LatestBudgetSnapshot(ctx, userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return budgetContext{}, err
	default:
		salary := snap.NetSalary
		netSalary = &salary
		var shares map[string]float64
		if err := json.Unmarshal(snap.TargetShares, &shares); err != nil {
			return budgetContext{}, err
		}
		items, err := s.q.BudgetPlanItems(ctx, snap.ID)
		if err != nil {
			return budgetContext{}, err
		}
		for _, item := range items {
			userAmount := item.PlannedAmount * item.UserSharePercent / 100
			if item.AllocationKind == "investmentContribution" {
				plannedSavings += userAmount
			} else {
				baseline += userAmount
			}
		}
		plannedSavings = math.Max(plannedSavings, snap.NetSalary*shares["futureYou"])
	}
	active, err := s.q.ListPlansWithStatus(ctx, financingdb.ListPlansWithStatusParams{UserID: userID, Status: statusActive})
	if err != nil {
		return budgetContext{}, err
	}
	existing := 0.0
	for _, p := range active {
		terms, ok, err := s.latestTerms(ctx, p.ID)
		if err != nil {
			return budgetContext{}, err
		}
		if !ok {
			continue
		}
		items, err := s.calc.projections(nil, terms, p.Currency, nil)
		if err != nil {
			return budgetContext{}, err
		}
		highest := 0.0
		for i, item := range items {
			if i == 0 || item.TotalAmount > highest {
				highest = item.TotalAmount
			}
		}
		existing += highest * p.UserSharePercent / 100
	}
	income := a.NetMonthlyIncomeOverride
	if income == nil {
		income = netSalary
	}
	return budgetContext{
		NetMonthlyIncome: income, BaselineSpending: baseline,
		PlannedSavings: plannedSavings, ExistingFinancingPayments: existing,
	}, nil
}

func (s *service) latestTerms(ctx context.Context, planID uuid.UUID) (OfferTerms, bool, error) {
	r, err := s.q.LatestRevision(ctx, planID)
	if errors.Is(err, pgx.ErrNoRows) {
		return OfferTerms{}, false, nil
	}
	if err != nil {
		return OfferTerms{}, false, err
	}
	var terms OfferTerms
	if err := decodeStrict(r.Terms, &terms); err != nil {
		return OfferTerms{}, false, err
	}
	// validate also turns a null additionalCosts into [].
	if err := terms.validate("terms"); err != nil {
		return OfferTerms{}, false, err
	}
	return terms, true, nil
}

func planResponse(p financingdb.FinancingPlan, terms OfferTerms) PlanResponse {
	market := p.Market
	if _, ok := marketCurrency[market]; !ok {
		market = "PT"
	}
	purchaseType := p.PurchaseType
	if !slices.Contains(purchaseTypes, purchaseType) {
		purchaseType = "other"
	}
	status := p.Status
	if !slices.Contains(planStatuses, status) {
		status = statusActive
	}
	return PlanResponse{
		ID: apijson.FormatUUID(p.ID), Title: p.Title, Market: market, PurchaseType: purchaseType,
		Currency: p.Currency, Status: status, UserSharePercent: p.UserSharePercent, Terms: terms,
		SourceDomain: p.SourceDomain, CreatedAt: timestamp(p.CreatedAt), UpdatedAt: timestamp(p.UpdatedAt),
	}
}

func (s *service) validatePlan(r PlanRequest) error {
	if strings.TrimSpace(r.Title) == "" ||
		strings.ToUpper(r.Currency) != marketCurrency[r.Market] ||
		!(r.UserSharePercent >= 0 && r.UserSharePercent <= 100) {
		return httpx.BadRequest("Invalid financing plan.")
	}
	return s.validateTerms(r.Terms)
}

func (s *service) validateTerms(t OfferTerms) error {
	if t.TermMonths < 1 || t.TermMonths > 480 || !(t.PurchaseAmount > 0) || !(t.DownPayment >= 0) {
		return httpx.BadRequest("Invalid financing terms.")
	}
	_, err := s.calc.projections(nil, t, "EUR", nil)
	return err
}
