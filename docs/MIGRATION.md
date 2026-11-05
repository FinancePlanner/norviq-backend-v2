# Vapor → Go migration (finished 2026-10-30)

This service replaced `norviq-backend` (Swift 6 / Vapor 4, ~128k lines, 693
routes, 163 Fluent migrations, ~30 background jobs). It serves the same
endpoints to iOS, web-v2 and norviq-mcp with the same wire contract, and no
client release was needed.

**Vapor is archived for good.** GitHub `FinancePlanner/norviq-backend` is
read-only, no Vapor service runs in the cluster, and there is no rollback to
it. No code goes to the Swift repo: every fix, including fixes for bugs ported
from Vapor, lands here.

## Schema

v2 owns the schema. Its migrate job runs as an ArgoCD PreSync hook, which
takes a `pg_dump` first.

- `db/migrations/00001_baseline.sql` is the schema Vapor's first 159
  migrations produced (norviq-backend e6e35df). `00002_vapor_37128c4.sql` adds
  the 4 that Vapor gained by 37128c4 (groups, achievements, digests, social
  activity). Both were checked by migrating scratch DBs both ways and diffing
  the `pg_dump`s (zero lines of difference).
- **Goose adoption.** `internal/migrate/fluent_migrations.txt` maps each goose
  version to the Fluent migrations it mirrors. When `norviq-api migrate` finds
  `_fluent_migrations` but no goose table, it stamps every version whose
  Fluent migrations are all applied, without running them, then runs the
  rest. It refuses a version that is applied only in part, and any Fluent
  migration that no goose version mirrors. Production and staging were
  adopted this way at cutover. The code stays because it is what makes an old
  Vapor-migrated dump usable.

### Schema changes after cutover

A new change is the next numbered file in `db/migrations`, with no section in
`fluent_migrations.txt`. Keep it safe on a populated table under traffic: a
nullable column with no default, `lock_timeout` around any `ALTER TABLE`, and
`CREATE INDEX CONCURRENTLY` under `-- +goose NO TRANSACTION` (goose then runs
each statement on its own, on one connection). Drop a same-named index first,
so a retry after a failed concurrent build does not keep an INVALID one.
`00003_expenses_external_id.sql` is the worked example, and
`internal/migrate/externalid_db_test.go` is its test.

## Background jobs

Jobs start only when they are named in `V2_JOBS` (comma-separated, or `all`).
The default is none, so a local run or a one-off command sends no pushes or
emails. Production and staging set `V2_JOBS=all`. Each job wraps its tick in
`joblock.RunAsLeaderWithSession` with the Swift job's lock name (a Postgres
advisory lock on `hashtext(name)`, held for one tick), so replicas never run
a tick at the same time. Every lock a process holds lives on one dedicated
connection (`joblock.Session`, dialed outside the pool and redialed after
it dies), so a tick never keeps a pool connection checked out while its body
needs the pool. The cost: losing that connection releases every lock this
process holds, and running ticks are not told. That is accepted at one replica,
where only a rollout's surge pod could take a lock. Revisit it before raising
the replica count.

On shutdown, one budget measured from SIGTERM keeps the process inside 30
seconds, the kubelet's default grace period (`cmd/api/main.go`; the chart sets
45 with a 5 second preStop sleep, so this leaves room): HTTP requests
get 15 seconds; Telegram turns, jobs and detached work (exports, group push
fan-out, live achievement checks) share what is left until 24 seconds; the
lock session and the pool then get 2 and 3 seconds to close. Names still
running at each deadline are logged. Exports and fan-out stop accepting new
work before the wait, so a handler that outlived `Shutdown` gets a 503 for an
export (its row is marked failed) and a skipped group push.

## Realtime: one replica

Realtime is in-process, as it was in Vapor: there is no Redis pub/sub. An SSE
client on `/v1/realtime/stream` sees only the events published by the same
process. The groups routes (`/v1/groups`, `/v1/group-channels`,
`/v1/group-invites`, `/v1/group-messages`) publish to it. **Keep api-v2 at one
replica** until the bus is moved to Redis.

## Shared state with Vapor's formats

Redis keys and values (rate-limit buckets, idempotency keys, caches, the AI
daily cap), sealed secrets and env variable names are Vapor's, unchanged.
That is why the code reads `DATABASE_*`, `JWT_SECRET` and the feature flags
under Vapor's names.
The config loader ignores env variables it does not know, so leftovers in the
infra values are harmless.

## Deliberate deviations from Vapor

Vapor bugs were ported faithfully unless they are listed here or in
`docs/vapor-bugs/`. Each fix is made in Go only.

- **RevenueCat decoding** (billing): Vapor's `JSONDecoder.backendAPI`
  rewrote RevenueCat's snake_case keys to camelCase, so every snake_case
  field decoded as nil. `/v1/billing/restore` downgraded paying users to
  free, and `/v1/billing/management-url` never returned RevenueCat's URL or
  the web-billing portal. v2 decodes RevenueCat bodies with plain
  `encoding/json` (`internal/features/billing/revenuecat.go`).
- **Job lock fail-open, narrowed** (`internal/platform/joblock`): Vapor ran
  the tick unlocked whenever the lock could not be taken. v2 still does when
  the database is unreachable: the lock session could not dial, and the pool
  can neither ping nor shows every connection checked out. In every other
  case it skips the tick and logs, because the database is up and running
  unlocked would duplicate sends: a Postgres error such as 53300 during a
  rollout, a lock statement failing on an established connection, a pool
  that pings, or a pool too busy for the ping to get a connection.
- **Idempotency fails closed on a Redis read error** (`middleware/ratelimit.go`):
  Vapor treated any `GET` failure as a cache miss and ran the handler, so a
  retry could commit the same write twice. v2 answers 503 in production
  before the handler runs. A replay now restores only the handler's own
  headers (`Content-Type`, `Location`, `Content-Disposition`,
  `Cache-Control`, `ETag`, `Last-Modified`) with `Set`; the global middleware
  sets the rest, so a replay no longer duplicates them or labels a plain
  body gzip. The cache write survives a client disconnect.
- **Global idempotency was missing from the port until
  `fix/idempotency-global-mount`** (`server.New`): Vapor mounted the middleware
  on every route when Redis was configured, and the port kept only the grouped
  mounts, so a repeated key on `/v1/expenses` and similar routes ran the write
  again from the 2026-10-30 cutover. Keys from that window were never stored,
  so a retry of a write from then is not replayed.
- **Enable Banking pagination** fails a fetch whose provider sends a
  continuation key it already sent in that fetch (back to back or in a
  cycle), instead of looping until shutdown.
- **RevenueCat webhook, one transaction** (billing): Vapor committed the
  event row before the entitlement writes, so a failed write was skipped as
  a duplicate on redelivery. v2 writes both in one transaction; the loser of
  two overlapping deliveries answers 200 without reapplying.
- **Target alerts mark before they send; earnings reminders record after a
  delivered push** (watchlist): Vapor's order re-sent a target push every
  poll while the mark kept failing, and counted a failed earnings push as
  sent. A crash between mark and send now loses one target push; a crash
  between send and record can send one extra earnings push.
- **Scenario run writes are fenced** on `lease_owner` and `state =
  'running'`: a reclaimed or user-canceled run is never completed or failed
  by the worker that lost it. A heartbeat that finds the run gone cancels the
  simulation. One that cannot reach the database also cancels it, but leaves
  the run leased rather than failed, so another worker claims it after expiry.
- The bugs that were ported faithfully, and the owner's decision on each (fix
  or keep), are in `docs/vapor-bugs/money.md`, `docs/vapor-bugs/portfolio.md`
  and `docs/vapor-bugs/social.md`. Tests that pin those decisions cite the
  file.

## Reference material

- `docs/vapor-routes.txt`: every route Vapor mounted (693, every feature flag
  on). `scripts/coverage.sh` compares it with `go run ./cmd/api routes`; v2
  serves 693/693.
- `cmd/parity` and `parity/*.jsonl`: the tool and cases that diffed v2
  against a running Vapor during the port. They need a Vapor build to run
  against, so they are history now, not a check to run.
- Code comments such as "ported from Vapor's X" explain why code is shaped the
  way it is. Vapor's behavior is the contract that clients were built
  against. That is why it still matters, even though Vapor no longer runs.

## History (2026-10-30)

- **Strangler.** From its first deploy, v2 took all API traffic behind
  `api.norviq.org`. It reverse-proxied every route it had not registered to
  Vapor (`LEGACY_UPSTREAM_URL`). Domains moved over as their parity suites
  passed. `V2_NATIVE_PREFIXES` widened production one prefix at a time, and
  `V2_DISABLED_ROUTES` could send a route back to Vapor. Jobs and the schema
  stayed with Vapor until the cutover. Both processes shared Postgres and
  Redis.
- **Cutover.** Staging was rehearsed first (infra #509, #510). Production
  went to wave 1 at 487/693 routes (#511), then to the full cutover at 14:41
  UTC (#513): Vapor was scaled to 0, and all routes and jobs moved to v2.
  `LEGACY_UPSTREAM_URL` was dropped at 14:52 (#515). The Vapor Applications
  were removed and api-v2 took over the ingress and the migrations (goose
  adoption) on staging (#516) and production (#517). MCP now calls api-v2 in
  the cluster (#520). The Vapor values were folded into
  `apps/norviq/api-v2/` and `api` was dropped from `promote-norviq` (#524).
- **Strangler removed.** The proxy, `LEGACY_UPSTREAM_URL`,
  `V2_NATIVE_PREFIXES` and `V2_DISABLED_ROUTES` were removed from the code
  after the cutover. With no upstream set, the proxy had already passed every
  request straight to echo. An unregistered route answers echo's 404, or 405
  with `Allow`, in the JSON error envelope (pinned by `TestUnmatchedRoutes`
  in `internal/server`).
