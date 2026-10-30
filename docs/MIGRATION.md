# Vapor → Go migration plan

Goal: replace `norviq-backend` (Swift 6 / Vapor 4, ~128k lines, 693 routes,
163 Fluent migrations, ~30 background jobs) with this Go service, serving the
same endpoints to iOS, the Go web app, web-v2 and norviq-mcp, without any
client release. Then archive the Vapor repo.

## Approach: strangler behind the same hostname

```
              api.norviq.org (Traefik)
                       │
               norviq-backend-v2  ──── route ported? ── yes ──▶ Go handler
                       │                                          │
                       no                                         │
                       ▼                                          ▼
               norviq-backend (Vapor) ───────── same Postgres + Redis
```

- v2 takes all API traffic from the first deploy. Unported routes are proxied
  to Vapor untouched, so deploying v2 changes nothing observable.
- Each domain flips when its parity suite passes, simply by being registered.
  `V2_DISABLED_ROUTES` sends any route back to Vapor without a deploy.
  `V2_NATIVE_PREFIXES` (e.g. `/v1/auth,/health,/.well-known`) narrows v2 to
  the ported routes under those prefixes, so production can widen domain by
  domain with a values change.
- **Realtime is in-process** (as in Vapor; no Redis pub/sub). An SSE client on
  `/v1/realtime/stream` only sees events published by the same process, so
  `/v1/realtime` must flip together with every route that publishes to it:
  `/v1/groups`, `/v1/group-channels`, `/v1/group-invites`,
  `/v1/group-messages`. Keep api-v2 at one replica. The
  ingress pointing back at the Vapor service is the full rollback.
- Shared Redis keeps rate-limit buckets and idempotency keys consistent across
  both processes. The Vapor-compatible cache format is in
  `internal/middleware/ratelimit.go`.
- Background jobs move one at a time: on in v2 (`V2_JOBS`), off in Vapor, in
  the same infra change. Most Vapor jobs have **no disable flag** (e.g.
  TargetAlertPoller, EarningsNotificationPoller are always registered); they
  run under `JobLock.runAsLeader(name)`, a Postgres advisory lock on
  `hashtext(name)` held for **one tick**. `internal/platform/joblock` takes the
  same lock, so the two copies never run a tick concurrently, but each still
  runs one tick per interval, so a non-idempotent job (tips, digests, pushes)
  would fire twice. Plan: **every job stays on Vapor until the final cutover**,
  when Vapor scales to zero and `V2_JOBS=all` lands in the same change
  (rehearsed on staging first). Ported jobs must wrap each tick in
  `joblock.RunAsLeader` with the Swift job's lock name.

## Schema ownership

- Until cutover, **Vapor's migrate job owns the schema**. v2's chart runs no
  migrate job.
- `db/migrations/00001_baseline.sql` is the schema Vapor's first 159
  migrations produce (norviq-backend e6e35df). `00002_vapor_37128c4.sql` adds
  the 4 Vapor gained by 37128c4 (groups, achievements, digests, social
  activity). Both were verified by migrating scratch DBs both ways and
  diffing `pg_dump`s (zero lines of difference).
- `internal/migrate/fluent_migrations.txt` maps each goose version to the
  Fluent migrations it mirrors. At cutover `norviq-api migrate` adopts the
  production DB: when it finds `_fluent_migrations` but no goose table, it
  stamps every version whose Fluent migrations are all applied, without
  running them, then runs the rest. It refuses a version applied only in
  part, and any Fluent migration no goose version mirrors.
- **Vapor keeps moving:** anything merged into Vapor after the snapshot
  (`.norviq-backend-snapshot`, now 37128c4) must be ported too. To catch up:
  move the snapshot, rebuild, migrate a scratch DB with Vapor, generate the
  next goose migration from the `pg_dump` diff, add its `# goose N` section,
  verify a zero-line diff, and regenerate `docs/vapor-routes.txt` with every
  `*_ENABLED` flag on.

## Deliberate deviations from Vapor

Vapor bugs are ported faithfully unless listed here. Each fix is v2-only;
Vapor is archived after cutover, not patched. **No line of code is added to
the Vapor repo.** The faithfully ported bugs are fixed in Go, after cutover.

- **RevenueCat decoding** (billing): Vapor's `JSONDecoder.backendAPI`
  rewrote RevenueCat's snake_case keys to camelCase, so every snake_case
  field decoded as nil. `/v1/billing/restore` downgraded paying users to
  free, and `/v1/billing/management-url` never returned RevenueCat's URL or
  the web-billing portal. v2 decodes RevenueCat bodies with plain
  `encoding/json` (`internal/features/billing/revenuecat.go`). Until cutover
  a user restoring through a route still served by Vapor gets the old result.

## Order

| Wave | Domains | Why first |
|---|---|---|
| 0 | Foundation: wire format, errors, auth, rate limit, idempotency, client IP, health, strangler, parity tool, test DB | everything depends on it (**done**) |
| 1 | identity (auth, users, OAuth server, PATs, crypto, mailer), billing/entitlements, market data platform, notifications/push, AI provider platform | shared services the other domains call (**done**) |
| 2 | portfolio, expenses/budget/receipts, tax, social/community, banking/brokers, AI features, news/macro/crypto/brief | the bulk of the routes |
| 3 | planning, scenarios, goals, retirement, rebalancing, automation, financing, statistics, reporting, recap, pilots, terminal positions, export, sharing, messaging, internal metrics | the remaining routes |
| 4 | jobs flip, full parity sweep on staging, cutover, archive Vapor | |

Progress: `scripts/coverage.sh` (693/693 since 5fce96b).

**The Vapor repo is archived** (GitHub `FinancePlanner/norviq-backend`, read-only
since 2026-10-30). New backend features land here only. Vapor still runs as
release `api` until cutover: the proxy target for prefixes not yet in
`V2_NATIVE_PREFIXES`, and the owner of every background job and the schema.

## Hosting (platform/infra)

- `apps/norviq/api-v2/values-*.yaml` with the existing `charts/norviq-app`,
  release name `norviq-backend-v2`, same `api-env` secret and sealed secrets
  as Vapor (both run in namespace `norviq` / `norviq-staging`, so the existing
  sealed blobs decrypt).
- `LEGACY_UPSTREAM_URL=http://api:8080` (the Vapor service).
- Staging first: point the staging ingress (port-forward only today) at v2,
  run the parity suites against both, then production.
- Cutover: Traefik `api.norviq.org` → `norviq-backend-v2`; Vapor stays
  deployed (no ingress) as the proxy target until coverage hits 693/693 and
  all jobs run in v2. Then remove `LEGACY_UPSTREAM_URL`, enable v2's migrate
  job, scale Vapor to zero, and archive the repo.

## Coordination with web-v2 (SvelteKit on Cloudflare Workers)

Agreed with the web-v2 session: backend v2 goes first, behind the same host.
That is invisible to clients. web-v2 builds against today's contract. Its
order: (1) v2 owns api.norviq.org, (2) web-v2 parity, (3) norviq.org moves to
the Worker, with the Go web kept for rollback.

API-side needs from web-v2:

- **Client IP from the edge:** web-v2 sends `X-Norviq-Client-IP`
  (from `CF-Connecting-IP`) and `X-Norviq-Edge-Key`. v2 trusts the IP only
  when the key equals `EDGE_CLIENT_IP_KEY` (`internal/middleware/clientip.go`).
  `/v1/auth` must be ported before web-v2's cutover so login/signup rate
  limits key per user IP.
- **Preview origins:** `WEBAUTHN_ORIGINS` and OAuth redirect URIs gain the
  preview host when it exists (config only).
- **Staging access:** the staging API has no public ingress. A protected one
  for a hosted web-v2 preview is the owner's decision.

## Removed from platform/infra at cutover (not before)

Each item below is still load-bearing until Vapor scales to zero:

- `norviq-api-{staging,production}` Argo Applications (Vapor release `api`)
  and the Vapor migrate job. First fold the env and secrets api-v2 inherits
  from `apps/norviq/api/values-*.yaml` into `apps/norviq/api-v2/`, because
  api-v2 layers on them today.
- `LEGACY_UPSTREAM_URL`, `V2_NATIVE_PREFIXES` and `V2_DISABLED_ROUTES` in the
  api-v2 values; `V2_JOBS=all`; enable v2's migrate job (goose adoption).
- `ingress.serviceName: api-v2` on the Vapor values: the ingress moves to the
  api-v2 release itself, and the PVCs get owned by it.
- The `api` option in `promote-norviq.yml`.
