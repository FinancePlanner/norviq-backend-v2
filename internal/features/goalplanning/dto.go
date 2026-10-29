package goalplanning

import (
	"strings"
	"time"

	"norviq-api/internal/apijson"
)

// StockPlanShared/GoalPlanning/GoalPlanningDTOs.swift (v5.25.0).

// Enum raw values.
const (
	statusActive = "active"

	roleObservedContribution = "observed_contribution"
	roleReductionCandidate   = "reduction_candidate"

	driftAhead            = "ahead"
	driftOnTrack          = "on_track"
	driftBehind           = "behind"
	driftComplete         = "complete"
	driftInsufficientData = "insufficient_data"

	kindIncreaseContribution = "increase_contribution"
	kindReduceContribution   = "reduce_contribution"
	kindReduceSpending       = "reduce_spending"
	kindExtendTargetDate     = "extend_target_date"
	kindRebalanceAllocation  = "rebalance_allocation"

	suggestionProposed  = "proposed"
	suggestionAccepted  = "accepted"
	suggestionDismissed = "dismissed"

	destinationBudget      = "budget"
	destinationRebalancing = "rebalancing"
	destinationGoal        = "goal"

	riskConservative = "conservative"
	riskModerate     = "moderate"
	riskAggressive   = "aggressive"
)

var (
	goalTypes     = []string{"retirement", "home_purchase", "financial_independence", "education", "emergency_fund", "investment_target", "custom"}
	goalStatuses  = []string{"active", "paused", "completed", "archived"}
	riskProfiles  = []string{riskConservative, riskModerate, riskAggressive}
	categoryRoles = []string{roleObservedContribution, roleReductionCandidate}
	suggestKinds  = []string{kindIncreaseContribution, kindReduceContribution, kindReduceSpending, kindExtendTargetDate, kindRebalanceAllocation}
	suggestStates = []string{suggestionProposed, suggestionAccepted, suggestionDismissed}
	destinations  = []string{destinationBudget, destinationRebalancing, destinationGoal}
)

// goalType is FinancialGoalType.
type goalType string

func (goalType) SwiftEnum() (string, []string, bool) { return "FinancialGoalType", goalTypes, false }

// goalStatus is FinancialGoalStatus.
type goalStatus string

func (goalStatus) SwiftEnum() (string, []string, bool) {
	return "FinancialGoalStatus", goalStatuses, false
}

// riskProfile is FinancialGoalRiskProfile.
type riskProfile string

func (riskProfile) SwiftEnum() (string, []string, bool) {
	return "FinancialGoalRiskProfile", riskProfiles, false
}

// defaultAnnualReturn is FinancialGoalRiskProfile.defaultAnnualReturn.
func defaultAnnualReturn(p string) float64 {
	switch p {
	case riskConservative:
		return 0.04
	case riskAggressive:
		return 0.08
	}
	return 0.06
}

// categoryRole is GoalExpenseCategoryRole.
type categoryRole string

func (categoryRole) SwiftEnum() (string, []string, bool) {
	return "GoalExpenseCategoryRole", categoryRoles, false
}

// allocationInput is GoalPortfolioAllocation as decoded inside
// FinancialGoalInput (synthesized Codable: every key required).
type allocationInput struct {
	ID                   string  `json:"id"`
	PortfolioListID      string  `json:"portfolioListId"`
	AllocationPercentage float64 `json:"allocationPercentage"`
}

// categoryLinkInput is GoalExpenseCategoryLink as decoded.
type categoryLinkInput struct {
	ID         string       `json:"id"`
	CategoryID string       `json:"categoryId"`
	Role       categoryRole `json:"role"`
}

// goalInput is FinancialGoalInput (synthesized Codable: only
// expectedAnnualReturn may be absent).
type goalInput struct {
	Name                     string              `json:"name"`
	GoalType                 goalType            `json:"goalType"`
	TargetAmount             float64             `json:"targetAmount"`
	TargetDate               string              `json:"targetDate"`
	BaseCurrency             string              `json:"baseCurrency"`
	StartingCapital          float64             `json:"startingCapital"`
	MonthlyContribution      float64             `json:"monthlyContribution"`
	AnnualContributionGrowth float64             `json:"annualContributionGrowth"`
	InflationAssumption      float64             `json:"inflationAssumption"`
	RiskProfile              riskProfile         `json:"riskProfile"`
	ExpectedAnnualReturn     *float64            `json:"expectedAnnualReturn"`
	Status                   goalStatus          `json:"status"`
	PortfolioAllocations     []allocationInput   `json:"portfolioAllocations"`
	ExpenseCategoryLinks     []categoryLinkInput `json:"expenseCategoryLinks"`
}

// configuredReturn is input.expectedAnnualReturn ?? riskProfile.defaultAnnualReturn.
func (in *goalInput) configuredReturn() float64 {
	if in.ExpectedAnnualReturn != nil {
		return *in.ExpectedAnnualReturn
	}
	return defaultAnnualReturn(string(in.RiskProfile))
}

// whatIfInput is GoalWhatIfRequest.
type whatIfInput struct {
	MonthlyContribution  *float64 `json:"monthlyContribution"`
	ExpectedAnnualReturn *float64 `json:"expectedAnnualReturn"`
	TargetDate           *string  `json:"targetDate"`
}

// contributionInput is GoalContributionInput.
type contributionInput struct {
	Amount     float64 `json:"amount"`
	OccurredAt string  `json:"occurredAt"`
	Note       *string `json:"note"`
}

// bulkInput is FinancialGoalBulkUpdate.
type bulkInput struct {
	GoalIDs []string   `json:"goalIds"`
	Status  goalStatus `json:"status"`
}

// PortfolioAllocation is GoalPortfolioAllocation.
type PortfolioAllocation struct {
	ID                   string  `json:"id"`
	PortfolioListID      string  `json:"portfolioListId"`
	AllocationPercentage float64 `json:"allocationPercentage"`
}

// CategoryLink is GoalExpenseCategoryLink.
type CategoryLink struct {
	ID         string `json:"id"`
	CategoryID string `json:"categoryId"`
	Role       string `json:"role"`
}

// FinancialGoal is FinancialGoal.
type FinancialGoal struct {
	ID                       string                `json:"id"`
	Name                     string                `json:"name"`
	PortfolioListID          string                `json:"portfolioListId"`
	GoalType                 string                `json:"goalType"`
	TargetAmount             float64               `json:"targetAmount"`
	TargetDate               string                `json:"targetDate"`
	BaseCurrency             string                `json:"baseCurrency"`
	StartingCapital          float64               `json:"startingCapital"`
	MonthlyContribution      float64               `json:"monthlyContribution"`
	AnnualContributionGrowth float64               `json:"annualContributionGrowth"`
	InflationAssumption      float64               `json:"inflationAssumption"`
	RiskProfile              string                `json:"riskProfile"`
	ExpectedAnnualReturn     float64               `json:"expectedAnnualReturn"`
	Status                   string                `json:"status"`
	PortfolioAllocations     []PortfolioAllocation `json:"portfolioAllocations"`
	ExpenseCategoryLinks     []CategoryLink        `json:"expenseCategoryLinks"`
	CreatedAt                *string               `json:"createdAt,omitempty"`
	UpdatedAt                *string               `json:"updatedAt,omitempty"`
}

// TrajectoryPoint is GoalTrajectoryPoint.
type TrajectoryPoint struct {
	Date           string   `json:"date"`
	PlannedValue   float64  `json:"plannedValue"`
	ActualValue    *float64 `json:"actualValue,omitempty"`
	ProjectedValue float64  `json:"projectedValue"`
}

// Progress is GoalProgress.
type Progress struct {
	GoalID                      string            `json:"goalId"`
	Currency                    string            `json:"currency"`
	CurrentValue                float64           `json:"currentValue"`
	TargetAmount                float64           `json:"targetAmount"`
	PercentComplete             float64           `json:"percentComplete"`
	PlannedValueToday           float64           `json:"plannedValueToday"`
	ProjectedValueAtTarget      float64           `json:"projectedValueAtTarget"`
	ProjectedCompletionDate     *string           `json:"projectedCompletionDate,omitempty"`
	DriftAmount                 float64           `json:"driftAmount"`
	DriftMonths                 *int              `json:"driftMonths,omitempty"`
	DriftState                  string            `json:"driftState"`
	PlannedMonthlyContribution  float64           `json:"plannedMonthlyContribution"`
	ObservedMonthlyContribution float64           `json:"observedMonthlyContribution"`
	Trajectory                  []TrajectoryPoint `json:"trajectory"`
	Warnings                    []string          `json:"warnings"`
	CalculatedAt                string            `json:"calculatedAt"`
}

// OverviewItem is GoalOverviewItem.
type OverviewItem struct {
	ID       string        `json:"id"`
	Goal     FinancialGoal `json:"goal"`
	Progress Progress      `json:"progress"`
}

// Overview is GoalOverview.
type Overview struct {
	Items             []OverviewItem `json:"items"`
	TotalCurrentValue float64        `json:"totalCurrentValue"`
	TotalTargetAmount float64        `json:"totalTargetAmount"`
	ActiveGoalCount   int            `json:"activeGoalCount"`
	ActiveGoalLimit   *int           `json:"activeGoalLimit,omitempty"`
	IsPro             bool           `json:"isPro"`
}

// WhatIfResponse is GoalWhatIfResponse.
type WhatIfResponse struct {
	Baseline Progress `json:"baseline"`
	Scenario Progress `json:"scenario"`
}

// Contribution is GoalContribution.
type Contribution struct {
	ID         string  `json:"id"`
	GoalID     string  `json:"goalId"`
	Amount     float64 `json:"amount"`
	OccurredAt string  `json:"occurredAt"`
	Note       *string `json:"note,omitempty"`
	CreatedAt  string  `json:"createdAt"`
}

// Suggestion is GoalSuggestion.
type Suggestion struct {
	ID                     string   `json:"id"`
	GoalID                 string   `json:"goalId"`
	Kind                   string   `json:"kind"`
	Title                  string   `json:"title"`
	Explanation            string   `json:"explanation"`
	MonthlyAmount          *float64 `json:"monthlyAmount,omitempty"`
	AllocationPercentage   *float64 `json:"allocationPercentage,omitempty"`
	EstimatedMonthsChanged *int     `json:"estimatedMonthsChanged,omitempty"`
	Status                 string   `json:"status"`
	CreatedAt              string   `json:"createdAt"`
}

// AdjustmentDraft is GoalAdjustmentDraft.
type AdjustmentDraft struct {
	ID           string            `json:"id"`
	SuggestionID string            `json:"suggestionId"`
	Destination  string            `json:"destination"`
	Payload      map[string]string `json:"payload"`
	CreatedAt    string            `json:"createdAt"`
}

// Template is GoalTemplate.
type Template struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	GoalType       string `json:"goalType"`
	SuggestedYears int    `json:"suggestedYears"`
	RiskProfile    string `json:"riskProfile"`
}

// templates is GoalPlanningController.templates.
var templates = []Template{
	{ID: "financial-independence", Name: "Financial independence", GoalType: "financial_independence", SuggestedYears: 20, RiskProfile: riskModerate},
	{ID: "retirement", Name: "Retirement", GoalType: "retirement", SuggestedYears: 25, RiskProfile: riskModerate},
	{ID: "home-deposit", Name: "Home deposit", GoalType: "home_purchase", SuggestedYears: 5, RiskProfile: riskConservative},
	{ID: "emergency-fund", Name: "Emergency fund", GoalType: "emergency_fund", SuggestedYears: 2, RiskProfile: riskConservative},
	{ID: "investment-target", Name: "Investment target", GoalType: "investment_target", SuggestedYears: 10, RiskProfile: riskAggressive},
}

// orDefault is `Enum(rawValue: raw) ?? fallback`.
func orDefault(raw string, allowed []string, fallback string) string {
	for _, a := range allowed {
		if a == raw {
			return raw
		}
	}
	return fallback
}

// timestamp is GoalPlanningService.timestamp (ISO8601DateFormatter).
func timestamp(t time.Time) string { return apijson.FormatTime(t) }

// dateString is GoalPlanningService.dateString ("yyyy-MM-dd", gregorian,
// process time zone: UTC in production).
func dateString(t time.Time) string { return t.UTC().Format("2006-01-02") }

// monthKey is GoalPlanningService.monthKey.
func monthKey(t time.Time) string { return dateString(t)[:7] }

// parseDate is GoalPlanningService.parseDate: ISO8601DateFormatter's
// internet date-time first (no fractional seconds), then "yyyy-MM-dd".
func parseDate(value string) (time.Time, bool) {
	if !strings.Contains(value, ".") {
		if t, err := time.Parse(time.RFC3339, value); err == nil && strings.ContainsAny(value, "Tt") {
			return t, true
		}
	}
	if t, err := time.Parse("2006-1-2", value); err == nil {
		return t, true
	}
	return time.Time{}, false
}
