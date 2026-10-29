-- Planning/PlanningController.swift and PlanningPrefillService.swift.

-- name: ListPlanningScenarios :many
SELECT * FROM planning_scenarios WHERE user_id = $1 ORDER BY created_at ASC;

-- name: GetPlanningScenario :many
SELECT * FROM planning_scenarios WHERE user_id = $1 AND id = $2 LIMIT 1;

-- name: InsertPlanningScenario :exec
INSERT INTO planning_scenarios (id, user_id, name, kind, is_default, input_json, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.arg(now), sqlc.arg(now));

-- name: UpdatePlanningScenario :exec
UPDATE planning_scenarios SET name = $2, kind = $3, is_default = $4, input_json = $5, updated_at = sqlc.arg(now)
WHERE id = $1;

-- name: ClearDefaultPlanningScenarios :exec
UPDATE planning_scenarios SET is_default = false, updated_at = sqlc.arg(now)
WHERE user_id = sqlc.arg(user_id) AND kind = sqlc.arg(kind) AND is_default = true
  AND (sqlc.narg(except_id)::uuid IS NULL OR id <> sqlc.narg(except_id)::uuid);

-- name: DeletePlanningScenario :exec
DELETE FROM planning_scenarios WHERE id = $1;

-- name: PlanningStocks :many
SELECT * FROM stocks WHERE user_id = $1;

-- name: LatestBudgetSnapshot :many
SELECT id, currency_code FROM budget_snapshots WHERE user_id = $1 ORDER BY month_start DESC LIMIT 1;

-- name: BudgetPlanItems :many
SELECT title, planned_amount, pillar, user_share_percent, allocation_kind FROM budget_plan_items WHERE snapshot_id = $1;

-- name: UserRetirementPlanInputs :many
SELECT rp.input_json FROM retirement_plans rp
WHERE rp.portfolio_id IN (SELECT id FROM portfolio_lists WHERE user_id = $1);
