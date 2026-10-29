-- Fixtures for parity/macro.jsonl. Idempotent: run before every parity pass
-- against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/macro-setup.sql
--
-- Users are 3ac00000-0000-4000-8000-00000000000N:
--   1 with a year of expenses (personal inflation), dates relative to today
--   2 with no expenses
-- PATs: nvq_pat_paritymacroread0001 (market:read) and
-- nvq_pat_paritymacronoscope01 (settings:read only).
--
-- Macro data itself is whatever the parity Vapor's MacroRefreshJob ingested
-- (Eurostat/ECB for the euro area, IBGE for BR; no FRED key, so US serves
-- stub data). Both backends read the same rows.

BEGIN;

DELETE FROM personal_access_tokens WHERE id::text LIKE '3ac00000-%';
DELETE FROM expenses WHERE user_id::text LIKE '3ac00000-%';
DELETE FROM expense_categories WHERE user_id::text LIKE '3ac00000-%';
DELETE FROM users WHERE id::text LIKE '3ac00000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('3ac00000-0000-4000-8000-000000000001', 'parity-news-macro-1@example.com', 'x', now(), true),
  ('3ac00000-0000-4000-8000-000000000002', 'parity-news-macro-2@example.com', 'x', now(), true);

INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at) VALUES
  ('3ac00000-0000-4000-8000-0000000000e1', '3ac00000-0000-4000-8000-000000000001', 'parity-macro-read',
   '4a7736339d61e0ac868954737dbde1dca44cbd3c5159eeb47ef0067e7ab978b8', '{market:read}', '2099-01-01T00:00:00Z', now()),
  ('3ac00000-0000-4000-8000-0000000000e2', '3ac00000-0000-4000-8000-000000000001', 'parity-macro-noscope',
   '5e44ac34b273a20bb5b66e137ae81400bd5e6643d6e9239ff268a7921544f822', '{settings:read}', '2099-01-01T00:00:00Z', now());

INSERT INTO expense_categories (id, user_id, name, pillar, is_default, created_at) VALUES
  ('3ac00000-0000-4000-8000-000000000101', '3ac00000-0000-4000-8000-000000000001', 'Groceries', 'fundamentals', true, now()),
  ('3ac00000-0000-4000-8000-000000000102', '3ac00000-0000-4000-8000-000000000001', 'Rent', 'fundamentals', true, now()),
  ('3ac00000-0000-4000-8000-000000000103', '3ac00000-0000-4000-8000-000000000001', 'Dining', 'fun', false, now()),
  ('3ac00000-0000-4000-8000-000000000104', '3ac00000-0000-4000-8000-000000000001', 'Pets', 'fun', false, now()),
  ('3ac00000-0000-4000-8000-000000000105', '3ac00000-0000-4000-8000-000000000001', '  Saúde  ', 'fundamentals', false, now());

INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at,
    split_mode, user_share_percent, category_id) VALUES
  ('3ac00000-0000-4000-8000-000000000401', '3ac00000-0000-4000-8000-000000000001', 'Weekly shop', 120.5, 'fundamentals', CURRENT_DATE - 3, now(), now(), 'personal', 100, '3ac00000-0000-4000-8000-000000000101'),
  ('3ac00000-0000-4000-8000-000000000402', '3ac00000-0000-4000-8000-000000000001', 'Supermarket', 80.25, 'fundamentals', CURRENT_DATE - 40, now(), now(), 'personal', 100, '3ac00000-0000-4000-8000-000000000101'),
  ('3ac00000-0000-4000-8000-000000000403', '3ac00000-0000-4000-8000-000000000001', 'October rent', 1400, 'fundamentals', CURRENT_DATE - 20, now(), now(), 'shared', 60, '3ac00000-0000-4000-8000-000000000102'),
  ('3ac00000-0000-4000-8000-000000000404', '3ac00000-0000-4000-8000-000000000001', 'Sushi night', 64, 'fun', CURRENT_DATE - 75, now(), now(), 'personal', 100, '3ac00000-0000-4000-8000-000000000103'),
  ('3ac00000-0000-4000-8000-000000000405', '3ac00000-0000-4000-8000-000000000001', 'Vet visit', 90, 'fun', CURRENT_DATE - 100, now(), now(), 'personal', 100, '3ac00000-0000-4000-8000-000000000104'),
  ('3ac00000-0000-4000-8000-000000000406', '3ac00000-0000-4000-8000-000000000001', 'Uber to airport', 35, 'fun', CURRENT_DATE - 130, now(), now(), 'personal', 100, NULL),
  ('3ac00000-0000-4000-8000-000000000407', '3ac00000-0000-4000-8000-000000000001', 'Netflix subscription', 15.99, 'fun', CURRENT_DATE - 200, now(), now(), 'personal', 100, NULL),
  ('3ac00000-0000-4000-8000-000000000408', '3ac00000-0000-4000-8000-000000000001', 'Pharmacy', 22.4, 'fundamentals', CURRENT_DATE - 250, now(), now(), 'personal', 100, '3ac00000-0000-4000-8000-000000000105'),
  ('3ac00000-0000-4000-8000-000000000409', '3ac00000-0000-4000-8000-000000000001', 'Old groceries', 300, 'fundamentals', CURRENT_DATE - 500, now(), now(), 'personal', 100, '3ac00000-0000-4000-8000-000000000101'),
  ('3ac00000-0000-4000-8000-000000000410', '3ac00000-0000-4000-8000-000000000001', 'Refund', -50, 'fundamentals', CURRENT_DATE - 5, now(), now(), 'personal', 100, '3ac00000-0000-4000-8000-000000000101'),
  ('3ac00000-0000-4000-8000-000000000411', '3ac00000-0000-4000-8000-000000000001', 'Café com amigos', 12, 'fun', CURRENT_DATE - 9, now(), now(), 'shared', 50, NULL),
  ('3ac00000-0000-4000-8000-000000000412', '3ac00000-0000-4000-8000-000000000001', 'Electric bill', 70, 'fundamentals', CURRENT_DATE, now(), now(), 'personal', 100, NULL);

COMMIT;
