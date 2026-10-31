-- NewsController / DatabaseNewsRepository: the user's saved news library.

-- name: GetNewsItem :one
SELECT * FROM news_items WHERE id = @id AND user_id = @user_id LIMIT 1;

-- name: InsertNewsItem :exec
INSERT INTO news_items (id, user_id, symbol, headline, source, url, summary, published_at, created_at, updated_at)
VALUES (@id, @user_id, @symbol, @headline, @source, @url, @summary, @published_at, @created_at, @updated_at);

-- name: UpdateNewsItem :exec
UPDATE news_items
SET symbol = @symbol, headline = @headline, source = @source, url = @url, summary = @summary,
    published_at = @published_at, updated_at = @updated_at
WHERE id = @id;

-- name: DeleteNewsItem :exec
DELETE FROM news_items WHERE id = @id;

-- name: ListNewsFeed :many
-- listFeed: the user's items for any tracked symbol, newest first.
SELECT * FROM news_items
WHERE user_id = @user_id AND symbol = ANY(@symbols::text[])
ORDER BY published_at DESC, created_at DESC
LIMIT @lim;

-- name: FindPotentialNewsDuplicates :many
SELECT * FROM news_items
WHERE user_id = @user_id AND symbol = @symbol
  AND (headline = @headline OR (sqlc.narg(url)::text IS NOT NULL AND url = sqlc.narg(url)::text))
ORDER BY published_at DESC
LIMIT 10;

-- name: TrackedStockSymbols :many
SELECT symbol FROM stocks WHERE user_id = @user_id;

-- name: TrackedWatchlistSymbols :many
SELECT DISTINCT symbol FROM watchlist_items WHERE user_id = @user_id AND status <> 'archived';

-- name: StockOwnersForSymbols :many
SELECT symbol, user_id FROM stocks WHERE symbol = ANY(@symbols::text[]);

-- name: WatchlistOwnersForSymbols :many
-- One row per (symbol, user): a symbol in two of a user's lists must not
-- notify them twice.
SELECT DISTINCT symbol, user_id FROM watchlist_items
WHERE status <> 'archived' AND symbol = ANY(@symbols::text[]);

-- name: GetNewsTickerPreference :one
SELECT * FROM news_ticker_preferences WHERE user_id = @user_id LIMIT 1;

-- name: InsertNewsTickerPreference :exec
INSERT INTO news_ticker_preferences (id, user_id, enabled, created_at, updated_at)
VALUES (@id, @user_id, @enabled, @now, @now);

-- name: UpdateNewsTickerPreference :exec
UPDATE news_ticker_preferences SET enabled = @enabled, updated_at = @now WHERE id = @id;

-- name: ListNewsTickerFeeds :many
SELECT * FROM news_ticker_feed_subscriptions WHERE user_id = @user_id ORDER BY created_at ASC;

-- name: CountNewsTickerFeeds :one
SELECT count(*) FROM news_ticker_feed_subscriptions WHERE user_id = @user_id;

-- name: NewsTickerFeedExists :one
SELECT EXISTS (SELECT 1 FROM news_ticker_feed_subscriptions WHERE user_id = @user_id AND feed_url = @feed_url);

-- name: InsertNewsTickerFeed :exec
INSERT INTO news_ticker_feed_subscriptions (id, user_id, feed_url, title, created_at)
VALUES (@id, @user_id, @feed_url, @title, @created_at);

-- name: DeleteNewsTickerFeed :execrows
DELETE FROM news_ticker_feed_subscriptions WHERE id = @id AND user_id = @user_id;

-- name: ListNewsItems :many
-- list: one page of the user's library, newest first, keyset-paged.
SELECT * FROM news_items
WHERE user_id = @user_id
  AND (sqlc.narg(symbol)::text IS NULL OR symbol = sqlc.narg(symbol)::text)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR published_at < sqlc.narg(cursor_at)::timestamptz
    OR (sqlc.narg(cursor_id)::uuid IS NOT NULL
        AND published_at = sqlc.narg(cursor_at)::timestamptz
        AND id < sqlc.narg(cursor_id)::uuid)
  )
ORDER BY published_at DESC, id DESC
LIMIT @lim;
