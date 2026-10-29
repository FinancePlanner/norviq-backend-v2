-- Watchlist items (watchlist_items).

-- name: ListWatchlistItems :many
SELECT * FROM watchlist_items
WHERE user_id = $1 AND watchlist_list_id = $2
ORDER BY updated_at DESC, created_at DESC;

-- name: WatchlistItemsInList :many
SELECT * FROM watchlist_items
WHERE user_id = $1 AND watchlist_list_id = $2;

-- name: WatchlistItemBySymbol :one
SELECT * FROM watchlist_items
WHERE user_id = $1 AND watchlist_list_id = $2 AND symbol = $3
LIMIT 1;

-- name: DuplicateWatchlistItem :one
SELECT * FROM watchlist_items
WHERE user_id = @user_id AND watchlist_list_id = @watchlist_list_id AND symbol = @symbol AND id <> @id
LIMIT 1;

-- name: OwnedWatchlistItem :one
SELECT * FROM watchlist_items WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: CountWatchlistItems :one
SELECT count(*) FROM watchlist_items WHERE user_id = $1;

-- name: InsertWatchlistItem :one
INSERT INTO watchlist_items (
    id, user_id, watchlist_list_id, symbol, note, status, last_reviewed_at, next_review_at, created_at, updated_at
) VALUES (
    @id, @user_id, @watchlist_list_id, @symbol, @note, @status, @last_reviewed_at, @next_review_at, @now, @now
)
RETURNING *;

-- name: UpdateWatchlistItem :one
UPDATE watchlist_items SET
    watchlist_list_id = @watchlist_list_id,
    note = @note,
    status = @status,
    last_reviewed_at = @last_reviewed_at,
    next_review_at = @next_review_at,
    updated_at = @now
WHERE id = @id
RETURNING *;

-- name: DeleteWatchlistItem :exec
DELETE FROM watchlist_items WHERE id = $1;

-- name: ActiveWatchlistUserIDsForSymbol :many
SELECT user_id FROM watchlist_items WHERE symbol = $1 AND status = 'active';

-- name: HoldingUserIDsForSymbol :many
SELECT user_id FROM stocks WHERE symbol = $1;

-- name: WatchlistSymbolsInLists :many
SELECT symbol FROM watchlist_items WHERE user_id = @user_id AND watchlist_list_id = ANY(@list_ids::uuid[]);
