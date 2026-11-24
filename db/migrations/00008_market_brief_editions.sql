-- Market brief editions: the structured, language-neutral market data a brief
-- was written from (instrument levels, changes, basis points, intraday series)
-- and a revision number, so the web page, the app and every export render
-- the same numbers and name the same revision.
--
-- edition is NULL for briefs written before this migration: those keep only
-- their formatted payload and are never backfilled.
--
-- A regenerated slot no longer disappears: its rows move to
-- market_brief_revisions before the new revision is written.
--
-- Goose-only (no Fluent counterpart): not listed in fluent_migrations.txt.
-- ADD COLUMN with a constant default is metadata-only on Postgres 11+; the
-- statements are idempotent.

-- +goose Up
ALTER TABLE public.market_briefs ADD COLUMN IF NOT EXISTS revision integer NOT NULL DEFAULT 1;
ALTER TABLE public.market_briefs ADD COLUMN IF NOT EXISTS content_hash text;
ALTER TABLE public.market_briefs ADD COLUMN IF NOT EXISTS edition jsonb;

CREATE TABLE IF NOT EXISTS public.market_brief_revisions (
    id           uuid NOT NULL PRIMARY KEY,
    trading_date text NOT NULL,
    slot         text NOT NULL,
    language     text NOT NULL,
    revision     integer NOT NULL,
    payload      text NOT NULL,
    edition      jsonb,
    model        text NOT NULL,
    degraded     boolean NOT NULL,
    content_hash text,
    generated_at timestamp with time zone NOT NULL,
    archived_at  timestamp with time zone NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_market_brief_revisions_slot
    ON public.market_brief_revisions (trading_date, slot, revision);

-- +goose Down
DROP TABLE IF EXISTS public.market_brief_revisions;
ALTER TABLE public.market_briefs DROP COLUMN IF EXISTS edition;
ALTER TABLE public.market_briefs DROP COLUMN IF EXISTS content_hash;
ALTER TABLE public.market_briefs DROP COLUMN IF EXISTS revision;
