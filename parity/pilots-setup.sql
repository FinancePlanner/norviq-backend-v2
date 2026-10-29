-- Fixtures for parity/pilots.jsonl. Idempotent; run against BOTH parity
-- copies of the scratch database (never production), one per backend, so
-- the write cases see identical state:
--
--   psql postgres://pg:pg@127.0.0.1:55499/parity_pilots_a -f parity/pilots-setup.sql
--   psql postgres://pg:pg@127.0.0.1:55499/parity_pilots_b -f parity/pilots-setup.sql
--
-- Users are 91107000-0000-4000-8000-00000000000N: 1 is Pro, 2 is free,
-- 3 owns nothing. Deleting the users cascades to their follows, lists,
-- stocks and accounts. Pilots come from the SeedPilots migration.

BEGIN;

DELETE FROM cash_balances WHERE account_id IN (SELECT id FROM accounts WHERE user_id::text LIKE '91107000-0000-4000-8000-%');
DELETE FROM accounts WHERE user_id::text LIKE '91107000-0000-4000-8000-%';
DELETE FROM watchlist_items WHERE user_id::text LIKE '91107000-0000-4000-8000-%';
DELETE FROM stocks WHERE user_id::text LIKE '91107000-0000-4000-8000-%';
DELETE FROM pilot_follows WHERE user_id::text LIKE '91107000-0000-4000-8000-%';
DELETE FROM portfolio_lists WHERE user_id::text LIKE '91107000-0000-4000-8000-%';
DELETE FROM watchlist_lists WHERE user_id::text LIKE '91107000-0000-4000-8000-%';
DELETE FROM entitlements WHERE user_id::text LIKE '91107000-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE '91107000-0000-4000-8000-%';
DELETE FROM pilot_book_versions WHERE pilot_id IN (SELECT id FROM pilots WHERE slug IN ('nancy-pelosi', 'dan-crenshaw', 'ro-khanna'));
DELETE FROM pilot_disclosures WHERE pilot_id IN (SELECT id FROM pilots WHERE slug IN ('nancy-pelosi', 'dan-crenshaw', 'ro-khanna'));

INSERT INTO users (id, email, password_hash, created_at, is_verified, email_verified_at, username) VALUES
  ('91107000-0000-4000-8000-000000000001', 'parity-social-pilots-pro@example.com', 'x', now(), true, now(), 'paritypilotspro'),
  ('91107000-0000-4000-8000-000000000002', 'parity-social-pilots-free@example.com', 'x', now(), true, now(), 'paritypilotsfree'),
  ('91107000-0000-4000-8000-000000000003', 'parity-social-pilots-other@example.com', 'x', now(), true, now(), 'paritypilotsother');
INSERT INTO entitlements (id, user_id, level, created_at) VALUES
  ('91107000-0000-4000-8000-0000000000e1', '91107000-0000-4000-8000-000000000001', 'pro', now());

-- Books: Pelosi v1 and v2, Crenshaw v1. Khanna has none (409 on follow).
INSERT INTO pilot_book_versions (id, pilot_id, version, computed_at, weights, skipped_puts)
SELECT '91107000-0000-4000-8000-0000000000b1', id, 1, '2026-10-01 00:00:00+00', '{"NVDA": 0.5, "AAPL": 0.5}', 0 FROM pilots WHERE slug = 'nancy-pelosi';
INSERT INTO pilot_book_versions (id, pilot_id, version, computed_at, weights, skipped_puts)
SELECT '91107000-0000-4000-8000-0000000000b2', id, 2, '2026-10-02 12:30:45.678+00', '{"NVDA": 0.6, "AAPL": 0.25, "MSFT": 0.15}', 2 FROM pilots WHERE slug = 'nancy-pelosi';
INSERT INTO pilot_book_versions (id, pilot_id, version, computed_at, weights, skipped_puts)
SELECT '91107000-0000-4000-8000-0000000000b3', id, 1, '2026-10-03 08:00:00+00', '{"TSLA": 1.0}', 0 FROM pilots WHERE slug = 'dan-crenshaw';

INSERT INTO pilot_disclosures (id, pilot_id, source_key, symbol, side, instrument, transaction_date, disclosure_date, amount_min, amount_max, period, discovered_at)
SELECT '91107000-0000-4000-8000-0000000000d1', id, 'parity-1', 'NVDA', 'buy', 'call', '2026-09-14', '2026-09-30', 1000001, 5000000, NULL, '2026-10-01 00:00:00+00' FROM pilots WHERE slug = 'nancy-pelosi';
INSERT INTO pilot_disclosures (id, pilot_id, source_key, symbol, side, instrument, transaction_date, disclosure_date, amount_min, amount_max, period, discovered_at)
SELECT '91107000-0000-4000-8000-0000000000d2', id, 'parity-2', 'AAPL', 'sell_full', 'stock', '2026-09-20', NULL, NULL, 15000, NULL, '2026-10-01 00:00:01+00' FROM pilots WHERE slug = 'nancy-pelosi';
INSERT INTO pilot_disclosures (id, pilot_id, source_key, symbol, side, instrument, transaction_date, disclosure_date, amount_min, amount_max, period, discovered_at)
SELECT '91107000-0000-4000-8000-0000000000d3', id, 'parity-3', 'SPY', 'buy', 'put', NULL, NULL, NULL, NULL, NULL, '2026-10-01 00:00:02+00' FROM pilots WHERE slug = 'nancy-pelosi';

-- User 1: a watchlist follow of Pelosi with events, and a portfolio follow
-- of Crenshaw with events and snapshots.
INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES
  ('91107000-0000-4000-8000-0000000000a1', '91107000-0000-4000-8000-000000000001', 'Nancy Pelosi feed', false, now()),
  ('91107000-0000-4000-8000-0000000000a2', '91107000-0000-4000-8000-000000000001', 'Main', true, now()),
  ('91107000-0000-4000-8000-0000000000a3', '91107000-0000-4000-8000-000000000002', 'Free empty', false, now());
INSERT INTO portfolio_lists (id, user_id, name, is_default, mode, created_at) VALUES
  ('91107000-0000-4000-8000-0000000000c1', '91107000-0000-4000-8000-000000000001', 'Dan Crenshaw copy', false, 'hypothetical', now()),
  ('91107000-0000-4000-8000-0000000000c2', '91107000-0000-4000-8000-000000000001', 'Real money', false, 'actual', now()),
  ('91107000-0000-4000-8000-0000000000c3', '91107000-0000-4000-8000-000000000001', 'Busy sim', false, 'hypothetical', now()),
  ('91107000-0000-4000-8000-0000000000c4', '91107000-0000-4000-8000-000000000001', 'Empty sim', false, 'hypothetical', now());
INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, created_at) VALUES
  ('91107000-0000-4000-8000-0000000000f1', '91107000-0000-4000-8000-000000000001', '91107000-0000-4000-8000-0000000000c3', 'KO', 1, 60, '2026-09-01', now());
INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, portfolio_list_id, watchlist_list_id, starting_capital, currency, applied_version, status, created_at, updated_at)
SELECT '91107000-0000-4000-8000-000000000f01', '91107000-0000-4000-8000-000000000001', id, 'watchlist', NULL, '91107000-0000-4000-8000-0000000000a1', NULL, 'USD', 1, 'active', '2026-10-05 10:00:00.123+00', NULL FROM pilots WHERE slug = 'nancy-pelosi';
INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, portfolio_list_id, watchlist_list_id, starting_capital, currency, applied_version, status, created_at, updated_at)
SELECT '91107000-0000-4000-8000-000000000f02', '91107000-0000-4000-8000-000000000001', id, 'portfolio', '91107000-0000-4000-8000-0000000000c1', NULL, 25000.5, 'USD', 1, 'paused', '2026-10-06 10:00:00+00', NULL FROM pilots WHERE slug = 'dan-crenshaw';
INSERT INTO pilot_follow_events (id, follow_id, book_version, kind, symbol, quantity, price, priced_at, note, created_at) VALUES
  ('91107000-0000-4000-8000-000000000e01', '91107000-0000-4000-8000-000000000f01', 1, 'watch_added', 'NVDA', NULL, NULL, '2026-10-05 10:00:00.5+00', 'Nancy Pelosi bought 2026-09-14', now()),
  ('91107000-0000-4000-8000-000000000e02', '91107000-0000-4000-8000-000000000f01', 1, 'watch_added', 'AAPL', NULL, NULL, '2026-10-05 10:00:00.5+00', 'Nancy Pelosi bought 2026-09-20', now()),
  ('91107000-0000-4000-8000-000000000e03', '91107000-0000-4000-8000-000000000f02', 1, 'buy', 'TSLA', 101.123456, 247.3, '2026-10-06 10:00:00+00', NULL, now()),
  ('91107000-0000-4000-8000-000000000e04', '91107000-0000-4000-8000-000000000f02', 1, 'skipped_unpriced', 'ZZZZ', NULL, NULL, '2026-10-06 10:00:00+00', 'No price available; left unchanged.', now());
INSERT INTO pilot_follow_snapshots (id, follow_id, captured_on, value, cash) VALUES
  ('91107000-0000-4000-8000-000000000501', '91107000-0000-4000-8000-000000000f02', '2026-10-07 00:00:00+00', 25010.42, 0.33),
  ('91107000-0000-4000-8000-000000000502', '91107000-0000-4000-8000-000000000f02', '2026-10-06 00:00:00+00', 25000.5, 0.33);

-- PATs for user 3 (raw tokens are in pilots-writes.jsonl).
INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at) VALUES
  ('91107000-0000-4000-8000-000000000701', '91107000-0000-4000-8000-000000000003', 'parity portfolio only', '83fa0de0001185dd0b55e2399faebd44a3283ffdf1997d57e4edc2a96be59283', '{portfolio:read,portfolio:write}', now() + interval '30 days', now()),
  ('91107000-0000-4000-8000-000000000702', '91107000-0000-4000-8000-000000000003', 'parity watch', '0cf096578f6287a52daddfee77c887802edbc8a634587853640f8898f67324eb', '{portfolio:write,watchlist:write}', now() + interval '30 days', now()),
  ('91107000-0000-4000-8000-000000000703', '91107000-0000-4000-8000-000000000003', 'parity read', 'f3f5c027456b23ee4a8ac58a6e0e827a3322dc6e6dfbb49a6182e4e1518c56f6', '{portfolio:read}', now() + interval '30 days', now());

COMMIT;
