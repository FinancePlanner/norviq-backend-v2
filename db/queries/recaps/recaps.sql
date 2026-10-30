-- Recap/RecapRepository.swift, RecapService.swift, RecapTimeZoneResolver.swift
-- and RecapFactsCollector.swift.

-- name: RecapByPeriod :many
SELECT * FROM recaps WHERE user_id = $1 AND period_kind = $2 AND period_key = $3 LIMIT 1;

-- name: RecapByID :many
SELECT * FROM recaps WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: ListRecaps :many
SELECT * FROM recaps WHERE user_id = $1 ORDER BY generated_at DESC;

-- name: InsertRecap :exec
INSERT INTO recaps (id, user_id, period_kind, period_key, payload, archetype, schema_version, generated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: SetRecapOpened :exec
UPDATE recaps SET opened_at = $2 WHERE id = $1;

-- name: SetRecapCompleted :exec
UPDATE recaps SET completed_at = $2 WHERE id = $1;

-- name: RecapUserCreatedAt :many
SELECT created_at FROM users WHERE id = $1 LIMIT 1;

-- name: LatestCheckInTimeZone :many
SELECT time_zone FROM gamification_check_ins WHERE user_id = $1 ORDER BY local_date DESC LIMIT 1;

-- name: RecapPilotFollowLists :many
SELECT portfolio_list_id, watchlist_list_id FROM pilot_follows WHERE user_id = $1;

-- name: RecapCheckInDates :many
SELECT local_date FROM gamification_check_ins WHERE user_id = $1;

-- name: RecapTrades :many
SELECT t.type, i.symbol
FROM transactions t
JOIN accounts a ON t.account_id = a.id
JOIN instruments i ON t.instrument_id = i.id
WHERE a.user_id = sqlc.arg(user_id) AND a.broker <> 'pilot'
  AND t.trade_date >= sqlc.arg(start_day) AND t.trade_date < sqlc.arg(end_day)
  AND t.type IN ('BUY', 'SELL', 'buy', 'sell');

-- name: RecapWatchlistSymbols :many
SELECT symbol FROM watchlist_items
WHERE user_id = sqlc.arg(user_id) AND created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at)
  AND (cardinality(sqlc.arg(excluded)::uuid[]) = 0 OR watchlist_list_id <> ALL(sqlc.arg(excluded)::uuid[]));

-- name: RecapNoteSymbols :many
SELECT symbol FROM research_notes
WHERE user_id = sqlc.arg(user_id) AND created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at);

-- name: RecapTargets :many
SELECT symbol, created_at, alert_triggered_at FROM targets
WHERE user_id = sqlc.arg(user_id)
  AND ((created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at))
    OR (alert_triggered_at >= sqlc.arg(start_at) AND alert_triggered_at < sqlc.arg(end_at)));

-- name: RecapMemoSymbols :many
SELECT primary_symbol FROM position_memos
WHERE user_id = sqlc.arg(user_id) AND created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at);

-- name: RecapStockSymbols :many
SELECT symbol FROM stocks
WHERE user_id = sqlc.arg(user_id) AND source_provider IS NULL
  AND created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at);

-- name: RecapSnapshots :many
SELECT portfolio_list_id, currency, captured_on, market_value FROM portfolio_value_snapshots
WHERE user_id = sqlc.arg(user_id) AND captured_on >= sqlc.arg(start_day) AND captured_on < sqlc.arg(end_day)
  AND (cardinality(sqlc.arg(excluded)::uuid[]) = 0 OR portfolio_list_id <> ALL(sqlc.arg(excluded)::uuid[]));

-- name: RecapDividends :many
SELECT d.currency, d.amount, d.instrument_id
FROM dividends d
JOIN accounts a ON d.account_id = a.id
WHERE a.user_id = sqlc.arg(user_id) AND a.broker <> 'pilot'
  AND d.pay_date >= sqlc.arg(start_day) AND d.pay_date < sqlc.arg(end_day);

-- name: RecapGoalContributions :many
SELECT g.base_currency, c.amount
FROM financial_goal_contributions c
JOIN financial_goals g ON c.goal_id = g.id
WHERE c.user_id = sqlc.arg(user_id) AND c.occurred_at >= sqlc.arg(start_at) AND c.occurred_at < sqlc.arg(end_at);

-- name: RecapBadges :many
SELECT badge_type::text AS badge_type, tier::text AS tier, earned_at FROM user_badges
WHERE user_id = $1 ORDER BY earned_at ASC;

-- name: RecapXPPoints :many
SELECT points FROM gamification_xp_events
WHERE user_id = sqlc.arg(user_id) AND created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at)
  AND exclude_from_leaderboard = false;

-- name: RecapPilotFollowCount :one
SELECT count(*) FROM pilot_follows
WHERE user_id = sqlc.arg(user_id) AND created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at);

-- name: RecapBoardPostCount :one
SELECT count(*) FROM board_posts
WHERE author_id = sqlc.arg(user_id) AND created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at)
  AND deleted_at IS NULL;

-- name: RecapBoardCommentCount :one
SELECT count(*) FROM board_comments
WHERE author_id = sqlc.arg(user_id) AND created_at >= sqlc.arg(start_at) AND created_at < sqlc.arg(end_at)
  AND deleted_at IS NULL;

-- name: RecapAIRequestCounts :many
SELECT request_count FROM ai_usage_monthly
WHERE user_id = sqlc.arg(user_id) AND month_start >= sqlc.arg(start_day) AND month_start < sqlc.arg(end_day);

-- name: RecapExpenseCount :one
SELECT count(*) FROM expenses
WHERE user_id = sqlc.arg(user_id) AND occurred_on >= sqlc.arg(start_day) AND occurred_on < sqlc.arg(end_day);
