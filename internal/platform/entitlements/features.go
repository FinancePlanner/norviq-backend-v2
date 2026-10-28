package entitlements

import (
	"fmt"
	"net/http"
)

// Feature is BillingFeature: the key a gate reports and the billing
// context lists.
type Feature string

// BillingFeature cases, with Vapor's raw values.
const (
	FeatureBrokerSync              Feature = "broker_sync"
	FeaturePortfolioLists          Feature = "portfolio_lists"
	FeatureHoldings                Feature = "holdings"
	FeatureWatchlistItems          Feature = "watchlist_items"
	FeatureValuationCases          Feature = "valuation_cases"
	FeatureCSVImports              Feature = "csv_imports"
	FeatureTargetAlerts            Feature = "target_alerts"
	FeatureReportGenerations       Feature = "report_generations"
	FeatureExpensePlanner          Feature = "expense_planner"
	FeatureReports                 Feature = "reports"
	FeatureStatistics              Feature = "statistics"
	FeatureMarketFundamentals      Feature = "market_fundamentals"
	FeatureAdvancedResearch        Feature = "advanced_research"
	FeaturePeerComparison          Feature = "peer_comparison"
	FeatureEarningsText            Feature = "earnings_text"
	FeatureHouseholdPartner        Feature = "household_partner"
	FeatureRecurringTemplates      Feature = "recurring_templates"
	FeatureYearOverview            Feature = "year_overview"
	FeatureSmartSuggestions        Feature = "smart_suggestions"
	FeatureCrypto                  Feature = "crypto"
	FeatureAIInsights              Feature = "ai_insights"
	FeatureMCPAccess               Feature = "mcp_access"
	FeaturePilotFollows            Feature = "pilot_follows"
	FeatureScenarioPlanning        Feature = "scenario_planning"
	FeatureTaxOptimization         Feature = "tax_optimization"
	FeatureReceiptScan             Feature = "receipt_scan"
	FeatureBankSync                Feature = "bank_sync"
	FeatureSpreadsheetImport       Feature = "spreadsheet_import"
	FeatureScreenshotImport        Feature = "screenshot_import"
	FeatureAdvancedPortfolios      Feature = "advanced_portfolios"
	FeatureJointPortfolios         Feature = "joint_portfolios"
	FeatureAdvancedReportTemplates Feature = "advanced_report_templates"
	FeatureAdvancedReportSchedules Feature = "advanced_report_schedules"
	FeatureAdvancedReportRuns      Feature = "advanced_report_runs"
	FeatureNetWorthForecasting     Feature = "net_worth_forecasting"
	FeatureSmartScreening          Feature = "smart_screening"
	FeatureRebalancingRules        Feature = "rebalancing_rules"
	FeatureGoalPlanning            Feature = "goal_planning"
	FeatureTerminalPositionAI      Feature = "terminal_position_ai"
)

// Limits is BillingPlanLimits. nil means unlimited.
type Limits struct {
	PortfolioListCount    *int
	HoldingCount          *int
	WatchlistItemCount    *int
	ValuationCaseCount    *int
	CSVImportCount        *int
	TargetAlertCount      *int
	ReportGenerationCount *int
}

func intp(v int) *int { return &v }

// FreeLimits and ProLimits are BillingPlanLimits.free and .pro.
var (
	FreeLimits = Limits{
		PortfolioListCount:    intp(1),
		HoldingCount:          intp(5),
		WatchlistItemCount:    intp(10),
		ValuationCaseCount:    intp(1),
		CSVImportCount:        intp(1),
		TargetAlertCount:      intp(1),
		ReportGenerationCount: intp(50),
	}
	ProLimits = Limits{}
)

// Limit is BillingPlanLimits.limit(for:). Features without a counted
// allowance have no limit.
func (l Limits) Limit(f Feature) *int {
	switch f {
	case FeaturePortfolioLists:
		return l.PortfolioListCount
	case FeatureHoldings:
		return l.HoldingCount
	case FeatureWatchlistItems:
		return l.WatchlistItemCount
	case FeatureValuationCases:
		return l.ValuationCaseCount
	case FeatureCSVImports:
		return l.CSVImportCount
	case FeatureTargetAlerts:
		return l.TargetAlertCount
	case FeatureReportGenerations:
		return l.ReportGenerationCount
	}
	return nil
}

// UpgradeRequiredError is BillingUpgradeRequiredError. Returned from a
// handler, it renders BillingErrorMiddleware's body with status 403.
type UpgradeRequiredError struct {
	Feature      Feature
	Plan         string
	RequiredPlan string
	Limit        *int
	Current      *int
}

// UpgradeRequired is BillingUpgradeRequiredError(feature:plan:).
func UpgradeRequired(feature Feature, plan string) *UpgradeRequiredError {
	return &UpgradeRequiredError{Feature: feature, Plan: plan, RequiredPlan: "pro"}
}

// UpgradeLimit is BillingUpgradeRequiredError(feature:plan:limit:current:).
func UpgradeLimit(feature Feature, plan string, limit, current int) *UpgradeRequiredError {
	return &UpgradeRequiredError{Feature: feature, Plan: plan, RequiredPlan: "pro", Limit: &limit, Current: &current}
}

// Reason is the error's reason string, verbatim from Swift.
func (e *UpgradeRequiredError) Reason() string {
	if e.Limit != nil && e.Current != nil {
		return fmt.Sprintf("Upgrade required. feature=%s plan=%s limit=%d current=%d", e.Feature, e.Plan, *e.Limit, *e.Current)
	}
	return fmt.Sprintf("Upgrade required. feature=%s plan=%s required=%s", e.Feature, e.Plan, e.RequiredPlan)
}

func (e *UpgradeRequiredError) Error() string { return e.Reason() }

// StatusCode is 403, as the Swift error's `status`.
func (e *UpgradeRequiredError) StatusCode() int { return http.StatusForbidden }

// UpgradeRequiredResponse is BillingUpgradeRequiredResponse.
type UpgradeRequiredResponse struct {
	Success      bool   `json:"success"`
	Code         string `json:"code"`
	Error        string `json:"error"`
	Feature      string `json:"feature"`
	Plan         string `json:"plan"`
	RequiredPlan string `json:"requiredPlan"`
	Limit        *int   `json:"limit,omitempty"`
	Current      *int   `json:"current,omitempty"`
}

// ResponseBody implements httpx.BodyError: BillingErrorMiddleware caught
// this error before APIErrorMiddleware and wrote its own body.
func (e *UpgradeRequiredError) ResponseBody() (int, any) {
	return http.StatusForbidden, UpgradeRequiredResponse{
		Success:      false,
		Code:         "upgrade_required",
		Error:        e.Reason(),
		Feature:      string(e.Feature),
		Plan:         e.Plan,
		RequiredPlan: e.RequiredPlan,
		Limit:        e.Limit,
		Current:      e.Current,
	}
}
