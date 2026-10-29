package pilots

import (
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/auth"
	"norviq-api/internal/server"
	"norviq-api/internal/store/pilotsdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type envelope struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func newApp(t *testing.T) *testutil.App {
	t.Helper()
	return testutil.NewApp(t, []server.Feature{Feature{Mirror: func(*server.Deps) *Mirror { return flatMirror() }}})
}

func pat(t *testing.T, app *testutil.App, user uuid.UUID, scopes ...string) string {
	t.Helper()
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	mustExec(t, app.Deps.DB, `INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		VALUES ($1, $2, 'pilot-scope-test', $3, $4, now() + interval '1 hour', now())`, uuid.New(), user, auth.SHA256Hex(token), scopes)
	return token
}

func expectError(t *testing.T, r *testutil.Response, status int, reason string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
	var e envelope
	r.JSON(t, &e)
	if reason != "" && e.Reason != reason {
		t.Fatalf("reason %q, want %q", e.Reason, reason)
	}
}

func pelosiID(t *testing.T, app *testutil.App) uuid.UUID {
	t.Helper()
	p, err := pilotsdb.New(app.Deps.DB).ActivePilotBySlug(bg, "nancy-pelosi")
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestFlagOff(t *testing.T) {
	t.Setenv("PILOTS_ENABLED", "")
	app := newApp(t)
	user := app.CreateUser("pilot-off@example.com")
	tok := app.Token(user)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/pilots"},
		{http.MethodGet, "/v1/pilots/nancy-pelosi"},
		{http.MethodGet, "/v1/pilot-follows"},
		{http.MethodGet, "/v1/pilot-follows/x"},
		{http.MethodGet, "/v1/pilot-follows/x/events"},
		{http.MethodGet, "/v1/pilot-follows/x/snapshots"},
		{http.MethodPost, "/v1/pilot-follows"},
		{http.MethodPatch, "/v1/pilot-follows/x"},
		{http.MethodDelete, "/v1/pilot-follows/x"},
	} {
		expectError(t, app.Do(c.method, c.path, tok, nil), http.StatusNotFound, "Not Found")
		// The guard runs before the flag check.
		expectError(t, app.Do(c.method, c.path, "", nil), http.StatusUnauthorized, "")
	}
}

func TestLifecycle(t *testing.T) {
	t.Setenv("PILOTS_ENABLED", "true")
	app := newApp(t)
	user := app.CreateUser("pilot-life@example.com")
	tok := app.Token(user)
	addVersion(t, app.Deps.DB, pelosiID(t, app), 1, map[string]float64{"NVDA": 1})

	var pilots []PilotSummary
	r := app.Do(http.MethodGet, "/v1/pilots", tok, nil)
	r.JSON(t, &pilots)
	found := false
	for _, p := range pilots {
		found = found || (p.Slug == "nancy-pelosi" && p.HoldingsCount == 1 && p.UpdatedAt != nil && *p.UpdatedAt == "2026-10-01T00:00:00Z")
	}
	if r.Status != http.StatusOK || !found || len(pilots) < 10 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if strings.Contains(string(r.Body), `"updatedAt":null`) || strings.Contains(string(r.Body), `"chamber":null`) {
		t.Fatal("nil optionals must be omitted")
	}

	var detail PilotDetail
	r = app.Do(http.MethodGet, "/v1/pilots/nancy-pelosi", tok, nil)
	r.JSON(t, &detail)
	if len(detail.Weights) != 1 || detail.Weights[0] != (PilotWeight{"NVDA", 1}) || !strings.Contains(detail.LagNote, "45 days") || detail.RecentDisclosures == nil {
		t.Fatal(string(r.Body))
	}
	expectError(t, app.Do(http.MethodGet, "/v1/pilots/nobody", tok, nil), http.StatusNotFound, "Pilot not found.")

	r = app.Do(http.MethodPost, "/v1/pilot-follows", tok, map[string]any{"pilotSlug": "nancy-pelosi", "targetKind": "watchlist"})
	if r.Status != http.StatusCreated {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var created PilotFollowResponse
	r.JSON(t, &created)
	if created.AppliedVersion != 1 || created.WatchlistListID == nil || created.PortfolioListID != nil || created.StartingCapital != nil ||
		created.ID != strings.ToUpper(created.ID) || created.Status != "active" || created.Currency != "USD" {
		t.Fatal(string(r.Body))
	}

	var events []PilotFollowEventResponse
	app.Do(http.MethodGet, "/v1/pilot-follows/"+created.ID+"/events", tok, nil).JSON(t, &events)
	if len(events) != 1 || events[0].Kind != "watch_added" || events[0].Quantity != nil || *events[0].Note != "Nancy Pelosi bought recently" {
		t.Fatal(events)
	}
	var snaps []PilotFollowSnapshotResponse
	r = app.Do(http.MethodGet, "/v1/pilot-follows/"+created.ID+"/snapshots", tok, nil)
	r.JSON(t, &snaps)
	if string(r.Body) != "[]" {
		t.Fatal(string(r.Body))
	}

	var list []PilotFollowResponse
	app.Do(http.MethodGet, "/v1/pilot-follows", tok, nil).JSON(t, &list)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatal(list)
	}

	var updated PilotFollowResponse
	r = app.Do(http.MethodPatch, "/v1/pilot-follows/"+created.ID, tok, map[string]any{"status": "paused"})
	r.JSON(t, &updated)
	if updated.Status != "paused" {
		t.Fatal(string(r.Body))
	}
	// Unchanged status: still 200.
	if r := app.Do(http.MethodPatch, "/v1/pilot-follows/"+strings.ToLower(created.ID), tok, map[string]any{"status": "paused"}); r.Status != 200 {
		t.Fatal(r.Status)
	}
	expectError(t, app.Do(http.MethodPatch, "/v1/pilot-follows/"+created.ID, tok, map[string]any{"status": "gone"}), http.StatusBadRequest,
		"Cannot initialize PilotFollowStatus from invalid String value gone")
	expectError(t, app.Do(http.MethodPatch, "/v1/pilot-follows/"+created.ID, tok, map[string]any{}), http.StatusBadRequest, "")

	if r := app.Do(http.MethodDelete, "/v1/pilot-follows/"+created.ID, tok, nil); r.Status != http.StatusNoContent {
		t.Fatal(r.Status)
	}
	// Deleting the follow keeps what it wrote.
	if count(t, app.Deps.DB, `SELECT count(*) FROM watchlist_items WHERE user_id = $1`, user) != 1 {
		t.Fatal("items removed")
	}
	expectError(t, app.Do(http.MethodGet, "/v1/pilot-follows/"+created.ID, tok, nil), http.StatusNotFound, "Follow not found.")
	expectError(t, app.Do(http.MethodGet, "/v1/pilot-follows/not-a-uuid", tok, nil), http.StatusNotFound, "Follow not found.")
}

func TestCreateValidation(t *testing.T) {
	t.Setenv("PILOTS_ENABLED", "1")
	app := newApp(t)
	user := app.CreateUser("pilot-val@example.com")
	tok := app.Token(user)
	expectError(t, app.Do(http.MethodPost, "/v1/pilot-follows", tok, map[string]any{"targetKind": "watchlist"}), http.StatusBadRequest, "No such key 'pilotSlug' at path ''.")
	expectError(t, app.Do(http.MethodPost, "/v1/pilot-follows", tok, map[string]any{"pilotSlug": "nancy-pelosi", "targetKind": "fund"}), http.StatusBadRequest,
		"Cannot initialize PilotFollowTargetKind from invalid String value fund")
	expectError(t, app.Do(http.MethodPost, "/v1/pilot-follows", tok, map[string]any{"pilotSlug": "nancy-pelosi", "targetKind": "watchlist"}), http.StatusConflict,
		"This pilot has no disclosures yet. Try again later.")
	addVersion(t, app.Deps.DB, pelosiID(t, app), 1, map[string]float64{"NVDA": 1})
	r := app.Do(http.MethodPost, "/v1/pilot-follows", tok, map[string]any{"pilotSlug": "nancy-pelosi", "targetKind": "portfolio", "startingCapital": 1000})
	if r.Status != http.StatusForbidden || !strings.Contains(string(r.Body), "upgrade_required") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// snake_case keys decode too.
	if r := app.Do(http.MethodPost, "/v1/pilot-follows", tok, map[string]any{"pilot_slug": "nancy-pelosi", "target_kind": "watchlist"}); r.Status != http.StatusCreated {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestOwnership(t *testing.T) {
	t.Setenv("PILOTS_ENABLED", "true")
	app := newApp(t)
	owner := app.CreateUser("pilot-owner@example.com")
	intruder := app.Token(app.CreateUser("pilot-intruder@example.com"))
	p, _ := pilotsdb.New(app.Deps.DB).PilotByID(bg, pelosiID(t, app))
	f, _ := watchlistFollow(t, app.Deps.DB, owner, p)
	for _, suffix := range []string{"", "/events", "/snapshots"} {
		expectError(t, app.Do(http.MethodGet, "/v1/pilot-follows/"+f.ID.String()+suffix, intruder, nil), http.StatusNotFound, "Follow not found.")
	}
	expectError(t, app.Do(http.MethodDelete, "/v1/pilot-follows/"+f.ID.String(), intruder, nil), http.StatusNotFound, "Follow not found.")
}

func TestFollowCreationScopes(t *testing.T) {
	t.Setenv("PILOTS_ENABLED", "true")
	app := newApp(t)
	user := app.CreateUser("pilot-scope@example.com")
	mustExec(t, app.Deps.DB, `INSERT INTO entitlements (id, user_id, level, created_at) VALUES ($1, $2, 'pro', now())`, uuid.New(), user)
	addVersion(t, app.Deps.DB, pelosiID(t, app), 1, map[string]float64{"NVDA": 1})
	post := func(tok, kind string) *testutil.Response {
		body := map[string]any{"pilotSlug": "nancy-pelosi", "targetKind": kind}
		if kind == TargetPortfolio {
			body["startingCapital"] = 10000
		}
		return app.Do(http.MethodPost, "/v1/pilot-follows", tok, body)
	}
	portfolioOnly := pat(t, app, user, "portfolio:read", "portfolio:write")
	for kind, needed := range map[string]string{TargetPortfolio: "holdings:write", TargetWatchlist: "watchlist:write"} {
		expectError(t, post(portfolioOnly, kind), http.StatusForbidden, "insufficient_scope: '"+needed+"' required")
	}
	if count(t, app.Deps.DB, `SELECT count(*) FROM pilot_follows WHERE user_id=$1`, user) != 0 ||
		count(t, app.Deps.DB, `SELECT count(*) FROM portfolio_lists WHERE user_id=$1`, user) != 0 ||
		count(t, app.Deps.DB, `SELECT count(*) FROM watchlist_lists WHERE user_id=$1`, user) != 0 {
		t.Fatal("wrote on 403")
	}
	readOnly := pat(t, app, user, "portfolio:read")
	expectError(t, post(readOnly, TargetWatchlist), http.StatusForbidden, "")
	watchOnly := pat(t, app, user, "portfolio:write", "watchlist:write")
	expectError(t, post(watchOnly, TargetPortfolio), http.StatusForbidden, "")
	if r := post(watchOnly, TargetWatchlist); r.Status != http.StatusCreated {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	holdings := pat(t, app, user, "portfolio:write", "holdings:write")
	if r := post(holdings, TargetPortfolio); r.Status != http.StatusCreated {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	session := app.Token(user)
	for _, kind := range []string{TargetPortfolio, TargetWatchlist} {
		if r := post(session, kind); r.Status != http.StatusCreated {
			t.Fatalf("session %s: %d %s", kind, r.Status, r.Body)
		}
	}
	if count(t, app.Deps.DB, `SELECT count(*) FROM pilot_follows WHERE user_id=$1`, user) != 4 {
		t.Fatal("follows")
	}
	// The second portfolio follow got "Nancy Pelosi copy 2"; the portfolio follow applied the book.
	var resp []PilotFollowResponse
	app.Do(http.MethodGet, "/v1/pilot-follows", session, nil).JSON(t, &resp)
	for _, f := range resp {
		if f.TargetKind == TargetPortfolio && (f.StartingCapital == nil || *f.StartingCapital != 10000 || f.AppliedVersion != 1) {
			t.Fatal(f)
		}
	}
	if count(t, app.Deps.DB, `SELECT count(*) FROM portfolio_lists WHERE user_id=$1 AND name='Nancy Pelosi copy 2'`, user) != 1 {
		t.Fatal("unique name")
	}
}
