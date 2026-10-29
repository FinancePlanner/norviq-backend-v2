-- Pilot follows, their events and snapshots (PilotController,
-- PilotFollowService, PilotMirrorService, PilotMirrorJob).

-- name: ListFollowsByUser :many
SELECT * FROM pilot_follows WHERE user_id = $1 ORDER BY created_at DESC;

-- name: FollowByIDForUser :one
SELECT * FROM pilot_follows WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: FollowByID :one
SELECT * FROM pilot_follows WHERE id = $1;

-- name: ListActiveFollows :many
SELECT * FROM pilot_follows WHERE status = 'active';

-- name: CountFollowsByUser :one
SELECT count(*) FROM pilot_follows WHERE user_id = $1;

-- name: CountFollowsForTarget :one
-- ensureNotFollowing: the target filters apply only when given.
SELECT count(*) FROM pilot_follows
WHERE user_id = @user_id AND pilot_id = @pilot_id
  AND (sqlc.narg(portfolio_list_id)::uuid IS NULL OR portfolio_list_id = sqlc.narg(portfolio_list_id)::uuid)
  AND (sqlc.narg(watchlist_list_id)::uuid IS NULL OR watchlist_list_id = sqlc.narg(watchlist_list_id)::uuid);

-- name: CountFollowsByPortfolio :one
SELECT count(*) FROM pilot_follows WHERE portfolio_list_id = $1;

-- name: CountFollowsByWatchlist :one
SELECT count(*) FROM pilot_follows WHERE watchlist_list_id = $1;

-- name: InsertFollow :one
INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, portfolio_list_id, watchlist_list_id,
                           starting_capital, currency, applied_version, status, created_at, updated_at)
VALUES (@id, @user_id, @pilot_id, @target_kind, @portfolio_list_id, @watchlist_list_id,
        @starting_capital, @currency, 0, 'active', @created_at, @created_at)
RETURNING *;

-- name: UpdateFollowStatus :one
-- Fluent's save writes nothing (not even updated_at) when nothing changed.
UPDATE pilot_follows SET status = @status, updated_at = now()
WHERE id = @id AND status <> @status
RETURNING *;

-- name: DeleteFollow :exec
DELETE FROM pilot_follows WHERE id = $1;

-- name: ClaimFollowVersion :many
-- Moves applied_version forward; no row when another run already applied
-- this version or a later one. The idempotency guard between pods.
UPDATE pilot_follows SET applied_version = @version, updated_at = NOW()
WHERE id = @id AND applied_version < @version
RETURNING id;

-- name: ListFollowEvents :many
SELECT * FROM pilot_follow_events
WHERE follow_id = $1
ORDER BY book_version DESC, symbol
LIMIT 500;

-- name: InsertFollowEvent :exec
INSERT INTO pilot_follow_events (id, follow_id, book_version, kind, symbol, quantity, price, priced_at, note, created_at)
VALUES (@id, @follow_id, @book_version, @kind, @symbol, @quantity, @price, @priced_at, @note, @created_at);

-- name: ListFollowSnapshots :many
SELECT * FROM pilot_follow_snapshots WHERE follow_id = $1 ORDER BY captured_on;

-- name: FollowSnapshotExists :one
SELECT EXISTS (SELECT 1 FROM pilot_follow_snapshots WHERE follow_id = $1 AND captured_on = $2);

-- name: InsertFollowSnapshot :exec
INSERT INTO pilot_follow_snapshots (id, follow_id, captured_on, value, cash)
VALUES (@id, @follow_id, @captured_on, @value, @cash);
