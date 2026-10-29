# Porting a Vapor controller to v2

v2 replaces `norviq-backend` (Swift/Vapor) route by route. Clients (iOS, the Go
web app, web-v2 on Cloudflare, norviq-mcp) do not change, so **every ported
route must answer exactly as Vapor does**: same method and path, status codes,
JSON keys, value formats, error envelope, auth rules, rate limits, and side
effects in the database. When in doubt, Vapor's behavior wins over what looks
cleaner. Port bugs faithfully, but write each one down in your report.

## How the strangler works

v2 serves the routes registered in `internal/features`. Any other request is
reverse-proxied to Vapor untouched (`internal/server/strangler.go`). Both
processes share one Postgres and one Redis. So:

- Port a route completely or not at all. Partial behavior is worse than
  proxying.
- `V2_DISABLED_ROUTES="GET /v1/foo/:id,..."` sends a ported route back to
  Vapor without a deploy. It is the rollback switch.
- **Vapor still owns the schema** until cutover. Do not add goose migrations
  that change existing tables. If a port truly needs one, stop and report it.
- **Background jobs run in exactly one process.** v2 jobs start only when
  named in `V2_JOBS`, and each flips there at the same time as Vapor's copy is
  switched off. Port the job, and say in your report which Vapor env/flag turns
  Vapor's copy off.

## Source of truth

| What | Where |
|---|---|
| Every route, as Vapor mounts it | `docs/vapor-routes.txt` (693 routes, every feature flag on) |
| Vapor sources (snapshot this port targets) | `/Users/fernandocorreiachill/Work/production/apps/norviq/.norviq-backend-snapshot/Sources/StockPlanBackend` |
| Vapor tests | `.../.norviq-backend-snapshot/Tests/StockPlanBackendTests` |
| Route registration and middleware per group | `.../StockPlanBackend/routes.swift` and each controller's `boot(routes:)` |
| Request/response DTOs (shared with iOS) | `/Users/fernandocorreiachill/Work/production/apps/norviq/norviq-shared/Sources/StockPlanShared` (backend pins v5.25.0) |
| DB schema | `db/migrations/00001_baseline.sql` (exactly what Vapor's migrations produce) |

Read the controller, its service/repository, its models, and its tests before
writing Go. Port the tests too.

## Layout

```
internal/features/<domain>/      handlers, services, DTOs. One package per domain
  feature.go                     type Feature struct{}; Name(); Register(d, r)
  <domain>_test.go               tests ported from the Swift suite
db/queries/<domain>/*.sql        sqlc queries -> internal/store/<domain>db
internal/platform/<name>/        services shared across domains (market data,
                                 entitlements, push, mailer, AI providers, crypto)
parity/<domain>.jsonl            parity cases (see below)
```

Register the feature in `internal/features/features.go` (`All()`), and its
jobs in `AllJobs()`.

**sqlc:** put queries in `db/queries/<domain>/`, then run `make generate`. It
rewrites `sqlc.yaml` from the directory list (never edit it by hand) and
generates `internal/store/<domain>db`. Prefer sqlc for every query. Use
hand-written pgx (`d.DB`) only where sqlc cannot express the SQL (truly
dynamic filters or sorts), and keep that SQL next to its sqlc siblings.

**Config:** each feature declares its own settings struct with `env:` tags,
using Vapor's variable names unchanged, and loads it with
`config.Load[MySettings]()`. For Vapor's lenient booleans (`1/true/yes/on`)
read a string and use `config.Bool(raw, default)`.

## Wire format: the parts that bite

The `apijson` package and `httpx.JSON` handle encoding. Your job is to pick
the right Go types.

| Swift | Go field | Notes |
|---|---|---|
| `Date` | `apijson.Time` | `"2026-10-28T12:00:00Z"`: UTC, **no fractional seconds** |
| `Date?` | `*apijson.Time` + `omitempty` | |
| `UUID` | `apijson.UUID` | **uppercase**, as Swift's `uuidString` |
| `String` holding `id.uuidString` | `string` via `apijson.FormatUUID(id)` | also uppercase |
| `Double` / `Int` / `Bool` / `String` | `float64` / `int` / `bool` / `string` | **never** `omitempty` on non-optionals: `0` and `false` must be sent |
| `X?` (optional) | `*X` + `omitempty` | Swift omits nil optionals (the key is absent, not `null`) |
| `[X]` | `[]X`, never nil | must encode `[]`, never `null`. Initialize with `make` or `[]X{}` |
| `Decimal` | `json.Number` holding the decimal string | keep Postgres `numeric` text exact |
| `[String: X]` | `map[string]X`, non-nil | |
| String enums | `string` constants | |

- **Key names are the Swift property names** (camelCase) unless the DTO
  declares `CodingKeys`. Check every DTO for `CodingKeys` and custom
  `encode(to:)`. Some encode snake_case keys or flatten nested values.
- **Request bodies** may arrive in snake_case or camelCase. `httpx.Decode`
  normalizes, so just tag Go structs with the camelCase Swift names.
  `URL`, `URI` and `Id` suffixes normalize as `avatar_url` -> `avatarURL`,
  `user_id` -> `userId`. Swift `[String: T]` is a Go `map[string]T`: its
  keys arrive untouched, only the structs inside it are normalized (the
  rewrite follows the target type). A hand-written decoder that walks raw
  JSON as keyed containers (`map[string]json.RawMessage`) uses
  `apijson.UnmarshalKeyed`, which rewrites every key.
- A Swift `Codable` decode failure was a **400**. A missing required field was
  a 400 too, so validate required fields explicitly where Swift had them
  non-optional.
- Query params: `req.query["x"]` was optional. A malformed typed value was a 400.

## Errors

Return `*httpx.Error`. Never write error JSON yourself.

- `Abort(.notFound)` -> `httpx.NotFound("")` (reason defaults to the status
  text, as Vapor's did)
- `Abort(.badRequest, reason: "x")` -> `httpx.BadRequest("x")`
- `CodedAbort(status:code:reason:)` -> `httpx.CodedAbort(status, code, reason)`
- Reasons are part of the contract: iOS shows some of them. Copy them verbatim.

## Auth and middleware per route

Mirror the group exactly as the Swift `boot(routes:)` builds it:

| Swift | Go |
|---|---|
| `SessionToken.authenticator()` + `SessionToken.guardMiddleware()` | `d.Auth.Session(), auth.Guard()` |
| `ScopedBearerAuthenticator()` + guard | `d.Auth.Scoped(), auth.Guard()` |
| `ScopeRequirementMiddleware(.x)` / `(anyOf:)` | `auth.RequireScope(auth.ScopeX, ...)` |
| `ScopeRequirementMiddleware.require(.x, on: req)` | `auth.CheckScope(c, auth.ScopeX)` |
| `FirstPartyOnlyMiddleware()` | `auth.FirstPartyOnly()` |
| `AdminGuard.requireAdmin(req)` / `AdminOnlyMiddleware()` | `d.Admin.Require(c)` / `d.Admin.Middleware()` |
| `RateLimitMiddleware(limit:interval:keyPrefix:)` | `d.Limiter.Limit(limit, interval, "same:prefix")`. Same key prefix: buckets are shared with Vapor through Redis |
| `IdempotencyMiddleware(keyPrefix:)` | `d.Limiter.Idempotency("same:prefix")` |
| `req.auth.require(SessionToken.self).userId` | `s, err := auth.Require(c)`; `s.UserID` |

Order matters: Vapor applies group middleware in the order `grouped(...)`
lists it, and `routes.swift` wraps some controllers in rate limiters
**outside** their own authenticator. Reproduce what actually runs.

Routes live at `r.V1` (`/v1/...`) or `r.Root` (no prefix: webhooks, share
pages, `.well-known`). Echo path params use `:name`, the same as Vapor.

## Tests

- Port the Swift tests for your domain into Go tests with
  `testutil.NewApp(t, []server.Feature{yourdomain.Feature{}})`. It gives you a
  fresh migrated database per test (`TEST_DATABASE_URL` must point at a
  disposable Postgres admin URL), `app.CreateUser`, `app.Token`, `app.Do`.
- `testutil.NewApp` has **no legacy upstream**: an unported route returns 404,
  so a test cannot pass by silently reaching Vapor.
- Never call a real external API in tests. Use `httptest` stub servers and
  point the provider base URL at them.
- Run `go test ./... -count=1 -race` and `golangci-lint run` before you report.

## Parity check against live Vapor

A Vapor build of the snapshot and a scratch Postgres are already running for
the whole migration:

- Vapor: `http://127.0.0.1:8081` (`JWT_SECRET=parity-secret`, DB below)
- Postgres: `postgres://pg:pg@127.0.0.1:55499/norviq` (scratch, safe to mutate)
- Test admin URL: `TEST_DATABASE_URL=postgres://pg:pg@127.0.0.1:55499/postgres?sslmode=disable`

Start your v2 build on your assigned port, against the same database:

```
set -a; . ./.env.example; set +a
PORT=<your port> LEGACY_UPSTREAM_URL=http://127.0.0.1:8081 go run ./cmd/api serve
```

Write cases to `parity/<domain>.jsonl` and run:

```
go run ./cmd/parity -a http://127.0.0.1:8081 -b http://127.0.0.1:<port> \
  -jwt-secret parity-secret -cases parity/<domain>.jsonl -ignore requestId,<volatile keys>
```

`"as":"<user uuid>"` signs a session for that user. Create parity users and
data with SQL against the scratch DB, or through Vapor's own endpoints.
Mutating cases run twice (Vapor first, then v2), so design them to be
repeatable, or compare only shape and status. Every read route you port needs
at least one passing parity case. Every write route needs one for its success
status/shape and one for its main error path. The `curl` command is blocked
in this environment; use the parity tool or Go tests.

To see Vapor's behavior for an env-gated feature, start another Vapor
instance with your flags on a free port:

```
cd /Users/fernandocorreiachill/Work/production/apps/norviq/.norviq-backend-snapshot
DATABASE_HOST=127.0.0.1 DATABASE_PORT=55499 DATABASE_USERNAME=pg DATABASE_PASSWORD=pg \
DATABASE_NAME=norviq JWT_SECRET=parity-secret <FLAGS> \
  "$(swift build --show-bin-path)/StockPlanBackend" serve --port <free port>
```

## Done means

1. Every route in your scope appears in `scripts/coverage.sh` as ported.
2. Ported tests pass, plus new ones for anything Swift left untested.
3. Parity cases pass against Vapor, or every remaining diff is listed in your
   report with the reason (e.g. Vapor nondeterminism).
4. `golangci-lint run` passes. Files are gofumpt-formatted (`make hooks`
   installs the pre-commit hook that does this).
5. Committed on your branch. Your report lists: routes ported, jobs ported
   (and the Vapor flag that disables Vapor's copy), env vars read, deviations
   from Vapor, and anything left for later.
