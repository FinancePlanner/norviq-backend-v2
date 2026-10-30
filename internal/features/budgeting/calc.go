package budgeting

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/budgetingdb"

	"github.com/google/uuid"
)

func finite(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) }

// roundHalfAway is Swift's Double.rounded() (toNearestOrAwayFromZero).
func roundHalfAway(v float64) float64 { return math.Round(v) }

// roundedFor is BudgetingEngineService.rounded(_:currencyCode:).
func roundedFor(amount float64, currency string) float64 {
	switch strings.ToUpper(currency) {
	case "JPY", "KRW", "VND":
		return roundHalfAway(amount)
	}
	return roundHalfAway(amount*100) / 100
}

// monthStartOf is the first instant of t's UTC month.
func monthStartOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// addMonths is Calendar.date(byAdding: .month, ...) in UTC: the day clamps to
// the target month's length instead of overflowing as time.AddDate does.
func addMonths(t time.Time, n int) time.Time {
	t = t.UTC()
	first := time.Date(t.Year(), t.Month(), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC).AddDate(0, n, 0)
	last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return first.AddDate(0, 0, min(t.Day(), last)-1)
}

// dateString is BudgetingEngineService.dateString (yyyy-MM-dd, UTC).
func dateString(t time.Time) string { return t.UTC().Format("2006-01-02") }

// isoString is ISO8601DateFormatter's default output (whole seconds, Z).
func isoString(t time.Time) string { return apijson.FormatTime(t) }

func effectiveAmount(amount float64, split string, share float64) float64 {
	if split == splitShared {
		return amount * share / 100
	}
	return amount
}

func driftLevel(drift float64, percent *float64, target, threshold float64) string {
	if drift <= 0 {
		return levelGreen
	}
	if target <= 0 || percent == nil {
		return levelRed
	}
	if *percent > threshold {
		return levelRed
	}
	return levelYellow
}

func levelRank(level string) int {
	switch level {
	case levelRed:
		return 2
	case levelYellow:
		return 1
	}
	return 0
}

func normalizeTitle(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// capitalized is NSString.capitalized for an already-lowercased title: the
// first letter of every whitespace-delimited word uppercased.
func capitalized(v string) string {
	var b strings.Builder
	start := true
	for _, r := range v {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			start = true
			b.WriteRune(r)
			continue
		}
		if start {
			b.WriteString(strings.ToUpper(string(r)))
			start = false
		} else {
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}

func uuidString(id uuid.UUID) string { return apijson.FormatUUID(id) }

func optUUIDString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := uuidString(*id)
	return &s
}

func ptr[T any](v T) *T { return &v }

// calculate is BudgetingEngineService.calculate.
func calculate(snapshot budgetingdb.BudgetSnapshot, items []budgetingdb.BudgetPlanItem, expenses []budgetingdb.ExpensesInRangeRow) BudgetDriftDashboard {
	actualByItem := map[uuid.UUID]float64{}
	actualByCategory := map[uuid.UUID]float64{}
	actualByTitle := map[string]float64{}
	// First-seen order for the unbudgeted rows. Swift iterated its
	// dictionaries in hash order (random per process); the final sort only
	// orders by level and drift, so ties among unbudgeted rows were
	// arbitrary in Vapor and follow expense order here.
	var categoryOrder []uuid.UUID
	var titleOrder []string
	investmentIDs := map[uuid.UUID]bool{}
	for i := range items {
		if it := &items[i]; it.AllocationKind == kindInvestment {
			investmentIDs[it.ID] = true
		}
	}
	totalActual := 0.0
	for _, e := range expenses {
		amount := effectiveAmount(e.Amount, string(e.SplitMode), e.UserSharePercent)
		switch {
		case e.LinkedItemID != nil:
			actualByItem[*e.LinkedItemID] += amount
			if investmentIDs[*e.LinkedItemID] {
				continue
			}
		case e.CategoryID != nil:
			if _, ok := actualByCategory[*e.CategoryID]; !ok {
				categoryOrder = append(categoryOrder, *e.CategoryID)
			}
			actualByCategory[*e.CategoryID] += amount
		default:
			key := normalizeTitle(e.Title)
			if _, ok := actualByTitle[key]; !ok {
				titleOrder = append(titleOrder, key)
			}
			actualByTitle[key] += amount
		}
		totalActual += amount
	}

	consumedCategories := map[uuid.UUID]bool{}
	consumedTitles := map[string]bool{}
	// Several plan items can draw on one category (or title). Their spend is
	// shared in proportion to their targets (equally when every target is
	// zero), so it is counted once. Vapor gave each item the full amount.
	type pool struct {
		targets float64
		items   int
	}
	poolKey := func(it *budgetingdb.BudgetPlanItem) (string, bool) {
		if _, linked := actualByItem[it.ID]; linked {
			return "", false
		}
		if it.CategoryID != nil {
			return "c:" + it.CategoryID.String(), true
		}
		return "t:" + normalizeTitle(it.Title), true
	}
	pools := map[string]*pool{}
	for i := range items {
		it := &items[i]
		if key, ok := poolKey(it); ok {
			p := pools[key]
			if p == nil {
				p = &pool{}
				pools[key] = p
			}
			p.targets += effectiveAmount(it.PlannedAmount, string(it.SplitMode), it.UserSharePercent)
			p.items++
		}
	}
	share := func(it *budgetingdb.BudgetPlanItem, target, spend float64) float64 {
		key, _ := poolKey(it)
		p := pools[key]
		if p.items <= 1 {
			return spend
		}
		if p.targets > 0 {
			return spend * target / p.targets
		}
		return spend / float64(p.items)
	}
	rows := make([]BudgetCategoryDrift, 0, len(items))
	for i := range items {
		it := &items[i]
		target := effectiveAmount(it.PlannedAmount, string(it.SplitMode), it.UserSharePercent)
		var actual float64
		if linked, ok := actualByItem[it.ID]; ok {
			actual = linked
		} else if it.CategoryID != nil {
			actual = share(it, target, actualByCategory[*it.CategoryID])
			consumedCategories[*it.CategoryID] = true
		} else {
			key := normalizeTitle(it.Title)
			actual = share(it, target, actualByTitle[key])
			consumedTitles[key] = true
		}
		rows = append(rows, driftRow(it, target, actual, snapshot.CategoryDriftThreshold))
	}
	for _, cat := range categoryOrder {
		if consumedCategories[cat] {
			continue
		}
		rows = append(rows, unbudgetedRow("unbudgeted-category:"+uuidString(cat), "Unbudgeted spending",
			optUUIDString(&cat), actualByCategory[cat], snapshot.CategoryDriftThreshold))
	}
	for _, title := range titleOrder {
		if consumedTitles[title] {
			continue
		}
		rows = append(rows, unbudgetedRow("unbudgeted-title:"+title, capitalized(title), nil,
			actualByTitle[title], snapshot.CategoryDriftThreshold))
	}

	totalTarget, investmentTarget := 0.0, 0.0
	for _, r := range rows {
		if r.AllocationKind == kindExpense {
			totalTarget += r.TargetAmount
		}
	}
	for _, r := range rows {
		if r.AllocationKind == kindInvestment {
			investmentTarget += r.TargetAmount
		}
	}
	totalDrift := totalActual - totalTarget
	var totalPercent *float64
	if totalTarget > 0 {
		totalPercent = ptr(totalDrift / totalTarget * 100)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		li, lj := levelRank(rows[i].Level), levelRank(rows[j].Level)
		return li > lj || (rows[i].Level == rows[j].Level && rows[i].DriftAmount > rows[j].DriftAmount)
	})
	cur := snapshot.CurrencyCode
	return BudgetDriftDashboard{
		SnapshotID:                   uuidString(snapshot.ID),
		MonthStart:                   dateString(snapshot.MonthStart),
		CurrencyCode:                 cur,
		Revision:                     int(snapshot.Revision),
		TotalTarget:                  roundedFor(totalTarget, cur),
		TotalActual:                  roundedFor(totalActual, cur),
		TotalDriftAmount:             roundedFor(totalDrift, cur),
		TotalDriftPercent:            totalPercent,
		TotalLevel:                   driftLevel(totalDrift, totalPercent, totalTarget, snapshot.TotalDriftThreshold),
		InvestmentContributionTarget: roundedFor(investmentTarget, cur),
		LostInvestmentCapital:        roundedFor(max(totalDrift, 0), cur),
		Categories:                   rows,
	}
}

func driftRow(it *budgetingdb.BudgetPlanItem, target, actual, defaultThreshold float64) BudgetCategoryDrift {
	drift := actual - target
	var percent *float64
	if target > 0 {
		percent = ptr(drift / target * 100)
	}
	threshold := defaultThreshold
	if it.ThresholdOverride != nil {
		threshold = *it.ThresholdOverride
	}
	return BudgetCategoryDrift{
		ID: uuidString(it.ID), Title: it.Title, CategoryID: optUUIDString(it.CategoryID),
		TargetAmount: target, ActualAmount: actual, DriftAmount: drift, DriftPercent: percent,
		Threshold: threshold, Level: driftLevel(drift, percent, target, threshold),
		AllocationKind: it.AllocationKind, ReallocationEligible: it.ReallocationEligible,
	}
}

func unbudgetedRow(id, title string, categoryID *string, actual, threshold float64) BudgetCategoryDrift {
	return BudgetCategoryDrift{
		ID: id, Title: title, CategoryID: categoryID, TargetAmount: 0, ActualAmount: actual,
		DriftAmount: actual, Threshold: threshold, Level: levelRed, AllocationKind: kindExpense,
	}
}

// decodeValues reads a {"values": [...]} payload, or the legacy bare array
// (BudgetReallocationAdjustmentsPayload / BudgetTemplateItemsPayload).
func decodeValues[T any](raw []byte) ([]T, error) {
	var legacy []T
	if err := json.Unmarshal(raw, &legacy); err == nil && legacy != nil {
		return legacy, nil
	}
	var wrapped struct {
		Values []T `json:"values"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	if wrapped.Values == nil {
		wrapped.Values = []T{}
	}
	return wrapped.Values, nil
}

func encodeValues[T any](values []T) ([]byte, error) {
	if values == nil {
		values = []T{}
	}
	return json.Marshal(struct {
		Values []T `json:"values"`
	}{values})
}
