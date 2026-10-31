# Vapor bugs: portfolio, planning, automation, goals, scenarios

Bugs that were ported 1:1 from Vapor and are now fixed (FIX), kept on purpose
(KEEP), waiting on an owner decision (DECIDE), or not yet worked on (TODO).
Fixes are v2-only; Vapor is archived.

## FIX

| Area | Bug: before → after | Commit |
|---|---|---|
| watchlist | POST /v1/watchlist with a symbol already in another list: 500 PSQLError → 409 (616a99b) → added, a symbol may sit in several lists (migration 00004) | a5bdbd2 |
| watchlist | CSV commit with such a symbol: 500 after some rows were written → 409 up front (616a99b) → inserted | a5bdbd2 |
| watchlist | Readers of a user's watchlist symbols (news, thesis watch, earnings reminders, recaps, aiplatform, statistics, sentiment, screens, stocks peers, goals, dashboard, achievements) would repeat a symbol in two lists → one row per symbol / user; item quotas still count rows | a5bdbd2 |
| watchlist | Duplicate list name (create or rename): 500 → 409 | 616a99b |
| pilots | A book symbol already in another list rolled back the whole mirror apply on every tick → skipped with a `skipped_duplicate` event (8a48a9a) → mirrored into the follow list; the savepoint and event are gone | a5bdbd2 |
| portfoliomgmt | Duplicate portfolio name (create, clone, PATCH): 500 → 409 | db82c04 |
| portfoliomgmt | Deleting a portfolio with holdings: 500 (FK RESTRICT) → 409 | db82c04 |
| portfoliomgmt | Create with a source the caller can't see saved the list, then 404 → 404 before anything is saved; list and copied sandbox in one transaction | db82c04 |
| portfoliomgmt | Malformed `sourcePortfolioId` silently dropped → 400 | db82c04 |
| portfolio | Duplicate name on /v1/portfolio/lists: 500 → 409 | 192e568 |
| portfolio | Sector exposure valued at cost, rows not merged per symbol → merged positions from the portfolio valuation | 192e568 |
| portfolio | Sector keyword "ai" matched as a substring → whole word | 192e568 |
| automation | Rebalancing policy, forecast runs and smart screens never round-tripped through `ScenarioJSON` (PUT returned 400 "No such key 'values'", nothing persisted) → stored and read back | ddff959 |
| goalplanning | Validation errors: 500 → 400 with a readable reason | cfabd8e |
| goalplanning | Repeated id in PATCH /bulk: 404 → each id once, request order | cfabd8e |
| goalplanning | Fallback warnings unsorted and duplicated → deduplicated, sorted | cfabd8e |
| goalplanning | Accepting a closed suggestion made another draft → 409 | cfabd8e |
| goalplanning | A zero quote valued the holding at 0 → purchase price with the "Live price unavailable" warning | cfabd8e |
| goalplanning | First failing goal aborted the daily job and the snapshot prune → others and the prune still run | b5a6d2e |
| scenarios | Unsaved scenario with no runs deleted even when just created → kept until 24h old | 6fc73f5 |
| scenarios | Worker that lost its lease marked another worker's run failed → leaves it alone | 6fc73f5 |
| scenarios | Goal probability after a cancel divided by requested paths → completed paths | 44bf5b4 |
| scenarios | Processor read `catalog_id` before `catalogId`, unlike the validator → same order | 44bf5b4 |
| stocks | Bulk create skipped the symbol check (a blank symbol was stored) and the holdings limit → both checked per item (`Abort.400` / `Abort.403` in that item's `error`); holdings count synced | 6cf7429 |
| stocks | Update that merges into the same symbol in another list left the holdings count stale → synced | 6cf7429 |
| stocks | `limit=200` never returned `X-Next-Cursor` → returned whenever more rows exist | 6cf7429 |
| stocks | `buyDate` 2025-02-30 rolled over to 03-02 → 400 "Invalid buyDate. Expected YYYY-MM-DD." | 6cf7429 |
| stocks | Sell hitting a `LedgerTradeError` (concurrent sell): 500 → 404 "Stock not found." / 400 "Cannot sell more shares than owned." | 6cf7429 |
| stocks, pilots | Ledger cash row `first()` had no ORDER BY (rows are per `as_of`) → latest `as_of` | 375969c |
| rebalancing | Lowercase `modelId` on simulate/plans: 409 stale model → matches its model | ae9566f |
| rebalancing | `updateModel` without `expectedRevision`: 409 no reload clears → 400 "expectedRevision is required to update an allocation model." | ae9566f |
| rebalancing | Engine errors in the overview: 500 → mapped 4xx, as simulate | ae9566f |
| rebalancing | `copyModel` copied destination by destination (a later 403/404 kept earlier copies) → all destinations checked first, one transaction | ae9566f |
| simulations | fromScratch with a valid but unknown `sourcePortfolioId`: 500 (FK) → 404; a source the caller can't read is no longer stored | 86810b6 |
| simulations | Engine errors in compute and preview: 500 → mapped 4xx | 86810b6 |
| simulations | Create/update saved the record before its legs → one transaction | 86810b6 |
| simulations | List cursor decoded by truncating float seconds, skipping rows that share the anchor's `created_at` → exact microsecond | 86810b6 |
| portfolio | `/v1/lots` ignored `portfolioId`/`portfolioListId` → filtered after the access check (no filter: all the caller's accounts, as before) | d67d0f4 |
| portfolio | Share-link status/create echoed the raw `:scope` (lowercase stayed lowercase) → canonical uppercase id, as the list | d67d0f4 |
| portfoliomgmt | GET /v1/portfolios listed a portfolio twice for an owner who is also a member → once | 8569843 |
| portfoliomgmt | Compare `cashBalance` always 0 → each portfolio's cash, as /v1/portfolio/summary | 8569843 |
| portfoliomgmt | Members list had no ORDER BY → owner, then join order | 8569843 |
| portfolio | `realizedPnl` always 0 on /v1/portfolio/summary and /v1/pnl → broker `lot_disposals.realized_pnl` plus manual/ledger sells (new nullable `transactions.realized_pnl`, migration 00005, new sells only) over the portfolio's accounts, FX-converted into the base currency | f1153b4 |
| scenarios | Snapshot cash counted every account of the user → only accounts linked to the snapshot's portfolio (`accounts.portfolio_id`) | dbcd7ed |
| portfoliomgmt | Revoking an accepted invitation: 204, invitation revoked, membership still active → 409 "This invitation was accepted; remove the member instead.", nothing changes | 97ef97e |
| portfoliomgmt | POST /v1/portfolios could leave two defaults (archived legacy default, racing creates) → same rule for when the new one is default, but it demotes every other default in the same transaction under a per-user lock | 9d7b8a0 |

## KEEP

- Unknown `range` falls back to 1M; saves bump `updated_at` without changes;
  Fluent's unordered `first()` where harmless.
- Watchlist timestamps rounded to the millisecond, ICU date parsing, unknown
  status read as `active`: wire-format parity.
- GET /v1/stocks creates the default list: by design.
- goalplanning GET `/:id/suggestions` stores suggestions so accept/dismiss have
  ids; moving it to a POST would change the client contract.
- goalplanning draft payload only ever holds plain values; nothing is lost.
- scenarios Monte Carlo default distribution: `normal` is what runs and what
  results report.
- Stock valuation GET answers a missing stock or valuation with a bare 404:
  iOS reads a bodiless 404 as "no valuation yet", while a 404 whose reason is
  not "...valuation not found" (e.g. "Stock not found.") is shown as an error
  on the screen of every stock the user does not hold.
- Rebalancing `updatePreferences` needs only read access to the portfolio
  (the route still needs `planning:write`): the row is the caller's own push
  opt-in, and drift alerts are per user, so a read-only member choosing
  whether to be pushed is correct.
- The `skipped_duplicate` pilot event is gone with the cross-list
  constraint (a5bdbd2), so the iOS/web label question is moot.
- System goals timestamps (`/v1/goals`): `completedAt`/`updatedAt` are the
  current time on every call. Kept (owner, 2026-10-31): iOS may decode them
  as non-optional dates, so null would break it.
- `goals.completed_at` has no time zone (stores UTC, read as UTC). Kept
  (owner, 2026-10-31): consistent as is; a `timestamptz` migration buys
  nothing today.
- Realized P&L of sells recorded before migration 00005 stays unknown
  (NULL, counted as 0): the average cost at the time of those sells is
  gone. A manual sell whose instrument could not be resolved writes no
  transaction row, so its P&L is not recorded either. /v1/pnl fills
  realized P&L for held symbols only (a fully sold symbol has no item).
- Realized P&L converts with the market data service's FX and falls back
  to 1:1 (logged) when a rate is missing, like the scenario snapshot. The
  live valuation itself does no FX (holdings and cash are summed as is).
- Scenario snapshot cash reads `cash_balances` of the portfolio's accounts
  only; the portfolio's manual cash positions (`portfolio_cash_positions`)
  are still not part of a snapshot, as in Vapor.
- `/v1/statistics` `totalRealizedPnl` is still 0 (statistics feature, not
  this decision); it can reuse the same two sources when wanted.

## Feature notes (not bugs)

- **Dividends** (`/v1/portfolio/dividends`): a flat 5% yield on cost with
  hardcoded dates. A real version needs a dividend calendar source
  (per-symbol ex-dividend/payment dates and amounts per share, e.g. the
  market data provider's dividends endpoint, cached like quotes), then
  projects shares held x amount per upcoming payment and sums the monthly
  breakdown from those. Same response shape.
- **Sector benchmark** (`/v1/portfolio/sector-exposure`):
  `benchmarkWeightPercent` and `overweightPercent` are always null. Needs a
  benchmark-weights source (e.g. S&P 500 sector weights from an ETF
  holdings feed, refreshed by a job and stored with an as-of date), then
  `overweightPercent = weightPercent - benchmarkWeightPercent` per sector
  and `benchmarkName`/`benchmarkAsOf` from that row.

## DECIDE

None (all decided 2026-10-31; see FIX, KEEP and the feature notes).

## TODO (still Vapor behaviour)

None.
