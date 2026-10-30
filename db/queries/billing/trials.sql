-- Trial lifecycle (TrialService.swift) and tax filing packs
-- (TaxPackEntitlementService.swift).

-- name: SetUserTrial :exec
UPDATE users
SET trial_started_at = $2, trial_days = $3, trial_tier = $4, updated_at = $5
WHERE id = $1;

-- name: ExpiredTrialCandidates :many
-- Up to 500 users with trial fields set, earliest trial end first, expired
-- or not; expiry is decided in Go. Vapor had no ORDER BY, so with more than
-- 500 running trials an expired one could be skipped tick after tick. Swept
-- users lose their trial fields, so the ordering always reaches the rest.
SELECT id, trial_started_at, trial_days
FROM users
WHERE trial_tier IS NOT NULL
  AND trial_started_at IS NOT NULL
  AND trial_days IS NOT NULL
ORDER BY trial_started_at + trial_days * interval '1 day', id
LIMIT 500;

-- name: ClearUserTrial :exec
UPDATE users
SET trial_tier = NULL, trial_started_at = NULL, trial_days = NULL, had_trial = true, updated_at = $2
WHERE id = $1;

-- name: SetTrialWarningSentAt :exec
UPDATE users SET trial_warning_sent_at = $2, updated_at = $2 WHERE id = $1;

-- name: TrialWarningExists :one
SELECT EXISTS (
    SELECT 1 FROM trial_warnings WHERE user_id = $1 AND warning_type = $2
);

-- name: InsertTrialWarning :exec
INSERT INTO trial_warnings (id, user_id, warning_type, sent_at, created_at)
VALUES ($1, $2, $3, $4, $5);

-- name: TaxPackByUserYear :one
SELECT *
FROM tax_pack_entitlements
WHERE user_id = $1 AND tax_year = $2
LIMIT 1;

-- name: InsertTaxPack :exec
INSERT INTO tax_pack_entitlements (id, user_id, tax_year, jurisdiction, source, product_id, provider_event_id, granted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: UpdateTaxPack :exec
UPDATE tax_pack_entitlements
SET source = $2, product_id = $3, provider_event_id = $4, jurisdiction = $5
WHERE id = $1;

-- name: DeleteTaxPack :exec
DELETE FROM tax_pack_entitlements WHERE user_id = $1 AND tax_year = $2;

-- name: CountTaxPacks :one
SELECT count(*) FROM tax_pack_entitlements WHERE user_id = $1 AND tax_year = $2;
