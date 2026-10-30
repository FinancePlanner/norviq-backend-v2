-- Smart screens (watchlist_screens, watchlist_screen_evaluations), holding
-- risk profiles, and the earnings reminder poller's state.

-- name: ListWatchlistScreens :many
SELECT * FROM watchlist_screens WHERE user_id = $1 ORDER BY created_at DESC;

-- name: OwnedWatchlistScreen :one
SELECT * FROM watchlist_screens WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: DeleteWatchlistScreen :exec
DELETE FROM watchlist_screens WHERE id = $1;

-- name: ScreenEvaluationHistory :many
SELECT * FROM watchlist_screen_evaluations
WHERE user_id = $1 AND screen_id = $2
ORDER BY evaluated_at DESC
LIMIT 30;

-- name: InsertWatchlistScreen :exec
INSERT INTO watchlist_screens (
    id, user_id, name, watchlist_list_ids, logical_operator, groups, alerts_enabled, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.arg(now), sqlc.arg(now));

-- name: UpdateWatchlistScreen :exec
UPDATE watchlist_screens SET
    name = $2, watchlist_list_ids = $3, logical_operator = $4, groups = $5, alerts_enabled = $6,
    last_evaluated_at = NULL, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: DeleteScreenEvaluations :exec
DELETE FROM watchlist_screen_evaluations WHERE screen_id = $1;

-- name: ScreenWatchlistSymbols :many
SELECT symbol FROM watchlist_items WHERE user_id = $1 AND watchlist_list_id = ANY(sqlc.arg(list_ids)::uuid[]);

-- name: LatestScreenEvaluation :many
SELECT * FROM watchlist_screen_evaluations
WHERE user_id = $1 AND screen_id = $2
ORDER BY evaluated_at DESC
LIMIT 1;

-- name: InsertScreenEvaluation :exec
INSERT INTO watchlist_screen_evaluations (
    id, user_id, screen_id, symbol_count, match_symbols, result, is_alert_baseline, evaluated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: MarkScreenEvaluated :exec
UPDATE watchlist_screens SET last_evaluated_at = sqlc.arg(now), updated_at = sqlc.arg(now) WHERE id = $1;

-- name: ListHoldingRiskProfiles :many
SELECT * FROM holding_risk_profiles WHERE user_id = $1 ORDER BY updated_at DESC;

-- name: OwnedHolding :one
SELECT id, category::text AS category FROM stocks WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: HoldingRiskProfileByHolding :one
SELECT * FROM holding_risk_profiles WHERE user_id = $1 AND holding_id = $2 LIMIT 1;

-- name: OwnedHoldingRiskProfile :one
SELECT * FROM holding_risk_profiles WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: InsertHoldingRiskProfile :one
INSERT INTO holding_risk_profiles (
    id, user_id, holding_id, asset_category, sector, region, benchmark_proxy,
    manual_value, duration, convexity, factor_overrides, created_at, updated_at
) VALUES (
    @id, @user_id, @holding_id, @asset_category, @sector, @region, @benchmark_proxy,
    @manual_value, @duration, @convexity, @factor_overrides, @now, @now
)
RETURNING *;

-- name: UpdateHoldingRiskProfile :one
UPDATE holding_risk_profiles SET
    asset_category = @asset_category,
    sector = @sector,
    region = @region,
    benchmark_proxy = @benchmark_proxy,
    manual_value = @manual_value,
    duration = @duration,
    convexity = @convexity,
    factor_overrides = @factor_overrides,
    updated_at = @now
WHERE id = @id
RETURNING *;

-- name: DeleteHoldingRiskProfile :exec
DELETE FROM holding_risk_profiles WHERE id = $1;

-- name: EarningsReminderEnabled :one
SELECT enabled FROM earnings_notification_preferences WHERE user_id = $1 LIMIT 1;

-- name: EarningsReminderDelivered :one
SELECT count(*) FROM earnings_notification_deliveries
WHERE user_id = @user_id AND symbol = @symbol AND earnings_date = @earnings_date AND lead_days = @lead_days;

-- name: InsertEarningsReminderDelivery :exec
INSERT INTO earnings_notification_deliveries (id, user_id, symbol, earnings_date, lead_days, sent_at)
VALUES (@id, @user_id, @symbol, @earnings_date, @lead_days, @sent_at);
