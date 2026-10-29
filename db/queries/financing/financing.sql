-- FinancingService (Vapor Financing/FinancingService.swift). Fluent's
-- `.first()` without a sort is a bare LIMIT 1, and `.all()` without a sort has
-- no ORDER BY; both are kept as they were.

-- name: GetAssumptions :one
SELECT * FROM financing_affordability_assumptions
WHERE user_id = @user_id
LIMIT 1;

-- name: InsertAssumptions :exec
INSERT INTO financing_affordability_assumptions (
    id, user_id, income_scope, net_monthly_income_override, gross_monthly_income,
    external_monthly_debt_payments, safety_buffer_percent, monthly_savings_target_override, updated_at
) VALUES (
    @id, @user_id, @income_scope, @net_monthly_income_override, @gross_monthly_income,
    @external_monthly_debt_payments, @safety_buffer_percent, @monthly_savings_target_override, @updated_at
);

-- name: UpdateAssumptions :exec
UPDATE financing_affordability_assumptions SET
    income_scope = @income_scope,
    net_monthly_income_override = @net_monthly_income_override,
    gross_monthly_income = @gross_monthly_income,
    external_monthly_debt_payments = @external_monthly_debt_payments,
    safety_buffer_percent = @safety_buffer_percent,
    monthly_savings_target_override = @monthly_savings_target_override,
    updated_at = @updated_at
WHERE id = @id;

-- name: InsertPlan :exec
INSERT INTO financing_plans (
    id, user_id, title, market, purchase_type, currency, status, user_share_percent,
    source_domain, created_at, updated_at
) VALUES (
    @id, @user_id, @title, @market, @purchase_type, @currency, @status, @user_share_percent,
    @source_domain, @created_at, @updated_at
);

-- name: InsertRevision :exec
INSERT INTO financing_plan_revisions (id, plan_id, effective_installment, terms, created_at)
VALUES (@id, @plan_id, @effective_installment, @terms, @created_at);

-- name: ListPlans :many
SELECT * FROM financing_plans
WHERE user_id = @user_id
ORDER BY created_at DESC;

-- name: ListPlansNotCancelled :many
SELECT * FROM financing_plans
WHERE user_id = @user_id AND status <> 'cancelled';

-- name: ListPlansWithStatus :many
SELECT * FROM financing_plans
WHERE user_id = @user_id AND status = @status;

-- name: GetPlan :one
SELECT * FROM financing_plans
WHERE id = @id AND user_id = @user_id
LIMIT 1;

-- name: UpdatePlanStatus :exec
UPDATE financing_plans SET status = @status, updated_at = @updated_at
WHERE id = @id;

-- name: LatestRevision :one
SELECT * FROM financing_plan_revisions
WHERE plan_id = @plan_id
ORDER BY effective_installment DESC
LIMIT 1;

-- name: LastMatch :one
SELECT * FROM financing_expense_matches
WHERE plan_id = @plan_id
ORDER BY installment_number DESC
LIMIT 1;

-- name: ListMatches :many
SELECT * FROM financing_expense_matches
WHERE plan_id = @plan_id;

-- name: GetMatch :one
SELECT * FROM financing_expense_matches
WHERE plan_id = @plan_id AND installment_number = @installment_number
LIMIT 1;

-- name: InsertMatch :exec
INSERT INTO financing_expense_matches (id, plan_id, installment_number, expense_id, created_at)
VALUES (@id, @plan_id, @installment_number, @expense_id, @created_at);

-- name: DeleteMatch :exec
DELETE FROM financing_expense_matches WHERE id = @id;

-- name: GetExpense :one
SELECT id, title, amount, occurred_on FROM expenses
WHERE id = @id AND user_id = @user_id
LIMIT 1;

-- name: LatestBudgetSnapshot :one
SELECT id, net_salary, target_shares FROM budget_snapshots
WHERE user_id = @user_id
ORDER BY month_start DESC
LIMIT 1;

-- name: BudgetPlanItems :many
SELECT planned_amount, user_share_percent, allocation_kind FROM budget_plan_items
WHERE snapshot_id = @snapshot_id;
