package reporting

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/reportingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func notFound() error { return httpx.NotFound("") }

// requirePro is the controller's requirePro: BillingUpgradeRequiredError
// with the user's level unless they resolve to Pro.
func (h *handlers) requirePro(ctx context.Context, userID uuid.UUID, feature entitlements.Feature) error {
	snap, err := h.ent.Resolve(ctx, userID)
	if err != nil {
		return err
	}
	if !snap.IsPro() {
		return entitlements.UpgradeRequired(feature, snap.Level)
	}
	return nil
}

// pathUUID is req.parameters.get(name).flatMap(UUID.init(uuidString:)).
func pathUUID(c *echo.Context, name string) (uuid.UUID, bool) {
	return parseSwiftUUID(c.Param(name))
}

func (h *handlers) ownedTemplate(ctx context.Context, c *echo.Context, userID uuid.UUID, includeArchived bool) (reportingdb.AdvancedReportTemplate, error) {
	id, ok := pathUUID(c, "templateId")
	if !ok {
		return reportingdb.AdvancedReportTemplate{}, notFound()
	}
	t, err := h.q.GetTemplate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, notFound()
	}
	if err != nil {
		return t, err
	}
	if t.OwnerUserID != userID || (!includeArchived && t.ArchivedAt != nil) {
		return t, notFound()
	}
	return t, nil
}

func (h *handlers) ownedSchedule(ctx context.Context, c *echo.Context, userID uuid.UUID) (reportingdb.AdvancedReportSchedule, error) {
	id, ok := pathUUID(c, "scheduleId")
	if !ok {
		return reportingdb.AdvancedReportSchedule{}, notFound()
	}
	s, err := h.q.GetSchedule(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, notFound()
	}
	if err != nil {
		return s, err
	}
	if s.OwnerUserID != userID {
		return s, notFound()
	}
	return s, nil
}

// GET /v1/reporting/templates
func (h *handlers) listTemplates(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListTemplates(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	items := make([]Template, 0, len(rows))
	for _, r := range rows {
		t, err := templateDTO(r)
		if err != nil {
			return err
		}
		items = append(items, t)
	}
	return httpx.OK(c, Page[Template]{Items: items})
}

// POST /v1/reporting/templates
func (h *handlers) createTemplate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePro(ctx, s.UserID, entitlements.FeatureAdvancedReportTemplates); err != nil {
		return err
	}
	in, err := decodeTemplateInput(c)
	if err != nil {
		return err
	}
	if err := h.validateTemplate(ctx, in, s.UserID); err != nil {
		return err
	}
	raw, err := encodeReportJSON(in)
	if err != nil {
		return err
	}
	now := h.now()
	id := uuid.New()
	if err := h.q.InsertTemplate(ctx, reportingdb.InsertTemplateParams{
		ID: id, OwnerUserID: s.UserID, InputJson: raw, Revision: 1, IsStarterTemplate: false, Now: now,
	}); err != nil {
		return err
	}
	out, err := templateDTO(reportingdb.AdvancedReportTemplate{
		ID: id, OwnerUserID: s.UserID, InputJson: raw, Revision: 1, CreatedAt: &now, UpdatedAt: &now,
	})
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

// GET /v1/reporting/templates/:templateId (archived templates included)
func (h *handlers) getTemplate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	t, err := h.ownedTemplate(c.Request().Context(), c, s.UserID, true)
	if err != nil {
		return err
	}
	out, err := templateDTO(t)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// PUT /v1/reporting/templates/:templateId
func (h *handlers) updateTemplate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePro(ctx, s.UserID, entitlements.FeatureAdvancedReportTemplates); err != nil {
		return err
	}
	t, err := h.ownedTemplate(ctx, c, s.UserID, false)
	if err != nil {
		return err
	}
	in, err := decodeTemplateInput(c)
	if err != nil {
		return err
	}
	if err := h.validateTemplate(ctx, in, s.UserID); err != nil {
		return err
	}
	raw, err := encodeReportJSON(in)
	if err != nil {
		return err
	}
	now := h.now()
	t.InputJson = raw
	t.Revision++
	t.UpdatedAt = &now
	if err := h.q.UpdateTemplate(ctx, reportingdb.UpdateTemplateParams{
		ID: t.ID, InputJson: t.InputJson, Revision: t.Revision, ArchivedAt: t.ArchivedAt, Now: now,
	}); err != nil {
		return err
	}
	out, err := templateDTO(t)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// DELETE /v1/reporting/templates/:templateId archives the template and
// pauses every schedule that uses it.
func (h *handlers) archiveTemplate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	t, err := h.ownedTemplate(ctx, c, s.UserID, false)
	if err != nil {
		return err
	}
	now := h.now()
	if err := h.q.UpdateTemplate(ctx, reportingdb.UpdateTemplateParams{
		ID: t.ID, InputJson: t.InputJson, Revision: t.Revision, ArchivedAt: &now, Now: now,
	}); err != nil {
		return err
	}
	schedules, err := h.q.SchedulesByOwner(ctx, s.UserID)
	if err != nil {
		return err
	}
	tid := formatUUID(t.ID)
	paused := "template_archived"
	for _, sc := range schedules {
		in, err := decodeStored[ScheduleInput](sc.InputJson)
		if err != nil {
			return err
		}
		if in.TemplateID != tid {
			continue
		}
		if err := h.q.UpdateSchedule(ctx, reportingdb.UpdateScheduleParams{
			ID: sc.ID, InputJson: sc.InputJson, NextRunAt: nil, LastRunAt: sc.LastRunAt,
			PausedReason: &paused, Now: now,
		}); err != nil {
			return err
		}
	}
	return httpx.NoContent(c)
}

// GET /v1/reporting/schedules
func (h *handlers) listSchedules(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListSchedules(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	items := make([]Schedule, 0, len(rows))
	for _, r := range rows {
		sc, err := scheduleDTO(r)
		if err != nil {
			return err
		}
		items = append(items, sc)
	}
	return httpx.OK(c, Page[Schedule]{Items: items})
}

// POST /v1/reporting/schedules
func (h *handlers) createSchedule(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePro(ctx, s.UserID, entitlements.FeatureAdvancedReportSchedules); err != nil {
		return err
	}
	count, err := h.q.CountSchedules(ctx, s.UserID)
	if err != nil {
		return err
	}
	if count >= 20 {
		return entitlements.UpgradeLimit(entitlements.FeatureAdvancedReportSchedules, "pro", 20, int(count))
	}
	in, err := decodeScheduleInput(c)
	if err != nil {
		return err
	}
	if err := h.validateSchedule(ctx, in, s.UserID); err != nil {
		return err
	}
	now := h.now()
	var next *time.Time
	if in.IsEnabled {
		n, err := nextRun(now, in.Recurrence)
		if err != nil {
			return err
		}
		next = &n
	}
	raw, err := encodeReportJSON(in)
	if err != nil {
		return err
	}
	id := uuid.New()
	if err := h.q.InsertSchedule(ctx, reportingdb.InsertScheduleParams{
		ID: id, OwnerUserID: s.UserID, InputJson: raw, NextRunAt: next, Now: now,
	}); err != nil {
		return err
	}
	out, err := scheduleDTO(reportingdb.AdvancedReportSchedule{
		ID: id, OwnerUserID: s.UserID, InputJson: raw, NextRunAt: next, CreatedAt: &now, UpdatedAt: &now,
	})
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

// GET /v1/reporting/schedules/:scheduleId
func (h *handlers) getSchedule(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	sc, err := h.ownedSchedule(c.Request().Context(), c, s.UserID)
	if err != nil {
		return err
	}
	out, err := scheduleDTO(sc)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// PUT /v1/reporting/schedules/:scheduleId
func (h *handlers) updateSchedule(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePro(ctx, s.UserID, entitlements.FeatureAdvancedReportSchedules); err != nil {
		return err
	}
	sc, err := h.ownedSchedule(ctx, c, s.UserID)
	if err != nil {
		return err
	}
	in, err := decodeScheduleInput(c)
	if err != nil {
		return err
	}
	if err := h.validateSchedule(ctx, in, s.UserID); err != nil {
		return err
	}
	raw, err := encodeReportJSON(in)
	if err != nil {
		return err
	}
	now := h.now()
	sc.InputJson = raw
	sc.NextRunAt = nil
	sc.PausedReason = nil
	if in.IsEnabled {
		n, err := nextRun(now, in.Recurrence)
		if err != nil {
			return err
		}
		sc.NextRunAt = &n
	} else {
		r := "disabled_by_user"
		sc.PausedReason = &r
	}
	sc.UpdatedAt = &now
	if err := h.q.UpdateSchedule(ctx, reportingdb.UpdateScheduleParams{
		ID: sc.ID, InputJson: sc.InputJson, NextRunAt: sc.NextRunAt, LastRunAt: sc.LastRunAt,
		PausedReason: sc.PausedReason, Now: now,
	}); err != nil {
		return err
	}
	out, err := scheduleDTO(sc)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// DELETE /v1/reporting/schedules/:scheduleId
func (h *handlers) deleteSchedule(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	sc, err := h.ownedSchedule(ctx, c, s.UserID)
	if err != nil {
		return err
	}
	if err := h.q.DeleteSchedule(ctx, sc.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// GET /v1/reporting/runs: the latest 100.
func (h *handlers) listRuns(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rows, err := h.q.ListRuns(ctx, s.UserID)
	if err != nil {
		return err
	}
	items := make([]Run, 0, len(rows))
	for i := range rows {
		run, err := runDTO(ctx, h.q, &rows[i])
		if err != nil {
			return err
		}
		items = append(items, run)
	}
	return httpx.OK(c, Page[Run]{Items: items})
}

// POST /v1/reporting/runs queues a run for the worker (202).
func (h *handlers) createRun(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePro(ctx, s.UserID, entitlements.FeatureAdvancedReportRuns); err != nil {
		return err
	}
	in, err := decodeRunCreate(c)
	if err != nil {
		return err
	}
	templateID, err := requireUUID(in.TemplateID, "templateId")
	if err != nil {
		return err
	}
	t, err := h.q.GetTemplate(ctx, templateID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (t.OwnerUserID != s.UserID || t.ArchivedAt != nil)) {
		return notFound()
	}
	if err != nil {
		return err
	}
	if err := validateFormats(in.OutputFormats); err != nil {
		return err
	}
	tin, err := decodeStored[TemplateInput](t.InputJson)
	if err != nil {
		return err
	}
	if err := h.validateRecipients(ctx, in.RecipientUserIDs, tin, s.UserID); err != nil {
		return err
	}
	now := h.now()
	today, err := artifactsRequestedToday(ctx, h.q, s.UserID, now)
	if err != nil {
		return err
	}
	if today+len(in.OutputFormats) > 100 {
		return httpx.Abort(http.StatusTooManyRequests, "The daily limit of 100 generated report files has been reached.")
	}
	recipients := in.RecipientUserIDs
	if len(recipients) == 0 {
		recipients = []string{formatUUID(s.UserID)}
	}
	formatsJSON, err := encodeReportJSON(in.OutputFormats)
	if err != nil {
		return err
	}
	recipientsJSON, err := encodeReportJSON(recipients)
	if err != nil {
		return err
	}
	run := reportingdb.AdvancedReportRun{
		ID: uuid.New(), TemplateID: templateID, RequestedByUserID: s.UserID,
		TemplateRevision: t.Revision, TemplateInputJson: t.InputJson,
		OutputFormatsJson: formatsJSON, RecipientUserIdsJson: recipientsJSON,
		Status: "pending", CreatedAt: &now, UpdatedAt: &now,
	}
	if err := h.q.InsertRun(ctx, reportingdb.InsertRunParams{
		ID: run.ID, TemplateID: run.TemplateID, RequestedByUserID: run.RequestedByUserID,
		TemplateRevision: run.TemplateRevision, TemplateInputJson: run.TemplateInputJson,
		OutputFormatsJson: run.OutputFormatsJson, RecipientUserIdsJson: run.RecipientUserIdsJson,
		Status: run.Status, Now: now,
	}); err != nil {
		return err
	}
	out, err := runDTO(ctx, h.q, &run)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusAccepted, out)
}

// GET /v1/reporting/runs/:runId
func (h *handlers) getRun(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	id, ok := pathUUID(c, "runId")
	if !ok {
		return notFound()
	}
	r, err := h.q.GetRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	if r.RequestedByUserID != s.UserID {
		return notFound()
	}
	out, err := runDTO(ctx, h.q, &r)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// signedURL is the download link the controller and the worker build.
func (h *handlers) signedURL(artifactID uuid.UUID, expires time.Time, recipient uuid.UUID) string {
	sig := h.signer.Signature(artifactID, expires, &recipient)
	return fmt.Sprintf("%s/v1/reporting/artifacts/%s/download?expires=%d&recipient=%s&signature=%s",
		h.settings.PublicAPIBaseURL, formatUUID(artifactID), expires.Unix(), formatUUID(recipient), sig)
}

// GET /v1/reporting/artifacts/:artifactId/link: a link valid for at most 24h.
func (h *handlers) artifactLink(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	id, ok := pathUUID(c, "artifactId")
	if !ok {
		return notFound()
	}
	a, err := h.q.GetArtifact(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	run, err := h.q.GetRun(ctx, a.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	if run.RequestedByUserID != s.UserID {
		return notFound()
	}
	now := h.now()
	if !a.ExpiresAt.After(now) {
		return httpx.Abort(statusGone, "This report has expired.")
	}
	expiry := a.ExpiresAt
	if limit := now.Add(24 * time.Hour); limit.Before(expiry) {
		expiry = limit
	}
	return httpx.OK(c, ArtifactDownloadResponse{URL: h.signedURL(a.ID, expiry, s.UserID), ExpiresAt: isoTime(expiry)})
}

// queryValue is the last value of a query key (URLEncodedFormDecoder).
func queryValue(c *echo.Context, key string) (string, bool) {
	vs, ok := c.Request().URL.Query()[key]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

func queryValueNotFound(typ, key string) error {
	return httpx.BadRequest(fmt.Sprintf("No value found (expected type '%s') at path '%s'. No %s was found at '%s'.", typ, key, typ, key))
}

// GET /v1/reporting/artifacts/:artifactId/download (unauthenticated,
// signed link).
func (h *handlers) downloadArtifact(c *echo.Context) error {
	ctx := c.Request().Context()
	id, ok := pathUUID(c, "artifactId")
	if !ok {
		return notFound()
	}
	a, err := h.q.GetArtifact(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	// ArtifactDownloadQuery { expires: Int64, recipient: String?, signature: String }
	rawExpires, ok := queryValue(c, "expires")
	if !ok {
		return queryValueNotFound("Int64", "expires")
	}
	expires, err := strconv.ParseInt(rawExpires, 10, 64)
	if err != nil {
		return httpx.BadRequest("Value was not of type 'Int64' at path 'expires'. Data found at 'expires' was not Int64.")
	}
	var recipient *uuid.UUID
	if raw, ok := queryValue(c, "recipient"); ok {
		if r, ok := parseSwiftUUID(raw); ok {
			recipient = &r
		}
	}
	signature, ok := queryValue(c, "signature")
	if !ok {
		return queryValueNotFound("String", "signature")
	}
	now := h.now()
	if !a.ExpiresAt.After(now) || !h.signer.Verify(signature, id, time.Unix(expires, 0), recipient, now) {
		return httpx.Forbidden("This report link is invalid or expired.")
	}
	run, err := h.q.GetRun(ctx, a.RunID)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var allowed []string
	if found {
		if allowed, err = decodeStored[[]string](run.RecipientUserIdsJson); err != nil {
			allowed = nil
		}
	}
	recipientString := ""
	if recipient != nil {
		recipientString = formatUUID(*recipient)
	}
	permitted := false
	if found {
		permitted = recipient != nil && *recipient == run.RequestedByUserID
		for _, v := range allowed {
			if v == recipientString {
				permitted = true
			}
		}
	}
	if !permitted {
		return httpx.Forbidden("")
	}
	data, err := h.storage.Load(a.StorageKey)
	if err != nil {
		return err
	}
	hdr := c.Response().Header()
	hdr.Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(a.Filename, `"`, "")+`"`)
	hdr.Set("Cache-Control", "private, no-store")
	return c.Blob(http.StatusOK, a.ContentType, data)
}

// validateTemplate is the controller's validateTemplate.
func (h *handlers) validateTemplate(ctx context.Context, in TemplateInput, userID uuid.UUID) error {
	if strings.TrimSpace(in.Name) == "" || swiftCount(in.Name) > 120 {
		return httpx.BadRequest("Template name must contain 1 to 120 characters.")
	}
	if len(in.Blocks) == 0 || len(in.Blocks) > 30 {
		return httpx.BadRequest("A report template must contain 1 to 30 blocks.")
	}
	ids := map[string]bool{}
	for i := range in.Blocks {
		ids[in.Blocks[i].ID] = true
	}
	if len(ids) != len(in.Blocks) {
		return httpx.BadRequest("Report block IDs must be unique.")
	}
	portfolios, err := templatePortfolioIDs(in)
	if err != nil {
		return err
	}
	for _, p := range portfolios {
		if err := requirePortfolioAccess(ctx, h.q, p, userID); err != nil {
			return err
		}
	}
	return nil
}

// validateSchedule is the controller's validateSchedule.
func (h *handlers) validateSchedule(ctx context.Context, in ScheduleInput, userID uuid.UUID) error {
	if strings.TrimSpace(in.Name) == "" || swiftCount(in.Name) > 120 {
		return httpx.BadRequest("Schedule name must contain 1 to 120 characters.")
	}
	if err := validateFormats(in.OutputFormats); err != nil {
		return err
	}
	if _, err := nextRun(h.now(), in.Recurrence); err != nil {
		return err
	}
	templateID, err := requireUUID(in.TemplateID, "templateId")
	if err != nil {
		return err
	}
	t, err := h.q.GetTemplate(ctx, templateID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (t.OwnerUserID != userID || t.ArchivedAt != nil)) {
		return httpx.NotFound("Report template not found.")
	}
	if err != nil {
		return err
	}
	tin, err := decodeStored[TemplateInput](t.InputJson)
	if err != nil {
		return err
	}
	return h.validateRecipients(ctx, in.RecipientUserIDs, tin, userID)
}

func validateFormats(formats []string) error {
	seen := map[string]bool{}
	for _, f := range formats {
		seen[f] = true
	}
	if len(formats) == 0 || len(formats) > 2 || len(seen) != len(formats) {
		return httpx.BadRequest("Choose one or two distinct report output formats.")
	}
	return nil
}

// validateRecipients: unique verified users, at most six, each with
// access to every portfolio the template reads.
func (h *handlers) validateRecipients(ctx context.Context, values []string, t TemplateInput, ownerID uuid.UUID) error {
	ids := []uuid.UUID{ownerID}
	if len(values) > 0 {
		ids = make([]uuid.UUID, 0, len(values))
		for _, v := range values {
			id, err := requireUUID(v, "recipientUserIds")
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
	}
	unique := map[uuid.UUID]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	if len(unique) != len(ids) || len(ids) > 6 {
		return httpx.BadRequest("Recipients must be unique and limited to the owner plus five shared members.")
	}
	users, err := h.q.UsersByIDs(ctx, ids)
	if err != nil {
		return err
	}
	verified := len(users) == len(ids)
	for _, u := range users {
		verified = verified && u.IsVerified
	}
	if !verified {
		return httpx.BadRequest("Every report recipient must have a verified account.")
	}
	portfolios, err := templatePortfolioIDs(t)
	if err != nil {
		return err
	}
	for _, p := range portfolios {
		for _, r := range ids {
			if err := requirePortfolioAccess(ctx, h.q, p, r); err != nil {
				return err
			}
		}
	}
	return nil
}
