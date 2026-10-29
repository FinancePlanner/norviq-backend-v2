-- Watchlists (watchlist_lists) and the pilot follows that manage some of
-- them (StockController+Watchlist, ListSupport.swift).

-- name: FollowedWatchlistListIDs :many
SELECT watchlist_list_id::uuid AS watchlist_list_id
FROM pilot_follows
WHERE user_id = $1 AND watchlist_list_id IS NOT NULL;

-- name: DefaultWatchlistList :one
SELECT * FROM watchlist_lists
WHERE user_id = $1 AND is_default = true
LIMIT 1;

-- name: SetWatchlistListDefault :exec
UPDATE watchlist_lists SET is_default = @is_default, updated_at = @now WHERE id = @id;

-- name: OldestWatchlistListExcluding :one
SELECT * FROM watchlist_lists
WHERE user_id = @user_id AND NOT (id = ANY(@excluded::uuid[]))
ORDER BY created_at ASC
LIMIT 1;

-- name: WatchlistListNames :many
SELECT name FROM watchlist_lists WHERE user_id = $1;

-- name: InsertWatchlistList :one
INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at, updated_at)
VALUES (@id, @user_id, @name, @is_default, @now, @now)
RETURNING *;

-- name: CountWatchlistLists :one
SELECT count(*) FROM watchlist_lists WHERE user_id = $1;

-- name: ListWatchlistLists :many
SELECT * FROM watchlist_lists
WHERE user_id = $1
ORDER BY is_default DESC, created_at ASC;

-- name: OwnedWatchlistList :one
SELECT * FROM watchlist_lists WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: RenameWatchlistList :one
UPDATE watchlist_lists SET name = @name, updated_at = @now WHERE id = @id RETURNING *;

-- name: DeleteWatchlistList :exec
DELETE FROM watchlist_lists WHERE id = $1;

-- name: CountOwnedWatchlistLists :one
-- `filter(\.$id ~~ ids).count()`: duplicates in ids match one row.
SELECT count(*) FROM watchlist_lists WHERE user_id = @user_id AND id = ANY(@ids::uuid[]);
