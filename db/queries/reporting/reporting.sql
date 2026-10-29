-- Advanced reporting: templates, schedules, runs, artifacts and deliveries
-- (AdvancedReportingController.swift, AdvancedReportWorker.swift,
-- ReportDocument.swift, PortfolioAccessService.swift).

-- name: ListTemplates :many
SELECT * FROM advanced_report_templates
WHERE owner_user_id = $1 AND archived_at IS NULL
ORDER BY updated_at DESC;

-- name: GetTemplate :one
SELECT * FROM advanced_report_templates WHERE id = $1;

-- name: InsertTemplate :exec
INSERT INTO advanced_report_templates (id, owner_user_id, input_json, revision, is_starter_template, archived_at, created_at, updated_at)
VALUES (@id, @owner_user_id, @input_json, @revision, @is_starter_template, NULL, @now::timestamptz, @now::timestamptz);

-- name: UpdateTemplate :exec
UPDATE advanced_report_templates
SET input_json = @input_json, revision = @revision, archived_at = @archived_at, updated_at = @now::timestamptz
WHERE id = @id;

-- name: ListSchedules :many
SELECT * FROM advanced_report_schedules
WHERE owner_user_id = $1
ORDER BY created_at DESC;

-- name: SchedulesByOwner :many
SELECT * FROM advanced_report_schedules WHERE owner_user_id = $1;

-- name: CountSchedules :one
SELECT count(*) FROM advanced_report_schedules WHERE owner_user_id = $1;

-- name: GetSchedule :one
SELECT * FROM advanced_report_schedules WHERE id = $1;

-- name: InsertSchedule :exec
INSERT INTO advanced_report_schedules (id, owner_user_id, input_json, next_run_at, last_run_at, paused_reason, created_at, updated_at)
VALUES (@id, @owner_user_id, @input_json, @next_run_at, NULL, NULL, @now::timestamptz, @now::timestamptz);

-- name: UpdateSchedule :exec
UPDATE advanced_report_schedules
SET input_json = @input_json, next_run_at = @next_run_at, last_run_at = @last_run_at,
    paused_reason = @paused_reason, updated_at = @now::timestamptz
WHERE id = @id;

-- name: DeleteSchedule :exec
DELETE FROM advanced_report_schedules WHERE id = $1;

-- name: ListRuns :many
SELECT * FROM advanced_report_runs
WHERE requested_by_user_id = $1
ORDER BY created_at DESC
LIMIT 100;

-- name: GetRun :one
SELECT * FROM advanced_report_runs WHERE id = $1;

-- name: RunFormatsSince :many
SELECT output_formats_json FROM advanced_report_runs
WHERE requested_by_user_id = @user_id AND created_at >= @since::timestamptz;

-- name: InsertRun :exec
INSERT INTO advanced_report_runs (
    id, template_id, schedule_id, requested_by_user_id, template_revision, template_input_json,
    output_formats_json, recipient_user_ids_json, status, scheduled_for, claimed_at, started_at,
    completed_at, failure_reason, attempt_count, retry_at, created_at, updated_at
)
VALUES (
    @id, @template_id, @schedule_id, @requested_by_user_id, @template_revision, @template_input_json,
    @output_formats_json, @recipient_user_ids_json, @status, @scheduled_for, NULL, NULL,
    NULL, NULL, 0, NULL, @now::timestamptz, @now::timestamptz
);

-- name: SaveRun :exec
UPDATE advanced_report_runs
SET status = @status, claimed_at = @claimed_at, started_at = @started_at, completed_at = @completed_at,
    failure_reason = @failure_reason, attempt_count = @attempt_count, retry_at = @retry_at, updated_at = @now::timestamptz
WHERE id = @id;

-- name: ArtifactsByRun :many
SELECT * FROM advanced_report_artifacts WHERE run_id = $1 ORDER BY created_at ASC;

-- name: ArtifactsByRunUnordered :many
SELECT * FROM advanced_report_artifacts WHERE run_id = $1;

-- name: GetArtifact :one
SELECT * FROM advanced_report_artifacts WHERE id = $1;

-- name: InsertArtifact :exec
INSERT INTO advanced_report_artifacts (id, run_id, format, filename, content_type, size_bytes, sha256, storage_key, expires_at, created_at)
VALUES (@id, @run_id, @format, @filename, @content_type, @size_bytes, @sha256, @storage_key, @expires_at, @now::timestamptz);

-- name: ExpiredArtifacts :many
SELECT * FROM advanced_report_artifacts WHERE expires_at < $1;

-- name: DeliveriesByRun :many
SELECT * FROM advanced_report_deliveries WHERE run_id = $1 ORDER BY created_at ASC;

-- name: DeliveriesByRunUnordered :many
SELECT * FROM advanced_report_deliveries WHERE run_id = $1;

-- name: InsertDelivery :exec
INSERT INTO advanced_report_deliveries (
    id, run_id, recipient_user_id, recipient_email, status, attempt_count, last_attempt_at,
    delivered_at, failure_reason, link_expires_at, created_at, updated_at
)
VALUES (@id, @run_id, @recipient_user_id, @recipient_email, @status, 0, NULL, NULL, NULL, NULL, @now::timestamptz, @now::timestamptz);

-- name: SaveDelivery :exec
UPDATE advanced_report_deliveries
SET status = @status, attempt_count = @attempt_count, last_attempt_at = @last_attempt_at,
    delivered_at = @delivered_at, failure_reason = @failure_reason, link_expires_at = @link_expires_at,
    updated_at = @now::timestamptz
WHERE id = @id;

-- name: RetryableDeliveries :many
SELECT * FROM advanced_report_deliveries
WHERE (status = 'pending' OR status = 'retry') AND attempt_count < 3
LIMIT 20;

-- name: DeleteRunsCreatedBefore :exec
DELETE FROM advanced_report_runs WHERE created_at < $1::timestamptz;

-- name: ClaimDueSchedule :one
WITH candidate AS (
    SELECT id FROM advanced_report_schedules
    WHERE next_run_at <= NOW()
      AND (
        paused_reason IS NULL
        OR (paused_reason = 'processing' AND updated_at < NOW() - INTERVAL '10 minutes')
      )
    ORDER BY next_run_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE advanced_report_schedules AS schedule
SET paused_reason = 'processing', updated_at = NOW()
FROM candidate WHERE schedule.id = candidate.id
RETURNING schedule.id;

-- name: ClaimRun :one
WITH candidate AS (
    SELECT id FROM advanced_report_runs
    WHERE status = 'pending'
       OR (status = 'retry' AND retry_at <= NOW())
       OR (status = 'claimed' AND claimed_at < NOW() - INTERVAL '10 minutes')
    ORDER BY created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE advanced_report_runs AS report_run
SET status = 'claimed', claimed_at = NOW(), started_at = COALESCE(started_at, NOW())
FROM candidate WHERE report_run.id = candidate.id
RETURNING report_run.id;

-- name: UsersByIDs :many
SELECT id, email, is_verified FROM users WHERE id = ANY(@ids::uuid[]);

-- name: PortfolioOwner :one
SELECT user_id FROM portfolio_lists WHERE id = $1;

-- name: ActiveMembershipExists :one
SELECT EXISTS (
    SELECT 1 FROM portfolio_memberships
    WHERE portfolio_id = @portfolio_id AND user_id = @user_id AND status = 'active'
);

-- name: PortfoliosByIDs :many
SELECT id, name, base_currency FROM portfolio_lists WHERE id = ANY(@ids::uuid[]);

-- name: StocksByPortfolio :many
SELECT symbol, shares, buy_price, category::text AS category
FROM stocks
WHERE portfolio_list_id = $1
ORDER BY symbol ASC;

-- name: CashBalancesByPortfolio :many
SELECT balance FROM portfolio_cash_positions WHERE portfolio_id = $1;
