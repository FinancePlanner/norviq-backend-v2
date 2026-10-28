-- Entitlement resolution, usage counters and the billing context
-- (EntitlementResolver.swift, BillingContextService.swift).

-- name: ResolverUser :one
SELECT id, email, email_verified_at, had_trial, trial_started_at, trial_days, trial_tier
FROM users
WHERE id = $1;

-- name: EntitlementByUser :one
SELECT id, user_id, level, subscription_id, created_at, updated_at
FROM entitlements
WHERE user_id = $1
LIMIT 1;

-- name: InsertEntitlement :exec
INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5);

-- name: UpdateEntitlement :exec
UPDATE entitlements
SET user_id = $2, level = $3, subscription_id = $4, updated_at = $5
WHERE id = $1;

-- name: CurrentSubscription :one
-- Fluent's .descending sort: Postgres puts NULL timestamps first.
SELECT *
FROM subscriptions
WHERE user_id = $1
ORDER BY updated_at DESC, created_at DESC
LIMIT 1;

-- name: UsageCounterByUser :one
SELECT *
FROM usage_counters
WHERE user_id = $1
LIMIT 1;

-- name: ResetUsageCounterPeriod :exec
UPDATE usage_counters
SET period_start = $2, csv_import_count = 0, report_generation_count = 0, updated_at = $3
WHERE id = $1;

-- name: InsertUsageCounterIfMissing :exec
-- Two requests for the same user can both read "no counter yet"; DO NOTHING
-- makes the losing insert harmless and the row is read back either way.
INSERT INTO usage_counters (
    id, user_id, period_start, holding_count, watchlist_item_count,
    csv_import_count, target_alert_count, report_generation_count,
    created_at, updated_at
)
VALUES ($1, $2, $3, 0, 0, 0, 0, 0, now(), now())
ON CONFLICT (user_id) DO NOTHING;

-- name: SetUsageCount :exec
-- Writes exactly one counter column, as Fluent saved only the field the
-- caller changed; the other columns keep whatever the row holds now.
UPDATE usage_counters
SET holding_count = CASE WHEN @col::text = 'holding_count' THEN @value::bigint ELSE holding_count END,
    watchlist_item_count = CASE WHEN @col::text = 'watchlist_item_count' THEN @value::bigint ELSE watchlist_item_count END,
    csv_import_count = CASE WHEN @col::text = 'csv_import_count' THEN @value::bigint ELSE csv_import_count END,
    target_alert_count = CASE WHEN @col::text = 'target_alert_count' THEN @value::bigint ELSE target_alert_count END,
    report_generation_count = CASE WHEN @col::text = 'report_generation_count' THEN @value::bigint ELSE report_generation_count END,
    updated_at = @updated_at
WHERE id = @id;

-- name: ResourceCounts :one
SELECT
    (SELECT count(*) FROM portfolio_lists p WHERE p.user_id = $1)::bigint AS portfolio_lists,
    (SELECT count(*) FROM stocks s WHERE s.user_id = $1)::bigint AS holdings,
    (SELECT count(*) FROM watchlist_items w WHERE w.user_id = $1)::bigint AS watchlist_items,
    (SELECT count(*) FROM stock_valuations v WHERE v.user_id = $1)::bigint AS valuation_cases,
    (SELECT count(*) FROM targets t WHERE t.user_id = $1)::bigint AS target_alerts;
