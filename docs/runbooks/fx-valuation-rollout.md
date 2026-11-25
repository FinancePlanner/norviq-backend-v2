# FX valuation rollout (`FX_VALUATION_ENABLED`)

Plan A of the money and currency work: ECB rates (`fx_daily_rates`), `stocks.currency`,
snapshots and the live summary convert holdings into the portfolio's base currency, and
realized P&L is converted. Everything that changes a total is behind `FX_VALUATION_ENABLED`
(`true` or `1`; default off). Owner decision numbers refer to the ADR and the plan's
"Deliberate refinement of the ADR" section:

- Q1: the source and depth of the FX history backfill.
- Q3: the owner's decision on when to flip production.
- R25: the owner accepts or rejects the two NULL-currency valuation behaviours (checks 2 and 3).

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

-- (1) Minor-unit codes (GBX, GBp, ZAc, ZAC, ILA) in positions.currency. Expect zero or a
--     small count. These are not copied by the 00009 backfill (R23); ZAC is listed because
--     the backfill excludes it while fx.Normalize handles only ZAc.
SELECT currency, count(*) FROM positions
WHERE currency IN ('GBX', 'GBp', 'ZAc', 'ILA', 'ZAC')
GROUP BY 1 ORDER BY 2 DESC;

-- (2) R25b: stocks with no stored currency whose latest quote is in a minor-unit code.
--     With the flag on these get price x0.01 but an unscaled cost.
--     This reads quote_cache only. quote_cache overrides market_price_bars when a live
--     quote exists, so bars matter only for symbols with no quote at all.
SELECT q.currency, count(*)
FROM stocks s
JOIN LATERAL (
  SELECT currency FROM quote_cache c WHERE upper(c.symbol) = upper(s.symbol)
  ORDER BY c.as_of DESC LIMIT 1
) q ON true
WHERE s.currency IS NULL AND q.currency IN ('GBX', 'GBp', 'ZAc', 'ILA', 'ZAC')
GROUP BY 1;

-- (3) R25a: stocks with no stored currency and no quote at all.
--     With the flag on these are valued at buy price, 1:1, in the base currency
--     (live: in totalValue and cost; snapshot: cost only).
SELECT count(*)
FROM stocks s
WHERE s.currency IS NULL
  AND NOT EXISTS (SELECT 1 FROM quote_cache c WHERE upper(c.symbol) = upper(s.symbol));

-- (4) Listing mismatches: stocks.currency is set and its normalised code differs from the
--     normalised currency of the latest quote. With the flag on these are unpriced in
--     snapshots (valued at cost) instead of being converted.
SELECT count(*)
FROM stocks s
JOIN LATERAL (
  SELECT currency FROM quote_cache c WHERE upper(c.symbol) = upper(s.symbol)
  ORDER BY c.as_of DESC LIMIT 1
) q ON true
WHERE s.currency IS NOT NULL
  AND CASE s.currency WHEN 'GBX' THEN 'GBP' WHEN 'GBp' THEN 'GBP' WHEN 'ZAc' THEN 'ZAR'
                      WHEN 'ILA' THEN 'ILS' ELSE upper(btrim(s.currency)) END
   <> CASE q.currency WHEN 'GBX' THEN 'GBP' WHEN 'GBp' THEN 'GBP' WHEN 'ZAc' THEN 'ZAR'
                      WHEN 'ILA' THEN 'ILS' ELSE upper(btrim(q.currency)) END;

-- (5) Quote currency semantics for London listings: quote_cache rows by currency for
--     symbols ending in .L. The quote currency from the provider drives the flag-on rules.
SELECT currency, count(*) FROM quote_cache
WHERE symbol LIKE '%.L'
GROUP BY 1 ORDER BY 2 DESC;

-- (6) Latest stored fixing per currency, to compare with the latest TARGET business day
--     (fx_daily_rates has no fetch timestamp, so freshness is judged by date).
SELECT quote, max(date) AS latest FROM fx_daily_rates
WHERE base = 'EUR'
GROUP BY 1 ORDER BY 1;
```

Checks (2) and (3) are the owner decision R25 in the plan's "Deliberate refinement of the
ADR" section: the owner accepts or rejects those two behaviours before the flip. Check (1)
tells you how many holdings rely on a stored minor-unit code being normalised at read time.

Check (4) counts holdings whose stored currency disagrees with the provider's quote
currency. They are valued at cost in snapshots once the flag is on, so a high count means
the snapshot totals will look different from the live summary.

Check (5) feeds pre-flip step 4 below.

## Pre-flip prerequisites

Do these before the flip. Production must not be flipped until every one is done and the
read-only checks above are recorded.

1. ECB history (ADR Q1). There is no load mechanism for ECB history yet. The daily job
   fetches only the last 10 days, and nothing in the code reads days older than that window
   today (`HistoricalSeries` has no production caller). Loading history therefore becomes
   required before any consumer of historical conversion ships; until then older days would
   report every foreign holding as missing.
2. Unsupported currencies. ECB does not publish every currency in use (TWD is not published,
   as of 2026-11-25). Holdings in such a currency stay in `fxMissing` with the flag on. List
   the currencies from the `unsupported currency` log line of the fxrates job, by code only.
   A currency that already has rows and then returns none logs a Warn
   (`fx_ecb_daily no new fixings`); treat that as stale rates.
3. Snapshot step change and cross-screen disagreement (owner decision). Pre-flip snapshot
   rows are raw mixed-currency sums and post-flip rows are converted, so readers of
   snapshots (recaps, achievements, gamification, performance) show a jump for
   multi-currency lists. Separately, the dashboard, why-moved, statistics, rebalancing,
   goal planning and expenses overview stay unconverted until Plan B, so they disagree with
   `/v1/portfolio/summary` once it converts. Choose: flip together with Plan B, or accept
   both and document them.
4. Provider quote currency. The quote currency from the provider drives the flag-on rules,
   and Finnhub falls back to `USD` when its profile fetch fails and caches that value. Use
   check (5): confirm what currency the provider reports for LSE pence quotes (GBp or GBX
   against GBP). If a `USD` fallback shows up for `.L` symbols in production data this is a
   flip blocker that needs a code change.
5. Deploy with the flag off, confirm `fx_daily_rates` has rows for every currency in use,
   then flip (below).

## Steps

1. Infra PR: set `FX_VALUATION_ENABLED=true` on staging. `V2_JOBS` is already `all`.
2. On staging, run check (6): for each currency in use, `max(date)` must be the latest
   TARGET business day (ECB publishes around 16:00 CET, so before that it is the previous
   one).
3. Compare `/v1/portfolio/summary` for a multi-currency staging portfolio: `totalValue`,
   `fx.asOf`, and that `fxMissing` is empty or lists only expected currencies.
4. Production flag flip: owner decision (ADR Q3), after the prerequisites above.
5. Watch the logs for `listing_mismatch` and for `fxMissing` after the flip.
6. Rollback: set the flag off. Totals return to today's values and the B3 labels stay.
   Snapshot rows captured while the flag was on are not rewritten.

## At the flip

- Realized P&L moves from the Finnhub conversion to ECB rates. A sale with no rate is left
  out of the total and its currency appears in `fxMissing`. Expect realized totals to shift
  slightly.
- Live and snapshot valuation apply the listing guard (a quote in a different currency from
  the holding counts as unpriced) and the minor-unit factor only while the flag is on.

## Post-flip cleanup

Once the flag has been on and stable, remove the legacy realized-P&L path: `FxSource` and
`realizedLegacy` (R28). They exist only so that turning the flag off restores every total.
