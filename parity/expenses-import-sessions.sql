-- Run after parity/expenses-import-upload.jsonl. Copies the session Vapor
-- created for user 1 (the older of the two; v2 ran second) to fixed ids, so
-- v2 is compared reading Vapor's encrypted blobs:
--   e5e00000-0000-4000-8002-00000000000a  ready (fetch, preview, discard)
--   ...b  committed   ...c  expired   ...d  owned by user 3
-- Then drops the phase-one sessions.

BEGIN;

DELETE FROM expense_import_sessions WHERE id::text LIKE 'e5e00000-0000-4000-8002-%';

WITH vapor AS (
    SELECT * FROM expense_import_sessions
    WHERE user_id = 'e5e00000-0000-4000-8000-000000000001'
    ORDER BY created_at ASC, id ASC LIMIT 1
)
INSERT INTO expense_import_sessions (id, user_id, status, file_name, sheet_count, row_count, ai_available, ai_model,
    ai_confidence, sheets_encrypted, mapping_encrypted, expires_at, created_at, updated_at, committed_at)
SELECT v.id2, v.uid, v.st, file_name, sheet_count, row_count, ai_available, ai_model, ai_confidence,
    sheets_encrypted, mapping_encrypted, v.exp, now(), now(), CASE WHEN v.st = 'committed' THEN now() END
FROM vapor, (VALUES
    ('e5e00000-0000-4000-8002-00000000000a'::uuid, 'e5e00000-0000-4000-8000-000000000001'::uuid, 'ready', now() + interval '1 hour'),
    ('e5e00000-0000-4000-8002-00000000000b'::uuid, 'e5e00000-0000-4000-8000-000000000001'::uuid, 'committed', now() + interval '1 hour'),
    ('e5e00000-0000-4000-8002-00000000000c'::uuid, 'e5e00000-0000-4000-8000-000000000001'::uuid, 'ready', now() - interval '1 minute'),
    ('e5e00000-0000-4000-8002-00000000000d'::uuid, 'e5e00000-0000-4000-8000-000000000003'::uuid, 'ready', now() + interval '1 hour')
) AS v(id2, uid, st, exp);

DELETE FROM expense_import_sessions
WHERE user_id::text LIKE 'e5e00000-0000-4000-8000-%' AND id::text NOT LIKE 'e5e00000-0000-4000-8002-%';

COMMIT;
