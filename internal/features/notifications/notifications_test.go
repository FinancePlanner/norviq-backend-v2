package notifications_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/notifications"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/notificationsdb"
	"norviq-api/internal/store/pushdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type fakeSender struct {
	mu      sync.Mutex
	sent    []push.Notification
	summary func(devices []push.Device) push.Summary
}

func (f *fakeSender) Send(_ context.Context, n push.Notification, devices []push.Device) push.Summary {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	if f.summary != nil {
		return f.summary(devices)
	}
	return push.Summary{Delivered: len(devices)}
}
func (f *fakeSender) Enabled() bool { return true }

type premiumGate struct{ allowed map[uuid.UUID]bool }

func (p premiumGate) RequirePremium(_ context.Context, feature string, userID uuid.UUID) error {
	if feature != notifications.BillingFeatureTargetAlerts {
		return errors.New("wrong feature " + feature)
	}
	if !p.allowed[userID] {
		return httpx.CodedAbort(http.StatusPaymentRequired, "upgrade_required", "Upgrade required.")
	}
	return nil
}

type env struct {
	*testutil.App
	sender  *fakeSender
	premium premiumGate
}

func newApp(t *testing.T) *env {
	t.Helper()
	e := &env{sender: &fakeSender{}, premium: premiumGate{allowed: map[uuid.UUID]bool{}}}
	f := notifications.Feature{Premium: func(*server.Deps) notifications.PremiumGate { return e.premium }}
	e.App = testutil.NewApp(t, []server.Feature{f}, func(d *server.Deps) {
		push.Install(d, &push.Service{Sender: e.sender, Devices: &push.Devices{Q: pushdb.New(d.DB)}})
	})
	return e
}

func (e *env) user(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	id := e.CreateUser(uuid.NewString() + "@example.com")
	return id, e.Token(id)
}

func expectStatus(t *testing.T, r *testutil.Response, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status %d, want %d: %s", r.Status, want, r.Body)
	}
}

// "APNS device registration upserts the same token idempotently"
func TestDeviceRegistrationUpsertsTokenIdempotently(t *testing.T) {
	app := newApp(t)
	userID, token := app.user(t)

	first := app.Do("PUT", "/v1/notifications/apns/device", token, map[string]any{
		"deviceToken": " ABC123TOKEN ", "platform": "ios", "apnsEnvironment": "development", "authorizationStatus": "authorized",
		"capabilities": []string{" Rich ", "rich", "", "Actions"},
	})
	expectStatus(t, first, 200)
	var a, b map[string]any
	first.JSON(t, &a)
	if a["deviceToken"] != "abc123token" || a["isActive"] != true || fmt.Sprint(a["capabilities"]) != "[actions rich]" {
		t.Fatalf("first: %v", a)
	}
	if id := a["id"].(string); id != strings.ToUpper(id) {
		t.Fatalf("id must be uppercase: %s", id)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", a["lastSeenAt"].(string)); err != nil {
		t.Fatalf("lastSeenAt must carry milliseconds: %v", a["lastSeenAt"])
	}

	second := app.Do("PUT", "/v1/notifications/apns/device", token, map[string]any{
		"device_token": "abc123token", "platform": "ios", "apns_environment": "production", "authorization_status": "provisional",
	})
	expectStatus(t, second, 200)
	second.JSON(t, &b)
	if a["id"] != b["id"] || b["apnsEnvironment"] != "production" || b["authorizationStatus"] != "provisional" {
		t.Fatalf("second: %v", b)
	}
	if fmt.Sprint(b["capabilities"]) != "[]" {
		t.Fatalf("omitted capabilities reset to []: %v", b["capabilities"])
	}

	devices, err := pushdb.New(app.Deps.DB).ActivePushDevices(context.Background(), userID)
	if err != nil || len(devices) != 1 || devices[0].ApnsEnvironment != "production" {
		t.Fatalf("devices %v %v", devices, err)
	}
}

func TestDeviceRegistrationMovesTokenToNewUser(t *testing.T) {
	app := newApp(t)
	_, tokenA := app.user(t)
	userB, tokenB := app.user(t)
	body := map[string]any{"deviceToken": "shared", "platform": "ios", "apnsEnvironment": "development", "authorizationStatus": "authorized"}
	expectStatus(t, app.Do("PUT", "/v1/notifications/apns/device", tokenA, body), 200)
	expectStatus(t, app.Do("PUT", "/v1/notifications/apns/device", tokenB, body), 200)
	devices, _ := pushdb.New(app.Deps.DB).ActivePushDevices(context.Background(), userB)
	if len(devices) != 1 {
		t.Fatalf("token should belong to user B: %v", devices)
	}
}

func TestDeviceRegistrationValidation(t *testing.T) {
	app := newApp(t)
	_, token := app.user(t)
	for name, body := range map[string]map[string]any{
		"blank token":    {"deviceToken": "  ", "platform": "ios", "apnsEnvironment": "development", "authorizationStatus": "authorized"},
		"missing token":  {"platform": "ios", "apnsEnvironment": "development", "authorizationStatus": "authorized"},
		"bad platform":   {"deviceToken": "t", "platform": "android", "apnsEnvironment": "development", "authorizationStatus": "authorized"},
		"bad env":        {"deviceToken": "t", "platform": "ios", "apnsEnvironment": "sandbox", "authorizationStatus": "authorized"},
		"bad status":     {"deviceToken": "t", "platform": "ios", "apnsEnvironment": "development", "authorizationStatus": "yes"},
		"missing status": {"deviceToken": "t", "platform": "ios", "apnsEnvironment": "development"},
	} {
		r := app.Do("PUT", "/v1/notifications/apns/device", token, body)
		if r.Status != 400 {
			t.Errorf("%s: status %d", name, r.Status)
		}
		if name == "blank token" && !strings.Contains(string(r.Body), "deviceToken is required.") {
			t.Errorf("blank token reason: %s", r.Body)
		}
	}
}

// "APNS device deactivate marks token inactive"
func TestDeviceDeactivateMarksTokenInactive(t *testing.T) {
	app := newApp(t)
	userID, token := app.user(t)
	devices := &push.Devices{Q: pushdb.New(app.Deps.DB)}
	if _, err := devices.Upsert(context.Background(), userID, push.Registration{
		DeviceToken: "deactivate-token", Platform: "ios", APNSEnvironment: "development", AuthorizationStatus: "authorized",
	}); err != nil {
		t.Fatal(err)
	}
	r := app.Do("POST", "/v1/notifications/apns/device/deactivate", token, map[string]any{"deviceToken": "DEACTIVATE-TOKEN"})
	expectStatus(t, r, 200)
	if len(r.Body) != 0 {
		t.Fatalf("expected empty body: %s", r.Body)
	}
	active, _ := devices.Active(context.Background(), userID)
	if len(active) != 0 {
		t.Fatalf("still active: %v", active)
	}
	// Unknown tokens are not an error; blank ones are.
	expectStatus(t, app.Do("POST", "/v1/notifications/apns/device/deactivate", token, map[string]any{"deviceToken": "nope"}), 200)
	expectStatus(t, app.Do("POST", "/v1/notifications/apns/device/deactivate", token, map[string]any{"deviceToken": " "}), 400)
}

// "APNS device endpoints require authentication"
func TestEndpointsRequireAuthentication(t *testing.T) {
	app := newApp(t)
	for _, c := range []struct{ method, path string }{
		{"PUT", "/v1/notifications/apns/device"},
		{"POST", "/v1/notifications/apns/device/deactivate"},
		{"GET", "/v1/notifications/earnings/preferences"},
		{"PUT", "/v1/notifications/earnings/preferences"},
		{"GET", "/v1/notifications/inbox"},
		{"POST", "/v1/notifications/inbox/read-all"},
		{"PUT", "/v1/notifications/inbox/" + uuid.NewString() + "/read"},
	} {
		if r := app.Do(c.method, c.path, "", map[string]any{"deviceToken": "x"}); r.Status != 401 {
			t.Errorf("%s %s: %d", c.method, c.path, r.Status)
		}
	}
}

func TestEarningsPreferences(t *testing.T) {
	app := newApp(t)
	userID, token := app.user(t)

	// Free users get billing's error, before the body is even read.
	r := app.Do("GET", "/v1/notifications/earnings/preferences", token, nil)
	expectStatus(t, r, 402)
	expectStatus(t, app.Do("PUT", "/v1/notifications/earnings/preferences", token, []byte("not json")), 402)

	app.premium.allowed[userID] = true
	r = app.Do("GET", "/v1/notifications/earnings/preferences", token, nil)
	expectStatus(t, r, 200)
	if string(r.Body) != `{"enabled":true,"leadDays":[7,1],"scope":"portfolio_and_watchlist"}` {
		t.Fatalf("default: %s", r.Body)
	}
	r = app.Do("PUT", "/v1/notifications/earnings/preferences", token, map[string]any{"enabled": false})
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.Body), `"enabled":false`) {
		t.Fatalf("update: %s", r.Body)
	}
	r = app.Do("GET", "/v1/notifications/earnings/preferences", token, nil)
	if !strings.Contains(string(r.Body), `"enabled":false`) {
		t.Fatalf("persisted: %s", r.Body)
	}
	expectStatus(t, app.Do("PUT", "/v1/notifications/earnings/preferences", token, map[string]any{}), 400)
}

func TestEarningsRoutesStayOnVaporWithoutBilling(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{notifications.Feature{}}, func(d *server.Deps) {
		push.Install(d, &push.Service{Sender: push.Noop{}, Devices: &push.Devices{Q: pushdb.New(d.DB)}})
	})
	id := app.CreateUser("x@example.com")
	if r := app.Do("GET", "/v1/notifications/earnings/preferences", app.Token(id), nil); r.Status != 404 {
		t.Fatalf("unwired route must not be served: %d", r.Status)
	}
}

func insertEvent(t *testing.T, app *env, userID uuid.UUID, id uuid.UUID, kind string, created time.Time, read *time.Time) {
	t.Helper()
	_, err := app.Deps.DB.Exec(context.Background(), `
		INSERT INTO notification_events (id, user_id, kind, deduplication_key, title, body, deep_link, payload, created_at, read_at)
		VALUES ($1, $2, $3, $4, 'Alert', 'Body', NULL, '{"k":"v"}', $5, $6)`,
		id, userID, kind, id.String(), created, read)
	if err != nil {
		t.Fatal(err)
	}
}

type page struct {
	Items []struct {
		ID, Kind, Title, Body, CreatedAt string
		ReadAt                           *string
		Payload                          map[string]string
	}
	UnreadCount int
	NextCursor  *string
}

// "Notification inbox cursor is stable when timestamps tie"
func TestInboxCursorStableWhenTimestampsTie(t *testing.T) {
	app := newApp(t)
	userID, token := app.user(t)
	ts := time.Unix(1_750_000_000, 0)
	for i := 1; i <= 3; i++ {
		insertEvent(t, app, userID, uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i)), "budget", ts, nil)
	}
	var first, second page
	r := app.Do("GET", "/v1/notifications/inbox?limit=2", token, nil)
	expectStatus(t, r, 200)
	r.JSON(t, &first)
	if len(first.Items) != 2 || first.NextCursor == nil || first.UnreadCount != 3 {
		t.Fatalf("first page: %s", r.Body)
	}
	if first.Items[0].ID != "00000000-0000-0000-0000-000000000003" || first.Items[0].CreatedAt != "2025-06-15T15:06:40Z" {
		t.Fatalf("ordering/format: %s", r.Body)
	}
	r = app.Do("GET", "/v1/notifications/inbox?limit=2&cursor="+url.QueryEscape(*first.NextCursor), token, nil)
	expectStatus(t, r, 200)
	r.JSON(t, &second)
	if len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].ID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("second page: %s", r.Body)
	}
}

func TestInboxFiltersAndErrors(t *testing.T) {
	app := newApp(t)
	userID, token := app.user(t)
	other, _ := app.user(t)
	now := time.Now().Truncate(time.Second)
	read := now
	insertEvent(t, app, userID, uuid.New(), "budget", now.Add(-time.Hour), &read)
	insertEvent(t, app, userID, uuid.New(), "tax", now.Add(-time.Minute), nil)
	insertEvent(t, app, userID, uuid.New(), "not_a_kind", now, nil) // skipped, still counted unread
	insertEvent(t, app, other, uuid.New(), "tax", now, nil)

	var p page
	app.Do("GET", "/v1/notifications/inbox", token, nil).JSON(t, &p)
	if len(p.Items) != 2 || p.UnreadCount != 2 || p.Items[0].Payload["k"] != "v" {
		t.Fatalf("all: %+v", p)
	}
	for _, q := range []string{"unread=true", "unread", "unread=1", "unread=ON"} {
		p = page{}
		app.Do("GET", "/v1/notifications/inbox?"+q, token, nil).JSON(t, &p)
		if len(p.Items) != 1 || p.Items[0].Kind != "tax" {
			t.Fatalf("%s: %+v", q, p)
		}
	}
	p = page{}
	app.Do("GET", "/v1/notifications/inbox?unread=garbage&limit=abc&kind=budget", token, nil).JSON(t, &p)
	if len(p.Items) != 1 || p.Items[0].ReadAt == nil {
		t.Fatalf("malformed unread/limit fall back to defaults: %+v", p)
	}
	r := app.Do("GET", "/v1/notifications/inbox?kind=nope", token, nil)
	expectStatus(t, r, 400)
	if !strings.Contains(string(r.Body), "Invalid notification kind.") {
		t.Fatalf("%s", r.Body)
	}
	for _, c := range []string{"", "%20", "garbage", "djF8MXwy"} {
		r = app.Do("GET", "/v1/notifications/inbox?cursor="+c, token, nil)
		if r.Status != 400 || !strings.Contains(string(r.Body), "Invalid notification cursor.") {
			t.Fatalf("cursor %q: %d %s", c, r.Status, r.Body)
		}
	}
	// Legacy timestamp cursor.
	p = page{}
	r = app.Do("GET", "/v1/notifications/inbox?cursor="+url.QueryEscape(now.Add(-30*time.Second).UTC().Format("2006-01-02T15:04:05.000Z")), token, nil)
	expectStatus(t, r, 200)
	r.JSON(t, &p)
	if len(p.Items) != 2 {
		t.Fatalf("legacy cursor: %s", r.Body)
	}
}

func TestInboxReadState(t *testing.T) {
	app := newApp(t)
	userID, token := app.user(t)
	other, otherToken := app.user(t)
	id := uuid.New()
	insertEvent(t, app, userID, id, "budget", time.Now(), nil)
	insertEvent(t, app, other, uuid.New(), "budget", time.Now(), nil)

	path := "/v1/notifications/inbox/" + id.String() + "/read"
	expectStatus(t, app.Do("PUT", path, otherToken, map[string]any{"read": true}), 404)
	expectStatus(t, app.Do("PUT", "/v1/notifications/inbox/not-a-uuid/read", token, map[string]any{"read": true}), 404)
	// The lookup runs before the body is decoded.
	expectStatus(t, app.Do("PUT", "/v1/notifications/inbox/"+uuid.NewString()+"/read", token, []byte("{")), 404)
	expectStatus(t, app.Do("PUT", path, token, map[string]any{}), 400)

	var item struct {
		ID     string
		ReadAt *string
	}
	r := app.Do("PUT", path, token, map[string]any{"read": true})
	expectStatus(t, r, 200)
	r.JSON(t, &item)
	if item.ReadAt == nil || item.ID != strings.ToUpper(id.String()) {
		t.Fatalf("read: %s", r.Body)
	}
	item.ReadAt = nil
	r = app.Do("PUT", path, token, map[string]any{"read": false})
	r.JSON(t, &item)
	if item.ReadAt != nil || strings.Contains(string(r.Body), "readAt") {
		t.Fatalf("unread must omit readAt: %s", r.Body)
	}

	r = app.Do("POST", "/v1/notifications/inbox/read-all", token, nil)
	expectStatus(t, r, 204)
	var p page
	app.Do("GET", "/v1/notifications/inbox", token, nil).JSON(t, &p)
	if p.UnreadCount != 0 {
		t.Fatalf("read-all: %+v", p)
	}
	app.Do("GET", "/v1/notifications/inbox", otherToken, nil).JSON(t, &p)
	if p.UnreadCount != 1 {
		t.Fatalf("read-all must not touch other users: %+v", p)
	}
}

func TestAPNSReadinessCheckRegistered(t *testing.T) {
	app := newApp(t)
	r := app.Do("GET", "/health/ready", "", nil)
	if !strings.Contains(string(r.Body), `"apns":{"status":"skipped","message":"APNS is not configured; push delivery is disabled."}`) {
		t.Fatalf("%s", r.Body)
	}
}

func TestPublisherDedupesAndClaimsDeliveries(t *testing.T) {
	app := newApp(t)
	userID, token := app.user(t)
	ctx := context.Background()
	q := notificationsdb.New(app.Deps.DB)
	svc, _ := push.From(app.Deps)
	pub := inbox.NewPublisher(q, svc, slog.New(slog.DiscardHandler))
	link := "norviq://financial-goals/x"
	msg := inbox.Message{
		UserID: userID, Kind: inbox.KindFinancialGoal, DeduplicationKey: "financial-goal:x:milestone:50",
		Title: "Goal: 50% complete", Body: "b", DeepLink: &link, Payload: map[string]string{"goal_id": "x"},
	}

	// No devices: the apns delivery is terminal "no_devices", nothing sent.
	ev, err := pub.PublishAndPush(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	d, err := q.NotificationDeliveryByEventChannel(ctx, notificationsdb.NotificationDeliveryByEventChannelParams{EventID: ev.ID, Channel: "apns"})
	if err != nil || d.Status != "no_devices" || d.AttemptCount != 1 {
		t.Fatalf("delivery %+v %v", d, err)
	}
	if len(app.sender.sent) != 0 {
		t.Fatal("nothing should be sent without devices")
	}

	// Same key again: same event, the finished claim is not retried.
	expectStatus(t, app.Do("PUT", "/v1/notifications/apns/device", token, map[string]any{
		"deviceToken": "abc", "platform": "ios", "apnsEnvironment": "development", "authorizationStatus": "authorized",
	}), 200)
	again, err := pub.PublishAndPush(ctx, msg)
	if err != nil || again.ID != ev.ID || len(app.sender.sent) != 0 {
		t.Fatalf("dedupe: %v %v sent=%d", again.ID, err, len(app.sender.sent))
	}

	// A failed delivery is retried on the next publish.
	msg.DeduplicationKey = "financial-goal:x:milestone:75"
	app.sender.summary = func(devs []push.Device) push.Summary { return push.Summary{Failed: len(devs)} }
	ev2, _ := pub.PublishAndPush(ctx, msg)
	d, _ = q.NotificationDeliveryByEventChannel(ctx, notificationsdb.NotificationDeliveryByEventChannelParams{EventID: ev2.ID, Channel: "apns"})
	if d.Status != "failed" || d.LastError == nil || *d.LastError != "Failed deliveries: 1" {
		t.Fatalf("failed delivery %+v", d)
	}
	app.sender.summary = nil
	_, _ = pub.PublishAndPush(ctx, msg)
	d, _ = q.NotificationDeliveryByEventChannel(ctx, notificationsdb.NotificationDeliveryByEventChannelParams{EventID: ev2.ID, Channel: "apns"})
	if d.Status != "delivered" || d.AttemptCount != 2 || d.LastError != nil {
		t.Fatalf("retried delivery %+v", d)
	}
	sent := app.sender.sent[len(app.sender.sent)-1]
	if sent.Category != "WEALTH_AUTOMATION" || sent.Payload["type"] != "financial_goal" || sent.Payload["deepLink"] != link {
		t.Fatalf("automation alert %+v", sent)
	}

	// The event shows up in the inbox.
	var p page
	app.Do("GET", "/v1/notifications/inbox?kind=financial_goal", token, nil).JSON(t, &p)
	if len(p.Items) != 2 || p.Items[0].Payload["goal_id"] != "x" {
		t.Fatalf("inbox %+v", p)
	}
}
