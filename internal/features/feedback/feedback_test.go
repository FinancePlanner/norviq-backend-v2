package feedback_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/feedback"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"
)

type fakeDiscord struct{ got chan string }

func (f fakeDiscord) Send(_ context.Context, m string) error { f.got <- m; return nil }

func newApp(t *testing.T) (*testutil.App, fakeDiscord) {
	t.Helper()
	d := fakeDiscord{got: make(chan string, 4)}
	return testutil.NewApp(t, []server.Feature{feedback.Feature{Discord: d}}), d
}

// "Authenticated users can submit feedback"
func TestFeedbackSubmissionSucceeds(t *testing.T) {
	app, discord := newApp(t)
	user := app.CreateUser("f@example.com")
	r := app.Do("POST", "/v1/feedback", app.Token(user), map[string]any{
		"topic": "  General Feedback ", "message": "The settings screen is clearer.\n",
	})
	if r.Status != 200 || string(r.Body) != `{"success":true,"message":"Feedback submitted successfully."}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var topic, message string
	var id string
	if err := app.Deps.DB.QueryRow(context.Background(),
		`SELECT id::text, topic, message FROM feedbacks WHERE user_id = $1`, user).Scan(&id, &topic, &message); err != nil {
		t.Fatal(err)
	}
	if topic != "General Feedback" || message != "The settings screen is clearer." {
		t.Fatalf("stored %q %q", topic, message)
	}
	select {
	case m := <-discord.got:
		want := "📝 New feedback " + strings.ToUpper(id) + " (Topic: General Feedback):\n```The settings screen is clearer.```"
		if m != want {
			t.Fatalf("discord message %q, want %q", m, want)
		}
		if strings.Contains(m, "f@example.com") || strings.Contains(m, user.String()) {
			t.Fatal("discord alert must not carry account identifiers")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no discord alert")
	}
}

// "Feedback requires authentication"
func TestFeedbackRequiresAuthentication(t *testing.T) {
	app, _ := newApp(t)
	if r := app.Do("POST", "/v1/feedback", "", map[string]any{"topic": "t", "message": "m"}); r.Status != 401 {
		t.Fatalf("%d", r.Status)
	}
}

// "Feedback rejects empty fields", plus the length limits.
func TestFeedbackValidation(t *testing.T) {
	app, _ := newApp(t)
	token := app.Token(app.CreateUser("v@example.com"))
	for _, c := range []struct {
		body   map[string]any
		reason string
	}{
		{map[string]any{"topic": " ", "message": " "}, "Feedback topic must be between 1 and 80 characters."},
		{map[string]any{"topic": strings.Repeat("é", 81), "message": "m"}, "Feedback topic must be between 1 and 80 characters."},
		{map[string]any{"topic": "t", "message": "\n"}, "Feedback message must be between 1 and 5000 characters."},
		{map[string]any{"topic": "t", "message": strings.Repeat("x", 5001)}, "Feedback message must be between 1 and 5000 characters."},
		{map[string]any{"message": "m"}, ""},
		{map[string]any{"topic": "t"}, ""},
	} {
		r := app.Do("POST", "/v1/feedback", token, c.body)
		if r.Status != 400 || !strings.Contains(string(r.Body), c.reason) {
			t.Errorf("%v: %d %s", c.body, r.Status, r.Body)
		}
	}
	if r := app.Do("POST", "/v1/feedback", token, map[string]any{"topic": strings.Repeat("é", 80), "message": strings.Repeat("x", 5000)}); r.Status != 200 {
		t.Fatalf("limits are inclusive: %d", r.Status)
	}
}
