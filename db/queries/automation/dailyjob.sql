-- WealthAutomationDailyJob.

-- name: AcquireWealthAutomationLease :many
-- Verbatim from acquireLease(on:): the row is taken only when the previous
-- holder's 30 minutes are up, and never released.
INSERT INTO automation_job_leases (name, owner, locked_until, updated_at)
VALUES ('wealth-automation-daily', sqlc.arg(owner)::text, NOW() + INTERVAL '30 minutes', NOW())
ON CONFLICT (name) DO UPDATE
SET owner = EXCLUDED.owner, locked_until = EXCLUDED.locked_until, updated_at = NOW()
WHERE automation_job_leases.locked_until < NOW()
RETURNING owner;

-- name: AlertingWatchlistScreens :many
SELECT id, user_id, groups FROM watchlist_screens WHERE alerts_enabled = true;

-- name: EnabledRebalancingPolicies :many
SELECT * FROM rebalancing_policies WHERE enabled = true;

-- name: PendingRebalanceEventPreview :many
SELECT preview FROM rebalance_events WHERE policy_id = $1 AND status = 'pending' LIMIT 1;

-- name: InsertRebalanceEvent :exec
INSERT INTO rebalance_events (id, user_id, policy_id, status, preview, created_at)
VALUES ($1, $2, $3, 'pending', $4, sqlc.arg(now));

-- name: MarkRebalancingPolicyTriggered :exec
UPDATE rebalancing_policies SET last_triggered_at = sqlc.arg(now), updated_at = sqlc.arg(now) WHERE id = $1;

-- name: AlertingBudgetSnapshotsInMonth :many
SELECT id, user_id, month_start FROM budget_snapshots
WHERE alerts_enabled = true AND month_start >= sqlc.arg(month_start)::timestamptz AND month_start < sqlc.arg(next_month)::timestamptz;
