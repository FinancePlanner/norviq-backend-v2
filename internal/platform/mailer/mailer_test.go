package mailer

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norviq-api/internal/httpx"

	"github.com/google/uuid"
)

func TestResendSendsPayload(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" {
			t.Errorf("path %s", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"em_1"}`))
	}))
	defer srv.Close()
	var logs bytes.Buffer
	m := New(Settings{APIKey: "key", FromEmail: "Norviq <no-reply@norviq.org>", BaseURL: srv.URL + "/"}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := m.Send(t.Context(), Message{To: "a@b.com", Subject: "S", Body: "B", Purpose: "mfa_login"}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer key" || got["from"] != "Norviq <no-reply@norviq.org>" || got["subject"] != "S" || got["text"] != "B" {
		t.Errorf("payload %v auth %q", got, auth)
	}
	if to, _ := got["to"].([]any); len(to) != 1 || to[0] != "a@b.com" {
		t.Errorf("to %v", got["to"])
	}
	if strings.Contains(logs.String(), "a@b.com") || !strings.Contains(logs.String(), "em_1") {
		t.Errorf("logs %s", logs.String())
	}
}

func TestResendSendsHTMLAndHeaders(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"em_2"}`))
	}))
	defer srv.Close()
	m := New(Settings{APIKey: "key", FromEmail: "from@norviq.org", BaseURL: srv.URL}, slog.New(slog.DiscardHandler))
	html := "<p>Hi</p>"
	if err := m.Send(t.Context(), Message{
		To: "a@b.com", Subject: "S", Body: "B", HTMLBody: &html,
		Headers: map[string]string{"List-Unsubscribe-Post": "List-Unsubscribe=One-Click"},
	}); err != nil {
		t.Fatal(err)
	}
	h, _ := got["headers"].(map[string]any)
	if got["html"] != html || h["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Errorf("payload %v", got)
	}
	// Without them, both keys are absent, as Vapor's encoder left them out.
	got = nil
	if err := m.Send(t.Context(), Message{To: "a@b.com", Subject: "S", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["html"]; ok {
		t.Errorf("html present: %v", got)
	}
	if _, ok := got["headers"]; ok {
		t.Errorf("headers present: %v", got)
	}
}

func TestResendFailureIs503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	m := Resend{APIKey: "k", FromEmail: "f@x.io", BaseURL: srv.URL, Logger: slog.New(slog.DiscardHandler)}
	err := m.Send(t.Context(), Message{To: "a@b.com"})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 503 || he.Reason != "Unable to send email right now." {
		t.Errorf("err %v", err)
	}
}

// Ported from AuthTests "Console mailer logs MFA metadata without leaking code".
func TestConsoleNeverLogsBody(t *testing.T) {
	var logs bytes.Buffer
	id := uuid.MustParse("6b1c2d1e-0000-4000-8000-000000000001")
	m := New(Settings{}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := m.Send(t.Context(), Message{To: "user@example.com", Subject: "Your Norviq code is 123456", Body: "code 123456", Purpose: "mfa_login", ChallengeID: &id}); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{"recipient=<redacted>@<redacted>.com", "purpose=mfa_login", "challenge_id=6B1C2D1E-0000-4000-8000-000000000001"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "123456") || strings.Contains(out, "user@") {
		t.Errorf("leaked: %s", out)
	}
	if RedactEmail("nope") != "<redacted>" {
		t.Error("redact without @")
	}
}
