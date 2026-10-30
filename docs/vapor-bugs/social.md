# Vapor bugs: social & platform

These bugs were ported 1:1 from Vapor and are now being fixed in Go (branch
`fix/vapor-bugs-social`). Status values:

- **FIX**: fixed, with a regression test.
- **KEEP**: deliberately unchanged.
- **DECIDE**: needs a product decision from the owner.
- **PENDING**: not started. Work stopped because free disk dropped below 8 GiB.

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

## Pending (not started; recommendation in brackets)

| Area | Bug | Recommendation |
|---|---|---|
| Identity | WebAuthn stores `credential_id` as standard base64 but looks it up by the client's base64url id | FIX: match both encodings in the lookup, no migration |
| apijson | The snake_case `show_source_urls` normalizes to `showSourceUrls` and never matches `showSourceURLs` | FIX: case-insensitive fallback when the exact camel key misses |
| Users / account deletion | DELETE on the user returns 500 when the user is referenced by `allocation_models.created_by_user_id` or `rebalance_plans.created_by_user_id` (ON DELETE RESTRICT). Bank links are already revoked at that point | FIX: null out or delete those rows before the delete, and check before revoking |
| Telegram / AI | The typed word "don't" never counts as a refusal (the split is on letters only) | FIX |
| Telegram | The pairing message does not advance the dedupe watermark. In the poller, one undecodable update fails the whole batch | FIX |
| Assistant | The `create_goal` activity label is "Working…" | FIX |
| Assistant | `update_expense` compares the string id against the uppercase id | FIX |
| Insights | Only the user tier is fetched. Reddit's `access_token` and StockTwits' `created_at` never decode. The Direct provider keeps Hermes sync running and failing with 501 | FIX or DECIDE |
| Social | OAuth flow marked used before the empty-code 400; `ensureSettings` unique race; profile XP in UTC; Facebook web flow uses the first app id | FIX (the last two: DECIDE) |
| Achievements | Sector inference matches the substring "ai". `notified_at` is stamped even when the push fails | FIX |
| Articles | Daily cap counts deleted articles; authors can edit hidden articles; admin delete of a deleted article writes nothing | DECIDE (cap) / FIX |
| Admin | Unsuspending a user with no settings row creates a default row. Export quirks: `type=all` manifest, strict date filters, pilot accounts skipped, negative limit 500, wrong `downloadURL` | FIX (negative limit, downloadURL) / DECIDE (rest) |
| Identity | A password change does not re-check the password rules | DECIDE |
| Groups | Members are sorted in memory; no push throttle without Redis; a digest payload that fails to decode gives a 500 | KEEP (performance and infra only) |
