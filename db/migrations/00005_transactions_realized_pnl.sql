-- Realized P&L of a manual or ledger sell: (sell price - average cost) x
-- shares, in the transaction's currency. Written by the stocks and pilots
-- ledger recorders on new sells only; NULL for buys, for every row written
-- before this migration, and for broker transactions, whose realized P&L is
-- in lot_disposals. /v1/portfolio/summary and /v1/pnl sum both.
--
-- Goose-only (no Fluent counterpart): not listed in fluent_migrations.txt.
--
-- A nullable column without a default is a catalog-only change, but ALTER
-- TABLE still needs a brief ACCESS EXCLUSIVE lock: lock_timeout makes the
-- PreSync migration fail fast instead of queueing behind live traffic.

-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE public.transactions ADD COLUMN IF NOT EXISTS realized_pnl double precision;

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE public.transactions DROP COLUMN IF EXISTS realized_pnl;
