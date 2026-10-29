-- Fixtures for parity/identity.jsonl. Idempotent: run before every parity
-- pass against the scratch database (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/identity-setup.sql
--
-- Users are 0b1d0000-...-00000000000N. The password hash is a real Vapor
-- bcrypt of "Str0ng!Pass". Deleting the users cascades to their tokens,
-- PATs, onboarding state and OAuth rows.

BEGIN;

DELETE FROM users WHERE id::text LIKE '0b1d0000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, email_verified_at, password_set_at, username,
    had_trial, trial_started_at, trial_days, trial_tier) VALUES
  ('0b1d0000-0000-4000-8000-000000000001', 'identity-parity-a@example.com',
    '$2b$11$tC9a6D7mNxanY5Tn1lRvp.TMVLtgAZfe3yNJtosmgtB3C1r21p9Ru', now(), true, now(), now(), 'identityparitya',
    false, NULL, NULL, NULL),
  ('0b1d0000-0000-4000-8000-000000000002', 'identity-parity-b@example.com',
    '$2b$11$tC9a6D7mNxanY5Tn1lRvp.TMVLtgAZfe3yNJtosmgtB3C1r21p9Ru', now(), true, now(), now(), 'identityparityb',
    false, NULL, NULL, NULL),
  ('0b1d0000-0000-4000-8000-000000000003', 'identity-parity-trial@example.com',
    '$2b$11$tC9a6D7mNxanY5Tn1lRvp.TMVLtgAZfe3yNJtosmgtB3C1r21p9Ru', now(), true, now(), now(), 'identityparitytrial',
    false, now() - interval '1 day', 14, 'temporary');

COMMIT;
