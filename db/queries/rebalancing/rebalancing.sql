-- RebalancingService.swift / RebalancingAlertEvaluator.swift: allocation
-- models, plans, drift alerts and notification preferences. Sorts mirror
-- the Fluent queries exactly (PG's default NULLS placement included).

-- name: ListModels :many
SELECT * FROM allocation_models
WHERE portfolio_id = $1
ORDER BY is_active DESC, created_at ASC;

-- name: GetModelInPortfolio :many
SELECT * FROM allocation_models WHERE id = $1 AND portfolio_id = $2 LIMIT 1;

-- name: GetModel :many
SELECT * FROM allocation_models WHERE id = $1 LIMIT 1;

-- name: ActiveModel :many
SELECT * FROM allocation_models
WHERE portfolio_id = $1 AND is_active = true
ORDER BY updated_at DESC
LIMIT 1;

-- name: AllActiveModels :many
SELECT * FROM allocation_models WHERE is_active = true;

-- name: DeactivateModels :exec
UPDATE allocation_models SET is_active = false, updated_at = sqlc.arg(now)
WHERE portfolio_id = sqlc.arg(portfolio_id) AND is_active = true
  AND (sqlc.narg(except_id)::uuid IS NULL OR id <> sqlc.narg(except_id)::uuid);

-- name: InsertModel :exec
INSERT INTO allocation_models (
    id, portfolio_id, created_by_user_id, name, grouping_mode, is_active, revision,
    base_currency, default_target_threshold_bps, total_threshold_bps,
    fractional_shares_enabled, quantity_increment, minimum_trade_amount, flat_fee,
    variable_fee_bps, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, sqlc.arg(now), sqlc.arg(now)
);

-- name: UpdateModel :exec
UPDATE allocation_models SET
    name = $2, grouping_mode = $3, is_active = $4, revision = $5,
    default_target_threshold_bps = $6, total_threshold_bps = $7,
    fractional_shares_enabled = $8, quantity_increment = $9, minimum_trade_amount = $10,
    flat_fee = $11, variable_fee_bps = $12, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: ActivateModel :exec
UPDATE allocation_models SET is_active = true, revision = $2, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: DeleteLeaves :exec
DELETE FROM allocation_leaves WHERE model_id = $1;

-- name: DeleteBuckets :exec
DELETE FROM allocation_buckets WHERE model_id = $1;

-- name: InsertBucket :exec
INSERT INTO allocation_buckets (id, model_id, name, target_bps, alert_threshold_bps, sector_key, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: InsertLeaf :exec
INSERT INTO allocation_leaves (id, model_id, bucket_id, kind, symbol, name, target_bps, alert_threshold_bps, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: BucketsForModel :many
SELECT * FROM allocation_buckets WHERE model_id = $1 ORDER BY sort_order ASC;

-- name: LeavesForModel :many
SELECT * FROM allocation_leaves WHERE model_id = $1 ORDER BY sort_order ASC;

-- name: ResolveOpenAlertsForModel :exec
UPDATE rebalancing_alerts
SET status = 'resolved', resolved_at = sqlc.arg(now), active_scope_key = NULL, updated_at = sqlc.arg(now)
WHERE model_id = $1 AND status <> 'resolved';

-- name: CountOpenAlertsForPortfolioUser :one
SELECT count(*) FROM rebalancing_alerts
WHERE portfolio_id = $1 AND user_id = $2 AND status <> 'resolved';

-- name: CountOpenAlertsForUser :one
SELECT count(*) FROM rebalancing_alerts WHERE user_id = $1 AND status <> 'resolved';

-- name: AlertsForUser :many
SELECT * FROM rebalancing_alerts WHERE user_id = $1 ORDER BY triggered_at DESC LIMIT 100;

-- name: GetAlertForUser :many
SELECT * FROM rebalancing_alerts WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: AcknowledgeAlert :exec
UPDATE rebalancing_alerts SET status = 'acknowledged', acknowledged_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: ActiveScopeAlerts :many
SELECT * FROM rebalancing_alerts
WHERE model_id = $1 AND user_id = $2 AND active_scope_key IS NOT NULL;

-- name: ResolveAlert :exec
UPDATE rebalancing_alerts
SET status = 'resolved', resolved_at = sqlc.arg(now), active_scope_key = NULL,
    drift_bps = $2, threshold_bps = $3, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: UpdateAlertDrift :exec
UPDATE rebalancing_alerts SET drift_bps = $2, threshold_bps = $3, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: InsertAlert :exec
INSERT INTO rebalancing_alerts (
    id, portfolio_id, model_id, user_id, scope_id, scope_name, drift_bps, threshold_bps,
    status, active_scope_key, triggered_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'open', $9, sqlc.arg(now), sqlc.arg(now), sqlc.arg(now));

-- name: InsertPlan :exec
INSERT INTO rebalance_plans (
    id, portfolio_id, model_id, created_by_user_id, model_revision, name, status, base_currency,
    drift_before_bps, drift_after_bps, total_value, estimated_fees, estimated_realized_gain_loss,
    simulation_json, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 'draft', $7, $8, $9, $10, $11, $12, $13, sqlc.arg(now), sqlc.arg(now));

-- name: PlansForPortfolio :many
SELECT * FROM rebalance_plans WHERE portfolio_id = $1 ORDER BY created_at DESC LIMIT 100;

-- name: GetPlanInPortfolio :many
SELECT * FROM rebalance_plans WHERE id = $1 AND portfolio_id = $2 LIMIT 1;

-- name: UpdatePlanTransition :exec
UPDATE rebalance_plans SET
    status = $2, completed_at = $3, completion_note = $4, cancelled_at = $5, exported_at = $6,
    updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: CompletedPlans :many
SELECT * FROM rebalance_plans
WHERE portfolio_id = $1 AND status = 'completed'
ORDER BY completed_at ASC;

-- name: GetPreference :many
SELECT * FROM rebalancing_notification_preferences
WHERE portfolio_id = $1 AND user_id = $2 LIMIT 1;

-- name: UpdatePreference :exec
UPDATE rebalancing_notification_preferences SET push_enabled = $2, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: InsertPreference :exec
INSERT INTO rebalancing_notification_preferences (id, portfolio_id, user_id, push_enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, sqlc.arg(now), sqlc.arg(now));

-- name: StocksForPortfolio :many
SELECT symbol, shares, buy_price FROM stocks WHERE user_id = $1 AND portfolio_list_id = $2;

-- name: CashPositionsForPortfolio :many
SELECT currency, balance FROM portfolio_cash_positions WHERE portfolio_id = $1;

-- name: OwnedOpenPortfolioIDs :many
SELECT id FROM portfolio_lists WHERE user_id = $1 AND archived_at IS NULL;

-- name: ActiveMemberPortfolioIDs :many
SELECT portfolio_id FROM portfolio_memberships WHERE user_id = $1 AND status = 'active';

-- name: ActiveMemberUserIDs :many
SELECT user_id FROM portfolio_memberships WHERE portfolio_id = $1 AND status = 'active';
