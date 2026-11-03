package tax

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/marketdata"
	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/notificationsdb"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
)

// Vapor's three tax jobs took no advisory lock (only an in-process
// BackgroundJobState), so the lock names below only keep v2 replicas apart;
// they do not exclude Vapor's own copies, which have no disable flag.
const (
	reportGenerationLock = "tax_report_generation_poller"
	reportCleanupLock    = "tax_report_cleanup_job"
	projectionLock       = "tax_projection_poller"
)

// envSeconds is `Int64(Environment.get(key) ?? def) ?? def`.
func envSeconds(key string, def int64) int64 {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// runEvery calls tick after initial, then every interval, until ctx ends.
func runEvery(ctx context.Context, initial, interval time.Duration, tick func(ctx context.Context)) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(initial):
	}
	for {
		tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// ReportGenerationJob is TaxReportGenerationPoller: every
// max(5, TAX_REPORT_GENERATION_POLL_SECONDS) seconds (default 10, first tick
// after 1 s) it generates up to 25 due reports.
type ReportGenerationJob struct{}

// Name is the V2_JOBS key.
func (ReportGenerationJob) Name() string { return reportGenerationLock }

// Run blocks until ctx ends.
func (ReportGenerationJob) Run(ctx context.Context, d *server.Deps) {
	svc, err := Feature{}.service(d)
	if err != nil {
		d.Logger.Error("tax_report_poller disabled", "error", err)
		return
	}
	interval := time.Duration(max(5, envSeconds("TAX_REPORT_GENERATION_POLL_SECONDS", 10))) * time.Second
	storage := reportStorageFromEnv()
	runEvery(ctx, time.Second, interval, func(ctx context.Context) {
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, reportGenerationLock, func(ctx context.Context) {
			svc.runReportGeneration(ctx, storage, newECBProvider(), d.Logger)
		})
	})
}

// reportRetryDelay is TaxReportGenerationPoller.retryDelaySeconds.
func reportRetryDelay(attempt int) time.Duration {
	return time.Duration(min(3600, 30*(1<<max(0, attempt-1)))) * time.Second
}

// runReportGeneration is TaxReportGenerationPoller.runOnce.
func (s *service) runReportGeneration(ctx context.Context, storage reportStorage, provider fxRateProvider, logger *slog.Logger) {
	if err := s.reportGenerationTick(ctx, storage, provider, logger); err != nil && ctx.Err() == nil {
		logger.Error("tax_report.poll_failed", "error", err)
	}
}

func (s *service) reportGenerationTick(ctx context.Context, storage reportStorage, provider fxRateProvider, logger *slog.Logger) error {
	candidates, err := s.q.TaxReportCandidates(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for idx := range candidates {
		r := &candidates[idx]
		if r.NextAttemptAt != nil && r.NextAttemptAt.After(now) {
			continue
		}
		if ctx.Err() != nil {
			return nil //nolint:nilerr // cancellation ends the tick quietly, as Vapor's CancellationError did
		}
		if err := s.processReport(ctx, *r, storage, provider, logger); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) processReport(ctx context.Context, r taxdb.TaxReport, storage reportStorage, provider fxRateProvider, logger *slog.Logger) error {
	attempt := int64(1)
	if r.AttemptCount != nil {
		attempt = *r.AttemptCount + 1
	}
	if err := s.q.UpdateTaxReportAttempt(ctx, taxdb.UpdateTaxReportAttemptParams{
		ID: r.ID, Status: "generating", AttemptCount: &attempt, NextAttemptAt: nil, Now: nowPtr(s.now()),
	}); err != nil {
		return err
	}
	id := uuidString(r.ID)
	logger.Info("tax_report.generation_started", "report_id", id, "attempt", attempt)
	s.generateReport(ctx, r.ID, storage, provider, func(msg string, args ...any) { logger.Error(msg, args...) })
	refreshed, err := s.q.TaxReportByID(ctx, r.ID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if refreshed.Status == "ready" {
		// next_attempt_at is already nil: Fluent saved nothing here.
		logger.Info("tax_report.generation_completed", "report_id", id, "attempt", attempt)
		return nil
	}
	if attempt >= 5 {
		logger.Error("tax_report.generation_exhausted", "report_id", id, "attempts", attempt)
		return s.q.UpdateTaxReportAttempt(ctx, taxdb.UpdateTaxReportAttemptParams{
			ID: r.ID, Status: "failed", AttemptCount: &attempt, NextAttemptAt: nil, Now: nowPtr(s.now()),
		})
	}
	delay := reportRetryDelay(int(attempt))
	next := s.now().Add(delay)
	logger.Warn("tax_report.generation_retry", "report_id", id, "attempt", attempt, "delay_seconds", int(delay.Seconds()))
	return s.q.UpdateTaxReportAttempt(ctx, taxdb.UpdateTaxReportAttemptParams{
		ID: r.ID, Status: "retry", AttemptCount: &attempt, NextAttemptAt: &next, Now: nowPtr(s.now()),
	})
}

// ReportCleanupJob is TaxReportCleanupJob: every
// TAX_REPORT_CLEANUP_INTERVAL_SECONDS (default 3600, first tick after 60 s)
// it deletes expired report files and marks the rows expired.
type ReportCleanupJob struct{}

// Name is the V2_JOBS key.
func (ReportCleanupJob) Name() string { return reportCleanupLock }

// Run blocks until ctx ends.
func (ReportCleanupJob) Run(ctx context.Context, d *server.Deps) {
	svc, err := Feature{}.service(d)
	if err != nil {
		d.Logger.Error("tax_report.cleanup disabled", "error", err)
		return
	}
	// Vapor scheduled a non-positive delay as given; v2 floors it at 1 s
	// rather than spin.
	interval := time.Duration(max(1, envSeconds("TAX_REPORT_CLEANUP_INTERVAL_SECONDS", 3600))) * time.Second
	storage := reportStorageFromEnv()
	runEvery(ctx, 60*time.Second, interval, func(ctx context.Context) {
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, reportCleanupLock, func(ctx context.Context) {
			if err := svc.cleanupExpiredReports(ctx, storage, d.Logger); err != nil && ctx.Err() == nil {
				d.Logger.Error("tax_report.cleanup failed", "error", err)
			}
		})
	})
}

// cleanupExpiredReports is TaxReportCleanupJob.removeExpiredReports.
func (s *service) cleanupExpiredReports(ctx context.Context, storage reportStorage, logger *slog.Logger) error {
	reports, err := s.q.ExpiredTaxReports(ctx, s.now())
	if err != nil {
		return err
	}
	deleted := 0
	for idx := range reports {
		r := &reports[idx]
		if r.FilePath != nil {
			if err := storage.delete(*r.FilePath); err != nil {
				logger.Warn("tax_report.cleanup_file_failed", "report_id", uuidString(r.ID), "error", err)
				continue
			}
			deleted++
		}
		if err := s.q.MarkTaxReportExpired(ctx, taxdb.MarkTaxReportExpiredParams{ID: r.ID, Now: nowPtr(s.now())}); err != nil {
			return err
		}
	}
	if len(reports) > 0 {
		logger.Info("tax_report.cleanup completed", "expired", len(reports), "deleted", deleted)
	}
	return nil
}

// ProjectionJob is TaxProjectionPoller: every
// max(3600, TAX_PROJECTION_POLL_SECONDS) seconds (default 86400, first tick
// after 90 s) it enqueues one daily projection per complete profile and
// runs up to 50 due ones (German loss reconciliation, dashboard, tax
// opportunity notifications).
type ProjectionJob struct{}

// Name is the V2_JOBS key.
func (ProjectionJob) Name() string { return projectionLock }

// Run blocks until ctx ends.
func (ProjectionJob) Run(ctx context.Context, d *server.Deps) {
	svc, err := Feature{}.service(d)
	if err != nil {
		d.Logger.Error("tax.projection disabled", "error", err)
		return
	}
	pushSvc, err := push.From(d)
	if err != nil {
		d.Logger.Error("tax.projection disabled", "error", err)
		return
	}
	n := notifier{q: notificationsdb.New(d.DB), push: pushSvc}
	interval := time.Duration(max(3600, envSeconds("TAX_PROJECTION_POLL_SECONDS", 86400))) * time.Second
	runEvery(ctx, 90*time.Second, interval, func(ctx context.Context) {
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, projectionLock, func(ctx context.Context) {
			if err := svc.runProjections(ctx, n); err != nil && ctx.Err() == nil {
				d.Logger.Error("tax.projection poll failed", "error", err)
			}
		})
	})
}

// runProjections is TaxProjectionPoller.runOnce.
func (s *service) runProjections(ctx context.Context, n notifier) error {
	if err := s.enqueueDailyProjections(ctx); err != nil {
		return err
	}
	return s.processDueProjections(ctx, n)
}

func (s *service) enqueueDailyProjections(ctx context.Context) error {
	profiles, err := s.q.CompleteTaxProfiles(ctx)
	if err != nil {
		return err
	}
	day := s.now().UTC().Format("2006-01-02")
	for idx := range profiles {
		p := &profiles[idx]
		key := "daily:" + uuidString(p.UserID) + ":" + p.Jurisdiction + ":" + strconv.FormatInt(p.TaxYear, 10) + ":" + day
		exists, err := s.q.ProjectionJobExists(ctx, key)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		now := s.now()
		if err := s.q.InsertProjectionJob(ctx, taxdb.InsertProjectionJobParams{
			ID: uuid.New(), UserID: p.UserID, TaxYear: p.TaxYear, Reason: "daily", IdempotencyKey: key,
			Status: "pending", AttemptCount: 0, NextAttemptAt: now, Now: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

//lint:ignore ST1005 Vapor's String(describing: Abort) is stored in last_error.
var errProjectionProfileMissing = errors.New("Abort.404: Tax profile not found for projection job.")

func (s *service) processDueProjections(ctx context.Context, n notifier) error {
	jobs, err := s.q.DueProjectionJobs(ctx, s.now())
	if err != nil {
		return err
	}
	for idx := range jobs {
		job := &jobs[idx]
		job.Status = "running"
		job.AttemptCount++
		if err := s.saveProjectionJob(ctx, *job); err != nil {
			return err
		}
		if err := s.runProjection(ctx, *job, n); err != nil {
			msg := err.Error()
			job.LastError = &msg
			if job.AttemptCount >= 5 {
				job.Status = "failed"
			} else {
				job.Status = "retry"
				delay := min(3600, 60*(1<<max(0, job.AttemptCount-1)))
				job.NextAttemptAt = s.now().Add(time.Duration(delay) * time.Second)
			}
		} else {
			job.Status = "complete"
			job.LastError = nil
		}
		if err := s.saveProjectionJob(ctx, *job); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) saveProjectionJob(ctx context.Context, j taxdb.TaxProjectionJob) error {
	return s.q.UpdateProjectionJob(ctx, taxdb.UpdateProjectionJobParams{
		ID: j.ID, Status: j.Status, AttemptCount: j.AttemptCount, NextAttemptAt: j.NextAttemptAt,
		LastError: j.LastError, Now: nowPtr(s.now()),
	})
}

func (s *service) runProjection(ctx context.Context, job taxdb.TaxProjectionJob, n notifier) error {
	profile, err := s.q.TaxProfileForYear(ctx, taxdb.TaxProfileForYearParams{UserID: job.UserID, TaxYear: job.TaxYear})
	if err != nil && !isNoRows(err) {
		return err
	}
	j := Jurisdiction(profile.Jurisdiction)
	if err != nil || !j.valid() {
		return errProjectionProfileMissing
	}
	taxYear := int(job.TaxYear)
	if j == DE {
		if err := s.reconcileGermanStockLosses(ctx, job.UserID, taxYear); err != nil {
			return err
		}
	}
	dashboard, err := s.dashboard(ctx, job.UserID, j, taxYear)
	if err != nil {
		return err
	}
	return s.evaluateTaxNotifications(ctx, dashboard, job.UserID, n)
}

// reconcileGermanStockLosses is TaxProjectionPoller.reconcileGermanStockLosses.
func (s *service) reconcileGermanStockLosses(ctx context.Context, userID uuid.UUID, taxYear int) error {
	accounts, err := s.q.AccountsForUser(ctx, userID)
	if err != nil {
		return err
	}
	ledger := germanyLedger{s, stockLossTables}
	if len(accounts) == 0 {
		_, err := ledger.reconcile(ctx, userID, taxYear, zero, "DE-2026.1")
		return err
	}
	ids := make([]uuid.UUID, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		ids[i] = a.ID
	}
	sells, err := s.q.SellTransactionsInRange(ctx, taxdb.SellTransactionsInRangeParams{
		AccountIds: ids,
		Start:      time.Date(taxYear, 1, 1, 0, 0, 0, 0, time.UTC),
		EndAt:      time.Date(taxYear+1, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		return err
	}
	instrumentSet := map[uuid.UUID]bool{}
	instrumentIDs := []uuid.UUID{}
	for idx := range sells {
		t := &sells[idx]
		if !instrumentSet[t.InstrumentID] {
			instrumentSet[t.InstrumentID] = true
			instrumentIDs = append(instrumentIDs, t.InstrumentID)
		}
	}
	stock := map[uuid.UUID]bool{}
	if len(instrumentIDs) > 0 {
		instruments, err := s.q.InstrumentsByIDs(ctx, instrumentIDs)
		if err != nil {
			return err
		}
		for idx := range instruments {
			in := &instruments[idx]
			if oneOf(lower(in.InstrumentType), []string{"stock", "equity"}) {
				stock[in.ID] = true
			}
		}
	}
	txIDs := []uuid.UUID{}
	for idx := range sells {
		t := &sells[idx]
		if stock[t.InstrumentID] {
			txIDs = append(txIDs, t.ID)
		}
	}
	annual := zero
	if len(txIDs) > 0 {
		disposals, err := s.q.DisposalsByTransactionIDs(ctx, txIDs)
		if err != nil {
			return err
		}
		for _, d := range disposals {
			annual = annual.Add(fd(d.RealizedPnl))
		}
	}
	version := newRuleRegistry(map[Jurisdiction]bool{DE: true}).pack(DE).ruleVersion
	_, err = ledger.reconcile(ctx, userID, taxYear, annual, version)
	return err
}

// notifier is what TaxNotificationEvaluator writes to: the inbox and APNS.
type notifier struct {
	q    notificationsdb.Querier
	push *push.Service
}

var (
	taxNotificationFloor   = di(250)
	taxNotificationShare   = dec.MustParse("0.005")
	taxNotificationGrowth  = dec.MustParse("1.25")
	opportunityActionable  = "actionable"
	taxOpportunityDeepLink = "norviq://tax/opportunities/"
)

// evaluateTaxNotifications is TaxNotificationEvaluator.evaluate.
func (s *service) evaluateTaxNotifications(ctx context.Context, dashboard DashboardResponse, userID uuid.UUID, n notifier) error {
	prefs, err := s.q.NotificationPreferenceForUser(ctx, userID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !prefs.Enabled {
		return nil
	}
	accountIDs, err := s.q.TaxableAccountIDsForUser(ctx, userID)
	if err != nil {
		return err
	}
	taxable := 0.0
	if len(accountIDs) > 0 {
		positions, err := s.q.PositionsForAccounts(ctx, accountIDs)
		if err != nil {
			return err
		}
		for idx := range positions {
			p := &positions[idx]
			price := p.AverageCost
			if p.LastPrice != nil {
				price = *p.LastPrice
			}
			taxable += max(0, p.Quantity*price)
		}
	}
	configured := zero
	if prefs.MinimumBenefit != nil {
		configured = fd(*prefs.MinimumBenefit)
	}
	threshold := dec.Max(taxNotificationFloor, dec.Max(fd(taxable).Mul(taxNotificationShare), configured))
	devices, err := n.push.Devices.Active(ctx, userID)
	if err != nil {
		return err
	}
	for idx := range dashboard.Opportunities {
		o := &dashboard.Opportunities[idx]
		if o.Status != opportunityActionable || o.EstimatedTaxBenefit.Amount.Cmp(threshold) < 0 {
			continue
		}
		instrumentID, ok := parseUUID(o.InstrumentID)
		if !ok {
			continue
		}
		now := s.now()
		since := now.AddDate(0, 0, -max(1, int(prefs.CooldownDays)))
		previous, err := s.q.LatestTaxNotificationDelivery(ctx, taxdb.LatestTaxNotificationDeliveryParams{
			UserID: userID, InstrumentID: instrumentID, Since: since,
		})
		var prevBenefit *dec.Decimal
		switch {
		case err == nil:
			v := fd(previous.EstimatedBenefit)
			prevBenefit = &v
		case !isNoRows(err):
			return err
		}
		amount := o.EstimatedTaxBenefit.Amount
		if !shouldNotifyTax(amount, threshold, prevBenefit) {
			continue
		}
		benefit := amount.Float64()
		deepLink := taxOpportunityDeepLink + o.ID
		if _, err := inbox.Publish(ctx, n.q, inbox.Message{
			UserID:           userID,
			Kind:             inbox.KindTax,
			DeduplicationKey: "tax:" + o.ID + ":" + strconv.FormatInt(int64(benefit*100), 10),
			Title:            "Tax opportunity",
			Body:             "Estimated benefit: " + marketdata.SwiftDoubleString(benefit) + " " + o.EstimatedTaxBenefit.Currency + ".",
			DeepLink:         &deepLink,
			Payload:          map[string]string{"opportunity_id": o.ID, "instrument_id": uuidString(instrumentID)},
		}, now); err != nil {
			return err
		}
		if err := s.q.InsertTaxNotificationDelivery(ctx, taxdb.InsertTaxNotificationDeliveryParams{
			ID: uuid.New(), UserID: userID, OpportunityID: o.ID, InstrumentID: instrumentID,
			EstimatedBenefit: benefit, Currency: o.EstimatedTaxBenefit.Currency, DeliveredAt: now,
		}); err != nil {
			return err
		}
		if len(devices) > 0 {
			n.push.Sender.Send(ctx, push.TaxOpportunityAlert(push.TaxOpportunity{
				ID: o.ID, InstrumentID: o.InstrumentID, Symbol: o.Symbol,
				EstimatedBenefit: json.Number(amount.String()), Currency: o.EstimatedTaxBenefit.Currency,
			}), devices)
		}
	}
	return nil
}

// shouldNotifyTax is TaxNotificationPolicy.shouldNotify.
func shouldNotifyTax(benefit, threshold dec.Decimal, previous *dec.Decimal) bool {
	if benefit.Cmp(threshold) < 0 {
		return false
	}
	if previous == nil {
		return true
	}
	return benefit.Cmp(previous.Mul(taxNotificationGrowth)) >= 0
}
