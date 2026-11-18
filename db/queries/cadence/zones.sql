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
