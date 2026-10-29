-- Fixtures for parity/portfolio-mgmt*.jsonl. Idempotent: run before every
-- parity pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/portfolio-mgmt-setup.sql
--
-- Ids are 6d676d74-0000-4000-8000-* ("mgmt"). Users:
--   …01 free owner of a1 (default)
--   …02 pro owner of b1 (default, actual), b2 (hypothetical sandbox),
--       b3 (to archive), b4 (to delete), b5 (pilot-followed)
--   …03 active editor of b1 (m1)      …06 active editor of b1 (m2, revoked by a case)
--   …04 invitee of i3/i4 on b2        …05 stranger with its own default c1
-- Mutating cases run twice (Vapor, then v2). Triggers named parity_mgmt_*
-- undo exactly the fixture rows those cases change, so both backends see the
-- same state: an earlier list a case created is renamed aside before the
-- next insert of the same name (names are unique per user, so the second
-- backend would hit a 500), b4 and cash x2
-- come back after DELETE, m1/m2 return to active,
-- i3/i4 return to pending, and invitations for invitee@ vanish after insert.

BEGIN;

DROP TRIGGER IF EXISTS parity_mgmt_list_restore ON portfolio_lists;
DROP TRIGGER IF EXISTS parity_mgmt_cash_restore ON portfolio_cash_positions;
DROP TRIGGER IF EXISTS parity_mgmt_member_restore ON portfolio_memberships;
DROP TRIGGER IF EXISTS parity_mgmt_invite_restore ON portfolio_invitations;
DROP TRIGGER IF EXISTS parity_mgmt_invite_drop ON portfolio_invitations;
DROP TRIGGER IF EXISTS parity_mgmt_list_rename ON portfolio_lists;

DELETE FROM stocks WHERE user_id::text LIKE '6d676d74-0000-4000-8000-%';
DELETE FROM accounts WHERE user_id::text LIKE '6d676d74-0000-4000-8000-%';
DELETE FROM pilot_follows WHERE user_id::text LIKE '6d676d74-0000-4000-8000-%';
DELETE FROM pilots WHERE slug = 'parity-mgmt-pilot';
DELETE FROM portfolio_lists WHERE user_id::text LIKE '6d676d74-0000-4000-8000-%';
DELETE FROM users WHERE id::text LIKE '6d676d74-0000-4000-8000-%';

INSERT INTO users (id, email, username, password_hash, created_at, is_verified) VALUES
  ('6d676d74-0000-4000-8000-000000000001', 'parity-portfolio-mgmt-free@example.com', NULL, 'x', '2026-01-01T00:00:00Z', true),
  ('6d676d74-0000-4000-8000-000000000002', 'parity-portfolio-mgmt-pro@example.com', 'mgmtpro', 'x', '2026-01-01T00:00:00Z', true),
  ('6d676d74-0000-4000-8000-000000000003', 'parity-portfolio-mgmt-editor@example.com', 'mgmteditor', 'x', '2026-01-01T00:00:00Z', true),
  ('6d676d74-0000-4000-8000-000000000004', 'parity-portfolio-mgmt-invitee@example.com', NULL, 'x', '2026-01-01T00:00:00Z', true),
  ('6d676d74-0000-4000-8000-000000000005', 'parity-portfolio-mgmt-stranger@example.com', NULL, 'x', '2026-01-01T00:00:00Z', true),
  ('6d676d74-0000-4000-8000-000000000006', 'parity-portfolio-mgmt-editor2@example.com', NULL, 'x', '2026-01-01T00:00:00Z', true);

INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES
  ('6d676d74-0000-4000-8000-0000000000e2', '6d676d74-0000-4000-8000-000000000002', 'pro', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');

INSERT INTO portfolio_lists (id, user_id, name, is_default, purpose, ownership, mode, base_currency, created_at, updated_at, archived_at) VALUES
  ('6d676d74-0000-4000-8000-0000000001a1', '6d676d74-0000-4000-8000-000000000001', 'Main', true, 'personal', 'individual', 'actual', 'USD', '2026-02-01T10:00:00.123Z', '2026-02-01T10:00:00.123Z', NULL),
  ('6d676d74-0000-4000-8000-0000000001b1', '6d676d74-0000-4000-8000-000000000002', 'Main', true, 'personal', 'joint', 'actual', 'USD', '2026-02-01T10:00:00Z', '2026-02-02T10:00:00Z', NULL),
  ('6d676d74-0000-4000-8000-0000000001b2', '6d676d74-0000-4000-8000-000000000002', 'Sandbox', false, 'retirement', 'individual', 'hypothetical', 'EUR', '2026-02-03T10:00:00Z', NULL, NULL),
  ('6d676d74-0000-4000-8000-0000000001b3', '6d676d74-0000-4000-8000-000000000002', 'Old', false, 'personal', 'individual', 'actual', 'USD', '2026-02-04T10:00:00Z', NULL, '2026-03-01T00:00:00Z'),
  ('6d676d74-0000-4000-8000-0000000001b4', '6d676d74-0000-4000-8000-000000000002', 'Doomed', false, 'personal', 'individual', 'actual', 'USD', '2026-02-05T10:00:00Z', NULL, NULL),
  ('6d676d74-0000-4000-8000-0000000001b5', '6d676d74-0000-4000-8000-000000000002', 'Pilot copy', false, 'personal', 'individual', 'hypothetical', 'USD', '2026-02-06T10:00:00Z', NULL, NULL),
  ('6d676d74-0000-4000-8000-0000000001c1', '6d676d74-0000-4000-8000-000000000005', 'Mine', true, 'personal', 'individual', 'actual', 'USD', '2026-02-07T10:00:00Z', NULL, NULL);

INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, notes, source_provider, created_at) VALUES
  ('6d676d74-0000-4000-8000-0000000005a1', '6d676d74-0000-4000-8000-000000000002', '6d676d74-0000-4000-8000-0000000001b1', 'AAPL', 3, 100.5, '2026-01-02', NULL, NULL, '2026-02-01T10:00:00Z'),
  ('6d676d74-0000-4000-8000-0000000005a2', '6d676d74-0000-4000-8000-000000000002', '6d676d74-0000-4000-8000-0000000001b1', 'msft', 2, 300.1, '2026-01-02', NULL, NULL, '2026-02-01T10:00:00Z'),
  ('6d676d74-0000-4000-8000-0000000005a3', '6d676d74-0000-4000-8000-000000000002', '6d676d74-0000-4000-8000-0000000001b1', 'MSFT', 1, 0.3, '2026-01-02', NULL, NULL, '2026-02-01T10:00:00Z'),
  ('6d676d74-0000-4000-8000-0000000005b1', '6d676d74-0000-4000-8000-000000000002', '6d676d74-0000-4000-8000-0000000001b2', 'AAPL', 7, 90, '2026-01-03', 'note', 'manual', '2026-02-03T10:00:00Z'),
  ('6d676d74-0000-4000-8000-0000000005b2', '6d676d74-0000-4000-8000-000000000002', '6d676d74-0000-4000-8000-0000000001b2', 'NVDA', 1.5, 0.1, '2026-01-03', NULL, NULL, '2026-02-03T10:00:00Z'),
  ('6d676d74-0000-4000-8000-0000000005b5', '6d676d74-0000-4000-8000-000000000002', '6d676d74-0000-4000-8000-0000000001b5', 'AAPL', 5, 100, '2026-01-03', NULL, 'pilot', '2026-02-06T10:00:00Z');

INSERT INTO accounts (id, user_id, external_id, broker, display_name, base_currency, portfolio_id, created_at, updated_at, tax_wrapper) VALUES
  ('6d676d74-0000-4000-8000-0000000006a1', '6d676d74-0000-4000-8000-000000000002', 'parity-mgmt-manual', 'manual', 'Manual', 'USD', '6d676d74-0000-4000-8000-0000000001b1', '2026-02-01T10:00:00Z', NULL, 'isa'),
  ('6d676d74-0000-4000-8000-0000000006a2', '6d676d74-0000-4000-8000-000000000002', 'parity-mgmt-pilot', 'pilot', NULL, 'USD', '6d676d74-0000-4000-8000-0000000001b5', '2026-02-06T10:00:00Z', '2026-02-06T11:00:00Z', NULL);

INSERT INTO pilots (id, kind, slug, display_name) VALUES
  ('6d676d74-0000-4000-8000-0000000007a1', 'politician', 'parity-mgmt-pilot', 'Parity Mgmt Pilot');
INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, portfolio_list_id, starting_capital) VALUES
  ('6d676d74-0000-4000-8000-0000000007b1', '6d676d74-0000-4000-8000-000000000002', '6d676d74-0000-4000-8000-0000000007a1', 'portfolio', '6d676d74-0000-4000-8000-0000000001b5', 1000);

INSERT INTO portfolio_cash_positions (id, portfolio_id, label, currency, balance, as_of, created_at, updated_at) VALUES
  ('6d676d74-0000-4000-8000-0000000008a1', '6d676d74-0000-4000-8000-0000000001b2', 'Broker cash', 'EUR', 1250.75, '2026-03-01T00:00:00Z', '2026-03-01T10:00:00.5Z', NULL),
  ('6d676d74-0000-4000-8000-0000000008a2', '6d676d74-0000-4000-8000-0000000001b2', 'Wallet', 'USD', 20, '2026-03-02T00:00:00Z', '2026-03-02T10:00:00Z', '2026-03-03T10:00:00Z'),
  ('6d676d74-0000-4000-8000-0000000008a5', '6d676d74-0000-4000-8000-0000000001b5', 'Pilot cash', 'USD', 10, '2026-03-02T00:00:00Z', '2026-03-02T10:00:00Z', NULL);

INSERT INTO retirement_plans (id, portfolio_id, rule_version, input_json, created_at) VALUES
  ('6d676d74-0000-4000-8000-0000000009a1', '6d676d74-0000-4000-8000-0000000001b2', 'v1', '{"retireAt":65}', '2026-03-01T10:00:00Z');

INSERT INTO portfolio_memberships (id, portfolio_id, user_id, role, status, joined_at, created_at, updated_at) VALUES
  ('6d676d74-0000-4000-8000-00000000a0a1', '6d676d74-0000-4000-8000-0000000001b1', '6d676d74-0000-4000-8000-000000000003', 'editor', 'active', '2026-02-10T10:00:00Z', '2026-02-09T10:00:00Z', NULL),
  ('6d676d74-0000-4000-8000-00000000a0a2', '6d676d74-0000-4000-8000-0000000001b1', '6d676d74-0000-4000-8000-000000000006', 'editor', 'active', NULL, '2026-02-09T11:00:00Z', NULL);

-- Tokens are sha256 of 'PARITY-MGMT-ACCEPT' (i3) and 'PARITY-MGMT-EXPIRED' (i4).
INSERT INTO portfolio_invitations (id, portfolio_id, email, role, status, token_hash, expires_at, accepted_at, created_at, updated_at) VALUES
  ('6d676d74-0000-4000-8000-00000000b0a1', '6d676d74-0000-4000-8000-0000000001b1', 'parity-portfolio-mgmt-pending@example.com', 'editor', 'pending', 'parity-mgmt-i1', '2027-01-01T00:00:00Z', NULL, '2026-02-11T10:00:00Z', NULL),
  ('6d676d74-0000-4000-8000-00000000b0a2', '6d676d74-0000-4000-8000-0000000001b1', 'parity-portfolio-mgmt-revoke@example.com', 'editor', 'pending', 'parity-mgmt-i2', '2027-01-01T00:00:00Z', NULL, '2026-02-12T10:00:00Z', NULL),
  ('6d676d74-0000-4000-8000-00000000b0a3', '6d676d74-0000-4000-8000-0000000001b2', 'Parity-Portfolio-Mgmt-Invitee@example.com', 'editor', 'pending', encode(sha256(convert_to('PARITY-MGMT-ACCEPT', 'UTF8')), 'hex'), '2027-01-01T00:00:00Z', NULL, '2026-02-13T10:00:00Z', NULL),
  ('6d676d74-0000-4000-8000-00000000b0a4', '6d676d74-0000-4000-8000-0000000001b2', 'parity-portfolio-mgmt-invitee@example.com', 'editor', 'pending', encode(sha256(convert_to('PARITY-MGMT-EXPIRED', 'UTF8')), 'hex'), '2026-01-01T00:00:00Z', NULL, '2026-02-14T10:00:00Z', NULL),
  ('6d676d74-0000-4000-8000-00000000b0a5', '6d676d74-0000-4000-8000-0000000001b1', 'parity-portfolio-mgmt-old@example.com', 'editor', 'accepted', 'parity-mgmt-i5', '2026-03-01T00:00:00Z', '2026-02-20T10:00:00.250Z', '2026-02-15T10:00:00Z', '2026-02-20T10:00:00Z');

CREATE OR REPLACE FUNCTION parity_mgmt_list_restore() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO portfolio_lists VALUES (OLD.*);
  RETURN NULL;
END $$;
CREATE TRIGGER parity_mgmt_list_restore AFTER DELETE ON portfolio_lists
  FOR EACH ROW WHEN (OLD.id = '6d676d74-0000-4000-8000-0000000001b4') EXECUTE FUNCTION parity_mgmt_list_restore();

CREATE OR REPLACE FUNCTION parity_mgmt_cash_restore() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO portfolio_cash_positions VALUES (OLD.*);
  RETURN NULL;
END $$;
CREATE TRIGGER parity_mgmt_cash_restore AFTER DELETE ON portfolio_cash_positions
  FOR EACH ROW WHEN (OLD.id = '6d676d74-0000-4000-8000-0000000008a2') EXECUTE FUNCTION parity_mgmt_cash_restore();

CREATE OR REPLACE FUNCTION parity_mgmt_member_restore() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE portfolio_memberships SET status = 'active' WHERE id = NEW.id;
  RETURN NULL;
END $$;
CREATE TRIGGER parity_mgmt_member_restore AFTER UPDATE ON portfolio_memberships
  FOR EACH ROW WHEN (NEW.status <> 'active' AND NEW.id::text LIKE '6d676d74-0000-4000-8000-%') EXECUTE FUNCTION parity_mgmt_member_restore();

CREATE OR REPLACE FUNCTION parity_mgmt_invite_restore() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE portfolio_invitations SET status = 'pending' WHERE id = NEW.id;
  RETURN NULL;
END $$;
CREATE TRIGGER parity_mgmt_invite_restore AFTER UPDATE ON portfolio_invitations
  FOR EACH ROW WHEN (NEW.status <> 'pending' AND NEW.id IN ('6d676d74-0000-4000-8000-00000000b0a2', '6d676d74-0000-4000-8000-00000000b0a3', '6d676d74-0000-4000-8000-00000000b0a4'))
  EXECUTE FUNCTION parity_mgmt_invite_restore();

CREATE OR REPLACE FUNCTION parity_mgmt_invite_drop() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM portfolio_invitations WHERE id = NEW.id;
  RETURN NULL;
END $$;
CREATE TRIGGER parity_mgmt_invite_drop AFTER INSERT ON portfolio_invitations
  FOR EACH ROW WHEN (NEW.email = 'parity-portfolio-mgmt-newbie@example.com') EXECUTE FUNCTION parity_mgmt_invite_drop();

CREATE OR REPLACE FUNCTION parity_mgmt_list_rename() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE portfolio_lists SET name = name || ' #' || left(id::text, 8)
  WHERE user_id = NEW.user_id AND name = NEW.name AND id::text NOT LIKE '6d676d74-%';
  RETURN NEW;
END $$;
CREATE TRIGGER parity_mgmt_list_rename BEFORE INSERT ON portfolio_lists
  FOR EACH ROW WHEN (NEW.user_id::text LIKE '6d676d74-0000-4000-8000-%' AND NEW.id::text NOT LIKE '6d676d74-%')
  EXECUTE FUNCTION parity_mgmt_list_rename();

COMMIT;
