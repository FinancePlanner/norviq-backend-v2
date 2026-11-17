-- The close the history ingestion job last stored for each instrument, used
-- to detect a split: the job refetches from anchor_date and compares the
-- provider's close on that day with anchor_close. A mismatch means the
-- provider's adjusted history moved, so the whole history is refetched.
--
-- The job owns this table. market_price_bars cannot serve as the reference
-- because /v1/market/history also writes fresh provider bars into it.
--
-- Goose-only (no Fluent counterpart): not listed in fluent_migrations.txt.
--
-- A new table takes no lock on live traffic; the statements are idempotent.

-- +goose Up
CREATE TABLE IF NOT EXISTS public.market_history_anchors (
    instrument_key text PRIMARY KEY,
    anchor_date date NOT NULL,
    anchor_close double precision NOT NULL,
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS public.market_history_anchors;
