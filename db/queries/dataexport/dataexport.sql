-- DataExportController, ExportFileController, ExportService,
-- DataExportCleanupJob, and the export-file sweep of account deletion.

-- name: InsertExport :exec
INSERT INTO data_exports (id, user_id, type, format, filters, status, file_path, file_size_bytes, expires_at, created_at, updated_at)
VALUES (@id, @user_id, @type, @format, @filters, @status, NULL, NULL, NULL, @created_at, @created_at);

-- name: ExportForUser :many
SELECT * FROM data_exports WHERE id = @id AND user_id = @user_id LIMIT 1;

-- name: ListExports :many
SELECT * FROM data_exports WHERE user_id = @user_id
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit)::bigint OFFSET sqlc.arg(row_offset)::bigint;

-- name: MarkExportReady :exec
UPDATE data_exports
SET status = 'ready', file_path = @file_path, file_size_bytes = @file_size_bytes,
    expires_at = @expires_at, updated_at = @updated_at
WHERE id = @id AND status = 'pending';

-- name: MarkExportFailed :exec
UPDATE data_exports SET status = 'failed', updated_at = @updated_at WHERE id = @id;

-- name: ExpiredReadyExports :many
SELECT id, file_path FROM data_exports
WHERE status = 'ready' AND expires_at IS NOT NULL AND expires_at < @before;

-- name: DeleteExport :exec
DELETE FROM data_exports WHERE id = @id;

-- name: ExportFilesForUser :many
SELECT id, file_path FROM data_exports WHERE user_id = @user_id;

-- name: StocksForExport :many
SELECT id, symbol, shares, buy_price, buy_date, notes, category::text AS category, portfolio_list_id, created_at
FROM stocks
WHERE user_id = @user_id
  AND (sqlc.narg(portfolio_list_id)::uuid IS NULL OR portfolio_list_id = sqlc.narg(portfolio_list_id)::uuid);

-- name: ExportAccountIDs :many
SELECT id FROM accounts WHERE user_id = @user_id AND broker <> 'pilot';

-- name: TransactionsForExport :many
SELECT id, account_id, instrument_id, external_id, type, quantity, price, currency, trade_date, settle_date, fees
FROM transactions
WHERE account_id = ANY(@account_ids::uuid[])
  AND (sqlc.narg(start_date)::timestamptz IS NULL OR trade_date >= sqlc.narg(start_date)::timestamptz)
  AND (sqlc.narg(end_date)::timestamptz IS NULL OR trade_date <= sqlc.narg(end_date)::timestamptz);

-- name: WatchlistItemsForExport :many
SELECT id, symbol, note, status, watchlist_list_id, last_reviewed_at, next_review_at, created_at
FROM watchlist_items WHERE user_id = @user_id;

-- name: ResearchNotesForExport :many
SELECT id, symbol, title, thesis, risks, catalysts, reference_links, created_at
FROM research_notes WHERE user_id = @user_id;
