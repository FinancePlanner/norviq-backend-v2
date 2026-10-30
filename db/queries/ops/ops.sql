-- InternalMetricsController (GET /internal/metrics).

-- name: CountUsers :one
SELECT count(*) FROM users;
