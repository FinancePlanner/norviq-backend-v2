package tax

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
)

// Portugal Category G (PortugalCapitalGainsCalculator).

type portugalPosition struct {
	realized    dec.Decimal
	holdingDays int
}

type portugalResult struct {
	annualBalance, taxableBalance, appliedCarryforward, remainingCarryforward dec.Decimal
	estimatedTax, appliedRate                                                 dec.Decimal
	aggregationRequired, aggregationApplied                                   bool
}

func portugalCalculate(positions []portugalPosition, income, marginal dec.Decimal, mode *string, eligible dec.Decimal) portugalResult {
	annual := zero
	shortTermGain := false
	for _, p := range positions {
		annual = annual.Add(p.realized)
		if p.holdingDays < 365 && p.realized.Sign() > 0 {
			shortTermGain = true
		}
	}
	positive := dmax(zero, annual)
	required := shortTermGain && income.Add(positive).Cmp(portugalTopBand2026) >= 0
	applied := required || (mode != nil && *mode == "aggregate_with_income")
	available := dmax(zero, eligible)
	appliedCF := zero
	if applied {
		appliedCF = dmin(positive, available)
	}
	taxable := dmax(zero, positive.Sub(appliedCF))
	rate := portugalAutonomous
	if applied {
		rate = dmax(zero, marginal)
	}
	currentLoss := zero
	if applied && annual.Sign() < 0 {
		currentLoss = annual.Neg()
	}
	return portugalResult{
		annualBalance: annual, taxableBalance: taxable, appliedCarryforward: appliedCF,
		remainingCarryforward: dmax(zero, available.Sub(appliedCF)).Add(currentLoss),
		estimatedTax:          taxable.Mul(rate), appliedRate: rate,
		aggregationRequired: required, aggregationApplied: applied,
	}
}

// ptLedger is PortugalLossCarryforwardLedger.
type ptLedger struct{ s *service }

func sumApplications(apps []taxdb.TaxLossCarryforwardApplication) float64 {
	t := 0.0
	for _, a := range apps {
		t += a.Amount
	}
	return t
}

func (l ptLedger) response(ctx context.Context, userID uuid.UUID, j Jurisdiction, asOf int) (LossCarryforwardLedger, error) {
	q := l.s.q
	balances, err := q.CarryforwardsAsOf(ctx, taxdb.CarryforwardsAsOfParams{UserID: userID, Jurisdiction: string(j), SourceTaxYear: int64(asOf)})
	if err != nil {
		return LossCarryforwardLedger{}, err
	}
	byBalance := map[uuid.UUID][]taxdb.TaxLossCarryforwardApplication{}
	if len(balances) > 0 {
		ids := make([]uuid.UUID, len(balances))
		for i := range balances {
			b := &balances[i]
			ids[i] = b.ID
		}
		apps, err := q.CarryforwardApplicationsSorted(ctx, ids)
		if err != nil {
			return LossCarryforwardLedger{}, err
		}
		for _, a := range apps {
			byBalance[a.CarryforwardID] = append(byBalance[a.CarryforwardID], a)
		}
	}
	now := l.s.now()
	rows := make([]LossCarryforwardBalance, 0, len(balances))
	total := zero
	for idx := range balances {
		b := &balances[idx]
		apps := []LossCarryforwardApplication{}
		for _, a := range byBalance[b.ID] {
			apps = append(apps, LossCarryforwardApplication{
				ID: uuidString(a.ID), TargetTaxYear: int(a.TargetTaxYear),
				Amount: money(fd(a.Amount), b.Currency), CreatedAt: isoSeconds(orNow(a.CreatedAt, now)),
			})
		}
		rows = append(rows, LossCarryforwardBalance{
			ID: uuidString(b.ID), Jurisdiction: j, SourceTaxYear: int(b.SourceTaxYear),
			ExpiresAfterTaxYear: int(b.ExpiresAfterTaxYear),
			OriginalAmount:      money(fd(b.OriginalAmount), b.Currency),
			RemainingAmount:     money(fd(b.RemainingAmount), b.Currency),
			RuleVersion:         b.RuleVersion, Applications: apps,
		})
		if int(b.SourceTaxYear) < asOf && int(b.ExpiresAfterTaxYear) >= asOf {
			total = total.Add(fd(max(0, b.RemainingAmount)))
		}
	}
	currency := defaultCurrency(j)
	if len(balances) > 0 {
		currency = balances[0].Currency
	}
	return LossCarryforwardLedger{
		GeneratedAt: isoSeconds(now), Jurisdiction: j, AsOfTaxYear: asOf,
		TotalAvailable: money(total, currency), Balances: rows,
	}, nil
}

func orNow(t *time.Time, now time.Time) time.Time {
	if t == nil {
		return now
	}
	return *t
}

func (l ptLedger) available(ctx context.Context, userID uuid.UUID, taxYear int) (dec.Decimal, error) {
	q := l.s.q
	balances, err := q.EligibleCarryforwards(ctx, taxdb.EligibleCarryforwardsParams{UserID: userID, TaxYear: int64(taxYear)})
	if err != nil || len(balances) == 0 {
		return zero, err
	}
	ids := make([]uuid.UUID, len(balances))
	for i := range balances {
		b := &balances[i]
		ids[i] = b.ID
	}
	apps, err := q.CarryforwardApplicationsBefore(ctx, taxdb.CarryforwardApplicationsBeforeParams{Ids: ids, TaxYear: int64(taxYear)})
	if err != nil {
		return zero, err
	}
	applied := map[uuid.UUID]float64{}
	for _, a := range apps {
		applied[a.CarryforwardID] += a.Amount
	}
	total := zero
	for idx := range balances {
		b := &balances[idx]
		total = total.Add(fd(max(0, b.OriginalAmount-applied[b.ID])))
	}
	return total, nil
}

func (l ptLedger) reconcile(ctx context.Context, userID uuid.UUID, taxYear int, currency, ruleVersion string, result portugalResult) error {
	now := l.s.now()
	return l.s.inTx(ctx, func(q *taxdb.Queries) error {
		balances, err := q.EligibleCarryforwards(ctx, taxdb.EligibleCarryforwardsParams{UserID: userID, TaxYear: int64(taxYear)})
		if err != nil {
			return err
		}
		sort.SliceStable(balances, func(a, b int) bool {
			if balances[a].SourceTaxYear != balances[b].SourceTaxYear {
				return balances[a].SourceTaxYear < balances[b].SourceTaxYear
			}
			return uuidString(balances[a].ID) < uuidString(balances[b].ID)
		})
		if len(balances) > 0 {
			ids := make([]uuid.UUID, len(balances))
			for i := range balances {
				b := &balances[i]
				ids[i] = b.ID
			}
			prior, err := q.CarryforwardApplicationsFor(ctx, taxdb.CarryforwardApplicationsForParams{Ids: ids, TaxYear: int64(taxYear)})
			if err != nil {
				return err
			}
			for _, a := range prior {
				if err := q.DeleteCarryforwardApplication(ctx, a.ID); err != nil {
					return err
				}
			}
			toApply := max(0, dd(result.appliedCarryforward))
			for idx := range balances {
				b := &balances[idx]
				if !(toApply > 0) {
					break
				}
				earlier, err := q.CarryforwardApplicationsOfBefore(ctx, taxdb.CarryforwardApplicationsOfBeforeParams{CarryforwardID: b.ID, TargetTaxYear: int64(taxYear)})
				if err != nil {
					return err
				}
				applied := min(max(0, b.OriginalAmount-sumApplications(earlier)), toApply)
				if !(applied > 0) {
					continue
				}
				if err := q.InsertCarryforwardApplication(ctx, taxdb.InsertCarryforwardApplicationParams{
					ID: uuid.New(), CarryforwardID: b.ID, TargetTaxYear: int64(taxYear), Amount: applied, Now: &now,
				}); err != nil {
					return err
				}
				toApply -= applied
			}
		}
		generated := 0.0
		if result.aggregationApplied && result.annualBalance.Sign() < 0 {
			generated = dd(result.annualBalance.Neg())
		}
		current, err := q.CarryforwardForYear(ctx, taxdb.CarryforwardForYearParams{UserID: userID, SourceTaxYear: int64(taxYear)})
		switch {
		case isNoRows(err):
			err = q.InsertCarryforward(ctx, taxdb.InsertCarryforwardParams{
				ID: uuid.New(), UserID: userID, Jurisdiction: string(PT), SourceTaxYear: int64(taxYear),
				ExpiresAfterTaxYear: int64(taxYear + 5), OriginalAmount: generated, RemainingAmount: max(0, generated),
				Currency: strings.ToUpper(currency), RuleVersion: ruleVersion, Now: &now,
			})
		case err == nil:
			var apps []taxdb.TaxLossCarryforwardApplication
			if apps, err = q.CarryforwardApplicationsOf(ctx, current.ID); err != nil {
				return err
			}
			err = q.UpdateCarryforward(ctx, taxdb.UpdateCarryforwardParams{
				ExpiresAfterTaxYear: int64(taxYear + 5), OriginalAmount: generated,
				RemainingAmount: max(0, generated-sumApplications(apps)), Currency: strings.ToUpper(currency),
				RuleVersion: ruleVersion, Now: &now, ID: current.ID,
			})
		}
		if err != nil {
			return err
		}
		for idx := range balances {
			b := &balances[idx]
			apps, err := q.CarryforwardApplicationsOf(ctx, b.ID)
			if err != nil {
				return err
			}
			if err := q.UpdateCarryforwardRemaining(ctx, taxdb.UpdateCarryforwardRemainingParams{
				RemainingAmount: max(0, b.OriginalAmount-sumApplications(apps)), Now: &now, ID: b.ID,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Germany loss ledgers: GermanyStockLossLedger and GermanyGeneralLossLedger
// are the same FIFO bucket walk over two table pairs.

type lossYear struct {
	id          uuid.UUID
	taxYear     int
	net         float64
	ruleVersion string
}

type lossApplication struct {
	id           uuid.UUID
	sourceYearID uuid.UUID
	target       int
	amount       float64
	createdAt    *time.Time
}

type lossYearResult struct {
	lossGenerated, priorApplied, taxableGain, carryforward float64
}

type lossTables struct {
	category     string
	yearFor      func(ctx context.Context, q *taxdb.Queries, userID uuid.UUID, year int) (*lossYear, error)
	years        func(ctx context.Context, q *taxdb.Queries, userID uuid.UUID, asOf *int) ([]lossYear, error)
	insertYear   func(ctx context.Context, q *taxdb.Queries, id, userID uuid.UUID, year int, net float64, rule string, now time.Time) error
	updateInput  func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, net float64, rule string, now time.Time) error
	updateResult func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, r lossYearResult, now time.Time) error
	applications func(ctx context.Context, q *taxdb.Queries, ids []uuid.UUID, sorted bool) ([]lossApplication, error)
	insertApp    func(ctx context.Context, q *taxdb.Queries, id, source uuid.UUID, target int, amount float64, now time.Time) error
	updateApp    func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, amount float64, now time.Time) error
	deleteApp    func(ctx context.Context, q *taxdb.Queries, id uuid.UUID) error
}

func stockYear(r taxdb.GermanyStockLossYear) lossYear {
	return lossYear{id: r.ID, taxYear: int(r.TaxYear), net: r.NetStockResult, ruleVersion: r.RuleVersion}
}

func generalYear(r taxdb.GermanyGeneralLossYear) lossYear {
	return lossYear{id: r.ID, taxYear: int(r.TaxYear), net: r.NetCapitalResult, ruleVersion: r.RuleVersion}
}

var stockLossTables = lossTables{
	category: "stock",
	yearFor: func(ctx context.Context, q *taxdb.Queries, userID uuid.UUID, year int) (*lossYear, error) {
		r, err := q.StockLossYearFor(ctx, taxdb.StockLossYearForParams{UserID: userID, TaxYear: int64(year)})
		if isNoRows(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		y := stockYear(r)
		return &y, nil
	},
	years: func(ctx context.Context, q *taxdb.Queries, userID uuid.UUID, asOf *int) ([]lossYear, error) {
		var rows []taxdb.GermanyStockLossYear
		var err error
		if asOf != nil {
			rows, err = q.StockLossYearsAsOf(ctx, taxdb.StockLossYearsAsOfParams{UserID: userID, TaxYear: int64(*asOf)})
		} else {
			rows, err = q.StockLossYears(ctx, userID)
		}
		out := make([]lossYear, len(rows))
		for i, r := range rows {
			out[i] = stockYear(r)
		}
		return out, err
	},
	insertYear: func(ctx context.Context, q *taxdb.Queries, id, userID uuid.UUID, year int, net float64, rule string, now time.Time) error {
		return q.InsertStockLossYear(ctx, taxdb.InsertStockLossYearParams{ID: id, UserID: userID, TaxYear: int64(year), NetStockResult: net, RuleVersion: rule, Now: &now})
	},
	updateInput: func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, net float64, rule string, now time.Time) error {
		return q.UpdateStockLossYearInput(ctx, taxdb.UpdateStockLossYearInputParams{NetStockResult: net, RuleVersion: rule, Now: &now, ID: id})
	},
	updateResult: func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, r lossYearResult, now time.Time) error {
		return q.UpdateStockLossYearResult(ctx, taxdb.UpdateStockLossYearResultParams{
			LossGenerated: r.lossGenerated, PriorLossApplied: r.priorApplied, TaxableGain: r.taxableGain,
			EndingLossCarryforward: r.carryforward, Now: &now, ID: id,
		})
	},
	applications: func(ctx context.Context, q *taxdb.Queries, ids []uuid.UUID, sorted bool) ([]lossApplication, error) {
		var rows []taxdb.GermanyStockLossApplication
		var err error
		if sorted {
			rows, err = q.StockLossApplicationsSorted(ctx, ids)
		} else {
			rows, err = q.StockLossApplications(ctx, ids)
		}
		out := make([]lossApplication, len(rows))
		for i, r := range rows {
			out[i] = lossApplication{id: r.ID, sourceYearID: r.SourceYearID, target: int(r.TargetTaxYear), amount: r.Amount, createdAt: r.CreatedAt}
		}
		return out, err
	},
	insertApp: func(ctx context.Context, q *taxdb.Queries, id, source uuid.UUID, target int, amount float64, now time.Time) error {
		return q.UpsertStockLossApplicationInsert(ctx, taxdb.UpsertStockLossApplicationInsertParams{ID: id, SourceYearID: source, TargetTaxYear: int64(target), Amount: amount, Now: &now})
	},
	updateApp: func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, amount float64, now time.Time) error {
		return q.UpdateStockLossApplication(ctx, taxdb.UpdateStockLossApplicationParams{Amount: amount, Now: &now, ID: id})
	},
	deleteApp: func(ctx context.Context, q *taxdb.Queries, id uuid.UUID) error {
		return q.DeleteStockLossApplication(ctx, id)
	},
}

var generalLossTables = lossTables{
	category: "general_capital",
	yearFor: func(ctx context.Context, q *taxdb.Queries, userID uuid.UUID, year int) (*lossYear, error) {
		r, err := q.GeneralLossYearFor(ctx, taxdb.GeneralLossYearForParams{UserID: userID, TaxYear: int64(year)})
		if isNoRows(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		y := generalYear(r)
		return &y, nil
	},
	years: func(ctx context.Context, q *taxdb.Queries, userID uuid.UUID, asOf *int) ([]lossYear, error) {
		var rows []taxdb.GermanyGeneralLossYear
		var err error
		if asOf != nil {
			rows, err = q.GeneralLossYearsAsOf(ctx, taxdb.GeneralLossYearsAsOfParams{UserID: userID, TaxYear: int64(*asOf)})
		} else {
			rows, err = q.GeneralLossYears(ctx, userID)
		}
		out := make([]lossYear, len(rows))
		for i, r := range rows {
			out[i] = generalYear(r)
		}
		return out, err
	},
	insertYear: func(ctx context.Context, q *taxdb.Queries, id, userID uuid.UUID, year int, net float64, rule string, now time.Time) error {
		return q.InsertGeneralLossYear(ctx, taxdb.InsertGeneralLossYearParams{ID: id, UserID: userID, TaxYear: int64(year), NetCapitalResult: net, RuleVersion: rule, Now: &now})
	},
	updateInput: func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, net float64, rule string, now time.Time) error {
		return q.UpdateGeneralLossYearInput(ctx, taxdb.UpdateGeneralLossYearInputParams{NetCapitalResult: net, RuleVersion: rule, Now: &now, ID: id})
	},
	updateResult: func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, r lossYearResult, now time.Time) error {
		return q.UpdateGeneralLossYearResult(ctx, taxdb.UpdateGeneralLossYearResultParams{
			LossGenerated: r.lossGenerated, PriorLossApplied: r.priorApplied, TaxableGain: r.taxableGain,
			EndingLossCarryforward: r.carryforward, Now: &now, ID: id,
		})
	},
	applications: func(ctx context.Context, q *taxdb.Queries, ids []uuid.UUID, sorted bool) ([]lossApplication, error) {
		var rows []taxdb.GermanyGeneralLossApplication
		var err error
		if sorted {
			rows, err = q.GeneralLossApplicationsSorted(ctx, ids)
		} else {
			rows, err = q.GeneralLossApplications(ctx, ids)
		}
		out := make([]lossApplication, len(rows))
		for i, r := range rows {
			out[i] = lossApplication{id: r.ID, sourceYearID: r.SourceYearID, target: int(r.TargetTaxYear), amount: r.Amount, createdAt: r.CreatedAt}
		}
		return out, err
	},
	insertApp: func(ctx context.Context, q *taxdb.Queries, id, source uuid.UUID, target int, amount float64, now time.Time) error {
		return q.InsertGeneralLossApplication(ctx, taxdb.InsertGeneralLossApplicationParams{ID: id, SourceYearID: source, TargetTaxYear: int64(target), Amount: amount, Now: &now})
	},
	updateApp: func(ctx context.Context, q *taxdb.Queries, id uuid.UUID, amount float64, now time.Time) error {
		return q.UpdateGeneralLossApplication(ctx, taxdb.UpdateGeneralLossApplicationParams{Amount: amount, Now: &now, ID: id})
	},
	deleteApp: func(ctx context.Context, q *taxdb.Queries, id uuid.UUID) error {
		return q.DeleteGeneralLossApplication(ctx, id)
	},
}

type germanyLedger struct {
	s *service
	t lossTables
}

type germanyLedgerResult struct {
	taxYear                                  int
	net, generated, applied, taxableGain, cf dec.Decimal
}

var errReconciliationMissing = errors.New("reconciliationResultMissing")

// balances is the per-source rows of response() (GermanyGeneralLossLedger.balances).
func (g germanyLedger) balances(ctx context.Context, userID uuid.UUID, asOf int) ([]LossCarryforwardBalance, error) {
	q := g.s.q
	years, err := g.t.years(ctx, q, userID, &asOf)
	if err != nil {
		return nil, err
	}
	bySource := map[uuid.UUID][]lossApplication{}
	if len(years) > 0 {
		ids := make([]uuid.UUID, len(years))
		for i, y := range years {
			ids[i] = y.id
		}
		apps, err := g.t.applications(ctx, q, ids, true)
		if err != nil {
			return nil, err
		}
		for _, a := range apps {
			bySource[a.sourceYearID] = append(bySource[a.sourceYearID], a)
		}
	}
	now := g.s.now()
	out := []LossCarryforwardBalance{}
	for _, y := range years {
		generated := dmax(zero, fd(y.net).Neg())
		if !(generated.Sign() > 0) {
			continue
		}
		applied := zero
		apps := []LossCarryforwardApplication{}
		for _, a := range bySource[y.id] {
			applied = applied.Add(fd(a.amount))
			apps = append(apps, LossCarryforwardApplication{
				ID: uuidString(a.id), TargetTaxYear: a.target, Amount: money(fd(a.amount), "EUR"),
				CreatedAt: isoSeconds(orNow(a.createdAt, now)),
			})
		}
		out = append(out, LossCarryforwardBalance{
			ID: uuidString(y.id), Jurisdiction: DE, SourceTaxYear: y.taxYear, ExpiresAfterTaxYear: 9999,
			OriginalAmount: money(generated, "EUR"), RemainingAmount: money(dmax(zero, generated.Sub(applied)), "EUR"),
			RuleVersion: y.ruleVersion, Category: strPtr(g.t.category), Applications: apps,
		})
	}
	return out, nil
}

// germanyLossResponse is GermanyStockLossLedger.response: stock and general
// balances together.
func (s *service) germanyLossResponse(ctx context.Context, userID uuid.UUID, asOf int) (LossCarryforwardLedger, error) {
	stock, err := germanyLedger{s, stockLossTables}.balances(ctx, userID, asOf)
	if err != nil {
		return LossCarryforwardLedger{}, err
	}
	general, err := germanyLedger{s, generalLossTables}.balances(ctx, userID, asOf)
	if err != nil {
		return LossCarryforwardLedger{}, err
	}
	combined := append(append([]LossCarryforwardBalance{}, stock...), general...)
	sort.SliceStable(combined, func(a, b int) bool {
		if combined[a].SourceTaxYear != combined[b].SourceTaxYear {
			return combined[a].SourceTaxYear < combined[b].SourceTaxYear
		}
		return *combined[a].Category < *combined[b].Category
	})
	total := zero
	for idx := range combined {
		b := &combined[idx]
		total = total.Add(b.RemainingAmount.Amount)
	}
	return LossCarryforwardLedger{
		GeneratedAt: isoSeconds(s.now()), Jurisdiction: DE, AsOfTaxYear: asOf,
		TotalAvailable: money(total, "EUR"), Balances: combined,
	}, nil
}

type lossBucket struct {
	source    uuid.UUID
	remaining dec.Decimal
}

func (g germanyLedger) reconcile(ctx context.Context, userID uuid.UUID, taxYear int, net dec.Decimal, ruleVersion string) (germanyLedgerResult, error) {
	var result *germanyLedgerResult
	now := g.s.now()
	err := g.s.inTx(ctx, func(q *taxdb.Queries) error {
		stored, err := g.t.yearFor(ctx, q, userID, taxYear)
		if err != nil {
			return err
		}
		if stored == nil {
			err = g.t.insertYear(ctx, q, uuid.New(), userID, taxYear, dd(net), ruleVersion, now)
		} else {
			err = g.t.updateInput(ctx, q, stored.id, dd(net), ruleVersion, now)
		}
		if err != nil {
			return err
		}
		years, err := g.t.years(ctx, q, userID, nil)
		if err != nil {
			return err
		}
		var existing []lossApplication
		if len(years) > 0 {
			ids := make([]uuid.UUID, len(years))
			for i, y := range years {
				ids[i] = y.id
			}
			if existing, err = g.t.applications(ctx, q, ids, false); err != nil {
				return err
			}
		}
		existingByKey := map[string]lossApplication{}
		for _, a := range existing {
			existingByKey[uuidString(a.sourceYearID)+":"+itoa(a.target)] = a
		}
		retained := map[uuid.UUID]bool{}
		var buckets []lossBucket
		for _, y := range years {
			annual := fd(y.net)
			gain := dmax(zero, annual)
			generated := dmax(zero, annual.Neg())
			remainingGain, applied := gain, zero
			for i := range buckets {
				if !(remainingGain.Sign() > 0) {
					break
				}
				amount := dmin(remainingGain, buckets[i].remaining)
				if !(amount.Sign() > 0) {
					continue
				}
				buckets[i].remaining = buckets[i].remaining.Sub(amount)
				remainingGain = remainingGain.Sub(amount)
				applied = applied.Add(amount)
				key := uuidString(buckets[i].source) + ":" + itoa(y.taxYear)
				if a, ok := existingByKey[key]; ok {
					err = g.t.updateApp(ctx, q, a.id, dd(amount), now)
					retained[a.id] = true
				} else {
					id := uuid.New()
					err = g.t.insertApp(ctx, q, id, buckets[i].source, y.taxYear, dd(amount), now)
					retained[id] = true
				}
				if err != nil {
					return err
				}
			}
			taxable := gain.Sub(applied)
			if generated.Sign() > 0 {
				buckets = append(buckets, lossBucket{source: y.id, remaining: generated})
			}
			cf := zero
			for _, b := range buckets {
				cf = cf.Add(b.remaining)
			}
			if err := g.t.updateResult(ctx, q, y.id, lossYearResult{
				lossGenerated: dd(generated), priorApplied: dd(applied), taxableGain: dd(taxable), carryforward: dd(cf),
			}, now); err != nil {
				return err
			}
			if y.taxYear == taxYear {
				result = &germanyLedgerResult{taxYear: y.taxYear, net: annual, generated: generated, applied: applied, taxableGain: taxable, cf: cf}
			}
		}
		for _, a := range existing {
			if !retained[a.id] {
				if err := g.t.deleteApp(ctx, q, a.id); err != nil {
					return err
				}
			}
		}
		if result == nil {
			return errReconciliationMissing
		}
		return nil
	})
	if err != nil {
		return germanyLedgerResult{}, err
	}
	return *result, nil
}

// reconcileFundAdvances is GermanyFundAdvanceAllocationService.reconcile.
func (s *service) reconcileFundAdvances(ctx context.Context, requested map[uuid.UUID]bool) (map[uuid.UUID]dec.Decimal, error) {
	out := map[uuid.UUID]dec.Decimal{}
	if len(requested) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(requested))
	for id := range requested {
		ids = append(ids, id)
	}
	disposals, err := s.q.DisposalsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	lotSet := map[uuid.UUID]bool{}
	lotIDs := []uuid.UUID{}
	for _, d := range disposals {
		if !lotSet[d.LotID] {
			lotSet[d.LotID] = true
			lotIDs = append(lotIDs, d.LotID)
		}
	}
	if len(lotIDs) == 0 {
		return out, nil
	}
	now := s.now()
	err = s.inTx(ctx, func(q *taxdb.Queries) error {
		holdings, err := q.FundAnnualHoldingsForLots(ctx, lotIDs)
		if err != nil || len(holdings) == 0 {
			return err
		}
		all, err := q.DisposalsForLots(ctx, lotIDs)
		if err != nil {
			return err
		}
		txSet := map[uuid.UUID]bool{}
		txIDs := []uuid.UUID{}
		for _, d := range all {
			if !txSet[d.TransactionID] {
				txSet[d.TransactionID] = true
				txIDs = append(txIDs, d.TransactionID)
			}
		}
		txs, err := q.TransactionsByIDs(ctx, txIDs)
		if err != nil {
			return err
		}
		tradeDate := map[uuid.UUID]time.Time{}
		for idx := range txs {
			t := &txs[idx]
			tradeDate[t.ID] = t.TradeDate
		}
		byLot := map[uuid.UUID][]taxdb.LotDisposal{}
		for _, d := range all {
			byLot[d.LotID] = append(byLot[d.LotID], d)
		}
		distantFuture := time.Date(4001, 1, 1, 0, 0, 0, 0, time.UTC)
		dateOf := func(d taxdb.LotDisposal) time.Time {
			if t, ok := tradeDate[d.TransactionID]; ok {
				return t
			}
			return distantFuture
		}
		for lot := range byLot {
			ds := byLot[lot]
			sort.SliceStable(ds, func(a, b int) bool {
				la, lb := dateOf(ds[a]), dateOf(ds[b])
				if !la.Equal(lb) {
					return la.Before(lb)
				}
				return uuidString(ds[a].ID) < uuidString(ds[b].ID)
			})
		}
		holdingIDs := make([]uuid.UUID, len(holdings))
		for i := range holdings {
			h := &holdings[i]
			holdingIDs[i] = h.ID
		}
		existing, err := q.FundAdvanceAllocationsForHoldings(ctx, holdingIDs)
		if err != nil {
			return err
		}
		existingByKey := map[string]taxdb.GermanyFundAdvanceAllocation{}
		for _, a := range existing {
			existingByKey[uuidString(a.AnnualHoldingID)+":"+uuidString(a.DisposalID)] = a
		}
		retained := map[uuid.UUID]bool{}
		for idx := range holdings {
			h := &holdings[idx]
			if h.LotID == nil || h.Quantity == nil || !(*h.Quantity > 0) {
				continue
			}
			remainingQty := *h.Quantity
			remainingGross := h.GrossAdvanceLumpSum
			perUnit := h.GrossAdvanceLumpSum / *h.Quantity
			deemed := int(h.CalculationYear) + 1
			for _, d := range byLot[*h.LotID] {
				t, ok := tradeDate[d.TransactionID]
				if !ok || t.UTC().Year() < deemed || !(remainingQty > 0) {
					continue
				}
				qty := min(d.Quantity, remainingQty)
				amount := min(remainingGross, qty*perUnit)
				if !(qty > 0) || !(amount > 0) {
					continue
				}
				remainingQty -= qty
				remainingGross -= amount
				key := uuidString(h.ID) + ":" + uuidString(d.ID)
				if a, ok := existingByKey[key]; ok {
					err = q.UpdateFundAdvanceAllocation(ctx, taxdb.UpdateFundAdvanceAllocationParams{Quantity: qty, GrossAdvanceAmount: amount, Now: &now, ID: a.ID})
					retained[a.ID] = true
				} else {
					id := uuid.New()
					err = q.InsertFundAdvanceAllocation(ctx, taxdb.InsertFundAdvanceAllocationParams{
						ID: id, AnnualHoldingID: h.ID, DisposalID: d.ID, Quantity: qty, GrossAdvanceAmount: amount, Now: &now,
					})
					retained[id] = true
				}
				if err != nil {
					return err
				}
				if requested[d.ID] {
					prev, ok := out[d.ID]
					if !ok {
						prev = zero
					}
					out[d.ID] = prev.Add(fd(amount))
				}
			}
			rq := remainingQty
			if err := q.UpdateFundAnnualHoldingRemaining(ctx, taxdb.UpdateFundAnnualHoldingRemainingParams{
				RemainingQuantity: &rq, RemainingGrossAdvance: max(0, remainingGross), Now: &now, ID: h.ID,
			}); err != nil {
				return err
			}
		}
		for _, a := range existing {
			if !retained[a.ID] {
				if err := q.DeleteFundAdvanceAllocation(ctx, a.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return out, err
}
