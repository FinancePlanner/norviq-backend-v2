-- Fixtures for parity/banking.jsonl. Idempotent: run before every parity
-- pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/banking-setup.sql
--
-- Neither backend has a bank provider or Telegram bot configured, so the
-- cases cover reads, validation, ownership, premium gating and the
-- "not configured" paths. Provider round trips are covered by Go tests
-- against stub servers.
--
-- Users are b4b4b4b4-0000-4000-8000-00000000000N:
--   1 free, 2 pro with connections, transactions and a manual expense,
--   3 pro with nothing, 4 pro used by mutating cases.
BEGIN;

DROP TRIGGER IF EXISTS parity_bank_tx_sticky ON bank_transactions;
DROP TRIGGER IF EXISTS parity_bank_conn_keep ON bank_connections;
DROP FUNCTION IF EXISTS parity_bank_tx_sticky();
DROP FUNCTION IF EXISTS parity_bank_conn_keep();

DELETE FROM bank_transactions WHERE user_id::text LIKE 'b4b4b4b4-0000-4000-8000-%';
DELETE FROM bank_accounts WHERE connection_id IN (SELECT id FROM bank_connections WHERE user_id::text LIKE 'b4b4b4b4-0000-4000-8000-%');
DELETE FROM bank_connections WHERE user_id::text LIKE 'b4b4b4b4-0000-4000-8000-%';
DELETE FROM bank_link_flows WHERE user_id::text LIKE 'b4b4b4b4-0000-4000-8000-%';
DELETE FROM messaging_preferences WHERE user_id::text LIKE 'b4b4b4b4-0000-4000-8000-%';
DELETE FROM messaging_link_codes WHERE user_id::text LIKE 'b4b4b4b4-0000-4000-8000-%';
DELETE FROM expenses WHERE user_id::text LIKE 'b4b4b4b4-0000-4000-8000-%';
DELETE FROM entitlements WHERE user_id::text LIKE 'b4b4b4b4-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE 'b4b4b4b4-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('b4b4b4b4-0000-4000-8000-000000000001', 'parity-banking-free@example.com', 'x', now(), true),
  ('b4b4b4b4-0000-4000-8000-000000000002', 'parity-banking-pro@example.com', 'x', now(), true),
  ('b4b4b4b4-0000-4000-8000-000000000003', 'parity-banking-empty@example.com', 'x', now(), true),
  ('b4b4b4b4-0000-4000-8000-000000000004', 'parity-banking-writer@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES
  (gen_random_uuid(), 'b4b4b4b4-0000-4000-8000-000000000002', 'pro', NULL, now(), now()),
  (gen_random_uuid(), 'b4b4b4b4-0000-4000-8000-000000000003', 'pro', NULL, now(), now()),
  (gen_random_uuid(), 'b4b4b4b4-0000-4000-8000-000000000004', 'pro', NULL, now(), now());

-- User 2: an Enable Banking connection with two accounts, and a Plaid one
-- that needs reauth. A free user's connection for the 403 paths.
INSERT INTO bank_connections (id, user_id, provider, institution_id, institution_name, provider_item_id, access_token_enc, sync_cursor, status, consent_expires_at, last_synced_at, last_sync_status, last_sync_error, created_at, updated_at) VALUES
  ('b4c00000-0000-4000-8000-000000000001', 'b4b4b4b4-0000-4000-8000-000000000002', 'enablebanking', 'PT:Banco Teste', 'Banco Teste', 'parity-banking-session-1', NULL, NULL, 'active', '2027-04-18 10:00:00.75+00', '2026-10-28 09:30:15.123+00', 'ok', NULL, '2026-10-01 08:00:00+00', '2026-10-28 09:30:15+00'),
  ('b4c00000-0000-4000-8000-000000000002', 'b4b4b4b4-0000-4000-8000-000000000002', 'plaid', 'ins_1', NULL, 'parity-banking-item-2', NULL, 'cursor', 'reauth_required', NULL, NULL, 'reauth_required', NULL, '2026-09-01 08:00:00+00', NULL),
  ('b4c00000-0000-4000-8000-000000000003', 'b4b4b4b4-0000-4000-8000-000000000004', 'enablebanking', 'PT:Banco Teste', 'Banco Teste', 'parity-banking-session-3', NULL, NULL, 'active', '2027-04-18 10:00:00+00', NULL, NULL, NULL, '2026-10-02 08:00:00+00', NULL),
  ('b4c00000-0000-4000-8000-000000000004', 'b4b4b4b4-0000-4000-8000-000000000004', 'enablebanking', 'PT:Banco Teste', 'Banco Teste', 'parity-banking-session-4', NULL, NULL, 'active', NULL, NULL, NULL, NULL, '2026-10-03 08:00:00+00', NULL),
  ('b4c00000-0000-4000-8000-000000000005', 'b4b4b4b4-0000-4000-8000-000000000001', 'gocardless', 'SANDBOXFINANCE_SFIN0000', 'Sandbox', 'parity-banking-req-5', NULL, NULL, 'active', NULL, NULL, NULL, NULL, '2026-10-03 08:00:00+00', NULL);

INSERT INTO bank_accounts (id, connection_id, provider_account_id, name, mask, currency, type, balance, balance_as_of, created_at, updated_at, identification_hash) VALUES
  ('b4a00000-0000-4000-8000-000000000001', 'b4c00000-0000-4000-8000-000000000001', 'uid-1', 'Conta Ordem', '1234', 'EUR', 'CACC', 1520.25, '2026-10-28 09:30:15.5+00', '2026-10-01 08:00:00+00', NULL, 'hash-1'),
  ('b4a00000-0000-4000-8000-000000000002', 'b4c00000-0000-4000-8000-000000000001', 'uid-2', 'Poupança', NULL, NULL, NULL, NULL, NULL, '2026-10-01 08:00:01+00', NULL, NULL),
  ('b4a00000-0000-4000-8000-000000000003', 'b4c00000-0000-4000-8000-000000000003', 'uid-3', 'Conta', NULL, 'EUR', NULL, NULL, NULL, '2026-10-02 08:00:00+00', NULL, NULL);

-- A manual expense of 12.00 on 2026-10-17 flags the -12.00 transaction a
-- day later as a possible duplicate.
INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at) VALUES
  ('b4e00000-0000-4000-8000-000000000001', 'b4b4b4b4-0000-4000-8000-000000000002', 'Lunch', 12, 'fun', '2026-10-17', now()),
  ('b4e00000-0000-4000-8000-000000000002', 'b4b4b4b4-0000-4000-8000-000000000002', 'Imported', 8.5, 'fixed', '2026-10-10', now());

INSERT INTO bank_transactions (id, account_id, user_id, provider_tx_id, dedupe_hash, amount, currency, occurred_on, merchant, description_text, pending, status, provider_category, expense_id, created_at, updated_at) VALUES
  ('b4f00000-0000-4000-8000-000000000001', 'b4a00000-0000-4000-8000-000000000001', 'b4b4b4b4-0000-4000-8000-000000000002', 'r-1', 'h1', -12, 'EUR', '2026-10-18 00:00:00+00', 'Cafe Central', 'Compra Cafe Central', false, 'suggested', '5812', NULL, now(), now()),
  ('b4f00000-0000-4000-8000-000000000002', 'b4a00000-0000-4000-8000-000000000001', 'b4b4b4b4-0000-4000-8000-000000000002', 'r-2', 'h2', 250.5, NULL, '2026-10-20 00:00:00+00', NULL, NULL, true, 'suggested', NULL, NULL, now(), now()),
  ('b4f00000-0000-4000-8000-000000000003', 'b4a00000-0000-4000-8000-000000000002', 'b4b4b4b4-0000-4000-8000-000000000002', 'r-3', 'h3', -3.2, 'EUR', '2026-10-05 00:00:00+00', 'Metro', NULL, false, 'dismissed', NULL, NULL, now(), now()),
  ('b4f00000-0000-4000-8000-000000000004', 'b4a00000-0000-4000-8000-000000000001', 'b4b4b4b4-0000-4000-8000-000000000002', 'r-4', 'h4', -8.5, 'EUR', '2026-10-10 00:00:00+00', 'Shop', NULL, false, 'imported', NULL, 'b4e00000-0000-4000-8000-000000000002', now(), now()),
  -- User 4: w1 is imported, w2 dismissed, w3 is already imported (409).
  ('b4f00000-0000-4000-8000-000000000011', 'b4a00000-0000-4000-8000-000000000003', 'b4b4b4b4-0000-4000-8000-000000000004', 'w-1', 'hw1', -23.45, 'EUR', '2026-10-21 00:00:00+00', 'Pingo Doce', 'Compra Pingo Doce', false, 'suggested', NULL, NULL, now(), now()),
  ('b4f00000-0000-4000-8000-000000000012', 'b4a00000-0000-4000-8000-000000000003', 'b4b4b4b4-0000-4000-8000-000000000004', 'w-2', 'hw2', -5, 'EUR', '2026-10-22 00:00:00+00', NULL, 'Transfer', false, 'suggested', NULL, NULL, now(), now()),
  ('b4f00000-0000-4000-8000-000000000013', 'b4a00000-0000-4000-8000-000000000003', 'b4b4b4b4-0000-4000-8000-000000000004', 'w-3', 'hw3', -7, 'EUR', '2026-10-22 00:00:00+00', 'Bolt', NULL, false, 'imported', NULL, NULL, now(), now());

-- Mutating cases run twice (Vapor, then v2): keep w1/w2 awaiting review and
-- connection 4 in place so both runs see the same state.
CREATE FUNCTION parity_bank_tx_sticky() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id IN ('b4f00000-0000-4000-8000-000000000011', 'b4f00000-0000-4000-8000-000000000012') THEN
    NEW.status := 'suggested';
    NEW.expense_id := NULL;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER parity_bank_tx_sticky BEFORE UPDATE ON bank_transactions
  FOR EACH ROW EXECUTE FUNCTION parity_bank_tx_sticky();

CREATE FUNCTION parity_bank_conn_keep() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.id = 'b4c00000-0000-4000-8000-000000000004' THEN
    RETURN NULL;
  END IF;
  RETURN OLD;
END $$;
CREATE TRIGGER parity_bank_conn_keep BEFORE DELETE ON bank_connections
  FOR EACH ROW EXECUTE FUNCTION parity_bank_conn_keep();

-- Link flows: one live, one expired, one already used.
INSERT INTO bank_link_flows (id, user_id, provider, reference, requisition_id, institution_id, app_redirect_uri, expires_at, used_at, created_at) VALUES
  (gen_random_uuid(), 'b4b4b4b4-0000-4000-8000-000000000004', 'enablebanking', 'parity-banking-state-live', 'auth-1', 'PT:Banco Teste', 'norviqa://oauth/bank-callback', now() + interval '1 day', NULL, now()),
  (gen_random_uuid(), 'b4b4b4b4-0000-4000-8000-000000000004', 'enablebanking', 'parity-banking-state-expired', 'auth-2', 'PT:Banco Teste', 'norviqa://oauth/bank-callback', now() - interval '1 hour', NULL, now()),
  (gen_random_uuid(), 'b4b4b4b4-0000-4000-8000-000000000004', 'gocardless', 'parity-banking-ref-live', 'req-1', 'SANDBOXFINANCE_SFIN0000', 'https://www.norviq.org/settings/banks?tab=banks', now() + interval '1 day', NULL, now());

-- Telegram preferences for user 2.
INSERT INTO messaging_preferences (id, user_id, platform, kind, enabled, quiet_hours_start, quiet_hours_end, timezone, created_at, updated_at) VALUES
  (gen_random_uuid(), 'b4b4b4b4-0000-4000-8000-000000000002', 'telegram', 'budget', true, 22, 7, 'Europe/Lisbon', now(), now());

COMMIT;
