# Vapor bugs: social & platform

These bugs were ported 1:1 from Vapor and are now fixed in Go (branches
`fix/vapor-bugs-social`, `-social-2` (PR #4), `-social-3` and
`fix/decide-social`). Every item that needed an owner decision has one
(2026-10-31); the decision is recorded in the row. Status values:

- **FIX**: fixed, with a regression test.
- **KEEP**: deliberately unchanged.

## Done

| Area | Bug | Status | Commit |
|---|---|---|---|
| Boards | A user blocked either way could still vote. The vote now returns 404 "Post not found", as comments do | FIX | 89b5e29 |
| Boards | Deleting a comment never decremented `comment_count`. It now decrements once; a repeat delete is still 404 | FIX | 89b5e29 |
| Boards | The unread count (badge and feed `unreadCount`) included notifications the feed hides. It now leaves out blocked actors and deleted posts or boards | FIX | 89b5e29 |
| Boards | Any `cursor`, even junk, hid the daily post. Now only a cursor that parses (a later page) hides it | FIX | 89b5e29 |
| Boards | Comments were sorted with no tiebreak. They now sort by `created_at, id` | FIX | 89b5e29 |
| Boards | Blocking "norviq" returned 404 before the system-username check. It now returns 400 first, for "@norviq" as well | FIX | 89b5e29 |
| Boards | An unknown `aiTopic` (for example `etf`) is left out of the response. Clients decode a closed enum, so sending it would break them | KEEP | - |
| Groups | A ban's invite sweep overwrote `revoked_at` on invitations already revoked. Rows already revoked now keep their original time | FIX | 1dc2328 |
| Groups | Deleted groups count toward the 3-per-day creation cap. Vapor did this on purpose ("or deleting would refill the quota"); only the comment was corrected | KEEP | 1dc2328 |
| Groups | Discover filtered out hidden owners' groups after paging, so a page could come back short with a `nextCursor`. The filter is now in SQL, before paging | FIX | 1dc2328 |
| News | The Thesis Watch feed GET wrote to the DB: it refreshed clusters and inserted state rows. The GET is now read-only. The ingestion job does the clustering, and now does it even when the provider refresh fails. The analysis is computed for display, which also fills in rows created by feedback or read | FIX | f984e8c |
| News | In the thesis notification poller, one failing user ended the tick. That user is now logged and skipped | FIX | f984e8c |
| Metrics | The latency histogram recorded end to start, so every duration was negative. Its buckets were not cumulative, and its sum kept only the sub-second part. All three are fixed; the names and text format are unchanged | FIX | 45f573a |
| Identity | WebAuthn looked up `credential_id` by the client's base64url id only. Passkey sign-in now matches both encodings | FIX | 4eb9769 (PR #4) |
| Users / account deletion | Users who created allocation models or rebalance plans can be deleted | FIX | 9d5059a (PR #4) |
| Telegram / AI | A typed "don't" declines the pending action | FIX | 52ac08a (PR #4) |
| Telegram | Pairing sets the dedupe watermark; one undecodable update is skipped, not the batch | FIX | 6e51913 (PR #4) |
| Assistant | `create_goal` activity label; `update_expense` finds lowercase ids | FIX | d9d6203 (PR #4) |
| Insights | The daily aggregation reached only the protocol-default `fetchSymbolPosts` (source x), so the sp500 and trending tiers fetched nothing and Direct's StockTwits/Reddit/news never fed the roll-up. Each provider's own override now runs: Fallback tries each link's and the first answer with a post wins, Direct serves its sources. DeepAPI keeps the default (see the KEEP row below) | FIX | 2b38d4c |
| Insights | StockTwits `created_at` and Reddit's `access_token`/`created_utc` never decoded (the snake_case-rewriting decoder). They decode now, so StockTwits posts keep their time and Reddit is searched when credentials are set | FIX | 2b38d4c |
| Insights | Hermes sync failed every tick with "Direct provider serves per-symbol posts only." (501) whenever Direct was the link that answered (staging: no Hermes, DeepAPI out of credit), so ticker posts never synced either. A step the chain does not serve now counts as nothing synced; a real failure still fails the run, and in the fallback chain a 501 no longer replaces an earlier real error. Our bug, not a missing upstream endpoint: the 501 is synthesized locally by the Direct provider | FIX | 2b38d4c |
| Social | An import exchange without a code returned 400 after spending the OAuth flow, so the retry was a 401. The code is now checked before the flow is marked used (after the flow checks, so statuses are unchanged) | FIX | 7f9444b |
| Social | Concurrent first `EnsureSettings` calls raced on the unique `user_id`; the loser answered 500. It now re-reads the winner's row | FIX | e7e7015 |
| Social | Profile XP level is computed with the UTC calendar. Only `Level` is used, which depends on the all-time total, not the zone (the zone only moves `weekXP`, which the profile drops) | KEEP | - |
| Achievements | Sector inference matched "ai" as a substring ("retail" read as Technology). It now matches only the word | FIX | 8d2b721 |
| Achievements | `notified_at` was stamped even when the push reached no device. It is now stamped only when at least one device took it | FIX | 2142f82 |
| Articles | The daily cap counts deleted (and hidden) articles. That is what a publishing cap is for: deleting must not refill it (the same reasoning as the Groups creation cap) | KEEP | - |
| Admin / export | `downloadURL` pointed at `/exports/<user>/<file>`, which nothing serves. It now names the file route `/v1/api/v3/export/file/<user>/<file>` (the key is unchanged; the web builds its own path and iOS does not use exports) | FIX | 0439df1 |
| Admin / export | A negative `limit` or `offset` on the export list reached Postgres and answered 500. Negative values now take the defaults (20/0), as malformed ones already did | FIX | 0439df1 |
| Admin | Unsuspending a user with no settings row inserted a default row. It now writes nothing (suspending still creates the row) | FIX | 0a957a7 |
| Identity | A password change skipped the password rules that registration, set-password and reset enforce. It now applies them (after the current-password check), with the same reasons | FIX | 8234361 |
| Insights | DeepAPI's own `fetchSymbolPosts` (X, Reddit, StockTwits, Investing and Seeking Alpha scrapes keyed on stable ids) was never reached in Vapor and is not ported; DeepAPI keeps the default (x via `FetchTickerPosts`). Owner decision: keep (option a). Porting it would make the sp500 and trending tiers spend paid DeepAPI scrapes whenever Hermes has nothing for them (prod has `DEEPAPI_SOCIAL_SCRAPING_ENABLED=true`); no new paid scrapes. The cheap tiers get Direct's news | KEEP | - |
| Social | The Facebook web flow uses the first id in `FACEBOOK_APP_ID` with the single `FACEBOOK_APP_SECRET`, and nothing ties that secret to the first id. Owner decision (option a): **the web app id goes first in `FACEBOOK_APP_ID`, and `FACEBOOK_APP_SECRET` is that app's secret**; the other ids (iOS) are only Limited Login audiences, which need no secret. No `FACEBOOK_WEB_APP_ID`. Documented on `facebookConfig.webAppID` and pinned by a test. Facebook is not configured in production | KEEP | e736ccc |
| Articles | Authors could edit an article an admin hid (it stayed hidden), and an admin who wrote a deleted article could edit it. Owner decision (option b): the author's edit of a hidden article is 403 "This article was hidden by a moderator and can't be edited." (the same `httpx.Forbidden` envelope, code `forbidden`, as "You can only edit your own articles."); an edit of a deleted article is 404 "Article not found", admins included. The web edit form shows any 4xx reason verbatim; iOS has no articles client | FIX | d50a814 |
| Articles | Admin delete of a deleted article writes nothing. Owner decision: keep; a repeat delete is already a 204 | KEEP | - |
| Admin / export | `type=all` writes a one-row manifest of counts, not the data. Owner decision: keep | KEEP | - |
| Admin / export | Date filters accepted only RFC 3339 without fractional seconds; anything else (a bare `2026-01-01`) was silently ignored and the export unfiltered. Owner decision (option b): `dateFrom`/`dateTo` also accept fractional seconds and a bare `yyyy-MM-dd`, which means the start of that UTC day for both. `trade_date` is a date compared with `>=`/`<=`, so `dateTo=2026-01-31` includes trades dated 2026-01-31, exactly as `2026-01-31T00:00:00Z` always did. Any other value is still ignored. No client sends filters today | FIX | 81e5e29 |
| Admin / export | Pilot (simulated) accounts are left out of the transactions export, on purpose in Vapor. Owner decision: keep (simulated cash is not the user's financial data) | KEEP | - |
| apijson | The snake_case rewrite spells acronym segments with one capital (`show_source_urls` -> `showSourceUrls`, `show_xp` -> `showXp`), so hand-written keyed decoders never found `showSourceURLs` (reporting) or `showXP` (`PUT /v1/social/privacy`): a 400 in Vapor. `apijson.LookupKey` (used by the social and reporting decoders) and the typed rewrite now fall back to the single case-insensitive match when the exact key misses. An exact key always wins, two candidates match neither, and a name with a case twin among its siblings (`redirectURI`/`redirectUri`) stays exact. Struct targets already decoded through `encoding/json`'s case folding; the rewrite now names the field exactly, so a camelCase key also beats its snake_case twin there | FIX | b429cba |
| Groups | Members are sorted in memory; there is no push throttle without Redis; a digest payload that fails to decode gives a 500. Owner decision: keep (performance and infra only) | KEEP | - |
