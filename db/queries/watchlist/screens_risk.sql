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
