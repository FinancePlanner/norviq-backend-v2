package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Alerter posts an operator notice. Vapor sends these through
// DiscordWebhookService; the notifications port may own a shared one, so
// identity depends only on this interface.
type Alerter interface {
	// Notify is fire-and-forget: it never blocks or fails the request.
	Notify(message string)
}

// discordAlerter is DefaultDiscordWebhookService.
type discordAlerter struct {
	url    string
	client *http.Client
	log    *slog.Logger
}

type discordPayload struct {
	Content         string `json:"content"`
	AllowedMentions struct {
		Parse []string `json:"parse"`
	} `json:"allowed_mentions"`
}

func (a discordAlerter) Notify(message string) {
	if a.url == "" {
		a.log.Debug("DISCORD_WEBHOOK_URL not set, skipping Discord notification.")
		return
	}
	p := discordPayload{Content: message}
	p.AllowedMentions.Parse = []string{}
	body, err := json.Marshal(p)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.client.Do(req)
		if err != nil {
			a.log.Warn("Failed to send Discord webhook", "error", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			a.log.Warn("Failed to send Discord webhook", "status", resp.StatusCode)
		}
	}()
}
