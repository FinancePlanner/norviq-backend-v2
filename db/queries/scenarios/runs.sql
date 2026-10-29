-- ScenarioController scenario-definition and scenario-run routes,
-- ScenarioRunWorker / ScenarioRunProcessor and ScenarioRetentionJob.

-- name: ListScenarioDefinitions :many
-- ScenarioDefinitionModel.owned(by:).sort(createdAt, .descending).all()
SELECT * FROM scenario_definitions
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: GetScenarioDefinition :one
SELECT * FROM scenario_definitions
WHERE user_id = $1 AND id = $2
LIMIT 1;

-- name: GetScenarioDefinitionByID :one
-- The worker's `.with(\.$scenario)` eager load (no owner filter).
SELECT * FROM scenario_definitions WHERE id = $1;

-- name: InsertScenarioDefinition :exec
INSERT INTO scenario_definitions (id, user_id, portfolio_list_id, financial_goal_id, name, kind, configuration, is_saved, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UpdateScenarioDefinition :exec
UPDATE scenario_definitions
SET portfolio_list_id = $2, financial_goal_id = $3, name = $4, kind = $5,
    configuration = $6, is_saved = $7, updated_at = $8
WHERE id = $1;

-- name: DeleteScenarioDefinition :exec
DELETE FROM scenario_definitions WHERE id = $1;

-- name: GetOwnedFinancialGoal :one
-- FinancialGoalModel.owned(by:).filter(id).first(): the fields scenarios use.
SELECT id, portfolio_list_id, target_amount, target_date, monthly_contribution,
       annual_contribution_growth, inflation_assumption
FROM financial_goals
WHERE user_id = $1 AND id = $2
LIMIT 1;

-- name: GetScenarioSnapshotByID :one
-- The worker's `.with(\.$snapshot)` eager load (no owner filter).
SELECT id, user_id, portfolio_list_id, base_currency, valuation_timestamp, payload, warnings, created_at
FROM portfolio_scenario_snapshots
WHERE id = $1;

-- name: ListScenarioRuns :many
SELECT * FROM scenario_runs
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: GetScenarioRun :one
SELECT * FROM scenario_runs
WHERE user_id = $1 AND id = $2
LIMIT 1;

-- name: GetScenarioRunByID :one
-- ScenarioRunModel.find(id)
SELECT * FROM scenario_runs WHERE id = $1;

-- name: ListScenarioRunsByIDs :many
-- owned(by:).filter(\.$id ~~ runIds).all()
SELECT * FROM scenario_runs
WHERE user_id = $1 AND id = ANY(@ids::uuid[]);

-- name: FindCompletedScenarioRunByHash :one
SELECT * FROM scenario_runs
WHERE user_id = $1 AND deduplication_hash = $2 AND state = 'completed'
LIMIT 1;

-- name: InsertScenarioRun :exec
INSERT INTO scenario_runs (id, user_id, scenario_id, snapshot_id, state, progress, seed, deduplication_hash,
                           engine_version, catalog_version, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: CancelScenarioRun :exec
-- cancelRun's save: only the changed fields.
UPDATE scenario_runs SET state = 'cancelled', completed_at = $2 WHERE id = $1;

-- name: ClaimScenarioRun :one
-- ScenarioRunWorker.claim, verbatim.
WITH candidate AS (
    SELECT id FROM scenario_runs
    WHERE state = 'queued'
       OR (state = 'running' AND lease_expires_at < NOW())
    ORDER BY created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE scenario_runs AS run
SET state = 'running', progress = GREATEST(progress, 0.01),
    started_at = COALESCE(started_at, NOW()),
    lease_owner = sqlc.arg(worker_id)::text, lease_expires_at = NOW() + INTERVAL '60 seconds'
FROM candidate WHERE run.id = candidate.id
RETURNING run.id;

-- name: SetScenarioRunProgress :exec
UPDATE scenario_runs SET progress = $2 WHERE id = $1;

-- name: RenewScenarioRunLease :exec
UPDATE scenario_runs SET lease_expires_at = $2 WHERE id = $1;

-- name: CompleteScenarioRun :exec
-- execute's success save: result, state, progress, completedAt and the
-- cleared lease (Fluent sends every assigned field).
UPDATE scenario_runs
SET result = $2, state = 'completed', progress = 1, completed_at = $3,
    lease_owner = NULL, lease_expires_at = NULL
WHERE id = $1;

-- name: FailScenarioRun :exec
UPDATE scenario_runs
SET state = 'failed', error_message = $2, completed_at = $3,
    lease_owner = NULL, lease_expires_at = NULL
WHERE id = $1;

-- name: DeleteExpiredScenarioRuns :exec
-- ScenarioRetentionJob: expired terminal runs.
DELETE FROM scenario_runs
WHERE expires_at < $1 AND state IN ('completed', 'failed', 'cancelled');

-- name: ListUnsavedScenarioDefinitionIDs :many
SELECT id FROM scenario_definitions WHERE is_saved = false;

-- name: CountScenarioRunsForDefinition :one
SELECT COUNT(*)::int FROM scenario_runs WHERE scenario_id = $1;

-- name: LatestBudgetSnapshotID :one
-- ScenarioBudgetSpending: BudgetSnapshot.query.filter(user).sort(monthStart, .descending).first()
SELECT id FROM budget_snapshots
WHERE user_id = $1
ORDER BY month_start DESC
LIMIT 1;

-- name: BudgetPlanItemsForSpending :many
SELECT allocation_kind, planned_amount, user_share_percent
FROM budget_plan_items
WHERE snapshot_id = $1;

-- name: AdjustedCloses :many
-- MarketPriceBarRepository.adjustedCloses: Vapor bound Dates (timestamptz)
-- against the date column.
SELECT date, adjusted_close FROM market_price_bars
WHERE instrument_key = sqlc.arg(instrument_key)
  AND date BETWEEN sqlc.arg(from_time)::timestamptz AND sqlc.arg(to_time)::timestamptz
ORDER BY date ASC;
