-- RetirementController (Retirement/RetirementController.swift).

-- name: GetRetirementPlanByPortfolio :one
-- RetirementPlanRecord.query.filter(portfolioId).first(): no ORDER BY.
SELECT id, portfolio_id, rule_version, input_json, created_at, updated_at
FROM retirement_plans
WHERE portfolio_id = $1
LIMIT 1;

-- name: InsertRetirementPlan :exec
INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: UpdateRetirementPlan :exec
-- Fluent save of an assigned record: every assigned field plus updated_at.
UPDATE retirement_plans
SET rule_version = $2, input_json = $3, updated_at = $4
WHERE id = $1;

-- name: UpdateRetirementPlanRuleVersion :exec
UPDATE retirement_plans
SET rule_version = $2, updated_at = $3
WHERE id = $1;

-- name: ListRetirementPlansForOwner :many
-- The saved plans on a user's own active portfolios, for the assistant.
SELECT rp.id, rp.portfolio_id, rp.rule_version, rp.input_json, rp.created_at, rp.updated_at
FROM retirement_plans rp
JOIN portfolio_lists pl ON pl.id = rp.portfolio_id
WHERE pl.user_id = $1 AND pl.archived_at IS NULL
ORDER BY rp.created_at DESC
LIMIT 10;
