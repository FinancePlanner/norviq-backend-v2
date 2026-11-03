# Go improvement plan: norviq-backend-v2

Date: 2026-11-03. This document is a plan only. No production code, schema, configuration or infrastructure was changed while producing it.

It consolidates two independent audits of the same snapshot. The snapshot is HEAD `81ce5ec` plus the uncommitted working tree; the sha1 of `git diff` starts `167b3614c135` and stayed unchanged for the whole of both audits.

| Source | File | Status of its items |
|---|---|---|
| `go-backend-audit` skill | `Go-backend-audit.md` (30 findings, GO-01…GO-30) | Classified `CONFIRMED DEFECT`, `CODE-SUPPORTED RISK` or `HYPOTHESIS / MEASURE FIRST`. No finding is a measured bottleneck. |
| `security-audit` skill, run-1 | `Security-audit.md`, a copy of `~/security-audit-skill/norviq-backend-v2/run-1/REPORT.md` | **Incomplete run: 0 verified findings.** It has 19 unvalidated candidates (C1…C19). Each was proposed and, for most, reproduced in the sandbox by a single hunter. No second agent has tried to refute them. 7 coverage units were deferred and no coverage critic ran. |

**How to read statuses.**
- Security candidates keep the security skill's wording: `unvalidated (hunter-proposed confirmed)` or `unvalidated (needs_validation)`. They are never written here as confirmed vulnerabilities.
- Go items keep the Go audit's classification.
- The parent re-read the source behind C1, C2 and C6 and the claims matched. That raises confidence for planning purposes. It is not the skill's verification.

**Why some unvalidated security fixes are scheduled now anyway.** A fix can be scheduled before validation finishes when all three of these hold:
1. The change is small.
2. It only adds or tightens a check, so it cannot break a correct path.
3. It has its own regression test that encodes the invariant.

If later validation rejects the candidate, the fix still stands as hardening. Where a candidate needs a deployment fact to settle it (the C11 to C19 leads), the plan schedules the owner-side check first. The one exception is a fail-closed guard that is correct whatever the answer.

---

## 1. Cross-audit challenge of the recommendations

The Go audit's security-integration checklist was applied in both directions. These are the outcomes:

| # | Recommendation | Challenge | Resolution |
|---|---|---|---|
| X1 | GO-05: give the AI platform `&http.Client{}` a `Timeout` | That same client carries **user-supplied BYOK base URLs** (C5, the SSRF bypass). A timeout alone leaves the redirect and dial-time SSRF open. Putting netguard dial control on the shared client would also constrain the operator's own provider chain. | **Split clients.** Use a `netguard.Client` (dial-time public-IP check, `Proxy=nil`, no or re-validated redirects, timeout) for every request to a user-supplied URL: the BYOK verifier and user-credential chat. The operator chain keeps a plain client with a timeout. Make one change set, not two. |
| X2 | GO-14 / GO-30: negative-cache `QuoteUnavailable` and singleflight per `provider:symbol` | Symbols are stored with only trim and uppercase (C17 hardening). Recon also found the market cache keys contain the raw symbol with no length bound. Negative caching user-chosen symbols lets one user write unbounded junk keys to shared Redis, and singleflight keys share the same issue. | **First add a strict symbol allowlist at write time:** `^[A-Z0-9][A-Z0-9.\-^=]{0,19}$` or similar, validated against existing data before enforcing. This also closes the C17 class. Negative cache and singleflight come after it. |
| X3 | GO-19: per-user limiter after auth, so MCP users stop sharing one bucket. The security hardening note says to narrow `TRUSTED_PROXY_CIDRS` (currently all RFC1918 plus 100.64/10). | Narrowing the CIDRs alone makes MCP's pod IP the client IP. MCP users still share a bucket, and in-cluster callers lose any forwarded identity. The per-user limiter alone leaves every in-cluster and tailnet peer able to spoof the pre-auth IP through X-Forwarded-For. | **Do both, plus one more.** Narrow the trusted CIDRs to the ingress. Add the per-user limiter after `Scoped()`. Have MCP forward the client IP with `X-Norviq-Edge-Key`; that is a new SealedSecret sealed for the **MCP namespace and name**, otherwise it silently decrypts to nothing. Keep a generous pre-auth per-IP limit. |
| X4 | GO-10 (Go: RevenueCat event ordering, MEASURE FIRST) and C15 / C14 (security: stale-event overwrite, sandbox environment ignored) | Both audits found the same code path independently, which raises confidence. The billing one-transaction change already in the working tree makes failed events redeliverable, so it **widens** the reorder window. | **Merge into one billing work item** (P1-6). Always insert the event row. Ignore SANDBOX events for entitlements. Skip writes older than the last applied `event_timestamp_ms` for each original transaction. Never grant Pro when `expiration_at_ms <= now`. Add a daily period-end reconciliation. Handle TRANSFER. Run the read-only inversion, sandbox-count and expired-Pro SQL first, to size the damage already done. |
| X5 | GO-06: serialize Telegram updates per chat | Telegram is also where C1 (a scoped token gains first-party authority) and C7 (loose "yes" parsing approves foreign actions) sit. Serialization alone makes confirmations ordered. It does not make them authorized. | **Land C1 and C7 before or with GO-06** in the same area. Make link-code minting first-party only and revoke links on token revoke and revoke-all. Require whole-message approvals, bound to actions proposed in that chat by a first-party turn. Then add the conditional claim and per-chat serialization. |
| X6 | GO-01: cap scenario Monte Carlo work. GO-02: concurrent 26 MB uploads | Both are **availability DoS paths for any Pro user** against the only replica. The security run's resource-exhaustion unit (h12) was deferred, so neither audit covered it from the security side. | Keep these as Go **P1**. Record them as a security coverage gap to re-run. GO-02's "gates before body read" also shrinks pre-auth amplification, which is a security improvement. |
| X7 | GO-04: `statement_timeout`, `lock_timeout`, per-tick deadlines | Security impact is positive: it bounds the cost of slow-query abuse. The risk is that the account-deletion cascade (GO-27) and exports hit the timeout and fail **half-way**. The deletion runs in one transaction, so it rolls back cleanly, but the user then sees a failure. | Exempt or `SET LOCAL` long transactions: account deletion, exports, reports, migrations. Add a test that a heavy deletion either completes or rolls back fully. |
| X8 | GO-28: route-labelled latency metrics | `/metrics` is unauthenticated and enabled in production (security hardening note). Labels must not carry raw paths, usernames, slugs or IDs. | Label only by `c.Path()`, the route template, plus status class. Separately, move `/metrics` to an internal listener or behind `METRICS_SECRET`. |
| X9 | GO-09: `recover()` at tick and goroutine boundaries | A recovered panic logged with `%+v` of request or job state can write secrets to logs. C10 shows this codebase already leaks a token through an error string. | Log the stack and the job name only, not arguments. Combine with C10's fix: redact `*url.Error` and stop persisting raw `err.Error()` into `status_detail` and redirect query strings. |
| X10 | GO-17: add `AND user_id = $n` to `budget_plan_items` queries | Adding a predicate never drops a tenant check, so it is defence in depth. It is unsafe only if rows exist whose `user_id` differs from the snapshot owner. | Run the data check from GO-17 first. If it returns non-zero, use an index instead and investigate those rows as a possible isolation defect. |
| X11 | GO-15/16/18/27: indexes | No security effect. Every one is MEASURE FIRST. | Unchanged. Stage them behind a staging `EXPLAIN`. |
| X12 | C6: move per-user tax attestations off the shared `instruments` row | This is the largest change in the plan: a migration plus reads at five call sites (`dashboard.go`, `filing.go`, `funds.go`, ledgers, `brokers/taxlots.go`). The Go audit flagged nothing here. The same root cause also appears in IBKR sync and pilot writes to the catalog (an uncovered unit). | Do it as one design: a per-user table with a catalog fallback, and catalog fields writable only by trusted sync. Add a two-tenant regression test. Audit the existing `instruments` rows for user-written values before the cut-over. |

**Incompatible recommendations and how each is resolved:**
- X1: shared client versus a guarded client. Resolved by splitting the clients.
- X3: narrowing CIDRs versus MCP rate limiting. Resolved by doing both, plus the edge key.
- GO-04's request-deadline middleware versus SSE and uploads. Resolved by route exemptions; the Go audit already rejects a global `WriteTimeout`.
- GO-02's gate reorder versus `docs/vapor-bugs/money.md` F5 ("413 before Pro gate", kept). This needs an owner decision. The plan recommends the reorder, because it removes the pre-auth 52 MB buffering.

---

## 2. Prioritized implementation plan

Each item lists: what to do, the files involved, the source IDs, the status, the effort (S/M/L) and the risk. Each item also lists the test that must pass and its rollback.

### P0: do first

**P0-0. Land the reviewed working tree.** Both audits reviewed the uncommitted changes: job-lock session, billing transaction, scenario fence, watchlist reorder, export and shutdown drain, metrics. Commit them as reviewed, as separate PRs per concern.
- Status: done in the working tree, with tests passing (`go test -race ./...`, 83 packages, on a disposable Postgres) and `golangci-lint` reporting 0 issues.
- Effort: S.
- Risk: these PRs must land before the items below, which edit the same files (`cmd/api/main.go`, `billing/webhook.go`, `scenarios/worker.go`).

**P0-1. Telegram link code becomes first-party only; links are revoked when their issuer is revoked.**
- Mount `POST /v1/integrations/telegram/code` with `Session()+Guard()+FirstPartyOnly()`.
- Store the issuing principal on the code and the link.
- Delete messaging links in `RevokeAllTokens` and when the issuing token is revoked.
- Files: `internal/features/integrations/feature.go`, `internal/features/assistant/messaging.go`, `internal/platform/users/sessions.go`.
- Source: C1, unvalidated (hunter-proposed confirmed, proposed high).
- Effort: S. Risk: third-party integrations can no longer mint codes. That is the intended effect, so check whether MCP uses this route.
- Test: a PAT with `integrations:write` gets 403. After revoke-all, the link is gone.
- Rollback: revert the route middleware.

**P0-2. An email change requires proof of the new address, and recovery removes identities linked under an unproven email.**
- Make `PATCH /v1/users/email` pending until a code sent to the new address is entered.
- In `neutralizeUnverifiedAccount` and in `resetPassword` when `email_verified_at` is NULL, delete `oauth_identities` and passkeys.
- Files: `identity/profile.go:205-275`, `identity/oauth_login.go:337-364`, `identity/auth.go:618-637`, `db/queries/identity/auth.sql`.
- Source: C2, unvalidated (hunter-proposed confirmed, proposed high); parent spot-check matched.
- Effort: M. This is a new pending-email state, which is a client-visible change for the email-change flow, so it needs an owner decision on the wire format.
- Test: the two-subtest pre-hijack harness from `agents/h01-identity` (the attacker's Google sign-in must fail after the victim claims the email).
- Rollback: feature-flag the pending-email step. Keep the cleanup.

**P0-3. Fail closed on weak secrets at boot.**
- In production, refuse to start when `JWT_SECRET` is empty, equals `dev-secret`, or is shorter than 32 bytes.
- Apply the same rule to the default DB credentials.
- Reject unknown `APP_ENV` values.
- Files: `cmd/api/main.go:161`, `internal/config/config.go:41,59-63,79`.
- Source: C11, unvalidated (needs_validation). The fix is correct whatever the deployment answer.
- Effort: S. Risk: the pod fails to boot if production really lacks the secret. That is the point, but **run the owner check first** (namespaces `norviq` and `norviq-staging`, key length only) so the deploy is not the discovery.
- Test: `buildDeps` errors under `APP_ENV=production` without the secret.

**P0-4. Credential changes revoke every other session.**
- In `updatePassword` and `updateEmail`, set the cutoff to `now` and mint a fresh token for the caller, instead of using the caller's `iat`.
- Files: `identity/profile.go:196-197,260,315`, `platform/users/sessions.go:37`.
- Source: C4, unvalidated (hunter-proposed confirmed).
- Effort: S. The response gains a token, which is a client-visible addition, so the iOS app must store it.
- Test: a JWT minted after the caller's token gets 401 after the change.

**P0-5. Assistant tools respect token scopes.**
- Pass `*auth.ScopeContext` into `runCoordinator`.
- Map each read and write tool to the scope its REST route requires.
- Filter tool definitions and refuse execution for missing scopes.
- Files: `assistant/chat.go:92`, `coordinator.go`, `catalog.go:300`, `readtools.go`.
- Source: C3, unvalidated (hunter-proposed confirmed).
- Effort: M. Risk: MCP clients holding only `assistant:*` lose read tools. Update the consent text (`identity/scope_descriptions.go:40`) to match.
- Test: an `assistant:write`-only PAT gets a scope error from `list_goals` and succeeds with `goals:read`.

**P0-6. Passkeys: step-up to enroll, removal on reset, list and delete endpoints.**
- Files: `identity/webauthn.go:267-335`, `identity/auth.go:622-637`, `identity/feature.go`.
- Source: C8, unvalidated (hunter-proposed confirmed).
- Effort: M. Two new endpoints, so the client UI needs them.
- Test: after a reset, the old passkey's login returns 401.

**P0-7. A guarded outbound client for user-supplied URLs.**
- Create `netguard.Client`: a dial-time public-IP check covering v4-mapped, ULA, link-local, CGNAT and NAT64 addresses, `Proxy=nil`, and redirects refused or re-validated on every hop.
- Make `netguard.Validate` IP-semantic.
- Use it for the BYOK verifier and user-credential chat.
- Files: `platform/netguard`, `features/aiplatform/feature.go:35`, `platform/ai/platform.go:174`, `ai/verifier.go`.
- Source: C5, unvalidated (hunter-proposed confirmed), plus GO-05 (see X1).
- Effort: S/M. Low risk: the financing importer already uses this pattern.
- Test: the redirect and mapped-literal cases from `agents/h10-ssrf` reach nothing on loopback.

**P0-8. Finish the security run before declaring the security posture.**
1. Run the post-wave critic and the final-clean critic.
2. Hunt the 7 deferred units: files and exports, CSV/XLSX injection, upload parsers, quota and spend, billing logic, wildcard, chained trust boundaries.
3. Hunt the 4 uncovered surfaces: Facebook Limited Login nonce, cross-user UUID disclosure, catalog writes from broker sync, billing restore.
4. Give the 19 candidates Phase 3 and Phase 5 verification.

- Source: security run-1 §1 and §6.
- Effort: about 25–30 agent invocations, over the original 40 cap. Needs your budget approval.
- Why P0: the plan's P0 security items rest on unvalidated candidates, and whole attack classes are still deferred.

### P1: high impact, scheduled immediately after P0

**P1-1. Single-replica memory limits.**
- Add GOMEMLIMIT (about 80% of 450Mi).
- Set `SCENARIO_WORKER_MAX_CONCURRENT=1`.
- Add a large-body semaphore and run cheap gates before `ReadBody` on receipts and broker screenshots.
- Fail runs whose `started_at` is older than N leases.
- Then add a work budget on `paths×(months+1)` and an `attempt_count` poison guard.
- Files: infra `values-production.yaml`, `scenarios/runs.go:378`, `engine.go:329`, `worker.go`, `receipts/handlers.go`, `multipartform.go`.
- Source: GO-01 and GO-02 (CODE-SUPPORTED RISK, arithmetic), see X6.
- Effort: S, then M. Owner decides whether the work budget clamps or rejects.
- Measure: memprofile of one max-size run, and RSS under concurrent scans on staging.

**P1-2. Readiness stops failing on a Redis blip.**
- Report Redis unreachable as `degraded`.
- Bound each check and run them concurrently.
- Set probe `timeoutSeconds: 3`.
- Files: `server/health_core.go`, `server/server.go:236-275`, infra values.
- Source: GO-03. Effort: S.

**P1-3. Database and tick deadlines.**
- Set `statement_timeout`, `idle_in_transaction_session_timeout` and `lock_timeout` through `pgxpool.ParseConfig`.
- Add per-tick `context.WithTimeout`.
- Exempt migrations, account deletion, exports and reports.
- Files: `cmd/api/main.go:111-131`, `joblock.run`.
- Source: GO-04, see X7. Effort: M. Values are a starting point; tune them from pool metrics.

**P1-4. Telegram correctness and authority, in order.**
1. Whole-message approval words only, bound to first-party proposals made in that chat (C7, unvalidated, hunter-proposed confirmed).
2. A conditional `ClaimMessagingUpdate` (`AND last_update_id < @x`, `:execrows`), with the claim taken before transcription (GO-06, CONFIRMED DEFECT).
3. Per-chat serialization.

- Files: `assistant/messaging.go:156-176,466-560`, `messaging.sql:9-12`, `telegram_bridge.go`.
- Depends on P0-1. Effort: S, S, M.

**P1-5. Lower-impact unvalidated identity and credential fixes.**
- Atomic attempt claims for MFA and reset codes (`UPDATE … SET failed_attempts = failed_attempts+1 WHERE … < max RETURNING`), and resend must not reset the counter. Source: C9.
- Redact the IBKR SOD URL from errors, and stop persisting raw `err.Error()` into `status_detail` and callback redirects. Source: C10 plus the hardening notes.
- Both are unvalidated (hunter-proposed confirmed). Effort: S each.

**P1-6. Billing event correctness (merged item, see X4).**
- Ignore SANDBOX for entitlements, on both the webhook and restore paths.
- Add an ordering guard: a nullable `subscriptions.last_event_at` plus `FOR UPDATE` on the subscription row.
- Never grant Pro after expiry.
- Add a daily period-end reconciliation job.
- Handle TRANSFER.
- Sources: GO-10 (MEASURE FIRST), C14 and C15 (unvalidated, needs_validation), the h03 TRANSFER hardening note, and the restore uncovered surface.
- Order: run the read-only SQL first (inversion count, SANDBOX-granted users, expired-but-Pro users). Effort: M, with a migration. Rollback: a feature flag on the skip logic; the column is additive.

**P1-7. Shared tax attestation becomes per user.**
- Add a `user_instrument_tax_classifications` table with a catalog fallback.
- Remove the user-facing UPDATE on `instruments`.
- Sources: C6 (unvalidated, hunter-proposed confirmed; parent spot-check matched), see X12.
- Effort: L. It changes five read sites and needs a data review.

**P1-8. CI supply chain.**
- Pin `yq` by sha256, or use a sumdb-verified `go install`.
- Set `persist-credentials: false`.
- Pass `INFRA_TOKEN` only to the push step, or better, use a GitHub App token restricted to the staging values file through a PR.
- Add a top-level `permissions: {}`, and pin actions by SHA.
- Source: C16 (unvalidated, needs_validation) plus hardening. Effort: S.
- The owner checks the token's scope and the infra rulesets.

**P1-9. Owner checks on deployment facts.**
- IBKR mode and gateway reachability (C12).
- Bank or IBKR consent completed on another device (C13).
- Hermes route normalization (C17).
- Whether `/v1/ai/chat` is still called (C18).
- The OAuth redirect allowlist (C19).

Each settles a needs_validation candidate. Fixes only after the answer, except two that are safe regardless:
- C12: refuse IBKR gateway mode in production, and refuse sync for token-less connections.
- C17: `url.PathEscape` the symbol (and see X2).

### P2: justified, with clear validation

| Item | Source / status | Effort |
|---|---|---|
| Reclaim stranded report runs, and `WithoutCancel` in `failOrRetry` | GO-07, CONFIRMED DEFECT | S |
| One shutdown deadline, bounded Telegram drain and `pool.Close`, a closed flag before detached `Add`, `terminationGracePeriodSeconds: 45`, preStop | GO-08 | S |
| `recover` at tick and goroutine boundaries that fails the claimed rows (see X9) | GO-09 | S |
| Client timeouts and `LimitReader` on the listed reads (with X1's split client) | GO-05 | S |
| Shared atomic INCR+TTL helper; scenario cache `SET … EX` | GO-11, CONFIRMED DEFECT | S |
| Restart-proof bank backstop based on `last_synced_at`; record webhook sync errors | GO-12 | S |
| AI chain: `ctx.Err()` short-circuit, 402 cooldown, turn deadline, async alert (no sampling params or prefill on Haiku) | GO-13 | M |
| Symbol allowlist at write time, then partial quote results for valuation plus a negative cache (X2) | GO-14, C17 hardening | S then S |
| Idempotency header allowlist, `Set`, `WithoutCancel` | GO-23 | S |
| Per-user limiter after auth, narrowed trusted CIDRs, MCP edge key (X3) | GO-19, security hardening | M |
| `/v1/ai/chat`: deferred destructive actions plus write budget and audit, or retire the route (after the P1-9 answer) | C18 (needs_validation) | S/M |
| Client-held PKCE verifier or flow secret for social login (after the P1-9 answer) | C19 (needs_validation) | M |
| Plaid webhook: negative `kid` cache, rate limit, honour key `expired_at` | security hardening | S |
| OAuth AS: reject `javascript:`, `data:` and `vbscript:` at DCR; conditional approve; atomic refresh rotation; rate-limit DCR | security hardening | S each |
| Indexes and predicates GO-15, GO-16, GO-17 (X10), GO-18 | MEASURE FIRST (staging `EXPLAIN`) | S each |

### P3: optional, only once there is evidence

| Item | Status |
|---|---|
| Fence `CancelScenarioRun` on state (GO-20); lock the group row on join (GO-21); advisory-locked category seeding (GO-22) | APPROVE, small race fixes |
| Classify `PgError` as "answered" in joblock fail-open; session heartbeat (GO-24) | APPROVE |
| Log, count and continue per item in alert, digest, reporting and tax loops (GO-25) | APPROVE |
| `why-moved` own budget and negative cache (GO-26) | APPROVE |
| Route-labelled metrics (GO-28, X8); move `/metrics` internal; 499 for client cancel | APPROVE |
| Finnhub 429 Redis back-off (GO-29) | APPROVE |
| Community ban gate matches the route, not a path suffix; revoke invites of removed members; re-check bans on the realtime stream | security hardening |
| Revoke share links on archive; read-only resolver for public shares; `Cache-Control: private` | security hardening |
| Account-deletion FK indexes ranked by measured size (GO-27); the GO-30 bundle | MEASURE FIRST |

---

## 3. Rejected or deferred proposals (from both audits)

**Rejected:**
- A global `WriteTimeout`, `ReadTimeout`, Echo Timeout or `TimeoutHandler`: they break SSE and slow uploads.
- Raising `DATABASE_MAX_CONNECTIONS`: the Postgres budget is shared.
- A second replica: realtime is in-process.
- A generic retrying `RoundTripper`: it causes duplicate sends and double billing.
- LLM retries inside a rung, longer AI timeouts, or adding temperature/top_p/prefill on Haiku 5.5.
- Plaid webhook 5xx on a sync error.
- A DLQ or broker for push, email or Telegram.
- A circuit-breaker library or Redis-distributed singleflight.
- Indexing all 116 FKs.
- OFFSET-to-keyset conversion, which would change the wire format.
- SERIALIZABLE isolation.
- `UNIQUE(user_id,name)` on `expense_categories`.
- Caching `SessionsValidAfter` or suspended IDs in process.
- `sync.Pool` or a JSON library swap.
- Quantile sketches.
- A separate jobs Deployment, for now.
- A hosted transcription API.

**Deferred until the security run completes:** any fix whose only basis is a needs_validation candidate and which is not a pure fail-closed guard.

**Deferred until measured:** every index, quote batching, APNs concurrency, and the GO-30 bundle.

## 4. Verification and rollback per phase

- **Every PR:**
  - `go vet ./...`, `golangci-lint run ./...`, and `go test -race -count=1 ./...` against a disposable `TEST_DATABASE_URL`, never production.
  - A regression test encoding the invariant named in the item.
- **Security items:** reuse the hunter's sandbox harnesses as regression tests. They live under `~/security-audit-skill/norviq-backend-v2/run-1/agents/<hunter>/scratch/src/...` and are referenced from each candidate's `execution.instructions`.
- **Performance and availability items:** compare pool `AcquireDuration` and `EmptyAcquireCount`, `job_duration_seconds{job}`, readiness transitions and container RSS before and after on staging. Do not claim a speedup without these numbers.
- **Rollback:** each item names its own. Migrations in P1-6 and P1-7 are additive and nullable, and the old reads keep a fallback until cut-over.

## 5. Open decisions for the owner

1. Approve the budget to finish the security run (P0-8).
2. Choose the email-change UX: a pending address with a verification code (P0-2).
3. Choose the GO-01 work budget: reject or clamp, and at which cell count.
4. Choose the GO-02 gate order versus vapor-bugs F5 (413 before the Pro gate).
5. Whether MCP or other third-party clients rely on minting Telegram codes, on assistant reads with only `assistant:*` scopes, or on `/v1/ai/chat`.
6. Answers to the deployment-fact checks in P1-9, and the C11 owner check, before P0-3 ships.
