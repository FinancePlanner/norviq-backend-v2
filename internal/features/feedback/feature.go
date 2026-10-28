// Package feedback ports FeedbackController (POST /v1/feedback).
package feedback

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/discord"
	"norviq-api/internal/server"
	"norviq-api/internal/store/feedbackdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/feedback.
type Feature struct {
	// Discord overrides the DISCORD_WEBHOOK_URL sender (tests).
	Discord discord.Sender
}

func (Feature) Name() string { return "feedback" }

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{q: feedbackdb.New(d.DB), discord: f.Discord, d: d}
	if h.discord == nil {
		h.discord = discord.FromEnv(d.Logger)
	}
	r.V1.POST("/feedback", h.submit, d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeSettingsWrite))
	return nil
}

type handler struct {
	q       feedbackdb.Querier
	discord discord.Sender
	d       *server.Deps
}

type response struct {
	Success bool    `json:"success"`
	Message *string `json:"message,omitempty"`
}

func (h *handler) submit(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body struct {
		Topic   *string `json:"topic"`
		Message *string `json:"message"`
	}
	if err := httpx.Decode(c, &body); err != nil {
		return err
	}
	if body.Topic == nil {
		return httpx.BadRequest("No such key 'topic' at path ''.")
	}
	if body.Message == nil {
		return httpx.BadRequest("No such key 'message' at path ''.")
	}
	topic := strings.TrimSpace(*body.Topic)
	message := strings.TrimSpace(*body.Message)
	if n := utf8.RuneCountInString(topic); n == 0 || n > 80 {
		return httpx.BadRequest("Feedback topic must be between 1 and 80 characters.")
	}
	if n := utf8.RuneCountInString(message); n == 0 || n > 5000 {
		return httpx.BadRequest("Feedback message must be between 1 and 5000 characters.")
	}

	id := uuid.New()
	now := time.Now()
	if err := h.q.InsertFeedback(c.Request().Context(), feedbackdb.InsertFeedbackParams{
		ID: id, Topic: topic, Message: message, UserID: s.UserID, CreatedAt: &now,
	}); err != nil {
		return err
	}

	// Fire and forget, as the Swift Task was. No email or user id in the
	// alert: Discord is an ops channel. The feedback id finds the row.
	text := "📝 New feedback " + apijson.FormatUUID(id) + " (Topic: " + topic + "):\n```" + message + "```"
	go func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := h.discord.Send(ctx, text); err != nil {
			h.d.Logger.Debug("Failed to send discord feedback notification", "error", err)
		}
	}(context.WithoutCancel(c.Request().Context()))

	msg := "Feedback submitted successfully."
	return httpx.OK(c, response{Success: true, Message: &msg})
}
