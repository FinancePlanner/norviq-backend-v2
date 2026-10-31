-- expenses.external_id: the id an import row carries (a receipt's QR id, a
-- CSV `external_id`, a spreadsheet column mapped to externalId). A re-import
-- skips a row whose (user_id, external_id) is already stored
-- (docs/vapor-bugs/money.md, I4). The first schema change v2 owns: no Fluent
-- migration mirrors it, so it has no section in fluent_migrations.txt.
--
-- Safe on a large, busy table:
-- * NO TRANSACTION, because CREATE INDEX CONCURRENTLY refuses to run inside
--   one. goose then runs each statement on its own, in order, on one
--   connection, so the session SETs below hold for the statements after them.
-- * ADD COLUMN with no default only changes the catalog (no table rewrite),
--   but it still takes a short ACCESS EXCLUSIVE lock. lock_timeout makes it
--   fail fast instead of queueing every expense query behind a long-running
--   transaction; the PreSync job then fails and the old pods keep serving.
-- * The unique index is built CONCURRENTLY, so expense reads and writes keep
--   flowing while it builds. Every existing row has external_id NULL, so the
--   partial index starts empty and cannot fail on duplicates.
-- * Re-runnable: a concurrent build that fails leaves an INVALID index with
--   this name behind, and IF NOT EXISTS would then keep it. The DROP first
--   removes it, so a retry builds a valid one.

-- +goose NO TRANSACTION

-- +goose Up
SET lock_timeout = '5s';
ALTER TABLE public.expenses ADD COLUMN IF NOT EXISTS external_id text;
RESET lock_timeout;
DROP INDEX CONCURRENTLY IF EXISTS public.expenses_user_id_external_id_key;
CREATE UNIQUE INDEX CONCURRENTLY expenses_user_id_external_id_key
    ON public.expenses (user_id, external_id) WHERE external_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.expenses_user_id_external_id_key;
SET lock_timeout = '5s';
ALTER TABLE public.expenses DROP COLUMN IF EXISTS external_id;
RESET lock_timeout;
