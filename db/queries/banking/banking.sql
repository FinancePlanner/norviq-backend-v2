-- BankController, BankCallbackController, PlaidWebhookController, the bank
-- providers (Plaid, GoCardless, Enable Banking) and BankSyncJob.
-- Fluent's `first()` is LIMIT 1 with no ORDER BY; `all()` has no ORDER BY.

-- name: InsertConnection :exec
INSERT INTO bank_connections (
    id, user_id, provider, institution_id, institution_name, provider_item_id,
    access_token_enc, sync_cursor, status, consent_expires_at, last_synced_at,
    last_sync_status, last_sync_error, created_at, updated_at
) VALUES (
    @id, @user_id, @provider, @institution_id, @institution_name, @provider_item_id,
    @access_token_enc, NULL, @status, @consent_expires_at, NULL,
    NULL, NULL, @now::timestamptz, @now::timestamptz
);

-- name: ListUserConnections :many
SELECT * FROM bank_connections WHERE user_id = @user_id;

-- name: GetUserConnection :one
SELECT * FROM bank_connections WHERE id = @id AND user_id = @user_id LIMIT 1;

-- name: ListActiveConnections :many
SELECT * FROM bank_connections WHERE status = 'active';

-- name: GetConnectionByItem :one
SELECT * FROM bank_connections
WHERE provider = @provider AND provider_item_id = @provider_item_id
LIMIT 1;

-- name: ListUserProviderConnections :many
SELECT * FROM bank_connections WHERE user_id = @user_id AND provider = @provider;

-- name: DeleteConnection :exec
DELETE FROM bank_connections WHERE id = @id;

-- name: UpdateConnectionPlaidSynced :exec
UPDATE bank_connections
SET sync_cursor = @sync_cursor, last_synced_at = @now::timestamptz, last_sync_status = 'ok',
    last_sync_error = NULL, status = 'active', updated_at = @now::timestamptz
WHERE id = @id;

-- name: UpdateConnectionSynced :exec
UPDATE bank_connections
SET last_synced_at = @now::timestamptz, last_sync_status = 'ok', last_sync_error = NULL, updated_at = @now::timestamptz
WHERE id = @id;

-- name: UpdateConnectionStatus :exec
UPDATE bank_connections
SET status = @status, last_sync_status = @last_sync_status, updated_at = @now::timestamptz
WHERE id = @id;

-- name: UpdateConnectionStatusError :exec
UPDATE bank_connections
SET status = @status, last_sync_status = @last_sync_status, last_sync_error = @last_sync_error,
    updated_at = @now::timestamptz
WHERE id = @id;

-- name: UpdateConnectionSyncError :exec
UPDATE bank_connections
SET last_sync_status = @last_sync_status, last_sync_error = @last_sync_error, updated_at = @now::timestamptz
WHERE id = @id;

-- name: UpdateConnectionReconsent :exec
UPDATE bank_connections
SET provider_item_id = @provider_item_id, status = 'active',
    consent_expires_at = @consent_expires_at, last_sync_status = NULL,
    last_sync_error = NULL, updated_at = @now::timestamptz
WHERE id = @id;

-- name: ListConnectionAccounts :many
SELECT * FROM bank_accounts WHERE connection_id = @connection_id;

-- name: GetAccountByProviderID :one
SELECT * FROM bank_accounts
WHERE connection_id = @connection_id AND provider_account_id = @provider_account_id
LIMIT 1;

-- name: FindAccountByHashes :one
SELECT * FROM bank_accounts
WHERE connection_id = ANY(@connection_ids::uuid[])
  AND identification_hash = ANY(@hashes::text[])
LIMIT 1;

-- name: InsertAccount :exec
INSERT INTO bank_accounts (
    id, connection_id, provider_account_id, name, mask, currency, type, balance,
    balance_as_of, identification_hash, created_at, updated_at
) VALUES (
    @id, @connection_id, @provider_account_id, @name, @mask, @currency, @type, @balance,
    @balance_as_of, @identification_hash, @now::timestamptz, @now::timestamptz
);

-- name: UpdateAccount :exec
UPDATE bank_accounts
SET provider_account_id = @provider_account_id, name = @name, mask = @mask,
    currency = @currency, type = @type, balance = @balance, balance_as_of = @balance_as_of,
    identification_hash = @identification_hash, updated_at = @now::timestamptz
WHERE id = @id;

-- name: ListUserTransactionsByStatus :many
SELECT * FROM bank_transactions
WHERE user_id = @user_id AND status = @status
ORDER BY occurred_on DESC;

-- name: GetUserTransaction :one
SELECT * FROM bank_transactions WHERE id = @id AND user_id = @user_id LIMIT 1;

-- name: UpdateTransactionStatus :exec
UPDATE bank_transactions SET status = @status, updated_at = @now::timestamptz WHERE id = @id;

-- name: MarkTransactionImported :exec
UPDATE bank_transactions
SET status = 'imported', expense_id = @expense_id, updated_at = @now::timestamptz
WHERE id = @id;

-- name: GetAccountTransaction :one
SELECT * FROM bank_transactions
WHERE account_id = @account_id AND provider_tx_id = @provider_tx_id
LIMIT 1;

-- name: GetUserTransactionByProviderTx :one
SELECT * FROM bank_transactions
WHERE provider_tx_id = @provider_tx_id AND user_id = @user_id
LIMIT 1;

-- name: GetPendingAccountTransaction :one
SELECT * FROM bank_transactions
WHERE account_id = @account_id AND provider_tx_id = @provider_tx_id AND pending = true
LIMIT 1;

-- name: ListPendingByHash :many
SELECT * FROM bank_transactions
WHERE account_id = @account_id AND dedupe_hash = @dedupe_hash AND pending = true;

-- name: DeleteTransaction :exec
DELETE FROM bank_transactions WHERE id = @id;

-- name: DeletePendingSuggestions :execrows
DELETE FROM bank_transactions
WHERE account_id = @account_id AND pending = true AND status = 'suggested'
  AND (sqlc.narg('since')::timestamptz IS NULL OR occurred_on >= sqlc.narg('since')::timestamptz);

-- name: InsertTransaction :exec
INSERT INTO bank_transactions (
    id, account_id, user_id, provider_tx_id, dedupe_hash, amount, currency, occurred_on,
    merchant, description_text, pending, status, provider_category, expense_id,
    created_at, updated_at
) VALUES (
    @id, @account_id, @user_id, @provider_tx_id, @dedupe_hash, @amount, @currency, @occurred_on,
    @merchant, @description_text, @pending, 'suggested', @provider_category, NULL,
    @now::timestamptz, @now::timestamptz
);

-- name: UpdateTransactionFields :exec
UPDATE bank_transactions
SET provider_tx_id = @provider_tx_id, amount = @amount, currency = @currency,
    occurred_on = @occurred_on, merchant = @merchant, description_text = @description_text,
    pending = @pending, dedupe_hash = @dedupe_hash, provider_category = @provider_category,
    updated_at = @now::timestamptz
WHERE id = @id;

-- name: ListExpenseDaysInWindow :many
-- BankTransactionImportService.markDuplicates: the date column compared
-- against Fluent's timestamptz binds.
SELECT occurred_on, amount FROM expenses
WHERE user_id = @user_id
  AND occurred_on >= @window_start::timestamptz
  AND occurred_on <= @window_end::timestamptz;

-- name: InsertLinkFlow :exec
INSERT INTO bank_link_flows (
    id, user_id, provider, reference, requisition_id, institution_id, app_redirect_uri,
    expires_at, used_at, created_at
) VALUES (
    @id, @user_id, @provider, @reference, @requisition_id, @institution_id, @app_redirect_uri,
    @expires_at, NULL, @now::timestamptz
);

-- name: GetLinkFlow :one
SELECT * FROM bank_link_flows
WHERE reference = @reference AND provider = @provider
LIMIT 1;

-- name: MarkLinkFlowUsed :exec
UPDATE bank_link_flows SET used_at = @used_at::timestamptz WHERE id = @id;

-- name: UpdateLinkFlowSession :exec
UPDATE bank_link_flows SET requisition_id = @requisition_id, used_at = @used_at::timestamptz WHERE id = @id;
