// Package features is the registry of ported Vapor controllers and
// background jobs. A route listed here is served by v2; every other route is
// proxied to the Vapor backend until it is ported.
package features

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/features/accountdeletion"
	"norviq-api/internal/features/achievements"
	"norviq-api/internal/features/activity"
	"norviq-api/internal/features/admin"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/articles"
	"norviq-api/internal/features/assets"
	"norviq-api/internal/features/assistant"
	"norviq-api/internal/features/autobuys"
	"norviq-api/internal/features/automation"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/features/banking"
	"norviq-api/internal/features/billing"
	"norviq-api/internal/features/boards"
	"norviq-api/internal/features/brokers"
	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/crypto"
	"norviq-api/internal/features/dashboard"
	"norviq-api/internal/features/dataexport"
	"norviq-api/internal/features/digest"
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/features/expenseimport"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/features/feedback"
	"norviq-api/internal/features/financing"
	"norviq-api/internal/features/goalplanning"
	"norviq-api/internal/features/goals"
	"norviq-api/internal/features/groups"
	"norviq-api/internal/features/identity"
	"norviq-api/internal/features/insights"
	"norviq-api/internal/features/integrations"
	"norviq-api/internal/features/macro"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/marketbrief"
	"norviq-api/internal/features/news"
	"norviq-api/internal/features/notifications"
	"norviq-api/internal/features/ops"
	"norviq-api/internal/features/pilots"
	"norviq-api/internal/features/planning"
	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/features/portfoliomgmt"
	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/features/recaps"
	"norviq-api/internal/features/receipts"
	"norviq-api/internal/features/reporting"
	"norviq-api/internal/features/research"
	"norviq-api/internal/features/retirement"
	"norviq-api/internal/features/scenarios"
	"norviq-api/internal/features/sharing"
	"norviq-api/internal/features/simulations"
	"norviq-api/internal/features/social"
	"norviq-api/internal/features/statistics"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/features/tax"
	"norviq-api/internal/features/terminalpositions"
	"norviq-api/internal/features/watchlist"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"

	"github.com/google/uuid"
)

// All returns every ported feature, in Vapor's registration order where it
// matters (a route registered twice is an error, not an override).
func All() []server.Feature {
	return []server.Feature{
		billing.Feature{},
		notifications.Feature{Premium: func(d *server.Deps) notifications.PremiumGate {
			return entitlements.NamedGate{Service: entitlements.NewFromEnv(d.DB)}
		}},
		activity.Feature{},
		// BadgeService records each new tier as a legacy achievement
		// (AchievementService.recordLegacyBadge).
		badges.Feature{OnEarned: func(d *server.Deps) badges.EarnedHook {
			return func(ctx context.Context, userID uuid.UUID, badgeType, tier string, earnedAt time.Time) {
				achievements.RecordLegacyBadge(ctx, d.DB, d.Logger, userID, badgeType, tier, earnedAt)
			}
		}},
		feedback.Feature{},
		assets.Feature{Search: func(d *server.Deps) (assets.SymbolSearch, error) {
			svc, err := market.SharedService(d)
			if err != nil {
				return nil, err
			}
			return marketSearch{svc}, nil
		}},
		aiplatform.Feature{},
		market.Feature{Premium: func(d *server.Deps) market.PremiumGate {
			return entitlements.NamedGate{Service: entitlements.NewFromEnv(d.DB)}
		}},
		// MarketBriefController (/v1/market/brief, behind MARKET_BRIEF_ENABLED).
		marketbrief.Feature{},
		earnings.Feature{},
		// MacroController (/v1/macro/*).
		macro.Feature{},
		// Watchlist, lists, CSV import, price targets, smart screens and
		// holding risk profiles.
		watchlist.Feature{},
		// TaxController (/v1/tax/*).
		tax.Feature{},
		// Identity: auth, tokens, OAuth server, profile and
		// onboarding.
		identity.Feature{},
		statistics.Feature{},
		// BudgetingEngineController (drift, reallocations, templates).
		budgeting.Feature{},
		reporting.Feature{},
		// Expenses, budget snapshots/items and reports. Expense writes run
		// the budgeting engine's drift evaluator, as DefaultExpensesService
		// did; the reports overview reads the statistics overview.
		expenses.Feature{
			Drift: budgetDrift,
			Overview: func(*server.Deps) expenses.OverviewSource {
				return expenses.StatisticsOverview{}
			},
		},
		// Spreadsheet import and receipts (throttled groups in routes.swift).
		expenseimport.Feature{},
		receipts.Feature{},
		// PortfolioController, PortfolioShareController (authenticated
		// routes).
		portfolio.Feature{},
		// Portfolio management: /v1/portfolios CRUD, members,
		// invitations, cash positions, account assignment.
		portfoliomgmt.Feature{},
		// Portfolio rebalancing (portfolio-scoped routes) and simulations.
		rebalancing.Feature{ScheduleAchievementCheck: scheduleAchievementCheck},
		simulations.Feature{},
		// Holdings, manual transactions and the stock share page.
		stocks.Feature{},
		// StockController research notes (/v1/research).
		research.Feature{},
		// RetirementController portfolio routes.
		retirement.Feature{ScheduleAchievementCheck: scheduleAchievementCheck},
		// ScenarioController: snapshot, scenario and scenario-run routes.
		scenarios.Feature{},
		// GoalPlanningController (/v1/financial-goals); unregistered when
		// GOAL_PLANNING_ENABLED is off, as in routes.swift.
		goalplanning.Feature{ScheduleAchievementCheck: scheduleAchievementCheck},
		// Dashboard GoalsController (/v1/goals).
		goals.Feature{ScheduleAchievementCheck: scheduleAchievementCheck},
		// WealthAutomationController rebalancing-policy routes.
		automation.Feature{},
		// Community boards (BoardsController).
		boards.Feature{},
		// Social graph, discovery and gamification (SocialController,
		// GamificationController).
		social.Feature{},
		// Achievements and the friends' activity feed; installs the
		// achievementhooks evaluator.
		achievements.Feature{},
		// User-published articles; behind ARTICLES_ENABLED.
		articles.Feature{},
		// Pilot follows (PILOTS_ENABLED, checked per request).
		pilots.Feature{},
		// FinancingController (/v1/financing/*).
		financing.Feature{},
		// NewsController (/v1/news/*) and FinnhubWebhookController.
		news.Feature{},
		// DashboardController (dashboard, insights, why-moved).
		dashboard.Feature{},
		// CryptoController (/v1/crypto/*, incl. the markets view).
		crypto.Feature{},
		// AI assistant, memos, action catalog, AI chat/insights/view
		// summaries.
		assistant.Feature{Sources: assistantSources},
		// Terminal position sizing and its AI suggestions
		// (TerminalPositionsController, TerminalPositionsAIController).
		terminalpositions.Feature{},
		// /v1/admin moderation (CommunityAdminController,
		// SocialModerationController).
		admin.Feature{},
		// UserProfileService.delete (DELETE /v1/users, /v1/users/:id).
		accountdeletion.Feature{},
		// DataExportController, ExportFileController (/v1/api/v3/export).
		dataexport.Feature{},
		// PortfolioShareController's public route.
		sharing.Feature{},
		// /docs, /openapi.yaml, /metrics, /internal/metrics.
		ops.Feature{},
		// InsightsController (/v1/insights/*): Hermes insights and retail
		// sentiment.
		insights.Feature{},
		// Groups (GroupsController, GroupMessagesController) and the
		// realtime stream; behind SOCIAL_ENABLED and GROUPS_ENABLED, read
		// per request.
		groups.Feature{},
		// DigestController: /v1/digests and /v1/digest-preferences behind
		// DIGEST_ENABLED (read once at boot); /v1/digest/unsubscribe always.
		digest.Feature{},
		// RecapController (/v1/recaps); 404 unless RECAP_ENABLED=true.
		recaps.Feature{ScheduleAchievementCheck: scheduleAchievementCheck},
		// TerminalPositionsController's /v1/autobuys routes.
		autobuys.Feature{},
		// PlanningController (/v1/planning).
		planning.Feature{},
		// BankController, the hosted-link callbacks and the Plaid webhook.
		banking.Feature{},
		// MessagingController (/v1/integrations/telegram).
		integrations.Feature{},
		// BrokerController and the IBKR OAuth callback.
		brokers.Feature{},
	}
}

// budgetDrift is the budgeting engine's BudgetDriftEvaluator, which
// ExpensesService and the bulk importers call after writes. A config error
// disables drift evaluation (it is best effort in Vapor too).
func budgetDrift(d *server.Deps) expenses.DriftEvaluator {
	e, err := budgeting.NewEngine(d)
	if err != nil || e.Evaluator == nil {
		d.Logger.Error("budget drift evaluator unavailable", "error", err)
		return nil
	}
	return e.Evaluator
}

// marketSearch adapts the market data service to assets.SymbolSearch; the
// result types share fields and tags.
type marketSearch struct{ svc *marketdata.Service }

func (m marketSearch) Search(ctx context.Context, q string) ([]assets.SearchResult, error) {
	rs, err := m.svc.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]assets.SearchResult, len(rs))
	for i, r := range rs {
		out[i] = assets.SearchResult(r)
	}
	return out, nil
}

// scheduleAchievementCheck is req.scheduleAchievementCheck(for:).
func scheduleAchievementCheck(userID uuid.UUID) { achievementhooks.Schedule(userID) }

// Job is a background loop: one Vapor LifecycleHandler.
type Job interface {
	Name() string
	// Run blocks until ctx is canceled.
	Run(ctx context.Context, d *server.Deps)
}

// AllJobs returns every ported background job.
func AllJobs() []Job {
	return []Job{
		identity.CleanupJob{},
		billing.TrialExpirationJob{},
		market.HistoryIngestionJob{},
		reporting.WorkerJob{},
		reporting.RetentionJob{},
		expenseimport.RetentionJob{},
		portfolio.SnapshotJob{},
		achievements.SweepJob{},
		pilots.IngestionJob{},
		pilots.MirrorJob{},
		watchlist.TargetAlertJob{},
		watchlist.EarningsReminderJob{},
		// Tax: report queue, report expiry, daily projections.
		tax.ReportGenerationJob{},
		tax.ReportCleanupJob{},
		tax.ProjectionJob{},
		news.IngestionJob{},
		news.NotificationJob{},
		// MacroRefreshJob.
		macro.RefreshJob{},
		// Crypto markets snapshot (CoinGecko + FMP YTD bases).
		crypto.RefreshJob{},
		// AI assistant: thread/tip/proposal retention, daily tips, standing
		// tasks (the last needs every read-tool source; see assistantSources).
		assistant.RetentionJob{},
		assistant.DailyTipJob{},
		assistant.WatchJob{Sources: assistantSources},
		// DataExportCleanupJob.
		dataexport.CleanupJob{},
		// Insights: Hermes pull, daily sentiment roll-up, retention.
		insights.HermesSyncJob{},
		insights.SentimentAggregationJob{},
		insights.SentimentRetentionJob{},
		// WeeklyDigestJob (lock weekly_digest_job); runs only with
		// DIGEST_ENABLED on, and Vapor's copy is off once Vapor has
		// DIGEST_ENABLED=false.
		digest.Job{},
		// Planning: rebalancing drift alerts.
		rebalancing.AlertJob{},
		// Scenario planning: run queue (claim + lease, no JobLock) and
		// retention; both no-ops unless SCENARIO_PLANNING_ENABLED.
		scenarios.RunWorkerJob{},
		scenarios.RetentionJob{},
		// GoalPlanningDailyJob (Vapor: GOAL_EVALUATOR_ENABLED=false).
		goalplanning.DailyJob{},
		// DailyBoardPostJob (also needs DAILY_BOARD_POST_ENABLED, as in Vapor).
		boards.DailyPostJob{},
		// MarketBriefJob (also needs MARKET_BRIEF_ENABLED, as in Vapor).
		marketbrief.Job{},
		// Bank reconcile sync (bank_sync_job).
		banking.SyncJob{},
		// IBKR daily sync (ibkr_sync_job).
		brokers.IBKRSyncJob{},
	}
}

// assistantSources wires the other domains' services the AI surfaces read
// (assistant.Sources). A nil field keeps the routes that need it on Vapor
// and the standing-task job off; fill each one in as its domain lands.
func assistantSources(d *server.Deps) (assistant.Sources, error) {
	var src assistant.Sources
	ins, err := insights.SharedService(d)
	if err != nil {
		return assistant.Sources{}, err
	}
	src.InsightsSummary = func(ctx context.Context, days int) (any, error) {
		return ins.Summary(ctx, days)
	}
	return src, nil
}

// Jobs tracks running jobs for shutdown.
type Jobs struct{ wg sync.WaitGroup }

// Wait blocks until every job returned.
func (j *Jobs) Wait() {
	done := make(chan struct{})
	go func() { j.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	}
}

// StartJobs starts the jobs named in V2_JOBS (comma-separated, or "all").
//
// During the migration Vapor still runs its own copies of these loops, and a
// job running in both processes would send every push and email twice. So a
// job runs in v2 only once it is listed here AND disabled on the Vapor side;
// the default is none.
func StartJobs(ctx context.Context, d *server.Deps) *Jobs {
	enabled := map[string]bool{}
	for _, n := range strings.Split(os.Getenv("V2_JOBS"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			enabled[n] = true
		}
	}
	j := &Jobs{}
	for _, job := range AllJobs() {
		if !enabled["all"] && !enabled[job.Name()] {
			continue
		}
		d.Logger.Info("job starting", "job", job.Name())
		j.wg.Add(1)
		go func() {
			defer j.wg.Done()
			job.Run(ctx, d)
		}()
	}
	return j
}
