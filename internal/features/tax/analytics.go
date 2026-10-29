package tax

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
)

type dragInput struct {
	accountIDs        []uuid.UUID
	taxYear           int
	profile           ProfileRequest
	pack              rulePack
	realizedTax       dec.Decimal
	embeddedLiability dec.Decimal
	positions         []taxdb.Position
}

func firstRate(rates ...*dec.Decimal) *dec.Decimal {
	for _, r := range rates {
		if r != nil {
			return r
		}
	}
	return nil
}

func rateOr(r *dec.Decimal, def dec.Decimal) dec.Decimal {
	if r == nil {
		return def
	}
	return *r
}

func positionValue(p *taxdb.Position) float64 {
	price := p.AverageCost
	if p.LastPrice != nil {
		price = *p.LastPrice
	}
	return p.Quantity * price
}

// dragProjection is TaxDragAnalytics.projection.
func (s *service) dragProjection(ctx context.Context, in dragInput) (DragProjection, error) {
	start, end := startOfYearUTC(in.taxYear), startOfYearUTC(in.taxYear+1)
	var dividends []taxdb.Dividend
	if len(in.accountIDs) > 0 {
		var err error
		dividends, err = s.q.DividendsPaidInRange(ctx, taxdb.DividendsPaidInRangeParams{AccountIds: in.accountIDs, Start: start, EndAt: end})
		if err != nil {
			return DragProjection{}, err
		}
	}
	income := zero
	for idx := range dividends {
		d := &dividends[idx]
		income = income.Add(fd(max(0, d.Amount)))
	}
	incomeRate := dmax(zero, rateOr(firstRate(in.profile.MarginalIncomeTaxRate, in.profile.ShortTermCapitalGainsRate,
		in.pack.rate(false, in.profile)), zero))
	dividendTax := income.Mul(incomeRate)
	factor := s.annualizationFactor(in.taxYear, start)
	projectedRealized := in.realizedTax.Mul(factor)
	projectedIncome := dividendTax.Mul(factor)
	projected := projectedRealized.Add(projectedIncome)
	value := zero
	for idx := range in.positions {
		p := &in.positions[idx]
		value = value.Add(fd(max(0, positionValue(p))))
	}
	var ratio *dec.Decimal
	if value.Sign() > 0 {
		ratio = decPtr(projected.Div(value))
	}
	c := in.profile.ReportingCurrency
	incomeQuality := "estimated"
	if len(dividends) == 0 {
		incomeQuality = "incomplete"
	}
	return DragProjection{
		YearToDateTax: money(in.realizedTax.Add(dividendTax), c), ProjectedYearEndTax: money(projected, c),
		AnnualTaxDrag: money(projected, c), TaxCostRatio: ratio,
		Components: []DragComponent{
			{ID: "realized-capital-gains", Label: "Realized capital gains", YearToDate: money(in.realizedTax, c), ProjectedYearEnd: money(projectedRealized, c), DataQuality: "verified"},
			{ID: "investment-income", Label: "Dividends and distributions", YearToDate: money(dividendTax, c), ProjectedYearEnd: money(projectedIncome, c), DataQuality: incomeQuality},
			{ID: "embedded-liability", Label: "Embedded unrealized liability", YearToDate: money(in.embeddedLiability, c), ProjectedYearEnd: money(in.embeddedLiability, c), DataQuality: "estimated"},
		},
	}, nil
}

func (s *service) annualizationFactor(taxYear int, start time.Time) dec.Decimal {
	now := s.now()
	current := now.UTC().Year()
	if taxYear != current {
		if taxYear < current {
			return di(1)
		}
		return di(0)
	}
	elapsed := max(1, daysBetween(start, now))
	return dmin(di(4), di(365).Div(di(elapsed)))
}

type positionedAsset struct {
	account    taxdb.Account
	position   taxdb.Position
	instrument taxdb.Instrument
	value      dec.Decimal
	costRate   dec.Decimal
}

func wrapperOf(a taxdb.Account) (Wrapper, bool) {
	if a.TaxWrapper == nil {
		return "", false
	}
	return parseWrapper(*a.TaxWrapper)
}

// locationOpportunities is TaxAssetLocationEngine.opportunities.
func (s *service) locationOpportunities(accounts []taxdb.Account, positions []taxdb.Position, instruments map[uuid.UUID]taxdb.Instrument,
	lots []taxdb.Lot, profile ProfileRequest, pack rulePack,
) []LocationOpportunity {
	accountByID := map[uuid.UUID]taxdb.Account{}
	for idx := range accounts {
		a := &accounts[idx]
		accountByID[a.ID] = *a
	}
	var assets []positionedAsset
	for idx := range positions {
		p := &positions[idx]
		account, ok := accountByID[p.AccountID]
		if !ok {
			continue
		}
		in, ok := instruments[p.InstrumentID]
		if !ok {
			continue
		}
		t := "stock"
		if in.InstrumentType != nil {
			t = *in.InstrumentType
		}
		entry, ok := s.catalog.efficiencyEntry(t)
		if !ok {
			continue
		}
		value := fd(max(0, positionValue(p)))
		if !(value.Sign() > 0) {
			continue
		}
		assets = append(assets, positionedAsset{account: account, position: *p, instrument: in, value: value, costRate: annualTaxCostRate(entry, profile, pack)})
	}
	var taxable, advantaged []positionedAsset
	for idx := range assets {
		a := &assets[idx]
		w, ok := wrapperOf(a.account)
		if ok && w == WrapperTaxable {
			taxable = append(taxable, *a)
		}
		if ok && w != WrapperTaxable && w != WrapperUnknown {
			advantaged = append(advantaged, *a)
		}
	}
	sort.SliceStable(taxable, func(a, b int) bool { return taxable[a].costRate.Cmp(taxable[b].costRate) > 0 })
	sort.SliceStable(advantaged, func(a, b int) bool { return advantaged[a].costRate.Cmp(advantaged[b].costRate) < 0 })
	out := []LocationOpportunity{}
	if len(taxable) == 0 || len(advantaged) == 0 {
		return out
	}
	high, low := taxable[0], advantaged[0]
	if !strings.EqualFold(high.position.Currency, low.position.Currency) {
		return out
	}
	notional := dmin(high.value, low.value)
	savings := dmax(zero, high.costRate.Sub(low.costRate).Mul(notional))
	if !(savings.Sign() > 0) {
		return out
	}
	immediate := s.realizedTaxCost(high, notional, lots, profile, pack)
	breakEven := 0
	if immediate.Sign() > 0 {
		breakEven = int(math.Ceil(dd(immediate.Div(savings).Mul(di(12)))))
	}
	currency := profile.ReportingCurrency
	support := EstimateOnly
	if pack.isValidated {
		support = Supported
	}
	leg := func(account, instrument uuid.UUID, symbol, side string) LocationLeg {
		return LocationLeg{
			ID: uuidString(uuid.New()), AccountID: uuidString(account), InstrumentID: uuidString(instrument),
			Symbol: symbol, Side: side, Notional: money(notional, currency),
		}
	}
	var legs []LocationLeg
	warnings := []string{}
	hs, ls := high.instrument.Symbol, low.instrument.Symbol
	if breakEven <= 36 {
		legs = []LocationLeg{
			leg(high.account.ID, high.instrument.ID, hs, "sell"),
			leg(low.account.ID, high.instrument.ID, hs, "buy"),
			leg(low.account.ID, low.instrument.ID, ls, "sell"),
			leg(high.account.ID, low.instrument.ID, ls, "buy"),
		}
	} else {
		legs = []LocationLeg{leg(low.account.ID, high.instrument.ID, hs, "future_contribution")}
		warnings = append(warnings, "The estimated realization cost exceeds a 36-month break-even; direct future contributions are preferred.")
	}
	if support != Supported {
		warnings = append(warnings, "Asset-location savings are estimate-only until this jurisdiction's rule pack is validated.")
	}
	confidence := lit("0.55")
	if support == Supported {
		confidence = lit("0.85")
	}
	return append(out, LocationOpportunity{
		ID: "location:" + uuidString(high.instrument.ID) + ":" + uuidString(low.instrument.ID) + ":" +
			uuidString(high.account.ID) + ":" + uuidString(low.account.ID),
		SupportLevel: support, Title: "Improve the location of " + hs,
		AnnualSavings: money(savings, currency), ImmediateTaxCost: money(immediate, currency),
		BreakEvenMonths: intPtr(breakEven), Confidence: confidence, Legs: legs, Warnings: warnings,
	})
}

func annualTaxCostRate(e efficiencyEntry, profile ProfileRequest, pack rulePack) dec.Decimal {
	ordinary := rateOr(firstRate(profile.MarginalIncomeTaxRate, profile.ShortTermCapitalGainsRate, pack.rate(false, profile)), zero)
	capital := rateOr(firstRate(profile.LongTermCapitalGainsRate, pack.rate(true, profile)), ordinary)
	income := e.OrdinaryIncomeShare.Mul(ordinary).Add(di(1).Sub(e.OrdinaryIncomeShare).Mul(capital))
	return e.ExpectedYield.Mul(income).Add(e.Turnover.Mul(capital))
}

func (s *service) realizedTaxCost(asset positionedAsset, notional dec.Decimal, lots []taxdb.Lot, profile ProfileRequest, pack rulePack) dec.Decimal {
	price := asset.position.AverageCost
	if asset.position.LastPrice != nil {
		price = *asset.position.LastPrice
	}
	if !(price > 0) {
		return zero
	}
	remaining := dmin(fd(asset.position.Quantity), notional.Div(fd(price)))
	var matching []taxdb.Lot
	for idx := range lots {
		l := &lots[idx]
		if l.AccountID == asset.position.AccountID && l.InstrumentID == asset.position.InstrumentID {
			matching = append(matching, *l)
		}
	}
	sort.SliceStable(matching, func(a, b int) bool { return matching[a].OpenDate.Before(matching[b].OpenDate) })
	taxable := zero
	now := s.now()
	for idx := range matching {
		l := &matching[idx]
		if !(remaining.Sign() > 0) {
			continue
		}
		sold := dmin(remaining, fd(l.RemainingQuantity))
		gain := sold.Mul(fd(price).Sub(fd(l.OpenPrice)))
		if gain.Sign() > 0 {
			isLong := timeIntervalSince(now, l.OpenDate) > yearSeconds
			taxable = taxable.Add(gain.Mul(rateOr(pack.rate(isLong, profile), zero)))
		}
		remaining = remaining.Sub(sold)
	}
	return dmax(zero, taxable)
}

// Goal impacts (TaxGoalImpactCalculator), on PlanningEngine (StockPlanShared).

func monthlyRate(annual float64) float64 { return math.Pow(1+annual, 1.0/12.0) - 1 }

func futureValue(principal, contribution, annual float64, months int, growth float64) float64 {
	if months <= 0 {
		return principal
	}
	rate := monthlyRate(annual)
	if !(math.Abs(growth) > 0.000000001) {
		if !(math.Abs(rate) > 0.000000001) {
			return principal + contribution*float64(months)
		}
		g := math.Pow(1+rate, float64(months))
		return principal*g + contribution*((g-1)/rate)
	}
	balance := principal
	for m := 1; m <= months; m++ {
		balance *= 1 + rate
		balance += contribution
		if m%12 == 0 {
			contribution *= 1 + growth
		}
	}
	return balance
}

func inflatedTarget(target, inflation float64, months int) float64 {
	if months <= 0 || !(math.Abs(inflation) > 0.000000001) {
		return target
	}
	return target * math.Pow(1+inflation, float64(months)/12)
}

func monthsToTarget(principal, target, contribution, annual, growth, inflation float64) *int {
	if principal >= inflatedTarget(target, inflation, 0) {
		return intPtr(0)
	}
	if !(contribution > 0) && !(annual > 0) {
		return nil
	}
	for m := 1; m <= 1200; m++ {
		if futureValue(principal, contribution, annual, m, growth) >= inflatedTarget(target, inflation, m) {
			return intPtr(m)
		}
	}
	return nil
}

// addMonths is Calendar.date(byAdding: .month): the day clamps to the end of
// a shorter month.
func addMonths(t time.Time, n int) time.Time {
	t = t.UTC()
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(d, last)-1)
}

func (s *service) goalImpacts(ctx context.Context, userID uuid.UUID, portfolios map[uuid.UUID]bool, benefit dec.Decimal, currency string) ([]GoalImpact, error) {
	out := []GoalImpact{}
	if !(benefit.Sign() > 0) || len(portfolios) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(portfolios))
	for id := range portfolios {
		ids = append(ids, id)
	}
	allocations, err := s.q.GoalAllocationsForPortfolios(ctx, taxdb.GoalAllocationsForPortfoliosParams{UserID: userID, PortfolioIds: ids})
	if err != nil {
		return nil, err
	}
	byGoal := map[uuid.UUID][]taxdb.FinancialGoalPortfolioAllocation{}
	goalIDs := []uuid.UUID{}
	for _, a := range allocations {
		if _, ok := byGoal[a.GoalID]; !ok {
			goalIDs = append(goalIDs, a.GoalID)
		}
		byGoal[a.GoalID] = append(byGoal[a.GoalID], a)
	}
	if len(goalIDs) == 0 {
		return out, nil
	}
	goals, err := s.q.ActiveGoalsByIDs(ctx, taxdb.ActiveGoalsByIDsParams{Ids: goalIDs, UserID: userID})
	if err != nil {
		return nil, err
	}
	snapshots, err := s.q.GoalSnapshotsNewestFirst(ctx, taxdb.GoalSnapshotsNewestFirstParams{UserID: userID, GoalIds: goalIDs})
	if err != nil {
		return nil, err
	}
	latest := map[uuid.UUID]float64{}
	for _, snap := range snapshots {
		if _, ok := latest[snap.GoalID]; !ok {
			latest[snap.GoalID] = snap.CurrentValue
		}
	}
	now := s.now()
	completion := func(months *int) *string {
		if months == nil {
			return nil
		}
		return strPtr(isoDate(addMonths(now, *months)))
	}
	for idx := range goals {
		g := &goals[idx]
		allocation := 0.0
		for _, a := range byGoal[g.ID] {
			allocation += a.AllocationPercentage / 100
		}
		applied := benefit.Mul(fd(min(1, max(0, allocation))))
		principal := g.StartingCapital
		if v, ok := latest[g.ID]; ok {
			principal = v
		}
		baseline := monthsToTarget(principal, g.TargetAmount, g.MonthlyContribution, g.ExpectedAnnualReturn, g.AnnualContributionGrowth, g.InflationAssumption)
		improved := monthsToTarget(principal+dd(applied), g.TargetAmount, g.MonthlyContribution, g.ExpectedAnnualReturn, g.AnnualContributionGrowth, g.InflationAssumption)
		var sooner *int
		if baseline != nil && improved != nil {
			sooner = intPtr(max(0, *baseline-*improved))
		}
		out = append(out, GoalImpact{
			GoalID: uuidString(g.ID), GoalName: g.Name, Currency: currency, BenefitApplied: applied,
			BaselineCompletionDate: completion(baseline), ProjectedCompletionDate: completion(improved),
			MonthsSooner: sooner, Assumption: "Assumes the after-cost tax benefit is immediately reinvested and earns the goal's configured return.",
		})
	}
	return out, nil
}

// allocationImpacts is TaxAllocationImpactCalculator.impacts.
func (s *service) allocationImpacts(ctx context.Context, userID uuid.UUID, opportunities []OpportunityResponse, replacements map[string]ReplacementCandidate) ([]AllocationImpact, error) {
	out := []AllocationImpact{}
	accounts, err := s.q.AccountsForUser(ctx, userID)
	if err != nil || len(accounts) == 0 {
		return out, err
	}
	accountIDs := make([]uuid.UUID, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		accountIDs[i] = a.ID
	}
	positions, err := s.q.PositionsForAccounts(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	instrumentSet := map[uuid.UUID]bool{}
	for idx := range positions {
		p := &positions[idx]
		instrumentSet[p.InstrumentID] = true
	}
	for k := range replacements {
		r := replacements[k]
		if id, ok := parseUUID(r.InstrumentID); ok {
			instrumentSet[id] = true
		}
	}
	symbols := map[uuid.UUID]string{}
	if len(instrumentSet) > 0 {
		ids := make([]uuid.UUID, 0, len(instrumentSet))
		for id := range instrumentSet {
			ids = append(ids, id)
		}
		rows, err := s.q.InstrumentsByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for idx := range rows {
			in := &rows[idx]
			symbols[in.ID] = in.Symbol
		}
	}
	byPortfolio := map[string][]OpportunityResponse{}
	keys := []string{}
	for idx := range opportunities {
		o := &opportunities[idx]
		k := ""
		if o.PortfolioID != nil {
			k = *o.PortfolioID
		}
		if _, ok := byPortfolio[k]; !ok {
			keys = append(keys, k)
		}
		byPortfolio[k] = append(byPortfolio[k], *o)
	}
	sort.Strings(keys)
	for _, raw := range keys {
		portfolioID, ok := parseUUID(raw)
		if raw == "" || !ok {
			continue
		}
		selected := byPortfolio[raw]
		currency := selected[0].MarketValue.Currency
		portfolioAccounts := map[uuid.UUID]bool{}
		for idx := range accounts {
			a := &accounts[idx]
			if a.PortfolioID != nil && *a.PortfolioID == portfolioID {
				portfolioAccounts[a.ID] = true
			}
		}
		before := map[uuid.UUID]dec.Decimal{}
		get := func(m map[uuid.UUID]dec.Decimal, id uuid.UUID) dec.Decimal {
			if v, ok := m[id]; ok {
				return v
			}
			return zero
		}
		for idx := range positions {
			p := &positions[idx]
			if !portfolioAccounts[p.AccountID] || p.Currency != currency {
				continue
			}
			v := positionValue(p)
			if p.MarketValue != nil {
				v = *p.MarketValue
			}
			before[p.InstrumentID] = get(before, p.InstrumentID).Add(fd(max(0, v)))
		}
		total := zero
		for _, v := range before {
			total = total.Add(v)
		}
		if !(total.Sign() > 0) {
			continue
		}
		after := map[uuid.UUID]dec.Decimal{}
		for k, v := range before {
			after[k] = v
		}
		affected := map[uuid.UUID]bool{}
		for idx := range selected {
			o := &selected[idx]
			sourceID, ok := parseUUID(o.InstrumentID)
			if !ok {
				continue
			}
			available := get(after, sourceID)
			notional := dmin(dmax(zero, o.MarketValue.Amount), available)
			if !(notional.Sign() > 0) {
				continue
			}
			after[sourceID] = available.Sub(notional)
			affected[sourceID] = true
			if r, ok := replacements[o.ID]; ok {
				if rid, ok := parseUUID(r.InstrumentID); ok {
					after[rid] = get(after, rid).Add(notional)
					affected[rid] = true
				}
			}
		}
		changes := []AllocationChange{}
		for id := range affected {
			sym, ok := symbols[id]
			if !ok {
				continue
			}
			changes = append(changes, AllocationChange{
				InstrumentID: uuidString(id), Symbol: sym,
				BeforeWeight: get(before, id).Div(total), AfterWeight: get(after, id).Div(total),
			})
		}
		sort.SliceStable(changes, func(a, b int) bool {
			if changes[a].Symbol != changes[b].Symbol {
				return changes[a].Symbol < changes[b].Symbol
			}
			return changes[a].InstrumentID < changes[b].InstrumentID
		})
		if len(changes) == 0 {
			continue
		}
		maxChange := changes[0].AfterWeight.Sub(changes[0].BeforeWeight).Abs()
		for _, c := range changes[1:] {
			if d := c.AfterWeight.Sub(c.BeforeWeight).Abs(); d.Cmp(maxChange) > 0 {
				maxChange = d
			}
		}
		out = append(out, AllocationImpact{PortfolioID: raw, PortfolioValue: money(total, currency), MaximumWeightChange: maxChange, Changes: changes})
	}
	return out, nil
}

// Rebalancing drafts (TaxRebalancingDraftBridge). The simulation JSON is the
// shared RebalancingSimulation, read back by the rebalancing domain.

type rebalanceTrade struct {
	Symbol                     string   `json:"symbol"`
	Side                       string   `json:"side"`
	Quantity                   float64  `json:"quantity"`
	Price                      float64  `json:"price"`
	Notional                   float64  `json:"notional"`
	EstimatedFee               float64  `json:"estimatedFee"`
	Currency                   string   `json:"currency"`
	AccountID                  *string  `json:"accountId,omitempty"`
	InstrumentID               *string  `json:"instrumentId,omitempty"`
	SelectedLotIDs             []string `json:"selectedLotIds"`
	SourceOpportunityID        *string  `json:"sourceOpportunityId,omitempty"`
	ReplacementForInstrumentID *string  `json:"replacementForInstrumentId,omitempty"`
}

type valuationWarning struct {
	Code    string  `json:"code"`
	Symbol  *string `json:"symbol,omitempty"`
	Message string  `json:"message"`
}

type rebalancingSimulation struct {
	PortfolioID               string             `json:"portfolioId"`
	ModelID                   string             `json:"modelId"`
	ModelRevision             int                `json:"modelRevision"`
	BaseCurrency              string             `json:"baseCurrency"`
	TotalValueBefore          float64            `json:"totalValueBefore"`
	TotalValueAfter           float64            `json:"totalValueAfter"`
	DriftBeforeBasisPoints    int                `json:"driftBeforeBasisPoints"`
	DriftAfterBasisPoints     int                `json:"driftAfterBasisPoints"`
	EstimatedFees             float64            `json:"estimatedFees"`
	EstimatedRealizedGainLoss float64            `json:"estimatedRealizedGainLoss"`
	Trades                    []rebalanceTrade   `json:"trades"`
	Before                    []struct{}         `json:"before"`
	After                     []struct{}         `json:"after"`
	Warnings                  []valuationWarning `json:"warnings"`
}

func (s *service) createDrafts(ctx context.Context, userID, planID uuid.UUID, kind string, legs []ActionLeg) ([]uuid.UUID, error) {
	grouped := map[string][]ActionLeg{}
	order := []string{}
	for idx := range legs {
		l := &legs[idx]
		if l.Side == "future_contribution" || l.PortfolioID == nil {
			continue
		}
		if _, ok := grouped[*l.PortfolioID]; !ok {
			order = append(order, *l.PortfolioID)
		}
		grouped[*l.PortfolioID] = append(grouped[*l.PortfolioID], *l)
	}
	ids := []uuid.UUID{}
	for _, raw := range order {
		portfolioID, ok := parseUUID(raw)
		if !ok {
			continue
		}
		model, err := s.q.ActiveAllocationModel(ctx, taxdb.ActiveAllocationModelParams{PortfolioID: portfolioID, CreatedByUserID: userID})
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		portfolioLegs := grouped[raw]
		var accountIDs []uuid.UUID
		for idx := range portfolioLegs {
			l := &portfolioLegs[idx]
			if id, ok := parseUUID(l.AccountID); ok {
				accountIDs = append(accountIDs, id)
			}
		}
		total := 0.0
		if len(accountIDs) > 0 {
			positions, err := s.q.PositionsForAccounts(ctx, accountIDs)
			if err != nil {
				return nil, err
			}
			for idx := range positions {
				p := &positions[idx]
				v := positionValue(p)
				if p.MarketValue != nil {
					v = *p.MarketValue
				}
				total += max(0, v)
			}
		}
		var firstSell *string
		for idx := range portfolioLegs {
			l := &portfolioLegs[idx]
			if l.Side == "sell" {
				firstSell = strPtr(l.InstrumentID)
				break
			}
		}
		trades := make([]rebalanceTrade, len(portfolioLegs))
		fees := 0.0
		warnings := []valuationWarning{{
			Code:    "tax_strategy_draft",
			Message: "This draft preserves the accepted tax-strategy legs. Verify current quotes, allocation, and tax treatment before execution.",
		}}
		for i := range portfolioLegs {
			l := &portfolioLegs[i]
			notional := dd(l.Notional.Amount)
			qty := 0.0
			if l.Quantity != nil {
				qty = dd(*l.Quantity)
			}
			price := 0.0
			if qty > 0 {
				price = notional / qty
			}
			side := "buy"
			if l.Side == "sell" {
				side = "sell"
			}
			var replacementFor *string
			if l.Side == "buy" {
				replacementFor = firstSell
			}
			var source *string
			if len(l.LotIDs) > 0 {
				source = strPtr(l.LotIDs[0])
			}
			trades[i] = rebalanceTrade{
				Symbol: l.Symbol, Side: side, Quantity: qty, Price: price, Notional: notional,
				EstimatedFee: notional * 0.001, Currency: l.Notional.Currency, AccountID: strPtr(l.AccountID),
				InstrumentID: strPtr(l.InstrumentID), SelectedLotIDs: l.LotIDs, SourceOpportunityID: source,
				ReplacementForInstrumentID: replacementFor,
			}
			fees += trades[i].EstimatedFee
			if l.Quantity == nil {
				warnings = append(warnings, valuationWarning{
					Code: "quote_required", Symbol: strPtr(l.Symbol),
					Message: "A reliable current quote is required to calculate quantity; the accepted notional is preserved.",
				})
			}
		}
		sim := rebalancingSimulation{
			PortfolioID: uuidString(portfolioID), ModelID: uuidString(model.ID), ModelRevision: int(model.Revision),
			BaseCurrency: model.BaseCurrency, TotalValueBefore: total, TotalValueAfter: total - fees,
			EstimatedFees: fees, Trades: trades, Before: []struct{}{}, After: []struct{}{}, Warnings: warnings,
		}
		raw, err := encodeSorted(sim)
		if err != nil {
			return nil, err
		}
		name := "Asset-location draft"
		if kind == "harvest" {
			name = "Tax-loss harvesting draft"
		}
		id := uuid.New()
		now := s.now()
		if err := s.q.InsertRebalancePlanDraft(ctx, taxdb.InsertRebalancePlanDraftParams{
			ID: id, PortfolioID: portfolioID, ModelID: model.ID, CreatedByUserID: userID, ModelRevision: model.Revision,
			Name: &name, BaseCurrency: model.BaseCurrency, TotalValue: total, EstimatedFees: fees,
			SimulationJson: raw, Now: nowPtr(now),
		}); err != nil {
			return nil, err
		}
		if err := s.q.InsertRebalancingLink(ctx, taxdb.InsertRebalancingLinkParams{
			ID: uuid.New(), ActionPlanID: planID, RebalancingPlanID: id, Now: nowPtr(now),
		}); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
