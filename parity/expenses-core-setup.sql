-- Fixtures for parity/expenses-core.jsonl. Idempotent: run before every
-- parity pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/expenses-core-setup.sql
--
-- Users are e5d00000-0000-4000-8000-00000000000N:
--   1 free, with two months of budget data
--   2 pro (lifetime entitlement), with recurring templates
--   3 free, empty (create/validation cases)
--   4 pro, household partner cases
-- Mutating cases run twice (Vapor, then v2), so a trigger keeps every
-- fixture row (fixed id e5d00000-...) from being deleted: the DELETE cases
-- answer the same on both backends. Rows the cases create (random ids) are
-- removed on the next run by user id.

BEGIN;

DROP TRIGGER IF EXISTS parity_expenses_core_keep ON expenses;
DROP TRIGGER IF EXISTS parity_expenses_core_keep ON budget_snapshots;
DROP TRIGGER IF EXISTS parity_expenses_core_keep ON budget_plan_items;
DROP TRIGGER IF EXISTS parity_expenses_core_keep ON expense_categories;
DROP TRIGGER IF EXISTS parity_expenses_core_keep ON recurring_templates;

DELETE FROM report_suggestion_dismissals WHERE user_id::text LIKE 'e5d00000-%';
DELETE FROM expenses WHERE user_id::text LIKE 'e5d00000-%';
DELETE FROM budget_plan_items WHERE user_id::text LIKE 'e5d00000-%';
DELETE FROM budget_snapshots WHERE user_id::text LIKE 'e5d00000-%';
DELETE FROM recurring_templates WHERE user_id::text LIKE 'e5d00000-%';
DELETE FROM expense_categories WHERE user_id::text LIKE 'e5d00000-%';
DELETE FROM entitlements WHERE user_id::text LIKE 'e5d00000-%';
DELETE FROM users WHERE id::text LIKE 'e5d00000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('e5d00000-0000-4000-8000-000000000001', 'parity-expenses-core-free@example.com', 'x', now(), true),
  ('e5d00000-0000-4000-8000-000000000002', 'parity-expenses-core-pro@example.com', 'x', now(), true),
  ('e5d00000-0000-4000-8000-000000000003', 'parity-expenses-core-empty@example.com', 'x', now(), true),
  ('e5d00000-0000-4000-8000-000000000004', 'parity-expenses-core-household@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES
  ('e5d00000-0000-4000-8000-000000000e02', 'e5d00000-0000-4000-8000-000000000002', 'pro', NULL, now(), now()),
  ('e5d00000-0000-4000-8000-000000000e04', 'e5d00000-0000-4000-8000-000000000004', 'pro', NULL, now(), now());

INSERT INTO expense_categories (id, user_id, name, pillar, is_default, created_at) VALUES
  ('e5d00000-0000-4000-8000-000000000101', 'e5d00000-0000-4000-8000-000000000001', 'Groceries', 'fundamentals', true, '2026-08-01T00:00:00Z'),
  ('e5d00000-0000-4000-8000-000000000102', 'e5d00000-0000-4000-8000-000000000001', 'Pets', 'fun', false, '2026-08-01T00:00:00Z'),
  ('e5d00000-0000-4000-8000-000000000103', 'e5d00000-0000-4000-8000-000000000001', 'Dining', 'fun', false, '2026-08-01T00:00:00Z'),
  ('e5d00000-0000-4000-8000-000000000104', 'e5d00000-0000-4000-8000-000000000002', 'Streaming', 'fun', false, '2026-08-01T00:00:00Z');

INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, created_at, updated_at,
    currency_code, category_drift_threshold, total_drift_threshold, alerts_enabled, alert_on_unbudgeted, revision) VALUES
  ('e5d00000-0000-4000-8000-000000000201', 'e5d00000-0000-4000-8000-000000000001', '2026-09-01', 3000,
    '{"fundamentals":0.5,"futureYou":0.3,"fun":0.2}', '2026-09-01T08:00:00.250Z', '2026-09-01T08:00:00.250Z', 'EUR', 15, 10, true, true, 0),
  ('e5d00000-0000-4000-8000-000000000202', 'e5d00000-0000-4000-8000-000000000001', '2026-07-01', 2800,
    '{"fundamentals":0.6,"futureYou":0.2,"fun":0.2}', '2026-07-01T08:00:00.250Z', '2026-07-01T08:00:00.250Z', 'EUR', 20, 8, false, false, 0);

INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, created_at, updated_at,
    split_mode, user_share_percent, category_id, target_type, income_percentage, threshold_override, allocation_kind, reallocation_eligible) VALUES
  ('e5d00000-0000-4000-8000-000000000301', 'e5d00000-0000-4000-8000-000000000201', 'e5d00000-0000-4000-8000-000000000001',
    'Core Living', 1000, 'fundamentals', '2026-09-01T08:00:00Z', '2026-09-01T08:00:00Z', 'shared', 60, 'e5d00000-0000-4000-8000-000000000101', 'fixed', NULL, NULL, 'expense', false),
  ('e5d00000-0000-4000-8000-000000000302', 'e5d00000-0000-4000-8000-000000000201', 'e5d00000-0000-4000-8000-000000000001',
    'Index fund', 0, 'futureYou', '2026-09-01T08:00:00Z', '2026-09-01T08:00:00Z', 'personal', 100, NULL, 'percentageIncome', 10, 3, 'investmentContribution', true),
  ('e5d00000-0000-4000-8000-000000000303', 'e5d00000-0000-4000-8000-000000000202', 'e5d00000-0000-4000-8000-000000000001',
    'Old rent', 900, 'fundamentals', '2026-07-01T08:00:00Z', '2026-07-01T08:00:00Z', 'personal', 100, NULL, 'fixed', NULL, NULL, 'expense', false);

INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, linked_item_id, created_at, updated_at,
    split_mode, user_share_percent, category_id, foreign_amount, foreign_currency, exchange_rate, notes, receipt_metadata) VALUES
  ('e5d00000-0000-4000-8000-000000000401', 'e5d00000-0000-4000-8000-000000000001', 'Core Living', 1400, 'fundamentals', '2026-09-05',
    'e5d00000-0000-4000-8000-000000000301', '2026-09-05T10:00:00.500Z', '2026-09-05T10:00:00.500Z', 'shared', 60, 'e5d00000-0000-4000-8000-000000000101', NULL, NULL, NULL, 'rent + bills', NULL),
  ('e5d00000-0000-4000-8000-000000000402', 'e5d00000-0000-4000-8000-000000000001', 'Unplanned Weekend', 350, 'fun', '2026-09-12',
    NULL, '2026-09-12T10:00:00.500Z', '2026-09-12T10:00:00.500Z', 'personal', 100, 'e5d00000-0000-4000-8000-000000000103', 80, 'GBP', 1.17, NULL,
    '{"source":"ocr","merchant":"Pub","total":80,"currency":"GBP","issuedOn":"2026-09-12"}'),
  ('e5d00000-0000-4000-8000-000000000403', 'e5d00000-0000-4000-8000-000000000001', 'Vet', 25.5, 'fun', '2026-07-20',
    NULL, '2026-07-20T10:00:00.500Z', '2026-07-20T10:00:00.500Z', 'personal', 100, 'e5d00000-0000-4000-8000-000000000102', NULL, NULL, NULL, NULL, NULL),
  ('e5d00000-0000-4000-8000-000000000404', 'e5d00000-0000-4000-8000-000000000001', 'Coffee, "large"', 4.5, 'fun', '2026-07-21',
    NULL, '2026-07-21T10:00:00.500Z', '2026-07-21T10:00:00.500Z', 'personal', 100, NULL, NULL, NULL, NULL, NULL, NULL);

INSERT INTO recurring_templates (id, user_id, title, amount, pillar, category_id, frequency, split_mode, user_share_percent, created_at) VALUES
  ('e5d00000-0000-4000-8000-000000000501', 'e5d00000-0000-4000-8000-000000000002', 'Gym', 40, 'fun', 'e5d00000-0000-4000-8000-000000000104', 'monthly', 'personal', 100, '2026-08-01T00:00:00.125Z'),
  ('e5d00000-0000-4000-8000-000000000502', 'e5d00000-0000-4000-8000-000000000002', 'Rent', 1200, 'fundamentals', NULL, 'monthly', 'shared', 50, '2026-08-02T00:00:00.125Z');

CREATE OR REPLACE FUNCTION parity_expenses_core_keep() RETURNS trigger AS $$
BEGIN
  IF OLD.id::text LIKE 'e5d00000-%' THEN
    RETURN NULL;
  END IF;
  RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER parity_expenses_core_keep BEFORE DELETE ON expenses FOR EACH ROW EXECUTE FUNCTION parity_expenses_core_keep();
CREATE TRIGGER parity_expenses_core_keep BEFORE DELETE ON budget_snapshots FOR EACH ROW EXECUTE FUNCTION parity_expenses_core_keep();
CREATE TRIGGER parity_expenses_core_keep BEFORE DELETE ON budget_plan_items FOR EACH ROW EXECUTE FUNCTION parity_expenses_core_keep();
CREATE TRIGGER parity_expenses_core_keep BEFORE DELETE ON expense_categories FOR EACH ROW EXECUTE FUNCTION parity_expenses_core_keep();
CREATE TRIGGER parity_expenses_core_keep BEFORE DELETE ON recurring_templates FOR EACH ROW EXECUTE FUNCTION parity_expenses_core_keep();

COMMIT;
