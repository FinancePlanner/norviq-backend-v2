-- XP ledger, check-ins, budget streak and leaderboard inputs
-- (XPService, LeaderboardService, LeaderboardReturns).

-- name: XPEventExists :one
SELECT EXISTS (SELECT 1 FROM gamification_xp_events WHERE user_id = @user_id AND dedupe_key = @dedupe_key);

-- name: InsertXPEvent :execrows
-- Zero rows: a concurrent request paid this key first (the unique
-- constraint Swift's insertIgnoringDuplicate caught).
INSERT INTO gamification_xp_events (id, user_id, type, points, dedupe_key, exclude_from_leaderboard, created_at)
VALUES (@id, @user_id, @type, @points, @dedupe_key, @exclude_from_leaderboard, @created_at)
ON CONFLICT (user_id, dedupe_key) DO NOTHING;

-- name: XPPoints :many
SELECT points FROM gamification_xp_events WHERE user_id = $1;

-- name: XPPointsSince :many
SELECT points FROM gamification_xp_events
WHERE user_id = @user_id AND created_at >= @since AND exclude_from_leaderboard = false;

-- name: XPEventsPage :many
SELECT id, type, points, created_at FROM gamification_xp_events
WHERE user_id = @user_id
ORDER BY created_at DESC, id DESC
OFFSET @row_offset::bigint LIMIT @row_limit::bigint;

-- name: XPInInterval :many
SELECT user_id, points FROM gamification_xp_events
WHERE user_id = ANY(@user_ids::uuid[])
  AND exclude_from_leaderboard = false
  AND created_at >= @start_at
  AND created_at < @end_at;

-- name: CheckInExists :one
SELECT EXISTS (SELECT 1 FROM gamification_check_ins WHERE user_id = @user_id AND local_date = @local_date);

-- name: InsertCheckIn :execrows
INSERT INTO gamification_check_ins (id, user_id, local_date, time_zone, created_at)
VALUES (@id, @user_id, @local_date, @time_zone, @created_at)
ON CONFLICT (user_id, local_date) DO NOTHING;

-- name: CheckInDates :many
SELECT user_id, local_date FROM gamification_check_ins WHERE user_id = ANY(@user_ids::uuid[]);

-- name: LastCheckInDate :one
SELECT local_date FROM gamification_check_ins WHERE user_id = $1 ORDER BY local_date DESC LIMIT 1;

-- name: BudgetStreakByUser :one
SELECT * FROM gamification_budget_streaks WHERE user_id = $1 LIMIT 1;

-- name: InsertBudgetStreak :execrows
INSERT INTO gamification_budget_streaks (id, user_id, months, best_months, created_at, updated_at)
VALUES (@id, @user_id, @months, @months, @created_at, @created_at)
ON CONFLICT (user_id) DO NOTHING;

-- name: UpdateBudgetStreak :exec
UPDATE gamification_budget_streaks SET months = @months, best_months = @best_months, updated_at = @updated_at
WHERE id = @id;

-- name: BudgetMonthsForUsers :many
SELECT user_id, months FROM gamification_budget_streaks WHERE user_id = ANY(@user_ids::uuid[]);

-- name: StreakBudgetSnapshots :many
-- ExpensesService.getMonthlyReports, reduced to what verifiedBudgetStreak reads.
SELECT id, month_start FROM budget_snapshots WHERE user_id = $1;

-- name: StreakBudgetPlanItems :many
SELECT snapshot_id, planned_amount FROM budget_plan_items WHERE user_id = $1;

-- name: StreakExpenses :many
SELECT amount, occurred_on FROM expenses WHERE user_id = $1;

-- name: ActualPortfolioLists :many
SELECT id, user_id FROM portfolio_lists
WHERE user_id = ANY(@user_ids::uuid[]) AND mode = 'actual' AND archived_at IS NULL;

-- name: ReturnSnapshots :many
SELECT user_id, portfolio_list_id, captured_on, market_value, cost_basis FROM portfolio_value_snapshots
WHERE user_id = ANY(@user_ids::uuid[])
  AND portfolio_list_id = ANY(@list_ids::uuid[])
  AND captured_on >= @from_at::timestamptz
  AND captured_on < @end_at::timestamptz;
