package assistant

import (
	"context"
	"errors"
	"net/http"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// preference is the controller's find-or-create helper.
func (h *handlers) preference(ctx context.Context, userID uuid.UUID) (assistantdb.AiPreference, error) {
	row, err := h.q.GetPreference(ctx, userID)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return row, err
	}
	now := h.now()
	row = assistantdb.AiPreference{
		ID: uuid.New(), UserID: userID, Timezone: "UTC", CreatedAt: &now, UpdatedAt: &now,
	}
	err = h.q.InsertPreference(ctx, assistantdb.InsertPreferenceParams{
		ID: row.ID, UserID: userID, ProactiveTipsEnabled: false, PushEnabled: false, Timezone: "UTC", CreatedAt: &now,
	})
	return row, err
}

func (h *handlers) getPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	row, err := h.preference(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, preferencesResponse{
		ProactiveTipsEnabled: row.ProactiveTipsEnabled, PushEnabled: row.PushEnabled, Timezone: row.Timezone,
		ImagesEnabled: h.imagesEnabled,
	})
}

// validTimeZone is `TimeZone(identifier:) != nil`.
func validTimeZone(id string) bool {
	if id == "" || id == "Local" {
		return false
	}
	_, err := time.LoadLocation(id)
	return err == nil
}

func (h *handlers) updatePreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var payload struct {
		ProactiveTipsEnabled bool   `json:"proactiveTipsEnabled"`
		PushEnabled          bool   `json:"pushEnabled"`
		Timezone             string `json:"timezone"`
	}
	if err := swiftjson.DecodeRequest(c, &payload); err != nil {
		return err
	}
	if !validTimeZone(payload.Timezone) {
		return httpx.BadRequest("Invalid IANA timezone.")
	}
	ctx := c.Request().Context()
	row, err := h.preference(ctx, s.UserID)
	if err != nil {
		return err
	}
	now := h.now()
	if err := h.q.UpdatePreference(ctx, assistantdb.UpdatePreferenceParams{
		ID: row.ID, ProactiveTipsEnabled: payload.ProactiveTipsEnabled, PushEnabled: payload.PushEnabled,
		Timezone: payload.Timezone, UpdatedAt: &now,
	}); err != nil {
		return err
	}
	return httpx.OK(c, preferencesResponse{
		ProactiveTipsEnabled: payload.ProactiveTipsEnabled, PushEnabled: payload.PushEnabled, Timezone: payload.Timezone,
		ImagesEnabled: h.imagesEnabled,
	})
}

func (h *handlers) listTips(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListTips(c.Request().Context(), assistantdb.ListTipsParams{UserID: s.UserID, Now: h.now()})
	if err != nil {
		return err
	}
	now := h.now()
	out := make([]tipResponse, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		title, err := h.pii.DecryptString(r.TitleEncrypted)
		if err != nil {
			return err
		}
		body, err := h.pii.DecryptString(r.BodyEncrypted)
		if err != nil {
			return err
		}
		expires := r.ExpiresAt
		out = append(out, tipResponse{
			ID: apijson.FormatUUID(r.ID), Kind: r.Kind, Title: title, Body: body, Importance: int(r.Importance),
			ActionPath: r.ActionPath, CreatedAt: isoTimestamp(r.CreatedAt, now), ExpiresAt: isoTimestamp(&expires, now),
		})
	}
	return httpx.OK(c, out)
}

func (h *handlers) ownedTip(c *echo.Context) (assistantdb.AiTip, error) {
	s, err := auth.Require(c)
	if err != nil {
		return assistantdb.AiTip{}, err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return assistantdb.AiTip{}, httpx.NotFound("")
	}
	row, err := h.q.GetOwnedTip(c.Request().Context(), assistantdb.GetOwnedTipParams{ID: id, UserID: s.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, httpx.NotFound("")
	}
	return row, err
}

func (h *handlers) markTipSeen(c *echo.Context) error {
	row, err := h.ownedTip(c)
	if err != nil {
		return err
	}
	if err := h.q.MarkTipSeen(c.Request().Context(), row.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) dismissTip(c *echo.Context) error {
	row, err := h.ownedTip(c)
	if err != nil {
		return err
	}
	if err := h.q.DismissTip(c.Request().Context(), row.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) getUsage(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	start := ai.UsageMonthStart(h.now())
	used, err := h.ai.MonthlyUsage(ctx, s.UserID, start)
	if err != nil {
		return err
	}
	billing, err := h.ent.Context(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := usageResponse{Month: monthString(start), Used: int(used), IsPro: billing.IsPro}
	if !billing.IsPro {
		limit := h.ai.Settings.FreeMonthlyLimit
		remaining := max(0, limit-int(used))
		out.Limit, out.Remaining = &limit, &remaining
	}
	return httpx.OK(c, out)
}

func (h *handlers) pendingActionDTO(row assistantdb.AiPendingAction, status string, now time.Time) (pendingActionResponse, error) {
	summary, err := h.pii.DecryptString(row.SummaryEncrypted)
	if err != nil {
		return pendingActionResponse{}, err
	}
	args, err := h.pii.DecryptString(row.ArgumentsEncrypted)
	if err != nil {
		return pendingActionResponse{}, err
	}
	var conv *string
	if row.ConversationID != nil {
		conv = strPtr(apijson.FormatUUID(*row.ConversationID))
	}
	expires := row.ExpiresAt
	return pendingActionResponse{
		ID: apijson.FormatUUID(row.ID), ConversationID: conv, ToolName: row.ToolName, Summary: summary,
		Arguments: args, Status: status, ExpiresAt: isoTimestamp(&expires, now), CreatedAt: isoTimestamp(row.CreatedAt, now),
	}, nil
}

// knownStatus is `AIActionStatus(rawValue:) ?? .pending`.
func knownStatus(s string) string {
	switch s {
	case statusPending, statusConfirmed, statusCompleted, statusCancelled, "expired", statusFailed:
		return s
	}
	return statusPending
}

func (h *handlers) listPendingActions(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListPendingActions(c.Request().Context(), assistantdb.ListPendingActionsParams{UserID: s.UserID, Now: h.now()})
	if err != nil {
		return err
	}
	now := h.now()
	out := make([]pendingActionResponse, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		dto, err := h.pendingActionDTO(*r, knownStatus(r.Status), now)
		if err != nil {
			return err
		}
		out = append(out, dto)
	}
	return httpx.OK(c, out)
}

func (h *handlers) cancelAction(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return httpx.NotFound("")
	}
	ctx := c.Request().Context()
	row, err := h.q.GetOwnedPendingAction(ctx, assistantdb.GetOwnedPendingActionParams{ID: id, UserID: s.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if row.Status != statusPending {
		return httpx.Abort(http.StatusConflict, "Action is no longer pending.")
	}
	// The read above can lose to a confirm, so the write is conditional, and the
	// answer comes from what the row says now.
	if err := h.q.CancelPendingActionIfPending(ctx, assistantdb.CancelPendingActionIfPendingParams{
		Now: h.now(), ID: row.ID, UserID: s.UserID,
	}); err != nil {
		return err
	}
	row, err = h.q.GetOwnedPendingAction(ctx, assistantdb.GetOwnedPendingActionParams{ID: id, UserID: s.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if row.Status != statusCancelled {
		return httpx.Abort(http.StatusConflict, "Action is no longer pending.")
	}
	return httpx.NoContent(c)
}
