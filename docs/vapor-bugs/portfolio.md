# Vapor bugs: portfolio, planning, automation, goals, scenarios

Bugs that were ported 1:1 from Vapor and are now fixed (FIX), kept on purpose
(KEEP), waiting on an owner decision (DECIDE), or not yet worked on (TODO).
Fixes are v2-only; Vapor is archived.

## FIX

| Area | Bug: before → after | Commit |
|---|---|---|
| watchlist | POST /v1/watchlist with a symbol already in another list: 500 PSQLError → 409 | 616a99b |
| watchlist | CSV commit with such a symbol: 500 after some rows were written → 409 up front, nothing written | 616a99b |
| watchlist | Duplicate list name (create or rename): 500 → 409 | 616a99b |
| pilots | A book symbol already in another list rolled back the whole mirror apply on every tick → that symbol is skipped (`skipped_duplicate` event), the rest applies | 8a48a9a |
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

## DECIDE

- **Watchlist symbol in several lists.** Vapor meant symbols to be unique per
  list, but its migration dropped the old constraint under the wrong name, so
  `uq:watchlist_items.user_id+watchlist_items.symbol` still blocks it. (a) keep
  the 409 (done); (b) a migration dropping the constraint, after the readers
  that select a user's symbols de-duplicate (news, thesiswatch, recaps,
  aiplatform, stocks queries).
- **`skipped_duplicate` pilot event:** clients render it generically; add a
  label in iOS and web?
- **`realizedPnl` always 0:** sum broker `lot_disposals.realized_pnl` (needs FX),
  record P&L on manual/ledger sells (new storage), or keep 0.
- **Dividends:** flat 5% yield on cost with hardcoded dates. Feature work.
- **Sector benchmark fields always null:** needs a benchmark-weights source.
- **Scenario snapshot cash** counts all the user's accounts, not just the
  portfolio's (`scenarios/capture.go`): filter, or keep and document.
- **System goals timestamps** (`/v1/goals`): `completedAt`/`updatedAt` are the
  current time on every call. Keep, return null, or use the first matching
  budget/expense row's `created_at`.
- **`goals.completed_at`** has no time zone (stores UTC, read as UTC). Keep, or
  migrate to `timestamptz`.

## TODO (still Vapor behaviour)

- Stocks: bulk create skips symbol validation and the holdings limit;
  `limit=200` never returns `X-Next-Cursor`; `buyDate` 2025-02-30 rolls over to
  03-02; merge-on-update doesn't sync the holdings count; valuation GET 404 has
  an empty body; cash-row `first()` has no ORDER BY; `LedgerTradeError` → 500.
- Rebalancing: lowercase `modelId` → 409; `updateModel` without
  `expectedRevision` → 409; fromScratch with a valid but unknown
  `sourcePortfolioId` → 500; engine errors in overview, simulation compute and
  preview → 500; `updatePreferences` needs only read scope; copyModel and
  simulation create/update are not transactional; simulation list cursor goes
  through a float.
- Portfolio core: `/v1/lots` ignores the portfolio filters; GET /v1/portfolios
  lists a portfolio twice for an owner who is also a member; compare
  `cashBalance` always 0; members list has no ORDER BY; revoking an accepted
  invitation keeps the membership; `isDefault` on create can make a second
  default; share status/create echo the raw `:scope`.
