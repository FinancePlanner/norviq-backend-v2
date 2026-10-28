-- Badge evaluation (BadgeService) and the XP it awards.

-- name: UserBadges :many
SELECT * FROM user_badges WHERE user_id = $1;

-- name: InsertUserBadge :one
-- No row back means the tier was already persisted by a concurrent request.
INSERT INTO user_badges (id, user_id, badge_type, tier, earned_at, created_at)
VALUES (@id, @user_id, @badge_type, @tier, @earned_at, @earned_at)
ON CONFLICT (user_id, badge_type, tier) DO NOTHING
RETURNING id;

-- name: BadgeStockSources :many
-- Pilot follows hold simulated positions; they are not the user's purchases.
SELECT source_provider, source_account_id FROM stocks
WHERE user_id = $1 AND source_provider IS DISTINCT FROM 'pilot';

-- name: BadgeBuyTransactionCount :one
SELECT count(*) FROM transactions t
JOIN accounts a ON a.id = t.account_id
WHERE a.user_id = $1 AND a.broker <> 'pilot' AND t.type = 'buy';

-- name: CountNewsViewedActivities :one
SELECT count(*) FROM user_activities WHERE user_id = $1 AND type = 'news_viewed';

-- name: BadgeBudgetSnapshots :many
SELECT id, month_start, net_salary FROM budget_snapshots WHERE user_id = $1;

-- name: BadgeBudgetPlanItems :many
SELECT snapshot_id, planned_amount, pillar FROM budget_plan_items WHERE user_id = $1;

-- name: BadgeExpenses :many
SELECT amount, pillar, occurred_on FROM expenses WHERE user_id = $1 ORDER BY occurred_on ASC;

-- name: UserCreatedAt :one
SELECT created_at FROM users WHERE id = $1;

-- name: XPEventExists :one
SELECT EXISTS (SELECT 1 FROM gamification_xp_events WHERE user_id = $1 AND dedupe_key = $2);

-- name: InsertXPEvent :exec
INSERT INTO gamification_xp_events (id, user_id, type, points, dedupe_key, created_at)
VALUES (@id, @user_id, @type, @points, @dedupe_key, @created_at)
ON CONFLICT (user_id, dedupe_key) DO NOTHING;
