package expenses

import (
	"context"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
)

func pillarRank(p BudgetPillar) int {
	switch p {
	case PillarFundamentals:
		return 0
	case PillarFutureYou:
		return 1
	case PillarFun:
		return 2
	}
	return 3
}

// sortedPillars is sortedPillars(_:): standard pillars first, then custom
// keys case-insensitively.
func sortedPillars(set map[BudgetPillar]struct{}) []BudgetPillar {
	out := make([]BudgetPillar, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := pillarRank(out[i]), pillarRank(out[j])
		if ri != rj {
			return ri < rj
		}
		li, lj := strings.ToLower(string(out[i])), strings.ToLower(string(out[j]))
		if li != lj {
			return li < lj
		}
		return out[i] < out[j]
	})
	return out
}

func resolvedPillars(snap expensesdb.BudgetSnapshot, items []expensesdb.BudgetPlanItem, expenses []expensesdb.Expense) []BudgetPillar {
	set := map[BudgetPillar]struct{}{}
	for _, p := range AllPillars {
		set[p] = struct{}{}
	}
	for k := range decodeShares(snap.TargetShares) {
		if p, ok := ParsePillar(k); ok {
			set[p] = struct{}{}
		}
	}
	for k := range items {
		it := &items[k]
		set[pillarFromDB(it.Pillar)] = struct{}{}
	}
	for k := range expenses {
		e := &expenses[k]
		set[pillarFromDB(e.Pillar)] = struct{}{}
	}
	return sortedPillars(set)
}

func widen(t *time.Time, days int) *time.Time {
	if t == nil {
		return nil
	}
	return timePtr(t.Add(time.Duration(days) * 24 * time.Hour))
}

func inRange(t time.Time, from, to *time.Time) bool {
	return (from == nil || !t.Before(*from)) && (to == nil || !t.After(*to))
}

// GetMonthlyReports is getMonthlyReports: one summary per snapshot in
// range, oldest first.
func (s *Service) GetMonthlyReports(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, from, to *time.Time) ([]BudgetMonthSummaryResponse, error) {
	qq := q(db)
	allSnapshots, err := qq.ListSnapshotsByUserRange(ctx, expensesdb.ListSnapshotsByUserRangeParams{
		UserID: userID, FromAt: widen(from, -1), ToAt: widen(to, 1),
	})
	if err != nil {
		return nil, err
	}
	items, err := qq.ListPlanItemsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	allExpenses, err := qq.ListExpensesRange(ctx, expensesdb.ListExpensesRangeParams{
		UserID: userID, FromAt: widen(from, -1), ToAt: widen(to, 1),
	})
	if err != nil {
		return nil, err
	}
	var snapshots []expensesdb.BudgetSnapshot
	for k := range allSnapshots {
		sn := &allSnapshots[k]
		if inRange(sn.MonthStart, from, to) {
			snapshots = append(snapshots, *sn)
		}
	}
	itemsBySnapshot := map[uuid.UUID][]expensesdb.BudgetPlanItem{}
	for k := range items {
		it := &items[k]
		itemsBySnapshot[it.SnapshotID] = append(itemsBySnapshot[it.SnapshotID], *it)
	}
	expensesByMonth := map[time.Time][]expensesdb.Expense{}
	for k := range allExpenses {
		e := &allExpenses[k]
		if inRange(e.OccurredOn, from, to) {
			m := MonthStart(e.OccurredOn)
			expensesByMonth[m] = append(expensesByMonth[m], *e)
		}
	}
	sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].MonthStart.Before(snapshots[j].MonthStart) })

	summaries := make([]BudgetMonthSummaryResponse, 0, len(snapshots))
	for k := range snapshots {
		sn := &snapshots[k]
		monthStart := MonthStart(sn.MonthStart)
		sItems := itemsBySnapshot[sn.ID]
		mExpenses := expensesByMonth[monthStart]
		var planned, actual, myPlanned, partnerPlanned, myActual, partnerActual float64
		for k := range sItems {
			it := &sItems[k]
			planned += it.PlannedAmount
		}
		for k := range mExpenses {
			e := &mExpenses[k]
			actual += e.Amount
		}
		for k := range sItems {
			it := &sItems[k]
			myPlanned += userPortion(it.PlannedAmount, it.SplitMode, it.UserSharePercent)
			partnerPlanned += partnerPortion(it.PlannedAmount, it.SplitMode, it.UserSharePercent)
		}
		for k := range mExpenses {
			e := &mExpenses[k]
			myActual += userPortion(e.Amount, e.SplitMode, e.UserSharePercent)
			partnerActual += partnerPortion(e.Amount, e.SplitMode, e.UserSharePercent)
		}
		sum := BudgetMonthSummaryResponse{
			MonthStart: FormatDay(monthStart), Planned: planned, Actual: actual, Salary: sn.NetSalary,
			MyPlanned: myPlanned, PartnerPlanned: partnerPlanned, MyActual: myActual, PartnerActual: partnerActual,
			PillarActuals: map[string]float64{}, PillarPlans: map[string]float64{},
			MyPillarActuals: map[string]float64{}, PartnerPillarActuals: map[string]float64{},
			MyPillarPlans: map[string]float64{}, PartnerPillarPlans: map[string]float64{},
		}
		for _, p := range resolvedPillars(*sn, sItems, mExpenses) {
			var pa, mpa, ppa float64
			for k := range sItems {
				it := &sItems[k]
				if pillarFromDB(it.Pillar) != p {
					continue
				}
				pa += it.PlannedAmount
				mpa += userPortion(it.PlannedAmount, it.SplitMode, it.UserSharePercent)
				ppa += partnerPortion(it.PlannedAmount, it.SplitMode, it.UserSharePercent)
			}
			key := string(p)
			sum.PillarPlans[key], sum.MyPillarPlans[key], sum.PartnerPillarPlans[key] = pa, mpa, ppa
			var aa, maa, paa float64
			for k := range mExpenses {
				e := &mExpenses[k]
				if pillarFromDB(e.Pillar) != p {
					continue
				}
				aa += e.Amount
				maa += userPortion(e.Amount, e.SplitMode, e.UserSharePercent)
				paa += partnerPortion(e.Amount, e.SplitMode, e.UserSharePercent)
			}
			sum.PillarActuals[key], sum.MyPillarActuals[key], sum.PartnerPillarActuals[key] = aa, maa, paa
		}
		summaries = append(summaries, sum)
	}
	return summaries, nil
}

// GetYearlyReports is getYearlyReports: the monthly summaries summed per
// UTC year, ascending.
func (s *Service) GetYearlyReports(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, from, to *time.Time) ([]BudgetYearSummaryResponse, error) {
	monthly, err := s.GetMonthlyReports(ctx, db, userID, from, to)
	if err != nil {
		return nil, err
	}
	byYear := map[int]*BudgetYearSummaryResponse{}
	var years []int
	for _, r := range monthly {
		d, ok := ParseDay(r.MonthStart)
		if !ok {
			continue
		}
		y := d.Year()
		acc, seen := byYear[y]
		if !seen {
			acc = &BudgetYearSummaryResponse{Year: y}
			byYear[y] = acc
			years = append(years, y)
		}
		acc.Planned += r.Planned
		acc.Actual += r.Actual
		acc.Salary += r.Salary
		acc.MyPlanned += r.MyPlanned
		acc.PartnerPlanned += r.PartnerPlanned
		acc.MyActual += r.MyActual
		acc.PartnerActual += r.PartnerActual
	}
	sort.Ints(years)
	out := make([]BudgetYearSummaryResponse, 0, len(years))
	for _, y := range years {
		out = append(out, *byYear[y])
	}
	return out, nil
}

// GetPillarPlanningSummaries is getPillarPlanningSummaries.
func (s *Service) GetPillarPlanningSummaries(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, monthStart time.Time) ([]PillarPlanningSummaryResponse, error) {
	ms := MonthStart(monthStart)
	next := ms.AddDate(0, 1, 0)
	snap, err := s.FindSnapshot(ctx, db, userID, ms)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return []PillarPlanningSummaryResponse{}, nil
	}
	items, err := q(db).ListPlanItemsByUserSnapshot(ctx, expensesdb.ListPlanItemsByUserSnapshotParams{UserID: userID, SnapshotID: snap.ID})
	if err != nil {
		return nil, err
	}
	monthExpenses, err := q(db).ListExpensesInMonth(ctx, expensesdb.ListExpensesInMonthParams{UserID: userID, FromAt: ms, BeforeAt: next})
	if err != nil {
		return nil, err
	}
	shares := decodeShares(snap.TargetShares)
	pillars := resolvedPillars(*snap, items, monthExpenses)
	out := make([]PillarPlanningSummaryResponse, 0, len(pillars))
	for _, p := range pillars {
		var planned []expensesdb.BudgetPlanItem
		for k := range items {
			if pillarFromDB(items[k].Pillar) == p {
				planned = append(planned, items[k])
			}
		}
		share, ok := shares[string(p)]
		if !ok {
			share = DefaultTargetShare(p)
		}
		var plannedAmount, actual, unplanned float64
		for k := range planned {
			it := &planned[k]
			plannedAmount += it.PlannedAmount
		}
		for k := range monthExpenses {
			e := &monthExpenses[k]
			if pillarFromDB(e.Pillar) != p {
				continue
			}
			actual += e.Amount
			isUnplanned := true
			if e.LinkedItemID != nil {
				for k := range planned {
					it := &planned[k]
					if it.ID == *e.LinkedItemID {
						isUnplanned = false
						break
					}
				}
			} else {
				title := normalizeBudgetKey(e.Title)
				for k := range planned {
					it := &planned[k]
					if normalizeBudgetKey(it.Title) == title {
						isUnplanned = false
						break
					}
				}
			}
			if isUnplanned {
				unplanned += e.Amount
			}
		}
		out = append(out, PillarPlanningSummaryResponse{
			Pillar: p, TargetAmount: snap.NetSalary * share, PlannedAmount: plannedAmount,
			ActualAmount: actual, UnplannedActualAmount: unplanned,
		})
	}
	return out, nil
}
