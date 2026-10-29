-- Budgeting engine: drift dashboards, alert policy, bulk targets,
-- reallocations, templates and spend-to-units (BudgetingEngineService.swift,
-- BudgetingEngineController.swift, BudgetDriftEvaluator).
--
-- Range filters compare the DATE columns to timestamptz parameters, exactly
-- as Fluent's Date binds did.

-- name: SnapshotForUser :one
SELECT * FROM budget_snapshots WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: SnapshotsInRange :many
SELECT * FROM budget_snapshots
WHERE user_id = @user_id
  AND month_start >= sqlc.arg(from_ts)::timestamptz
  AND month_start < sqlc.arg(to_ts)::timestamptz;

-- name: UpdateSnapshotPolicy :exec
UPDATE budget_snapshots
SET category_drift_threshold = @category_drift_threshold,
    total_drift_threshold = @total_drift_threshold,
    alerts_enabled = @alerts_enabled,
    alert_on_unbudgeted = @alert_on_unbudgeted,
    revision = @revision,
    updated_at = @updated_at
WHERE id = @id;

-- name: UpdateSnapshotRevision :exec
UPDATE budget_snapshots SET revision = @revision, updated_at = @updated_at WHERE id = @id;

-- name: InsertSnapshot :exec
INSERT INTO budget_snapshots (
    id, user_id, month_start, net_salary, target_shares, currency_code,
    category_drift_threshold, total_drift_threshold, alerts_enabled, alert_on_unbudgeted,
    revision, created_at, updated_at
) VALUES (
    @id, @user_id, @month_start, @net_salary, @target_shares, @currency_code,
    @category_drift_threshold, @total_drift_threshold, @alerts_enabled, @alert_on_unbudgeted,
    @revision, @now, @now
);

-- name: PlanItemsForSnapshot :many
SELECT * FROM budget_plan_items WHERE user_id = $1 AND snapshot_id = $2;

-- name: PlanItemsBySnapshot :many
SELECT * FROM budget_plan_items WHERE snapshot_id = $1;

-- name: PlanItemInSnapshot :one
SELECT * FROM budget_plan_items WHERE id = $1 AND snapshot_id = $2 AND user_id = $3 LIMIT 1;

-- name: UpdatePlanItemTargets :exec
UPDATE budget_plan_items
SET planned_amount = @planned_amount,
    target_type = @target_type,
    income_percentage = @income_percentage,
    threshold_override = @threshold_override,
    reallocation_eligible = @reallocation_eligible,
    updated_at = @updated_at
WHERE id = @id;

-- name: UpdatePlanItemFixedAmount :exec
UPDATE budget_plan_items
SET planned_amount = @planned_amount, target_type = 'fixed', income_percentage = NULL, updated_at = @updated_at
WHERE id = @id;

-- name: UpdatePlanItemAmount :exec
UPDATE budget_plan_items SET planned_amount = @planned_amount, updated_at = @updated_at WHERE id = @id;

-- name: InsertPlanItem :exec
INSERT INTO budget_plan_items (
    id, snapshot_id, user_id, title, planned_amount, pillar, split_mode, user_share_percent,
    category_id, target_type, income_percentage, threshold_override, allocation_kind,
    reallocation_eligible, destination_financial_goal_id, destination_portfolio_list_id,
    created_at, updated_at
) VALUES (
    @id, @snapshot_id, @user_id, @title, @planned_amount, @pillar, @split_mode, @user_share_percent,
    @category_id, @target_type, @income_percentage, @threshold_override, @allocation_kind,
    @reallocation_eligible, @destination_financial_goal_id, @destination_portfolio_list_id,
    @now, @now
);

-- name: DeletePlanItems :exec
DELETE FROM budget_plan_items WHERE id = ANY(@ids::uuid[]);

-- name: ExpensesInRange :many
SELECT id, title, amount, split_mode, user_share_percent, linked_item_id, category_id
FROM expenses
WHERE user_id = @user_id
  AND occurred_on >= sqlc.arg(from_ts)::timestamptz
  AND occurred_on < sqlc.arg(to_ts)::timestamptz;

-- name: OwnedCategoryID :one
SELECT id FROM expense_categories WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: OwnedGoal :one
SELECT id, portfolio_list_id, base_currency, monthly_contribution
FROM financial_goals WHERE user_id = $1 AND id = $2 LIMIT 1;

-- name: UpdateGoalMonthlyContribution :exec
UPDATE financial_goals SET monthly_contribution = @monthly_contribution, updated_at = @updated_at WHERE id = @id;

-- name: OwnedPortfolioExists :one
SELECT EXISTS (SELECT 1 FROM portfolio_lists WHERE id = $1 AND user_id = $2);

-- name: UserDcaSymbol :one
SELECT dca_symbol FROM users WHERE id = $1;

-- name: SetUserDcaSymbol :execrows
UPDATE users SET dca_symbol = @dca_symbol, updated_at = @updated_at WHERE id = @id;

-- name: UserHoldings :many
SELECT symbol, shares, buy_price, category::text AS category FROM stocks WHERE user_id = $1;

-- name: ReallocationByRequest :one
SELECT * FROM budget_reallocation_events WHERE user_id = $1 AND request_id = $2 LIMIT 1;

-- name: InsertReallocation :exec
INSERT INTO budget_reallocation_events (
    id, user_id, request_id, source_snapshot_id, target_snapshot_id, effective_month,
    freed_capital, financial_goal_id, portfolio_list_id, adjustments, created_at
) VALUES (
    @id, @user_id, @request_id, @source_snapshot_id, @target_snapshot_id, @effective_month,
    @freed_capital, @financial_goal_id, @portfolio_list_id, @adjustments, @created_at
);

-- name: ReallocationHistory :many
SELECT * FROM budget_reallocation_events WHERE user_id = $1 ORDER BY created_at DESC LIMIT 100;

-- name: TemplatesForUser :many
SELECT * FROM budget_templates WHERE user_id = $1 ORDER BY name;

-- name: TemplateForUser :one
SELECT * FROM budget_templates WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: InsertTemplate :exec
INSERT INTO budget_templates (id, user_id, name, items, created_at, updated_at)
VALUES (@id, @user_id, @name, @items, @now, @now);

-- name: UpdateTemplate :exec
UPDATE budget_templates SET name = @name, items = @items, updated_at = @updated_at WHERE id = @id;

-- name: DeleteTemplate :exec
DELETE FROM budget_templates WHERE id = $1;

-- name: AlertState :one
SELECT * FROM budget_drift_alert_states WHERE snapshot_id = $1 AND scope_key = $2 LIMIT 1;

-- name: InsertAlertState :exec
INSERT INTO budget_drift_alert_states (
    id, user_id, snapshot_id, scope_key, level, breach_sequence, last_notified_level,
    last_notified_at, evaluated_at, cleared_at
) VALUES (
    @id, @user_id, @snapshot_id, @scope_key, @level, @breach_sequence, @last_notified_level,
    @last_notified_at, @evaluated_at, @cleared_at
);

-- name: UpdateAlertState :exec
UPDATE budget_drift_alert_states
SET level = @level,
    breach_sequence = @breach_sequence,
    last_notified_level = @last_notified_level,
    last_notified_at = @last_notified_at,
    evaluated_at = @evaluated_at,
    cleared_at = @cleared_at
WHERE id = @id;
