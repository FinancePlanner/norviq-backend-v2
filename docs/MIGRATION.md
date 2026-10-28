# Vapor → Go migration plan

Goal: replace `norviq-backend` (Swift 6 / Vapor 4, ~120k lines, 646 routes,
159 Fluent migrations, ~30 background jobs) with this Go service, serving the
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
  `V2_DISABLED_ROUTES` sends any route back to Vapor without a deploy. The
  ingress pointing back at the Vapor service is the full rollback.
- Shared Redis keeps rate-limit buckets and idempotency keys consistent across
  both processes. The Vapor-compatible cache format is in
  `internal/middleware/ratelimit.go`.
- Background jobs move one at a time: on in v2 (`V2_JOBS`), off in Vapor, in
  the same infra change.

## Schema ownership

- Until cutover, **Vapor's migrate job owns the schema**. v2's chart runs no
  migrate job.
- `db/migrations/00001_baseline.sql` is the schema Vapor's 159 migrations
  produce, verified by migrating a scratch DB both ways and diffing
  `pg_dump`s (zero lines of difference).
- At cutover `norviq-api migrate` adopts the production DB: when it finds
  `_fluent_migrations` but no goose table, it checks every Fluent migration the
  baseline captured is applied, then stamps the baseline without running it.
  It refuses if Vapor has migrations newer than the baseline.
- **Feature freeze risk:** anything merged into Vapor after the snapshot
  (`.norviq-backend-snapshot`, commit in the baseline header) must be ported
  too: new routes, new migrations (as goose migrations). Keep Vapor changes to
  fixes during the migration.

## Order

| Wave | Domains | Why first |
|---|---|---|
| 0 | Foundation: wire format, errors, auth, rate limit, idempotency, client IP, health, strangler, parity tool, test DB | everything depends on it (**done**) |
| 1 | identity (auth, users, OAuth server, PATs, crypto, mailer), billing/entitlements, market data platform, notifications/push, AI provider platform | shared services the other domains call |
| 2 | portfolio, expenses/budget/receipts, tax, social/community, banking/brokers, AI features, news/macro/crypto/brief | the bulk of the routes |
| 3 | planning, scenarios, goals, retirement, rebalancing, automation, financing, statistics, reporting, recap, pilots, terminal positions, export, sharing, messaging, internal metrics | the remaining routes |
| 4 | jobs flip, full parity sweep on staging, cutover, archive Vapor | |

Progress: `scripts/coverage.sh`.

## Hosting (platform/infra)

- `apps/norviq/api-v2/values-*.yaml` with the existing `charts/norviq-app`,
  release name `norviq-backend-v2`, same `api-env` secret and sealed secrets
  as Vapor (both run in namespace `norviq` / `norviq-staging`, so the existing
  sealed blobs decrypt).
- `LEGACY_UPSTREAM_URL=http://api:8080` (the Vapor service).
- Staging first: point the staging ingress (port-forward only today) at v2,
  run the parity suites against both, then production.
- Cutover: Traefik `api.norviq.org` → `norviq-backend-v2`; Vapor stays
  deployed (no ingress) as the proxy target until coverage hits 646/646 and
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
