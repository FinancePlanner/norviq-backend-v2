-- Fixtures for parity/expenses-import-upload.jsonl and parity/expenses-import.jsonl.
-- Idempotent; scratch database only (never production):
--
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/expenses-import-setup.sql
--   go run ./cmd/parity -a http://127.0.0.1:8081 -b http://127.0.0.1:18117 -jwt-secret parity-secret \
--     -ignore requestId,sessionId,expiresAt -cases parity/expenses-import-upload.jsonl
--   psql postgres://pg:pg@127.0.0.1:55499/norviq -f parity/expenses-import-sessions.sql
--   go run ./cmd/parity ... -ignore requestId -cases parity/expenses-import.jsonl
--
-- Both servers run with SPREADSHEET_IMPORT_AI and RECEIPT_OCR_PROVIDER unset
-- (heuristic analysis, OCR 503) and no Redis (throttles pass outside
-- production). Users are e5e00000-0000-4000-8000-00000000000N:
--   1 pro (uploads, commits)    2 free (premium errors)
--   3 pro (owns a foreign session)   4 pro (multipart upload)
-- PATs: nvq_pat_parityexpimportread (expenses:read), nvq_pat_parityexpimportwrite
-- (expenses:write). Deleting the users cascades to their PATs, sessions, expenses, categories,
-- snapshots and entitlements.

BEGIN;

DELETE FROM users WHERE id::text LIKE 'e5e00000-0000-4000-8000-%';

INSERT INTO users (id, email, password_hash, created_at, is_verified, email_verified_at, password_set_at, username,
    had_trial, trial_started_at, trial_days, trial_tier)
SELECT ('e5e00000-0000-4000-8000-00000000000' || n)::uuid, 'parity-expenses-import-' || n || '@example.com',
    '$2b$11$tC9a6D7mNxanY5Tn1lRvp.TMVLtgAZfe3yNJtosmgtB3C1r21p9Ru', now(), true, now(), now(), 'parityexpimport' || n,
    false, NULL, NULL, NULL
FROM generate_series(1, 4) AS n;

INSERT INTO entitlements (id, user_id, level, created_at, updated_at)
SELECT ('e5e00000-0000-4000-8001-00000000000' || n)::uuid, ('e5e00000-0000-4000-8000-00000000000' || n)::uuid, 'pro', now(), now()
FROM unnest(ARRAY[1, 3, 4]) AS n;

-- PATs for the scope and first-party cases (the token is the literal string).
INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at) VALUES
  ('e5e00000-0000-4000-8003-000000000001', 'e5e00000-0000-4000-8000-000000000001', 'parity read',
    encode(sha256('nvq_pat_parityexpimportread'::bytea), 'hex'), ARRAY['expenses:read'], now() + interval '30 days', now()),
  ('e5e00000-0000-4000-8003-000000000002', 'e5e00000-0000-4000-8000-000000000001', 'parity write',
    encode(sha256('nvq_pat_parityexpimportwrite'::bytea), 'hex'), ARRAY['expenses:write'], now() + interval '30 days', now());

COMMIT;
