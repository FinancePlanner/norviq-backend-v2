<!-- Copy of /Users/fernandocorreia/security-audit-skill/norviq-backend-v2/run-1/REPORT.md (security-audit skill, run-1). Full run artifacts: /Users/fernandocorreia/security-audit-skill/norviq-backend-v2/run-1 -->

# Security audit — norviq-backend-v2 (run-1) — PARTIAL, INCOMPLETE RUN

Skill: `security-audit` (Cloudflare), full-audit mode, profile `standard`, budget 40 agent invocations, 17 spent.
Date: 2026-11-03.

## 1. Run status — read this first

**This run is incomplete. It contains no verified findings.**

- **Why incomplete.** The user asked to finish once the first hunter wave returned. These phases did not run:
  - Phase 2: the post-wave coverage critic and the final-clean coverage critic.
  - Phase 3: candidate validation.
  - Phase 5: record verification.
- **What that means for this report.** No candidate reached the skill's evidence bar for a finding, which is independent validation by a fresh verifier. `findings.json` is therefore an empty array, and both validators pass:
  - `validate-findings.cjs`: `PASS: 0 findings valid`
  - `validate-coverage-ledger.cjs`: `PASS: 32 coverage units valid`
- **Recorded reason.** `run-metadata.json` has `run_status: "incomplete"` with `incomplete_reason: stopped_by_user …`.
- **Unvalidated candidates.** The 19 candidates listed in section 3 are **unvalidated candidates, not findings**. Each was proposed by one hunter, and most were reproduced by that same hunter in the sandbox. No second agent has tried to refute them. Severities in section 3 are the hunter's proposals, not verdicts.
- **No coverage claim.** No critic ran, so this run makes no clean-coverage claim. 7 units are `deferred` (section 6).

**Scope and source.**
- Scope: the whole repository, `cmd/`, `internal/`, `db/`, CI and container files.
- Source: HEAD `81ce5ec37be56b8251c49d43f8fd2700aa3c8642` plus the dirty working tree, reviewed as is. The sha1 of `git diff` starts `167b3614c135`, and it was unchanged at the end of the run. Untracked files were not part of the audit inputs.
- Prior runs: none, so there are no carried records and no changed-source revalidations.
- Independence: hunters were told not to read `Golang-audit-plan.md`, `Go-backend-audit.md` or `Go-improvement-plan.md`. A result from an earlier, pre-skill substitute agent exists outside this run, and it was not used as hunter input.

**Execution boundary: sandboxed source and local only.** No deployed endpoint, provider or shared service was contacted. Target code ran only through `tools/run-sandboxed-test.sh`:
- **Compile.** A disposable source copy is compiled under macOS `sandbox-exec`. Outbound network is denied except loopback, writes are confined to the agent's scratch directory, the environment is an `env -i` allowlist, and CPU, file-size and process ulimits apply. The build targets `GOOS=linux`.
- **Run.** Test binaries run in Docker on an `--internal` network. I checked that the network has no external route and that the gateway is closed. Limits: `--memory 1g --cpus 1 --pids-limit 256 --read-only --cap-drop ALL --user 65534`, with read-only mounts.
- **Fixture.** The tests run beside a hardened disposable `postgres:16-alpine` fixture: uid 70, no capabilities, read-only rootfs, size-capped tmpfs. All data is dummy.
- **Evidence promotion.** Artifacts were promoted by `tools/promote.py`, which walks directories by file descriptor without following links, checks each file is a regular single-link file, creates the destination exclusively, and caps size at 256 KiB per file and 1 MiB total.
- **Residual gaps.**
  - The host compile step caps file size per file only. The Go compiler runs no target code: cgo is off and there is no `go:generate`.
  - `-race` is not available.
  - There is no Redis fixture.
  - Recon agent 1b wrote one file into the parent scratchpad, a list of id-only queries. It was retained as recon data.

## 2. Security posture summary

The authentication core is careful:
- HS256 is pinned, `exp` is required, and tokens are hashed.
- PKCE S256 is enforced and authorization codes are single-use and atomically claimed.
- Webhook verification is sound: Plaid ES256 with a body hash, RevenueCat HMAC, constant-time compares for Finnhub and Telegram.
- The admin allowlist fails closed.

Per-user ownership checks held across portfolios, expenses, budgeting, goals, banking, watchlist, reporting, social, groups, boards and articles. Dummy-tenant sandbox tests covered strangers, members and revoked members, and none crossed a tenant boundary.

The weak points are at **boundaries between features**:
- Token scopes stop at the route and do not carry into assistant tools.
- The Telegram link turns a narrow token into first-party authority.
- An email change does not require proving the new address, and linked identities and passkeys survive recovery.
- A shared catalog row (instruments) holds per-user tax attestations.
- The SSRF guard validates a string but not the connection.
- Several secret defaults depend on deployment configuration that is not visible here.

## 3. Unvalidated candidates (not findings)

| # | Proposed by hunter | Hunter-proposed severity (unverified) | Fingerprint | Title | Local evidence |
|---|---|---|---|---|---|
| C1 | confirmed | high | `assistant/telegram:integrations-write-link-code-grants-first-party-assistant` | A token holding only `integrations:write` mints a Telegram link code. The linked chat then reads portfolio data and confirms deletions as first-party, and it survives token revocation | `agents/h04-ai/artifacts/out/check-1.log` |
| C2 | confirmed | high | `identity/oauth-identity-retained-across-unverified-email-change` | Pre-account takeover: link your own Google identity, change the email to the victim's with no proof, and the identity survives the victim's Google sign-in or reset | `agents/h01-identity/artifacts/out/check-1.log` |
| C3 | confirmed | medium | `assistant-read-tools-ignore-token-scopes` | A token with only `assistant:write` reads goals, trades, expenses and portfolio data through the assistant read tools, while REST returns 403 for the same data | `agents/h02-tokens/artifacts/out/check-1.log` |
| C4 | confirmed | medium | `profile-credential-change-revocation-cutoff-uses-caller-iat` | A password or email change keeps newer foreign JWTs valid, and they can then mint a 365-day PAT | `agents/h02-tokens/artifacts/out/check-3.log` |
| C5 | confirmed | medium | `internal/platform/netguard/netguard.go:Validate:string-only-host-check-no-dial-or-redirect-enforcement` | The BYOK base-URL SSRF guard is bypassed through redirects and IPv4-mapped literals. It gives a status oracle on internal GETs | `agents/h10-ssrf/artifacts/out/check-1.log` |
| C6 | confirmed | medium | `internal/features/tax/service.go:ownedInstrument:shared-instrument-tax-classification-write` | Any holder rewrites the fund classification and market admission on the shared `instruments` row for every user | `agents/h06-finance/artifacts/out/check-1.log` |
| C7 | confirmed | medium | `assistant/messaging:parseConfirmationAnswer-substring-approves-latest-pending-action` | A Telegram message containing "do", "ok" or "go" approves the latest pending action, including one proposed by a token | `agents/h04-ai/artifacts/out/check-2.log` |
| C8 | confirmed | medium | `identity/passkey-survives-password-reset` | A passkey enrolled with a stolen session survives reset, and no endpoint can list or delete passkeys | `agents/h01-identity/artifacts/out/check-2.log` |
| C9 | confirmed | medium | `identity/otp-attempt-counter-lost-update` | Concurrent guesses exceed the 5-attempt cap on MFA and reset codes because the counter is a lost-update read-modify-write | `agents/h01-identity/artifacts/out/check-3.log` |
| C10 | confirmed | low | `brokers/sod:request-url-with-flex-token-persisted-and-logged` | An IBKR Flex token in the request URL leaks in plaintext to `status_detail`, the API response and logs on a transport error | `agents/h07-integrations/artifacts/out/check-3.log` |
| C11 | needs_validation | — | `internal/config/config.go:Base.JWTSecret:dev-secret-default-without-production-guard` | `JWT_SECRET` defaults to `"dev-secret"` with no production guard, so any session can be forged if the secret is missing. Forgery was reproduced locally. Blocker: is the production secret set? | `agents/h11-ops/artifacts/out/check-1.log` |
| C12 | needs_validation | — | `brokers/ibkr:gateway-mode-connect-attaches-shared-gateway-account` | IBKR gateway mode, and token-less connections, use the server's shared gateway account as brokerage authority. Blocker: production IBKR mode and gateway reachability | h03 check-3, h07 check-2 |
| C13 | needs_validation | — | `banking/callback:hosted-link-completion-not-bound-to-flow-owner` | A bank or IBKR consent completed by anyone attaches to the flow creator's account. Blocker: whether a provider's hosted URL can be completed on another device | `agents/h03-webhooks/artifacts/out/check-2.log` |
| C14 | needs_validation | — | `billing/webhook:revenuecat-environment-not-checked` | SANDBOX RevenueCat events grant production Pro and permanent tax packs. Blocker: RevenueCat environment filter, and which builds point at prod | `agents/h03-webhooks/artifacts/out/check-1.log` |
| C15 | needs_validation | — | `billing/webhook:stale-event-overwrites-newer-subscription-state` | A late or retried event overwrites newer state. Pro outlives the paid period because nothing reconciles `period_ends_at` | `agents/h03-webhooks/artifacts/out/check-1.log` |
| C16 | needs_validation | — | `github/workflows/ci.yml:bump-staging:unverified-yq-download-with-INFRA_TOKEN` | CI runs an unverified `yq` download in a checkout that holds `INFRA_TOKEN`, which pushes to the auto-deploying infra repo | source only |
| C17 | needs_validation | — | `internal/features/insights/provider.go:HermesProvider.FetchTickerPosts:unescaped-symbol-path` | A stored stock symbol rewrites the path of the request that carries the Hermes bearer token. Blocker: how Hermes routes such paths | `agents/h10-ssrf/artifacts/out/check-2.log` |
| C18 | needs_validation | — | `assistant/chat:aiChat-inline-model-confirm-destructive-no-budget` | `/v1/ai/chat` runs destructive tools on the model's own `confirm:true`, with no budget or audit, next to third-party insight text. Blocker: real model behavior, and whether clients still call the route | `agents/h04-ai/artifacts/out/check-3.log` |
| C19 | needs_validation | — | `identity/oauth-login-flow-unbound-server-held-pkce` | The social-login flow is not bound to the initiating client, and the PKCE verifier is held by the server. Blocker: production redirect allowlist and client platform | source only |

**Merged during consolidation:**
- h01 `auth/jwt-secret-default-dev-secret-no-production-guard` was merged into C11 (same root cause).
- h07 `brokers/syncDataClient:tokenless-ibkr-connection-uses-shared-gateway` was merged into C12 (same root cause: the shared gateway client acts as user authority).

**Parent source spot-checks.** These are not a verification pass, but each one matched the hunter's claim:
- C1: `integrations/feature.go:105-110` and `assistant/messaging.go:416`.
- C2: `oauth_login.go:352-358` and `profile.go:263`.
- C6: `tax/service.go:218-242`.

The full candidate records, including traces, conditions, remediation and the exact sandbox commands, are in `work/results/<hunter>.json`. The deduplicated list is in `work/unvalidated-candidates.json`.

**How to finish this run:**
1. Run the two critics.
2. Give each candidate a fresh Phase 3 verifier (about 19 agents), then Phase 5 verifiers.
3. Write the verified records to `findings.json`.

None of the candidates above should be quoted as a confirmed vulnerability until that is done.

## 4. Needs-validation leads: owner-observed checks

These are deployment facts the owner can check without sending audit traffic to production:
- **C11:** confirm that `JWT_SECRET` in namespaces `norviq` and `norviq-staging` comes from a non-optional `secretKeyRef` and is at least 32 bytes. Check the key name and length only; do not print the value.
- **C12:** check `IBKR_CONNECT_MODE`, the `IBKR_OAUTH_*` variables and `IBKR_API_BASE_URL`, and whether anything answers `/portfolio/accounts` from the pod. Then run, read-only, a query for external IDs connected to more than one user.
- **C13:** with two sandbox Norviq accounts, complete a provider sandbox consent on a different device and see which account it lands on.
- **C14:** check the RevenueCat webhook environment filter. Then run, read-only, `SELECT count(*) FROM billing_events WHERE raw_payload::jsonb->'event'->>'environment'='SANDBOX' AND user_id IS NOT NULL`.
- **C15:** run, read-only, a query for users holding Pro whose subscription has `period_ends_at < now()-3 days`.
- **C16:** check the type and scope of `INFRA_TOKEN`, the infra `main` rulesets, and whether the yq release is immutable.
- **C17:** review the Hermes route table and its path normalization.
- **C18:** check whether clients still call `/v1/ai/chat`.
- **C19:** review the production `OAUTH_ALLOWED_REDIRECT_URIS` and how the app handles custom schemes.

## 5. Hardening notes and positive patterns

Hunter hardening notes. None of these is a finding:
- **Auth.**
  - DCR accepts `javascript:` and `data:` redirect schemes. Only the external consent page filters them.
  - OAuth approve has no `status='pending'` predicate.
  - Refresh rotation (first-party and OAuth) is check-then-act.
  - A deleted user's JWT authenticates as a ghost id.
  - PAT minting and consent need no re-authentication.
  - WebAuthn uses `allowCrossOrigin=true` and `verifyUser=false`.
  - OAuth and passkey sign-in skip email MFA.
- **Community.**
  - The ban gate exempts any path ending in `/community/me` (an encoded slash reaches the exemption, but no state was reachable).
  - Group invites stay usable after the inviter is removed.
  - The realtime stream does not re-check bans mid-stream.
- **Portfolio.**
  - Archiving does not revoke share links.
  - An anonymous read of an "all" share link writes a row.
  - The share response has `Cache-Control: public`.
  - A `portfolio:write` PAT can publish share links and invite editors.
  - Invitation acceptance checks `users.is_verified`, which no v2 code sets.
  - Members can see sector labels derived from the owner's private notes.
- **Ops.**
  - `/metrics` is unauthenticated when enabled, and production has `PROMETHEUS_ENABLED=true`. It exposes only aggregate counters.
  - pprof will bind any address.
  - Every fail-closed branch keys on an exact `APP_ENV=="production"`.
  - The DB defaults to `sslmode=disable` with default credentials.
  - The default trusted-proxy CIDRs include all RFC1918 ranges plus 100.64/10.
  - Swagger is loaded from unpkg without SRI.
  - CI actions and base images are pinned by tag, not digest, and the workflow has no top-level `permissions`.
- **Crypto.**
  - Credential AAD binds the context, not the row or user.
  - The PII key falls back to a fixed value outside production.
  - Legacy unprefixed values decrypt as plaintext.
  - The `INSIGHTS_SYMBOLS_TOKEN` compare is not constant-time.
  - Re-registering a push token moves it to the last registrant.
- **SSRF and outbound.**
  - The financing importer's deny set lacks NAT64 and reserved ranges.
  - StockTwits and ECB URLs carry unescaped values, with no credential impact.
  - Symbol writes accept any characters.
- **Webhooks.**
  - A random Plaid `kid` triggers an outbound key fetch on every request, with no negative cache and no rate limit, and key expiry is ignored.
  - RevenueCat TRANSFER events are rejected with 400 and not handled.
  - Callback redirects carry raw DB error text.

**Positive patterns to keep:**
- The financing URL importer's dial-time IP check and per-hop redirect check. It is the model for fixing C5.
- Atomic code claims and `FOR UPDATE SKIP LOCKED` queues.
- `portfolios.Access.Require` with composite child lookups.
- Field-allowlist decoding, which closes off mass assignment.
- Fail-closed production checks for the PII key, report signing, RevenueCat secrets, CORS and the Telegram secret.

## 6. Coverage summary (from `coverage-ledger.json`)

- **Units: 32.** 16 `candidate`, 9 `covered`, 0 `blocked`, 7 `deferred`.
- **Deferred.** The reason recorded for each is `stopped_by_user: hunter terminated before returning a structured result`.
  - Files and exports (h09): the export and lifecycle unit, CSV/XLSX injection, and upload/import parsers.
  - Abuse (h12): quota and spend containment, and billing/entitlement business logic.
  - Wildcard and chained trust boundaries (h13).
- **Uncovered surfaces reported by hunters.** These are queued for a critic and have no ledger unit yet:
  - Facebook Limited Login nonce binding in social discovery (h08).
  - Cross-user disclosure of user UUIDs, which decides how exploitable C11 is (h11).
  - Shared instrument catalog writes from IBKR sync and pilots (h06).
  - `/v1/billing/restore`: sandbox handling, and two accounts holding Pro on one subscription (h03).
- **Critics.** The post-wave critic did not run and the final-clean critic did not run. **No complete-coverage claim is made.**
- **Excluded companions:**
  - `CLIENT-SIDE.md`: no browser code.
  - `DESKTOP-MOBILE-AND-LOCAL-IPC.md`: the iOS app is not in this repo.
  - `MEMORY-SAFETY-AND-BINARY.md`: pure Go with cgo off.
