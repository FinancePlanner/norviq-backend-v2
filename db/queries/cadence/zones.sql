-- Cadence zones: the zones job writes, the read endpoints read.

-- name: UpsertZone :exec
INSERT INTO cadence_zones (
    symbol, timeframe, period_end, as_of, close, trend, distance, percentile,
    sample_count, zone, capped_zone, guard_reason, guard_checked, touch_zone,
    confirmed, rule_version, computed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, now()
)
ON CONFLICT (symbol, timeframe, period_end) DO UPDATE SET
    as_of = EXCLUDED.as_of,
    close = EXCLUDED.close,
    trend = EXCLUDED.trend,
    distance = EXCLUDED.distance,
    percentile = EXCLUDED.percentile,
    sample_count = EXCLUDED.sample_count,
    zone = EXCLUDED.zone,
    capped_zone = EXCLUDED.capped_zone,
    guard_reason = EXCLUDED.guard_reason,
    guard_checked = EXCLUDED.guard_checked,
    touch_zone = EXCLUDED.touch_zone,
    confirmed = EXCLUDED.confirmed,
    rule_version = EXCLUDED.rule_version,
    computed_at = now();

-- name: DeleteZonesNotIn :exec
-- Removes periods the engine no longer returns (the 21 year window slid
-- forward, or the history became shorter). An empty keep list removes every
-- row of the timeframe.
DELETE FROM cadence_zones
WHERE symbol = $1 AND timeframe = $2
  AND NOT (period_end = ANY(sqlc.arg(keep)::date[]));

-- name: ZonesForSymbol :many
SELECT * FROM cadence_zones
WHERE symbol = $1 AND timeframe = $2 AND period_end >= sqlc.arg(from_date)::date
ORDER BY period_end ASC;

-- name: LatestZonesForSymbols :many
-- Per symbol, the newest confirmed row and the newest row overall (they are
-- the same row when the newest period is confirmed).
SELECT DISTINCT ON (symbol, confirmed) *
FROM cadence_zones
WHERE symbol = ANY(sqlc.arg(symbols)::text[]) AND timeframe = $1
ORDER BY symbol, confirmed, period_end DESC;

-- name: SymbolIsETF :one
SELECT EXISTS (
    SELECT 1 FROM stocks WHERE upper(symbol) = upper(sqlc.arg(symbol)::text) AND category = 'etf'
) AS is_etf;

-- name: UserHoldingSymbols :many
-- Real holdings only: an open position in an active, actual portfolio.
SELECT DISTINCT upper(s.symbol)::text AS symbol
FROM stocks s
JOIN portfolio_lists pl ON pl.id = s.portfolio_list_id
WHERE s.user_id = $1 AND s.shares > 0
  AND pl.mode = 'actual' AND pl.archived_at IS NULL
ORDER BY 1;

-- name: UserWatchlistSymbols :many
SELECT DISTINCT upper(symbol)::text AS symbol FROM watchlist_items
WHERE user_id = $1 AND status NOT IN ('archived', 'exited')
ORDER BY 1;

-- name: UserBullHighs :many
-- The user's newest valuation per symbol: its bull range high feeds the
-- read-time bull range guard.
SELECT DISTINCT ON (upper(symbol)) upper(symbol)::text AS symbol, bull_high
FROM stock_valuations
WHERE user_id = $1 AND upper(symbol) = ANY(sqlc.arg(symbols)::text[])
ORDER BY upper(symbol), COALESCE(updated_at, created_at) DESC;

-- name: UserTradeSizing :many
-- The sizing rules of the newest-updated active allocation model on the
-- user's default portfolio. No row means fractional shares and no minimum.
SELECT am.quantity_increment, am.minimum_trade_amount
FROM allocation_models am
JOIN portfolio_lists pl ON pl.id = am.portfolio_id
WHERE pl.user_id = $1 AND pl.is_default = true AND pl.archived_at IS NULL AND am.is_active
ORDER BY COALESCE(am.updated_at, am.created_at) DESC
LIMIT 1;
