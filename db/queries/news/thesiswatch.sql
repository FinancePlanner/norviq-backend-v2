-- ThesisWatchService: story clustering over market_news_archive and the
-- per-user feed state.

-- name: UnclusteredArchiveSince :many
SELECT * FROM market_news_archive
WHERE published_at >= @since AND story_id IS NULL
ORDER BY published_at ASC;

-- name: GetThesisStoryByClusterKey :one
SELECT * FROM thesis_watch_stories WHERE cluster_key = @cluster_key LIMIT 1;

-- name: UpdateThesisStorySeen :exec
UPDATE thesis_watch_stories
SET last_seen_at = @last_seen_at, representative_news_id = @representative_news_id,
    event_type = @event_type, severity = @severity, updated_at = @now
WHERE id = @id;

-- name: InsertThesisStory :exec
INSERT INTO thesis_watch_stories (
    id, cluster_key, representative_news_id, event_type, severity, first_seen_at, last_seen_at, created_at, updated_at
) VALUES (@id, @cluster_key, @representative_news_id, @event_type, @severity, @first_seen_at, @last_seen_at, @now, @now);

-- name: SetArchiveStory :exec
UPDATE market_news_archive SET story_id = @story_id, updated_at = @now WHERE id = @id;

-- name: ThesisStoryExists :one
SELECT EXISTS (SELECT 1 FROM thesis_watch_stories WHERE id = @id);

-- name: ListThesisStories :many
SELECT * FROM thesis_watch_stories WHERE id = ANY(@ids::uuid[]);

-- name: ArchiveSince :many
SELECT * FROM market_news_archive
WHERE published_at >= @since
ORDER BY published_at DESC
LIMIT 500;

-- name: UserStocksForThesis :many
SELECT symbol, shares, buy_price FROM stocks WHERE user_id = @user_id;

-- name: ActiveWatchlistSymbols :many
SELECT symbol FROM watchlist_items WHERE user_id = @user_id AND status = 'active';

-- name: ProfileIndustries :many
SELECT symbol, finnhub_industry FROM profile_cache WHERE symbol = ANY(@symbols::text[]);

-- name: UserResearchNotes :many
SELECT * FROM research_notes WHERE user_id = @user_id;

-- name: ListThesisUserStates :many
SELECT * FROM thesis_watch_user_states WHERE user_id = @user_id AND story_id = ANY(@story_ids::uuid[]);

-- name: GetThesisUserState :one
SELECT * FROM thesis_watch_user_states WHERE user_id = @user_id AND story_id = @story_id LIMIT 1;

-- name: InsertThesisUserState :exec
INSERT INTO thesis_watch_user_states (
    id, user_id, story_id, symbol, impact, confidence, summary, why_it_matters,
    feedback, read_at, dismissed_at, created_at, updated_at
) VALUES (
    @id, @user_id, @story_id, @symbol, @impact, @confidence, @summary, @why_it_matters,
    @feedback, @read_at, @dismissed_at, @now, @now
);

-- name: UpdateThesisUserState :exec
UPDATE thesis_watch_user_states
SET impact = @impact, feedback = @feedback, read_at = @read_at, dismissed_at = @dismissed_at, updated_at = @now
WHERE id = @id;

-- name: GetThesisNotificationPreference :one
SELECT * FROM thesis_watch_notification_preferences WHERE user_id = @user_id LIMIT 1;

-- name: InsertThesisNotificationPreference :exec
INSERT INTO thesis_watch_notification_preferences (id, user_id, enabled, timezone, created_at, updated_at)
VALUES (@id, @user_id, @enabled, @timezone, @now, @now);

-- name: UpdateThesisNotificationPreference :exec
UPDATE thesis_watch_notification_preferences SET enabled = @enabled, timezone = @timezone, updated_at = @now WHERE id = @id;

-- name: EnabledThesisNotificationPreferences :many
SELECT * FROM thesis_watch_notification_preferences WHERE enabled = true;

-- name: AllStockSymbols :many
SELECT symbol FROM stocks;

-- name: AllActiveWatchlistSymbols :many
SELECT symbol FROM watchlist_items WHERE status = 'active';

-- name: CountThesisNotificationsSince :one
SELECT count(*) FROM notification_events
WHERE user_id = @user_id AND kind = 'thesis_watch' AND created_at >= @since;

-- name: NotificationEventExists :one
SELECT EXISTS (SELECT 1 FROM notification_events WHERE user_id = @user_id AND deduplication_key = @deduplication_key);
