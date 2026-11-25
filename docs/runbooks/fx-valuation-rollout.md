# FX valuation rollout (`FX_VALUATION_ENABLED`)

Plan A of the money and currency work: ECB rates (`fx_daily_rates`), `stocks.currency`,
snapshots and the live summary convert holdings into the portfolio's base currency, and
realized P&L is converted. Everything that changes a total is behind `FX_VALUATION_ENABLED`
(`true` or `1`; default off). Owner decision numbers (Q1, Q3, R25) refer to the ADR and the
plan's "Deliberate refinement of the ADR" section.

## What changes on deploy, even with the flag off

- The `currency` label on each allocation row and each `pnl-by-symbol` row becomes the
  instrument currency (the B3 correction). Totals do not change. Clients will see the new
  labels as soon as the backend is deployed.
- One exception: a raw minor-unit quote (GBX, GBp, ZAc, ILA) keeps the base-currency label
  while the flag is off, so label and amount stay consistent.
- The response gains `fx` and `fxMissing` fields (additive).

## Pre-flip read-only checks (R25)

Run these against production in a read-only session. Aggregates only: no amounts, no user or
account identifiers. Record the counts in the rollout ticket.

```sql
SET default_transaction_read_only = on;

-- (1) Minor-unit codes in positions.currency. Expect zero or a small count.
--     A raw GBX/ZAC/ILA code is not copied by the 00009 backfill (R23).
SELECT currency, count(*) FROM positions GROUP BY 1 ORDER BY 2 DESC;

-- (2) R25b: stocks with no stored currency whose latest quote is in a minor-unit code.
--     With the flag on these get price x0.01 but an unscaled cost.
SELECT q.currency, count(*)
FROM stocks s
JOIN LATERAL (
  SELECT currency FROM quote_cache c WHERE upper(c.symbol) = upper(s.symbol)
  ORDER BY c.as_of DESC LIMIT 1
) q ON true
WHERE s.currency IS NULL AND q.currency IN ('GBX', 'GBp', 'ZAc', 'ILA')
GROUP BY 1;

-- (3) R25a: stocks with no stored currency and no quote at all.
--     With the flag on these are valued at buy price, 1:1, in the base currency
--     (live: in totalValue and cost; snapshot: cost only).
SELECT count(*)
FROM stocks s
WHERE s.currency IS NULL
  AND NOT EXISTS (SELECT 1 FROM quote_cache c WHERE upper(c.symbol) = upper(s.symbol));
```

Checks (2) and (3) are the owner decision R25 in the plan's "Deliberate refinement of the
ADR" section: the owner accepts or rejects those two behaviours before the flip. Check (1)
tells you how many holdings rely on a stored minor-unit code being normalised at read time.

## Steps

1. Infra PR: set `FX_VALUATION_ENABLED=true` on staging. `V2_JOBS` is already `all`.
2. On staging, check that `fx_daily_rates` has rows fetched within the last 6 hours.
3. Compare `/v1/portfolio/summary` for a multi-currency staging portfolio: `totalValue`,
   `fx.asOf`, and that `fxMissing` is empty or lists only expected currencies.
4. Production flag flip: owner decision (ADR Q3). Do not flip before steps 7 to 9 and the
   pre-flip checks are done.
5. Watch the logs for `listing_mismatch` and for `fxMissing` after the flip.
6. Rollback: set the flag off. Totals return to today's values and the B3 labels stay.
   Snapshot rows captured while the flag was on are not rewritten.
7. Before the production flip, load ECB history back to the oldest snapshot day the series
   shows (ADR Q1). The daily job only fetches the last 10 days, so older days report every
   foreign holding as missing until the history is loaded.
8. ECB does not publish every currency in use (TWD is not published, as of 2026-11-25).
   Holdings in such a currency stay in `fxMissing` with the flag on. List them from the
   `unsupported currency` log line of the fxrates job before the flip.
9. Order: deploy with the flag off, confirm `fx_daily_rates` has rows for every currency in
   use, then flip. Rolling back does not rewrite snapshot rows captured while the flag was on.

## At the flip

- Realized P&L moves from the Finnhub conversion to ECB rates. A sale with no rate is left
  out of the total and its currency appears in `fxMissing`. Expect realized totals to shift
  slightly.
- Live and snapshot valuation apply the listing guard (a quote in a different currency from
  the holding counts as unpriced) and the minor-unit factor only while the flag is on.

## Post-flip cleanup

Once the flag has been on and stable, remove the legacy realized-P&L path: `FxSource` and
`realizedLegacy` (R28). They exist only so that turning the flag off restores every total.
