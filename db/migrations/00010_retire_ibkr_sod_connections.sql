-- Retire the IBKR Web Service (SOD) feed.
--
-- The only IBKR connection the apps offered was "paste a Web Service token and
-- query ID", stored as a broker_connections row with external_id
-- 'sod-web-service'. Its sync fetched IBKR's vendor feed but wrote nothing,
-- yet the row being connected/error made the portfolio count as broker-synced
-- and blocked recording trades on it. The feed is gone (HTTP 410); statement
-- and screenshot imports replace it.
--
-- This disconnects the existing SOD rows and drops their sealed credentials.
-- It deliberately does NOT delete any stocks: holdings imported from a
-- statement or CSV share source_provider = 'ibkr' and must survive.
-- 'disconnected' rows are already outside ConnectedConnections and
-- BrokerConnectionsForList, so no query changes.
--
-- Goose-only (no Fluent counterpart): not listed in fluent_migrations.txt.
-- Idempotent: re-running matches the same rows; only updated_at changes.

-- +goose Up
-- +goose StatementBegin
UPDATE public.broker_connections
SET access_token = NULL,
    refresh_token = NULL,
    expires_at = NULL,
    status = 'disconnected',
    status_detail = 'Replaced by IBKR statement import.',
    updated_at = now()
WHERE provider = 'ibkr'
  AND external_id = 'sod-web-service';
-- +goose StatementEnd

-- +goose Down
-- No-op: the sealed token and query ID were discarded and cannot be restored.
SELECT 1;
