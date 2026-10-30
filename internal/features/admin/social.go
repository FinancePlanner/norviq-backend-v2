package admin

import (
	"context"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/social"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/store/admindb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// socialReportDTO is SocialModerationReportDTO.
type socialReportDTO struct {
	ID             apijson.UUID  `json:"id"`
	ReporterID     apijson.UUID  `json:"reporterId"`
	TargetType     string        `json:"targetType"`
	TargetID       string        `json:"targetId"`
	TargetUsername *string       `json:"targetUsername,omitempty"`
	Reason         string        `json:"reason"`
	Note           *string       `json:"note,omitempty"`
	Status         string        `json:"status"`
	Resolution     *string       `json:"resolution,omitempty"`
	ResolutionNote *string       `json:"resolutionNote,omitempty"`
	CreatedAt      *apijson.Time `json:"createdAt,omitempty"`
	ResolvedAt     *apijson.Time `json:"resolvedAt,omitempty"`
}

type socialReportsResponse struct {
	Reports []socialReportDTO `json:"reports"`
}

// moderationAction is SocialModerationAction.
type moderationAction string

const (
	actionDismiss     moderationAction = "dismiss"
	actionSuspendUser moderationAction = "suspend_user"
)

func (moderationAction) SwiftEnum() (string, []string, bool) {
	return "SocialModerationAction", []string{string(actionDismiss), string(actionSuspendUser)}, false
}

// resolveBody is SocialModerationResolveBody.
type resolveBody struct {
	Action moderationAction `json:"action"`
	Note   *string          `json:"note"`
}

// GET /v1/admin/social/reports — `?status=open` (default) or `?status=all`,
// oldest first.
func (h *handler) socialReports(c *echo.Context) error {
	ctx := c.Request().Context()
	status := "open"
	if s := httpx.QueryString(c, "status"); s != nil {
		status = *s
	}
	var rows []admindb.SocialReport
	var err error
	if status != "all" {
		rows, err = h.q.OpenSocialReports(ctx)
	} else {
		rows, err = h.q.AllSocialReports(ctx)
	}
	if err != nil {
		return err
	}
	var userIDs []uuid.UUID
	for i := range rows {
		r := &rows[i]
		if r.TargetType != "user" {
			continue
		}
		if id, ok := portfolios.ParseSwiftUUID(r.TargetID); ok {
			userIDs = append(userIDs, id)
		}
	}
	names := map[uuid.UUID]string{}
	if len(userIDs) > 0 {
		if names, err = h.usernames(ctx, userIDs); err != nil {
			return err
		}
	}
	out := make([]socialReportDTO, len(rows))
	for i := range rows {
		r := &rows[i]
		dto := socialReportDTO{
			ID: apijson.NewUUID(r.ID), ReporterID: apijson.NewUUID(r.ReporterID),
			TargetType: r.TargetType, TargetID: r.TargetID, Reason: r.Reason, Note: r.Note,
			Status: r.Status, Resolution: r.Resolution, ResolutionNote: r.ResolutionNote,
			CreatedAt: apijson.TimePtr(&r.CreatedAt), ResolvedAt: apijson.TimePtr(r.ResolvedAt),
		}
		if id, ok := portfolios.ParseSwiftUUID(r.TargetID); ok {
			dto.TargetUsername = nameOf(names, id)
		}
		out[i] = dto
	}
	return httpx.OK(c, socialReportsResponse{Reports: out})
}

// POST /v1/admin/social/reports/:reportId/resolve
func (h *handler) resolveSocialReport(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	reportID, ok := portfolios.ParseSwiftUUID(c.Param("reportId"))
	if !ok {
		return httpx.NotFound("Report not found")
	}
	rows, err := h.q.ReportByID(ctx, reportID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("Report not found")
	}
	report := rows[0]
	var body resolveBody
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	if body.Action == actionSuspendUser {
		target, ok := portfolios.ParseSwiftUUID(report.TargetID)
		if report.TargetType != "user" || !ok {
			return httpx.Unprocessable("Only user reports can suspend a user.")
		}
		if err := h.setSuspended(ctx, target, true); err != nil {
			return err
		}
	}
	var note *string
	if body.Note != nil {
		n := strings.TrimSpace(*body.Note)
		note = &n
	}
	resolution := string(body.Action)
	now := h.now()
	moderator := s.UserID
	if err := h.q.ResolveSocialReport(ctx, admindb.ResolveSocialReportParams{
		Resolution: &resolution, ResolutionNote: note, ResolvedBy: &moderator, ResolvedAt: &now, ID: reportID,
	}); err != nil {
		return err
	}
	h.d.Logger.Info("social.report resolved action=" + resolution)
	return httpx.NoContent(c)
}

// POST /v1/admin/social/users/:userId/suspend
func (h *handler) suspend(c *echo.Context) error {
	id, err := userParameter(c)
	if err != nil {
		return err
	}
	if err := h.setSuspended(c.Request().Context(), id, true); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// DELETE /v1/admin/social/users/:userId/suspend
func (h *handler) unsuspend(c *echo.Context) error {
	id, err := userParameter(c)
	if err != nil {
		return err
	}
	if err := h.setSuspended(c.Request().Context(), id, false); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func userParameter(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("userId"))
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Invalid userId")
	}
	return id, nil
}

// setSuspended is SocialModerationController.setSuspended: suspending also
// ends the user's friendships and pending requests. A user without a
// settings row gets one with the defaults, in either direction, as Fluent's
// `first() ?? SocialSettingsRecord(userId:)` followed by save did.
func (h *handler) setSuspended(ctx context.Context, userID uuid.UUID, suspended bool) error {
	rows, err := h.q.UserForModeration(ctx, userID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("User not found")
	}
	if suspended && users.IsSystemEmail(rows[0].Email) {
		return httpx.BadRequest("That account can't be suspended.")
	}
	return h.inTx(ctx, func(q *admindb.Queries) error {
		now := h.now()
		existing, err := q.SocialSettingsSuspension(ctx, userID)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			var suspendedAt *time.Time
			if suspended {
				suspendedAt = &now
			}
			d := social.DefaultPrivacy
			if err := q.InsertSocialSettings(ctx, admindb.InsertSocialSettingsParams{
				ID: uuid.New(), UserID: userID, SearchVisibility: d.SearchVisibility,
				DiscoverableByContacts: d.DiscoverableByContacts, DiscoverableByX: d.DiscoverableByX,
				DiscoverableByFacebook: d.DiscoverableByFacebook, ShowReturnPercent: d.ShowReturnPercent,
				ShowStreaks: d.ShowStreaks, ShowXp: d.ShowXP, LeaderboardOptIn: d.LeaderboardOptIn,
				ShowAchievements: d.ShowAchievements, SuspendedAt: suspendedAt, CreatedAt: now,
			}); err != nil {
				return err
			}
		} else {
			rec := existing[0]
			var suspendedAt *time.Time
			if suspended {
				suspendedAt = rec.SuspendedAt
				if suspendedAt == nil {
					suspendedAt = &now
				}
			}
			if err := q.SetSocialSuspended(ctx, admindb.SetSocialSuspendedParams{SuspendedAt: suspendedAt, UpdatedAt: &now, ID: rec.ID}); err != nil {
				return err
			}
		}
		if suspended {
			if err := q.DeleteFriendshipsOf(ctx, userID); err != nil {
				return err
			}
			return q.DeleteFriendRequestsOf(ctx, userID)
		}
		return nil
	})
}
