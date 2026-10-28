package entitlements

import (
	"context"
	"errors"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/billingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BillingContext is BillingContextResponse, the body of GET /v1/billing/me.
type BillingContext struct {
	Plan               string           `json:"plan"`
	EntitlementLevel   string           `json:"entitlementLevel"`
	IsPro              bool             `json:"isPro"`
	IsPremium          bool             `json:"isPremium"`
	Subscription       *SubscriptionDTO `json:"subscription,omitempty"`
	PlanOptions        []PlanOptionDTO  `json:"planOptions"`
	Features           []FeatureDTO     `json:"features"`
	Usage              []UsageDTO       `json:"usage"`
	TrialDaysRemaining *int             `json:"trialDaysRemaining,omitempty"`
	IsTrialActive      bool             `json:"isTrialActive"`
	TrialExpired       bool             `json:"trialExpired"`
	GeneratedAt        apijson.Time     `json:"generatedAt"`
}

// PlanOptionDTO is BillingPlanOptionDTO.
type PlanOptionDTO struct {
	ProductID   string  `json:"productId"`
	Plan        string  `json:"plan"`
	DisplayName string  `json:"displayName"`
	Interval    string  `json:"interval"`
	Rank        int     `json:"rank"`
	Badge       *string `json:"badge,omitempty"`
	IsCurrent   bool    `json:"isCurrent"`
	ChangeKind  string  `json:"changeKind"`
}

// SubscriptionDTO is BillingSubscriptionDTO.
type SubscriptionDTO struct {
	Provider               string        `json:"provider"`
	Store                  *string       `json:"store,omitempty"`
	ProductID              string        `json:"productId"`
	Plan                   string        `json:"plan"`
	Status                 string        `json:"status"`
	PeriodStartedAt        *apijson.Time `json:"periodStartedAt,omitempty"`
	PeriodEndsAt           *apijson.Time `json:"periodEndsAt,omitempty"`
	TrialEndsAt            *apijson.Time `json:"trialEndsAt,omitempty"`
	GracePeriodEndsAt      *apijson.Time `json:"gracePeriodEndsAt,omitempty"`
	CancelledAt            *apijson.Time `json:"cancelledAt,omitempty"`
	IsTrial                bool          `json:"isTrial"`
	IsInGracePeriod        bool          `json:"isInGracePeriod"`
	HasBillingIssue        bool          `json:"hasBillingIssue"`
	IsCancelledButActive   bool          `json:"isCancelledButActive"`
	RenewsOrExpiresAt      *apijson.Time `json:"renewsOrExpiresAt,omitempty"`
	WillRenew              *bool         `json:"willRenew,omitempty"`
	AccessEndsAt           *apijson.Time `json:"accessEndsAt,omitempty"`
	PendingProductID       *string       `json:"pendingProductId,omitempty"`
	PendingPlan            *string       `json:"pendingPlan,omitempty"`
	PendingPlanEffectiveAt *apijson.Time `json:"pendingPlanEffectiveAt,omitempty"`
}

// FeatureDTO is BillingFeatureDTO.
type FeatureDTO struct {
	Key          string  `json:"key"`
	Title        string  `json:"title"`
	Available    bool    `json:"available"`
	RequiredPlan *string `json:"requiredPlan,omitempty"`
	Reason       *string `json:"reason,omitempty"`
	Limit        *int    `json:"limit,omitempty"`
	Used         *int    `json:"used,omitempty"`
	Remaining    *int    `json:"remaining,omitempty"`
}

// UsageDTO is BillingUsageDTO.
type UsageDTO struct {
	Key         string        `json:"key"`
	Used        int           `json:"used"`
	Limit       *int          `json:"limit,omitempty"`
	Remaining   *int          `json:"remaining,omitempty"`
	PeriodStart *apijson.Time `json:"periodStart,omitempty"`
}

type planOptionDescriptor struct {
	productID, plan, displayName, interval string
	rank                                   int
	badge                                  string
}

var planOptionDescriptors = []planOptionDescriptor{
	{"pro_weekly", "pro_weekly", "Weekly", "weekly", 1, ""},
	{"pro_monthly", "pro_monthly", "Monthly", "monthly", 2, "Better value"},
	{"pro_yearly", "pro_yearly", "Annual", "annual", 3, "Best value"},
}

type featureDescriptor struct {
	feature Feature
	title   string
	proOnly bool
}

// featureDescriptors is BillingFeatureDescriptor.all, in its order.
var featureDescriptors = []featureDescriptor{
	{FeatureBrokerSync, "Broker sync", true},
	{FeaturePortfolioLists, "Portfolio lists", false},
	{FeatureHoldings, "Holdings", false},
	{FeatureWatchlistItems, "Watchlist items", false},
	{FeatureValuationCases, "Saved valuation cases", true},
	{FeatureCSVImports, "CSV imports", false},
	{FeatureTargetAlerts, "Price, dividend, and earnings alerts", true},
	{FeatureReportGenerations, "Report generations", false},
	{FeatureExpensePlanner, "Expense planner (core budgeting)", false},
	{FeatureHouseholdPartner, "Household partner split view", true},
	{FeatureRecurringTemplates, "Recurring expense templates", true},
	{FeatureYearOverview, "Year-over-year expense history", true},
	{FeatureSmartSuggestions, "Smart spending suggestions", true},
	{FeatureReports, "Reports with charts", false},
	{FeatureStatistics, "Advanced statistics", true},
	{FeatureMarketFundamentals, "Real stock fundamentals", true},
	{FeatureAdvancedResearch, "Advanced stock research", true},
	{FeaturePeerComparison, "Peer comparison", true},
	{FeatureEarningsText, "Earnings detail", true},
	{FeatureCrypto, "Cryptocurrency", true},
	{FeatureReceiptScan, "Receipt photo scanning (OCR)", true},
	{FeatureSpreadsheetImport, "Smart spreadsheet import", true},
	{FeatureScreenshotImport, "Portfolio screenshot import", true},
	{FeatureBankSync, "Bank sync", true},
	{FeatureAdvancedPortfolios, "Retirement and hypothetical portfolios", true},
	{FeatureJointPortfolios, "Joint portfolio collaboration", true},
	{FeatureAdvancedReportTemplates, "Advanced report templates", true},
	{FeatureAdvancedReportSchedules, "Scheduled report delivery", true},
	{FeatureAdvancedReportRuns, "PDF and Excel reports", true},
	{FeatureNetWorthForecasting, "Net worth forecasting", true},
	{FeatureSmartScreening, "Smart watchlist screening", true},
	{FeatureRebalancingRules, "Custom rebalancing rules", true},
	{FeatureGoalPlanning, "Goal-based financial planning", false},
	{FeatureTerminalPositionAI, "AI terminal scenario research", true},
	{FeatureAIInsights, "AI assistant and insights", true},
	{FeatureMCPAccess, "MCP integrations", true},
	{FeatureScenarioPlanning, "Scenario planning", true},
	{FeatureTaxOptimization, "Tax strategy", true},
}

// Context is billingContextService.context: plan, entitlement, trial
// state, feature availability and usage for userID. A missing user is a
// 404 "User not found". As in Vapor, it creates or resets the user's usage
// counter row.
func (s *Service) Context(ctx context.Context, userID uuid.UUID) (*BillingContext, error) {
	user, err := s.q.ResolverUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("User not found")
	}
	if err != nil {
		return nil, err
	}

	ent, err := s.Resolve(ctx, userID)
	if err != nil {
		return nil, err
	}
	sub, err := s.q.CurrentSubscription(ctx, userID)
	hasSub := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	usage, err := s.Counter(ctx, userID)
	if err != nil {
		return nil, err
	}
	counts, err := s.q.ResourceCounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	limits := LimitsFor(ent)
	now := s.now()

	trial := trialOf(user.TrialStartedAt, user.TrialDays, user.TrialTier)
	status := checkTrialStatus(trial, now, s.warnDays)
	trialExpired := !ent.IsPremium() && (user.HadTrial || IsTrialExpired(trial, now))

	periodStart := apijson.NewTime(usage.PeriodStart)
	usageRows := []UsageDTO{
		usageRow(FeaturePortfolioLists, int(counts.PortfolioLists), limits, nil),
		usageRow(FeatureHoldings, int(counts.Holdings), limits, nil),
		usageRow(FeatureWatchlistItems, int(counts.WatchlistItems), limits, nil),
		usageRow(FeatureValuationCases, int(counts.ValuationCases), limits, nil),
		usageRow(FeatureCSVImports, int(usage.CsvImportCount), limits, &periodStart),
		usageRow(FeatureTargetAlerts, int(counts.TargetAlerts), limits, nil),
		usageRow(FeatureReportGenerations, int(usage.ReportGenerationCount), limits, &periodStart),
	}
	byKey := make(map[string]UsageDTO, len(usageRows))
	for _, r := range usageRows {
		byKey[r.Key] = r
	}

	features := make([]FeatureDTO, 0, len(featureDescriptors))
	for _, d := range featureDescriptors {
		available := ent.IsPro() || !d.proOnly
		f := FeatureDTO{
			Key:       string(d.feature),
			Title:     d.title,
			Available: available,
			Limit:     limits.Limit(d.feature),
		}
		if !available {
			pro := "pro"
			reason := "Upgrade to Pro to use " + d.title + "."
			f.RequiredPlan, f.Reason = &pro, &reason
		}
		if u, ok := byKey[string(d.feature)]; ok {
			used := u.Used
			f.Used, f.Remaining = &used, u.Remaining
		}
		features = append(features, f)
	}

	plan := ent.Level
	var subDTO *SubscriptionDTO
	if hasSub {
		plan = sub.Plan
		subDTO = subscriptionDTO(sub, now)
	}

	return &BillingContext{
		Plan:               plan,
		EntitlementLevel:   ent.Level,
		IsPro:              ent.IsPremium(),
		IsPremium:          ent.IsPremium(),
		Subscription:       subDTO,
		PlanOptions:        planOptions(plan, ent.IsPremium()),
		Features:           features,
		Usage:              usageRows,
		TrialDaysRemaining: status.DaysRemainingPtr(),
		IsTrialActive:      status.IsActive(),
		TrialExpired:       trialExpired,
		GeneratedAt:        apijson.NewTime(now),
	}, nil
}

func usageRow(f Feature, used int, limits Limits, periodStart *apijson.Time) UsageDTO {
	row := UsageDTO{Key: string(f), Used: used, Limit: limits.Limit(f), PeriodStart: periodStart}
	if row.Limit != nil {
		remaining := max(0, *row.Limit-used)
		row.Remaining = &remaining
	}
	return row
}

func after(t *time.Time, now time.Time) bool { return t != nil && t.After(now) }

func subscriptionDTO(s billingdb.Subscription, now time.Time) *SubscriptionDTO {
	willRenew := s.Status == "active" || s.Status == "trialing" || s.Status == "grace_period"
	return &SubscriptionDTO{
		Provider:               s.Provider,
		Store:                  s.Store,
		ProductID:              s.ProductID,
		Plan:                   s.Plan,
		Status:                 s.Status,
		PeriodStartedAt:        apijson.TimePtr(s.PeriodStartedAt),
		PeriodEndsAt:           apijson.TimePtr(s.PeriodEndsAt),
		TrialEndsAt:            apijson.TimePtr(s.TrialEndsAt),
		GracePeriodEndsAt:      apijson.TimePtr(s.GracePeriodEndsAt),
		CancelledAt:            apijson.TimePtr(s.CancelledAt),
		IsTrial:                s.Status == "trialing" || after(s.TrialEndsAt, now),
		IsInGracePeriod:        after(s.GracePeriodEndsAt, now),
		HasBillingIssue:        s.Status == "billing_issue",
		IsCancelledButActive:   s.Status == "cancelled" && after(s.PeriodEndsAt, now),
		RenewsOrExpiresAt:      apijson.TimePtr(s.PeriodEndsAt),
		WillRenew:              &willRenew,
		AccessEndsAt:           apijson.TimePtr(s.PeriodEndsAt),
		PendingProductID:       s.PendingProductID,
		PendingPlan:            s.PendingPlan,
		PendingPlanEffectiveAt: apijson.TimePtr(s.PendingPlanEffectiveAt),
	}
}

func planRank(plan string) (int, bool) {
	switch plan {
	case "pro_weekly":
		return 1, true
	case "pro_monthly":
		return 2, true
	case "pro_yearly", "pro_annual":
		return 3, true
	}
	return 0, false
}

func planOptions(currentPlan string, isPremium bool) []PlanOptionDTO {
	out := make([]PlanOptionDTO, 0, len(planOptionDescriptors))
	currentRank, ranked := planRank(currentPlan)
	for _, d := range planOptionDescriptors {
		isCurrent := d.plan == currentPlan
		var kind string
		switch {
		case isCurrent:
			kind = "current"
		case !isPremium:
			kind = "subscribe"
		case ranked && d.rank > currentRank:
			kind = "upgrade"
		case ranked && d.rank < currentRank:
			kind = "downgrade"
		default:
			kind = "subscribe"
		}
		var badge *string
		if d.badge != "" {
			b := d.badge
			badge = &b
		}
		out = append(out, PlanOptionDTO{
			ProductID: d.productID, Plan: d.plan, DisplayName: d.displayName, Interval: d.interval,
			Rank: d.rank, Badge: badge, IsCurrent: isCurrent, ChangeKind: kind,
		})
	}
	return out
}
