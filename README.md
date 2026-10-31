# norviq-backend-v2

The Norviq API in Go: Echo v5, pgx, sqlc, goose, caarlos0/env. It replaced the
Swift/Vapor `norviq-backend` on 2026-10-30 with an identical wire contract.
Vapor is archived for good: there is no rollback to it, and no code goes to the
Swift repo. See [docs/MIGRATION.md](docs/MIGRATION.md) for the record of the
migration and [docs/PORTING.md](docs/PORTING.md) for how to write and change
routes.

## Develop

```sh
make hooks                       # gofumpt + golangci-lint pre-commit hook
cp .env.example .env             # then: set -a; . ./.env; set +a
go run ./cmd/api serve           # serves every registered route; anything else is a 404
go run ./cmd/api migrate         # goose up (adopts a Vapor-migrated DB first)
go run ./cmd/api routes          # every route v2 serves
scripts/coverage.sh              # served vs Vapor's 693 routes
```

Tests need a disposable Postgres:

```sh
TEST_DATABASE_URL='postgres://pg:pg@127.0.0.1:55499/postgres?sslmode=disable' make test
```

Each test gets its own database cloned from a migrated template, so tests run
in parallel and take milliseconds to set up.

## Layout

| Path | What |
|---|---|
| `cmd/api` | server, `migrate`, `routes` |
| `cmd/parity` | replayed requests against Vapor and v2 and diffed them (history: needs a running Vapor) |
| `internal/apijson` | the Vapor wire format: dates, uppercase UUIDs, snake/camel request keys |
| `internal/httpx` | error envelope, JSON helpers |
| `internal/auth` | session JWTs, PATs, OAuth tokens, scopes, admin allowlist |
| `internal/middleware` | client IP, rate limit, idempotency, security headers, request log |
| `internal/server` | echo assembly, health |
| `internal/features/<domain>` | one package per domain (ported from Vapor's controllers) |
| `internal/platform/<name>` | services shared across domains |
| `db/migrations` | goose migrations (00001 = Vapor's schema) |
| `db/queries/<domain>` | sqlc queries → `internal/store/<domain>db` |
# norviq-backend-v2
