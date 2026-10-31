-- Expenses, categories, budget snapshots, plan items, recurring templates and
-- report suggestion dismissals (Vapor's DefaultExpensesService and
-- ReportsController). Date columns are compared against timestamptz binds,
-- exactly as Fluent bound Swift Dates, so boundary behavior matches.

-- name: ListSnapshotsByUser :many
SELECT * FROM budget_snapshots WHERE user_id = $1;

-- name: ListSnapshotsByUserRange :many
SELECT * FROM budget_snapshots
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR month_start >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR month_start <= sqlc.narg(to_at)::timestamptz);

-- name: LatestSnapshotByUser :one
SELECT * FROM budget_snapshots WHERE user_id = $1 ORDER BY month_start DESC LIMIT 1;

-- name: GetSnapshot :one
SELECT * FROM budget_snapshots WHERE id = $1;

-- name: InsertSnapshot :exec
INSERT INTO budget_snapshots (
    id, user_id, month_start, net_salary, target_shares, currency_code,
    category_drift_threshold, total_drift_threshold, alerts_enabled,
    alert_on_unbudgeted, revision, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
);

-- name: UpdateSnapshot :exec
UPDATE budget_snapshots SET
    month_start = $2, net_salary = $3, target_shares = $4, currency_code = $5,
    category_drift_threshold = $6, total_drift_threshold = $7,
    alerts_enabled = $8, alert_on_unbudgeted = $9, revision = $10, updated_at = $11
WHERE id = $1;

-- name: SetSnapshotRevision :exec
UPDATE budget_snapshots SET revision = $2, updated_at = $3 WHERE id = $1;

-- name: DeleteSnapshot :exec
DELETE FROM budget_snapshots WHERE id = $1;

-- name: AdvisoryXactLock :exec
SELECT pg_advisory_xact_lock(hashtext($1::text));

-- name: ListPlanItemsByUser :many
SELECT * FROM budget_plan_items WHERE user_id = $1;

-- name: ListPlanItemsByUserSnapshot :many
SELECT * FROM budget_plan_items WHERE user_id = $1 AND snapshot_id = $2;

-- name: ListPlanItemsWithCategoryName :many
-- BudgetPlanItem.query(...).with(\.$category): the item plus its category's name.
SELECT sqlc.embed(budget_plan_items), c.name AS category_name
FROM budget_plan_items
LEFT JOIN expense_categories c ON c.id = budget_plan_items.category_id
WHERE budget_plan_items.snapshot_id = $1 AND budget_plan_items.user_id = $2;

-- name: GetPlanItem :one
SELECT * FROM budget_plan_items WHERE id = $1;

-- name: InsertPlanItem :exec
INSERT INTO budget_plan_items (
    id, snapshot_id, user_id, title, planned_amount, pillar, split_mode,
    user_share_percent, category_id, target_type, income_percentage,
    threshold_override, allocation_kind, reallocation_eligible,
    destination_financial_goal_id, destination_portfolio_list_id,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
);

-- name: UpdatePlanItem :exec
UPDATE budget_plan_items SET
    title = $2, planned_amount = $3, pillar = $4, split_mode = $5,
    user_share_percent = $6, category_id = $7, target_type = $8,
    income_percentage = $9, threshold_override = $10, allocation_kind = $11,
    reallocation_eligible = $12, destination_financial_goal_id = $13,
    destination_portfolio_list_id = $14, updated_at = $15
WHERE id = $1;

-- name: DeletePlanItem :exec
DELETE FROM budget_plan_items WHERE id = $1;

-- name: ListExpensesPage :many
-- getExpenses: newest first, the cursor already moved to the preceding day.
SELECT * FROM expenses
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR occurred_on >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR occurred_on <= sqlc.narg(to_at)::timestamptz)
  AND (sqlc.narg(through_at)::timestamptz IS NULL OR occurred_on <= sqlc.narg(through_at)::timestamptz)
ORDER BY occurred_on DESC
LIMIT sqlc.arg(row_limit);

-- name: SearchExpenses :many
SELECT * FROM expenses
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR occurred_on >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR occurred_on <= sqlc.narg(to_at)::timestamptz)
  AND (sqlc.narg(category_id)::uuid IS NULL OR category_id = sqlc.narg(category_id)::uuid)
  AND (sqlc.narg(pillar)::text IS NULL OR pillar = sqlc.narg(pillar)::text)
  AND (sqlc.narg(before_at)::timestamptz IS NULL OR occurred_on < sqlc.narg(before_at)::timestamptz)
ORDER BY occurred_on DESC
LIMIT 1000;

-- name: ListExpensesRange :many
SELECT * FROM expenses
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR occurred_on >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR occurred_on <= sqlc.narg(to_at)::timestamptz);

-- name: ListExpensesInMonth :many
SELECT * FROM expenses
WHERE user_id = sqlc.arg(user_id)
  AND occurred_on >= sqlc.arg(from_at)::timestamptz
  AND occurred_on < sqlc.arg(before_at)::timestamptz;

-- name: GetExpense :one
SELECT * FROM expenses WHERE id = $1;

-- name: InsertExpense :exec
INSERT INTO expenses (
    id, user_id, title, amount, pillar, occurred_on, linked_item_id, split_mode,
    user_share_percent, category_id, foreign_amount, foreign_currency,
    exchange_rate, notes, receipt_metadata, created_at, updated_at, external_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
);

-- name: ListExpenseExternalIDs :many
-- The import dedupe: which of these external ids the user already stored
-- (expenses_user_id_external_id_key).
SELECT external_id FROM expenses
WHERE user_id = @user_id AND external_id = ANY(@external_ids::text[]);

-- name: UpdateExpense :exec
UPDATE expenses SET
    title = $2, amount = $3, pillar = $4, occurred_on = $5, linked_item_id = $6,
    split_mode = $7, user_share_percent = $8, category_id = $9,
    foreign_amount = $10, foreign_currency = $11, exchange_rate = $12,
    notes = $13, receipt_metadata = $14, updated_at = $15
WHERE id = $1;

-- name: DeleteExpense :exec
DELETE FROM expenses WHERE id = $1;

-- name: ListCategories :many
SELECT * FROM expense_categories WHERE user_id = $1;

-- name: GetCategory :one
SELECT * FROM expense_categories WHERE id = $1;

-- name: InsertCategory :exec
INSERT INTO expense_categories (id, user_id, name, pillar, is_default, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: DeleteCategory :exec
DELETE FROM expense_categories WHERE id = $1;

-- name: ListRecurringTemplates :many
SELECT * FROM recurring_templates WHERE user_id = $1;

-- name: GetRecurringTemplate :one
SELECT * FROM recurring_templates WHERE id = $1;

-- name: InsertRecurringTemplate :exec
INSERT INTO recurring_templates (
    id, user_id, title, amount, pillar, category_id, frequency, split_mode,
    user_share_percent, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UpdateRecurringTemplate :exec
UPDATE recurring_templates SET
    title = $2, amount = $3, pillar = $4, category_id = $5, frequency = $6,
    split_mode = $7, user_share_percent = $8
WHERE id = $1;

-- name: DeleteRecurringTemplate :exec
DELETE FROM recurring_templates WHERE id = $1;

-- name: ListSuggestionDismissals :many
SELECT suggestion_id FROM report_suggestion_dismissals
WHERE user_id = $1 AND suggestion_id = ANY(sqlc.arg(suggestion_ids)::text[]);

-- name: SuggestionDismissalExists :one
SELECT EXISTS (
    SELECT 1 FROM report_suggestion_dismissals WHERE user_id = $1 AND suggestion_id = $2
);

-- name: InsertSuggestionDismissal :exec
INSERT INTO report_suggestion_dismissals (id, user_id, suggestion_id, dismissed_at)
VALUES ($1, $2, $3, $4);

-- name: FinancialGoalOwned :one
SELECT EXISTS (SELECT 1 FROM financial_goals WHERE user_id = $1 AND id = $2);

-- name: PortfolioListOwned :one
SELECT EXISTS (SELECT 1 FROM portfolio_lists WHERE id = $1 AND user_id = $2);

-- name: SetHouseholdDefaultShare :exec
UPDATE users SET household_default_user_share_percent = $2, updated_at = $3 WHERE id = $1;
