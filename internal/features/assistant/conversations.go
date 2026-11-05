package assistant

import (
	"errors"
	"net/http"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// conversationTTL is the 30-day expiry every write pushes forward.
const conversationTTL = 30 * 24 * time.Hour

func (h *handlers) listConversations(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rows, err := h.q.ListConversations(ctx, assistantdb.ListConversationsParams{UserID: s.UserID, Now: h.now()})
	if err != nil {
		return err
	}
	now := h.now()
	out := make([]conversationSummaryResponse, 0, len(rows))
	for _, row := range rows {
		title, err := h.pii.DecryptString(row.TitleEncrypted)
		if err != nil {
			return err
		}
		updated := row.UpdatedAt
		if updated == nil {
			updated = row.CreatedAt
		}
		out = append(out, conversationSummaryResponse{
			ID: apijson.FormatUUID(row.ID), Title: title,
			CreatedAt: isoTimestamp(row.CreatedAt, now), UpdatedAt: isoTimestamp(updated, now),
		})
	}
	return httpx.OK(c, out)
}

func (h *handlers) createConversation(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var payload struct {
		Title         *string `json:"title"`
		BindMessaging *bool   `json:"bindMessaging"`
	}
	if err := swiftjson.DecodeRequest(c, &payload); err != nil {
		return err
	}
	title := "New conversation"
	if payload.Title != nil {
		title = graphemePrefix(portfolios.TrimSwift(*payload.Title), 120)
	}
	sealed, err := h.pii.EncryptString(title)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	now := h.now()
	id := uuid.New()
	if err := h.q.InsertConversation(ctx, assistantdb.InsertConversationParams{
		ID: id, UserID: s.UserID, TitleEncrypted: sealed, CreatedAt: &now, ExpiresAt: now.Add(conversationTTL),
	}); err != nil {
		return err
	}
	achievementhooks.Schedule(s.UserID)
	// MessagingConversations.bindLinks: starting fresh in the app starts
	// fresh in every linked chat too, unless the client asks to keep this
	// conversation out of them.
	if payload.BindMessaging == nil || *payload.BindMessaging {
		if err := h.q.BindMessagingLinks(ctx, assistantdb.BindMessagingLinksParams{
			UserID: s.UserID, ConversationID: &id,
		}); err != nil {
			return err
		}
	}
	ts := apijson.FormatTime(now)
	return httpx.Created(c, conversationResponse{
		ID: apijson.FormatUUID(id), Title: title, Messages: []messageResponse{}, CreatedAt: ts, UpdatedAt: ts,
	})
}

// ownedConversation is the controller's helper: an unparseable or foreign
// id is a 404.
func (h *handlers) ownedConversation(c *echo.Context, userID uuid.UUID) (assistantdb.AiConversation, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return assistantdb.AiConversation{}, httpx.NotFound("")
	}
	row, err := h.q.GetOwnedConversation(c.Request().Context(), assistantdb.GetOwnedConversationParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, httpx.NotFound("")
	}
	return row, err
}

func (h *handlers) getConversation(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	row, err := h.ownedConversation(c, s.UserID)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	msgs, err := h.q.ListConversationMessages(ctx, assistantdb.ListConversationMessagesParams{
		ConversationID: row.ID, UserID: s.UserID,
	})
	if err != nil {
		return err
	}
	now := h.now()
	values := make([]messageResponse, 0, len(msgs))
	for _, m := range msgs {
		content, err := h.pii.DecryptString(m.ContentEncrypted)
		if err != nil {
			return err
		}
		role := m.Role
		if role != roleUser && role != roleAssistant && role != "tool" {
			role = roleAssistant
		}
		var origin *string
		if m.Origin != nil && (*m.Origin == originReply || *m.Origin == originProactive) {
			origin = m.Origin
		}
		values = append(values, messageResponse{
			ID: apijson.FormatUUID(m.ID), ConversationID: apijson.FormatUUID(row.ID), Role: role,
			Content: content, CreatedAt: isoTimestamp(m.CreatedAt, now), Origin: origin, SourceLabel: m.SourceLabel,
		})
	}
	title, err := h.pii.DecryptString(row.TitleEncrypted)
	if err != nil {
		return err
	}
	updated := row.UpdatedAt
	if updated == nil {
		updated = row.CreatedAt
	}
	return httpx.OK(c, conversationResponse{
		ID: apijson.FormatUUID(row.ID), Title: title, Messages: values,
		CreatedAt: isoTimestamp(row.CreatedAt, now), UpdatedAt: isoTimestamp(updated, now),
	})
}

func (h *handlers) deleteConversation(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	row, err := h.ownedConversation(c, s.UserID)
	if err != nil {
		return err
	}
	if err := h.q.DeleteConversation(c.Request().Context(), row.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// createMessage appends a user message without a model turn. Its quota
// check hard-codes 5, not AI_FREE_MONTHLY_LIMIT, as Vapor's did.
func (h *handlers) createMessage(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	conv, err := h.ownedConversation(c, s.UserID)
	if err != nil {
		return err
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := swiftjson.DecodeRequest(c, &payload); err != nil {
		return err
	}
	content := portfolios.TrimSwift(payload.Content)
	if content == "" || graphemeLen(content) > maxMessageCharacters {
		return httpx.BadRequest("Message must contain 1 to 12,000 characters.")
	}
	ctx := c.Request().Context()
	billing, err := h.ent.Context(ctx, s.UserID)
	if err != nil {
		return err
	}
	tx, err := h.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	used, err := ai.IncrementUsage(ctx, h.ai.Queries.WithTx(tx), s.UserID, h.now())
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if !billing.IsPro && used > 5 {
		_ = tx.Rollback(ctx)
		return httpx.Abort(http.StatusPaymentRequired, "The free AI preview includes 5 requests per month.")
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	sealed, err := h.pii.EncryptString(content)
	if err != nil {
		return err
	}
	now := h.now()
	msgID := uuid.New()
	tx, err = h.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q.WithTx(tx)
	if err := qtx.InsertMessage(ctx, assistantdb.InsertMessageParams{
		ID: msgID, ConversationID: conv.ID, UserID: s.UserID, Role: roleUser, ContentEncrypted: sealed, CreatedAt: &now,
	}); err != nil {
		return err
	}
	if err := qtx.TouchConversation(ctx, assistantdb.TouchConversationParams{
		ID: conv.ID, ExpiresAt: now.Add(conversationTTL), UpdatedAt: &now,
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return httpx.Created(c, messageResponse{
		ID: apijson.FormatUUID(msgID), ConversationID: apijson.FormatUUID(conv.ID), Role: roleUser,
		Content: content, CreatedAt: apijson.FormatTime(now),
	})
}
