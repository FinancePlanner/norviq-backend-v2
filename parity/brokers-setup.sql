-- Fixtures for parity/brokers.jsonl. Idempotent; scratch database only
-- (never production). Both backends share it, so brokers.jsonl keeps to
-- reads, validation errors and writes that land the same way twice.
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/brokers-setup.sql
--
-- Users are b7e0c000-0000-4000-8000-00000000000N: 1 is Pro with a connected
-- IBKR link and a holding, 2 is free with nothing, 3 has an IBKR link in
-- error. Deleting the users cascades to their connections, lists and stocks.

BEGIN;

DELETE FROM stocks WHERE user_id::text LIKE 'b7e0c000-0000-4000-8000-%';
DELETE FROM broker_connections WHERE user_id::text LIKE 'b7e0c000-0000-4000-8000-%';
-- CSV commits into a list create its brokerage account (ON DELETE RESTRICT).
DELETE FROM accounts WHERE portfolio_id IN (SELECT id FROM portfolio_lists WHERE user_id::text LIKE 'b7e0c000-0000-4000-8000-%');
DELETE FROM portfolio_lists WHERE user_id::text LIKE 'b7e0c000-0000-4000-8000-%';
DELETE FROM entitlements WHERE user_id::text LIKE 'b7e0c000-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE 'b7e0c000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, email_verified_at, username) VALUES
  ('b7e0c000-0000-4000-8000-000000000001', 'parity-brokers-pro@example.com', 'x', now(), true, now(), 'paritybrokerspro'),
  ('b7e0c000-0000-4000-8000-000000000002', 'parity-brokers-free@example.com', 'x', now(), true, now(), 'paritybrokersfree'),
  ('b7e0c000-0000-4000-8000-000000000003', 'parity-brokers-error@example.com', 'x', now(), true, now(), 'paritybrokerserror');
INSERT INTO entitlements (id, user_id, level, created_at) VALUES
  ('b7e0c000-0000-4000-8000-0000000000e1', 'b7e0c000-0000-4000-8000-000000000001', 'pro', now());

INSERT INTO portfolio_lists (id, user_id, name, is_default, mode, created_at) VALUES
  ('b7e0c000-0000-4000-8000-0000000000c1', 'b7e0c000-0000-4000-8000-000000000001', 'Broker main', true, 'actual', now()),
  ('b7e0c000-0000-4000-8000-0000000000c2', 'b7e0c000-0000-4000-8000-000000000001', 'Sim', false, 'hypothetical', now()),
  ('b7e0c000-0000-4000-8000-0000000000c3', 'b7e0c000-0000-4000-8000-000000000002', 'Free main', true, 'actual', now());

INSERT INTO broker_connections (id, user_id, provider, external_id, status, created_at, updated_at, display_name, status_detail, connected_at, last_synced_at, portfolio_list_id) VALUES
  ('b7e0c000-0000-4000-8000-0000000000a1', 'b7e0c000-0000-4000-8000-000000000001', 'ibkr', 'U1234567', 'connected',
   '2026-10-01 09:00:00+00', '2026-10-02 10:11:12.345+00', 'IBKR U1234567', NULL,
   '2026-10-01 09:00:05.5+00', '2026-10-02 10:11:12.345+00', 'b7e0c000-0000-4000-8000-0000000000c1'),
  ('b7e0c000-0000-4000-8000-0000000000a3', 'b7e0c000-0000-4000-8000-000000000003', 'ibkr', NULL, 'error',
   '2026-10-03 08:00:00+00', '2026-10-03 08:00:00+00', NULL, 'IBKR session expired. Reconnect to continue syncing.',
   NULL, NULL, NULL);

INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, created_at) VALUES
  ('b7e0c000-0000-4000-8000-0000000000f1', 'b7e0c000-0000-4000-8000-000000000001', 'b7e0c000-0000-4000-8000-0000000000c1', 'KO', 10, 60.5, '2026-09-01', '2026-09-01 12:00:00+00'),
  -- KO already in the hypothetical list, so the CSV commit case updates it on
  -- both runs instead of inserting on the first and updating on the second.
  ('b7e0c000-0000-4000-8000-0000000000f2', 'b7e0c000-0000-4000-8000-000000000001', 'b7e0c000-0000-4000-8000-0000000000c2', 'KO', 2, 50, '2026-09-01', '2026-09-01 12:00:00+00');

COMMIT;
