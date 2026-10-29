-- Onboarding state (internal/platform/onboarding). Other domains latch the
-- guided-start steps through LatchFirst*.

-- name: EnsureOnboardingState :exec
INSERT INTO onboarding_state (id, user_id, created_at, updated_at)
VALUES ($1, $2, NOW(), NOW())
ON CONFLICT (user_id) DO NOTHING;

-- name: OnboardingStateByUser :one
SELECT * FROM onboarding_state WHERE user_id = $1 LIMIT 1;

-- name: SaveOnboardingState :exec
UPDATE onboarding_state
SET funnel_step = $2, funnel_completed_at = $3, guided_start_dismissed_at = $4, updated_at = $5
WHERE id = $1;

-- name: LatchFirstHolding :exec
INSERT INTO onboarding_state (id, user_id, first_holding_at, created_at, updated_at)
VALUES ($1, $2, NOW(), NOW(), NOW())
ON CONFLICT (user_id) DO UPDATE
SET first_holding_at = COALESCE(onboarding_state.first_holding_at, EXCLUDED.first_holding_at), updated_at = NOW();

-- name: LatchFirstBudget :exec
INSERT INTO onboarding_state (id, user_id, first_budget_at, created_at, updated_at)
VALUES ($1, $2, NOW(), NOW(), NOW())
ON CONFLICT (user_id) DO UPDATE
SET first_budget_at = COALESCE(onboarding_state.first_budget_at, EXCLUDED.first_budget_at), updated_at = NOW();

-- name: LatchFirstGoal :exec
INSERT INTO onboarding_state (id, user_id, first_goal_at, created_at, updated_at)
VALUES ($1, $2, NOW(), NOW(), NOW())
ON CONFLICT (user_id) DO UPDATE
SET first_goal_at = COALESCE(onboarding_state.first_goal_at, EXCLUDED.first_goal_at), updated_at = NOW();
