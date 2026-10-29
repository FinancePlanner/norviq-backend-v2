-- ExpenseImportSessionStore and ExpenseImportRetentionJob.

-- name: ListOpenSessions :many
SELECT id FROM expense_import_sessions
WHERE user_id = @user_id AND status = 'ready'
ORDER BY created_at DESC;

-- name: DeleteSession :exec
DELETE FROM expense_import_sessions WHERE id = @id;

-- name: InsertSession :exec
INSERT INTO expense_import_sessions (
    id, user_id, status, file_name, sheet_count, row_count, ai_available,
    ai_model, ai_confidence, sheets_encrypted, mapping_encrypted, expires_at,
    created_at, updated_at, committed_at
) VALUES (
    @id, @user_id, 'ready', @file_name, @sheet_count, @row_count, @ai_available,
    @ai_model, @ai_confidence, @sheets_encrypted, @mapping_encrypted, @expires_at,
    @now, @now, NULL
);

-- name: GetUserSession :one
SELECT * FROM expense_import_sessions
WHERE id = @id AND user_id = @user_id
LIMIT 1;

-- name: UpdateSessionMapping :exec
UPDATE expense_import_sessions
SET mapping_encrypted = @mapping_encrypted, updated_at = @now
WHERE id = @id;

-- name: MarkSessionCommitted :exec
UPDATE expense_import_sessions
SET status = 'committed', committed_at = @now, updated_at = @now
WHERE id = @id;

-- name: DiscardUserSession :exec
DELETE FROM expense_import_sessions WHERE id = @id AND user_id = @user_id;

-- name: DeleteExpiredSessions :exec
DELETE FROM expense_import_sessions WHERE expires_at < @now;

-- name: DeleteFinishedSessions :exec
DELETE FROM expense_import_sessions
WHERE status <> 'ready' AND updated_at < @cutoff;
