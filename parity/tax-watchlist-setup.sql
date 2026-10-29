-- Fixtures for parity/tax-watchlist.jsonl (watchlist, lists, CSV import,
-- targets, smart screens, holding risk profiles). Idempotent: run before
-- every parity pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/tax-watchlist-setup.sql
--
-- Users 0a7c0000-0000-4000-8000-0000000000aN:
--   a1 pro, read fixtures        a2 free, no data
--   a3 pro, mutating cases       a4 pro, no data (default list creation)
--   a5 pro, follows a pilot on its only (default) watchlist
--
-- Mutating cases run twice (Vapor, then v2). The triggers below make them
-- repeatable: rows inserted with PARITY* markers are removed again right
-- after the insert, and the rows the DELETE cases name are never deleted.
-- Every trigger only touches rows of user a3.

BEGIN;

DROP TRIGGER IF EXISTS parity_tax_wl_keep ON watchlist_lists;
DROP TRIGGER IF EXISTS parity_tax_wl_keep ON watchlist_items;
DROP TRIGGER IF EXISTS parity_tax_wl_keep ON targets;
DROP TRIGGER IF EXISTS parity_tax_wl_keep ON watchlist_screens;
DROP TRIGGER IF EXISTS parity_tax_wl_keep ON holding_risk_profiles;
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON watchlist_items;
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON watchlist_lists;
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON targets;
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON watchlist_screens;
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON watchlist_screen_evaluations;

DELETE FROM users WHERE id::text LIKE '0a7c0000-0000-4000-8000-%';
DELETE FROM pilots WHERE id = '0a7c0000-0000-4000-8000-0000000000f1';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('0a7c0000-0000-4000-8000-0000000000a1', 'parity-tax-wl-a1@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('0a7c0000-0000-4000-8000-0000000000a2', 'parity-tax-wl-a2@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('0a7c0000-0000-4000-8000-0000000000a3', 'parity-tax-wl-a3@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('0a7c0000-0000-4000-8000-0000000000a4', 'parity-tax-wl-a4@example.com', 'x', '2026-01-01T00:00:00Z', true),
  ('0a7c0000-0000-4000-8000-0000000000a5', 'parity-tax-wl-a5@example.com', 'x', '2026-01-01T00:00:00Z', true);

INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-0000000000e1', '0a7c0000-0000-4000-8000-0000000000a1', 'pro', now(), now()),
  ('0a7c0000-0000-4000-8000-0000000000e3', '0a7c0000-0000-4000-8000-0000000000a3', 'pro', now(), now()),
  ('0a7c0000-0000-4000-8000-0000000000e4', '0a7c0000-0000-4000-8000-0000000000a4', 'pro', now(), now()),
  ('0a7c0000-0000-4000-8000-0000000000e5', '0a7c0000-0000-4000-8000-0000000000a5', 'pro', now(), now());

-- a1: two watchlists, items with microsecond timestamps.
INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000101', '0a7c0000-0000-4000-8000-0000000000a1', 'Main Watchlist', true, '2026-02-01T10:00:00.123456Z', '2026-02-01T10:00:00.123456Z'),
  ('0a7c0000-0000-4000-8000-000000000102', '0a7c0000-0000-4000-8000-0000000000a1', 'Tech', false, '2026-02-02T10:00:00.999999Z', NULL);
INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, note, status, last_reviewed_at, next_review_at, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000201', '0a7c0000-0000-4000-8000-0000000000a1', '0a7c0000-0000-4000-8000-000000000101', 'AAPL', 'Core', 'researching', '2026-03-01', '2026-04-01', '2026-03-01T09:15:42.123456Z', '2026-03-02T09:15:42.987654Z'),
  ('0a7c0000-0000-4000-8000-000000000202', '0a7c0000-0000-4000-8000-0000000000a1', '0a7c0000-0000-4000-8000-000000000101', 'MSFT', NULL, 'archived', NULL, NULL, '2026-03-01T09:15:43Z', NULL),
  ('0a7c0000-0000-4000-8000-000000000203', '0a7c0000-0000-4000-8000-0000000000a1', '0a7c0000-0000-4000-8000-000000000101', 'OLD', NULL, 'legacy_status', NULL, NULL, '2026-03-01T09:15:44.0005Z', '2026-03-01T09:15:44.0005Z'),
  ('0a7c0000-0000-4000-8000-000000000204', '0a7c0000-0000-4000-8000-0000000000a1', '0a7c0000-0000-4000-8000-000000000102', 'NVDA', 'GPU', 'ready', NULL, '2026-05-05', '2026-03-03T00:00:00Z', '2026-03-03T00:00:00Z');

-- a1: targets.
INSERT INTO targets (id, user_id, symbol, scenario, target_price, target_date, rationale, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000301', '0a7c0000-0000-4000-8000-0000000000a1', 'AAPL', 'bull', 250.5, '2026-12-31', 'AI cycle', '2026-03-01T00:00:00Z', '2026-03-05T00:00:00Z'),
  ('0a7c0000-0000-4000-8000-000000000302', '0a7c0000-0000-4000-8000-0000000000a1', 'MSFT', 'bear', 300, NULL, NULL, '2026-03-02T00:00:00Z', '2026-03-04T00:00:00Z'),
  ('0a7c0000-0000-4000-8000-000000000303', '0a7c0000-0000-4000-8000-0000000000a1', 'AAPL', 'base', 220, NULL, NULL, '2026-03-03T00:00:00Z', '2026-03-04T00:00:00Z');

-- a1: a holding with a risk profile.
INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000401', '0a7c0000-0000-4000-8000-0000000000a1', 'Main Portfolio', true, '2026-01-01T00:00:00Z'),
  ('0a7c0000-0000-4000-8000-000000000403', '0a7c0000-0000-4000-8000-0000000000a3', 'Main Portfolio', true, '2026-01-01T00:00:00Z');
INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, category) VALUES
  ('0a7c0000-0000-4000-8000-000000000411', '0a7c0000-0000-4000-8000-0000000000a1', 'AAPL', 1, 100, '2026-01-01', '2026-01-01T00:00:00Z', '0a7c0000-0000-4000-8000-000000000401', 'stock'),
  ('0a7c0000-0000-4000-8000-000000000412', '0a7c0000-0000-4000-8000-0000000000a1', 'BND', 3, 70, '2026-01-01', '2026-01-01T00:00:00Z', '0a7c0000-0000-4000-8000-000000000401', 'bond'),
  ('0a7c0000-0000-4000-8000-000000000413', '0a7c0000-0000-4000-8000-0000000000a3', 'VTI', 2, 200, '2026-01-01', '2026-01-01T00:00:00Z', '0a7c0000-0000-4000-8000-000000000403', 'etf'),
  ('0a7c0000-0000-4000-8000-000000000414', '0a7c0000-0000-4000-8000-0000000000a3', 'GLD', 2, 180, '2026-01-01', '2026-01-01T00:00:00Z', '0a7c0000-0000-4000-8000-000000000403', 'commodity');
INSERT INTO holding_risk_profiles (id, user_id, holding_id, asset_category, sector, region, benchmark_proxy, manual_value, duration, convexity, factor_overrides, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000421', '0a7c0000-0000-4000-8000-0000000000a1', '0a7c0000-0000-4000-8000-000000000411', 'stock', 'Technology', 'US', 'QQQ', NULL, NULL, NULL, '{"equity_beta": 1.2, "nested": {"b": [1, 2.5, "x", true, null]}}', '2026-03-01T00:00:00Z', '2026-03-02T00:00:00.5Z'),
  ('0a7c0000-0000-4000-8000-000000000422', '0a7c0000-0000-4000-8000-0000000000a1', '0a7c0000-0000-4000-8000-000000000412', 'bond', NULL, NULL, NULL, 210, 6.5, 0.4, '{}', '2026-03-01T00:00:00Z', '2026-03-03T00:00:00Z'),
  ('0a7c0000-0000-4000-8000-000000000423', '0a7c0000-0000-4000-8000-0000000000a3', '0a7c0000-0000-4000-8000-000000000414', 'commodity', NULL, NULL, NULL, NULL, NULL, NULL, '{}', '2026-03-01T00:00:00Z', '2026-03-03T00:00:00Z');

-- a1: a smart screen with one prior evaluation.
INSERT INTO watchlist_screens (id, user_id, name, watchlist_list_ids, logical_operator, groups, alerts_enabled, last_evaluated_at, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000501', '0a7c0000-0000-4000-8000-0000000000a1', 'Cheap quality', ARRAY['0a7c0000-0000-4000-8000-000000000101'::uuid, '0a7c0000-0000-4000-8000-000000000102'::uuid], 'all',
   '{"groups": [{"id": "g1", "logical_operator": "any", "conditions": [{"id": "c1", "metric": "pe_ratio", "comparison": "less_than", "period": "ttm", "value": 15}, {"id": "c2", "metric": "revenue_growth", "comparison": "improving", "period": "annual"}]}]}',
   true, '2026-03-10T08:00:00Z', '2026-03-01T00:00:00Z', '2026-03-01T00:00:00.25Z');
INSERT INTO watchlist_screen_evaluations (id, user_id, screen_id, symbol_count, match_symbols, result, is_alert_baseline, evaluated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000511', '0a7c0000-0000-4000-8000-0000000000a1', '0a7c0000-0000-4000-8000-000000000501', 3, ARRAY['AAPL'],
   '{"matches": [{"id": "AAPL", "symbol": "AAPL", "name": "Apple Inc.", "is_new": false, "condition_results": [{"condition_id": "c1", "matched": true, "value": 12.5, "explanation": "Condition matched."}, {"condition_id": "c2", "matched": false, "value": 0.1, "previous_value": 0.2, "explanation": "Condition did not match."}]}]}',
   true, '2026-03-10T08:00:00Z'),
  ('0a7c0000-0000-4000-8000-000000000512', '0a7c0000-0000-4000-8000-0000000000a1', '0a7c0000-0000-4000-8000-000000000501', 2, ARRAY[]::text[], '{"matches": []}', false, '2026-03-11T08:00:00.75Z');

-- a3: lists and items for the mutating cases.
INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000601', '0a7c0000-0000-4000-8000-0000000000a3', 'Main Watchlist', true, '2026-02-01T00:00:00Z', NULL),
  ('0a7c0000-0000-4000-8000-000000000602', '0a7c0000-0000-4000-8000-0000000000a3', 'Second', false, '2026-02-02T00:00:00Z', NULL),
  ('0a7c0000-0000-4000-8000-000000000603', '0a7c0000-0000-4000-8000-0000000000a3', 'Keep Me', false, '2026-02-03T00:00:00Z', NULL);
INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, note, status, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000611', '0a7c0000-0000-4000-8000-0000000000a3', '0a7c0000-0000-4000-8000-000000000601', 'AMZN', 'Retail', 'active', '2026-03-01T00:00:00Z', NULL),
  ('0a7c0000-0000-4000-8000-000000000612', '0a7c0000-0000-4000-8000-0000000000a3', '0a7c0000-0000-4000-8000-000000000601', 'META', NULL, 'waiting', '2026-03-01T00:00:00Z', NULL),
  ('0a7c0000-0000-4000-8000-000000000613', '0a7c0000-0000-4000-8000-0000000000a3', '0a7c0000-0000-4000-8000-000000000602', 'SHOP', NULL, 'active', '2026-03-01T00:00:00Z', NULL),
  ('0a7c0000-0000-4000-8000-000000000614', '0a7c0000-0000-4000-8000-0000000000a3', '0a7c0000-0000-4000-8000-000000000601', 'KEEPME', NULL, 'active', '2026-03-01T00:00:00Z', NULL);
INSERT INTO targets (id, user_id, symbol, scenario, target_price, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000621', '0a7c0000-0000-4000-8000-0000000000a3', 'AMZN', 'base', 180, '2026-03-01T00:00:00Z', NULL),
  ('0a7c0000-0000-4000-8000-000000000622', '0a7c0000-0000-4000-8000-0000000000a3', 'KEEP', 'base', 1, '2026-03-01T00:00:00Z', NULL);
INSERT INTO watchlist_screens (id, user_id, name, watchlist_list_ids, logical_operator, groups, alerts_enabled, last_evaluated_at, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000631', '0a7c0000-0000-4000-8000-0000000000a3', 'Editable', ARRAY['0a7c0000-0000-4000-8000-000000000601'::uuid], 'any',
   '{"groups": [{"id": "g1", "logical_operator": "all", "conditions": [{"id": "c1", "metric": "price", "comparison": "greater_than", "period": "ttm", "value": 1}]}]}',
   false, NULL, '2026-03-01T00:00:00Z', NULL),
  ('0a7c0000-0000-4000-8000-000000000632', '0a7c0000-0000-4000-8000-0000000000a3', 'Keep screen', ARRAY['0a7c0000-0000-4000-8000-000000000601'::uuid], 'any',
   '{"groups": [{"id": "g1", "logical_operator": "all", "conditions": [{"id": "c1", "metric": "price", "comparison": "greater_than", "period": "ttm", "value": 1}]}]}',
   false, NULL, '2026-03-01T00:00:00Z', NULL);
INSERT INTO watchlist_screen_evaluations (id, user_id, screen_id, symbol_count, match_symbols, result, is_alert_baseline, evaluated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000641', '0a7c0000-0000-4000-8000-0000000000a3', '0a7c0000-0000-4000-8000-000000000632', 1, ARRAY['AMZN'], '{"matches": []}', true, '2026-03-10T08:00:00Z');

-- a5: its only watchlist is managed by a pilot follow, so resolving the
-- default creates "Main Watchlist 2" (the follow's list is "Main Watchlist").
INSERT INTO pilots (id, kind, slug, display_name, created_at) VALUES
  ('0a7c0000-0000-4000-8000-0000000000f1', 'fund', 'parity-tax-wl-pilot', 'Parity pilot', now());
INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at, updated_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000701', '0a7c0000-0000-4000-8000-0000000000a5', 'Main Watchlist', true, '2026-02-01T00:00:00Z', NULL);
INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, watchlist_list_id, created_at) VALUES
  ('0a7c0000-0000-4000-8000-000000000702', '0a7c0000-0000-4000-8000-0000000000a5', '0a7c0000-0000-4000-8000-0000000000f1', 'watchlist', '0a7c0000-0000-4000-8000-000000000701', now());

-- Repeatability triggers (user a3 only).
CREATE OR REPLACE FUNCTION parity_tax_wl_drop_new() RETURNS trigger AS $$
DECLARE
  row jsonb := to_jsonb(NEW);
BEGIN
  -- Read columns through jsonb: plpgsql does not short-circuit, so naming a
  -- column another table lacks would fail the insert.
  IF row->>'user_id' = '0a7c0000-0000-4000-8000-0000000000a3' AND (
       (TG_TABLE_NAME IN ('watchlist_items', 'targets') AND row->>'symbol' LIKE 'PARITYNEW%')
    OR (TG_TABLE_NAME IN ('watchlist_lists', 'watchlist_screens') AND row->>'name' LIKE 'Parity New%')
    OR TG_TABLE_NAME = 'watchlist_screen_evaluations')
  THEN
    EXECUTE format('DELETE FROM %I WHERE id = $1', TG_TABLE_NAME) USING NEW.id;
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION parity_tax_wl_keep() RETURNS trigger AS $$
BEGIN
  IF OLD.id IN ('0a7c0000-0000-4000-8000-000000000603', '0a7c0000-0000-4000-8000-000000000614',
                '0a7c0000-0000-4000-8000-000000000622', '0a7c0000-0000-4000-8000-000000000632',
                '0a7c0000-0000-4000-8000-000000000423') THEN
    RETURN NULL;
  END IF;
  RETURN OLD;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON watchlist_items;
CREATE TRIGGER parity_tax_wl_drop_new AFTER INSERT ON watchlist_items FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_drop_new();
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON watchlist_lists;
CREATE TRIGGER parity_tax_wl_drop_new AFTER INSERT ON watchlist_lists FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_drop_new();
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON targets;
CREATE TRIGGER parity_tax_wl_drop_new AFTER INSERT ON targets FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_drop_new();
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON watchlist_screens;
CREATE TRIGGER parity_tax_wl_drop_new AFTER INSERT ON watchlist_screens FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_drop_new();
DROP TRIGGER IF EXISTS parity_tax_wl_drop_new ON watchlist_screen_evaluations;
CREATE TRIGGER parity_tax_wl_drop_new AFTER INSERT ON watchlist_screen_evaluations FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_drop_new();

DROP TRIGGER IF EXISTS parity_tax_wl_keep ON watchlist_lists;
CREATE TRIGGER parity_tax_wl_keep BEFORE DELETE ON watchlist_lists FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_keep();
DROP TRIGGER IF EXISTS parity_tax_wl_keep ON watchlist_items;
CREATE TRIGGER parity_tax_wl_keep BEFORE DELETE ON watchlist_items FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_keep();
DROP TRIGGER IF EXISTS parity_tax_wl_keep ON targets;
CREATE TRIGGER parity_tax_wl_keep BEFORE DELETE ON targets FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_keep();
DROP TRIGGER IF EXISTS parity_tax_wl_keep ON watchlist_screens;
CREATE TRIGGER parity_tax_wl_keep BEFORE DELETE ON watchlist_screens FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_keep();
DROP TRIGGER IF EXISTS parity_tax_wl_keep ON holding_risk_profiles;
CREATE TRIGGER parity_tax_wl_keep BEFORE DELETE ON holding_risk_profiles FOR EACH ROW EXECUTE FUNCTION parity_tax_wl_keep();

COMMIT;
