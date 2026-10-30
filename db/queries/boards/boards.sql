-- BoardsController / BoardsService / BoardNotifier (Community/*.swift).
-- Fluent's soft delete: `Board.query`, `BoardPost.find` and friends leave
-- out rows with deleted_at set unless the Swift called `.withDeleted()`.

-- name: BoardUsernames :many
SELECT id, username FROM users WHERE id = ANY(@ids::uuid[]);

-- name: BoardUserIDByUsername :many
-- Fluent's .first() without a sort.
SELECT id FROM users WHERE username = $1 LIMIT 1;

-- name: BoardGuidelinesAccept :exec
INSERT INTO community_guidelines_acceptances (id, user_id, accepted_at) VALUES ($1, $2, $3);

-- Blocks and the friends graph (SocialService).

-- name: BoardBlocksInvolving :many
SELECT blocker_id, blocked_id FROM social_blocks WHERE blocker_id = @viewer OR blocked_id = @viewer;

-- name: BoardSuspendedUserIDs :many
SELECT user_id FROM social_settings WHERE suspended_at IS NOT NULL;

-- name: BoardBlockExists :one
SELECT EXISTS (SELECT 1 FROM social_blocks WHERE blocker_id = $1 AND blocked_id = $2);

-- name: BoardInsertBlock :exec
INSERT INTO social_blocks (id, blocker_id, blocked_id, created_at) VALUES ($1, $2, $3, $4);

-- name: BoardDeleteBlock :exec
DELETE FROM social_blocks WHERE blocker_id = $1 AND blocked_id = $2;

-- name: BoardUnfriend :exec
DELETE FROM social_friendships
WHERE (user_id = @a AND friend_id = @b) OR (user_id = @b AND friend_id = @a);

-- name: BoardDeleteFriendRequests :exec
DELETE FROM social_friend_requests
WHERE (from_user_id = @a AND to_user_id = @b) OR (from_user_id = @b AND to_user_id = @a);

-- Boards.

-- name: BoardBySlug :many
SELECT * FROM boards WHERE slug = $1 AND deleted_at IS NULL LIMIT 1;

-- name: BoardBySlugWithDeleted :many
SELECT * FROM boards WHERE slug = $1 LIMIT 1;

-- name: BoardByID :many
SELECT * FROM boards WHERE id = $1 AND deleted_at IS NULL;

-- name: BoardsList :many
SELECT * FROM boards
WHERE deleted_at IS NULL
  AND (NOT @has_cursor::bool OR last_activity_at < @cursor_at OR (last_activity_at = @cursor_at AND id < @cursor_id))
ORDER BY last_activity_at DESC, id DESC
LIMIT $1;

-- name: BoardsCreatedSince :one
-- Deleted boards still count, or deleting would refill the quota.
SELECT count(*) FROM boards WHERE creator_id = $1 AND created_at > $2;

-- name: BoardInsert :exec
INSERT INTO boards (id, slug, name, description, creator_id, post_count, last_activity_at, created_at)
VALUES ($1, $2, $3, $4, $5, 0, $6, $7);

-- name: BoardUpdateDescription :exec
UPDATE boards SET description = $2 WHERE id = $1;

-- name: BoardRestore :exec
UPDATE boards SET deleted_at = NULL WHERE id = $1;

-- name: BoardBumpPost :exec
UPDATE boards SET post_count = post_count + 1, last_activity_at = $2 WHERE id = $1;

-- name: BoardDropPost :exec
UPDATE boards SET post_count = GREATEST(post_count - 1, 0) WHERE id = $1;

-- name: BoardTouch :exec
UPDATE boards SET last_activity_at = $2 WHERE id = $1;

-- Posts.

-- name: BoardPostByID :many
SELECT * FROM board_posts WHERE id = $1 AND deleted_at IS NULL;

-- name: BoardPostsByIDs :many
SELECT * FROM board_posts WHERE id = ANY(@ids::uuid[]) AND deleted_at IS NULL;

-- name: BoardPostInsert :exec
INSERT INTO board_posts (
    id, board_id, author_id, kind, title, url, domain, body, tags, score, comment_count,
    participant_count, view_count, last_activity_at, created_at, ai_generated, ai_topic, trading_date
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, @tags::text[], 0, 0, 1, 0, $9, $10, $11, $12, $13
);

-- name: BoardPostSoftDelete :exec
UPDATE board_posts SET deleted_at = $2 WHERE id = $1;

-- name: BoardLatestDailyPost :many
-- No tiebreak, as Swift sorted by trading_date only.
SELECT * FROM board_posts
WHERE board_id = $1 AND ai_generated = true AND deleted_at IS NULL
ORDER BY trading_date DESC
LIMIT 1;

-- name: BoardPostCommentBump :exec
UPDATE board_posts
SET comment_count = comment_count + 1,
    participant_count = participant_count + @participants::bigint,
    last_activity_at = @now
WHERE id = @id;

-- name: BoardPostViewCountBump :exec
UPDATE board_posts SET view_count = view_count + 1 WHERE id = $1;

-- Votes.

-- name: BoardVoteInsert :many
INSERT INTO board_votes (id, post_id, user_id, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (post_id, user_id) DO NOTHING
RETURNING id;

-- name: BoardVoteDelete :exec
DELETE FROM board_votes WHERE post_id = $1 AND user_id = $2;

-- name: BoardPostScoreMove :many
UPDATE board_posts SET score = GREATEST(score + @delta::bigint, 0)
WHERE id = @id RETURNING score;

-- name: BoardVotedPostIDs :many
SELECT post_id FROM board_votes WHERE user_id = @viewer AND post_id = ANY(@ids::uuid[]);

-- name: BoardHasVoted :one
SELECT EXISTS (SELECT 1 FROM board_votes WHERE post_id = $1 AND user_id = $2);

-- Views.

-- name: BoardPostViews :many
SELECT post_id, last_seen_comment_count FROM board_post_views
WHERE user_id = @viewer AND post_id = ANY(@ids::uuid[]);

-- name: BoardPostView :many
SELECT * FROM board_post_views WHERE post_id = $1 AND user_id = $2 LIMIT 1;

-- name: BoardPostViewUpdate :exec
UPDATE board_post_views SET last_seen_comment_count = $2, last_seen_at = $3 WHERE id = $1;

-- name: BoardPostViewInsert :many
INSERT INTO board_post_views (id, post_id, user_id, last_seen_comment_count, last_seen_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (post_id, user_id) DO NOTHING
RETURNING id;

-- name: BoardPostViewOwnComment :exec
-- Your own comment is not news to you.
UPDATE board_post_views SET last_seen_comment_count = last_seen_comment_count + 1
WHERE post_id = $1 AND user_id = $2;

-- Comments.

-- name: BoardCommentByID :many
SELECT * FROM board_comments WHERE id = $1 AND deleted_at IS NULL;

-- name: BoardCommentsByIDs :many
SELECT * FROM board_comments WHERE id = ANY(@ids::uuid[]) AND deleted_at IS NULL;

-- name: BoardCommentsForPost :many
-- withDeleted. The id tiebreak keeps comments made in the same instant in a
-- stable order (Vapor sorted by created_at only).
SELECT * FROM board_comments WHERE post_id = $1 ORDER BY created_at ASC, id ASC LIMIT $2;

-- name: BoardHasCommented :one
-- withDeleted.
SELECT EXISTS (SELECT 1 FROM board_comments WHERE post_id = $1 AND author_id = $2);

-- name: BoardCommentInsert :exec
INSERT INTO board_comments (id, post_id, parent_id, author_id, body, depth, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: BoardCommentSoftDelete :many
-- Returns the post id only when the comment was live, so a repeat delete
-- does not decrement twice.
UPDATE board_comments SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL
RETURNING post_id;

-- name: BoardPostCommentDrop :exec
-- A deleted comment leaves comment_count (Vapor never decremented it).
UPDATE board_posts SET comment_count = GREATEST(comment_count - 1, 0) WHERE id = $1;

-- Reports.

-- name: BoardReportInsert :exec
INSERT INTO social_reports (id, reporter_id, target_type, target_id, reason, note, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, 'open', $7);
