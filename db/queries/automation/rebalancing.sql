-- WealthAutomationController rebalancing-policy routes.

-- name: OwnedPortfolioListExists :one
SELECT EXISTS (SELECT 1 FROM portfolio_lists WHERE id = $1 AND user_id = $2)::bool;

-- name: GetRebalancingPolicy :one
-- RebalancingPolicyModel.owned(by:).filter(portfolioListId).first()
SELECT id, user_id, portfolio_list_id, base_currency, cadence, drift_threshold, targets, enabled,
       last_confirmed_at, last_triggered_at, created_at, updated_at
FROM rebalancing_policies
WHERE user_id = $1 AND portfolio_list_id = $2
LIMIT 1;

-- name: InsertRebalancingPolicy :exec
INSERT INTO rebalancing_policies (id, user_id, portfolio_list_id, base_currency, cadence, drift_threshold, targets, enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UpdateRebalancingPolicy :exec
UPDATE rebalancing_policies
SET user_id = $2, portfolio_list_id = $3, base_currency = $4, cadence = $5, drift_threshold = $6,
    targets = $7, enabled = $8, updated_at = $9
WHERE id = $1;

-- name: DeleteRebalancingPolicy :exec
DELETE FROM rebalancing_policies WHERE id = $1;

-- name: ConfirmRebalancingPolicy :exec
UPDATE rebalancing_policies SET last_confirmed_at = $2, updated_at = $3 WHERE id = $1;

-- name: ListRebalanceEvents :many
SELECT id, user_id, policy_id, status, preview, created_at, confirmed_at, dismissed_at
FROM rebalance_events
WHERE user_id = $1 AND policy_id = $2
ORDER BY created_at DESC;

-- name: GetRebalanceEvent :one
SELECT id, user_id, policy_id, status, preview, created_at, confirmed_at, dismissed_at
FROM rebalance_events
WHERE id = $1 AND user_id = $2 AND policy_id = $3
LIMIT 1;

-- name: ConfirmRebalanceEvent :exec
UPDATE rebalance_events SET status = $2, confirmed_at = $3 WHERE id = $1;

-- name: DismissRebalanceEvent :exec
UPDATE rebalance_events SET status = $2, dismissed_at = $3 WHERE id = $1;
