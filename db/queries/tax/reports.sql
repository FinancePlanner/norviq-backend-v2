-- Tax reports, filing pack inputs and the tax jobs (TaxController report
-- routes, TaxReportGenerator, TaxReportGenerationPoller,
-- TaxReportCleanupJob, TaxProjectionPoller, TaxNotificationEvaluator).

-- name: InsertTaxReport :one
INSERT INTO tax_reports (id, user_id, tax_year, kind, format, status, attempt_count, next_attempt_at, created_at, updated_at)
VALUES (@id, @user_id, @tax_year, @kind, @format, @status, @attempt_count, @next_attempt_at, @now, @now)
RETURNING *;

-- name: CountActiveTaxReports :one
SELECT count(*) FROM tax_reports
WHERE user_id = $1 AND status IN ('pending', 'retry', 'generating', 'ready');

-- name: TaxReportsForUser :many
SELECT * FROM tax_reports WHERE user_id = $1 ORDER BY created_at DESC;

-- name: TaxReportForUser :one
SELECT * FROM tax_reports WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: TaxReportByID :one
SELECT * FROM tax_reports WHERE id = $1;

-- name: TaxReportCandidates :many
SELECT * FROM tax_reports
WHERE status IN ('pending', 'retry', 'generating')
ORDER BY created_at ASC
LIMIT 25;

-- name: UpdateTaxReportAttempt :exec
UPDATE tax_reports
SET status = @status, attempt_count = @attempt_count, next_attempt_at = @next_attempt_at, updated_at = @now
WHERE id = @id;

-- name: UpdateTaxReportReady :exec
UPDATE tax_reports
SET status = 'ready', file_path = @file_path, expires_at = @expires_at, error_message = NULL, updated_at = @now
WHERE id = @id;

-- name: UpdateTaxReportFailed :exec
UPDATE tax_reports
SET status = 'failed', error_message = @error_message, updated_at = @now
WHERE id = @id;

-- name: ExpiredTaxReports :many
SELECT * FROM tax_reports WHERE expires_at IS NOT NULL AND expires_at < @now::timestamptz;

-- name: MarkTaxReportExpired :exec
UPDATE tax_reports SET file_path = NULL, status = 'expired', updated_at = @now WHERE id = @id;

-- name: TaxProfileForYear :one
-- Fluent's `.first()` with no sort: whichever row Postgres returns first.
SELECT * FROM tax_profiles WHERE user_id = $1 AND tax_year = $2 LIMIT 1;

-- name: CompleteTaxProfiles :many
SELECT * FROM tax_profiles WHERE is_complete = true;

-- name: OpeningBalanceCount :one
SELECT count(*) FROM transactions
WHERE account_id = ANY(@account_ids::uuid[]) AND type = 'OPENING_BALANCE';

-- name: FundAnnualHoldingsForYear :many
SELECT * FROM germany_fund_annual_holdings WHERE user_id = $1 AND calculation_year = $2;

-- name: CachedFXRates :many
SELECT id, date, base, quote, rate::text AS rate FROM fx_daily_rates
WHERE quote = @quote AND date >= @start_at::timestamptz AND date <= @end_at::timestamptz;

-- name: InsertFXRate :exec
INSERT INTO fx_daily_rates (id, date, base, quote, rate)
VALUES (@id, @date, @base, @quote, CAST(sqlc.arg(rate)::text AS numeric))
ON CONFLICT DO NOTHING;

-- name: ProjectionJobExists :one
SELECT EXISTS (SELECT 1 FROM tax_projection_jobs WHERE idempotency_key = $1);

-- name: InsertProjectionJob :exec
INSERT INTO tax_projection_jobs (id, user_id, tax_year, reason, idempotency_key, status, attempt_count, next_attempt_at, created_at, updated_at)
VALUES (@id, @user_id, @tax_year, @reason, @idempotency_key, @status, @attempt_count, @next_attempt_at, @now, @now);

-- name: DueProjectionJobs :many
SELECT * FROM tax_projection_jobs
WHERE status IN ('pending', 'retry') AND next_attempt_at <= @now::timestamptz
ORDER BY next_attempt_at ASC
LIMIT 50;

-- name: UpdateProjectionJob :exec
UPDATE tax_projection_jobs
SET status = @status, attempt_count = @attempt_count, next_attempt_at = @next_attempt_at, last_error = @last_error, updated_at = @now
WHERE id = @id;

-- name: TaxableAccountIDsForUser :many
SELECT id FROM accounts WHERE user_id = $1 AND tax_wrapper = 'taxable';

-- name: LatestTaxNotificationDelivery :one
SELECT * FROM tax_notification_deliveries
WHERE user_id = @user_id AND instrument_id = @instrument_id AND delivered_at >= @since::timestamptz
ORDER BY delivered_at DESC
LIMIT 1;

-- name: InsertTaxNotificationDelivery :exec
INSERT INTO tax_notification_deliveries (id, user_id, opportunity_id, instrument_id, estimated_benefit, currency, delivered_at)
VALUES (@id, @user_id, @opportunity_id, @instrument_id, @estimated_benefit, @currency, @delivered_at);
