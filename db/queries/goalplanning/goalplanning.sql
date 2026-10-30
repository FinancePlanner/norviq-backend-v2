-- GoalPlanningController, GoalPlanningService and GoalPlanningDailyJob
-- (GoalPlanning/*.swift). Queries without ORDER BY mirror Fluent queries
-- that had no sort.

-- name: ListFinancialGoalsByUser :many
-- FinancialGoalModel.owned(by:).sort(\.$createdAt, .descending).all().
SELECT * FROM financial_goals
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: ListActiveFinancialGoalsByTargetDate :many
-- overview: owned, status == active, sort(\.$targetDate).
SELECT * FROM financial_goals
WHERE user_id = $1 AND status = 'active'
ORDER BY target_date;

-- name: ListAllActiveFinancialGoals :many
-- GoalPlanningDailyJob: every active goal, no sort.
SELECT * FROM financial_goals
WHERE status = 'active';

-- name: GetOwnedFinancialGoal :one
SELECT * FROM financial_goals
WHERE user_id = $1 AND id = $2
LIMIT 1;

-- name: ListOwnedFinancialGoalsByIDs :many
-- bulkUpdate: owned, id IN ids, no sort.
SELECT * FROM financial_goals
WHERE user_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CountActiveFinancialGoals :one
-- enforceActiveLimit: owned, status == active, optionally id != excluded.
SELECT count(*) FROM financial_goals
WHERE user_id = $1 AND status = 'active'
  AND (sqlc.narg(excluding)::uuid IS NULL OR id <> sqlc.narg(excluding)::uuid);

-- name: CountActiveFinancialGoalsNotIn :one
-- bulkUpdate: owned, status == active, id !~ ids.
SELECT count(*) FROM financial_goals
WHERE user_id = $1 AND status = 'active' AND NOT (id = ANY(sqlc.arg(ids)::uuid[]));

-- name: ListActiveFinancialGoalIDs :many
SELECT id FROM financial_goals
WHERE user_id = $1 AND status = 'active';

-- name: InsertFinancialGoal :exec
INSERT INTO financial_goals (
    id, user_id, portfolio_list_id, name, goal_type, target_amount, target_date, base_currency,
    starting_capital, monthly_contribution, annual_contribution_growth, inflation_assumption,
    risk_profile, expected_annual_return, status, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
);

-- name: UpdateFinancialGoal :exec
-- Fluent save of an updated model: every assigned field plus updated_at.
UPDATE financial_goals SET
    portfolio_list_id = $2, name = $3, goal_type = $4, target_amount = $5, target_date = $6,
    base_currency = $7, starting_capital = $8, monthly_contribution = $9,
    annual_contribution_growth = $10, inflation_assumption = $11, risk_profile = $12,
    expected_annual_return = $13, status = $14, updated_at = $15
WHERE id = $1;

-- name: UpdateFinancialGoalStatus :exec
UPDATE financial_goals SET status = $2, updated_at = $3
WHERE id = $1;

-- name: DeleteFinancialGoal :exec
DELETE FROM financial_goals WHERE id = $1;

-- name: PortfolioOwned :one
SELECT EXISTS (SELECT 1 FROM portfolio_lists WHERE id = $1 AND user_id = $2);

-- name: FallbackPortfolioID :one
-- PortfolioList.query.filter(userId).sort(\.$isDefault, .descending).first().
SELECT id FROM portfolio_lists
WHERE user_id = $1
ORDER BY is_default DESC
LIMIT 1;

-- name: ExpenseCategoryOwned :one
SELECT EXISTS (SELECT 1 FROM expense_categories WHERE id = $1 AND user_id = $2);

-- name: ListAllocationsByGoalSorted :many
-- goalDTO: sort(\.$createdAt).
SELECT * FROM financial_goal_portfolio_allocations
WHERE goal_id = $1
ORDER BY created_at;

-- name: ListAllocationsByGoal :many
-- allocationModels: no sort.
SELECT * FROM financial_goal_portfolio_allocations
WHERE goal_id = $1;

-- name: ListAllocationsForPortfolio :many
-- validateAllocationCapacity: rows on the portfolio with their goal's status.
SELECT a.goal_id, a.allocation_percentage, g.status AS goal_status
FROM financial_goal_portfolio_allocations a
JOIN financial_goals g ON g.id = a.goal_id
WHERE a.portfolio_list_id = $1 AND a.user_id = $2;

-- name: ListAllocationsByUser :many
-- validateBulkActivation: every allocation row of the user.
SELECT * FROM financial_goal_portfolio_allocations
WHERE user_id = $1;

-- name: DeleteAllocationsByGoal :exec
DELETE FROM financial_goal_portfolio_allocations WHERE goal_id = $1;

-- name: InsertAllocation :exec
INSERT INTO financial_goal_portfolio_allocations (id, goal_id, user_id, portfolio_list_id, allocation_percentage, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListCategoryLinksByGoalSorted :many
SELECT * FROM financial_goal_expense_category_links
WHERE goal_id = $1
ORDER BY created_at;

-- name: ListCategoryIDsByGoalRole :many
SELECT category_id FROM financial_goal_expense_category_links
WHERE goal_id = $1 AND role = $2;

-- name: DeleteCategoryLinksByGoal :exec
DELETE FROM financial_goal_expense_category_links WHERE goal_id = $1;

-- name: InsertCategoryLink :exec
INSERT INTO financial_goal_expense_category_links (id, goal_id, user_id, category_id, role, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListHoldingsForPortfolio :many
-- currentValue: Stock.query.filter(userId).filter(portfolioListId).all().
SELECT symbol, shares, buy_price FROM stocks
WHERE user_id = $1 AND portfolio_list_id = $2;

-- name: ListHoldingCategoriesForPortfolios :many
-- supportsAllocationAdvice.
SELECT category::text AS category FROM stocks
WHERE user_id = $1 AND portfolio_list_id = ANY(sqlc.arg(portfolio_ids)::uuid[]);

-- name: ListContributionAmountsSince :many
SELECT amount FROM financial_goal_contributions
WHERE goal_id = $1 AND user_id = $2 AND occurred_at >= sqlc.arg(since)::timestamptz;

-- name: ListExpenseAmountsSince :many
-- Expense.query.filter(user).filter(occurredOn >= start).filter(category ~~ ids):
-- a date compared with a timestamptz bind is promoted to timestamptz.
SELECT amount FROM expenses
WHERE user_id = $1 AND occurred_on >= sqlc.arg(since)::timestamptz
  AND category_id = ANY(sqlc.arg(category_ids)::uuid[]);

-- name: ListContributionsByGoal :many
SELECT * FROM financial_goal_contributions
WHERE goal_id = $1
ORDER BY occurred_at DESC;

-- name: InsertContribution :exec
INSERT INTO financial_goal_contributions (id, goal_id, user_id, amount, occurred_at, note, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: DeleteOwnedContribution :execrows
DELETE FROM financial_goal_contributions
WHERE id = $1 AND goal_id = $2 AND user_id = $3;

-- name: ListSnapshotsByGoal :many
SELECT * FROM financial_goal_progress_snapshots
WHERE goal_id = $1
ORDER BY calculated_at;

-- name: SnapshotExistsSince :one
SELECT EXISTS (
    SELECT 1 FROM financial_goal_progress_snapshots
    WHERE goal_id = $1 AND calculated_at >= sqlc.arg(since)::timestamptz
);

-- name: LatestSnapshots :many
SELECT * FROM financial_goal_progress_snapshots
WHERE goal_id = $1
ORDER BY calculated_at DESC
LIMIT 2;

-- name: InsertSnapshot :exec
INSERT INTO financial_goal_progress_snapshots (
    id, goal_id, user_id, current_value, planned_value, projected_value, drift_state, is_month_end, calculated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: PruneDailySnapshots :exec
DELETE FROM financial_goal_progress_snapshots
WHERE calculated_at < sqlc.arg(cutoff)::timestamptz AND is_month_end = false;

-- name: DeleteStaleProposedSuggestions :exec
DELETE FROM financial_goal_suggestions
WHERE goal_id = $1 AND status = 'proposed' AND created_at < sqlc.arg(cutoff)::timestamptz;

-- name: ListProposedSuggestions :many
SELECT * FROM financial_goal_suggestions
WHERE goal_id = $1 AND status = 'proposed'
ORDER BY created_at DESC;

-- name: InsertSuggestion :exec
INSERT INTO financial_goal_suggestions (
    id, goal_id, user_id, kind, title, explanation, monthly_amount, allocation_percentage,
    estimated_months_changed, status, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: GetOwnedSuggestion :one
SELECT * FROM financial_goal_suggestions
WHERE id = $1 AND goal_id = $2 AND user_id = $3
LIMIT 1;

-- name: UpdateSuggestionStatus :exec
UPDATE financial_goal_suggestions SET status = $2 WHERE id = $1;

-- name: ListSuggestionIDsForGoal :many
SELECT id FROM financial_goal_suggestions
WHERE goal_id = $1 AND user_id = $2;

-- name: InsertAdjustmentDraft :exec
INSERT INTO financial_goal_adjustment_drafts (id, suggestion_id, user_id, destination, payload, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListDraftsForSuggestions :many
SELECT * FROM financial_goal_adjustment_drafts
WHERE suggestion_id = ANY(sqlc.arg(suggestion_ids)::uuid[])
ORDER BY created_at DESC;
