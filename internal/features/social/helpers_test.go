package social_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/social"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/pushdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// fakeDiscord records messages.
type fakeDiscord struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeDiscord) Send(_ context.Context, m string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
	return nil
}

// fakePush records notifications.
type fakePush struct {
	mu   sync.Mutex
	sent []push.Notification
}

func (f *fakePush) Send(_ context.Context, n push.Notification, devices []push.Device) push.Summary {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	return push.Summary{Delivered: len(devices)}
}

func (*fakePush) Enabled() bool { return true }

type env struct {
	t       *testing.T
	app     *testutil.App
	discord *fakeDiscord
	push    *fakePush
}

// setup turns social on (as SocialRouteTests.withApp did) and starts the
// feature. flags are extra KEY=VALUE env pairs.
func setup(t *testing.T, f social.Feature, flags ...string) *env {
	t.Helper()
	t.Setenv("SOCIAL_ENABLED", "1")
	t.Setenv("SOCIAL_CONTACT_PEPPER", "test-pepper")
	t.Setenv("SOCIAL_INVITE_BASE_URL", "https://norviq.test")
	for i := 0; i+1 < len(flags); i += 2 {
		t.Setenv(flags[i], flags[i+1])
	}
	e := &env{t: t, discord: &fakeDiscord{}, push: &fakePush{}}
	if f.Discord == nil {
		f.Discord = e.discord
	}
	e.app = testutil.NewApp(t, []server.Feature{f}, func(d *server.Deps) {
		push.Install(d, &push.Service{Sender: e.push, Devices: &push.Devices{Q: pushdb.New(d.DB)}})
	})
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) scalar(sql string, out any, args ...any) {
	e.t.Helper()
	if err := e.app.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// user creates a user with username name and email name@example.com.
func (e *env) user(name string) uuid.UUID {
	e.t.Helper()
	id := e.app.CreateUser(name + "@example.com")
	e.exec(`UPDATE users SET username = $1 WHERE id = $2`, name, id)
	return id
}

type resp struct {
	Status int
	Body   []byte
}

func (r resp) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r resp) reason(t *testing.T) string {
	t.Helper()
	var env struct {
		Reason string `json:"reason"`
	}
	r.decode(t, &env)
	return env.Reason
}

// call sends a request as user (uuid.Nil: anonymous). headers are pairs.
func (e *env) call(method, path string, as uuid.UUID, body any, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.app.Server.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if as != uuid.Nil {
		req.Header.Set("Authorization", "Bearer "+e.app.Token(as))
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := e.app.Server.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return resp{Status: res.StatusCode, Body: data}
}

// get calls GET and decodes a 200.
func (e *env) get(path string, as uuid.UUID, v any, headers ...string) {
	e.t.Helper()
	r := e.call(http.MethodGet, path, as, nil, headers...)
	if r.Status != http.StatusOK {
		e.t.Fatalf("GET %s: %d %s", path, r.Status, r.Body)
	}
	r.decode(e.t, v)
}

func (e *env) status(method, path string, as uuid.UUID, body any) int {
	e.t.Helper()
	return e.call(method, path, as, body).Status
}

func (e *env) friendIDs(of uuid.UUID) []string {
	e.t.Helper()
	var res struct {
		Friends []struct {
			ID string `json:"id"`
		} `json:"friends"`
	}
	e.get("/v1/social/friends", of, &res)
	out := []string{}
	for _, f := range res.Friends {
		out = append(out, f.ID)
	}
	return out
}

func up(id uuid.UUID) string { return apijson.FormatUUID(id) }

func defaultPrivacy() map[string]any {
	return map[string]any{
		"searchVisibility": "everyone", "discoverableByContacts": true, "discoverableByX": true,
		"discoverableByFacebook": true, "showReturnPercent": false, "showStreaks": true, "showXP": true,
		"leaderboardOptIn": true, "showAchievements": true,
	}
}

func (e *env) setPrivacy(as uuid.UUID, change func(map[string]any)) {
	e.t.Helper()
	p := defaultPrivacy()
	change(p)
	if r := e.call(http.MethodPut, "/v1/social/privacy", as, p); r.Status != http.StatusOK {
		e.t.Fatalf("PUT privacy: %d %s", r.Status, r.Body)
	}
}

func (e *env) befriend(a, b uuid.UUID) {
	e.t.Helper()
	if err := social.NewService(e.app.Deps.DB).Befriend(context.Background(), a, b); err != nil {
		e.t.Fatal(err)
	}
}
