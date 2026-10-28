-- name: InsertFeedback :exec
INSERT INTO feedbacks (id, topic, message, user_id, created_at)
VALUES (@id, @topic, @message, @user_id, @created_at);
