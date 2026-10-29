package integrations

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

var fixedNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func newApp(t *testing.T, cfg *TelegramConfig) *testutil.App {
	t.Helper()
	return testutil.NewApp(t, []server.Feature{Feature{Telegram: cfg, Disabled: cfg == nil, Now: func() time.Time { return fixedNow }}})
}

func TestResolve(t *testing.T) {
	if Resolve(Settings{}, false) != nil {
		t.Fatal("no token must disable")
	}
	if Resolve(Settings{BotToken: "tok"}, true) != nil {
		t.Fatal("production without webhook secret must disable")
	}
	got := Resolve(Settings{BotToken: " tok\n", BotUsername: " @NorviqBot "}, false)
	if got == nil || got.BotToken != "tok" || got.BotUsername != "NorviqBot" {
		t.Fatalf("resolve %+v", got)
	}
	if Resolve(Settings{BotToken: "tok", WebhookSecret: "s"}, true) == nil {
		t.Fatal("production with secret must enable")
	}
}

func TestStatusUnavailable(t *testing.T) {
	app := newApp(t, nil)
	token := app.Token(app.CreateUser("tg-off@example.com"))
	r := app.Do(http.MethodGet, "/v1/integrations/telegram", token, nil)
	if r.Status != http.StatusOK || string(r.Body) != `{"available":false,"connected":false,"botUsername":""}` {
		t.Fatalf("status %d %s", r.Status, r.Body)
	}
	r = app.Do(http.MethodPost, "/v1/integrations/telegram/code", token, nil)
	if r.Status != http.StatusServiceUnavailable || !strings.Contains(string(r.Body), "Telegram is not configured.") {
		t.Fatalf("code %d %s", r.Status, r.Body)
	}
	if r := app.Do(http.MethodGet, "/v1/integrations/telegram", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous %d", r.Status)
	}
}

func TestCodeStatusDisconnect(t *testing.T) {
	app := newApp(t, &TelegramConfig{BotToken: "tok", BotUsername: "NorviqBot"})
	userID := app.CreateUser("tg-on@example.com")
	token := app.Token(userID)
	ctx := context.Background()

	r := app.Do(http.MethodGet, "/v1/integrations/telegram", token, nil)
	if string(r.Body) != `{"available":true,"connected":false,"botUsername":"NorviqBot"}` {
		t.Fatalf("status %s", r.Body)
	}

	var first, second map[string]string
	app.Do(http.MethodPost, "/v1/integrations/telegram/code", token, nil).JSON(t, &first)
	r = app.Do(http.MethodPost, "/v1/integrations/telegram/code", token, nil)
	r.JSON(t, &second)
	code := second["code"]
	if r.Status != http.StatusOK || len(code) != codeLength || strings.Trim(code, codeAlphabet) != "" {
		t.Fatalf("code %d %s", r.Status, r.Body)
	}
	if second["expiresAt"] != "2026-10-29T12:15:00Z" || second["deepLink"] != "https://t.me/NorviqBot?start="+code {
		t.Fatalf("code body %v", second)
	}
	var n int
	var stored []byte
	_ = app.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM messaging_link_codes WHERE user_id = $1`, userID).Scan(&n)
	_ = app.Deps.DB.QueryRow(ctx, `SELECT code_hash FROM messaging_link_codes WHERE user_id = $1`, userID).Scan(&stored)
	if n != 1 || !bytes.Equal(stored, hashCode(strings.ToLower(code))) {
		t.Fatalf("codes %d, hash match %v", n, bytes.Equal(stored, hashCode(code)))
	}

	if _, err := app.Deps.DB.Exec(ctx,
		`INSERT INTO messaging_links (id, user_id, platform, external_id, created_at, last_seen_at) VALUES ($1, $2, 'telegram', '42', $3, $3)`,
		uuid.New(), userID, fixedNow); err != nil {
		t.Fatal(err)
	}
	r = app.Do(http.MethodGet, "/v1/integrations/telegram", token, nil)
	if want := `{"available":true,"connected":true,"botUsername":"NorviqBot","lastSeenAt":"2026-10-29T12:00:00Z","connectedAt":"2026-10-29T12:00:00Z"}`; string(r.Body) != want {
		t.Fatalf("connected %s", r.Body)
	}
	if r := app.Do(http.MethodDelete, "/v1/integrations/telegram", token, nil); r.Status != http.StatusNoContent {
		t.Fatalf("disconnect %d %s", r.Status, r.Body)
	}
	r = app.Do(http.MethodGet, "/v1/integrations/telegram", token, nil)
	if !strings.Contains(string(r.Body), `"connected":false`) {
		t.Fatalf("after disconnect %s", r.Body)
	}
}

func TestPreferences(t *testing.T) {
	app := newApp(t, nil)
	token := app.Token(app.CreateUser("tg-prefs@example.com"))

	var list preferencesResponse
	r := app.Do(http.MethodGet, "/v1/integrations/telegram/preferences", token, nil)
	r.JSON(t, &list)
	if r.Status != http.StatusOK || len(list.Preferences) != 8 || list.Preferences[0].Kind != "price_target" ||
		list.Preferences[0].Enabled || list.Preferences[0].Timezone != "UTC" {
		t.Fatalf("list %d %s", r.Status, r.Body)
	}

	for _, c := range []struct {
		body any
		want string
	}{
		{map[string]any{"kind": "nope", "enabled": true}, "Unknown notification kind."},
		{map[string]any{"kind": "budget", "enabled": true, "quietHoursStart": 22}, "Quiet hours need both a start and an end, or neither."},
		{map[string]any{"kind": "budget", "enabled": true, "quietHoursStart": 22, "quietHoursEnd": 24}, "Quiet hours must be hours between 0 and 23."},
		{map[string]any{"kind": "budget", "enabled": true, "timezone": "Mars/Base"}, "Unknown timezone identifier."},
		{map[string]any{"enabled": true}, "No such key 'kind'"},
	} {
		r := app.Do(http.MethodPut, "/v1/integrations/telegram/preferences", token, c.body)
		if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), c.want) {
			t.Errorf("%v: %d %s", c.body, r.Status, r.Body)
		}
	}

	put := func(body any) preferenceItem {
		t.Helper()
		var out preferencesResponse
		r := app.Do(http.MethodPut, "/v1/integrations/telegram/preferences", token, body)
		r.JSON(t, &out)
		if r.Status != http.StatusOK {
			t.Fatalf("put %d %s", r.Status, r.Body)
		}
		for _, p := range out.Preferences {
			if p.Kind == "budget" {
				return p
			}
		}
		t.Fatal("budget missing")
		return preferenceItem{}
	}
	got := put(map[string]any{"kind": "budget", "enabled": true, "quiet_hours_start": 22, "quiet_hours_end": 7, "timezone": "Europe/Lisbon"})
	if !got.Enabled || got.QuietHoursStart == nil || *got.QuietHoursStart != 22 || *got.QuietHoursEnd != 7 || got.Timezone != "Europe/Lisbon" {
		t.Fatalf("insert %+v", got)
	}
	got = put(map[string]any{"kind": "budget", "enabled": false})
	if got.Enabled || got.QuietHoursStart != nil || got.Timezone != "UTC" {
		t.Fatalf("update %+v", got)
	}
	var n int
	_ = app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM messaging_preferences`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows %d", n)
	}
}
