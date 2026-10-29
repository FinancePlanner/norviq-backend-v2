-- AchievementMetrics.countQuery: one query per catalogue key, for one user.
-- Counts are rows; yes/no keys are 0/1. Read-only over other domains' tables.

-- name: MetricFirstHolding :one
SELECT (EXISTS (SELECT 1 FROM onboarding_state o WHERE o.user_id = @user_id AND o.first_holding_at IS NOT NULL)
     OR EXISTS (SELECT 1 FROM stocks s WHERE s.user_id = @user_id))::int AS n;

-- name: MetricFirstBudget :one
SELECT EXISTS (SELECT 1 FROM onboarding_state WHERE user_id = $1 AND (first_budget_at IS NOT NULL))::int AS n;

-- name: MetricFirstGoal :one
SELECT (EXISTS (SELECT 1 FROM onboarding_state o WHERE o.user_id = @user_id AND o.first_goal_at IS NOT NULL)
     OR EXISTS (SELECT 1 FROM goals g WHERE g.user_id = @user_id))::int AS n;

-- name: MetricOnboarded :one
SELECT EXISTS (SELECT 1 FROM onboarding_state WHERE user_id = $1 AND (funnel_completed_at IS NOT NULL))::int AS n;

-- name: MetricBankLinked :one
SELECT EXISTS (SELECT 1 FROM bank_connections WHERE user_id = $1 AND (TRUE))::int AS n;

-- name: MetricBrokerLinked :one
SELECT EXISTS (SELECT 1 FROM broker_connections WHERE user_id = $1 AND (TRUE))::int AS n;

-- name: MetricBudgetStreak :one
SELECT COALESCE(MAX(best_months), 0)::int AS n FROM gamification_budget_streaks WHERE user_id = $1;

-- name: MetricExpenseDays :one
SELECT COUNT(DISTINCT (occurred_on AT TIME ZONE 'UTC')::date)::int AS n
FROM expenses WHERE user_id = $1;

-- name: MetricThesis :one
SELECT COUNT(*)::int AS n FROM research_notes WHERE user_id = $1 AND (btrim(COALESCE(thesis, '')) <> '');

-- name: MetricMemoSaved :one
SELECT EXISTS (SELECT 1 FROM position_memos WHERE user_id = $1 AND (bookmarked = true))::int AS n;

-- name: MetricWatchlistReview :one
SELECT COUNT(*)::int AS n FROM watchlist_items WHERE user_id = $1 AND (last_reviewed_at IS NOT NULL);

-- name: MetricTargetHit :one
SELECT COUNT(*)::int AS n FROM targets WHERE user_id = $1 AND (alert_triggered_at IS NOT NULL);

-- name: MetricPositionSizing :one
SELECT EXISTS (SELECT 1 FROM terminal_positions WHERE user_id = $1 AND (TRUE))::int AS n;

-- name: MetricRebalanced :one
SELECT EXISTS (SELECT 1 FROM rebalance_plans WHERE created_by_user_id = $1 AND (completed_at IS NOT NULL))::int AS n;

-- name: MetricPatientCapital :one
-- Swift bound a Date (timestamptz) against the date column.
SELECT EXISTS (SELECT 1 FROM stocks WHERE user_id = @user_id AND buy_date <= @cutoff::timestamptz)::int AS n;

-- name: MetricRetirement :one
-- Retirement plans belong to a portfolio; archiving the portfolio archives the plan.
SELECT EXISTS (
    SELECT 1 FROM retirement_plans rp
    JOIN portfolio_lists pl ON pl.id = rp.portfolio_id
    WHERE pl.user_id = $1 AND pl.archived_at IS NULL
)::int AS n;

-- name: MetricScenario :one
SELECT COUNT(*)::int AS n FROM scenario_runs WHERE user_id = $1 AND (completed_at IS NOT NULL);

-- name: MetricGoalDone :one
SELECT COUNT(*)::int AS n FROM goals WHERE user_id = $1 AND (completed_at IS NOT NULL);

-- name: MetricContributions :one
SELECT COUNT(*)::int AS n FROM financial_goal_contributions WHERE user_id = $1 AND (TRUE);

-- name: MetricArticles :one
-- Same key the articles controller writes: "u:" + Swift's uppercase uuidString.
SELECT COUNT(DISTINCT article_id)::int AS n FROM article_views
WHERE viewer_key = $1;

-- name: MetricMuse :one
SELECT COUNT(*)::int AS n FROM ai_conversations WHERE user_id = $1 AND (TRUE);

-- name: MetricMCP :one
SELECT (EXISTS (SELECT 1 FROM personal_access_tokens p WHERE p.user_id = @user_id AND p.revoked_at IS NULL)
     OR EXISTS (SELECT 1 FROM oauth_tokens o WHERE o.user_id = @user_id AND o.revoked_at IS NULL))::int AS n;

-- name: MetricPilot :one
SELECT EXISTS (SELECT 1 FROM pilot_follows WHERE user_id = $1 AND (TRUE))::int AS n;

-- name: MetricFriends :one
SELECT COUNT(*)::int AS n FROM social_friendships WHERE user_id = $1 AND (TRUE);

-- name: MetricFirstPost :one
SELECT EXISTS (SELECT 1 FROM board_posts WHERE author_id = $1 AND (deleted_at IS NULL AND ai_generated = false))::int AS n;

-- name: MetricPopularPost :one
SELECT GREATEST(COALESCE(MAX(score), 0), 0)::int AS n FROM board_posts
WHERE author_id = $1 AND deleted_at IS NULL AND ai_generated = false;

-- name: MetricGroup :one
SELECT EXISTS (SELECT 1 FROM group_members WHERE user_id = $1 AND (TRUE))::int AS n;

-- name: MetricInviter :one
-- New accounts that joined through the user's invite link.
SELECT COUNT(*)::int AS n FROM social_invite_redemptions WHERE inviter_id = $1 AND (TRUE);

-- name: MetricFoundingMember :many
-- Rank by created_at, ties broken by id; a user without created_at never
-- qualifies. No row (unknown user) measures 0.
SELECT (u.created_at IS NOT NULL AND (
    SELECT COUNT(*) FROM users o
    WHERE o.created_at < u.created_at OR (o.created_at = u.created_at AND o.id < u.id)
) < @founding_limit::bigint)::int AS n
FROM users u WHERE u.id = @user_id;

-- name: CheckInLocalDates :many
-- mirror of XPService.checkInDays (social slice owns the route-facing copy).
SELECT local_date FROM gamification_check_ins WHERE user_id = $1;

-- name: OpenedMonthlyRecapKeys :many
SELECT period_key FROM recaps
WHERE user_id = $1 AND period_kind = 'month' AND opened_at IS NOT NULL;

-- Diversified: the inputs of DatabaseStatisticsRepository.sectorAllocation
-- that the sector split reads.

-- name: StatsStocks :many
SELECT symbol, shares, buy_price FROM stocks WHERE user_id = $1;

-- name: StatsResearchNotes :many
SELECT symbol, title, thesis, risks, catalysts FROM research_notes WHERE user_id = $1;

-- name: StatsLatestQuotes :many
SELECT DISTINCT ON (symbol)
    symbol,
    currency,
    price
FROM quote_cache
WHERE symbol = ANY(@symbols::text[])
ORDER BY symbol, as_of DESC;

-- name: StatsPriceHistory :many
SELECT symbol, date, close FROM price_history
WHERE symbol = ANY(@symbols::text[]) AND date >= @from_date::timestamptz AND date <= @as_of::timestamptz
ORDER BY symbol ASC, date ASC;

-- Performance: LeaderboardReturns inputs.

-- name: ActualPortfolioListIDs :many
SELECT id FROM portfolio_lists
WHERE user_id = $1 AND mode = 'actual' AND archived_at IS NULL;

-- name: PerformanceSnapshots :many
SELECT portfolio_list_id, captured_on, market_value, cost_basis FROM portfolio_value_snapshots
WHERE user_id = @user_id AND portfolio_list_id = ANY(@list_ids::uuid[]) AND captured_on <= @as_of::timestamptz;

-- name: BenchmarkAdjustedCloses :many
-- MarketPriceBarRepository.adjustedCloses.
SELECT date, adjusted_close FROM market_price_bars
WHERE instrument_key = @instrument_key AND date BETWEEN @from_date::timestamptz AND @to_date::timestamptz
ORDER BY date ASC;
