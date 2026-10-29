package reporting

import (
	"context"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/reportingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// errPortfolioNotFound is PortfolioAccessService's Abort(.notFound).
func errPortfolioNotFound() error { return httpx.NotFound("Portfolio not found.") }

// requirePortfolioAccess is PortfolioAccessService.require(portfolioId:userId:)
// with editing and ownerOnly false: the owner or an active member passes.
// (The owner's entitlement it resolved only mattered for editing.)
func requirePortfolioAccess(ctx context.Context, q *reportingdb.Queries, portfolioID, userID uuid.UUID) error {
	owner, err := q.PortfolioOwner(ctx, portfolioID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errPortfolioNotFound()
	}
	if err != nil {
		return err
	}
	if owner == userID {
		return nil
	}
	ok, err := q.ActiveMembershipExists(ctx, reportingdb.ActiveMembershipExistsParams{PortfolioID: portfolioID, UserID: userID})
	if err != nil {
		return err
	}
	if !ok {
		return errPortfolioNotFound()
	}
	return nil
}

// isRevokedAccess is isRevokedReportAccess: a 404 or 403 abort.
func isRevokedAccess(err error) bool {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Status == http.StatusNotFound || he.Status == http.StatusForbidden
	}
	var ue *entitlements.UpgradeRequiredError
	return errors.As(err, &ue) // BillingUpgradeRequiredError is a 403 AbortError
}

func requireUUID(value, field string) (uuid.UUID, error) {
	id, ok := parseSwiftUUID(value)
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Invalid " + field + ".")
	}
	return id, nil
}

// templatePortfolioIDs is portfolioIds(in:): every block's portfolios plus
// its comparison portfolio, deduplicated.
func templatePortfolioIDs(t TemplateInput) ([]uuid.UUID, error) {
	seen := map[string]bool{}
	var out []uuid.UUID
	add := func(v string) error {
		if seen[v] {
			return nil
		}
		seen[v] = true
		id, err := requireUUID(v, "portfolioIds")
		if err != nil {
			return err
		}
		out = append(out, id)
		return nil
	}
	for i := range t.Blocks {
		b := &t.Blocks[i]
		for _, p := range b.PortfolioIDs {
			if err := add(p); err != nil {
				return nil, err
			}
		}
		if b.Settings.ComparisonPortfolioID != nil {
			if err := add(*b.Settings.ComparisonPortfolioID); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// swiftCount is String.count, approximated by runes.
func swiftCount(s string) int { return utf8.RuneCountInString(s) }

// startOfUTCDay is Calendar(gregorian, UTC).startOfDay(for:).
func startOfUTCDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// artifactsRequestedToday sums the formats of the user's runs created since
// UTC midnight.
func artifactsRequestedToday(ctx context.Context, q *reportingdb.Queries, userID uuid.UUID, now time.Time) (int, error) {
	rows, err := q.RunFormatsSince(ctx, reportingdb.RunFormatsSinceParams{UserID: userID, Since: startOfUTCDay(now)})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, raw := range rows {
		formats, err := decodeStored[[]string](raw)
		if err != nil {
			return 0, err
		}
		total += len(formats)
	}
	return total, nil
}

func formatUUID(id uuid.UUID) string { return apijson.FormatUUID(id) }

func templateDTO(r reportingdb.AdvancedReportTemplate) (Template, error) {
	in, err := decodeStored[TemplateInput](r.InputJson)
	if err != nil {
		return Template{}, err
	}
	return Template{
		ID:                formatUUID(r.ID),
		OwnerUserID:       formatUUID(r.OwnerUserID),
		Input:             normalizeTemplate(in),
		Revision:          int(r.Revision),
		IsStarterTemplate: r.IsStarterTemplate,
		ArchivedAt:        isoPtr(r.ArchivedAt),
		CreatedAt:         isoOrEmpty(r.CreatedAt),
		UpdatedAt:         isoPtr(r.UpdatedAt),
	}, nil
}

// normalizeTemplate keeps arrays non-nil for encoding.
func normalizeTemplate(t TemplateInput) TemplateInput {
	if t.Blocks == nil {
		t.Blocks = []Block{}
	}
	for i := range t.Blocks {
		if t.Blocks[i].PortfolioIDs == nil {
			t.Blocks[i].PortfolioIDs = []string{}
		}
	}
	return t
}

func scheduleDTO(r reportingdb.AdvancedReportSchedule) (Schedule, error) {
	in, err := decodeStored[ScheduleInput](r.InputJson)
	if err != nil {
		return Schedule{}, err
	}
	if in.OutputFormats == nil {
		in.OutputFormats = []string{}
	}
	if in.RecipientUserIDs == nil {
		in.RecipientUserIDs = []string{}
	}
	return Schedule{
		ID:           formatUUID(r.ID),
		OwnerUserID:  formatUUID(r.OwnerUserID),
		Input:        in,
		NextRunAt:    isoPtr(r.NextRunAt),
		LastRunAt:    isoPtr(r.LastRunAt),
		PausedReason: r.PausedReason,
		CreatedAt:    isoOrEmpty(r.CreatedAt),
		UpdatedAt:    isoPtr(r.UpdatedAt),
	}, nil
}

func artifactDTO(a *reportingdb.AdvancedReportArtifact) Artifact {
	format := a.Format
	if !outputFormats[format] {
		format = formatPDF
	}
	id := formatUUID(a.ID)
	return Artifact{
		ID:           id,
		RunID:        formatUUID(a.RunID),
		Format:       format,
		Filename:     a.Filename,
		ContentType:  a.ContentType,
		SizeBytes:    a.SizeBytes,
		SHA256:       a.Sha256,
		DownloadPath: "/v1/reporting/artifacts/" + id + "/download",
		ExpiresAt:    isoTime(a.ExpiresAt),
		CreatedAt:    isoOrEmpty(a.CreatedAt),
	}
}

func deliveryDTO(d *reportingdb.AdvancedReportDelivery) Delivery {
	status := d.Status
	if !deliveryStatuses[status] {
		status = "failed"
	}
	return Delivery{
		ID:              formatUUID(d.ID),
		RunID:           formatUUID(d.RunID),
		RecipientUserID: formatUUID(d.RecipientUserID),
		RecipientEmail:  d.RecipientEmail,
		Status:          status,
		AttemptCount:    int(d.AttemptCount),
		LastAttemptAt:   isoPtr(d.LastAttemptAt),
		DeliveredAt:     isoPtr(d.DeliveredAt),
		FailureReason:   d.FailureReason,
		LinkExpiresAt:   isoPtr(d.LinkExpiresAt),
	}
}

// runDTO is AdvancedReportMapper.run.
func runDTO(ctx context.Context, q *reportingdb.Queries, r *reportingdb.AdvancedReportRun) (Run, error) {
	arts, err := q.ArtifactsByRun(ctx, r.ID)
	if err != nil {
		return Run{}, err
	}
	dels, err := q.DeliveriesByRun(ctx, r.ID)
	if err != nil {
		return Run{}, err
	}
	formats, err := decodeStored[[]string](r.OutputFormatsJson)
	if err != nil {
		return Run{}, err
	}
	if formats == nil {
		formats = []string{}
	}
	status := r.Status
	if !runStatuses[status] {
		status = "failed"
	}
	out := Run{
		ID:                formatUUID(r.ID),
		TemplateID:        formatUUID(r.TemplateID),
		RequestedByUserID: formatUUID(r.RequestedByUserID),
		TemplateRevision:  int(r.TemplateRevision),
		OutputFormats:     formats,
		Status:            status,
		ScheduledFor:      isoPtr(r.ScheduledFor),
		StartedAt:         isoPtr(r.StartedAt),
		CompletedAt:       isoPtr(r.CompletedAt),
		FailureReason:     r.FailureReason,
		Artifacts:         make([]Artifact, 0, len(arts)),
		Deliveries:        make([]Delivery, 0, len(dels)),
		CreatedAt:         isoOrEmpty(r.CreatedAt),
	}
	if r.ScheduleID != nil {
		s := formatUUID(*r.ScheduleID)
		out.ScheduleID = &s
	}
	for i := range arts {
		out.Artifacts = append(out.Artifacts, artifactDTO(&arts[i]))
	}
	for i := range dels {
		out.Deliveries = append(out.Deliveries, deliveryDTO(&dels[i]))
	}
	return out, nil
}
