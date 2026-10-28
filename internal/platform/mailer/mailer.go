// Package mailer sends transactional email (Services/MailerService.swift):
// Resend when RESEND_API_KEY and RESEND_FROM_EMAIL are set, otherwise a
// console mailer that logs metadata and never the body.
package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
)

// Message is MailMessage.
type Message struct {
	To          string
	Subject     string
	Body        string
	Purpose     string     // "" logs as "general"
	ChallengeID *uuid.UUID // MFA challenge, for log correlation
}

// Mailer delivers a message or returns an error the caller may surface.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// Console logs who a message was for, never its content (codes stay out of logs).
type Console struct{ Logger *slog.Logger }

// Send logs the redacted recipient and purpose.
func (c Console) Send(_ context.Context, m Message) error {
	c.Logger.Info("[mailer] recipient=" + RedactEmail(m.To) + " purpose=" + m.purposeOrGeneral() + " challenge_id=" + m.challengeOrNone())
	return nil
}

// Resend posts to the Resend API (`POST {BaseURL}/emails`).
type Resend struct {
	APIKey    string
	FromEmail string
	BaseURL   string // default https://api.resend.com
	Client    *http.Client
	Logger    *slog.Logger
}

type resendPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
}

// Send delivers the message; any non-2xx answer is a 503 "Unable to send
// email right now.", as in Vapor.
func (r Resend) Send(ctx context.Context, m Message) error {
	body, err := apijson.Marshal(resendPayload{From: r.FromEmail, To: []string{m.To}, Subject: m.Subject, Text: m.Body})
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(r.BaseURL, "/")
	if base == "" {
		base = "https://api.resend.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		r.Logger.Error("resend.mail failed", "status", resp.StatusCode, "recipient", RedactEmail(m.To),
			"purpose", m.purposeOrGeneral(), "challenge_id", m.challengeOrNone())
		return httpx.Abort(http.StatusServiceUnavailable, "Unable to send email right now.")
	}
	id := "unknown"
	var decoded struct {
		ID *string `json:"id"`
	}
	if json.Unmarshal(raw, &decoded) == nil && decoded.ID != nil {
		id = *decoded.ID
	}
	r.Logger.Info("resend.mail sent", "id", id, "recipient", RedactEmail(m.To),
		"purpose", m.purposeOrGeneral(), "challenge_id", m.challengeOrNone())
	return nil
}

// Settings are Vapor's variable names.
type Settings struct {
	APIKey    string
	FromEmail string
	BaseURL   string
}

// SettingsFromEnv reads RESEND_API_KEY, RESEND_FROM_EMAIL and RESEND_BASE_URL.
func SettingsFromEnv() Settings {
	return Settings{
		APIKey:    strings.TrimSpace(os.Getenv("RESEND_API_KEY")),
		FromEmail: strings.TrimSpace(os.Getenv("RESEND_FROM_EMAIL")),
		BaseURL:   strings.TrimSpace(os.Getenv("RESEND_BASE_URL")),
	}
}

// Configured reports whether Resend can be used.
func (s Settings) Configured() bool { return s.APIKey != "" && s.FromEmail != "" }

// New returns Resend when configured, otherwise the console mailer.
func New(s Settings, logger *slog.Logger) Mailer {
	if !s.Configured() {
		return Console{Logger: logger}
	}
	return Resend{APIKey: s.APIKey, FromEmail: s.FromEmail, BaseURL: s.BaseURL, Logger: logger}
}

// RedactEmail keeps only the top-level domain: "<redacted>@<redacted>.com".
func RedactEmail(email string) string {
	_, domain, ok := strings.Cut(email, "@")
	if !ok {
		return "<redacted>"
	}
	tld := "domain"
	if parts := strings.Split(domain, "."); len(parts) > 0 && parts[len(parts)-1] != "" {
		tld = parts[len(parts)-1]
	}
	return "<redacted>@<redacted>." + tld
}

func (m Message) purposeOrGeneral() string {
	if m.Purpose == "" {
		return "general"
	}
	return m.Purpose
}

func (m Message) challengeOrNone() string {
	if m.ChallengeID == nil {
		return "none"
	}
	return apijson.FormatUUID(*m.ChallengeID)
}
