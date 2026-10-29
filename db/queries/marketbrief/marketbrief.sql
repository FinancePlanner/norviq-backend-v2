-- Market brief storage (DatabaseMarketBriefRepository) and push opt-ins
-- (DatabaseMarketBriefNotificationPreferenceService, MarketBriefNotifier).
-- Orderings are Fluent's, column for column; text columns sort under the
-- database collation exactly as Vapor's queries did.

-- name: CountMarketBriefSlot :one
SELECT count(*) FROM market_briefs
WHERE trading_date = $1 AND slot = $2;

-- name: InsertMarketBrief :exec
INSERT INTO market_briefs (id, trading_date, slot, language, payload, model, degraded, generated_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

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
