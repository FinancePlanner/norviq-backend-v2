-- Tax optimization tables (TaxModels.swift, TaxService.swift).

-- name: ProfileFor :one
SELECT * FROM tax_profiles WHERE user_id = $1 AND jurisdiction = $2 AND tax_year = $3 LIMIT 1;

-- name: ProfileByID :one
SELECT * FROM tax_profiles WHERE id = $1;

-- name: InsertProfile :exec
INSERT INTO tax_profiles (id, user_id, jurisdiction, tax_year, filing_status, reporting_currency, profile_json, is_complete, created_at, updated_at)
VALUES (@id, @user_id, @jurisdiction, @tax_year, @filing_status, @reporting_currency, @profile_json, @is_complete, @now, @now);

-- name: UpdateProfile :exec
UPDATE tax_profiles
SET filing_status = @filing_status, reporting_currency = @reporting_currency, profile_json = @profile_json,
    is_complete = @is_complete, updated_at = @now
WHERE id = @id;

-- name: InsertProjectionSnapshot :exec
INSERT INTO tax_projection_snapshots (id, user_id, profile_id, tax_year, jurisdiction, rule_version, status, response_json, input_hash, generated_at, created_at)
VALUES (@id, @user_id, @profile_id, @tax_year, @jurisdiction, @rule_version, @status, @response_json, @input_hash, @generated_at, @now);

-- name: DecisionsForYear :many
SELECT * FROM tax_opportunity_decisions WHERE user_id = $1 AND tax_year = $2;

-- name: DecisionFor :one
SELECT * FROM tax_opportunity_decisions WHERE user_id = $1 AND tax_year = $2 AND opportunity_id = $3 LIMIT 1;

-- name: InsertDecision :exec
INSERT INTO tax_opportunity_decisions (id, user_id, tax_year, opportunity_id, status, estimated_benefit, currency, created_at, updated_at)
VALUES (@id, @user_id, @tax_year, @opportunity_id, @status, @estimated_benefit, @currency, @now, @now);

-- name: UpdateDecision :exec
UPDATE tax_opportunity_decisions SET status = @status, estimated_benefit = @estimated_benefit, currency = @currency, updated_at = @now
WHERE id = @id;

-- name: DeleteDecision :exec
DELETE FROM tax_opportunity_decisions WHERE id = $1;

-- name: InsertScenario :exec
INSERT INTO tax_scenarios (id, user_id, profile_id, kind, request_json, response_json, created_at)
VALUES (@id, @user_id, @profile_id, @kind, @request_json, @response_json, @now);

-- name: ScenarioForUser :one
SELECT * FROM tax_scenarios WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: ScenarioForUserOfKind :one
SELECT * FROM tax_scenarios WHERE id = $1 AND user_id = $2 AND kind = $3 LIMIT 1;

-- name: ActionPlanByIdempotencyKey :one
SELECT * FROM tax_action_plans WHERE user_id = $1 AND idempotency_key = $2 LIMIT 1;

-- name: ActionPlanForUser :one
SELECT * FROM tax_action_plans WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: ActionPlansForUser :many
SELECT * FROM tax_action_plans WHERE user_id = $1 ORDER BY created_at DESC;

-- name: InsertActionPlan :exec
INSERT INTO tax_action_plans (id, user_id, scenario_id, kind, idempotency_key, status, response_json, created_at, updated_at)
VALUES (@id, @user_id, @scenario_id, @kind, @idempotency_key, @status, @response_json, @now, @now);

-- name: UpdateActionPlanResponse :exec
UPDATE tax_action_plans SET response_json = @response_json, updated_at = @now WHERE id = @id;

-- name: UpdateActionPlanStatus :exec
UPDATE tax_action_plans
SET status = @status, executed_at = @executed_at, confirmation_note = @confirmation_note, updated_at = @now
WHERE id = @id;

-- name: InsertActionLeg :exec
INSERT INTO tax_action_legs (id, action_plan_id, account_id, portfolio_id, instrument_id, symbol, side, quantity, notional, currency, lot_ids_json, status, matched_transaction_id, created_at, updated_at)
VALUES (@id, @action_plan_id, @account_id, @portfolio_id, @instrument_id, @symbol, @side, @quantity, @notional, @currency, @lot_ids_json, @status, @matched_transaction_id, @now, @now);

-- name: ActionLegsForPlanOrdered :many
SELECT * FROM tax_action_legs WHERE action_plan_id = $1 ORDER BY created_at ASC;

-- name: ActionLegsForPlan :many
SELECT * FROM tax_action_legs WHERE action_plan_id = $1;

-- name: UpdateActionLegStatus :exec
UPDATE tax_action_legs SET status = @status, updated_at = @now WHERE id = @id;

-- name: RebalancingLinksForPlan :many
SELECT * FROM tax_action_rebalancing_plans WHERE action_plan_id = $1 ORDER BY created_at ASC;

-- name: InsertRebalancingLink :exec
INSERT INTO tax_action_rebalancing_plans (id, action_plan_id, rebalancing_plan_id, created_at)
VALUES (@id, @action_plan_id, @rebalancing_plan_id, @now);

-- name: RestrictionWindowExistsForLeg :one
SELECT EXISTS (SELECT 1 FROM tax_restriction_windows WHERE action_leg_id = $1);

-- name: InsertRestrictionWindow :exec
INSERT INTO tax_restriction_windows (id, user_id, action_leg_id, jurisdiction, tax_identity_key, starts_at, ends_at, status, violating_transaction_id, created_at, updated_at)
VALUES (@id, @user_id, @action_leg_id, @jurisdiction, @tax_identity_key, @starts_at, @ends_at, @status, NULL, @now, @now);

-- name: MarkRestrictionWindowViolated :exec
UPDATE tax_restriction_windows SET status = 'violated', violating_transaction_id = @transaction_id, updated_at = @now WHERE id = @id;

-- name: NotificationPreferenceForUser :one
SELECT * FROM tax_notification_preferences WHERE user_id = $1 LIMIT 1;

-- name: InsertNotificationPreference :exec
INSERT INTO tax_notification_preferences (id, user_id, enabled, minimum_benefit, cooldown_days, updated_at)
VALUES (@id, @user_id, @enabled, @minimum_benefit, @cooldown_days, @now);

-- name: UpdateNotificationPreference :exec
UPDATE tax_notification_preferences SET enabled = @enabled, minimum_benefit = @minimum_benefit, cooldown_days = @cooldown_days, updated_at = @now
WHERE id = @id;

-- name: TaxPackEntitlementCount :one
SELECT count(*) FROM tax_pack_entitlements WHERE user_id = $1 AND tax_year = $2;

-- name: ActiveAllocationModel :one
SELECT * FROM allocation_models WHERE portfolio_id = $1 AND created_by_user_id = $2 AND is_active = true LIMIT 1;

-- name: InsertRebalancePlanDraft :exec
INSERT INTO rebalance_plans (id, portfolio_id, model_id, created_by_user_id, model_revision, name, status, base_currency,
    drift_before_bps, drift_after_bps, total_value, estimated_fees, estimated_realized_gain_loss, simulation_json, created_at, updated_at)
VALUES (@id, @portfolio_id, @model_id, @created_by_user_id, @model_revision, @name, 'draft', @base_currency,
    0, 0, @total_value, @estimated_fees, 0, @simulation_json, @now, @now);

-- name: GoalAllocationsForPortfolios :many
SELECT * FROM financial_goal_portfolio_allocations WHERE user_id = @user_id AND portfolio_list_id = ANY(@portfolio_ids::uuid[]);

-- name: ActiveGoalsByIDs :many
SELECT * FROM financial_goals WHERE id = ANY(@ids::uuid[]) AND user_id = @user_id AND status = 'active';

-- name: GoalSnapshotsNewestFirst :many
SELECT * FROM financial_goal_progress_snapshots WHERE user_id = @user_id AND goal_id = ANY(@goal_ids::uuid[]) ORDER BY calculated_at DESC;
