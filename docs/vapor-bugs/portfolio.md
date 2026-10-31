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
- **Revoking an accepted invitation** (`DELETE .../invitations/:id`) flips it
  to `revoked` and answers 204, but the membership stays active. (a) 409
  "This invitation was accepted; remove the member instead."; (b) also revoke
  the membership it created (same effect as `DELETE .../members/:id`); (c)
  keep. Membership change, so left to the owner.
- **`isDefault` on POST /v1/portfolios** is "no unarchived owned list"
  (`count == 0`). A user whose only default is archived (legacy data; archive
  now refuses the default) or two racing creates get a second default, and
  `DefaultPortfolioList` then picks one arbitrarily. (a) default only when the
  user has no default list at all, archived included; (b) keep the rule but
  demote other defaults in the same transaction; (c) keep. Touches the
  default-list rules (pilot follows demote too), so left to the owner.

## TODO (still Vapor behaviour)

None.
