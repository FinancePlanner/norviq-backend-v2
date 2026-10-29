package budgeting

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/budgetingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Quoter is the slice of the market data service spend-to-units needs.
type Quoter interface {
	Quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error)
}

// Engine is BudgetingEngineService. Other domains (expenses, digest,
// automation) reuse Dashboard and the Evaluator.
type Engine struct {
	DB        *pgxpool.Pool
	Quotes    Quoter
	Evaluator *Evaluator
	Now       func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) q(db budgetingdb.DBTX) *budgetingdb.Queries {
	if db == nil {
		db = e.DB
	}
	return budgetingdb.New(db)
}

func notFound() error   { return httpx.NotFound("") }
func badRequest() error { return httpx.BadRequest("") }

func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func (e *Engine) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, e.DB, fn)
}

// Dashboard is BudgetingEngineService.dashboard. db may be a transaction or
// nil for the pool.
func (e *Engine) Dashboard(ctx context.Context, db budgetingdb.DBTX, userID, snapshotID uuid.UUID) (BudgetDriftDashboard, error) {
	q := e.q(db)
	snap, err := q.SnapshotForUser(ctx, budgetingdb.SnapshotForUserParams{ID: snapshotID, UserID: userID})
	if noRows(err) {
		return BudgetDriftDashboard{}, notFound()
	}
	if err != nil {
		return BudgetDriftDashboard{}, err
	}
	items, err := q.PlanItemsForSnapshot(ctx, budgetingdb.PlanItemsForSnapshotParams{UserID: userID, SnapshotID: snapshotID})
	if err != nil {
		return BudgetDriftDashboard{}, err
	}
	start := monthStartOf(snap.MonthStart)
	expenses, err := q.ExpensesInRange(ctx, budgetingdb.ExpensesInRangeParams{UserID: userID, FromTs: start, ToTs: addMonths(start, 1)})
	if err != nil {
		return BudgetDriftDashboard{}, err
	}
	return calculate(snap, items, expenses), nil
}

// snapshotNear is the private snapshot(userId:monthStart:) lookup: the
// snapshot closest to the month's first day within a day either side.
func snapshotNear(ctx context.Context, q *budgetingdb.Queries, userID uuid.UUID, month time.Time) (*budgetingdb.BudgetSnapshot, error) {
	start := monthStartOf(month)
	cands, err := q.SnapshotsInRange(ctx, budgetingdb.SnapshotsInRangeParams{
		UserID: userID, FromTs: start.AddDate(0, 0, -1), ToTs: start.AddDate(0, 0, 1),
	})
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	best := 0
	for i := 1; i < len(cands); i++ {
		if math.Abs(cands[i].MonthStart.Sub(start).Seconds()) < math.Abs(cands[best].MonthStart.Sub(start).Seconds()) {
			best = i
		}
	}
	return &cands[best], nil
}

// --- spend to units ----------------------------------------------------------

func (e *Engine) spendToUnits(ctx context.Context, userID uuid.UUID) (spendToUnitsCapacity, error) {
	q := e.q(nil)
	var preference string
	hasPreference := false
	if sym, err := q.UserDcaSymbol(ctx, userID); err == nil {
		if sym != nil {
			preference, hasPreference = normalizeSymbol(*sym)
		}
	} else if !noRows(err) {
		return spendToUnitsCapacity{}, err
	}
	holding, err := e.largestHoldingSymbol(ctx, q, userID)
	if err != nil {
		return spendToUnitsCapacity{}, err
	}
	symbol, source := defaultDcaSymbol, "default"
	switch {
	case hasPreference:
		symbol, source = preference, "preference"
	case holding != "":
		symbol, source = holding, "largestHolding"
	}

	surplus := 0.0
	currency := "EUR"
	var cats []spendToUnitsCategory
	now := e.now()
	snap, err := snapshotNear(ctx, q, userID, monthStartOf(now))
	if err != nil {
		return spendToUnitsCapacity{}, err
	}
	if snap != nil {
		drift, err := e.Dashboard(ctx, nil, userID, snap.ID)
		if err != nil {
			return spendToUnitsCapacity{}, err
		}
		surplus = spendSurplus(drift.InvestmentContributionTarget, drift.LostInvestmentCapital, drift.TotalTarget, drift.TotalActual)
		currency = drift.CurrencyCode
		for _, c := range drift.Categories {
			if c.AllocationKind == kindExpense && c.DriftAmount > 0 {
				cats = append(cats, spendToUnitsCategory{Title: c.Title, OverspendAmount: c.DriftAmount})
				if len(cats) == 5 {
					break
				}
			}
		}
	}

	var (
		price     *float64
		priceCur  *string
		quoteDate *time.Time
	)
	if e.Quotes != nil {
		if quote, err := e.Quotes.Quote(ctx, symbol); err == nil {
			if quote.CurrentPrice > 0 {
				price = ptr(quote.CurrentPrice)
			}
			priceCur = ptr(quote.Currency)
			sec, frac := math.Modf(quote.Timestamp)
			quoteDate = ptr(time.Unix(int64(sec), int64(frac*1e9)).UTC())
		}
	}
	stale := price == nil
	var asOf *string
	if quoteDate != nil {
		stale = now.Sub(*quoteDate).Seconds() > staleQuoteSeconds
		asOf = ptr(isoString(*quoteDate))
	}
	out := spendToUnitsCapacity{
		Symbol: symbol, ResolvedFrom: source, Price: price, PriceCurrency: priceCur, QuoteAsOf: asOf,
		QuoteStale: stale, SurplusAmount: surplus, SurplusUnits: spendUnits(surplus, price),
		CurrencyCode: currency, Categories: make([]spendToUnitsCategory, 0, len(cats)), Disclaimer: spendDisclaimer,
	}
	for _, c := range cats {
		c.Units = spendUnits(c.OverspendAmount, price)
		out.Categories = append(out.Categories, c)
	}
	return out, nil
}

func (e *Engine) updateDcaSymbol(ctx context.Context, userID uuid.UUID, symbol string) (spendToUnitsCapacity, error) {
	normalized, ok := normalizeSymbol(symbol)
	if !ok {
		return spendToUnitsCapacity{}, httpx.BadRequest("Choose a ticker like VWCE or IWDA.")
	}
	n, err := e.q(nil).SetUserDcaSymbol(ctx, budgetingdb.SetUserDcaSymbolParams{ID: userID, DcaSymbol: &normalized, UpdatedAt: ptr(e.now())})
	if err != nil {
		return spendToUnitsCapacity{}, err
	}
	if n == 0 {
		return spendToUnitsCapacity{}, httpx.Abort(http.StatusUnauthorized, "User not found")
	}
	return e.spendToUnits(ctx, userID)
}

func assetRank(category string) int {
	switch category {
	case "etf":
		return 0
	case "mutual_fund":
		return 1
	case "stock":
		return 2
	}
	return 3
}

// largestHoldingSymbol ranks the user's symbols ETF, fund, stock, other and
// then by cost basis. Swift grouped through a Dictionary, so ties came out in
// hash order; here the first symbol seen wins a tie.
func (e *Engine) largestHoldingSymbol(ctx context.Context, q *budgetingdb.Queries, userID uuid.UUID) (string, error) {
	rows, err := q.UserHoldings(ctx, userID)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	type agg struct {
		value float64
		rank  int
	}
	var order []string
	groups := map[string]*agg{}
	for _, r := range rows {
		g, ok := groups[r.Symbol]
		if !ok {
			g = &agg{rank: assetRank(r.Category)}
			groups[r.Symbol] = g
			order = append(order, r.Symbol)
		}
		g.value += r.Shares * r.BuyPrice
	}
	best := ""
	var bestAgg *agg
	for _, sym := range order {
		g := groups[sym]
		if g.value <= 0 {
			continue
		}
		// max(by:) keeps the current best unless it is "less" than the
		// candidate: a worse (higher) rank, or the same rank and lower value.
		if bestAgg == nil || bestAgg.rank > g.rank || (bestAgg.rank == g.rank && bestAgg.value < g.value) {
			best, bestAgg = sym, g
		}
	}
	return best, nil
}

// --- discipline --------------------------------------------------------------

func (e *Engine) discipline(ctx context.Context, userID uuid.UUID, through time.Time, months int) (budgetDisciplineSummary, error) {
	bounded := min(max(months, 1), 24)
	throughStart := monthStartOf(through)
	q := e.q(nil)
	results := make([]budgetDisciplineMonth, 0, bounded)
	for offset := bounded - 1; offset >= 0; offset-- {
		start := addMonths(throughStart, -offset)
		snap, err := snapshotNear(ctx, q, userID, start)
		if err != nil {
			return budgetDisciplineSummary{}, err
		}
		if snap == nil {
			results = append(results, budgetDisciplineMonth{MonthStart: dateString(start)})
			continue
		}
		drift, err := e.Dashboard(ctx, nil, userID, snap.ID)
		if err != nil {
			return budgetDisciplineSummary{}, err
		}
		positive := 0.0
		anyRed := false
		for _, c := range drift.Categories {
			if c.AllocationKind == kindExpense {
				positive += max(c.DriftAmount, 0)
			}
			if c.Level == levelRed {
				anyRed = true
			}
		}
		m := budgetDisciplineMonth{MonthStart: dateString(start)}
		if drift.TotalTarget > 0 {
			m.Score = ptr(max(0, 100*(1-positive/drift.TotalTarget)))
		}
		if start.Before(throughStart) {
			m.Compliant = ptr(drift.TotalLevel != levelRed && !anyRed)
		}
		results = append(results, m)
	}
	out := budgetDisciplineSummary{Months: results}
	if len(results) > 0 {
		out.CurrentScore = results[len(results)-1].Score
	}
	var completed []budgetDisciplineMonth
	for _, m := range results {
		if m.Compliant != nil {
			completed = append(completed, m)
		}
	}
	for i := len(completed) - 1; i >= 0; i-- {
		if !*completed[i].Compliant {
			break
		}
		out.CompletedMonthStreak++
	}
	for _, m := range completed {
		if *m.Compliant {
			out.CompliantMonths++
		}
	}
	out.EvaluatedMonths = len(completed)
	return out, nil
}

// --- reallocation ------------------------------------------------------------

type validatedAdjustment struct {
	item   budgetingdb.BudgetPlanItem
	amount float64
}

func errBudgetChanged() error {
	return httpx.Conflict("Budget changed. Refresh before reallocating.")
}

func (e *Engine) validateAdjustments(ctx context.Context, q *budgetingdb.Queries, values []reallocationAdjustment, snapshotID, userID uuid.UUID) ([]validatedAdjustment, error) {
	if len(values) == 0 {
		return nil, httpx.BadRequest("At least one adjustment is required.")
	}
	invalid := httpx.Unprocessable("Invalid reallocation adjustment.")
	seen := map[uuid.UUID]bool{}
	out := make([]validatedAdjustment, 0, len(values))
	for _, v := range values {
		if !finite(v.Amount) || v.Amount <= 0 {
			return nil, invalid
		}
		id, ok := parseUUID(v.PlanItemID)
		if !ok || seen[id] {
			return nil, invalid
		}
		seen[id] = true
		item, err := q.PlanItemInSnapshot(ctx, budgetingdb.PlanItemInSnapshotParams{ID: id, SnapshotID: snapshotID, UserID: userID})
		if noRows(err) {
			return nil, invalid
		}
		if err != nil {
			return nil, err
		}
		if item.AllocationKind != kindExpense || !item.ReallocationEligible || v.Amount > item.PlannedAmount {
			return nil, invalid
		}
		out = append(out, validatedAdjustment{item: item, amount: v.Amount})
	}
	return out, nil
}

// validUUIDString is UUID(uuidString:): only the 36-character hyphenated
// form parses (uuid.Parse also takes braces, URNs and bare hex).
func validUUIDString(s string) bool { return len(s) == 36 }

func parseUUID(s string) (uuid.UUID, bool) {
	if !validUUIDString(s) {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

func (e *Engine) validatedDestination(ctx context.Context, q *budgetingdb.Queries, userID uuid.UUID, goal, portfolio *string) (*budgetingdb.OwnedGoalRow, *uuid.UUID, error) {
	var requestedPortfolio *uuid.UUID
	if portfolio != nil {
		id, ok := parseUUID(*portfolio)
		if !ok {
			return nil, nil, httpx.BadRequest("Invalid portfolio.")
		}
		requestedPortfolio = &id
	}
	var goalRow *budgetingdb.OwnedGoalRow
	if goal != nil {
		id, ok := parseUUID(*goal)
		if !ok {
			return nil, nil, httpx.BadRequest("Invalid financial goal.")
		}
		g, err := q.OwnedGoal(ctx, budgetingdb.OwnedGoalParams{UserID: userID, ID: id})
		if noRows(err) {
			return nil, nil, httpx.BadRequest("Invalid financial goal.")
		}
		if err != nil {
			return nil, nil, err
		}
		goalRow = &g
	}
	if goalRow != nil && requestedPortfolio != nil && goalRow.PortfolioListID != *requestedPortfolio {
		return nil, nil, httpx.BadRequest("The financial goal does not belong to the selected portfolio.")
	}
	resolved := requestedPortfolio
	if resolved == nil && goalRow != nil {
		resolved = &goalRow.PortfolioListID
	}
	if resolved != nil {
		ok, err := q.OwnedPortfolioExists(ctx, budgetingdb.OwnedPortfolioExistsParams{ID: *resolved, UserID: userID})
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, httpx.BadRequest("Invalid portfolio.")
		}
	}
	return goalRow, resolved, nil
}

func investmentTarget(ctx context.Context, q *budgetingdb.Queries, userID uuid.UUID, snapshotID *uuid.UUID) (float64, error) {
	if snapshotID == nil {
		return 0, nil
	}
	items, err := q.PlanItemsForSnapshot(ctx, budgetingdb.PlanItemsForSnapshotParams{UserID: userID, SnapshotID: *snapshotID})
	if err != nil {
		return 0, err
	}
	total := 0.0
	for i := range items {
		if it := &items[i]; it.AllocationKind == kindInvestment {
			total += it.PlannedAmount
		}
	}
	return total, nil
}

func (e *Engine) preview(ctx context.Context, userID uuid.UUID, req previewRequest) (reallocationPreviewResponse, error) {
	q := e.q(nil)
	snapshotID, ok := parseUUID(req.SnapshotID)
	if !ok {
		return reallocationPreviewResponse{}, notFound()
	}
	snap, err := q.SnapshotForUser(ctx, budgetingdb.SnapshotForUserParams{ID: snapshotID, UserID: userID})
	if noRows(err) {
		return reallocationPreviewResponse{}, notFound()
	}
	if err != nil {
		return reallocationPreviewResponse{}, err
	}
	if int(snap.Revision) != req.ExpectedRevision {
		return reallocationPreviewResponse{}, errBudgetChanged()
	}
	validated, err := e.validateAdjustments(ctx, q, req.Adjustments, snapshotID, userID)
	if err != nil {
		return reallocationPreviewResponse{}, err
	}
	if _, _, err := e.validatedDestination(ctx, q, userID, req.FinancialGoalID, req.PortfolioListID); err != nil {
		return reallocationPreviewResponse{}, err
	}
	sum := 0.0
	for i := range validated {
		sum += validated[i].amount
	}
	cur := snap.CurrencyCode
	freed := roundedFor(sum, cur)
	nextMonth := addMonths(snap.MonthStart, 1)
	existing, err := snapshotNear(ctx, q, userID, nextMonth)
	if err != nil {
		return reallocationPreviewResponse{}, err
	}
	var existingID *uuid.UUID
	if existing != nil {
		existingID = &existing.ID
	}
	existingInvestment, err := investmentTarget(ctx, q, userID, existingID)
	if err != nil {
		return reallocationPreviewResponse{}, err
	}
	remaining := 0.0
	for i := range validated {
		remaining += validated[i].item.PlannedAmount - validated[i].amount
	}
	warnings := []string{}
	if remaining+existingInvestment > snap.NetSalary {
		warnings = append(warnings, "Planned allocations exceed monthly income.")
	}
	return reallocationPreviewResponse{
		EffectiveMonth:         dateString(nextMonth),
		FreedCapital:           freed,
		AnnualImpact:           roundedFor(freed*12, cur),
		InvestmentTargetBefore: existingInvestment,
		InvestmentTargetAfter:  roundedFor(existingInvestment+freed, cur),
		Warnings:               warnings,
	}, nil
}

func eventResponse(m budgetingdb.BudgetReallocationEvent) (reallocationEventResponse, error) {
	adj, err := decodeValues[reallocationAdjustment](m.Adjustments)
	if err != nil {
		return reallocationEventResponse{}, err
	}
	out := reallocationEventResponse{
		ID: uuidString(m.ID), RequestID: uuidString(m.RequestID),
		SourceSnapshotID: uuidString(m.SourceSnapshotID), TargetSnapshotID: uuidString(m.TargetSnapshotID),
		EffectiveMonth: dateString(m.EffectiveMonth), FreedCapital: m.FreedCapital,
		FinancialGoalID: optUUIDString(m.FinancialGoalID), PortfolioListID: optUUIDString(m.PortfolioListID),
		Adjustments: adj,
	}
	if m.CreatedAt != nil {
		out.CreatedAt = ptr(isoString(*m.CreatedAt))
	}
	return out, nil
}

func optEqual(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (e *Engine) commit(ctx context.Context, userID uuid.UUID, requestIDRaw string, req previewRequest) (reallocationEventResponse, error) {
	requestID, ok := parseUUID(requestIDRaw)
	if !ok {
		return reallocationEventResponse{}, httpx.BadRequest("requestId must be a UUID.")
	}
	byRequest := budgetingdb.ReallocationByRequestParams{UserID: userID, RequestID: requestID}
	if existing, err := e.q(nil).ReallocationByRequest(ctx, byRequest); err == nil {
		return eventResponse(existing)
	} else if !noRows(err) {
		return reallocationEventResponse{}, err
	}
	var out reallocationEventResponse
	err := e.tx(ctx, func(tx pgx.Tx) error {
		q := e.q(tx)
		if existing, err := q.ReallocationByRequest(ctx, byRequest); err == nil {
			out, err = eventResponse(existing)
			return err
		} else if !noRows(err) {
			return err
		}
		sourceID, ok := parseUUID(req.SnapshotID)
		if !ok {
			return notFound()
		}
		source, err := q.SnapshotForUser(ctx, budgetingdb.SnapshotForUserParams{ID: sourceID, UserID: userID})
		if noRows(err) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if int(source.Revision) != req.ExpectedRevision {
			return errBudgetChanged()
		}
		validated, err := e.validateAdjustments(ctx, q, req.Adjustments, sourceID, userID)
		if err != nil {
			return err
		}
		goal, portfolioID, err := e.validatedDestination(ctx, q, userID, req.FinancialGoalID, req.PortfolioListID)
		if err != nil {
			return err
		}
		now := e.now()
		effectiveMonth := addMonths(source.MonthStart, 1)
		target, err := e.cloneSnapshotIfNeeded(ctx, q, source, effectiveMonth, userID, now)
		if err != nil {
			return err
		}
		targetItems, err := q.PlanItemsForSnapshot(ctx, budgetingdb.PlanItemsForSnapshotParams{UserID: userID, SnapshotID: target.ID})
		if err != nil {
			return err
		}
		total := 0.0
		for ai := range validated {
			adj := &validated[ai]
			idx := -1
			for i := range targetItems {
				if ti := &targetItems[i]; (ti.CategoryID != nil && optEqual(ti.CategoryID, adj.item.CategoryID)) || strings.EqualFold(ti.Title, adj.item.Title) {
					idx = i
					break
				}
			}
			if idx < 0 {
				return httpx.Conflict("A target category no longer exists in the effective month.")
			}
			ti := &targetItems[idx]
			if ti.AllocationKind != kindExpense || ti.PlannedAmount < adj.amount {
				return httpx.Unprocessable("A reallocation exceeds its next-month target.")
			}
			ti.PlannedAmount = roundedFor(ti.PlannedAmount-adj.amount, target.CurrencyCode)
			ti.TargetType = targetFixed
			ti.IncomePercentage = nil
			if err := q.UpdatePlanItemFixedAmount(ctx, budgetingdb.UpdatePlanItemFixedAmountParams{
				ID: ti.ID, PlannedAmount: ti.PlannedAmount, UpdatedAt: &now,
			}); err != nil {
				return err
			}
			total += adj.amount
		}
		var goalID *uuid.UUID
		if goal != nil {
			goalID = &goal.ID
		}
		found := -1
		for i := range targetItems {
			if ti := &targetItems[i]; ti.AllocationKind == kindInvestment && optEqual(ti.DestinationFinancialGoalID, goalID) && optEqual(ti.DestinationPortfolioListID, portfolioID) {
				found = i
				break
			}
		}
		if found >= 0 {
			inv := targetItems[found]
			if err := q.UpdatePlanItemAmount(ctx, budgetingdb.UpdatePlanItemAmountParams{
				ID: inv.ID, PlannedAmount: roundedFor(inv.PlannedAmount+total, target.CurrencyCode), UpdatedAt: &now,
			}); err != nil {
				return err
			}
		} else {
			if err := q.InsertPlanItem(ctx, budgetingdb.InsertPlanItemParams{
				ID: uuid.New(), SnapshotID: target.ID, UserID: userID, Title: "Investments",
				PlannedAmount: roundedFor(total, target.CurrencyCode), Pillar: "futureYou",
				SplitMode: budgetingdb.ExpenseSplitModePersonal, UserSharePercent: 100, TargetType: targetFixed,
				AllocationKind: kindInvestment, DestinationFinancialGoalID: goalID,
				DestinationPortfolioListID: portfolioID, Now: &now,
			}); err != nil {
				return err
			}
		}
		if goal != nil {
			if err := q.UpdateGoalMonthlyContribution(ctx, budgetingdb.UpdateGoalMonthlyContributionParams{
				ID: goal.ID, MonthlyContribution: roundedFor(goal.MonthlyContribution+total, goal.BaseCurrency), UpdatedAt: &now,
			}); err != nil {
				return err
			}
		}
		if err := q.UpdateSnapshotRevision(ctx, budgetingdb.UpdateSnapshotRevisionParams{ID: target.ID, Revision: target.Revision + 1, UpdatedAt: &now}); err != nil {
			return err
		}
		if err := q.UpdateSnapshotRevision(ctx, budgetingdb.UpdateSnapshotRevisionParams{ID: source.ID, Revision: source.Revision + 1, UpdatedAt: &now}); err != nil {
			return err
		}
		adjustments, err := encodeValues(req.Adjustments)
		if err != nil {
			return err
		}
		event := budgetingdb.BudgetReallocationEvent{
			ID: uuid.New(), UserID: userID, RequestID: requestID, SourceSnapshotID: sourceID, TargetSnapshotID: target.ID,
			EffectiveMonth: effectiveMonth, FreedCapital: roundedFor(total, target.CurrencyCode),
			FinancialGoalID: goalID, PortfolioListID: portfolioID, Adjustments: adjustments, CreatedAt: &now,
		}
		if err := q.InsertReallocation(ctx, budgetingdb.InsertReallocationParams{
			ID: event.ID, UserID: userID, RequestID: requestID, SourceSnapshotID: sourceID,
			TargetSnapshotID: target.ID, EffectiveMonth: effectiveMonth, FreedCapital: event.FreedCapital,
			FinancialGoalID: goalID, PortfolioListID: portfolioID, Adjustments: adjustments, CreatedAt: &now,
		}); err != nil {
			return err
		}
		out, err = eventResponse(event)
		return err
	})
	return out, err
}

func (e *Engine) cloneSnapshotIfNeeded(ctx context.Context, q *budgetingdb.Queries, source budgetingdb.BudgetSnapshot, month time.Time, userID uuid.UUID, now time.Time) (budgetingdb.BudgetSnapshot, error) {
	existing, err := snapshotNear(ctx, q, userID, month)
	if err != nil {
		return budgetingdb.BudgetSnapshot{}, err
	}
	if existing != nil {
		return *existing, nil
	}
	clone := budgetingdb.BudgetSnapshot{
		ID: uuid.New(), UserID: userID, MonthStart: month, NetSalary: source.NetSalary, TargetShares: source.TargetShares,
		CurrencyCode: source.CurrencyCode, CategoryDriftThreshold: source.CategoryDriftThreshold,
		TotalDriftThreshold: source.TotalDriftThreshold, AlertsEnabled: source.AlertsEnabled,
		AlertOnUnbudgeted: source.AlertOnUnbudgeted, Revision: 0, CreatedAt: &now, UpdatedAt: &now,
	}
	if err := q.InsertSnapshot(ctx, budgetingdb.InsertSnapshotParams{
		ID: clone.ID, UserID: userID, MonthStart: month, NetSalary: clone.NetSalary, TargetShares: clone.TargetShares,
		CurrencyCode: clone.CurrencyCode, CategoryDriftThreshold: clone.CategoryDriftThreshold,
		TotalDriftThreshold: clone.TotalDriftThreshold, AlertsEnabled: clone.AlertsEnabled,
		AlertOnUnbudgeted: clone.AlertOnUnbudgeted, Revision: 0, Now: &now,
	}); err != nil {
		return budgetingdb.BudgetSnapshot{}, err
	}
	items, err := q.PlanItemsForSnapshot(ctx, budgetingdb.PlanItemsForSnapshotParams{UserID: userID, SnapshotID: source.ID})
	if err != nil {
		return budgetingdb.BudgetSnapshot{}, err
	}
	for i := range items {
		it := &items[i]
		if err := q.InsertPlanItem(ctx, budgetingdb.InsertPlanItemParams{
			ID: uuid.New(), SnapshotID: clone.ID, UserID: userID, Title: it.Title, PlannedAmount: it.PlannedAmount,
			Pillar: it.Pillar, SplitMode: it.SplitMode, UserSharePercent: it.UserSharePercent, CategoryID: it.CategoryID,
			TargetType: it.TargetType, IncomePercentage: it.IncomePercentage, ThresholdOverride: it.ThresholdOverride,
			AllocationKind: it.AllocationKind, ReallocationEligible: it.ReallocationEligible,
			DestinationFinancialGoalID: it.DestinationFinancialGoalID, DestinationPortfolioListID: it.DestinationPortfolioListID,
			Now: &now,
		}); err != nil {
			return budgetingdb.BudgetSnapshot{}, err
		}
	}
	return clone, nil
}

func (e *Engine) history(ctx context.Context, userID uuid.UUID) ([]reallocationEventResponse, error) {
	rows, err := e.q(nil).ReallocationHistory(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]reallocationEventResponse, 0, len(rows))
	for i := range rows {
		ev, err := eventResponse(rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// --- bulk update -------------------------------------------------------------

func (e *Engine) bulkUpdate(ctx context.Context, userID, snapshotID uuid.UUID, w bulkUpdateWire) (BudgetDriftDashboard, error) {
	err := e.tx(ctx, func(tx pgx.Tx) error {
		q := e.q(tx)
		snap, err := q.SnapshotForUser(ctx, budgetingdb.SnapshotForUserParams{ID: snapshotID, UserID: userID})
		if noRows(err) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if int(snap.Revision) != *w.ExpectedRevision {
			return httpx.Conflict("")
		}
		now := e.now()
		for _, u := range *w.Items {
			invalid := httpx.BadRequest("Invalid bulk budget item.")
			if !finite(*u.PlannedAmount) || *u.PlannedAmount < 0 {
				return invalid
			}
			id, ok := parseUUID(*u.ID)
			if !ok {
				return invalid
			}
			item, err := q.PlanItemInSnapshot(ctx, budgetingdb.PlanItemInSnapshotParams{ID: id, SnapshotID: snapshotID, UserID: userID})
			if noRows(err) {
				return invalid
			}
			if err != nil {
				return err
			}
			planned := *u.PlannedAmount
			if *u.TargetType == targetPercentageIncome {
				p := u.IncomePercentage
				if p == nil || !finite(*p) || *p < 0 || *p > 1000 {
					return httpx.BadRequest("Invalid income percentage.")
				}
				planned = snap.NetSalary * *p / 100
			}
			if err := q.UpdatePlanItemTargets(ctx, budgetingdb.UpdatePlanItemTargetsParams{
				ID: item.ID, PlannedAmount: planned, TargetType: *u.TargetType, IncomePercentage: u.IncomePercentage,
				ThresholdOverride: u.ThresholdOverride, ReallocationEligible: *u.ReallocationEligible, UpdatedAt: &now,
			}); err != nil {
				return err
			}
		}
		return q.UpdateSnapshotRevision(ctx, budgetingdb.UpdateSnapshotRevisionParams{ID: snap.ID, Revision: snap.Revision + 1, UpdatedAt: &now})
	})
	if err != nil {
		return BudgetDriftDashboard{}, err
	}
	snap, err := e.q(nil).SnapshotForUser(ctx, budgetingdb.SnapshotForUserParams{ID: snapshotID, UserID: userID})
	if noRows(err) {
		return BudgetDriftDashboard{}, notFound()
	}
	if err != nil {
		return BudgetDriftDashboard{}, err
	}
	if e.Evaluator != nil {
		if err := e.Evaluator.Evaluate(ctx, userID, snap.MonthStart, true); err != nil {
			return BudgetDriftDashboard{}, err
		}
	}
	return e.Dashboard(ctx, nil, userID, snapshotID)
}

// --- templates ---------------------------------------------------------------

func templateResponseFor(m budgetingdb.BudgetTemplate) (templateResponse, error) {
	items, err := decodeValues[templateItem](m.Items)
	if err != nil {
		return templateResponse{}, err
	}
	out := templateResponse{ID: uuidString(m.ID), Name: m.Name, Items: items}
	if m.CreatedAt != nil {
		out.CreatedAt = ptr(isoString(*m.CreatedAt))
	}
	if m.UpdatedAt != nil {
		out.UpdatedAt = ptr(isoString(*m.UpdatedAt))
	}
	return out, nil
}

func validateTemplate(name string, items []templateItem) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 80 || len(items) == 0 {
		return httpx.BadRequest("Template name and at least one target are required.")
	}
	for _, it := range items {
		if !finite(it.PlannedAmount) || it.PlannedAmount < 0 {
			return badRequest()
		}
	}
	return nil
}

func (e *Engine) listTemplates(ctx context.Context, userID uuid.UUID) ([]templateResponse, error) {
	rows, err := e.q(nil).TemplatesForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]templateResponse, 0, len(rows))
	for _, r := range rows {
		t, err := templateResponseFor(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (e *Engine) createTemplate(ctx context.Context, userID uuid.UUID, name string, items []templateItem) (templateResponse, error) {
	if err := validateTemplate(name, items); err != nil {
		return templateResponse{}, err
	}
	payload, err := encodeValues(items)
	if err != nil {
		return templateResponse{}, err
	}
	now := e.now()
	m := budgetingdb.BudgetTemplate{ID: uuid.New(), UserID: userID, Name: strings.TrimSpace(name), Items: payload, CreatedAt: &now, UpdatedAt: &now}
	if err := e.q(nil).InsertTemplate(ctx, budgetingdb.InsertTemplateParams{ID: m.ID, UserID: userID, Name: m.Name, Items: payload, Now: &now}); err != nil {
		return templateResponse{}, err
	}
	return templateResponseFor(m)
}

func (e *Engine) updateTemplate(ctx context.Context, userID, id uuid.UUID, name string, items []templateItem) (templateResponse, error) {
	if err := validateTemplate(name, items); err != nil {
		return templateResponse{}, err
	}
	q := e.q(nil)
	m, err := q.TemplateForUser(ctx, budgetingdb.TemplateForUserParams{ID: id, UserID: userID})
	if noRows(err) {
		return templateResponse{}, notFound()
	}
	if err != nil {
		return templateResponse{}, err
	}
	payload, err := encodeValues(items)
	if err != nil {
		return templateResponse{}, err
	}
	now := e.now()
	m.Name, m.Items, m.UpdatedAt = strings.TrimSpace(name), payload, &now
	if err := q.UpdateTemplate(ctx, budgetingdb.UpdateTemplateParams{ID: m.ID, Name: m.Name, Items: payload, UpdatedAt: &now}); err != nil {
		return templateResponse{}, err
	}
	return templateResponseFor(m)
}

func (e *Engine) deleteTemplate(ctx context.Context, userID, id uuid.UUID) error {
	q := e.q(nil)
	m, err := q.TemplateForUser(ctx, budgetingdb.TemplateForUserParams{ID: id, UserID: userID})
	if noRows(err) {
		return notFound()
	}
	if err != nil {
		return err
	}
	return q.DeleteTemplate(ctx, m.ID)
}

func (e *Engine) applyTemplate(ctx context.Context, userID, id uuid.UUID, w applyWire) (BudgetDriftDashboard, error) {
	snapshotID, ok := parseUUID(*w.SnapshotID)
	if !ok {
		return BudgetDriftDashboard{}, badRequest()
	}
	err := e.tx(ctx, func(tx pgx.Tx) error {
		q := e.q(tx)
		tmpl, err := q.TemplateForUser(ctx, budgetingdb.TemplateForUserParams{ID: id, UserID: userID})
		if noRows(err) {
			return notFound()
		}
		if err != nil {
			return err
		}
		snap, err := q.SnapshotForUser(ctx, budgetingdb.SnapshotForUserParams{ID: snapshotID, UserID: userID})
		if noRows(err) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if int(snap.Revision) != *w.ExpectedRevision {
			return httpx.Conflict("")
		}
		existing, err := q.PlanItemsBySnapshot(ctx, snapshotID)
		if err != nil {
			return err
		}
		if len(existing) > 0 && !*w.ReplaceExisting {
			return httpx.Conflict("Snapshot already has targets.")
		}
		if *w.ReplaceExisting && len(existing) > 0 {
			ids := make([]uuid.UUID, len(existing))
			for i := range existing {
				ids[i] = existing[i].ID
			}
			if err := q.DeletePlanItems(ctx, ids); err != nil {
				return err
			}
		}
		items, err := decodeValues[templateItem](tmpl.Items)
		if err != nil {
			return err
		}
		now := e.now()
		for _, v := range items {
			planned := v.PlannedAmount
			if v.TargetType == targetPercentageIncome {
				pct := 0.0
				if v.IncomePercentage != nil {
					pct = *v.IncomePercentage
				}
				planned = snap.NetSalary * pct / 100
			}
			var category *uuid.UUID
			if v.CategoryID != nil {
				if cid, ok := parseUUID(*v.CategoryID); ok {
					owned, err := q.OwnedCategoryID(ctx, budgetingdb.OwnedCategoryIDParams{ID: cid, UserID: userID})
					if err == nil {
						category = &owned
					} else if !noRows(err) {
						return err
					}
				}
			}
			if err := q.InsertPlanItem(ctx, budgetingdb.InsertPlanItemParams{
				ID: uuid.New(), SnapshotID: snapshotID, UserID: userID, Title: v.Title, PlannedAmount: planned,
				Pillar: v.Pillar, SplitMode: budgetingdb.ExpenseSplitModePersonal, UserSharePercent: 100,
				CategoryID: category, TargetType: v.TargetType, IncomePercentage: v.IncomePercentage,
				ThresholdOverride: v.ThresholdOverride, AllocationKind: v.AllocationKind,
				ReallocationEligible: v.ReallocationEligible, Now: &now,
			}); err != nil {
				return err
			}
		}
		return q.UpdateSnapshotRevision(ctx, budgetingdb.UpdateSnapshotRevisionParams{ID: snap.ID, Revision: snap.Revision + 1, UpdatedAt: &now})
	})
	if err != nil {
		return BudgetDriftDashboard{}, err
	}
	return e.Dashboard(ctx, nil, userID, snapshotID)
}

// --- alert policy ------------------------------------------------------------

func (e *Engine) updatePolicy(ctx context.Context, userID, snapshotID uuid.UUID, w alertPolicyWire) (snapshotResponse, error) {
	cat, total := *w.CategoryThreshold, *w.TotalThreshold
	if !finite(cat) || !finite(total) || cat < 0 || cat > 1000 || total < 0 || total > 1000 {
		return snapshotResponse{}, badRequest()
	}
	q := e.q(nil)
	snap, err := q.SnapshotForUser(ctx, budgetingdb.SnapshotForUserParams{ID: snapshotID, UserID: userID})
	if noRows(err) {
		return snapshotResponse{}, notFound()
	}
	if err != nil {
		return snapshotResponse{}, err
	}
	now := e.now()
	snap.Revision++
	if err := q.UpdateSnapshotPolicy(ctx, budgetingdb.UpdateSnapshotPolicyParams{
		ID: snap.ID, CategoryDriftThreshold: cat, TotalDriftThreshold: total,
		AlertsEnabled: *w.AlertsEnabled, AlertOnUnbudgeted: *w.AlertOnUnbudgeted, Revision: snap.Revision, UpdatedAt: &now,
	}); err != nil {
		return snapshotResponse{}, err
	}
	shares := map[string]float64{}
	if err := json.Unmarshal(snap.TargetShares, &shares); err != nil {
		return snapshotResponse{}, err
	}
	if shares == nil {
		shares = map[string]float64{}
	}
	return snapshotResponse{
		ID: uuidString(snapshotID), MonthStart: dateString(snap.MonthStart), NetSalary: snap.NetSalary,
		TargetShares: shares, CurrencyCode: snap.CurrencyCode, CategoryDriftThreshold: cat,
		TotalDriftThreshold: total, AlertsEnabled: *w.AlertsEnabled, AlertOnUnbudgeted: *w.AlertOnUnbudgeted,
		Revision: int(snap.Revision),
	}, nil
}
