# Writing and changing routes in v2

v2 is the Norviq API. It replaced `norviq-backend` (Swift/Vapor), which is
archived for good; see `docs/MIGRATION.md`. Clients (iOS, web-v2 on Cloudflare,
norviq-mcp) were built against Vapor's wire contract, so **every route answers
exactly as Vapor did**: same method and path, status codes, JSON keys, value
formats, error envelope, auth rules, rate limits, and side effects in the
database. A change to any of those is a client contract change and needs the
clients to change with it. Vapor bugs were ported faithfully; whether each is
fixed or kept is recorded in `docs/vapor-bugs/`.

## Ground rules

- v2 serves the routes registered in `internal/features`. Any other request
  gets a 404 (or a 405 with `Allow`) in the JSON error envelope.
- **v2 owns the schema.** A schema change is a new goose migration; follow
  "Schema changes after cutover" in `docs/MIGRATION.md`.
- **Background jobs** start only when they are named in `V2_JOBS` (production
  and staging set `all`). Wrap each tick in `joblock.RunAsLeader` with a
  stable lock name, so replicas never run a tick at the same time.
- Fixes go here. Never write code in the Swift repo.

## Reference

| What | Where |
|---|---|
| Every route, as Vapor mounted it | `docs/vapor-routes.txt` (693 routes, every feature flag on) |
| Vapor sources, for how a route behaved | GitHub `FinancePlanner/norviq-backend` (archived, read-only): `Sources/StockPlanBackend`, its tests in `Tests/StockPlanBackendTests` |
| Request/response DTOs (shared with iOS) | `/Users/fernandocorreiachill/Work/production/apps/norviq/norviq-shared/Sources/StockPlanShared` (backend pins v5.25.0) |
| DB schema | `db/migrations` (`00001_baseline.sql` is exactly what Vapor's migrations produced) |

## Layout

```
internal/features/<domain>/      handlers, services, DTOs. One package per domain
  feature.go                     type Feature struct{}; Name(); Register(d, r)
  <domain>_test.go               tests ported from the Swift suite
db/queries/<domain>/*.sql        sqlc queries -> internal/store/<domain>db
internal/platform/<name>/        services shared across domains (market data,
                                 entitlements, push, mailer, AI providers, crypto)
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
| `RateLimitMiddleware(limit:interval:keyPrefix:)` | `d.Limiter.Limit(limit, interval, "same:prefix")`. Keep Vapor's key prefix: existing buckets in Redis use it |
| `IdempotencyMiddleware(keyPrefix:)` | `d.Limiter.Idempotency("same:prefix")` |
| `req.auth.require(SessionToken.self).userId` | `s, err := auth.Require(c)`; `s.UserID` |

Order matters: Vapor applies group middleware in the order `grouped(...)`
lists it, and `routes.swift` wraps some controllers in rate limiters
**outside** their own authenticator. Reproduce what actually runs.

Routes live at `r.V1` (`/v1/...`) or `r.Root` (no prefix: webhooks, share
pages, `.well-known`). Echo path params use `:name`, the same as Vapor.

## Tests

- Test a feature with
  `testutil.NewApp(t, []server.Feature{yourdomain.Feature{}})`. It gives you a
  fresh migrated database per test (`TEST_DATABASE_URL` must point at a
  disposable Postgres admin URL), `app.CreateUser`, `app.Token`, `app.Do`.
- A route the features under test do not register returns 404.
- Never call a real external API in tests. Use `httptest` stub servers and
  point the provider base URL at them.
- Run `go test ./... -count=1 -race` and `golangci-lint run` before you report.

## Parity tool (history)

`cmd/parity` and `parity/*.jsonl` diffed v2 against a running Vapor during the
port. Vapor no longer runs, so they cannot be run as a check now. Pin
behavior with Go tests instead.

## Done means

1. Tests cover every route you add or change, including its main error path.
2. `golangci-lint run` passes. Files are gofumpt-formatted (`make hooks`
   installs the pre-commit hook that does this).
3. Your report lists the routes and jobs added or changed, the env vars read,
   and any client contract change.
