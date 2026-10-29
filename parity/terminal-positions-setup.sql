-- Fixtures for parity/terminal-positions*.jsonl and parity/research*.jsonl.
-- Each backend runs against its own copy of the scratch DB (never
-- production), seeded identically, so write cases compare exactly:
--
--   docker exec norviq-v2-schema-pg createdb -U pg parity_terminal_a   (and _b)
--   docker exec norviq-v2-schema-pg sh -c "pg_dump -U pg norviq | psql -q -U pg parity_terminal_a"   (and _b)
--   psql postgres://pg:pg@127.0.0.1:55499/parity_terminal_a -f parity/terminal-positions-setup.sql   (and _b)
--
-- Re-running resets every fixture row (writes leave extra rows behind, so
-- re-seed both copies before re-running the *-writes files).

DELETE FROM users WHERE email LIKE 'parity-terminal-%@example.com';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('7e000000-0000-4000-8000-000000000001', 'parity-terminal-reader@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('7e000000-0000-4000-8000-000000000002', 'parity-terminal-writer@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('7e000000-0000-4000-8000-000000000003', 'parity-terminal-free@example.com',   'x', '2026-01-01T00:00:00Z', true),
  ('7e000000-0000-4000-8000-000000000004', 'parity-terminal-pro@example.com',    'x', '2026-01-01T00:00:00Z', true),
  ('7e000000-0000-4000-8000-000000000005', 'parity-terminal-empty@example.com',  'x', '2026-01-01T00:00:00Z', true),
  ('7e000000-0000-4000-8000-000000000006', 'parity-terminal-order@example.com',  'x', '2026-01-01T00:00:00Z', true),
  ('7e000000-0000-4000-8000-000000000007', 'parity-terminal-research-reader@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('7e000000-0000-4000-8000-000000000008', 'parity-terminal-research-writer@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('7e000000-0000-4000-8000-000000000009', 'parity-terminal-other@example.com',  'x', '2026-01-01T00:00:00Z', true);

-- Pro via an entitlement row.
INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('7e000000-0000-4000-8000-0000000000e4', '7e000000-0000-4000-8000-000000000004', 'pro', now(), now());

-- PATs: planning:read only (reader), research:read only (research reader).
INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at) VALUES
  ('7e000000-0000-4000-8000-0000000000a1', '7e000000-0000-4000-8000-000000000001', 'parity',
   '584f8b2a461891d9d8e5a9c92fc93406df856700e6008bcc5a7c7e072d6e4bca', '{planning:read}', now() + interval '30 days', now()),
  ('7e000000-0000-4000-8000-0000000000a2', '7e000000-0000-4000-8000-000000000007', 'parity',
   'e042cf4c5abe004e6a3cf2aad59148aaa5a3a68c297b9c4dec61e93f94914450', '{research:read}', now() + interval '30 days', now());

-- Reader: EUR default portfolio, a non-default USD one, three positions
-- (one with a scenario error), two autobuys (one paused).
INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, base_currency) VALUES
  ('7e000000-0000-4000-8000-0000000000b1', '7e000000-0000-4000-8000-000000000001', 'Main', true,  '2026-01-01T00:00:00Z', 'EUR'),
  ('7e000000-0000-4000-8000-0000000000b2', '7e000000-0000-4000-8000-000000000001', 'Side', false, '2026-01-02T00:00:00Z', 'USD'),
  ('7e000000-0000-4000-8000-0000000000b3', '7e000000-0000-4000-8000-000000000002', 'Main', true,  '2026-01-01T00:00:00Z', 'CHF');

INSERT INTO terminal_positions (id, user_id, ticker, shares_outstanding, terminal_share_count, terminal_market_cap,
  value_wanted, shares_owned, current_share_price, notes, sort_order, created_at, updated_at) VALUES
  ('7e000000-0000-4000-8000-000000000c01', '7e000000-0000-4000-8000-000000000001', 'AMZN', 10600000000, 11000000000, 10000000000000,
   1000000, 750, 200, 'base case', 0, '2026-02-01T10:00:00.123456Z', '2026-02-02T10:00:00Z'),
  ('7e000000-0000-4000-8000-000000000c02', '7e000000-0000-4000-8000-000000000001', 'VG', NULL, 200000000, 12500000000,
   500000, 0, NULL, NULL, 1, '2026-02-01T11:00:00Z', NULL),
  ('7e000000-0000-4000-8000-000000000c03', '7e000000-0000-4000-8000-000000000001', 'BAD', NULL, 0, 1,
   99, 0, NULL, NULL, 2, '2026-02-01T12:00:00Z', '2026-02-01T12:00:00Z'),
  ('7e000000-0000-4000-8000-000000000c04', '7e000000-0000-4000-8000-000000000001', 'AMZN', NULL, 12000000000, 3000000000000,
   0, 10, 0.5, NULL, 2, '2026-02-01T13:00:00Z', '2026-02-01T13:00:00Z');

INSERT INTO autobuys (id, user_id, ticker, label, amount, cadence, percent, active, created_at, updated_at) VALUES
  ('7e000000-0000-4000-8000-000000000d01', '7e000000-0000-4000-8000-000000000001', 'AMZN', 'AMZN', 275, 'bimonthly', NULL, true,  '2026-02-01T00:00:00Z', '2026-02-01T00:00:00Z'),
  ('7e000000-0000-4000-8000-000000000d02', '7e000000-0000-4000-8000-000000000001', NULL,   'Paused', 999, 'monthly', NULL, false, '2026-02-02T00:00:00Z', '2026-02-02T00:00:00Z'),
  ('7e000000-0000-4000-8000-000000000d03', '7e000000-0000-4000-8000-000000000001', NULL,   '401k', 5000, 'percentOfContribution', 0.04, true, '2026-02-03T00:00:00Z', '2026-02-03T00:00:00Z'),
  ('7e000000-0000-4000-8000-000000000d04', '7e000000-0000-4000-8000-000000000001', NULL,   'Future', 10, 'quarterly', NULL, true, '2026-02-04T00:00:00Z', '2026-02-04T00:00:00Z');

-- Writer: three rows to patch, duplicate and delete.
INSERT INTO terminal_positions (id, user_id, ticker, shares_outstanding, terminal_share_count, terminal_market_cap,
  value_wanted, shares_owned, current_share_price, notes, sort_order, created_at, updated_at) VALUES
  ('7e000000-0000-4000-8000-000000000c11', '7e000000-0000-4000-8000-000000000002', 'AMZN', 10600000000, 11000000000, 10000000000000,
   1000000, 750, 200, 'keep me', 0, '2026-02-01T10:00:00Z', '2026-02-01T10:00:00Z'),
  ('7e000000-0000-4000-8000-000000000c12', '7e000000-0000-4000-8000-000000000002', 'VG', NULL, 200000000, 12500000000,
   500000, 0, NULL, NULL, 1, '2026-02-01T11:00:00Z', '2026-02-01T11:00:00Z'),
  ('7e000000-0000-4000-8000-000000000c13', '7e000000-0000-4000-8000-000000000002', 'DEL', NULL, 1, 1,
   1, 0, NULL, NULL, 2, '2026-02-01T12:00:00Z', '2026-02-01T12:00:00Z');

-- Order user: two rows only, so a reorder can list every id.
INSERT INTO terminal_positions (id, user_id, ticker, shares_outstanding, terminal_share_count, terminal_market_cap,
  value_wanted, shares_owned, current_share_price, notes, sort_order, created_at, updated_at) VALUES
  ('7e000000-0000-4000-8000-000000000c21', '7e000000-0000-4000-8000-000000000006', 'AAA', NULL, 1, 10, 5, 0, NULL, NULL, 0, '2026-02-01T10:00:00Z', '2026-02-01T10:00:00Z'),
  ('7e000000-0000-4000-8000-000000000c22', '7e000000-0000-4000-8000-000000000006', 'BBB', NULL, 1, 10, 5, 0, NULL, NULL, 1, '2026-02-01T11:00:00Z', '2026-02-01T11:00:00Z');

-- Research reader: three notes, one with links, one with unreadable links.
INSERT INTO research_notes (id, user_id, symbol, title, thesis, risks, catalysts, reference_links, created_at, updated_at) VALUES
  ('7e000000-0000-4000-8000-000000000f01', '7e000000-0000-4000-8000-000000000007', 'AAPL', 'Services', 'Margin expansion', 'China', 'WWDC',
   '["https:\/\/a.example\/x","https://b.example"]', '2026-03-01T00:00:00Z', '2026-03-05T00:00:00Z'),
  ('7e000000-0000-4000-8000-000000000f02', '7e000000-0000-4000-8000-000000000007', 'MSFT', NULL, 'Azure', NULL, NULL,
   NULL, '2026-03-02T00:00:00Z', '2026-03-06T00:00:00Z'),
  ('7e000000-0000-4000-8000-000000000f03', '7e000000-0000-4000-8000-000000000007', 'AAPL', NULL, 'Old thesis', NULL, NULL,
   'not json', '2026-03-03T00:00:00Z', NULL);

-- Research writer: notes to update and delete.
INSERT INTO research_notes (id, user_id, symbol, title, thesis, risks, catalysts, reference_links, created_at, updated_at) VALUES
  ('7e000000-0000-4000-8000-000000000f11', '7e000000-0000-4000-8000-000000000008', 'NVDA', 't', 'GPUs', NULL, NULL, NULL, '2026-03-01T00:00:00Z', '2026-03-01T00:00:00Z'),
  ('7e000000-0000-4000-8000-000000000f12', '7e000000-0000-4000-8000-000000000008', 'DEL', NULL, 'delete me', NULL, NULL, NULL, '2026-03-01T00:00:00Z', '2026-03-01T00:00:00Z');
