-- The activity feed (UserActivityService).

-- name: ListUserActivities :many
SELECT * FROM user_activities
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: UserActivityReferenceExists :one
SELECT EXISTS (
    SELECT 1 FROM user_activities
    WHERE user_id = $1 AND type = $2 AND reference_key = $3
);

-- name: InsertUserActivity :exec
-- A row with a reference key is recorded once (idx_user_activities_unique_reference).
INSERT INTO user_activities (
    id, user_id, type, title, subtitle, amount, is_growth, symbol, reference_key, created_at
) VALUES (
    @id, @user_id, @type, @title, @subtitle, @amount, @is_growth, @symbol, @reference_key, @created_at
)
ON CONFLICT (user_id, type, reference_key) WHERE reference_key IS NOT NULL DO NOTHING;
