package expenses

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// BudgetPillar is StockPlanShared.BudgetPillar: a canonicalized string
// (fundamentals, futureYou, fun, or any custom camelCase key).
type BudgetPillar string

// The standard pillars, in BudgetPillar.allCases order.
const (
	PillarFundamentals BudgetPillar = "fundamentals"
	PillarFutureYou    BudgetPillar = "futureYou"
	PillarFun          BudgetPillar = "fun"
)

// AllPillars is BudgetPillar.allCases.
var AllPillars = []BudgetPillar{PillarFundamentals, PillarFutureYou, PillarFun}

var (
	nonAlnum     = regexp.MustCompile(`[^A-Za-z0-9]`)
	nonAlnumRuns = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// CanonicalPillarKey is BudgetPillar.canonicalKey(from:).
func CanonicalPillarKey(value string) string {
	trimmed := strings.TrimFunc(value, unicode.IsSpace)
	if trimmed == "" {
		return ""
	}
	var normalized string
	if !nonAlnum.MatchString(trimmed) {
		normalized = strings.ToLower(trimmed[:1]) + trimmed[1:]
	} else {
		var words []string
		for _, w := range strings.Split(nonAlnumRuns.ReplaceAllString(trimmed, " "), " ") {
			if w != "" {
				words = append(words, w)
			}
		}
		if len(words) == 0 {
			return ""
		}
		var b strings.Builder
		b.WriteString(strings.ToLower(words[0]))
		for _, w := range words[1:] {
			b.WriteString(strings.ToUpper(w[:1]) + strings.ToLower(w[1:]))
		}
		normalized = b.String()
	}
	switch strings.ToLower(normalized) {
	case "fundamentals":
		return string(PillarFundamentals)
	case "futureyou":
		return string(PillarFutureYou)
	case "fun":
		return string(PillarFun)
	}
	return normalized
}

// ParsePillar is BudgetPillar(rawValue:).
func ParsePillar(raw string) (BudgetPillar, bool) {
	c := CanonicalPillarKey(raw)
	if c == "" {
		return "", false
	}
	return BudgetPillar(c), true
}

// pillarFromDB decodes a stored pillar as Fluent did (through
// BudgetPillar.init(from:)); an uncanonicalizable value falls back to the
// raw text rather than failing the request.
func pillarFromDB(raw string) BudgetPillar {
	if p, ok := ParsePillar(raw); ok {
		return p
	}
	return BudgetPillar(raw)
}

// IsStandard is BudgetPillar.isStandard.
func (p BudgetPillar) IsStandard() bool {
	return p == PillarFundamentals || p == PillarFutureYou || p == PillarFun
}

// Split modes (ExpenseSplitMode).
const (
	SplitPersonal = "personal"
	SplitShared   = "shared"
)

// Budget target types, allocation kinds and recurring frequencies.
const (
	TargetFixed            = "fixed"
	TargetPercentageIncome = "percentageIncome"
	AllocationExpense      = "expense"
	AllocationInvestment   = "investmentContribution"
	FrequencyWeekly        = "weekly"
	FrequencyMonthly       = "monthly"
)

// ExpenseReceiptMetadata is StockPlanShared.ExpenseReceiptMetadata, stored
// as JSON in expenses.receipt_metadata with the same keys.
type ExpenseReceiptMetadata struct {
	Source        string   `json:"source"` // qr | ocr
	Merchant      *string  `json:"merchant,omitempty"`
	TaxIdentifier *string  `json:"taxIdentifier,omitempty"`
	IssuedOn      *string  `json:"issuedOn,omitempty"`
	Currency      *string  `json:"currency,omitempty"`
	Total         *float64 `json:"total,omitempty"`
	VatTotal      *float64 `json:"vatTotal,omitempty"`
}

// ExpenseRequest is StockPlanShared.ExpenseRequest with its defaults
// applied (splitMode personal, userSharePercent 100).
type ExpenseRequest struct {
	Title            string                  `json:"title"`
	Amount           float64                 `json:"amount"`
	Pillar           BudgetPillar            `json:"pillar"`
	OccurredOn       string                  `json:"occurredOn"`
	LinkedPlanItemID *string                 `json:"linkedPlanItemId,omitempty"`
	CategoryID       *string                 `json:"categoryId,omitempty"`
	SplitMode        string                  `json:"splitMode"`
	UserSharePercent float64                 `json:"userSharePercent"`
	ForeignAmount    *float64                `json:"foreignAmount,omitempty"`
	ForeignCurrency  *string                 `json:"foreignCurrency,omitempty"`
	ExchangeRate     *float64                `json:"exchangeRate,omitempty"`
	Notes            *string                 `json:"notes,omitempty"`
	ReceiptMetadata  *ExpenseReceiptMetadata `json:"receiptMetadata,omitempty"`
}

// NewExpenseRequest is ExpenseRequest's memberwise init with its defaults.
func NewExpenseRequest(title string, amount float64, pillar BudgetPillar, occurredOn string, categoryID *string) ExpenseRequest {
	return ExpenseRequest{
		Title: title, Amount: amount, Pillar: pillar, OccurredOn: occurredOn,
		CategoryID: categoryID, SplitMode: SplitPersonal, UserSharePercent: 100,
	}
}

// ExpenseResponse is StockPlanShared.ExpenseResponse.
type ExpenseResponse struct {
	ID               string                  `json:"id"`
	Title            string                  `json:"title"`
	Amount           float64                 `json:"amount"`
	Pillar           BudgetPillar            `json:"pillar"`
	OccurredOn       string                  `json:"occurredOn"`
	LinkedPlanItemID *string                 `json:"linkedPlanItemId,omitempty"`
	CategoryID       *string                 `json:"categoryId,omitempty"`
	SplitMode        string                  `json:"splitMode"`
	UserSharePercent float64                 `json:"userSharePercent"`
	ForeignAmount    *float64                `json:"foreignAmount,omitempty"`
	ForeignCurrency  *string                 `json:"foreignCurrency,omitempty"`
	ExchangeRate     *float64                `json:"exchangeRate,omitempty"`
	Notes            *string                 `json:"notes,omitempty"`
	ReceiptMetadata  *ExpenseReceiptMetadata `json:"receiptMetadata,omitempty"`
	CreatedAt        *string                 `json:"createdAt,omitempty"`
	UpdatedAt        *string                 `json:"updatedAt,omitempty"`
}

// ExpenseSearchFilters is the Swift struct of the same name.
type ExpenseSearchFilters struct {
	From       *time.Time
	To         *time.Time
	Query      *string
	CategoryID *uuid.UUID
	Pillar     *BudgetPillar
	Limit      int
	Cursor     *time.Time
}

// ExpenseCategoryRequest is StockPlanShared.ExpenseCategoryRequest.
type ExpenseCategoryRequest struct {
	Name   string        `json:"name"`
	Pillar *BudgetPillar `json:"pillar,omitempty"`
}

// ExpenseCategoryResponse is StockPlanShared.ExpenseCategoryResponse.
type ExpenseCategoryResponse struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Pillar    *BudgetPillar `json:"pillar,omitempty"`
	IsDefault bool          `json:"isDefault"`
}

// RecurringTemplateRequest is StockPlanShared.RecurringTemplateRequest.
type RecurringTemplateRequest struct {
	Title            string       `json:"title"`
	Amount           float64      `json:"amount"`
	Pillar           BudgetPillar `json:"pillar"`
	CategoryID       *string      `json:"categoryId,omitempty"`
	Frequency        string       `json:"frequency"`
	SplitMode        string       `json:"splitMode"`
	UserSharePercent float64      `json:"userSharePercent"`
}

// RecurringTemplateResponse is StockPlanShared.RecurringTemplateResponse.
type RecurringTemplateResponse struct {
	ID               string       `json:"id"`
	Title            string       `json:"title"`
	Amount           float64      `json:"amount"`
	Pillar           BudgetPillar `json:"pillar"`
	CategoryID       *string      `json:"categoryId,omitempty"`
	Frequency        string       `json:"frequency"`
	SplitMode        string       `json:"splitMode"`
	UserSharePercent float64      `json:"userSharePercent"`
	CreatedAt        *string      `json:"createdAt,omitempty"`
}

// HouseholdPartnerProfileRequest is the shared request DTO.
type HouseholdPartnerProfileRequest struct {
	DisplayName             *string  `json:"displayName,omitempty"`
	DefaultUserSharePercent *float64 `json:"defaultUserSharePercent,omitempty"`
}

// HouseholdPartnerProfileResponse is the shared response DTO.
type HouseholdPartnerProfileResponse struct {
	DisplayName             *string  `json:"displayName,omitempty"`
	DefaultUserSharePercent *float64 `json:"defaultUserSharePercent,omitempty"`
}

// BudgetSnapshotRequest is StockPlanShared.BudgetSnapshotRequest.
type BudgetSnapshotRequest struct {
	MonthStart             string             `json:"monthStart"`
	NetSalary              float64            `json:"netSalary"`
	TargetShares           map[string]float64 `json:"targetShares"`
	CurrencyCode           *string            `json:"currencyCode,omitempty"`
	CategoryDriftThreshold *float64           `json:"categoryDriftThreshold,omitempty"`
	TotalDriftThreshold    *float64           `json:"totalDriftThreshold,omitempty"`
	AlertsEnabled          *bool              `json:"alertsEnabled,omitempty"`
	AlertOnUnbudgeted      *bool              `json:"alertOnUnbudgeted,omitempty"`
}

// BudgetSnapshotResponse is StockPlanShared.BudgetSnapshotResponse.
type BudgetSnapshotResponse struct {
	ID                     string             `json:"id"`
	MonthStart             string             `json:"monthStart"`
	NetSalary              float64            `json:"netSalary"`
	TargetShares           map[string]float64 `json:"targetShares"`
	CurrencyCode           string             `json:"currencyCode"`
	CategoryDriftThreshold float64            `json:"categoryDriftThreshold"`
	TotalDriftThreshold    float64            `json:"totalDriftThreshold"`
	AlertsEnabled          bool               `json:"alertsEnabled"`
	AlertOnUnbudgeted      bool               `json:"alertOnUnbudgeted"`
	Revision               int64              `json:"revision"`
	CreatedAt              *string            `json:"createdAt,omitempty"`
	UpdatedAt              *string            `json:"updatedAt,omitempty"`
}

// BudgetPlanItemRequest is StockPlanShared.BudgetPlanItemRequest with its
// defaults applied.
type BudgetPlanItemRequest struct {
	SnapshotID                 string       `json:"snapshotId"`
	Title                      string       `json:"title"`
	PlannedAmount              float64      `json:"plannedAmount"`
	Pillar                     BudgetPillar `json:"pillar"`
	CategoryID                 *string      `json:"categoryId,omitempty"`
	SplitMode                  string       `json:"splitMode"`
	UserSharePercent           float64      `json:"userSharePercent"`
	TargetType                 string       `json:"targetType"`
	IncomePercentage           *float64     `json:"incomePercentage,omitempty"`
	ThresholdOverride          *float64     `json:"thresholdOverride,omitempty"`
	AllocationKind             string       `json:"allocationKind"`
	ReallocationEligible       bool         `json:"reallocationEligible"`
	DestinationFinancialGoalID *string      `json:"destinationFinancialGoalId,omitempty"`
	DestinationPortfolioListID *string      `json:"destinationPortfolioListId,omitempty"`
}

// BudgetPlanItemResponse is StockPlanShared.BudgetPlanItemResponse.
type BudgetPlanItemResponse struct {
	ID                         string       `json:"id"`
	SnapshotID                 string       `json:"snapshotId"`
	Title                      string       `json:"title"`
	PlannedAmount              float64      `json:"plannedAmount"`
	Pillar                     BudgetPillar `json:"pillar"`
	CategoryID                 *string      `json:"categoryId,omitempty"`
	SplitMode                  string       `json:"splitMode"`
	UserSharePercent           float64      `json:"userSharePercent"`
	TargetType                 string       `json:"targetType"`
	IncomePercentage           *float64     `json:"incomePercentage,omitempty"`
	ThresholdOverride          *float64     `json:"thresholdOverride,omitempty"`
	AllocationKind             string       `json:"allocationKind"`
	ReallocationEligible       bool         `json:"reallocationEligible"`
	DestinationFinancialGoalID *string      `json:"destinationFinancialGoalId,omitempty"`
	DestinationPortfolioListID *string      `json:"destinationPortfolioListId,omitempty"`
	CreatedAt                  *string      `json:"createdAt,omitempty"`
	UpdatedAt                  *string      `json:"updatedAt,omitempty"`
}

// PillarPlanningSummaryResponse is the shared DTO.
type PillarPlanningSummaryResponse struct {
	Pillar                BudgetPillar `json:"pillar"`
	TargetAmount          float64      `json:"targetAmount"`
	PlannedAmount         float64      `json:"plannedAmount"`
	ActualAmount          float64      `json:"actualAmount"`
	UnplannedActualAmount float64      `json:"unplannedActualAmount"`
}

// BudgetMonthSummaryResponse is the shared DTO.
type BudgetMonthSummaryResponse struct {
	MonthStart           string             `json:"monthStart"`
	Planned              float64            `json:"planned"`
	Actual               float64            `json:"actual"`
	Salary               float64            `json:"salary"`
	MyPlanned            float64            `json:"myPlanned"`
	PartnerPlanned       float64            `json:"partnerPlanned"`
	MyActual             float64            `json:"myActual"`
	PartnerActual        float64            `json:"partnerActual"`
	PillarActuals        map[string]float64 `json:"pillarActuals"`
	PillarPlans          map[string]float64 `json:"pillarPlans"`
	MyPillarActuals      map[string]float64 `json:"myPillarActuals"`
	PartnerPillarActuals map[string]float64 `json:"partnerPillarActuals"`
	MyPillarPlans        map[string]float64 `json:"myPillarPlans"`
	PartnerPillarPlans   map[string]float64 `json:"partnerPillarPlans"`
}

// BudgetYearSummaryResponse is the shared DTO.
type BudgetYearSummaryResponse struct {
	Year           int     `json:"year"`
	Planned        float64 `json:"planned"`
	Actual         float64 `json:"actual"`
	Salary         float64 `json:"salary"`
	MyPlanned      float64 `json:"myPlanned"`
	PartnerPlanned float64 `json:"partnerPlanned"`
	MyActual       float64 `json:"myActual"`
	PartnerActual  float64 `json:"partnerActual"`
}

// ReportsCashFlowPointResponse is the shared DTO (its computed id is not
// encoded).
type ReportsCashFlowPointResponse struct {
	MonthStart  string  `json:"monthStart"`
	Income      float64 `json:"income"`
	Expenses    float64 `json:"expenses"`
	Net         float64 `json:"net"`
	SavingsRate float64 `json:"savingsRate"`
}

// ReportSuggestionResponse is the shared DTO.
type ReportSuggestionResponse struct {
	ID                 string            `json:"id"`
	Title              string            `json:"title"`
	Message            string            `json:"message"`
	Severity           string            `json:"severity"`
	Category           string            `json:"category"`
	MonthStart         string            `json:"monthStart"`
	RecommendedSavings float64           `json:"recommendedSavings"`
	DetailPayload      map[string]string `json:"detailPayload"`
}

// ReportSuggestionsResponse is the shared DTO.
type ReportSuggestionsResponse struct {
	GeneratedAt string                     `json:"generatedAt"`
	Suggestions []ReportSuggestionResponse `json:"suggestions"`
}
