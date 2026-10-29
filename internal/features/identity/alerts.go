package identity

import (
	"context"
	"log/slog"
	"time"

	"norviq-api/internal/platform/discord"
)

// Alerter posts an operator notice. Vapor fires these from a detached Task,
// so Notify never blocks or fails the request.
type Alerter interface {
	Notify(message string)
}

// discordAlerter sends through the shared discord platform package.
type discordAlerter struct {
	sender discord.Sender
	log    *slog.Logger
}

func (a discordAlerter) Notify(message string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.sender.Send(ctx, message); err != nil {
			a.log.Warn("Failed to send Discord webhook", "error", err)
		}
	}()
}
