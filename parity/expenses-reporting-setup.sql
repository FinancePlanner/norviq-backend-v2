-- Fixtures for parity/expenses-reporting*.jsonl (advanced reporting).
-- Idempotent; run before every parity pass against the scratch database
-- (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/expenses-reporting-setup.sql
--
-- Users e5b00000-0000-4000-8000-00000000000N:
--   01 pro reader (all fixtures), 02 free, 03 pro member of portfolio a1,
--   04 unverified, 05 pro run creator, 06 pro at the daily file limit,
--   07 pro template/schedule writer.
-- Triggers keep the mutating cases repeatable when they run twice (Vapor,
-- then v2): template b3's revision and template b4's archived_at never
-- change, and schedule c3 cannot be deleted.
-- No fixture run is pending and no schedule is due, so neither backend's
-- worker touches them; artifact e1 expires in an hour so the signed link's
-- expiry (min(artifact, now + 24h)) is deterministic.

BEGIN;

DROP TRIGGER IF EXISTS parity_reporting_pin ON advanced_report_templates;
DROP TRIGGER IF EXISTS parity_reporting_pin ON advanced_report_schedules;
DELETE FROM advanced_report_runs WHERE requested_by_user_id::text LIKE 'e5b00000-%';
DELETE FROM advanced_report_schedules WHERE owner_user_id::text LIKE 'e5b00000-%';
DELETE FROM advanced_report_templates WHERE owner_user_id::text LIKE 'e5b00000-%';
DELETE FROM users WHERE id::text LIKE 'e5b00000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES
  ('e5b00000-0000-4000-8000-000000000001', 'parity-expenses-reporting-1@example.com', 'x', now(), true),
  ('e5b00000-0000-4000-8000-000000000002', 'parity-expenses-reporting-2@example.com', 'x', now(), true),
  ('e5b00000-0000-4000-8000-000000000003', 'parity-expenses-reporting-3@example.com', 'x', now(), true),
  ('e5b00000-0000-4000-8000-000000000004', 'parity-expenses-reporting-4@example.com', 'x', now(), false),
  ('e5b00000-0000-4000-8000-000000000005', 'parity-expenses-reporting-5@example.com', 'x', now(), true),
  ('e5b00000-0000-4000-8000-000000000006', 'parity-expenses-reporting-6@example.com', 'x', now(), true),
  ('e5b00000-0000-4000-8000-000000000007', 'parity-expenses-reporting-7@example.com', 'x', now(), true);

INSERT INTO entitlements (id, user_id, level, created_at, updated_at)
SELECT gen_random_uuid(), u, 'pro', now(), now()
FROM unnest(ARRAY['e5b00000-0000-4000-8000-000000000001', 'e5b00000-0000-4000-8000-000000000003',
                  'e5b00000-0000-4000-8000-000000000005', 'e5b00000-0000-4000-8000-000000000006',
                  'e5b00000-0000-4000-8000-000000000007']::uuid[]) AS u;

INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES
  ('e5b00000-0000-4000-8000-0000000000a1', 'e5b00000-0000-4000-8000-000000000001', 'Parity Main', now()),
  ('e5b00000-0000-4000-8000-0000000000a2', 'e5b00000-0000-4000-8000-000000000002', 'Parity Other', now());
INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, joined_at, created_at, updated_at) VALUES
  ('e5b00000-0000-4000-8000-0000000000aa', 'e5b00000-0000-4000-8000-0000000000a1', 'e5b00000-0000-4000-8000-000000000003', 'editor', 'active', now(), now(), now());

-- Template inputs (camelCase, as JSONEncoder.backendAPI stored them).
CREATE TEMP TABLE parity_tpl (input text) ON COMMIT DROP;
INSERT INTO parity_tpl VALUES ('{"name":"Parity review","description":"Fixture","theme":"midnight","pageSettings":{"size":"letter","orientation":"landscape","showPageNumbers":false,"showGeneratedAt":true},"blocks":[{"id":"h","kind":"holdings","title":"Holdings","commentary":"c","portfolioIds":["E5B00000-0000-4000-8000-0000000000A1"],"dateRange":{"preset":"custom","from":"2026-01-01","to":"2026-06-30"},"settings":{"visualization":"table","showLegend":false,"showDetails":true,"showSourceURLs":false,"maximumRows":25,"benchmarkSymbol":"SPY"},"isVisible":true,"pageBreakBefore":false},{"id":"t","kind":"custom_text","title":"Notes","portfolioIds":[],"settings":{"visualization":"automatic","showLegend":true,"showDetails":true,"showSourceURLs":true,"customText":"Hello <b>"},"isVisible":false,"pageBreakBefore":true}]}');

INSERT INTO advanced_report_templates (id, owner_user_id, input_json, revision, is_starter_template, archived_at, created_at, updated_at)
SELECT v.id::uuid, v.owner::uuid, (SELECT input FROM parity_tpl), v.rev, v.starter, v.archived::timestamptz,
       '2026-10-01 12:00:00.123789+00', v.updated::timestamptz
FROM (VALUES
  ('e5b00000-0000-4000-8000-0000000000b1', 'e5b00000-0000-4000-8000-000000000001', 3, false, NULL, '2026-10-02 08:30:00.999999+00'),
  ('e5b00000-0000-4000-8000-0000000000b2', 'e5b00000-0000-4000-8000-000000000001', 1, true, '2026-10-03 09:00:00.5+00', '2026-10-03 09:00:00.5+00'),
  ('e5b00000-0000-4000-8000-0000000000b3', 'e5b00000-0000-4000-8000-000000000007', 1, false, NULL, NULL),
  ('e5b00000-0000-4000-8000-0000000000b4', 'e5b00000-0000-4000-8000-000000000007', 1, false, NULL, NULL),
  ('e5b00000-0000-4000-8000-0000000000b5', 'e5b00000-0000-4000-8000-000000000005', 1, false, NULL, NULL),
  ('e5b00000-0000-4000-8000-0000000000b6', 'e5b00000-0000-4000-8000-000000000006', 1, false, NULL, NULL),
  ('e5b00000-0000-4000-8000-0000000000b7', 'e5b00000-0000-4000-8000-000000000003', 1, false, NULL, NULL)
) AS v(id, owner, rev, starter, archived, updated);

INSERT INTO advanced_report_schedules (id, owner_user_id, input_json, next_run_at, last_run_at, paused_reason, created_at, updated_at) VALUES
  ('e5b00000-0000-4000-8000-0000000000c1', 'e5b00000-0000-4000-8000-000000000001',
   '{"name":"Weekly","templateId":"E5B00000-0000-4000-8000-0000000000B1","outputFormats":["pdf","xlsx"],"recurrence":{"frequency":"weekly","timeZone":"Europe/Lisbon","localTime":"09:30","weekday":2},"recipientUserIds":["E5B00000-0000-4000-8000-000000000003"],"isEnabled":true}',
   '2030-01-07 09:30:00.250001+00', '2026-10-05 09:30:00+00', NULL, '2026-10-01 12:00:00+00', '2026-10-05 09:30:00+00'),
  ('e5b00000-0000-4000-8000-0000000000c2', 'e5b00000-0000-4000-8000-000000000007',
   '{"name":"Old","templateId":"E5B00000-0000-4000-8000-0000000000B3","outputFormats":["pdf"],"recurrence":{"frequency":"monthly","timeZone":"UTC","localTime":"08:00","dayOfMonth":1},"recipientUserIds":[],"isEnabled":false}',
   NULL, NULL, 'disabled_by_user', '2026-10-01 12:00:00+00', '2026-10-01 12:00:00+00'),
  ('e5b00000-0000-4000-8000-0000000000c3', 'e5b00000-0000-4000-8000-000000000007',
   '{"name":"Undeletable","templateId":"E5B00000-0000-4000-8000-0000000000B4","outputFormats":["xlsx"],"recurrence":{"frequency":"yearly","timeZone":"UTC","localTime":"08:00","dayOfMonth":15,"anchorMonth":4},"recipientUserIds":[],"isEnabled":false}',
   NULL, NULL, 'disabled_by_user', '2026-10-01 12:00:00+00', '2026-10-01 12:00:00+00');

-- A delivered run with two artifacts (one expired) and one delivery.
INSERT INTO advanced_report_runs (id, template_id, schedule_id, requested_by_user_id, template_revision, template_input_json,
  output_formats_json, recipient_user_ids_json, status, scheduled_for, claimed_at, started_at, completed_at,
  failure_reason, attempt_count, retry_at, created_at, updated_at) VALUES
  ('e5b00000-0000-4000-8000-0000000000d1', 'e5b00000-0000-4000-8000-0000000000b1', 'e5b00000-0000-4000-8000-0000000000c1',
   'e5b00000-0000-4000-8000-000000000001', 3, (SELECT input FROM parity_tpl), '["pdf","xlsx"]',
   '["E5B00000-0000-4000-8000-000000000001"]', 'delivered', '2026-10-05 09:30:00+00', NULL,
   '2026-10-05 09:30:01.000500+00', '2026-10-05 09:30:09.987654+00', NULL, 1, NULL, now() - interval '1 hour', now()),
  ('e5b00000-0000-4000-8000-0000000000d2', 'e5b00000-0000-4000-8000-0000000000b1', NULL,
   'e5b00000-0000-4000-8000-000000000001', 3, (SELECT input FROM parity_tpl), '["xlsx"]',
   '[]', 'mystery_status', NULL, NULL, NULL, '2026-10-06 10:00:00+00', 'Abort.503: PDF renderer is unavailable.', 3, NULL,
   now() - interval '2 hours', now());
INSERT INTO advanced_report_artifacts (id, run_id, format, filename, content_type, size_bytes, sha256, storage_key, expires_at, created_at) VALUES
  ('e5b00000-0000-4000-8000-0000000000e1', 'e5b00000-0000-4000-8000-0000000000d1', 'pdf', 'parity-"review"-1.pdf', 'application/pdf',
   17, 'ab', 'e5b00000-0000-4000-8000-0000000000e1.pdf', date_trunc('second', now() + interval '1 hour') + interval '0.123789 seconds', '2026-10-05 09:30:05+00'),
  ('e5b00000-0000-4000-8000-0000000000e2', 'e5b00000-0000-4000-8000-0000000000d1', 'xlsx', 'parity-review-1.xlsx',
   'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', 1234, 'cd', 'e5b00000-0000-4000-8000-0000000000e2.xlsx',
   '2020-01-01 00:00:00+00', '2026-10-05 09:30:06+00');
INSERT INTO advanced_report_deliveries (id, run_id, recipient_user_id, recipient_email, status, attempt_count, last_attempt_at,
  delivered_at, failure_reason, link_expires_at, created_at, updated_at) VALUES
  ('e5b00000-0000-4000-8000-0000000000f1', 'e5b00000-0000-4000-8000-0000000000d1', 'e5b00000-0000-4000-8000-000000000001',
   'parity-expenses-reporting-1@example.com', 'delivered', 1, '2026-10-05 09:30:08+00', '2026-10-05 09:30:09+00', NULL,
   '2026-10-12 09:30:08+00', '2026-10-05 09:30:07+00', now());

-- b6 reads no portfolio, so user 06's run reaches the daily-limit check.
UPDATE advanced_report_templates
SET input_json = replace(input_json, '"E5B00000-0000-4000-8000-0000000000A1"', '')
WHERE id = 'e5b00000-0000-4000-8000-0000000000b6';

-- User 06 already generated 100 files today (fifty two-format runs).
INSERT INTO advanced_report_runs (id, template_id, requested_by_user_id, template_revision, template_input_json,
  output_formats_json, recipient_user_ids_json, status, attempt_count, created_at, updated_at)
SELECT gen_random_uuid(), 'e5b00000-0000-4000-8000-0000000000b6', 'e5b00000-0000-4000-8000-000000000006', 1,
       (SELECT input FROM parity_tpl), '["pdf","xlsx"]', '[]', 'delivered', 1, now(), now()
FROM generate_series(1, 50);

CREATE OR REPLACE FUNCTION parity_reporting_pin() RETURNS trigger AS $$
BEGIN
  IF TG_TABLE_NAME = 'advanced_report_templates' THEN
    IF NEW.id = 'e5b00000-0000-4000-8000-0000000000b3' THEN NEW.revision := 1; END IF;
    IF NEW.id = 'e5b00000-0000-4000-8000-0000000000b4' THEN NEW.archived_at := NULL; END IF;
    RETURN NEW;
  END IF;
  IF TG_OP = 'DELETE' AND OLD.id = 'e5b00000-0000-4000-8000-0000000000c3' THEN RETURN NULL; END IF;
  RETURN OLD;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER parity_reporting_pin BEFORE UPDATE ON advanced_report_templates FOR EACH ROW EXECUTE FUNCTION parity_reporting_pin();
CREATE TRIGGER parity_reporting_pin BEFORE DELETE ON advanced_report_schedules FOR EACH ROW EXECUTE FUNCTION parity_reporting_pin();

COMMIT;
