package brokers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/store/brokersdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// dbtx is a connection or transaction that can open a (nested) transaction.
type dbtx interface {
	brokersdb.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// inTx is `database.transaction { ... }`.
func inTx(ctx context.Context, db dbtx, fn func(tx dbtx) error) error {
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return fn(tx) })
}

// Lot selection methods (TaxLotSelectionMethod raw values).
const (
	methodFIFO                = "fifo"
	methodLIFO                = "lifo"
	methodSpecificID          = "specific_id"
	methodJurisdictionDefault = "jurisdiction_default"
)

// lotAccountingError is TaxLotAccountingError (422).
type lotAccountingError struct {
	kind      string // invalidQuantity, insufficientQuantity, specificIdentificationRequired, unknownSpecificLot
	available float64
	requested float64
	lotID     uuid.UUID
}

func (e *lotAccountingError) Error() string { return e.reason() }

func (e *lotAccountingError) reason() string {
	switch e.kind {
	case "invalidQuantity":
		return "Disposal quantity and price must be positive."
	case "insufficientQuantity":
		return "Open tax lots contain " + formatSwiftDouble(e.available) + " units, but " + formatSwiftDouble(e.requested) + " units were requested."
	case "specificIdentificationRequired":
		return "Specific ID accounting requires the broker-confirmed lot identifiers."
	default:
		return "Specific tax lot " + strings.ToUpper(e.lotID.String()) + " is not open or does not belong to this account and instrument."
	}
}

// httpError is the Abort the error answers with when it escapes a handler.
func (e *lotAccountingError) httpError() *httpx.Error {
	return httpx.Abort(http.StatusUnprocessableEntity, e.reason())
}

type lotCandidate struct {
	id                uuid.UUID
	openDate          time.Time
	remainingQuantity float64
	unitBasis         float64
}

type lotMatch struct {
	lotID       uuid.UUID
	quantity    float64
	proceeds    float64
	costBasis   float64
	realizedPnL float64
}

func uuidString(id uuid.UUID) string { return strings.ToUpper(id.String()) }

// matchLots is TaxLotMatcher.match.
func matchLots(candidates []lotCandidate, quantity, unitPrice, fees float64, method string, specific []uuid.UUID) ([]lotMatch, error) {
	if quantity <= 0 || unitPrice < 0 || fees < 0 {
		return nil, &lotAccountingError{kind: "invalidQuantity"}
	}
	ordered := append([]lotCandidate(nil), candidates...)
	switch method {
	case methodLIFO:
		sort.SliceStable(ordered, func(i, j int) bool {
			a, b := ordered[i], ordered[j]
			if !a.openDate.Equal(b.openDate) {
				return a.openDate.After(b.openDate)
			}
			return uuidString(a.id) > uuidString(b.id)
		})
	case methodSpecificID:
		if len(specific) == 0 {
			return nil, &lotAccountingError{kind: "specificIdentificationRequired"}
		}
		byID := map[uuid.UUID]lotCandidate{}
		for _, c := range candidates {
			byID[c.id] = c
		}
		ordered = ordered[:0]
		for _, id := range specific {
			c, ok := byID[id]
			if !ok {
				return nil, &lotAccountingError{kind: "unknownSpecificLot", lotID: id}
			}
			ordered = append(ordered, c)
		}
	default: // fifo, jurisdiction_default
		sort.SliceStable(ordered, func(i, j int) bool {
			a, b := ordered[i], ordered[j]
			if !a.openDate.Equal(b.openDate) {
				return a.openDate.Before(b.openDate)
			}
			return uuidString(a.id) < uuidString(b.id)
		})
	}
	available := 0.0
	for _, c := range ordered {
		available += max(0, c.remainingQuantity)
	}
	if available+0.0000001 < quantity {
		return nil, &lotAccountingError{kind: "insufficientQuantity", available: available, requested: quantity}
	}
	remaining := quantity
	var matches []lotMatch
	for _, lot := range ordered {
		if remaining <= 0 {
			break
		}
		consumed := min(remaining, max(0, lot.remainingQuantity))
		if consumed <= 0 {
			continue
		}
		allocatedFees := fees * (consumed / quantity)
		proceeds := consumed*unitPrice - allocatedFees
		basis := consumed * lot.unitBasis
		matches = append(matches, lotMatch{lotID: lot.id, quantity: consumed, proceeds: proceeds, costBasis: basis, realizedPnL: proceeds - basis})
		remaining -= consumed
	}
	return matches, nil
}

// unitBasis is TaxAcquisitionBasisCalculator.unitBasis.
func unitBasis(quantity, price, fees float64) float64 { return price + abs(fees)/quantity }

// lotAccounting is TaxLotAccountingService.
type lotAccounting struct{ now func() time.Time }

// recordAcquisition opens the lot for a BUY/OPENING_BALANCE trade, or returns
// the one it already opened.
func (a lotAccounting) recordAcquisition(ctx context.Context, db dbtx, t brokersdb.Transaction) error {
	if t.Quantity == nil || abs(*t.Quantity) <= 0 || t.Price == nil || *t.Price < 0 {
		return &lotAccountingError{kind: "invalidQuantity"}
	}
	q := brokersdb.New(db)
	id := t.ID
	existing, err := q.LotByOpenTransaction(ctx, &id)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	quantity := abs(*t.Quantity)
	fees := 0.0
	if t.Fees != nil {
		fees = *t.Fees
	}
	_, err = q.InsertLot(ctx, brokersdb.InsertLotParams{
		ID: uuid.New(), AccountID: t.AccountID, InstrumentID: t.InstrumentID, OpenTransactionID: &id,
		OpenDate: t.TradeDate, OpenQuantity: quantity, RemainingQuantity: quantity,
		OpenPrice: unitBasis(quantity, *t.Price, fees), Currency: t.Currency, Status: "open", Now: a.now(),
	})
	return err
}

// recordDisposal matches a SELL against the open lots in one transaction;
// a sale already disposed returns its existing disposals.
func (a lotAccounting) recordDisposal(ctx context.Context, db dbtx, t brokersdb.Transaction, method string, specific []uuid.UUID) ([]brokersdb.LotDisposal, error) {
	if t.Quantity == nil || t.Price == nil {
		return nil, &lotAccountingError{kind: "invalidQuantity"}
	}
	quantity, price := abs(*t.Quantity), *t.Price
	var records []brokersdb.LotDisposal
	err := inTx(ctx, db, func(tx dbtx) error {
		q := brokersdb.New(tx)
		existing, err := q.DisposalsForTransaction(ctx, t.ID)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			records = existing
			return nil
		}
		lots, err := q.OpenLots(ctx, brokersdb.OpenLotsParams{AccountID: t.AccountID, InstrumentID: t.InstrumentID})
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(lots))
		for i := range lots {
			l := &lots[i]
			ids[i] = l.ID
		}
		adjustmentByLot := map[uuid.UUID]float64{}
		if len(ids) > 0 {
			adjustments, err := q.AdjustmentsForLots(ctx, ids)
			if err != nil {
				return err
			}
			for ix := range adjustments {
				adj := adjustments[ix]
				adjustmentByLot[adj.LotID] += adj.Amount
			}
		}
		var candidates []lotCandidate
		for ix := range lots {
			l := lots[ix]
			if l.RemainingQuantity <= 0 {
				continue
			}
			adjusted := l.OpenPrice + adjustmentByLot[l.ID]/max(l.RemainingQuantity, 0.0000001)
			candidates = append(candidates, lotCandidate{id: l.ID, openDate: l.OpenDate, remainingQuantity: l.RemainingQuantity, unitBasis: adjusted})
		}
		fees := 0.0
		if t.Fees != nil {
			fees = *t.Fees
		}
		matches, err := matchLots(candidates, quantity, price, fees, method, specific)
		if err != nil {
			return err
		}
		lotByID := map[uuid.UUID]*brokersdb.Lot{}
		for i := range lots {
			lotByID[lots[i].ID] = &lots[i]
		}
		now := a.now()
		for _, m := range matches {
			lot := lotByID[m.lotID]
			if lot == nil {
				continue
			}
			record, err := q.InsertDisposal(ctx, brokersdb.InsertDisposalParams{
				ID: uuid.New(), LotID: m.lotID, TransactionID: t.ID, Quantity: m.quantity,
				Proceeds: m.proceeds, CostBasis: m.costBasis, RealizedPnl: m.realizedPnL,
				HoldingPeriod: holdingPeriod(lot.OpenDate, t.TradeDate), Now: now,
			})
			if err != nil {
				return err
			}
			lot.RemainingQuantity = max(0, lot.RemainingQuantity-m.quantity)
			realized := m.realizedPnL
			if lot.RealizedPnl != nil {
				realized += *lot.RealizedPnl
			}
			lot.RealizedPnl = &realized
			if lot.RemainingQuantity <= 0.0000001 {
				lot.RemainingQuantity = 0
				lot.Status = "closed"
				closeDate, closeTx := t.TradeDate, t.ID
				lot.CloseDate, lot.CloseTransactionID = &closeDate, &closeTx
			}
			if err := q.UpdateLotDisposal(ctx, brokersdb.UpdateLotDisposalParams{
				ID: lot.ID, RemainingQuantity: lot.RemainingQuantity, RealizedPnl: lot.RealizedPnl,
				Status: lot.Status, CloseDate: lot.CloseDate, CloseTransactionID: lot.CloseTransactionID, Now: &now,
			}); err != nil {
				return err
			}
			records = append(records, record)
		}
		return nil
	})
	return records, err
}

func holdingPeriod(open, closed time.Time) string {
	if closed.Sub(open) > 365*86400*time.Second {
		return "long_term"
	}
	return "short_term"
}

// sameInstrumentIDs is the sold instrument plus every instrument sharing its
// non-empty tax identity group.
func sameInstrumentIDs(sold brokersdb.Instrument, all []brokersdb.Instrument) []uuid.UUID {
	var ids []uuid.UUID
	for ix := range all {
		in := all[ix]
		if in.ID == sold.ID {
			ids = append(ids, in.ID)
			continue
		}
		if sold.TaxIdentityGroup == nil || *sold.TaxIdentityGroup == "" {
			continue
		}
		if in.TaxIdentityGroup != nil && *in.TaxIdentityGroup == *sold.TaxIdentityGroup {
			ids = append(ids, in.ID)
		}
	}
	return ids
}

type washAllocation struct {
	replacementLotID uuid.UUID
	matchedQuantity  float64
	loss             float64
	isPermanent      bool
}

type washReplacement struct {
	lotID           uuid.UUID
	acquisitionDate time.Time
	available       float64
	isTaxAdvantaged bool
}

// matchReplacements is TaxWashSaleMatcher.match / SpainLossDeferralMatcher.match
// over [start, end].
func matchReplacements(start, end time.Time, soldQuantity, realizedPnL float64, replacements []washReplacement) []washAllocation {
	if soldQuantity <= 0 || realizedPnL >= 0 {
		return nil
	}
	lossPerUnit := -realizedPnL / soldQuantity
	var eligible []washReplacement
	for _, r := range replacements {
		if r.available > 0 && !r.acquisitionDate.Before(start) && !r.acquisitionDate.After(end) {
			eligible = append(eligible, r)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if !a.acquisitionDate.Equal(b.acquisitionDate) {
			return a.acquisitionDate.Before(b.acquisitionDate)
		}
		return uuidString(a.lotID) < uuidString(b.lotID)
	})
	remaining := soldQuantity
	var out []washAllocation
	for _, r := range eligible {
		if remaining <= 0 {
			break
		}
		qty := min(remaining, r.available)
		out = append(out, washAllocation{replacementLotID: r.lotID, matchedQuantity: qty, loss: qty * lossPerUnit, isPermanent: r.isTaxAdvantaged})
		remaining -= qty
	}
	return out
}

// addMonths is Calendar.date(byAdding: .month): the day clamps to the end
// of a shorter month instead of overflowing.
func addMonths(t time.Time, months int) time.Time {
	first := time.Date(t.Year(), t.Month(), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).AddDate(0, months, 0)
	last := first.AddDate(0, 1, -1).Day()
	day := min(t.Day(), last)
	return time.Date(first.Year(), first.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// recordUSWashSales is TaxLotAccountingService.recordUSWashSales.
func (a lotAccounting) recordUSWashSales(ctx context.Context, db dbtx, disposals []brokersdb.LotDisposal, sale brokersdb.Transaction, userID uuid.UUID, ruleVersion string) error {
	if len(disposals) == 0 {
		return nil
	}
	return inTx(ctx, db, func(tx dbtx) error {
		q := brokersdb.New(tx)
		accounts, err := q.UserAccounts(ctx, userID)
		if err != nil {
			return err
		}
		accountByID := map[uuid.UUID]brokersdb.Account{}
		var accountIDs []uuid.UUID
		for idx := range accounts {
			acc := &accounts[idx]
			if _, dup := accountByID[acc.ID]; !dup {
				accountIDs = append(accountIDs, acc.ID)
			}
			accountByID[acc.ID] = *acc
		}
		if len(accountIDs) == 0 {
			return nil
		}
		sold, err := q.GetInstrument(ctx, sale.InstrumentID)
		if err != nil || len(sold) == 0 {
			return err
		}
		all, err := q.AllInstruments(ctx)
		if err != nil {
			return err
		}
		start, end := sale.TradeDate.AddDate(0, 0, -30), sale.TradeDate.AddDate(0, 0, 30)
		lots, err := q.LotsInWindow(ctx, brokersdb.LotsInWindowParams{
			AccountIds: accountIDs, InstrumentIds: sameInstrumentIDs(sold[0], all), StartDate: start, EndDate: end,
		})
		if err != nil {
			return err
		}
		available := map[uuid.UUID]float64{}
		for ix := range lots {
			l := lots[ix]
			available[l.ID] = max(0, l.OpenQuantity)
		}
		now := a.now()
		for _, d := range disposals {
			if d.RealizedPnl >= 0 {
				continue
			}
			var replacements []washReplacement
			for ix := range lots {
				l := lots[ix]
				acc, ok := accountByID[l.AccountID]
				if !ok {
					continue
				}
				wrapper := ""
				if acc.TaxWrapper != nil {
					wrapper = *acc.TaxWrapper
				}
				replacements = append(replacements, washReplacement{
					lotID: l.ID, acquisitionDate: l.OpenDate, available: available[l.ID],
					isTaxAdvantaged: wrapper == "traditional_ira" || wrapper == "roth_ira",
				})
			}
			for _, alloc := range matchReplacements(start, end, d.Quantity, d.RealizedPnl, replacements) {
				exists, err := q.WashMatchExists(ctx, brokersdb.WashMatchExistsParams{DisposalID: d.ID, ReplacementLotID: alloc.replacementLotID})
				if err != nil {
					return err
				}
				if exists {
					continue
				}
				if err := q.InsertWashMatch(ctx, brokersdb.InsertWashMatchParams{
					ID: uuid.New(), DisposalID: d.ID, ReplacementLotID: alloc.replacementLotID,
					MatchedQuantity: alloc.matchedQuantity, DisallowedLoss: alloc.loss, Currency: sale.Currency,
					IsPermanent: alloc.isPermanent, RuleVersion: ruleVersion, Now: now,
				}); err != nil {
					return err
				}
				if !alloc.isPermanent {
					if err := insertAdjustment(ctx, q, alloc, sale, "wash_sale_deferred_loss", now); err != nil {
						return err
					}
				}
				available[alloc.replacementLotID] -= alloc.matchedQuantity
			}
		}
		return nil
	})
}

func insertAdjustment(ctx context.Context, q *brokersdb.Queries, alloc washAllocation, sale brokersdb.Transaction, kind string, now time.Time) error {
	saleID, qty := sale.ID, alloc.matchedQuantity
	return q.InsertAdjustment(ctx, brokersdb.InsertAdjustmentParams{
		ID: uuid.New(), LotID: alloc.replacementLotID, SourceTransactionID: &saleID, Kind: kind,
		Amount: alloc.loss, Quantity: &qty, Currency: sale.Currency, EffectiveDate: sale.TradeDate, Now: now,
	})
}

// spainWindowMonths is SpainMarketAdmissionPolicy.windowMonths.
func spainWindowMonths(in brokersdb.Instrument) (int, bool) {
	if in.RegulatedMarketSource == nil || *in.RegulatedMarketSource == "" || in.RegulatedMarketReviewedAt == nil {
		return 0, false
	}
	if in.RegulatedMarketStatus == nil {
		return 0, false
	}
	switch *in.RegulatedMarketStatus {
	case "regulated":
		return 2, true
	case "unlisted":
		return 12, true
	}
	return 0, false
}

// recordSpainLossDeferrals is TaxLotAccountingService.recordSpainLossDeferrals.
func (a lotAccounting) recordSpainLossDeferrals(ctx context.Context, db dbtx, disposals []brokersdb.LotDisposal, sale brokersdb.Transaction, userID uuid.UUID, ruleVersion string) error {
	if len(disposals) == 0 {
		return nil
	}
	return inTx(ctx, db, func(tx dbtx) error {
		q := brokersdb.New(tx)
		accounts, err := q.UserAccounts(ctx, userID)
		if err != nil {
			return err
		}
		accountIDs := make([]uuid.UUID, len(accounts))
		for i := range accounts {
			acc := &accounts[i]
			accountIDs[i] = acc.ID
		}
		if len(accountIDs) == 0 {
			return nil
		}
		sold, err := q.GetInstrument(ctx, sale.InstrumentID)
		if err != nil || len(sold) == 0 {
			return err
		}
		months, ok := spainWindowMonths(sold[0])
		if !ok {
			return nil
		}
		all, err := q.AllInstruments(ctx)
		if err != nil {
			return err
		}
		start, end := addMonths(sale.TradeDate, -months), addMonths(sale.TradeDate, months)
		soldLots := map[uuid.UUID]bool{}
		for _, d := range disposals {
			soldLots[d.LotID] = true
		}
		windowLots, err := q.LotsInWindow(ctx, brokersdb.LotsInWindowParams{
			AccountIds: accountIDs, InstrumentIds: sameInstrumentIDs(sold[0], all), StartDate: start, EndDate: end,
		})
		if err != nil {
			return err
		}
		var lots []brokersdb.Lot
		for ix := range windowLots {
			l := windowLots[ix]
			if !soldLots[l.ID] {
				lots = append(lots, l)
			}
		}
		available := map[uuid.UUID]float64{}
		for ix := range lots {
			l := lots[ix]
			available[l.ID] = max(0, l.RemainingQuantity)
		}
		now := a.now()
		for _, d := range disposals {
			if d.RealizedPnl >= 0 {
				continue
			}
			var replacements []washReplacement
			for ix := range lots {
				l := lots[ix]
				replacements = append(replacements, washReplacement{lotID: l.ID, acquisitionDate: l.OpenDate, available: available[l.ID]})
			}
			if months <= 0 {
				continue
			}
			for _, alloc := range matchReplacements(start, end, d.Quantity, d.RealizedPnl, replacements) {
				exists, err := q.WashMatchExists(ctx, brokersdb.WashMatchExistsParams{DisposalID: d.ID, ReplacementLotID: alloc.replacementLotID})
				if err != nil {
					return err
				}
				if exists {
					continue
				}
				if err := q.InsertWashMatch(ctx, brokersdb.InsertWashMatchParams{
					ID: uuid.New(), DisposalID: d.ID, ReplacementLotID: alloc.replacementLotID,
					MatchedQuantity: alloc.matchedQuantity, DisallowedLoss: alloc.loss, Currency: sale.Currency,
					IsPermanent: false, RuleVersion: ruleVersion, Now: now,
				}); err != nil {
					return err
				}
				if err := insertAdjustment(ctx, q, alloc, sale, "spain_homogeneous_security_deferred_loss", now); err != nil {
					return err
				}
				available[alloc.replacementLotID] -= alloc.matchedQuantity
			}
		}
		return nil
	})
}

// MARK: - TaxPlanReconciler

type planMatch struct {
	legID  uuid.UUID
	planID uuid.UUID
	lotIDs []uuid.UUID
}

type planReconciler struct{ now func() time.Time }

// match is TaxPlanReconciler.match: the single accepted plan leg this trade
// executes. Several eligible legs flag their plans for review.
func (p planReconciler) match(ctx context.Context, db dbtx, t brokersdb.Transaction, userID uuid.UUID) (*planMatch, error) {
	typ := strings.ToUpper(t.Type)
	if t.Quantity == nil || abs(*t.Quantity) <= 0 || (typ != "BUY" && typ != "SELL") {
		return nil, nil
	}
	quantity := abs(*t.Quantity)
	side := "buy"
	if typ == "SELL" {
		side = "sell"
	}
	q := brokersdb.New(db)
	legs, err := q.PlannedLegs(ctx, brokersdb.PlannedLegsParams{AccountID: t.AccountID, InstrumentID: t.InstrumentID, Side: side})
	if err != nil {
		return nil, err
	}
	type pair struct {
		leg  brokersdb.TaxActionLeg
		plan brokersdb.TaxActionPlan
	}
	var eligible []pair
	for ix := range legs {
		leg := legs[ix]
		plans, err := q.UserPlan(ctx, brokersdb.UserPlanParams{ID: leg.ActionPlanID, UserID: userID})
		if err != nil {
			return nil, err
		}
		if len(plans) == 0 {
			continue
		}
		plan := plans[0]
		if plan.Status != "accepted" && plan.Status != "partially_matched" {
			continue
		}
		if !withinReconciliationWindow(t.TradeDate, plan.CreatedAt) || !quantitiesMatch(leg.Quantity, quantity) || !notionalsMatch(leg, t) {
			continue
		}
		eligible = append(eligible, pair{leg, plan})
	}
	if len(eligible) != 1 {
		if len(eligible) > 1 {
			now := p.now()
			for idx := range eligible {
				e := &eligible[idx]
				if err := q.SetPlanStatus(ctx, brokersdb.SetPlanStatusParams{ID: e.plan.ID, Status: "requires_review", ExecutedAt: e.plan.ExecutedAt, Now: &now}); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	}
	var raw []string
	if err := json.Unmarshal([]byte(eligible[0].leg.LotIdsJson), &raw); err != nil {
		return nil, fmt.Errorf("decode lot ids: %w", err)
	}
	var lotIDs []uuid.UUID
	for _, s := range raw {
		if id, ok := parseSwiftUUID(s); ok {
			lotIDs = append(lotIDs, id)
		}
	}
	return &planMatch{legID: eligible[0].leg.ID, planID: eligible[0].plan.ID, lotIDs: lotIDs}, nil
}

func quantitiesMatch(planned *float64, actual float64) bool {
	if planned == nil || *planned <= 0 {
		return true
	}
	return abs(*planned-actual) / *planned <= 0.005
}

func notionalsMatch(leg brokersdb.TaxActionLeg, t brokersdb.Transaction) bool {
	if leg.Quantity != nil || t.Quantity == nil || t.Price == nil || leg.Notional <= 0 {
		return true
	}
	return abs(leg.Notional-abs(*t.Quantity)**t.Price)/leg.Notional <= 0.02
}

// withinReconciliationWindow: whole days from the plan's creation to the
// trade (truncated toward zero, as Calendar.dateComponents counts them) in
// -1...35.
func withinReconciliationWindow(tradeDate, createdAt time.Time) bool {
	days := int(tradeDate.Sub(createdAt) / (24 * time.Hour))
	return days >= -1 && days <= 35
}

// complete is TaxPlanReconciler.complete.
func (p planReconciler) complete(ctx context.Context, db dbtx, m *planMatch, t brokersdb.Transaction, userID uuid.UUID) error {
	q := brokersdb.New(db)
	var leg *brokersdb.TaxActionLeg
	var plan *brokersdb.TaxActionPlan
	if m != nil {
		legs, err := q.GetLeg(ctx, m.legID)
		if err != nil {
			return err
		}
		if len(legs) > 0 {
			plans, err := q.UserPlan(ctx, brokersdb.UserPlanParams{ID: m.planID, UserID: userID})
			if err != nil {
				return err
			}
			if len(plans) > 0 {
				leg, plan = &legs[0], &plans[0]
			}
		}
	}
	typ := strings.ToUpper(t.Type)
	if leg == nil || plan == nil {
		if typ == "BUY" {
			return p.detectRestrictionViolation(ctx, db, t, userID)
		}
		return nil
	}
	now := p.now()
	txID := t.ID
	if err := q.MatchLeg(ctx, brokersdb.MatchLegParams{ID: leg.ID, Status: "matched", MatchedTransactionID: &txID, Now: &now}); err != nil {
		return err
	}
	if typ == "SELL" {
		if err := p.createRestrictionWindow(ctx, db, userID, *leg, t.TradeDate); err != nil {
			return err
		}
	} else if err := p.detectRestrictionViolation(ctx, db, t, userID); err != nil {
		return err
	}
	planLegs, err := q.PlanLegs(ctx, m.planID)
	if err != nil {
		return err
	}
	allMatched := true
	for ix := range planLegs {
		l := planLegs[ix]
		if l.Status != "matched" {
			allMatched = false
		}
	}
	params := brokersdb.SetPlanStatusParams{ID: plan.ID, Status: "partially_matched", ExecutedAt: plan.ExecutedAt, Now: &now}
	if allMatched {
		executed := t.TradeDate
		params.Status, params.ExecutedAt = "completed", &executed
	}
	return q.SetPlanStatus(ctx, params)
}

func (p planReconciler) createRestrictionWindow(ctx context.Context, db dbtx, userID uuid.UUID, leg brokersdb.TaxActionLeg, date time.Time) error {
	q := brokersdb.New(db)
	exists, err := q.WindowExistsForLeg(ctx, leg.ID)
	if err != nil || exists {
		return err
	}
	accounts, err := q.GetAccount(ctx, leg.AccountID)
	if err != nil || len(accounts) == 0 {
		return err
	}
	acc := accounts[0]
	if acc.UserID != userID || acc.TaxJurisdiction == nil || *acc.TaxJurisdiction != "US" {
		return nil
	}
	instruments, err := q.GetInstrument(ctx, leg.InstrumentID)
	if err != nil || len(instruments) == 0 {
		return err
	}
	key := identityKey(instruments[0])
	now := p.now()
	windowID := uuid.New()
	start, end := date.AddDate(0, 0, -30), date.AddDate(0, 0, 30)
	if err := q.InsertWindow(ctx, brokersdb.InsertWindowParams{
		ID: windowID, UserID: userID, ActionLegID: leg.ID, Jurisdiction: "US", TaxIdentityKey: key,
		StartsAt: start, EndsAt: end, Status: "active", Now: &now,
	}); err != nil {
		return err
	}
	// detectExistingRestrictionViolation: the earliest real-account BUY of
	// the same identity inside the window.
	accountIDs, err := q.NonPilotAccountIDs(ctx, userID)
	if err != nil || len(accountIDs) == 0 {
		return err
	}
	buys, err := q.BuysInAccountsBetween(ctx, brokersdb.BuysInAccountsBetweenParams{AccountIds: accountIDs, StartDate: start, EndDate: end})
	if err != nil {
		return err
	}
	idSet := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for ix := range buys {
		b := buys[ix]
		if !idSet[b.InstrumentID] {
			idSet[b.InstrumentID] = true
			ids = append(ids, b.InstrumentID)
		}
	}
	matching := map[uuid.UUID]bool{}
	if len(ids) > 0 {
		ins, err := q.InstrumentsByIDs(ctx, ids)
		if err != nil {
			return err
		}
		for ix := range ins {
			in := ins[ix]
			if identityKey(in) == key {
				matching[in.ID] = true
			}
		}
	}
	var candidates []brokersdb.Transaction
	for ix := range buys {
		b := buys[ix]
		if matching[b.InstrumentID] {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].TradeDate.Before(candidates[j].TradeDate) })
	violating := candidates[0].ID
	return q.ViolateWindow(ctx, brokersdb.ViolateWindowParams{ID: windowID, ViolatingTransactionID: &violating, Now: &now})
}

func (p planReconciler) detectRestrictionViolation(ctx context.Context, db dbtx, t brokersdb.Transaction, userID uuid.UUID) error {
	q := brokersdb.New(db)
	accounts, err := q.GetAccount(ctx, t.AccountID)
	if err != nil || len(accounts) == 0 {
		return err
	}
	// A pilot follow's buys are simulated: they replace nothing.
	if accounts[0].UserID != userID || accounts[0].Broker == "pilot" {
		return nil
	}
	instruments, err := q.GetInstrument(ctx, t.InstrumentID)
	if err != nil || len(instruments) == 0 {
		return err
	}
	windows, err := q.ActiveWindows(ctx, brokersdb.ActiveWindowsParams{UserID: userID, TaxIdentityKey: identityKey(instruments[0]), At: t.TradeDate})
	if err != nil {
		return err
	}
	now := p.now()
	txID := t.ID
	for idx := range windows {
		w := &windows[idx]
		if err := q.ViolateWindow(ctx, brokersdb.ViolateWindowParams{ID: w.ID, ViolatingTransactionID: &txID, Now: &now}); err != nil {
			return err
		}
	}
	return nil
}

func identityKey(in brokersdb.Instrument) string {
	switch {
	case in.TaxIdentityGroup != nil:
		return *in.TaxIdentityGroup
	case in.Cusip != nil:
		return *in.Cusip
	case in.Isin != nil:
		return *in.Isin
	}
	return strings.ToUpper(in.Symbol)
}

// MARK: - DividendWithholdingReconciler

// applyWithholding is DividendWithholdingReconciler.apply for one dividend:
// the first withholding line on the same UTC day, symbol and currency sets
// withholding = |amount| and gross = amount + withholding. Vapor applies a
// fresh copy of the statement's lines to every dividend, so a line is never
// used up across dividends.
func applyWithholding(lines []withholdingRow, symbol string, payDate time.Time, currency string, amount float64) (withholding, gross *float64) {
	day := utcDay(payDate)
	sym, cur := strings.ToUpper(symbol), strings.ToUpper(currency)
	for _, w := range lines {
		if strings.EqualFold(w.Symbol, sym) && utcDay(w.PayDate).Equal(day) && strings.EqualFold(w.Currency, cur) {
			tax := abs(w.Amount)
			g := amount + tax
			return &tax, &g
		}
	}
	return nil, nil
}

func utcDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
