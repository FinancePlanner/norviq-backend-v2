package tax

import (
	"context"
	"math"
	"sort"
	"time"

	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
)

const yearSeconds = 365 * 86400

func (s *service) emptyDashboard(j Jurisdiction, taxYear int, currency string) DashboardResponse {
	z := money(zero, currency)
	return DashboardResponse{
		GeneratedAt: isoDate(s.now()), TaxYear: taxYear, Jurisdiction: j,
		RuleVersion: s.rules.pack(j).ruleVersion, ProfileComplete: false,
		Summary: ProjectionSummary{
			RealizedEstimatedLiability: z, EmbeddedUnrealizedLiability: z, HarvestableLosses: z,
			EstimatedNetBenefit: z, ShortTermCarryover: z, LongTermCarryover: z,
		},
		Opportunities: []OpportunityResponse{}, UnsupportedValue: z,
		Assumptions: []string{"Complete a tax profile and classify investment accounts to enable calculations."},
		Disclaimer:  taxDisclaimer,
	}
}

// priceQuality is taxPriceQuality.
func priceQuality(p *taxdb.Position, now time.Time) string {
	if p == nil || p.LastPrice == nil || p.LastPriceDate == nil {
		return "missing"
	}
	if timeIntervalSince(now, *p.LastPriceDate) <= 86400 {
		return "fresh"
	}
	return "stale"
}

func (s *service) dashboard(ctx context.Context, userID uuid.UUID, j Jurisdiction, taxYear int) (DashboardResponse, error) {
	profileRow, err := s.profileRow(ctx, userID, j, taxYear)
	if err != nil {
		return DashboardResponse{}, err
	}
	if profileRow == nil {
		return s.emptyDashboard(j, taxYear, defaultCurrency(j)), nil
	}
	var profile ProfileRequest
	if err := decodeStored(profileRow.ProfileJson, &profile); err != nil {
		return DashboardResponse{}, err
	}
	pack := s.rules.pack(j)
	accounts, err := s.q.AccountsForUser(ctx, userID)
	if err != nil {
		return DashboardResponse{}, err
	}
	accountIDs := make([]uuid.UUID, len(accounts))
	accountsByID := map[uuid.UUID]taxdb.Account{}
	for i := range accounts {
		a := &accounts[i]
		accountIDs[i] = a.ID
		accountsByID[a.ID] = *a
	}
	var lots []taxdb.Lot
	var positions []taxdb.Position
	if len(accountIDs) > 0 {
		if lots, err = s.q.OpenLotsForAccounts(ctx, accountIDs); err != nil {
			return DashboardResponse{}, err
		}
		if positions, err = s.q.PositionsForAccounts(ctx, accountIDs); err != nil {
			return DashboardResponse{}, err
		}
	}
	adjustmentByLot := map[uuid.UUID]float64{}
	if len(lots) > 0 {
		lotIDs := make([]uuid.UUID, len(lots))
		for i := range lots {
			l := &lots[i]
			lotIDs[i] = l.ID
		}
		adjustments, err := s.q.LotAdjustmentsForLots(ctx, lotIDs)
		if err != nil {
			return DashboardResponse{}, err
		}
		for idx := range adjustments {
			a := &adjustments[idx]
			adjustmentByLot[a.LotID] += a.Amount
		}
	}
	decisions, err := s.q.DecisionsForYear(ctx, taxdb.DecisionsForYearParams{UserID: userID, TaxYear: int64(taxYear)})
	if err != nil {
		return DashboardResponse{}, err
	}
	decisionByOpportunity := map[string]taxdb.TaxOpportunityDecision{}
	for _, d := range decisions {
		decisionByOpportunity[d.OpportunityID] = d
	}
	instrumentSet := map[uuid.UUID]bool{}
	for idx := range lots {
		l := &lots[idx]
		instrumentSet[l.InstrumentID] = true
	}
	for idx := range positions {
		p := &positions[idx]
		instrumentSet[p.InstrumentID] = true
	}
	instrumentsByID := map[uuid.UUID]taxdb.Instrument{}
	if len(instrumentSet) > 0 {
		ids := make([]uuid.UUID, 0, len(instrumentSet))
		for id := range instrumentSet {
			ids = append(ids, id)
		}
		rows, err := s.q.InstrumentsByIDs(ctx, ids)
		if err != nil {
			return DashboardResponse{}, err
		}
		for idx := range rows {
			in := &rows[idx]
			instrumentsByID[in.ID] = *in
		}
	}
	positionsByKey := map[string]*taxdb.Position{}
	for i := range positions {
		p := &positions[i]
		positionsByKey[positionKey(p.AccountID, p.InstrumentID)] = p
	}

	opportunities := []OpportunityResponse{}
	embedded, harvestable, estimatedBenefit, unsupported := zero, zero, zero, zero
	now := s.now()
	currency := profile.ReportingCurrency

	for idx := range lots {
		lot := &lots[idx]
		if !(lot.RemainingQuantity > 0) {
			continue
		}
		instrument, ok := instrumentsByID[lot.InstrumentID]
		if !ok {
			continue
		}
		account, ok := accountsByID[lot.AccountID]
		if !ok {
			continue
		}
		wrapper := WrapperUnknown
		if account.TaxWrapper != nil {
			if w, ok := parseWrapper(*account.TaxWrapper); ok {
				wrapper = w
			}
		}
		instrumentType := "stock"
		if instrument.InstrumentType != nil {
			instrumentType = *instrument.InstrumentType
		}
		support := pack.supportLevel(instrumentType, wrapper)
		position := positionsByKey[positionKey(lot.AccountID, lot.InstrumentID)]
		quality := priceQuality(position, now)
		price := lot.OpenPrice
		if position != nil {
			price = position.AverageCost
			if position.LastPrice != nil {
				price = *position.LastPrice
			}
		}
		marketValue := fd(price * lot.RemainingQuantity)
		basis := fd(lot.OpenPrice*lot.RemainingQuantity + adjustmentByLot[lot.ID])
		gain := marketValue.Sub(basis)
		taxableGain := gain
		isGermanFund := pack.jurisdiction == DE && isFundType(&instrumentType)
		if isGermanFund {
			adjusted, ok := zero, false
			if instrument.FundClassification != nil && oneOf(*instrument.FundClassification, fundClasses) {
				adjusted, ok = fundTaxableAmount(gain, *instrument.FundClassification)
			}
			if ok {
				taxableGain = adjusted
				support = EstimateOnly
			} else {
				support = ProfessionalReview
			}
		}
		isLongTerm := timeIntervalSince(now, lot.OpenDate) > yearSeconds
		if support != Supported && support != EstimateOnly {
			unsupported = unsupported.Add(marketValue)
			continue
		}
		rate := pack.rate(isLongTerm, profile)
		if rate == nil {
			unsupported = unsupported.Add(marketValue)
			continue
		}
		if gain.Sign() >= 0 {
			embedded = embedded.Add(taxableGain.Mul(*rate))
			continue
		}
		loss := gain.Neg()
		benefit := dmax(zero, taxableGain.Neg()).Mul(*rate)
		recent, err := s.hasRecentReplacement(ctx, userID, instrument, addDays(now, -30), lot.OpenTransactionID)
		if err != nil {
			return DashboardResponse{}, err
		}
		actionable := support == Supported && !recent && profileRow.IsComplete && quality == "fresh"
		costs := marketValue.Mul(lit("0.001"))
		afterCost := dmax(zero, benefit.Sub(costs))
		opportunityID := uuidString(lot.ID)
		decision, hasDecision := decisionByOpportunity[opportunityID]
		improved := hasDecision && afterCost.Cmp(fd(decision.EstimatedBenefit).Mul(lit("1.25"))) >= 0
		var status string
		switch {
		case hasDecision && decision.Status == "dismissed" && !improved:
			status = "dismissed"
		case hasDecision && decision.Status == "accepted":
			status = "accepted"
		case actionable:
			status = "actionable"
		case recent:
			status = "blocked"
		default:
			status = "watch"
		}
		warnings := []string{}
		if recent {
			warnings = append(warnings, "A substantially identical acquisition was detected in the prior 30 days. Review wash-sale treatment.")
		}
		if support != Supported {
			warnings = append(warnings, "This rule pack is estimate-only until professional validation is enabled.")
		}
		if isGermanFund {
			warnings = append(warnings, "The estimate applies the configured German fund partial exemption to both gains and losses.")
		}
		if quality != "fresh" {
			warnings = append(warnings, "A fresh market price is required before this opportunity can become actionable.")
		}
		replacements := []ReplacementCandidate{}
		if s.v2 {
			if replacements, err = s.replacementCandidates(ctx, instrument, j); err != nil {
				return DashboardResponse{}, err
			}
		}
		effective := status
		if status == "actionable" && s.v2 && len(replacements) == 0 {
			effective = "watch"
		}
		if s.v2 && len(replacements) == 0 {
			warnings = append(warnings, "No advisor-reviewed replacement is available in catalog "+s.catalog.replacementVersion+".")
		}
		holding := "short_term"
		if isLongTerm {
			holding = "long_term"
		}
		dataQuality := "estimated"
		if quality == "fresh" {
			dataQuality = "verified"
		}
		lotDetail := LotDetail{
			ID: opportunityID, OpenedAt: isoDate(lot.OpenDate), EligibleQuantity: fd(lot.RemainingQuantity),
			UnitBasis: money(fd(lot.OpenPrice), lot.Currency), AdjustedBasis: money(basis, lot.Currency),
			MarketValue: money(marketValue, currency), UnrealizedGainLoss: money(gain, currency),
			HoldingPeriod: holding, DataQuality: dataQuality,
		}
		var washEnds *string
		if recent {
			washEnds = strPtr(isoDate(addDays(now, 31)))
		}
		confidence := lit("0.50")
		if support == Supported && quality == "fresh" {
			confidence = lit("0.95")
		}
		var pricedAt *string
		if position != nil && position.LastPriceDate != nil {
			pricedAt = strPtr(isoDate(*position.LastPriceDate))
		}
		opportunities = append(opportunities, OpportunityResponse{
			ID: opportunityID, AccountID: uuidString(lot.AccountID), InstrumentID: uuidString(lot.InstrumentID),
			Symbol: instrument.Symbol, InstrumentType: instrumentType, Status: effective, SupportLevel: support,
			MarketValue: money(marketValue, currency), UnrealizedLoss: money(loss, currency),
			EstimatedTaxBenefit: money(benefit, currency), EligibleQuantity: fd(lot.RemainingQuantity),
			HoldingPeriod: holding, WashSaleWindowEndsAt: washEnds, Warnings: warnings, Confidence: confidence,
			PortfolioID: uuidPtrString(account.PortfolioID), PriceQuality: strPtr(quality), PricedAt: pricedAt,
			Lots: []LotDetail{lotDetail}, ReplacementCandidates: replacements,
			CurrentYearTaxReduction: moneyPtr(benefit, currency), DeferredTaxLiability: moneyPtr(benefit, currency),
			EstimatedTransactionCosts: moneyPtr(costs, currency), EstimatedAfterCostBenefit: moneyPtr(afterCost, currency),
		})
		harvestable = harvestable.Add(loss)
		if effective == "actionable" {
			estimatedBenefit = estimatedBenefit.Add(afterCost)
		}
	}

	realized, err := s.realizedLiability(ctx, accountIDs, taxYear, profile, pack)
	if err != nil {
		return DashboardResponse{}, err
	}
	var drag *DragProjection
	var location *[]LocationOpportunity
	var catalogVersion *string
	if s.v2 {
		d, err := s.dragProjection(ctx, dragInput{
			accountIDs: accountIDs, taxYear: taxYear, profile: profile, pack: pack,
			realizedTax: realized, embeddedLiability: embedded, positions: positions,
		})
		if err != nil {
			return DashboardResponse{}, err
		}
		drag = &d
		l := s.locationOpportunities(accounts, positions, instrumentsByID, lots, profile, pack)
		location = &l
		catalogVersion = strPtr(s.catalog.replacementVersion)
	}
	sort.SliceStable(opportunities, func(a, b int) bool {
		return opportunities[a].EstimatedTaxBenefit.Amount.Cmp(opportunities[b].EstimatedTaxBenefit.Amount) > 0
	})
	var ratio *dec.Decimal
	if drag != nil {
		ratio = drag.TaxCostRatio
	}
	response := DashboardResponse{
		GeneratedAt: isoDate(now), TaxYear: taxYear, Jurisdiction: j, RuleVersion: pack.ruleVersion,
		IsStale: false, ProfileComplete: profileRow.IsComplete,
		Summary: ProjectionSummary{
			RealizedEstimatedLiability:  money(realized, currency),
			EmbeddedUnrealizedLiability: money(embedded, currency),
			HarvestableLosses:           money(harvestable, currency),
			EstimatedNetBenefit:         money(estimatedBenefit, currency),
			ShortTermCarryover:          money(profile.PriorShortTermLossCarryover, currency),
			LongTermCarryover:           money(profile.PriorLongTermLossCarryover, currency),
			TaxCostRatio:                ratio,
		},
		Opportunities: opportunities, UnsupportedValue: money(unsupported, currency),
		Assumptions: pack.assumptions(taxYear), Disclaimer: taxDisclaimer,
		CatalogVersion: catalogVersion, TaxDrag: drag, LocationOpportunities: location,
	}
	if err := s.storeSnapshot(ctx, response, userID, profileRow.ID); err != nil {
		return DashboardResponse{}, err
	}
	return response, nil
}

func (s *service) storeSnapshot(ctx context.Context, r DashboardResponse, userID, profileID uuid.UUID) error {
	raw, err := encodeSorted(r)
	if err != nil {
		return err
	}
	now := s.now()
	return s.q.InsertProjectionSnapshot(ctx, taxdb.InsertProjectionSnapshotParams{
		ID: uuid.New(), UserID: userID, ProfileID: profileID, TaxYear: int64(r.TaxYear),
		Jurisdiction: string(r.Jurisdiction), RuleVersion: r.RuleVersion, Status: "ready",
		ResponseJson: raw, InputHash: sha256Hex(raw), GeneratedAt: now, Now: now,
	})
}

func (s *service) hasRecentReplacement(ctx context.Context, userID uuid.UUID, instrument taxdb.Instrument, since time.Time, excluding *uuid.UUID) (bool, error) {
	accounts, err := s.q.AccountsForUserExceptBroker(ctx, taxdb.AccountsForUserExceptBrokerParams{UserID: userID, Broker: pilotBroker})
	if err != nil || len(accounts) == 0 {
		return false, err
	}
	accountIDs := make([]uuid.UUID, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		accountIDs[i] = a.ID
	}
	matching := []uuid.UUID{instrument.ID}
	if instrument.TaxIdentityGroup != nil {
		if matching, err = s.q.InstrumentIDsByTaxIdentityGroup(ctx, instrument.TaxIdentityGroup); err != nil {
			return false, err
		}
	}
	txs, err := s.q.RecentBuys(ctx, taxdb.RecentBuysParams{AccountIds: accountIDs, InstrumentIds: matching, Since: since})
	if err != nil {
		return false, err
	}
	for idx := range txs {
		t := &txs[idx]
		if (excluding == nil || t.ID != *excluding) && math.Abs(f64(t.Quantity)) > 0.0000001 {
			return true, nil
		}
	}
	return false, nil
}

// realizedLiability is TaxService.realizedLiability.
func (s *service) realizedLiability(ctx context.Context, accountIDs []uuid.UUID, taxYear int, profile ProfileRequest, pack rulePack) (dec.Decimal, error) {
	if len(accountIDs) == 0 {
		return zero, nil
	}
	start, end := startOfYearUTC(taxYear), startOfYearUTC(taxYear+1)
	if profile.Jurisdiction == PT {
		return s.portugalRealized(ctx, accountIDs, taxYear, start, end, profile, pack)
	}
	if pack.jurisdiction == DE {
		return s.germanyRealized(ctx, accountIDs, taxYear, start, end, profile, pack)
	}
	lots, err := s.q.LotsClosedInRange(ctx, taxdb.LotsClosedInRangeParams{AccountIds: accountIDs, Start: start, EndAt: end})
	if err != nil {
		return zero, err
	}
	total := zero
	for idx := range lots {
		l := &lots[idx]
		if l.RealizedPnl == nil || !(*l.RealizedPnl > 0) || l.CloseDate == nil {
			continue
		}
		rate := pack.rate(timeIntervalSince(*l.CloseDate, l.OpenDate) > yearSeconds, profile)
		if rate == nil {
			continue
		}
		total = total.Add(fd(*l.RealizedPnl).Mul(*rate))
	}
	return total, nil
}

func (s *service) portugalRealized(ctx context.Context, accountIDs []uuid.UUID, taxYear int, start, end time.Time, profile ProfileRequest, pack rulePack) (dec.Decimal, error) {
	owner, err := s.q.FirstAccountByIDs(ctx, accountIDs)
	if isNoRows(err) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	sourceLots, err := s.q.LotsForAccounts(ctx, accountIDs)
	if err != nil {
		return zero, err
	}
	lotByID := map[uuid.UUID]taxdb.Lot{}
	lotIDs := []uuid.UUID{}
	for idx := range sourceLots {
		l := &sourceLots[idx]
		if _, ok := lotByID[l.ID]; !ok {
			lotIDs = append(lotIDs, l.ID)
		}
		lotByID[l.ID] = *l
	}
	if len(lotIDs) == 0 {
		return zero, nil
	}
	disposals, err := s.q.DisposalsForLots(ctx, lotIDs)
	if err != nil {
		return zero, err
	}
	txSet := map[uuid.UUID]bool{}
	txIDs := []uuid.UUID{}
	for _, d := range disposals {
		if !txSet[d.TransactionID] {
			txSet[d.TransactionID] = true
			txIDs = append(txIDs, d.TransactionID)
		}
	}
	txByID := map[uuid.UUID]taxdb.Transaction{}
	if len(txIDs) > 0 {
		txs, err := s.q.TransactionsByIDsInRange(ctx, taxdb.TransactionsByIDsInRangeParams{Ids: txIDs, Start: start, EndAt: end})
		if err != nil {
			return zero, err
		}
		for idx := range txs {
			t := &txs[idx]
			txByID[t.ID] = *t
		}
	}
	positions := []portugalPosition{}
	for _, d := range disposals {
		lot, ok := lotByID[d.LotID]
		if !ok {
			continue
		}
		t, ok := txByID[d.TransactionID]
		if !ok {
			continue
		}
		positions = append(positions, portugalPosition{realized: fd(d.RealizedPnl), holdingDays: max(0, daysBetween(lot.OpenDate, t.TradeDate))})
	}
	ledger := ptLedger{s}
	persisted, err := ledger.available(ctx, owner.UserID, taxYear)
	if err != nil {
		return zero, err
	}
	manual := dmax(zero, profile.PriorShortTermLossCarryover.Add(profile.PriorLongTermLossCarryover))
	marginal := portugalAutonomous
	if profile.MarginalIncomeTaxRate != nil {
		marginal = *profile.MarginalIncomeTaxRate
	}
	result := portugalCalculate(positions, profile.EstimatedTaxableIncome, marginal, profile.CapitalGainsTaxationMode, dmax(persisted, manual))
	if err := ledger.reconcile(ctx, owner.UserID, taxYear, profile.ReportingCurrency, pack.ruleVersion, result); err != nil {
		return zero, err
	}
	return result.estimatedTax, nil
}

func (s *service) germanyRealized(ctx context.Context, accountIDs []uuid.UUID, taxYear int, start, end time.Time, profile ProfileRequest, pack rulePack) (dec.Decimal, error) {
	owner, err := s.q.FirstAccountByIDs(ctx, accountIDs)
	if isNoRows(err) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	txs, err := s.q.SellTransactionsInRange(ctx, taxdb.SellTransactionsInRangeParams{AccountIds: accountIDs, Start: start, EndAt: end})
	if err != nil {
		return zero, err
	}
	instrumentSet := map[uuid.UUID]bool{}
	ids := []uuid.UUID{}
	for idx := range txs {
		t := &txs[idx]
		if !instrumentSet[t.InstrumentID] {
			instrumentSet[t.InstrumentID] = true
			ids = append(ids, t.InstrumentID)
		}
	}
	var instruments []taxdb.Instrument
	if len(ids) > 0 {
		if instruments, err = s.q.InstrumentsByIDs(ctx, ids); err != nil {
			return zero, err
		}
	}
	stockIDs := map[uuid.UUID]bool{}
	fundClass := map[uuid.UUID]string{}
	for idx := range instruments {
		in := &instruments[idx]
		if oneOf(lower(in.InstrumentType), []string{"stock", "equity"}) {
			stockIDs[in.ID] = true
		}
		if isFundType(in.InstrumentType) && in.FundClassification != nil && oneOf(*in.FundClassification, fundClasses) {
			if _, ok := fundExemptionRate(*in.FundClassification); ok {
				fundClass[in.ID] = *in.FundClassification
			}
		}
	}
	var stockTxIDs, fundTxIDs []uuid.UUID
	txByID := map[uuid.UUID]taxdb.Transaction{}
	for idx := range txs {
		t := &txs[idx]
		txByID[t.ID] = *t
		if stockIDs[t.InstrumentID] {
			stockTxIDs = append(stockTxIDs, t.ID)
		}
		if _, ok := fundClass[t.InstrumentID]; ok {
			fundTxIDs = append(fundTxIDs, t.ID)
		}
	}
	annual := zero
	if len(stockTxIDs) > 0 {
		disposals, err := s.q.DisposalsByTransactionIDs(ctx, stockTxIDs)
		if err != nil {
			return zero, err
		}
		for _, d := range disposals {
			annual = annual.Add(fd(d.RealizedPnl))
		}
	}
	stock, err := germanyLedger{s, stockLossTables}.reconcile(ctx, owner.UserID, taxYear, annual, pack.ruleVersion)
	if err != nil {
		return zero, err
	}
	var fundDisposals []taxdb.LotDisposal
	if len(fundTxIDs) > 0 {
		if fundDisposals, err = s.q.DisposalsByTransactionIDs(ctx, fundTxIDs); err != nil {
			return zero, err
		}
	}
	disposalIDs := map[uuid.UUID]bool{}
	for _, d := range fundDisposals {
		disposalIDs[d.ID] = true
	}
	advance, err := s.reconcileFundAdvances(ctx, disposalIDs)
	if err != nil {
		return zero, err
	}
	fundResult := zero
	for _, d := range fundDisposals {
		t, ok := txByID[d.TransactionID]
		if !ok {
			continue
		}
		class, ok := fundClass[t.InstrumentID]
		if !ok {
			continue
		}
		a := zero
		if v, ok := advance[d.ID]; ok {
			a = v
		}
		if adjusted, ok := fundTaxableAmount(fd(d.RealizedPnl).Sub(a), class); ok {
			fundResult = fundResult.Add(adjusted)
		}
	}
	general, err := germanyLedger{s, generalLossTables}.reconcile(ctx, owner.UserID, taxYear, stock.taxableGain.Add(fundResult), pack.ruleVersion)
	if err != nil {
		return zero, err
	}
	return germanyEstimatedTax(general.taxableGain, profile.RemainingCapitalIncomeAllowance, profile.ChurchTaxRate), nil
}
