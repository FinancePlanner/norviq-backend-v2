# Vapor bugs in the money domains: fixed, kept, or open

The Go port copied these Vapor bugs on purpose. This file records, for each one, whether it is now fixed in Go only (Vapor is archived) or kept as it is. Every item that waited for an owner decision (DECIDE) now records that decision as FIX or KEEP.

Domains covered: billing, banking (Plaid, Enable Banking, GoCardless), brokers (IBKR), expenses, expense import and receipts, budgeting, tax (including tax report downloads), financing, advanced reporting and statistics.

Nothing outside these feature packages changed, with these exceptions: the sqlc queries and generated code for billing, banking and expenses (under `db/queries` and `internal/store`), and one goose migration, `db/migrations/00003_expenses_external_id.sql` (I4).

## Billing

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| BIL-1 | FIX (earlier) | Vapor decoded RevenueCat API bodies with snake_case keys turned into nil | Restore downgraded paying users. Fixed before cutover | 244c85b |
| BIL-2 | FIX | Vapor never read the webhook field `grace_period_expiration_at_ms` | A `BILLING_ISSUE` during the grace period removed Pro; now Pro stays until the grace period ends | c8f53ce |
| BIL-3 | FIX | `CANCELLATION` always gave Pro, even for a transaction never seen before or a period already over | Pro is now kept only while the paid period is still running, or for a known subscription whose end date is unknown. Otherwise the user is free | c8f53ce |
| BIL-4 | FIX | The trial sweep ran `LIMIT 500` with no `ORDER BY`, so expired trials could wait behind 500 running ones | Candidates are now ordered by when the trial ends | c8f53ce |
| BIL-5 | FIX | A webhook whose `app_user_id` is a UUID with no matching user failed with a 500 (foreign key), and RevenueCat retried it forever | The event is stored without a user and answered 200 | c8f53ce |

## Banking

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| K1 | FIX | Plaid response bodies went through the snake_case key rewrite, so link, exchange and sync never worked | 400 "No such key 'link_token'" → the keys are decoded as Plaid sends them. A body that still fails to decode now returns 502 | de54be4 |
| K2 | FIX | A transaction on an account first seen during the same sync was skipped, then lost when the cursor moved past it | The accounts are refreshed and the transaction is imported | de54be4 |
| K3 | FIX | Fluent's `first()`/`all()` had no `ORDER BY` | Connection and account lists, and the lookups that can match more than one row, are now ordered by `created_at, id` | de54be4 |
| K4 | KEEP | An unknown stored status is shown as `active`, and an unknown provider as `plaid` | This is a fallback for values that no code writes; failing on them would turn into a 500 | — |
| K5 | KEEP | Importing a transaction with an unknown pillar creates the expense | Custom pillars are a supported `BudgetPillar` feature, so this is not a bug | — |

## Brokers (IBKR)

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| R1 | FIX | The OAuth token response never decoded, because `access_token` was rewritten before lookup | Every code exchange and refresh failed with 502 "…format was not recognized." → the token is decoded and used | 7657ab3 |
| R2 | FIX | The import limit message was missing its closing parenthesis | "(max 300." → "(max 300)." | 7657ab3 |

## Financing

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| F0 | KEEP (already deviates) | Vapor crashed on reading stored assumptions (the Fluent alias exceeds 63 bytes) | v2 never reproduced the crash; it reads the row | — |
| F1 | FIX | Calculator errors (`invalidOffer`/`invalidDate`) returned 500 | 500 → 400 with the same reason | 4f2409f |
| F2 | FIX | Matching an installment or expense a second time returned 500 (unique constraint) | 500 → 409 "This installment or expense is already matched." | 4f2409f |
| F3 | FIX | URL import had SSRF gaps: redirects and DNS answers were not checked again, `https://[::1]` was let through, and `https:///x` returned 500 | Every redirect is checked again, the dialer refuses non-public resolved IPs, IPv6 private and CGNAT literals are blocked, and `https:///x` returns 400 | 4f2409f |
| F4 | FIX | The offer extractor read "42.000" as 42 | Prices and monthly payments now treat dot groups of 3 digits as thousands (42000). Rates keep reading the dot as a decimal point | 4f2409f |
| F5 | KEEP | In a file import the last multipart `file` part wins, and an oversized upload gets 413 before the Pro gate | Harmless. Rejecting an oversized body early is fine | — |

## Expenses

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| E1 | FIX | CSV import with no `dry_run` wrote rows for real | A missing flag now means a dry run; only `dry_run=false` writes. norviq-mcp's preview relied on a dry run; web and iOS don't call this route | a8d4b6a |
| E2 | FIX | Bulk and CSV inserts skipped the amount ≥ 0 and split checks, and dropped foreign amount, currency, exchange rate, notes, receipt metadata and linked plan item | They now get the same checks and fields as a single create. A bad row fails on its own | a8d4b6a |
| E3 | FIX | Creating an expense made the month snapshot before validating the item and category | The snapshot is now made only after validation succeeds | a8d4b6a |
| E4 | FIX | A recurring template's `categoryId` was not validated | 500 (foreign key) → 400 "Invalid categoryId." | a8d4b6a |
| E5 | KEEP | A plan-item update ignores `snapshotId` | Moving an item between snapshots is feature work, and rejecting the value could break clients that send a stale id | — |
| E6 | FIX | The search cursor only worked with fractional seconds | A cursor without them was ignored → it is now accepted | a8d4b6a |
| E7 | FIX | A user's first category list came back in seed order | It is now sorted by name, like every later list | a8d4b6a |
| E8 | FIX | Two simultaneous dismisses of the same suggestion | The second one got 500 → both answer 200 | a8d4b6a |

## Import and receipts

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| I1 | KEEP | The spreadsheet and OCR providers send `temperature: 0` | They use the OpenAI-compatible wire, not the Haiku chain, so this is not a bug | — |
| I2 | KEEP (owner decision) | `/ocr` and `/scan` never count receipt scans toward usage | Kept unmetered on purpose: both stay Pro/trial-gated, with no per-scan quota. Vapor's comment said the batch was "counted per image below", which nothing did; the Go handlers now say they are unmetered on purpose | 8e72609 |
| I3 | FIX | The bulk importer dropped FX, notes, receipt metadata and the linked plan item | Same fix as E2 | a8d4b6a |
| I4 | FIX (owner decision) | Receipt-commit dedup never compares `externalId` with stored rows, because `expenses` has no external id column | Migration 00003 adds a nullable `expenses.external_id` and a partial unique index on `(user_id, external_id) WHERE external_id IS NOT NULL`. Receipt commit, CSV import (`external_id`) and the spreadsheet importer (a column mapped to `externalId`, which Vapor ignored) store it. A row whose external id the user already has, whatever its date, is skipped and reported as each path reports duplicates: receipts `skipped`, CSV `skipped_duplicate`, spreadsheet `duplicateExisting`. A row a concurrent import stored first hits the index and is reported the same way. Bank transaction import carries no external id and is unchanged: the transaction row links to its expense, and a second import gets 409 | ad7aacd |
| I5 | FIX | Raw-body `/ocr` accepted up to 10 MB | → 413 above 8 MB, the same as multipart | 3f632ff |
| I6 | FIX | The currency character set included space and letters | Any text with a space "had a currency symbol" → only real symbols and whole tokens count | 6caf182 |
| I7 | FIX | In a pretty-printed `styles.xml`, date cells lost their date styles | Read as numbers → read as dates | 6caf182 |
| I8 | FIX | `skippedRows` ignored the sheet name | Another sheet's exclusions are no longer applied to the selected sheet | 6caf182 |
| I9 | KEEP (owner decision) | Fetching an import session has no premium check | Kept: sessions can only be created while premium, and a lapsed user can still read their own session | — |

## Budgeting

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| G1 | FIX | A reallocation from a snapshot dated at the end of a month targeted itself | The source is now excluded, and the effective month is the 1st of the next month | 95d3f68 |
| G2 | FIX | Plan items in the same category each got the full actual spend, so it was counted twice | The spend is now split in proportion to each item's target | 95d3f68 |
| G3 | FIX | The deep-link `scope` was not URL-encoded | It is now query-escaped | 95d3f68 |
| G4 | FIX | Bulk-targets returned 500 after committing when drift evaluation failed | The error is logged and the 200 dashboard is returned | 95d3f68 |
| G5 | FIX | A duplicate template name returned 500 | 500 → 409 | 95d3f68 |
| G6 | KEEP | A malformed `months` falls back to 6 | The value is lenient and clamped; returning 400 could break clients that send it empty | — |
| G7 | FIX | `investmentContributionTarget` was not rounded | It is now rounded to the currency's precision | 95d3f68 |

## Tax

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| X1 | FIX | Swift `Decimal(Double)` digits | 0.2800000000000000512 → 0.28 | 47c495e |
| X2 | FIX | Stored doubles were read back with different trailing digits | 88.55000000000003 → 88.55 | 47c495e |
| X3 | FIX (owner decision) | PUT market-admission has no Pro gate, and its 422 runs before the owner check | A free user now gets the same 403 `upgrade_required` (`feature=tax_optimization`) as the other tax writes. The gate runs first, before the path check and the attestation 422, in the same order as the sibling gated routes. The owner check stays in the service, after the 422 | 1ca2c07 |
| X4 | KEEP (owner decision) | GET profile and profile/context are not Pro-gated | Kept open for the upsell screens | — |
| X5a | FIX | A malformed `scenarioId` on action-plans and placement-plans | 404 → 400 "Invalid tax scenario id." | dee7586 |
| X5b | KEEP | A harvest scenario sent to placement-plans returns 404 | "Asset-location scenario not found." is accurate | — |
| X6 | FIX | Report download sent `Accept: bytes` on a 206 | Now sends `Accept-Ranges: bytes` | 47c495e |
| X7 | FIX | A bad or unsatisfiable Range got an empty 400 | Unsatisfiable → 416 with `Content-Range: bytes */N`. Malformed → the header is ignored and the full 200 is served | 47c495e |
| X8 | KEEP | The ETag is `"<mtime>-<size>"` | ETags are opaque, and this one is stable | — |
| X9 | FIX | The PDF writer turned every non-ASCII character into "?" | Text is now written in Windows-1252, so accents, € and dashes render | d9a5254 |
| X10 | FIX | The FX cache answered from a partial set of cached rows | The cache is used only if it covers the window; otherwise ECB is fetched, with the cache as fallback | 45f2132 |
| X11 | KEEP | The projection job stores the text "Abort.404: …" | It only goes into the internal `last_error` column | — |

## Advanced reporting

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| P1 | FIX | snake_case `show_source_urls` never matched | 400 → accepted | 50eec6a |
| P2 | FIX | Monthly, quarterly and yearly recurrence skipped the current period, and the anchor day was clamped to the wrong month | It now schedules the current period and clamps to the anchor month | 5f75ad0 |
| P3 | FIX | A missing or unlisted recipient got 403 with inconsistent reasons, and recipients were compared case-sensitively | Every bad link now gets one reason, and recipients are compared as UUIDs | 51f7b1c |
| P4 | FIX | A schedule whose next run could not be computed failed the whole worker tick | That schedule is paused (`pausedReason: "invalid_recurrence"`) and the tick continues | 9fffac9 |
| P5 | KEEP | Retention deletes expired files but keeps their rows | The rows let the link return 410 and keep run history; they cascade away with their runs | — |

## Statistics

| ID | Status | Bug | Before → after | Commit |
|---|---|---|---|---|
| S1–S3 | KEEP | `currency-split`, `scenario-tracking`, `scorecard` and the top-level routes return the full overview | Missing features, not bugs | — |
| S4 | FIX | Any word containing "ai" ("retail") made a sector Technology | "ai" must now be a whole word | 78b807a |
| S5 | KEEP | `totalRealizedPnl` is always 0 | Missing feature; the portfolio group owns realized P&L | — |
| S6 | KEEP (owner decision) | `baseCurrency` is always "USD", and the quote currency is ignored | Kept. FX conversion to a real base currency is feature work, not a bug fix | — |
| S7 | KEEP | A non-integer `top` gets Vapor's decode-error wording | Not a bug | — |

## Notes

- `parity/tax-reports.jsonl` range cases now differ from Vapor by design: 416 and `Accept-Ranges`.
- `TestBillingContextTrialUserHasFullAccess` already fails on `origin/main` (it expects 7 trial days but gets 8). It is not caused by this branch.
