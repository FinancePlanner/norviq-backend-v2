-- Macro snapshots and series points (Vapor's DatabaseMacroRepository) and the
-- expense read behind personal inflation (DefaultMacroService). No ORDER BY
-- where Fluent had none: latest-vintage resolution happens in Go, iterating
-- rows in the order Postgres returns them, as Swift did.

-- name: MacroSnapshotExists :one
SELECT EXISTS (
    SELECT 1 FROM macro_snapshots
    WHERE country = $1 AND as_of = $2 AND source = $3
);

-- name: InsertMacroSnapshot :exec
INSERT INTO macro_snapshots (id, country, as_of, source, payload, fetched_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: LatestMacroSnapshot :one
SELECT * FROM macro_snapshots
WHERE country = $1
ORDER BY fetched_at DESC
LIMIT 1;

-- name: MacroPointsForPeriods :many
SELECT * FROM macro_series_points
WHERE country = sqlc.arg(country)
  AND series_key = sqlc.arg(series_key)
  AND source = sqlc.arg(source)
  AND period_date = ANY(sqlc.arg(periods)::text[]);

-- name: InsertMacroPoint :exec
INSERT INTO macro_series_points (
    id, country, series_key, period_date, value, unit, source, vintage_date, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: MacroSeriesRows :many
SELECT * FROM macro_series_points
WHERE country = sqlc.arg(country)
  AND series_key = sqlc.arg(series_key)
  AND (sqlc.narg(from_period)::text IS NULL OR period_date >= sqlc.narg(from_period)::text)
  AND (sqlc.narg(to_period)::text IS NULL OR period_date <= sqlc.narg(to_period)::text);

-- name: LatestMacroPoint :one
SELECT * FROM macro_series_points
WHERE country = $1 AND series_key = $2
ORDER BY period_date DESC, vintage_date DESC
LIMIT 1;

-- name: MacroFreshness :many
-- Most recent snapshot fetch per country, for the readiness check.
SELECT country, fetched_at FROM macro_snapshots
ORDER BY fetched_at DESC
LIMIT 200;

-- name: PersonalInflationExpenses :many
-- Expense.query(on:).filter(user).filter(occurredOn >= start).filter(occurredOn <= now)
-- .with(\.$category): occurred_on is a date compared against timestamptz binds.
SELECT e.title, e.amount, e.split_mode::text AS split_mode, e.user_share_percent,
       c.name AS category_name
FROM expenses e
LEFT JOIN expense_categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND e.occurred_on >= sqlc.arg(start_at)::timestamptz
  AND e.occurred_on <= sqlc.arg(end_at)::timestamptz;
