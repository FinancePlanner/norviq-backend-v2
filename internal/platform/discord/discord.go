// Package discord posts ops alerts to the DISCORD_WEBHOOK_URL webhook. It
// ports DefaultDiscordWebhookService.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// Sender posts one message.
type Sender interface {
	Send(ctx context.Context, message string) error
}

// Webhook is the default sender. An empty URL skips sending.
type Webhook struct {
	URL    string
	Client *http.Client
	Logger *slog.Logger
}

// FromEnv reads DISCORD_WEBHOOK_URL.
func FromEnv(logger *slog.Logger) *Webhook {
	return &Webhook{URL: os.Getenv("DISCORD_WEBHOOK_URL"), Client: &http.Client{Timeout: 10 * time.Second}, Logger: logger}
}

type allowedMentions struct {
	Parse []string `json:"parse"`
}

// Payload is DiscordPayload. Messages quote user text, so nothing in them
// may ping @everyone, a role or a person.
type Payload struct {
	Content         string          `json:"content"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

// NewPayload builds the webhook body for message.
func NewPayload(message string) Payload {
	return Payload{Content: message, AllowedMentions: allowedMentions{Parse: []string{}}}
}

func (w *Webhook) Send(ctx context.Context, message string) error {
	if w.URL == "" {
		w.Logger.Debug("DISCORD_WEBHOOK_URL not set, skipping Discord notification.")
		return nil
	}
	body, err := json.Marshal(NewPayload(message))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := w.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		w.Logger.Warn("Failed to send Discord webhook", "status", resp.StatusCode)
	}
	return nil
}
