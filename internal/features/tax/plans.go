package tax

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
)

func (s *service) createScenario(ctx context.Context, userID uuid.UUID, req ScenarioRequest, j Jurisdiction) (ScenarioResponse, error) {
	dashboard, err := s.dashboard(ctx, userID, j, req.TaxYear)
	if err != nil {
		return ScenarioResponse{}, err
	}
	requested := map[string]bool{}
	for _, id := range req.OpportunityIDs {
		requested[id] = true
	}
	var selected []OpportunityResponse
	for idx := range dashboard.Opportunities {
		o := &dashboard.Opportunities[idx]
		if requested[o.ID] {
			selected = append(selected, *o)
		}
	}
	if len(selected) == 0 {
		return ScenarioResponse{}, httpx.Unprocessable("Select at least one available tax opportunity.")
	}
	replacements := map[string]ReplacementCandidate{}
	for idx := range selected {
		o := &selected[idx]
		if len(o.ReplacementCandidates) == 0 {
			continue
		}
		want, ok := req.PlannedReplacementInstrumentIDs[o.ID]
		found := false
		if ok {
			for idx := range o.ReplacementCandidates {
				c := &o.ReplacementCandidates[idx]
				if c.InstrumentID == want {
					replacements[o.ID], found = *c, true
					break
				}
			}
		}
		if !found {
			return ScenarioResponse{}, httpx.Unprocessable("Choose an advisor-reviewed replacement for " + o.Symbol + ".")
		}
	}
	benefit, losses, fees, deferred := zero, zero, zero, zero
	portfolios := map[uuid.UUID]bool{}
	warnings := []string{}
	for idx := range selected {
		o := &selected[idx]
		if o.CurrentYearTaxReduction != nil {
			benefit = benefit.Add(o.CurrentYearTaxReduction.Amount)
		} else {
			benefit = benefit.Add(o.EstimatedTaxBenefit.Amount)
		}
		losses = losses.Add(o.UnrealizedLoss.Amount)
		if o.EstimatedTransactionCosts != nil {
			fees = fees.Add(o.EstimatedTransactionCosts.Amount)
		} else {
			fees = fees.Add(o.MarketValue.Amount.Mul(lit("0.001")))
		}
		if o.DeferredTaxLiability != nil {
			deferred = deferred.Add(o.DeferredTaxLiability.Amount)
		}
		if o.PortfolioID != nil {
			if id, ok := parseUUID(*o.PortfolioID); ok {
				portfolios[id] = true
			}
		}
		warnings = append(warnings, o.Warnings...)
	}
	sum := dashboard.Summary
	currency := sum.EstimatedNetBenefit.Currency
	baselineTax := sum.RealizedEstimatedLiability.Amount.Add(sum.EmbeddedUnrealizedLiability.Amount)
	goals, err := s.goalImpacts(ctx, userID, portfolios, dmax(zero, benefit.Sub(fees)), currency)
	if err != nil {
		return ScenarioResponse{}, err
	}
	allocations, err := s.allocationImpacts(ctx, userID, selected, replacements)
	if err != nil {
		return ScenarioResponse{}, err
	}
	id := uuid.New()
	response := ScenarioResponse{
		ID: uuidString(id), CreatedAt: isoDate(s.now()),
		Baseline: ScenarioColumn{
			CurrentYearTax: money(baselineTax, currency), NextYearTax: money(sum.EmbeddedUnrealizedLiability.Amount, currency),
			RealizedLosses: money(zero, currency),
			Carryover:      money(sum.ShortTermCarryover.Amount.Add(sum.LongTermCarryover.Amount), currency),
			FeesAndSpread:  money(zero, currency),
		},
		HarvestNow: ScenarioColumn{
			CurrentYearTax: money(dmax(zero, baselineTax.Sub(benefit)), currency),
			NextYearTax:    money(sum.EmbeddedUnrealizedLiability.Amount, currency),
			RealizedLosses: money(losses, currency), Carryover: money(dmax(zero, losses.Sub(baselineTax)), currency),
			FeesAndSpread: money(fees, currency),
		},
		EstimatedNetBenefit: money(dmax(zero, benefit.Sub(fees)), currency),
		Warnings:            warnings,
		Assumptions: append(append([]string{}, dashboard.Assumptions...),
			"Allocation impacts use current same-currency position values and assume replacement purchases equal each proposed sale notional."),
		CurrentYearTaxReduction:   moneyPtr(benefit, currency),
		DeferredTaxLiability:      moneyPtr(deferred, currency),
		EstimatedTransactionCosts: moneyPtr(fees, currency),
		GoalImpacts:               &goals, SelectedReplacements: &replacements, AllocationImpacts: &allocations,
	}
	profile, err := s.profileRow(ctx, userID, j, req.TaxYear)
	if err != nil {
		return ScenarioResponse{}, err
	}
	if profile == nil {
		return ScenarioResponse{}, httpx.Unprocessable("Complete a tax profile first.")
	}
	if err := s.insertScenario(ctx, id, userID, profile.ID, "harvest", req, response); err != nil {
		return ScenarioResponse{}, err
	}
	return response, nil
}

func (s *service) insertScenario(ctx context.Context, id, userID, profileID uuid.UUID, kind string, req, resp any) error {
	reqJSON, err := encodeSorted(req)
	if err != nil {
		return err
	}
	respJSON, err := encodeSorted(resp)
	if err != nil {
		return err
	}
	return s.q.InsertScenario(ctx, taxdb.InsertScenarioParams{
		ID: id, UserID: userID, ProfileID: profileID, Kind: kind, RequestJson: reqJSON, ResponseJson: respJSON, Now: s.now(),
	})
}

func (s *service) scenario(ctx context.Context, userID, id uuid.UUID) (*ScenarioResponse, error) {
	row, err := s.q.ScenarioForUser(ctx, taxdb.ScenarioForUserParams{ID: id, UserID: userID})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var resp ScenarioResponse
	if err := decodeStored(row.ResponseJson, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *service) existingPlan(ctx context.Context, userID uuid.UUID, key string) (*ActionPlanResponse, error) {
	row, err := s.q.ActionPlanByIdempotencyKey(ctx, taxdb.ActionPlanByIdempotencyKeyParams{UserID: userID, IdempotencyKey: key})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	resp, err := s.actionPlanResponse(ctx, row)
	return &resp, err
}

func (s *service) createActionPlan(ctx context.Context, userID uuid.UUID, req ActionPlanRequest) (ActionPlanResponse, error) {
	if existing, err := s.existingPlan(ctx, userID, req.IdempotencyKey); err != nil || existing != nil {
		if existing == nil {
			return ActionPlanResponse{}, err
		}
		return *existing, err
	}
	notFound := httpx.NotFound("Tax scenario not found.")
	scenarioID, ok := parseUUID(req.ScenarioID)
	if !ok {
		// Deliberate fix: Vapor answered 404 for a malformed id.
		return ActionPlanResponse{}, httpx.BadRequest("Invalid tax scenario id.")
	}
	scenario, err := s.q.ScenarioForUser(ctx, taxdb.ScenarioForUserParams{ID: scenarioID, UserID: userID})
	if isNoRows(err) {
		return ActionPlanResponse{}, notFound
	}
	if err != nil {
		return ActionPlanResponse{}, err
	}
	if scenario.Kind != "harvest" {
		return ActionPlanResponse{}, httpx.Unprocessable("Use the placement-plan endpoint for an asset-location scenario.")
	}
	var scenarioReq ScenarioRequest
	var scenarioResp ScenarioResponse
	if err := decodeStored(scenario.RequestJson, &scenarioReq); err != nil {
		return ActionPlanResponse{}, err
	}
	if err := decodeStored(scenario.ResponseJson, &scenarioResp); err != nil {
		return ActionPlanResponse{}, err
	}
	profile, err := s.q.ProfileByID(ctx, scenario.ProfileID)
	if isNoRows(err) || (err == nil && !Jurisdiction(profile.Jurisdiction).valid()) {
		return ActionPlanResponse{}, httpx.Unprocessable("The tax profile for this scenario is no longer available.")
	}
	if err != nil {
		return ActionPlanResponse{}, err
	}
	current, err := s.dashboard(ctx, userID, Jurisdiction(profile.Jurisdiction), scenarioReq.TaxYear)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	selectedIDs := map[string]bool{}
	for _, id := range scenarioReq.OpportunityIDs {
		selectedIDs[id] = true
	}
	benefitByID := map[string]dec.Decimal{}
	count, allActionable := 0, true
	for idx := range current.Opportunities {
		o := &current.Opportunities[idx]
		if !selectedIDs[o.ID] {
			continue
		}
		count++
		if o.Status != "actionable" || o.SupportLevel != Supported {
			allActionable = false
		}
		b := o.EstimatedTaxBenefit.Amount
		if o.EstimatedAfterCostBenefit != nil {
			b = o.EstimatedAfterCostBenefit.Amount
		}
		benefitByID[o.ID] = b
	}
	if count != len(selectedIDs) || !allActionable {
		return ActionPlanResponse{}, httpx.Unprocessable("One or more harvesting opportunities are no longer actionable. Run the simulator again with current prices and tax rules.")
	}
	var lotIDs []uuid.UUID
	for _, id := range scenarioReq.OpportunityIDs {
		if u, ok := parseUUID(id); ok {
			lotIDs = append(lotIDs, u)
		}
	}
	lotByID := map[uuid.UUID]taxdb.Lot{}
	if len(lotIDs) > 0 {
		lots, err := s.q.LotsByIDs(ctx, lotIDs)
		if err != nil {
			return ActionPlanResponse{}, err
		}
		for idx := range lots {
			l := &lots[idx]
			lotByID[l.ID] = *l
		}
	}
	accountSet := map[uuid.UUID]bool{}
	var accountIDs []uuid.UUID
	instrumentSet := map[uuid.UUID]bool{}
	for idx := range lotByID {
		l := lotByID[idx]
		if !accountSet[l.AccountID] {
			accountSet[l.AccountID] = true
			accountIDs = append(accountIDs, l.AccountID)
		}
		instrumentSet[l.InstrumentID] = true
	}
	accountByID := map[uuid.UUID]taxdb.Account{}
	if len(accountIDs) > 0 {
		accounts, err := s.q.AccountsByIDsForUser(ctx, taxdb.AccountsByIDsForUserParams{UserID: userID, Ids: accountIDs})
		if err != nil {
			return ActionPlanResponse{}, err
		}
		for idx := range accounts {
			a := &accounts[idx]
			accountByID[a.ID] = *a
		}
	}
	if len(accountByID) != len(accountIDs) {
		return ActionPlanResponse{}, httpx.Abort(http.StatusForbidden, "A selected tax lot is no longer available to this user.")
	}
	if scenarioResp.SelectedReplacements != nil {
		for k := range *scenarioResp.SelectedReplacements {
			c := (*scenarioResp.SelectedReplacements)[k]
			if id, ok := parseUUID(c.InstrumentID); ok {
				instrumentSet[id] = true
			}
		}
	}
	instrumentByID := map[uuid.UUID]taxdb.Instrument{}
	if len(instrumentSet) > 0 {
		ids := make([]uuid.UUID, 0, len(instrumentSet))
		for id := range instrumentSet {
			ids = append(ids, id)
		}
		rows, err := s.q.InstrumentsByIDs(ctx, ids)
		if err != nil {
			return ActionPlanResponse{}, err
		}
		for idx := range rows {
			in := &rows[idx]
			instrumentByID[in.ID] = *in
		}
	}
	positionByKey := map[string]taxdb.Position{}
	if len(accountIDs) > 0 {
		positions, err := s.q.PositionsForAccounts(ctx, accountIDs)
		if err != nil {
			return ActionPlanResponse{}, err
		}
		for idx := range positions {
			p := &positions[idx]
			positionByKey[positionKey(p.AccountID, p.InstrumentID)] = *p
		}
	}
	planID := uuid.New()
	legs := []ActionLeg{}
	for _, opportunityID := range scenarioReq.OpportunityIDs {
		lotID, ok := parseUUID(opportunityID)
		lot, okLot := lotByID[lotID]
		var account taxdb.Account
		var instrument taxdb.Instrument
		okAccount, okInstrument := false, false
		if ok && okLot {
			account, okAccount = accountByID[lot.AccountID]
			instrument, okInstrument = instrumentByID[lot.InstrumentID]
		}
		if !ok || !okLot || !okAccount || !okInstrument {
			return ActionPlanResponse{}, httpx.Unprocessable("A selected tax lot is no longer open or cannot be valued.")
		}
		price := lot.OpenPrice
		if p, ok := positionByKey[positionKey(lot.AccountID, lot.InstrumentID)]; ok && p.LastPrice != nil {
			price = *p.LastPrice
		}
		notional := dmax(zero, fd(lot.RemainingQuantity).Mul(fd(price)))
		legs = append(legs, ActionLeg{
			ID: uuidString(uuid.New()), AccountID: uuidString(lot.AccountID), PortfolioID: uuidPtrString(account.PortfolioID),
			InstrumentID: uuidString(lot.InstrumentID), Symbol: instrument.Symbol, Side: "sell",
			Quantity: decPtr(fd(lot.RemainingQuantity)), Notional: money(notional, lot.Currency),
			LotIDs: []string{opportunityID}, Status: "planned",
		})
		if scenarioResp.SelectedReplacements == nil {
			continue
		}
		candidate, ok := (*scenarioResp.SelectedReplacements)[opportunityID]
		if !ok {
			continue
		}
		replacementID, ok := parseUUID(candidate.InstrumentID)
		if !ok {
			continue
		}
		replacement, ok := instrumentByID[replacementID]
		if !ok {
			continue
		}
		var quantity *dec.Decimal
		if p, ok := positionByKey[positionKey(lot.AccountID, replacementID)]; ok && p.LastPrice != nil && *p.LastPrice > 0 {
			quantity = decPtr(notional.Div(fd(*p.LastPrice)))
		}
		legs = append(legs, ActionLeg{
			ID: uuidString(uuid.New()), AccountID: uuidString(lot.AccountID), PortfolioID: uuidPtrString(account.PortfolioID),
			InstrumentID: uuidString(replacementID), Symbol: replacement.Symbol, Side: "buy",
			Quantity: quantity, Notional: money(notional, lot.Currency), LotIDs: []string{}, Status: "planned",
		})
	}
	if len(legs) == 0 {
		return ActionPlanResponse{}, httpx.Unprocessable("No open lots remain for this scenario.")
	}
	steps := []ActionStep{}
	for i, id := range scenarioReq.OpportunityIDs {
		steps = append(steps, ActionStep{
			ID: uuidString(uuid.New()), Order: i + 1, Title: "Review and place the proposed disposal",
			Detail: "Use your broker to review lot " + id + ". Confirm quantity, fees, and replacement activity before submitting.",
		})
	}
	steps = append(steps, ActionStep{
		ID: uuidString(uuid.New()), Order: len(scenarioReq.OpportunityIDs) + 1, Title: "Monitor the wash-sale window",
		Detail: "Avoid substantially identical acquisitions across household accounts during the applicable window unless your adviser confirms treatment.",
	})
	response, err := s.createPlan(ctx, userID, planID, scenarioID, req, "harvest", steps, legs)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	for _, id := range scenarioReq.OpportunityIDs {
		b, ok := benefitByID[id]
		if !ok {
			b = zero
		}
		if err := s.saveOpportunityDecision(ctx, userID, scenarioReq.TaxYear, id, b, scenarioResp.EstimatedNetBenefit.Currency, "accepted"); err != nil {
			return ActionPlanResponse{}, err
		}
	}
	if err := s.updatePlanResponse(ctx, planID, response); err != nil {
		return ActionPlanResponse{}, err
	}
	return response, nil
}

// createPlan stores the initial plan, its legs and rebalancing drafts and
// returns the final response (stored by the caller after any further
// writes, as Vapor did).
func (s *service) createPlan(ctx context.Context, userID, planID, scenarioID uuid.UUID, req ActionPlanRequest, kind string, steps []ActionStep, legs []ActionLeg) (ActionPlanResponse, error) {
	accepted := "accepted"
	empty := []string{}
	initial := ActionPlanResponse{
		ID: uuidString(planID), ScenarioID: req.ScenarioID, Status: accepted, CreatedAt: isoDate(s.now()),
		Steps: steps, Disclaimer: taxDisclaimer, Kind: &kind, ExecutionStatus: &accepted, Legs: &legs,
		RebalancingPlanIDs: &empty,
	}
	raw, err := encodeSorted(initial)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	now := s.now()
	if err := s.q.InsertActionPlan(ctx, taxdb.InsertActionPlanParams{
		ID: planID, UserID: userID, ScenarioID: scenarioID, Kind: kind, IdempotencyKey: req.IdempotencyKey,
		Status: accepted, ResponseJson: raw, Now: now,
	}); err != nil {
		return ActionPlanResponse{}, err
	}
	if err := s.persistActionLegs(ctx, legs, planID); err != nil {
		return ActionPlanResponse{}, err
	}
	linked, err := s.createDrafts(ctx, userID, planID, kind, legs)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	ids := make([]string, len(linked))
	for i, id := range linked {
		ids[i] = uuidString(id)
	}
	final := initial
	final.RebalancingPlanIDs = &ids
	return final, nil
}

func (s *service) updatePlanResponse(ctx context.Context, planID uuid.UUID, r ActionPlanResponse) error {
	raw, err := encodeSorted(r)
	if err != nil {
		return err
	}
	return s.q.UpdateActionPlanResponse(ctx, taxdb.UpdateActionPlanResponseParams{ResponseJson: raw, Now: nowPtr(s.now()), ID: planID})
}

func (s *service) persistActionLegs(ctx context.Context, legs []ActionLeg, planID uuid.UUID) error {
	for idx := range legs {
		l := &legs[idx]
		id, ok1 := parseUUID(l.ID)
		accountID, ok2 := parseUUID(l.AccountID)
		instrumentID, ok3 := parseUUID(l.InstrumentID)
		if !ok1 || !ok2 || !ok3 {
			return httpx.Unprocessable("A tax action leg contains an invalid identifier.")
		}
		var portfolioID, matched *uuid.UUID
		if l.PortfolioID != nil {
			if u, ok := parseUUID(*l.PortfolioID); ok {
				portfolioID = &u
			}
		}
		if l.MatchedTransactionID != nil {
			if u, ok := parseUUID(*l.MatchedTransactionID); ok {
				matched = &u
			}
		}
		var qty *float64
		if l.Quantity != nil {
			v := dd(*l.Quantity)
			qty = &v
		}
		lotIDs, err := encodeSorted(l.LotIDs)
		if err != nil {
			return err
		}
		if err := s.q.InsertActionLeg(ctx, taxdb.InsertActionLegParams{
			ID: id, ActionPlanID: planID, AccountID: accountID, PortfolioID: portfolioID, InstrumentID: instrumentID,
			Symbol: l.Symbol, Side: l.Side, Quantity: qty, Notional: dd(l.Notional.Amount), Currency: l.Notional.Currency,
			LotIdsJson: lotIDs, Status: l.Status, MatchedTransactionID: matched, Now: nowPtr(s.now()),
		}); err != nil {
			return err
		}
	}
	return nil
}

var legSides = []string{"sell", "buy", "future_contribution"}

var legStatuses = []string{"planned", "matched", "completed", statusCancelled}

func (s *service) actionPlanResponse(ctx context.Context, row taxdb.TaxActionPlan) (ActionPlanResponse, error) {
	var stored ActionPlanResponse
	if err := decodeStored(row.ResponseJson, &stored); err != nil {
		return ActionPlanResponse{}, err
	}
	records, err := s.q.ActionLegsForPlanOrdered(ctx, row.ID)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	legs := []ActionLeg{}
	for idx := range records {
		r := &records[idx]
		lotIDs := []string{}
		if err := decodeStored(r.LotIdsJson, &lotIDs); err != nil {
			return ActionPlanResponse{}, err
		}
		side := r.Side
		if !oneOf(side, legSides) {
			side = "buy"
		}
		status := r.Status
		if !oneOf(status, legStatuses) {
			status = "planned"
		}
		var qty *dec.Decimal
		if r.Quantity != nil {
			qty = decPtr(fd(*r.Quantity))
		}
		legs = append(legs, ActionLeg{
			ID: uuidString(r.ID), AccountID: uuidString(r.AccountID), PortfolioID: uuidPtrString(r.PortfolioID),
			InstrumentID: uuidString(r.InstrumentID), Symbol: r.Symbol, Side: side, Quantity: qty,
			Notional: money(fd(r.Notional), r.Currency), LotIDs: lotIDs, Status: status,
			MatchedTransactionID: uuidPtrString(r.MatchedTransactionID),
		})
	}
	links, err := s.q.RebalancingLinksForPlan(ctx, row.ID)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	linkIDs := make([]string, len(links))
	for i, l := range links {
		linkIDs[i] = uuidString(l.RebalancingPlanID)
	}
	execution := row.Status
	if !oneOf(execution, planStatuses) {
		execution = "requires_review"
	}
	terminal := execution == "completed" || execution == statusCancelled
	steps := make([]ActionStep, len(stored.Steps))
	for i, st := range stored.Steps {
		st.Completed = terminal
		steps[i] = st
	}
	var kind *string
	if oneOf(row.Kind, []string{"harvest", "asset_location"}) {
		kind = strPtr(row.Kind)
	}
	return ActionPlanResponse{
		ID: stored.ID, ScenarioID: stored.ScenarioID, Status: row.Status, CreatedAt: stored.CreatedAt,
		Steps: steps, Disclaimer: stored.Disclaimer, Kind: kind, ExecutionStatus: &execution,
		Legs: &legs, RebalancingPlanIDs: &linkIDs,
	}, nil
}

func (s *service) actionPlans(ctx context.Context, userID uuid.UUID) ([]ActionPlanResponse, error) {
	rows, err := s.q.ActionPlansForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ActionPlanResponse, 0, len(rows))
	for idx := range rows {
		r := &rows[idx]
		resp, err := s.actionPlanResponse(ctx, *r)
		if err != nil {
			return nil, err
		}
		out = append(out, resp)
	}
	return out, nil
}

func (s *service) actionPlan(ctx context.Context, userID, id uuid.UUID) (*ActionPlanResponse, error) {
	row, err := s.q.ActionPlanForUser(ctx, taxdb.ActionPlanForUserParams{ID: id, UserID: userID})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	resp, err := s.actionPlanResponse(ctx, row)
	return &resp, err
}

func (s *service) transitionActionPlan(ctx context.Context, userID, id uuid.UUID, req ActionPlanTransitionRequest) (ActionPlanResponse, error) {
	row, err := s.q.ActionPlanForUser(ctx, taxdb.ActionPlanForUserParams{ID: id, UserID: userID})
	if isNoRows(err) {
		return ActionPlanResponse{}, httpx.NotFound("Tax action plan not found.")
	}
	if err != nil {
		return ActionPlanResponse{}, err
	}
	if req.Status != "completed" && req.Status != statusCancelled {
		return ActionPlanResponse{}, httpx.Unprocessable("An action plan can only be manually completed or cancelled.") //nolint:misspell // Vapor reason text
	}
	if row.Status == "completed" || row.Status == statusCancelled {
		if row.Status == req.Status {
			return s.actionPlanResponse(ctx, row)
		}
		return ActionPlanResponse{}, httpx.Abort(http.StatusConflict, "A terminal action plan cannot change status.")
	}
	executedAt := s.now()
	if req.ExecutedAt != nil {
		if t, ok := parseTaxDate(*req.ExecutedAt); ok {
			executedAt = t
		}
	}
	now := s.now()
	row.Status = req.Status
	row.ExecutedAt = nil
	if req.Status == "completed" {
		row.ExecutedAt = &executedAt
	}
	row.ConfirmationNote = req.ConfirmationNote
	if err := s.q.UpdateActionPlanStatus(ctx, taxdb.UpdateActionPlanStatusParams{
		Status: row.Status, ExecutedAt: row.ExecutedAt, ConfirmationNote: row.ConfirmationNote, Now: &now, ID: row.ID,
	}); err != nil {
		return ActionPlanResponse{}, err
	}
	legs, err := s.q.ActionLegsForPlan(ctx, id)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	for idx := range legs {
		l := &legs[idx]
		status := statusCancelled
		if req.Status == "completed" {
			status = "completed"
		}
		if err := s.q.UpdateActionLegStatus(ctx, taxdb.UpdateActionLegStatusParams{Status: status, Now: &now, ID: l.ID}); err != nil {
			return ActionPlanResponse{}, err
		}
		if req.Status == "completed" && l.Side == "sell" {
			if err := s.createRestrictionWindow(ctx, userID, *l, executedAt); err != nil {
				return ActionPlanResponse{}, err
			}
		}
	}
	return s.actionPlanResponse(ctx, row)
}

func identityKey(in taxdb.Instrument) string {
	for _, v := range []*string{in.TaxIdentityGroup, in.Cusip, in.Isin} {
		if v != nil {
			return *v
		}
	}
	return strings.ToUpper(in.Symbol)
}

func (s *service) createRestrictionWindow(ctx context.Context, userID uuid.UUID, leg taxdb.TaxActionLeg, executedAt time.Time) error {
	exists, err := s.q.RestrictionWindowExistsForLeg(ctx, leg.ID)
	if err != nil || exists {
		return err
	}
	account, err := s.q.AccountByIDForUser(ctx, taxdb.AccountByIDForUserParams{ID: leg.AccountID, UserID: userID})
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if account.TaxJurisdiction == nil || *account.TaxJurisdiction != string(US) {
		return nil
	}
	instrument, err := s.q.InstrumentByID(ctx, leg.InstrumentID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	key := identityKey(instrument)
	start, end := addDays(executedAt, -30), addDays(executedAt, 30)
	windowID := uuid.New()
	now := s.now()
	if err := s.q.InsertRestrictionWindow(ctx, taxdb.InsertRestrictionWindowParams{
		ID: windowID, UserID: userID, ActionLegID: leg.ID, Jurisdiction: string(US), TaxIdentityKey: key,
		StartsAt: start, EndsAt: end, Status: "active", Now: nowPtr(now),
	}); err != nil {
		return err
	}
	accounts, err := s.q.AccountsForUserExceptBroker(ctx, taxdb.AccountsForUserExceptBrokerParams{UserID: userID, Broker: pilotBroker})
	if err != nil || len(accounts) == 0 {
		return err
	}
	accountIDs := make([]uuid.UUID, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		accountIDs[i] = a.ID
	}
	purchases, err := s.q.BuysBetween(ctx, taxdb.BuysBetweenParams{AccountIds: accountIDs, Start: start, EndAt: end})
	if err != nil || len(purchases) == 0 {
		return err
	}
	set := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for idx := range purchases {
		p := &purchases[idx]
		if !set[p.InstrumentID] {
			set[p.InstrumentID] = true
			ids = append(ids, p.InstrumentID)
		}
	}
	instruments, err := s.q.InstrumentsByIDs(ctx, ids)
	if err != nil {
		return err
	}
	matching := map[uuid.UUID]bool{}
	for idx := range instruments {
		in := &instruments[idx]
		if identityKey(*in) == key {
			matching[in.ID] = true
		}
	}
	var hits []taxdb.Transaction
	for idx := range purchases {
		p := &purchases[idx]
		if matching[p.InstrumentID] {
			hits = append(hits, *p)
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].TradeDate.Before(hits[b].TradeDate) })
	return s.q.MarkRestrictionWindowViolated(ctx, taxdb.MarkRestrictionWindowViolatedParams{TransactionID: &hits[0].ID, Now: nowPtr(now), ID: windowID})
}

func (s *service) createLocationScenario(ctx context.Context, userID uuid.UUID, req LocationScenarioRequest, j Jurisdiction) (LocationScenarioResponse, error) {
	dashboard, err := s.dashboard(ctx, userID, j, req.TaxYear)
	if err != nil {
		return LocationScenarioResponse{}, err
	}
	requested := map[string]bool{}
	for _, id := range req.OpportunityIDs {
		requested[id] = true
	}
	selected := []LocationOpportunity{}
	if dashboard.LocationOpportunities != nil {
		all := *dashboard.LocationOpportunities
		for idx := range all {
			o := &all[idx]
			if requested[o.ID] {
				selected = append(selected, *o)
			}
		}
	}
	if len(selected) == 0 {
		return LocationScenarioResponse{}, httpx.Unprocessable("Select at least one available asset-location opportunity.")
	}
	profile, err := s.profileRow(ctx, userID, j, req.TaxYear)
	if err != nil {
		return LocationScenarioResponse{}, err
	}
	if profile == nil {
		return LocationScenarioResponse{}, httpx.Unprocessable("Complete a tax profile first.")
	}
	currency := selected[0].AnnualSavings.Currency
	savings, immediate := zero, zero
	accountSet := map[uuid.UUID]bool{}
	var accountIDs []uuid.UUID
	warnings := []string{}
	for idx := range selected {
		o := &selected[idx]
		savings = savings.Add(o.AnnualSavings.Amount)
		immediate = immediate.Add(o.ImmediateTaxCost.Amount)
		for _, l := range o.Legs {
			if id, ok := parseUUID(l.AccountID); ok && !accountSet[id] {
				accountSet[id] = true
				accountIDs = append(accountIDs, id)
			}
		}
		warnings = append(warnings, o.Warnings...)
	}
	portfolios := map[uuid.UUID]bool{}
	if len(accountIDs) > 0 {
		accounts, err := s.q.AccountsByIDsForUser(ctx, taxdb.AccountsByIDsForUserParams{UserID: userID, Ids: accountIDs})
		if err != nil {
			return LocationScenarioResponse{}, err
		}
		for idx := range accounts {
			a := &accounts[idx]
			if a.PortfolioID != nil {
				portfolios[*a.PortfolioID] = true
			}
		}
	}
	impacts, err := s.goalImpacts(ctx, userID, portfolios, dmax(zero, savings.Sub(immediate)), currency)
	if err != nil {
		return LocationScenarioResponse{}, err
	}
	id := uuid.New()
	response := LocationScenarioResponse{
		ID: uuidString(id), CreatedAt: isoDate(s.now()), Opportunities: selected,
		AnnualSavings: money(savings, currency), ImmediateTaxCost: money(immediate, currency),
		GoalImpacts: impacts, Warnings: warnings,
		Assumptions: append(append([]string{}, dashboard.Assumptions...), "Annual savings are reinvested immediately when estimating goal impact."),
	}
	if err := s.insertScenario(ctx, id, userID, profile.ID, "asset_location", req, response); err != nil {
		return LocationScenarioResponse{}, err
	}
	return response, nil
}

func (s *service) createPlacementPlan(ctx context.Context, userID uuid.UUID, req ActionPlanRequest) (ActionPlanResponse, error) {
	if existing, err := s.existingPlan(ctx, userID, req.IdempotencyKey); err != nil || existing != nil {
		if existing == nil {
			return ActionPlanResponse{}, err
		}
		return *existing, err
	}
	notFound := httpx.NotFound("Asset-location scenario not found.")
	scenarioID, ok := parseUUID(req.ScenarioID)
	if !ok {
		// Deliberate fix: Vapor answered 404 for a malformed id.
		return ActionPlanResponse{}, httpx.BadRequest("Invalid tax scenario id.")
	}
	scenario, err := s.q.ScenarioForUserOfKind(ctx, taxdb.ScenarioForUserOfKindParams{ID: scenarioID, UserID: userID, Kind: "asset_location"})
	if isNoRows(err) {
		return ActionPlanResponse{}, notFound
	}
	if err != nil {
		return ActionPlanResponse{}, err
	}
	var stored LocationScenarioResponse
	if err := decodeStored(scenario.ResponseJson, &stored); err != nil {
		return ActionPlanResponse{}, err
	}
	supported := len(stored.Opportunities) > 0
	for idx := range stored.Opportunities {
		o := &stored.Opportunities[idx]
		if o.SupportLevel != Supported {
			supported = false
		}
	}
	if !supported {
		return ActionPlanResponse{}, httpx.Unprocessable("Asset-location trade plans are only available for fully supported jurisdictions. The estimate remains available for review.")
	}
	accounts, err := s.q.AccountsForUser(ctx, userID)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	accountByID := map[uuid.UUID]taxdb.Account{}
	for idx := range accounts {
		a := &accounts[idx]
		accountByID[a.ID] = *a
	}
	legs := []ActionLeg{}
	for idx := range stored.Opportunities {
		o := &stored.Opportunities[idx]
		for _, l := range o.Legs {
			id, ok := parseUUID(l.AccountID)
			account, okAccount := accountByID[id]
			if !ok || !okAccount {
				return ActionPlanResponse{}, httpx.Abort(http.StatusForbidden, "An asset-location account is no longer available.")
			}
			legs = append(legs, ActionLeg{
				ID: uuidString(uuid.New()), AccountID: l.AccountID, PortfolioID: uuidPtrString(account.PortfolioID),
				InstrumentID: l.InstrumentID, Symbol: l.Symbol, Side: l.Side, Notional: l.Notional,
				LotIDs: []string{}, Status: "planned",
			})
		}
	}
	steps := make([]ActionStep, len(legs))
	for i := range legs {
		l := &legs[i]
		steps[i] = ActionStep{
			ID: uuidString(uuid.New()), Order: i + 1,
			Title:  "Review " + strings.ReplaceAll(l.Side, "_", " ") + " for " + l.Symbol,
			Detail: "Confirm account, notional, allocation impact, tax cost, and current quote before trading.",
		}
	}
	planID := uuid.New()
	response, err := s.createPlan(ctx, userID, planID, scenarioID, req, "asset_location", steps, legs)
	if err != nil {
		return ActionPlanResponse{}, err
	}
	if err := s.updatePlanResponse(ctx, planID, response); err != nil {
		return ActionPlanResponse{}, err
	}
	return response, nil
}
