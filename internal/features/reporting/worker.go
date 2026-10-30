package reporting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/server"
	"norviq-api/internal/store/reportingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Lock names. Vapor's worker and retention job take no JobLock: runs and
// schedules are claimed row by row with FOR UPDATE SKIP LOCKED, which still
// holds against Vapor's copy. The locks below only keep several v2 replicas
// from retrying the same deliveries in one tick.
const (
	WorkerLockName    = "advanced_report_worker"
	RetentionLockName = "advanced_report_retention"
)

// worker is AdvancedReportWorker's state for one process.
type worker struct {
	q         *reportingdb.Queries
	ent       *entitlements.Service
	storage   Storage
	signer    Signer
	mail      mailer.Mailer
	client    *http.Client
	gotenberg string
	baseURL   string
	logger    *slog.Logger
	now       func() time.Time
}

// WorkerJob is AdvancedReportWorker: enqueues due schedules, retries
// deliveries, then generates and delivers every claimable run, each tick.
type WorkerJob struct {
	Settings     *Settings
	Entitlements *entitlements.Config
	Mailer       mailer.Mailer
	HTTPClient   *http.Client
	Now          func() time.Time
}

// Name implements features.Job.
func (WorkerJob) Name() string { return "advanced_report_worker" }

func newWorker(d *server.Deps, s Settings, cfg *entitlements.Config, m mailer.Mailer, client *http.Client, now func() time.Time) (*worker, error) {
	signer, err := s.signer(d.Config.IsProduction())
	if err != nil {
		return nil, err
	}
	if m == nil {
		m = mailer.New(mailer.SettingsFromEnv(), d.Logger)
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	if now == nil {
		now = time.Now
	}
	return &worker{
		q: reportingdb.New(d.DB), ent: newEntitlements(d, cfg),
		storage: Storage{Root: s.StoragePath}, signer: signer, mail: m, client: client,
		gotenberg: s.GotenbergBaseURL, baseURL: s.PublicAPIBaseURL, logger: d.Logger, now: now,
	}, nil
}

func settingsOr(s *Settings) Settings {
	if s != nil {
		return *s
	}
	return LoadSettings()
}

// Run implements features.Job: a 2s initial delay, then every
// ADVANCED_REPORT_WORKER_INTERVAL_SECONDS (at least 2).
func (j WorkerJob) Run(ctx context.Context, d *server.Deps) {
	s := settingsOr(j.Settings)
	w, err := newWorker(d, s, j.Entitlements, j.Mailer, j.HTTPClient, j.Now)
	if err != nil {
		d.Logger.Error("advanced_report_worker config", "error", err)
		return
	}
	interval := time.Duration(max(2, s.WorkerIntervalSeconds)) * time.Second
	loop(ctx, 2*time.Second, interval, func() {
		joblock.RunAsLeader(ctx, d.DB, d.Logger, WorkerLockName, func(ctx context.Context) { w.runOnce(ctx) })
	})
}

// RetentionJob is AdvancedReportRetentionJob: deletes expired artifact
// files and run metadata older than a year.
type RetentionJob struct {
	Settings *Settings
	Now      func() time.Time
}

// Name implements features.Job.
func (RetentionJob) Name() string { return "advanced_report_retention" }

// Run implements features.Job: a 60s initial delay, then every
// ADVANCED_REPORT_CLEANUP_INTERVAL_SECONDS (at least 60).
func (j RetentionJob) Run(ctx context.Context, d *server.Deps) {
	s := settingsOr(j.Settings)
	now := j.Now
	if now == nil {
		now = time.Now
	}
	interval := time.Duration(max(60, s.CleanupIntervalSeconds)) * time.Second
	loop(ctx, 60*time.Second, interval, func() {
		joblock.RunAsLeader(ctx, d.DB, d.Logger, RetentionLockName, func(ctx context.Context) {
			retentionOnce(ctx, reportingdb.New(d.DB), Storage{Root: s.StoragePath}, d.Logger, now())
		})
	})
}

func loop(ctx context.Context, initial, interval time.Duration, tick func()) {
	timer := time.NewTimer(initial)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		tick()
		timer.Reset(interval)
	}
}

// retentionOnce is AdvancedReportRetentionJob.runOnce.
func retentionOnce(ctx context.Context, q *reportingdb.Queries, st Storage, logger *slog.Logger, now time.Time) {
	expired, err := q.ExpiredArtifacts(ctx, now)
	if err != nil {
		logger.Error("advanced_report.cleanup_failed error=" + err.Error())
		return
	}
	for i := range expired {
		a := &expired[i]
		if err := st.Delete(a.StorageKey); err != nil {
			logger.Warn("advanced_report.cleanup_file_failed id=" + formatUUID(a.ID) + " error=" + err.Error())
		}
	}
	if err := q.DeleteRunsCreatedBefore(ctx, now.Add(-365*86400*time.Second)); err != nil {
		logger.Error("advanced_report.cleanup_failed error=" + err.Error())
	}
}

// runOnce is AdvancedReportWorker.runOnce.
func (w *worker) runOnce(ctx context.Context) {
	err := func() error {
		if err := w.enqueueDueSchedules(ctx); err != nil {
			return err
		}
		if err := w.retryDeliveries(ctx); err != nil {
			return err
		}
		for {
			id, err := w.q.ClaimRun(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			w.execute(ctx, id)
		}
	}()
	if err != nil {
		w.logger.Error("advanced_report.worker_failed error=" + err.Error())
	}
}

func (w *worker) saveSchedule(ctx context.Context, s reportingdb.AdvancedReportSchedule) error {
	return w.q.UpdateSchedule(ctx, reportingdb.UpdateScheduleParams{
		ID: s.ID, InputJson: s.InputJson, NextRunAt: s.NextRunAt, LastRunAt: s.LastRunAt,
		PausedReason: s.PausedReason, Now: w.now(),
	})
}

func (w *worker) saveRun(ctx context.Context, r reportingdb.AdvancedReportRun) error {
	return w.q.SaveRun(ctx, reportingdb.SaveRunParams{
		ID: r.ID, Status: r.Status, ClaimedAt: r.ClaimedAt, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt,
		FailureReason: r.FailureReason, AttemptCount: r.AttemptCount, RetryAt: r.RetryAt, Now: w.now(),
	})
}

func (w *worker) saveDelivery(ctx context.Context, d reportingdb.AdvancedReportDelivery) error {
	return w.q.SaveDelivery(ctx, reportingdb.SaveDeliveryParams{
		ID: d.ID, Status: d.Status, AttemptCount: d.AttemptCount, LastAttemptAt: d.LastAttemptAt,
		DeliveredAt: d.DeliveredAt, FailureReason: d.FailureReason, LinkExpiresAt: d.LinkExpiresAt, Now: w.now(),
	})
}

func strp(s string) *string { return &s }

func timep(t time.Time) *time.Time { return &t }

func (w *worker) enqueueDueSchedules(ctx context.Context) error {
	for range 20 {
		id, err := w.q.ClaimDueSchedule(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		sc, err := w.q.GetSchedule(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		snap, err := w.ent.Resolve(ctx, sc.OwnerUserID)
		if err != nil {
			return err
		}
		if !snap.IsPro() {
			sc.NextRunAt, sc.PausedReason = nil, strp("subscription_required")
			if err := w.saveSchedule(ctx, sc); err != nil {
				return err
			}
			continue
		}
		in, err := decodeStored[ScheduleInput](sc.InputJson)
		if err != nil {
			return err
		}
		var tmpl reportingdb.AdvancedReportTemplate
		templateID, idOK := parseSwiftUUID(in.TemplateID)
		available := in.IsEnabled && idOK
		if available {
			tmpl, err = w.q.GetTemplate(ctx, templateID)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				available = false
			case err != nil:
				return err
			case tmpl.ArchivedAt != nil:
				available = false
			}
		}
		if !available {
			sc.NextRunAt, sc.PausedReason = nil, strp("template_unavailable")
			if err := w.saveSchedule(ctx, sc); err != nil {
				return err
			}
			continue
		}
		tin, err := decodeStored[TemplateInput](tmpl.InputJson)
		if err != nil {
			return err
		}
		if err := w.ensureAccess(ctx, sc.OwnerUserID, in.RecipientUserIDs, tin); err != nil {
			if !isRevokedAccess(err) {
				return err
			}
			sc.NextRunAt, sc.PausedReason = nil, strp("access_revoked")
			if err := w.saveSchedule(ctx, sc); err != nil {
				return err
			}
			w.logger.Warn("advanced_report.schedule_paused reason=access_revoked schedule_id=" + formatUUID(sc.ID))
			continue
		}
		scheduledFor := sc.NextRunAt
		today, err := artifactsRequestedToday(ctx, w.q, sc.OwnerUserID, w.now())
		if err != nil {
			return err
		}
		after := w.now()
		if scheduledFor != nil {
			after = *scheduledFor
		}
		// Deliberate fix: Vapor let a recurrence it could not compute fail
		// the whole tick, leaving the schedule claimed until it was
		// reclaimed 10 minutes later (and failing again forever). It is now
		// paused with reason invalid_recurrence and the tick goes on.
		next, nextErr := nextRun(after, in.Recurrence)
		if nextErr != nil {
			sc.NextRunAt, sc.PausedReason = nil, strp("invalid_recurrence")
			if err := w.saveSchedule(ctx, sc); err != nil {
				return err
			}
			w.logger.Warn("advanced_report.schedule_paused reason=invalid_recurrence schedule_id=" + formatUUID(sc.ID) + " error=" + nextErr.Error())
			continue
		}
		if today+len(in.OutputFormats) > 100 {
			sc.LastRunAt, sc.NextRunAt, sc.PausedReason = scheduledFor, &next, nil
			if err := w.saveSchedule(ctx, sc); err != nil {
				return err
			}
			w.logger.Warn("advanced_report.schedule_skipped reason=daily_limit schedule_id=" + formatUUID(sc.ID))
			continue
		}
		recipients := in.RecipientUserIDs
		if len(recipients) == 0 {
			recipients = []string{formatUUID(sc.OwnerUserID)}
		}
		formatsJSON, err := encodeReportJSON(in.OutputFormats)
		if err != nil {
			return err
		}
		recipientsJSON, err := encodeReportJSON(recipients)
		if err != nil {
			return err
		}
		scheduleID := sc.ID
		if err := w.q.InsertRun(ctx, reportingdb.InsertRunParams{
			ID: uuid.New(), TemplateID: templateID, ScheduleID: &scheduleID, RequestedByUserID: sc.OwnerUserID,
			TemplateRevision: tmpl.Revision, TemplateInputJson: tmpl.InputJson,
			OutputFormatsJson: formatsJSON, RecipientUserIdsJson: recipientsJSON,
			Status: "pending", ScheduledFor: scheduledFor, Now: w.now(),
		}); err != nil {
			return err
		}
		sc.LastRunAt, sc.NextRunAt, sc.PausedReason = scheduledFor, &next, nil
		if err := w.saveSchedule(ctx, sc); err != nil {
			return err
		}
	}
	return nil
}

// ensureAccess is ensureReportAccess: the requester and every recipient
// must still reach every portfolio the template reads.
func (w *worker) ensureAccess(ctx context.Context, requester uuid.UUID, recipientValues []string, t TemplateInput) error {
	principals := []uuid.UUID{requester}
	seen := map[uuid.UUID]bool{requester: true}
	for _, v := range recipientValues {
		id, err := requireUUID(v, "recipientUserIds")
		if err != nil {
			return err
		}
		if !seen[id] {
			seen[id] = true
			principals = append(principals, id)
		}
	}
	portfolios, err := templatePortfolioIDs(t)
	if err != nil {
		return err
	}
	for _, p := range portfolios {
		for _, u := range principals {
			if err := requirePortfolioAccess(ctx, w.q, p, u); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *worker) ensureRunAccess(ctx context.Context, r reportingdb.AdvancedReportRun, t TemplateInput) error {
	recipients, err := decodeStored[[]string](r.RecipientUserIdsJson)
	if err != nil {
		return err
	}
	return w.ensureAccess(ctx, r.RequestedByUserID, recipients, t)
}

func (w *worker) retryDeliveries(ctx context.Context) error {
	deliveries, err := w.q.RetryableDeliveries(ctx)
	if err != nil {
		return err
	}
	for i := range deliveries {
		d := deliveries[i]
		r, err := w.q.GetRun(ctx, d.RunID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		t, err := decodeStored[TemplateInput](r.TemplateInputJson)
		if err != nil {
			return err
		}
		if err := w.ensureRunAccess(ctx, r, t); err != nil {
			if !isRevokedAccess(err) {
				return err
			}
			w.markAccessRevoked(ctx, r)
			continue
		}
		w.send(ctx, d, t.Name)
		if err := w.updateRunDeliveryStatus(ctx, d.RunID); err != nil {
			return err
		}
	}
	return nil
}

func (w *worker) execute(ctx context.Context, runID uuid.UUID) {
	if err := w.generate(ctx, runID); err != nil {
		w.failOrRetry(ctx, runID, err)
	}
}

func (w *worker) generate(ctx context.Context, runID uuid.UUID) error {
	r, err := w.q.GetRun(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	r.Status = "generating"
	r.AttemptCount++
	if err := w.saveRun(ctx, r); err != nil {
		return err
	}
	t, err := decodeStored[TemplateInput](r.TemplateInputJson)
	if err != nil {
		return err
	}
	if err := w.ensureRunAccess(ctx, r, t); err != nil {
		if isRevokedAccess(err) {
			w.markAccessRevoked(ctx, r)
			return nil
		}
		return err
	}
	doc, err := collectDocument(ctx, w.q, t, w.now())
	if err != nil {
		return err
	}
	formats, err := decodeStored[[]string](r.OutputFormatsJson)
	if err != nil {
		return err
	}
	existing, err := w.q.ArtifactsByRunUnordered(ctx, runID)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for i := range existing {
		have[existing[i].Format] = true
	}
	for _, format := range formats {
		if have[format] {
			continue
		}
		data, err := w.render(ctx, doc, format)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return httpx.Abort(http.StatusServiceUnavailable, "The report renderer returned an empty file.")
		}
		artifactID := uuid.New()
		key, err := w.storage.Store(data, artifactID, format)
		if err != nil {
			return err
		}
		contentType := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		if format == formatPDF {
			contentType = "application/pdf"
		}
		sum := sha256.Sum256(data)
		now := w.now()
		if err := w.q.InsertArtifact(ctx, reportingdb.InsertArtifactParams{
			ID: artifactID, RunID: runID, Format: format, Filename: reportFilename(doc.Title, format, now),
			ContentType: contentType, SizeBytes: int64(len(data)), Sha256: hex.EncodeToString(sum[:]),
			StorageKey: key, ExpiresAt: now.Add(90 * 86400 * time.Second), Now: now,
		}); err != nil {
			return err
		}
	}
	r.Status = "delivering"
	if err := w.saveRun(ctx, r); err != nil {
		return err
	}
	return w.createAndSendDeliveries(ctx, r, t)
}

// render is AdvancedReportGenerator.generate.
func (w *worker) render(ctx context.Context, d Document, format string) ([]byte, error) {
	if format == formatXLSX {
		return makeWorkbook(d), nil
	}
	return w.pdf(ctx, d)
}

// gotenbergBody is AdvancedReportGenerator.gotenbergBody: the HTML as a
// "files" part named index.html, then printBackground=true.
func gotenbergBody(html, boundary string) []byte {
	var b bytes.Buffer
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString(`Content-Disposition: form-data; name="files"; filename="index.html"` + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString(html)
	b.WriteString("\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString(`Content-Disposition: form-data; name="printBackground"` + "\r\n\r\n")
	b.WriteString("true\r\n")
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

func (w *worker) pdf(ctx context.Context, d Document) ([]byte, error) {
	boundary := "norviq-" + formatUUID(uuid.New())
	body := gotenbergBody(renderHTML(d), boundary)
	endpoint := strings.Trim(w.gotenberg, "/") + "/forms/chromium/convert/html"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.Abort(http.StatusServiceUnavailable, "PDF renderer is unavailable.")
	}
	return io.ReadAll(resp.Body)
}

func (w *worker) createAndSendDeliveries(ctx context.Context, r reportingdb.AdvancedReportRun, t TemplateInput) error {
	if err := w.ensureRunAccess(ctx, r, t); err != nil {
		if isRevokedAccess(err) {
			w.markAccessRevoked(ctx, r)
			return nil
		}
		return err
	}
	values, err := decodeStored[[]string](r.RecipientUserIdsJson)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for _, v := range values {
		if id, ok := parseSwiftUUID(v); ok {
			ids = append(ids, id)
		}
	}
	users, err := w.q.UsersByIDs(ctx, ids)
	if err != nil {
		return err
	}
	existing, err := w.q.DeliveriesByRunUnordered(ctx, r.ID)
	if err != nil {
		return err
	}
	for _, u := range users {
		if !u.IsVerified {
			continue
		}
		var delivery *reportingdb.AdvancedReportDelivery
		for i := range existing {
			if existing[i].RecipientUserID == u.ID {
				delivery = &existing[i]
				break
			}
		}
		if delivery == nil {
			now := w.now()
			d := reportingdb.AdvancedReportDelivery{
				ID: uuid.New(), RunID: r.ID, RecipientUserID: u.ID, RecipientEmail: u.Email,
				Status: "pending", CreatedAt: &now, UpdatedAt: &now,
			}
			if err := w.q.InsertDelivery(ctx, reportingdb.InsertDeliveryParams{
				ID: d.ID, RunID: d.RunID, RecipientUserID: d.RecipientUserID, RecipientEmail: d.RecipientEmail,
				Status: d.Status, Now: now,
			}); err != nil {
				return err
			}
			delivery = &d
		}
		w.send(ctx, *delivery, t.Name)
	}
	return w.updateRunDeliveryStatus(ctx, r.ID)
}

// send emails the recipient their signed links (at most 7 days).
func (w *worker) send(ctx context.Context, d reportingdb.AdvancedReportDelivery, templateName string) {
	err := func() error {
		now := w.now()
		d.Status = "sending"
		d.AttemptCount++
		d.LastAttemptAt = &now
		if err := w.saveDelivery(ctx, d); err != nil {
			return err
		}
		arts, err := w.q.ArtifactsByRun(ctx, d.RunID)
		if err != nil {
			return err
		}
		week := now.Add(7 * 86400 * time.Second)
		expiry := week
		if len(arts) > 0 {
			earliest := arts[0].ExpiresAt
			for i := range arts {
				if arts[i].ExpiresAt.Before(earliest) {
					earliest = arts[i].ExpiresAt
				}
			}
			if earliest.Before(expiry) {
				expiry = earliest
			}
		}
		links := make([]string, 0, len(arts))
		for i := range arts {
			a := &arts[i]
			sig := w.signer.Signature(a.ID, expiry, &d.RecipientUserID)
			links = append(links, fmt.Sprintf("%s: %s/v1/reporting/artifacts/%s/download?expires=%d&recipient=%s&signature=%s",
				a.Filename, w.baseURL, formatUUID(a.ID), expiry.Unix(), formatUUID(d.RecipientUserID), sig))
		}
		if err := w.mail.Send(ctx, mailer.Message{
			To:      d.RecipientEmail,
			Subject: "Your Norviq report is ready: " + templateName,
			Body:    "Your requested report is ready. These private links expire on " + isoTime(expiry) + ".\n\n" + strings.Join(links, "\n"),
			Purpose: "advanced_report",
		}); err != nil {
			return err
		}
		d.Status = "delivered"
		d.DeliveredAt = timep(w.now())
		d.LinkExpiresAt = &expiry
		d.FailureReason = nil
		return w.saveDelivery(ctx, d)
	}()
	if err == nil {
		return
	}
	if d.AttemptCount < 3 {
		d.Status = "retry"
	} else {
		d.Status = "failed"
	}
	d.FailureReason = strp(describe(err))
	_ = w.saveDelivery(ctx, d)
	w.logger.Error("advanced_report.delivery_failed id=" + formatUUID(d.ID) + " error=" + err.Error())
}

func (w *worker) updateRunDeliveryStatus(ctx context.Context, runID uuid.UUID) error {
	r, err := w.q.GetRun(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	deliveries, err := w.q.DeliveriesByRunUnordered(ctx, runID)
	if err != nil {
		return err
	}
	delivered, exhausted := 0, true
	for i := range deliveries {
		d := &deliveries[i]
		if d.Status == "delivered" {
			delivered++
		}
		if d.AttemptCount < 3 {
			exhausted = false
		}
	}
	switch {
	case len(deliveries) == 0:
		r.Status = "ready"
	case delivered == len(deliveries):
		r.Status = "delivered"
	case exhausted:
		if delivered > 0 {
			r.Status = "partially_delivered"
		} else {
			r.Status = "failed"
		}
	default:
		r.Status = "delivering"
	}
	if r.Status != "delivering" {
		r.CompletedAt = timep(w.now())
	}
	return w.saveRun(ctx, r)
}

// describe is String(describing: error) truncated to 500 characters. An
// Abort reads "Abort.<status>: <reason>"; other errors carry Go's text.
func describe(err error) string {
	s := err.Error()
	var he *httpx.Error
	if errors.As(err, &he) {
		s = "Abort." + strconv.Itoa(he.Status) + ": " + he.Reason
	}
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500])
	}
	return s
}

func (w *worker) failOrRetry(ctx context.Context, runID uuid.UUID, cause error) {
	r, err := w.q.GetRun(ctx, runID)
	if err != nil {
		return
	}
	r.FailureReason = strp(describe(cause))
	r.ClaimedAt = nil
	now := w.now()
	if r.AttemptCount < 3 {
		r.Status = "retry"
		r.RetryAt = timep(now.Add(time.Duration(math.Pow(2, float64(r.AttemptCount))*30) * time.Second))
	} else {
		r.Status = "failed"
		r.CompletedAt = &now
	}
	_ = w.saveRun(ctx, r)
	w.logger.Error("advanced_report.generation_failed id=" + strings.ToUpper(runID.String()) + " error=" + cause.Error())
}

func (w *worker) markAccessRevoked(ctx context.Context, r reportingdb.AdvancedReportRun) {
	now := w.now()
	r.Status = "failed"
	r.FailureReason = strp("Report generation stopped because portfolio access was revoked.")
	r.ClaimedAt = nil
	r.RetryAt = nil
	r.CompletedAt = &now
	_ = w.saveRun(ctx, r)
	if r.ScheduleID != nil {
		if sc, err := w.q.GetSchedule(ctx, *r.ScheduleID); err == nil {
			sc.NextRunAt, sc.PausedReason = nil, strp("access_revoked")
			_ = w.saveSchedule(ctx, sc)
		}
	}
	deliveries, err := w.q.DeliveriesByRunUnordered(ctx, r.ID)
	if err != nil {
		deliveries = nil
	}
	for i := range deliveries {
		d := deliveries[i]
		if d.Status == "delivered" {
			continue
		}
		d.Status = "failed"
		d.FailureReason = strp("Report delivery stopped because portfolio access was revoked.")
		_ = w.saveDelivery(ctx, d)
	}
	w.logger.Warn("advanced_report.access_revoked run_id=" + formatUUID(r.ID))
}
