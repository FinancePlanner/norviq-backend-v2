-- Fixtures for parity/expenses-budgeting*.jsonl (BudgetingEngineController).
-- Idempotent: run before every parity pass against the scratch database
-- (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/expenses-budgeting-setup.sql
--
-- Users e5c00000-0000-4000-8000-00000000000N:
--   1 free (no trial)       2 pro via a long trial (reads, previews)
--   3 pro (mutating cases)
-- Mutating cases run twice (Vapor, then v2). To keep them repeatable, the
-- triggers below hold the revision of the snapshots they touch at 0,
-- replace a same-named parity template on insert, and put a deleted parity
-- template straight back. They only fire for rows of these users.

BEGIN;

DELETE FROM users WHERE id::text LIKE 'e5c00000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, had_trial, trial_started_at, trial_days, trial_tier) VALUES
  ('e5c00000-0000-4000-8000-000000000001', 'parity-expenses-budgeting-free@example.com', 'x', now(), true, true, NULL, NULL, NULL),
  ('e5c00000-0000-4000-8000-000000000002', 'parity-expenses-budgeting-pro@example.com', 'x', now(), true, false, now() - interval '1 day', 3650, 'temporary'),
  ('e5c00000-0000-4000-8000-000000000003', 'parity-expenses-budgeting-writer@example.com', 'x', now(), true, false, now() - interval '1 day', 3650, 'temporary');

INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES
  ('e5c00000-0000-4000-8000-0000000000a2', 'e5c00000-0000-4000-8000-000000000002', 'Main', now()),
  ('e5c00000-0000-4000-8000-0000000000a3', 'e5c00000-0000-4000-8000-000000000003', 'Main', now());

INSERT INTO expense_categories (id, user_id, name, pillar, is_default, created_at) VALUES
  ('e5c00000-0000-4000-8000-0000000000c1', 'e5c00000-0000-4000-8000-000000000002', 'Food', 'fundamentals', false, now()),
  ('e5c00000-0000-4000-8000-0000000000c2', 'e5c00000-0000-4000-8000-000000000002', 'Misc', NULL, false, now());

-- User 2: July (drift, previews), June (discipline), a policy snapshot.
INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, currency_code, created_at, updated_at) VALUES
  ('e5c00000-0000-4000-8000-0000000000d1', 'e5c00000-0000-4000-8000-000000000002', '2026-07-01', 4000, '{"fundamentals": 0.5, "fun": 0.3}', 'EUR', now(), now()),
  ('e5c00000-0000-4000-8000-0000000000d2', 'e5c00000-0000-4000-8000-000000000002', '2026-06-01', 4000, '{}', 'EUR', now(), now()),
  ('e5c00000-0000-4000-8000-0000000000d4', 'e5c00000-0000-4000-8000-000000000002', '2025-01-01', 3000, '{"fun": 1}', 'USD', now(), now());

INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, category_id, threshold_override, allocation_kind, reallocation_eligible, created_at, updated_at) VALUES
  ('e5c00000-0000-4000-8000-0000000000e1', 'e5c00000-0000-4000-8000-0000000000d1', 'e5c00000-0000-4000-8000-000000000002', 'Dining Out', 150, 'fun', NULL, 15, 'expense', true, now(), now()),
  ('e5c00000-0000-4000-8000-0000000000e2', 'e5c00000-0000-4000-8000-0000000000d1', 'e5c00000-0000-4000-8000-000000000002', 'Monthly Investments', 500, 'futureYou', NULL, NULL, 'investmentContribution', false, now(), now()),
  ('e5c00000-0000-4000-8000-0000000000e3', 'e5c00000-0000-4000-8000-0000000000d1', 'e5c00000-0000-4000-8000-000000000002', 'Groceries', 400, 'fundamentals', 'e5c00000-0000-4000-8000-0000000000c1', NULL, 'expense', false, now(), now()),
  ('e5c00000-0000-4000-8000-0000000000e4', 'e5c00000-0000-4000-8000-0000000000d2', 'e5c00000-0000-4000-8000-000000000002', 'Dining Out', 150, 'fun', NULL, NULL, 'expense', false, now(), now());

INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, category_id, created_at, updated_at) VALUES
  ('e5c00000-0000-4000-8000-0000000000f1', 'e5c00000-0000-4000-8000-000000000002', 'Dining Out', 210, 'fun', '2026-07-15', NULL, now(), now()),
  ('e5c00000-0000-4000-8000-0000000000f2', 'e5c00000-0000-4000-8000-000000000002', 'Supermarket', 120.4, 'fundamentals', '2026-07-03', 'e5c00000-0000-4000-8000-0000000000c1', now(), now()),
  ('e5c00000-0000-4000-8000-0000000000f3', 'e5c00000-0000-4000-8000-000000000002', 'Gadget', 33, 'fun', '2026-07-09', 'e5c00000-0000-4000-8000-0000000000c2', now(), now()),
  ('e5c00000-0000-4000-8000-0000000000f4', 'e5c00000-0000-4000-8000-000000000002', '  coffee beans ', 12.5, 'fun', '2026-07-21', NULL, now(), now()),
  ('e5c00000-0000-4000-8000-0000000000f5', 'e5c00000-0000-4000-8000-000000000002', 'Dining Out', 90, 'fun', '2026-06-11', NULL, now(), now());

INSERT INTO budget_templates (id, user_id, name, items, created_at, updated_at) VALUES
  ('e5c00000-0000-4000-8000-0000000000b1', 'e5c00000-0000-4000-8000-000000000002', 'Balanced',
   '{"values": [{"title": "Groceries", "plannedAmount": 400, "pillar": "fundamentals", "targetType": "fixed", "allocationKind": "expense", "reallocationEligible": true}]}',
   '2026-07-01 10:00:00+00', '2026-07-01 10:00:00+00'),
  ('e5c00000-0000-4000-8000-0000000000b2', 'e5c00000-0000-4000-8000-000000000002', 'Legacy',
   '[{"title": "Rent", "plannedAmount": 900, "pillar": "fundamentals", "categoryId": "E5C00000-0000-4000-8000-0000000000C1", "targetType": "percentageIncome", "incomePercentage": 25, "allocationKind": "expense", "reallocationEligible": false}]',
   '2026-07-02 10:00:00+00', NULL);

INSERT INTO budget_reallocation_events (id, user_id, request_id, source_snapshot_id, target_snapshot_id, effective_month, freed_capital, financial_goal_id, portfolio_list_id, adjustments, created_at) VALUES
  ('e5c00000-0000-4000-8000-000000000101', 'e5c00000-0000-4000-8000-000000000002', 'e5c00000-0000-4000-8000-000000000102',
   'e5c00000-0000-4000-8000-0000000000d2', 'e5c00000-0000-4000-8000-0000000000d1', '2026-07-01', 25, NULL,
   'e5c00000-0000-4000-8000-0000000000a2', '{"values": [{"planItemId": "E5C00000-0000-4000-8000-0000000000E4", "amount": 25}]}', '2026-06-20 08:00:00+00');

-- User 3: commit source (March, no April), apply target, bulk target.
INSERT INTO budget_snapshots (id, user_id, month_start, net_salary, target_shares, currency_code, created_at, updated_at) VALUES
  ('e5c00000-0000-4000-8000-0000000003d1', 'e5c00000-0000-4000-8000-000000000003', '2026-03-01', 3000, '{}', 'EUR', now(), now()),
  ('e5c00000-0000-4000-8000-0000000003d2', 'e5c00000-0000-4000-8000-000000000003', '2026-09-01', 3000, '{}', 'EUR', now(), now()),
  ('e5c00000-0000-4000-8000-0000000003d3', 'e5c00000-0000-4000-8000-000000000003', '2026-10-01', 3000, '{}', 'EUR', now(), now());

INSERT INTO budget_plan_items (id, snapshot_id, user_id, title, planned_amount, pillar, allocation_kind, reallocation_eligible, created_at, updated_at) VALUES
  ('e5c00000-0000-4000-8000-0000000003e1', 'e5c00000-0000-4000-8000-0000000003d1', 'e5c00000-0000-4000-8000-000000000003', 'Fun', 200, 'fun', 'expense', true, now(), now()),
  ('e5c00000-0000-4000-8000-0000000003e2', 'e5c00000-0000-4000-8000-0000000003d2', 'e5c00000-0000-4000-8000-000000000003', 'Old target', 10, 'fun', 'expense', false, now(), now()),
  ('e5c00000-0000-4000-8000-0000000003e3', 'e5c00000-0000-4000-8000-0000000003d3', 'e5c00000-0000-4000-8000-000000000003', 'Dining Out', 300, 'fun', 'expense', false, now(), now());

INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at, updated_at) VALUES
  ('e5c00000-0000-4000-8000-0000000003f1', 'e5c00000-0000-4000-8000-000000000003', 'Dining Out', 210, 'fun', '2026-10-12', now(), now());

INSERT INTO budget_templates (id, user_id, name, items, created_at, updated_at) VALUES
  ('e5c00000-0000-4000-8000-0000000003b1', 'e5c00000-0000-4000-8000-000000000003', 'Writer',
   '{"values": [{"title": "Groceries", "plannedAmount": 400, "pillar": "fundamentals", "targetType": "fixed", "allocationKind": "expense", "reallocationEligible": true}, {"title": "Invest", "plannedAmount": 0, "pillar": "futureYou", "targetType": "percentageIncome", "incomePercentage": 10, "allocationKind": "investmentContribution", "reallocationEligible": false}]}',
   '2026-07-01 10:00:00+00', '2026-07-01 10:00:00+00'),
  ('e5c00000-0000-4000-8000-0000000003b2', 'e5c00000-0000-4000-8000-000000000003', 'Disposable',
   '{"values": [{"title": "X", "plannedAmount": 1, "pillar": "fun", "targetType": "fixed", "allocationKind": "expense", "reallocationEligible": false}]}',
   '2026-07-01 10:00:00+00', '2026-07-01 10:00:00+00');

CREATE OR REPLACE FUNCTION parity_budget_hold_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.user_id::text LIKE 'e5c00000-0000-4000-8000-%' AND NEW.id::text LIKE 'e5c00000-%' AND NEW.id <> 'e5c00000-0000-4000-8000-0000000003d1' THEN
    NEW.revision := 0;
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS parity_budget_hold_revision ON budget_snapshots;
CREATE TRIGGER parity_budget_hold_revision BEFORE UPDATE ON budget_snapshots
  FOR EACH ROW EXECUTE FUNCTION parity_budget_hold_revision();

CREATE OR REPLACE FUNCTION parity_budget_template_replace() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.user_id::text LIKE 'e5c00000-0000-4000-8000-%' THEN
    DELETE FROM budget_templates WHERE user_id = NEW.user_id AND name = NEW.name;
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS parity_budget_template_replace ON budget_templates;
CREATE TRIGGER parity_budget_template_replace BEFORE INSERT ON budget_templates
  FOR EACH ROW EXECUTE FUNCTION parity_budget_template_replace();

CREATE OR REPLACE FUNCTION parity_budget_template_restore() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.id = 'e5c00000-0000-4000-8000-0000000003b2' AND EXISTS (SELECT 1 FROM users WHERE id = OLD.user_id) THEN
    INSERT INTO budget_templates SELECT OLD.*;
  END IF;
  RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS parity_budget_template_restore ON budget_templates;
CREATE TRIGGER parity_budget_template_restore AFTER DELETE ON budget_templates
  FOR EACH ROW EXECUTE FUNCTION parity_budget_template_restore();

COMMIT;
