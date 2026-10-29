package budgeting

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/httpx"
)

// Raw values of the StockPlanShared enums this domain stores and sends.
const (
	targetFixed            = "fixed"
	targetPercentageIncome = "percentageIncome"

	kindExpense    = "expense"
	kindInvestment = "investmentContribution"

	levelGreen  = "green"
	levelYellow = "yellow"
	levelRed    = "red"

	splitShared = "shared"
)

// BudgetCategoryDrift is StockPlanShared.BudgetCategoryDrift.
type BudgetCategoryDrift struct {
	ID                   string   `json:"id"`
	Title                string   `json:"title"`
	CategoryID           *string  `json:"categoryId,omitempty"`
	TargetAmount         float64  `json:"targetAmount"`
	ActualAmount         float64  `json:"actualAmount"`
	DriftAmount          float64  `json:"driftAmount"`
	DriftPercent         *float64 `json:"driftPercent,omitempty"`
	Threshold            float64  `json:"threshold"`
	Level                string   `json:"level"`
	AllocationKind       string   `json:"allocationKind"`
	ReallocationEligible bool     `json:"reallocationEligible"`
}

// BudgetDriftDashboard is StockPlanShared.BudgetDriftDashboard.
type BudgetDriftDashboard struct {
	SnapshotID                   string                `json:"snapshotId"`
	MonthStart                   string                `json:"monthStart"`
	CurrencyCode                 string                `json:"currencyCode"`
	Revision                     int                   `json:"revision"`
	TotalTarget                  float64               `json:"totalTarget"`
	TotalActual                  float64               `json:"totalActual"`
	TotalDriftAmount             float64               `json:"totalDriftAmount"`
	TotalDriftPercent            *float64              `json:"totalDriftPercent,omitempty"`
	TotalLevel                   string                `json:"totalLevel"`
	InvestmentContributionTarget float64               `json:"investmentContributionTarget"`
	LostInvestmentCapital        float64               `json:"lostInvestmentCapital"`
	Categories                   []BudgetCategoryDrift `json:"categories"`
}

// budgetDisciplineMonth is BudgetDisciplineMonth (its `id` is computed and
// not encoded).
type budgetDisciplineMonth struct {
	MonthStart string   `json:"monthStart"`
	Score      *float64 `json:"score,omitempty"`
	Compliant  *bool    `json:"compliant,omitempty"`
}

type budgetDisciplineSummary struct {
	CurrentScore         *float64                `json:"currentScore,omitempty"`
	CompletedMonthStreak int                     `json:"completedMonthStreak"`
	CompliantMonths      int                     `json:"compliantMonths"`
	EvaluatedMonths      int                     `json:"evaluatedMonths"`
	Months               []budgetDisciplineMonth `json:"months"`
}

// reallocationAdjustment is BudgetReallocationAdjustment. It is also the
// element stored in budget_reallocation_events.adjustments.
type reallocationAdjustment struct {
	PlanItemID string  `json:"planItemId"`
	Amount     float64 `json:"amount"`
}

type reallocationPreviewResponse struct {
	EffectiveMonth         string   `json:"effectiveMonth"`
	FreedCapital           float64  `json:"freedCapital"`
	AnnualImpact           float64  `json:"annualImpact"`
	InvestmentTargetBefore float64  `json:"investmentTargetBefore"`
	InvestmentTargetAfter  float64  `json:"investmentTargetAfter"`
	Warnings               []string `json:"warnings"`
}

type reallocationEventResponse struct {
	ID               string                   `json:"id"`
	RequestID        string                   `json:"requestId"`
	SourceSnapshotID string                   `json:"sourceSnapshotId"`
	TargetSnapshotID string                   `json:"targetSnapshotId"`
	EffectiveMonth   string                   `json:"effectiveMonth"`
	FreedCapital     float64                  `json:"freedCapital"`
	FinancialGoalID  *string                  `json:"financialGoalId,omitempty"`
	PortfolioListID  *string                  `json:"portfolioListId,omitempty"`
	Adjustments      []reallocationAdjustment `json:"adjustments"`
	CreatedAt        *string                  `json:"createdAt,omitempty"`
}

// templateItem is BudgetTemplateItem, as sent and as stored in
// budget_templates.items.
type templateItem struct {
	Title                string   `json:"title"`
	PlannedAmount        float64  `json:"plannedAmount"`
	Pillar               string   `json:"pillar"`
	CategoryID           *string  `json:"categoryId,omitempty"`
	TargetType           string   `json:"targetType"`
	IncomePercentage     *float64 `json:"incomePercentage,omitempty"`
	ThresholdOverride    *float64 `json:"thresholdOverride,omitempty"`
	AllocationKind       string   `json:"allocationKind"`
	ReallocationEligible bool     `json:"reallocationEligible"`
}

type templateResponse struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Items     []templateItem `json:"items"`
	CreatedAt *string        `json:"createdAt,omitempty"`
	UpdatedAt *string        `json:"updatedAt,omitempty"`
}

// snapshotResponse is BudgetSnapshotResponse as the alert-policy route
// builds it (no timestamps).
type snapshotResponse struct {
	ID                     string             `json:"id"`
	MonthStart             string             `json:"monthStart"`
	NetSalary              float64            `json:"netSalary"`
	TargetShares           map[string]float64 `json:"targetShares"`
	CurrencyCode           string             `json:"currencyCode"`
	CategoryDriftThreshold float64            `json:"categoryDriftThreshold"`
	TotalDriftThreshold    float64            `json:"totalDriftThreshold"`
	AlertsEnabled          bool               `json:"alertsEnabled"`
	AlertOnUnbudgeted      bool               `json:"alertOnUnbudgeted"`
	Revision               int                `json:"revision"`
}

// SpendToUnitsCategory is StockPlanShared.SpendToUnitsCategory.
type spendToUnitsCategory struct {
	Title           string   `json:"title"`
	OverspendAmount float64  `json:"overspendAmount"`
	Units           *float64 `json:"units,omitempty"`
}

type spendToUnitsCapacity struct {
	Symbol        string                 `json:"symbol"`
	ResolvedFrom  string                 `json:"resolvedFrom"`
	Price         *float64               `json:"price,omitempty"`
	PriceCurrency *string                `json:"priceCurrency,omitempty"`
	QuoteAsOf     *string                `json:"quoteAsOf,omitempty"`
	QuoteStale    bool                   `json:"quoteStale"`
	SurplusAmount float64                `json:"surplusAmount"`
	SurplusUnits  *float64               `json:"surplusUnits,omitempty"`
	CurrencyCode  string                 `json:"currencyCode"`
	Categories    []spendToUnitsCategory `json:"categories"`
	Disclaimer    string                 `json:"disclaimer"`
}

// SpendToUnitsMath constants.
const (
	defaultDcaSymbol  = "VWCE"
	staleQuoteSeconds = 30 * 60
	spendDisclaimer   = "Equivalent at last price. Not an order. Not financial advice."
)

// --- request decoding --------------------------------------------------------
//
// Swift's synthesized Decodable requires every non-optional property, and a
// missing key or a wrong type is a 400 with DecodingError's description.
// The wire structs below hold pointers so absence is visible; each has a
// validate method that reproduces the keyNotFound reason Vapor sent.

// keyNotFound is Vapor's DecodingError.keyNotFound reason. path is the
// coding path of the container ("" at the top level).
func keyNotFound(key, path string) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, path, key, key))
}

func dataCorrupted(path, desc string) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. %s", path, desc))
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func indexPath(parent string, i int) string {
	return joinPath(parent, fmt.Sprintf("Index %d", i))
}

type alertPolicyWire struct {
	CategoryThreshold *float64 `json:"categoryThreshold"`
	TotalThreshold    *float64 `json:"totalThreshold"`
	AlertsEnabled     *bool    `json:"alertsEnabled"`
	AlertOnUnbudgeted *bool    `json:"alertOnUnbudgeted"`
}

func (w alertPolicyWire) validate() error {
	switch {
	case w.CategoryThreshold == nil:
		return keyNotFound("categoryThreshold", "")
	case w.TotalThreshold == nil:
		return keyNotFound("totalThreshold", "")
	case w.AlertsEnabled == nil:
		return keyNotFound("alertsEnabled", "")
	case w.AlertOnUnbudgeted == nil:
		return keyNotFound("alertOnUnbudgeted", "")
	}
	return nil
}

type dcaSymbolWire struct {
	Symbol *string `json:"symbol"`
}

type bulkItemWire struct {
	ID                   *string  `json:"id"`
	PlannedAmount        *float64 `json:"plannedAmount"`
	TargetType           *string  `json:"targetType"`
	IncomePercentage     *float64 `json:"incomePercentage"`
	ThresholdOverride    *float64 `json:"thresholdOverride"`
	ReallocationEligible *bool    `json:"reallocationEligible"`
}

type bulkUpdateWire struct {
	ExpectedRevision *int            `json:"expectedRevision"`
	Items            *[]bulkItemWire `json:"items"`
}

func (w bulkUpdateWire) validate() error {
	if w.ExpectedRevision == nil {
		return keyNotFound("expectedRevision", "")
	}
	if w.Items == nil {
		return keyNotFound("items", "")
	}
	for i, it := range *w.Items {
		p := indexPath("items", i)
		switch {
		case it.ID == nil:
			return keyNotFound("id", p)
		case it.PlannedAmount == nil:
			return keyNotFound("plannedAmount", p)
		case it.TargetType == nil:
			return keyNotFound("targetType", p)
		}
		if err := checkEnum(*it.TargetType, joinPath(p, "targetType"), "BudgetTargetType", targetFixed, targetPercentageIncome); err != nil {
			return err
		}
		if it.ReallocationEligible == nil {
			return keyNotFound("reallocationEligible", p)
		}
	}
	return nil
}

// checkEnum is a RawRepresentable enum's decode failure.
func checkEnum(v, path, typ string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return dataCorrupted(path, fmt.Sprintf("Cannot initialize %s from invalid String value %s", typ, v))
}

type adjustmentWire struct {
	PlanItemID *string  `json:"planItemId"`
	Amount     *float64 `json:"amount"`
}

type previewWire struct {
	SnapshotID       *string           `json:"snapshotId"`
	ExpectedRevision *int              `json:"expectedRevision"`
	Adjustments      *[]adjustmentWire `json:"adjustments"`
	FinancialGoalID  *string           `json:"financialGoalId"`
	PortfolioListID  *string           `json:"portfolioListId"`
}

// previewRequest is a validated BudgetReallocationPreviewRequest.
type previewRequest struct {
	SnapshotID       string
	ExpectedRevision int
	Adjustments      []reallocationAdjustment
	FinancialGoalID  *string
	PortfolioListID  *string
}

func (w *previewWire) toRequest(path string) (previewRequest, error) {
	switch {
	case w.SnapshotID == nil:
		return previewRequest{}, keyNotFound("snapshotId", path)
	case w.ExpectedRevision == nil:
		return previewRequest{}, keyNotFound("expectedRevision", path)
	case w.Adjustments == nil:
		return previewRequest{}, keyNotFound("adjustments", path)
	}
	adj := make([]reallocationAdjustment, len(*w.Adjustments))
	for i, a := range *w.Adjustments {
		p := indexPath(joinPath(path, "adjustments"), i)
		if a.PlanItemID == nil {
			return previewRequest{}, keyNotFound("planItemId", p)
		}
		if a.Amount == nil {
			return previewRequest{}, keyNotFound("amount", p)
		}
		adj[i] = reallocationAdjustment{PlanItemID: *a.PlanItemID, Amount: *a.Amount}
	}
	return previewRequest{
		SnapshotID:       *w.SnapshotID,
		ExpectedRevision: *w.ExpectedRevision,
		Adjustments:      adj,
		FinancialGoalID:  w.FinancialGoalID,
		PortfolioListID:  w.PortfolioListID,
	}, nil
}

type commitWire struct {
	RequestID *string      `json:"requestId"`
	Preview   *previewWire `json:"preview"`
}

type templateItemWire struct {
	Title                *string  `json:"title"`
	PlannedAmount        *float64 `json:"plannedAmount"`
	Pillar               *string  `json:"pillar"`
	CategoryID           *string  `json:"categoryId"`
	TargetType           *string  `json:"targetType"`
	IncomePercentage     *float64 `json:"incomePercentage"`
	ThresholdOverride    *float64 `json:"thresholdOverride"`
	AllocationKind       *string  `json:"allocationKind"`
	ReallocationEligible *bool    `json:"reallocationEligible"`
}

type templateWire struct {
	Name  *string             `json:"name"`
	Items *[]templateItemWire `json:"items"`
}

// toRequest validates a BudgetTemplateRequest.
func (w templateWire) toRequest() (string, []templateItem, error) {
	if w.Name == nil {
		return "", nil, keyNotFound("name", "")
	}
	if w.Items == nil {
		return "", nil, keyNotFound("items", "")
	}
	items := make([]templateItem, len(*w.Items))
	for i, it := range *w.Items {
		p := indexPath("items", i)
		switch {
		case it.Title == nil:
			return "", nil, keyNotFound("title", p)
		case it.PlannedAmount == nil:
			return "", nil, keyNotFound("plannedAmount", p)
		case it.Pillar == nil:
			return "", nil, keyNotFound("pillar", p)
		}
		pillar, ok := canonicalPillar(*it.Pillar)
		if !ok {
			return "", nil, dataCorrupted(joinPath(p, "pillar"), "Invalid pillar value.")
		}
		if it.TargetType == nil {
			return "", nil, keyNotFound("targetType", p)
		}
		if err := checkEnum(*it.TargetType, joinPath(p, "targetType"), "BudgetTargetType", targetFixed, targetPercentageIncome); err != nil {
			return "", nil, err
		}
		if it.AllocationKind == nil {
			return "", nil, keyNotFound("allocationKind", p)
		}
		if err := checkEnum(*it.AllocationKind, joinPath(p, "allocationKind"), "BudgetAllocationKind", kindExpense, kindInvestment); err != nil {
			return "", nil, err
		}
		if it.ReallocationEligible == nil {
			return "", nil, keyNotFound("reallocationEligible", p)
		}
		items[i] = templateItem{
			Title: *it.Title, PlannedAmount: *it.PlannedAmount, Pillar: pillar, CategoryID: it.CategoryID,
			TargetType: *it.TargetType, IncomePercentage: it.IncomePercentage, ThresholdOverride: it.ThresholdOverride,
			AllocationKind: *it.AllocationKind, ReallocationEligible: *it.ReallocationEligible,
		}
	}
	return *w.Name, items, nil
}

type applyWire struct {
	SnapshotID       *string `json:"snapshotId"`
	ExpectedRevision *int    `json:"expectedRevision"`
	ReplaceExisting  *bool   `json:"replaceExisting"`
}

func (w applyWire) validate() error {
	switch {
	case w.SnapshotID == nil:
		return keyNotFound("snapshotId", "")
	case w.ExpectedRevision == nil:
		return keyNotFound("expectedRevision", "")
	case w.ReplaceExisting == nil:
		return keyNotFound("replaceExisting", "")
	}
	return nil
}

// --- shared value helpers ----------------------------------------------------

var (
	nonAlnum    = regexp.MustCompile(`[^A-Za-z0-9]`)
	nonAlnumRun = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// canonicalPillar is BudgetPillar(rawValue:): false when the value trims to
// nothing.
func canonicalPillar(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false
	}
	var normalized string
	if !nonAlnum.MatchString(trimmed) {
		r, size := utf8.DecodeRuneInString(trimmed)
		normalized = strings.ToLower(string(r)) + trimmed[size:]
	} else {
		words := strings.Fields(nonAlnumRun.ReplaceAllString(trimmed, " "))
		if len(words) == 0 {
			return "", false
		}
		var b strings.Builder
		b.WriteString(strings.ToLower(words[0]))
		for _, w := range words[1:] {
			r, size := utf8.DecodeRuneInString(w)
			b.WriteString(strings.ToUpper(string(r)) + strings.ToLower(w[size:]))
		}
		normalized = b.String()
	}
	switch strings.ToLower(normalized) {
	case "fundamentals":
		return "fundamentals", true
	case "futureyou":
		return "futureYou", true
	case "fun":
		return "fun", true
	}
	return normalized, true
}

// normalizeSymbol is SpendToUnitsMath.normalizeSymbol.
func normalizeSymbol(raw string) (string, bool) {
	trimmed := strings.ToUpper(strings.TrimSpace(raw))
	n := utf8.RuneCountInString(trimmed)
	if n < 1 || n > 12 {
		return "", false
	}
	for _, r := range trimmed {
		if r == '.' || r == '-' || unicode.In(r, unicode.L, unicode.M, unicode.N) {
			continue
		}
		return "", false
	}
	return trimmed, true
}

// spendUnits is SpendToUnitsMath.units.
func spendUnits(amount float64, price *float64) *float64 {
	if price == nil || *price <= 0 || !finite(amount) || !finite(*price) {
		return nil
	}
	v := roundHalfAway(amount / *price * 100) / 100
	return &v
}

// spendSurplus is SpendToUnitsMath.surplus.
func spendSurplus(investmentTarget, lostCapital, totalTarget, totalActual float64) float64 {
	if investmentTarget > 0 {
		return max(investmentTarget-lostCapital, 0)
	}
	return max(totalTarget-totalActual, 0)
}
