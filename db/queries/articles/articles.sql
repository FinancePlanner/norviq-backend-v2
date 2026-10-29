-- ArticlesController / ArticleService / ArticlePresenter.

-- name: ArticleByID :one
SELECT * FROM articles WHERE id = $1;

-- name: ArticleByCode :many
-- Fluent's .first() without a sort.
SELECT * FROM articles WHERE code = $1 LIMIT 1;

-- name: ListPublishedArticles :many
-- The feed: published, newest first, (published_at, id) keyset cursor.
SELECT * FROM articles
WHERE status = 'published'
  AND (sqlc.narg(ticker)::text IS NULL OR tickers @> ARRAY[sqlc.narg(ticker)::text])
  AND (sqlc.narg(author_id)::uuid IS NULL OR author_id = sqlc.narg(author_id)::uuid)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR published_at < sqlc.narg(cursor_at)::timestamptz
       OR (published_at = sqlc.narg(cursor_at)::timestamptz AND id < sqlc.narg(cursor_id)::uuid))
ORDER BY published_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: UserIDByUsername :many
-- Fluent's .first() without a sort.
SELECT id FROM users WHERE username = $1 LIMIT 1;

-- name: ArticleAuthors :many
SELECT id, username, avatar_url FROM users WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ArticleAuthor :many
SELECT id, username, avatar_url FROM users WHERE id = $1;

-- name: ArticleVoteExists :one
SELECT EXISTS (SELECT 1 FROM article_votes WHERE article_id = $1 AND user_id = $2);

-- name: CountArticlesSince :one
-- Every status counts toward the daily cap, deleted ones included.
SELECT count(*) FROM articles WHERE author_id = $1 AND published_at > $2;

-- name: InsertArticle :exec
INSERT INTO articles (
    id, code, author_id, slug, title, body_markdown, bullet_points, tickers, disclosure,
    cover_image_id, status, source, view_count, upvote_count, word_count, published_at,
    edited_at, created_at, updated_at
) VALUES (
    @id, @code, @author_id, @slug, @title, @body_markdown, @bullet_points, @tickers, @disclosure,
    @cover_image_id, @status, @source, 0, 0, @word_count, @published_at,
    NULL, @created_at, @created_at
);

-- name: UpdateArticleContent :exec
UPDATE articles SET
    title = @title, slug = @slug, body_markdown = @body_markdown, bullet_points = @bullet_points,
    tickers = @tickers, disclosure = @disclosure, cover_image_id = @cover_image_id,
    word_count = @word_count, edited_at = @edited_at, updated_at = @updated_at
WHERE id = @id;

-- name: SetArticleStatus :exec
UPDATE articles SET status = @status, updated_at = @updated_at WHERE id = @id;

-- name: InsertArticleView :many
INSERT INTO article_views (id, article_id, viewer_key, day)
VALUES ($1, $2, $3, $4)
ON CONFLICT (article_id, viewer_key, day) DO NOTHING
RETURNING id;

-- name: IncrementArticleViews :exec
UPDATE articles SET view_count = view_count + 1 WHERE id = $1;

-- name: InsertArticleVote :many
INSERT INTO article_votes (id, article_id, user_id, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (article_id, user_id) DO NOTHING
RETURNING id;

-- name: DeleteArticleVote :many
DELETE FROM article_votes WHERE article_id = $1 AND user_id = $2
RETURNING id;

-- name: BumpArticleUpvotes :many
UPDATE articles SET upvote_count = GREATEST(upvote_count + sqlc.arg(delta)::bigint, 0)
WHERE id = sqlc.arg(id) RETURNING upvote_count;

-- name: InsertArticleReport :exec
INSERT INTO social_reports (id, reporter_id, target_type, target_id, reason, note, status, created_at)
VALUES (@id, @reporter_id, 'article', @target_id, @reason, @note, 'open', @created_at);

-- name: CountImagesSince :one
SELECT count(*) FROM article_images WHERE owner_id = $1 AND created_at > $2;

-- name: InsertArticleImage :exec
INSERT INTO article_images (id, owner_id, content_type, bytes, width, height, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: SweepOrphanImages :exec
-- The owner's day-old uploads that no article (any status) uses.
DELETE FROM article_images AS i
WHERE i.owner_id = $1 AND i.created_at < $2
  AND NOT EXISTS (SELECT 1 FROM articles AS a WHERE a.cover_image_id = i.id);

-- name: ArticleImageByID :one
SELECT * FROM article_images WHERE id = $1;

-- name: ArticleImageOwner :many
SELECT owner_id FROM article_images WHERE id = $1;

-- name: CountPublishedCovers :one
SELECT count(*) FROM articles WHERE cover_image_id = $1 AND status = 'published';
