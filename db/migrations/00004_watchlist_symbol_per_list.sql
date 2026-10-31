-- A symbol may sit in several of a user's watchlists, unique per list only.
--
-- Vapor's AddPortfolioAndWatchlistLists meant to replace Fluent's
-- (user_id, symbol) unique constraint with the per-list
-- idx_watchlist_items_user_list_symbol_unique, but dropped it under the wrong
-- name (watchlist_items_user_id_symbol_key), so the old constraint survived
-- and a symbol could only ever be in one list. Every reader that selects a
-- user's watchlist symbols de-duplicates them (PR fix/decide-portfolio).
--
-- Goose-only (no Fluent counterpart): not listed in fluent_migrations.txt.
--
-- Runs outside a transaction so the index (already present in the baseline;
-- this only guarantees it) can build CONCURRENTLY on the live table. The
-- constraint drop takes a brief ACCESS EXCLUSIVE lock; lock_timeout makes the
-- PreSync migration fail fast instead of queueing behind live traffic. Each
-- statement is idempotent, so a failed run is simply re-run.

-- +goose NO TRANSACTION

-- +goose Up
-- The per-list index comes first, so there is never a moment without one.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_watchlist_items_user_list_symbol_unique
    ON public.watchlist_items USING btree (user_id, watchlist_list_id, symbol);

-- +goose StatementBegin
DO $$
BEGIN
    PERFORM set_config('lock_timeout', '5s', true);
    ALTER TABLE public.watchlist_items
        DROP CONSTRAINT IF EXISTS "uq:watchlist_items.user_id+watchlist_items.symbol";
END
$$;
-- +goose StatementEnd

-- +goose Down
-- Fails while any symbol sits in two lists of one user: remove those rows
-- first.
-- +goose StatementBegin
DO $$
BEGIN
    PERFORM set_config('lock_timeout', '5s', true);
    ALTER TABLE public.watchlist_items
        ADD CONSTRAINT "uq:watchlist_items.user_id+watchlist_items.symbol" UNIQUE (user_id, symbol);
END
$$;
-- +goose StatementEnd
