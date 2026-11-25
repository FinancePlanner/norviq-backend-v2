-- Money and currency: the listing currency of a holding, and the FX facts a
-- portfolio value snapshot was converted with.
--
-- stocks.currency is the currency the holding is priced in. It is NULL for
-- manual entries (the quote's currency applies at runtime) and for rows that
-- cannot be told apart; it is never guessed.
--
-- Backfill (broker positions only): a stocks row imported from a broker
-- account takes the currency of the matching position (same account, same
-- symbol) when exactly one distinct currency matches and it is a plain ISO
-- code (case-exact three capitals, not a minor-unit code such as GBX, ZAC or
-- ILA): a position's average cost may be in minor units, so those and
-- mixed-case codes (GBp, ZAc) leave NULL. Zero or several matches leave NULL.
-- The quote's currency is deliberately not used as a source.
--
-- portfolio_value_snapshots.fx_as_of is the rate date used for the snapshot;
-- fx_missing lists the currencies that had no rate (so the total is partial).
--
-- Goose-only (no Fluent counterpart): not listed in fluent_migrations.txt.
-- Plain nullable ADD COLUMN is metadata-only; the statements are idempotent.

-- +goose Up
ALTER TABLE public.stocks ADD COLUMN IF NOT EXISTS currency text;

-- +goose StatementBegin
UPDATE public.stocks s
SET currency = m.cur
FROM (
    SELECT s2.id, min(p.currency) AS cur
    FROM public.stocks s2
    JOIN public.positions p ON p.account_id = s2.source_account_id
    JOIN public.instruments i ON i.id = p.instrument_id AND upper(i.symbol) = upper(s2.symbol)
    WHERE s2.currency IS NULL AND s2.source_account_id IS NOT NULL
    GROUP BY s2.id
    HAVING count(DISTINCT p.currency) = 1
       AND min(p.currency) ~ '^[A-Z]{3}$'
       AND min(p.currency) NOT IN ('GBX', 'ZAC', 'ILA')
) m
WHERE s.id = m.id;
-- +goose StatementEnd

ALTER TABLE public.portfolio_value_snapshots
    ADD COLUMN IF NOT EXISTS fx_as_of date,
    ADD COLUMN IF NOT EXISTS fx_missing text[];

-- +goose Down
ALTER TABLE public.portfolio_value_snapshots DROP COLUMN IF EXISTS fx_missing;
ALTER TABLE public.portfolio_value_snapshots DROP COLUMN IF EXISTS fx_as_of;
ALTER TABLE public.stocks DROP COLUMN IF EXISTS currency;
