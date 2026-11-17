-- Cadence: how far a security's price sits from its own long-term trend, as a
-- contribution zone per timeframe (W, M, Q, Y). One row per symbol, timeframe
-- and period; the zones job upserts every period it computes, so a forming
-- period is overwritten as it fills and a confirmed one never changes.
--
-- zone is the engine's output; capped_zone is zone after the fundamentals
-- guard, which applies to the newest row of each timeframe only.
--
-- Goose-only (no Fluent counterpart): not listed in fluent_migrations.txt.
--
-- A new table takes no lock on live traffic; the statements are idempotent.

-- +goose Up
CREATE TABLE IF NOT EXISTS public.cadence_zones (
    symbol        text NOT NULL,
    timeframe     text NOT NULL,
    period_end    date NOT NULL,
    as_of         date NOT NULL,
    close         double precision NOT NULL,
    trend         double precision,
    distance      double precision,
    percentile    double precision,
    sample_count  integer NOT NULL,
    zone          text NOT NULL,
    capped_zone   text NOT NULL,
    guard_reason  text,
    guard_checked boolean NOT NULL DEFAULT false,
    touch_zone    text,
    confirmed     boolean NOT NULL,
    rule_version  integer NOT NULL,
    computed_at   timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (symbol, timeframe, period_end)
);

CREATE INDEX IF NOT EXISTS cadence_zones_latest_idx
    ON public.cadence_zones (symbol, timeframe, period_end DESC);

-- +goose Down
DROP TABLE IF EXISTS public.cadence_zones;
