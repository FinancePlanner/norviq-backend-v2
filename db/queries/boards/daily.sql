-- DailyBoardPost.swift: the Daily board's account, board, runs and history.

-- name: DailyUserByUsername :many
SELECT id, email FROM users WHERE username = $1 LIMIT 1;

-- name: DailyInsertSystemUser :exec
-- User(email:passwordHash:username:isVerified: false, hadTrial: true).
INSERT INTO users (id, email, password_hash, username, failed_login_attempts, is_verified, had_trial, created_at, updated_at)
VALUES ($1, $2, $3, $4, 0, false, true, $5, $5);

-- name: DailyRunByDate :many
SELECT * FROM daily_board_post_runs WHERE trading_date = $1;

-- name: DailyInsertRun :exec
INSERT INTO daily_board_post_runs (trading_date, status, post_id, topic, created_at)
VALUES ($1, $2, $3, $4, $5);

-- name: DailyDeleteRun :exec
DELETE FROM daily_board_post_runs WHERE trading_date = $1;

-- name: DailyPostsForDate :many
SELECT * FROM board_posts WHERE ai_generated = true AND trading_date = $1 AND deleted_at IS NULL;

-- name: DailyHistory :many
-- The last 60 daily posts before a date, newest first.
SELECT trading_date, ai_topic, tags FROM board_posts
WHERE ai_generated = true AND trading_date < $1 AND deleted_at IS NULL
ORDER BY trading_date DESC
LIMIT 60;

-- name: DailyEveningBrief :many
-- MarketBriefRepository.find(tradingDate:slot: .evening, language: "en"),
-- which DailyBoardPostJob.waitingForBrief reads.
SELECT payload FROM market_briefs
WHERE trading_date = $1 AND slot = 'evening' AND language = 'en'
LIMIT 1;
