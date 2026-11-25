-- Market brief storage (DatabaseMarketBriefRepository) and push opt-ins
-- (DatabaseMarketBriefNotificationPreferenceService, MarketBriefNotifier).
-- Orderings are Fluent's, column for column; text columns sort under the
-- database collation exactly as Vapor's queries did.

-- name: CountMarketBriefSlot :one
SELECT count(*) FROM market_briefs
WHERE trading_date = $1 AND slot = $2;

-- name: InsertMarketBrief :exec
INSERT INTO market_briefs (id, trading_date, slot, language, payload, model, degraded, generated_at, created_at, revision, content_hash, edition)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ArchiveMarketBriefSlot :exec
-- Copies a slot's rows into the revision history before a regeneration
-- deletes them.
INSERT INTO market_brief_revisions (id, trading_date, slot, language, revision, payload, edition, model, degraded, content_hash, generated_at, archived_at)
SELECT b.id, b.trading_date, b.slot, b.language, b.revision, b.payload, b.edition, b.model, b.degraded, b.content_hash, b.generated_at, sqlc.arg(archived_at)::timestamptz
FROM market_briefs b
WHERE b.trading_date = sqlc.arg(trading_date) AND b.slot = sqlc.arg(slot);

-- name: NextMarketBriefRevision :one
-- 1 for a slot never regenerated, else one past the newest archived revision.
SELECT (COALESCE(MAX(revision), 0) + 1)::integer AS next_revision FROM market_brief_revisions
WHERE trading_date = $1 AND slot = $2;

-- name: MarketBriefEdition :one
-- The edition is language-neutral; every language row of a slot holds the same one.
SELECT revision, edition FROM market_briefs
WHERE trading_date = $1 AND slot = $2
ORDER BY language ASC
LIMIT 1;

-- name: MarketBriefSnapshot :one
-- One statement, so payload, edition, revision and hash always belong to the
-- same revision of the slot. content_hash is NULL on a legacy row.
SELECT payload, edition, revision, content_hash FROM market_briefs
WHERE trading_date = $1 AND slot = $2 AND language = $3
LIMIT 1;

-- name: DeleteMarketBriefSlot :exec
DELETE FROM market_briefs
WHERE trading_date = $1 AND slot = $2;

-- name: LatestMarketBrief :one
SELECT * FROM market_briefs
WHERE language = $1
ORDER BY trading_date DESC, slot ASC, generated_at DESC
LIMIT 1;

-- name: FindMarketBrief :one
SELECT * FROM market_briefs
WHERE trading_date = $1 AND slot = $2 AND language = $3
LIMIT 1;

-- name: MarketBriefOnDay :one
SELECT slot, trading_date FROM market_briefs
WHERE language = $1 AND trading_date = $2 AND slot = $3
LIMIT 1;

-- name: MarketBriefBeforeDay :one
SELECT slot, trading_date FROM market_briefs
WHERE language = $1 AND trading_date < $2
ORDER BY trading_date DESC, slot ASC
LIMIT 1;

-- name: GetMarketBriefNotificationPreference :one
SELECT * FROM market_brief_notification_preferences
WHERE user_id = $1
LIMIT 1;

-- name: InsertMarketBriefNotificationPreference :exec
INSERT INTO market_brief_notification_preferences (id, user_id, enabled, language, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: UpdateMarketBriefNotificationPreference :exec
UPDATE market_brief_notification_preferences
SET enabled = $2, language = $3, updated_at = $4
WHERE id = $1;

-- name: EnabledMarketBriefNotificationPreferences :many
SELECT * FROM market_brief_notification_preferences
WHERE enabled = true;
