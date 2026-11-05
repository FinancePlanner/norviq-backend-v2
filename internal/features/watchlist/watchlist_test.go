package watchlist_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/earnings"
	"norviq-api/internal/features/watchlist"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/pushdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type env struct {
	*testutil.App
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("SCENARIO_PLANNING_ENABLED", "1")
	return &env{testutil.NewApp(t, []server.Feature{watchlist.Feature{}})}
}

func (e *env) exec(sql string, args ...any) {
	e.T.Helper()
	if _, err := e.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.T.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) user(pro bool) (uuid.UUID, string) {
	e.T.Helper()
	id := e.CreateUser(uuid.NewString() + "@example.com")
	if pro {
		e.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), id)
	}
	return id, e.Token(id)
}

func (e *env) post(path, token, contentType, body string) *testutil.Response {
	e.T.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.Server.URL+path, strings.NewReader(body))
	if err != nil {
		e.T.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.Server.Client().Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var body struct{ Reason string }
	r.JSON(t, &body)
	return body.Reason
}

func expect(t *testing.T, r *testutil.Response, status int, wantReason string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
	if wantReason != "" {
		if got := reason(t, r); got != wantReason {
			t.Fatalf("reason %q, want %q", got, wantReason)
		}
	}
}

type item struct {
	ID              string  `json:"id"`
	Symbol          string  `json:"symbol"`
	Note            *string `json:"note"`
	Status          string  `json:"status"`
	NextReviewAt    *string `json:"nextReviewAt"`
	WatchlistListID string  `json:"watchlistListId"`
}

type list struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
}

// "Watchlist endpoints create, update, and list enriched items".
func TestWatchlistCreateUpdateList(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)

	r := e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": " aapl ", "note": "Core", "status": "researching", "nextReviewAt": "2026-04-01"})
	expect(t, r, 201, "")
	var created item
	r.JSON(t, &created)
	if created.Symbol != "AAPL" || created.Status != "researching" || created.NextReviewAt == nil || *created.NextReviewAt != "2026-04-01" {
		t.Fatalf("created %+v", created)
	}
	if created.ID != strings.ToUpper(created.ID) {
		t.Fatalf("id not uppercase: %s", created.ID)
	}

	// Posting the same symbol again updates in place (200).
	r = e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "AAPL", "status": "ready"})
	expect(t, r, 200, "")

	r = e.Do("PATCH", "/v1/watchlist/"+created.ID, token, map[string]any{"note": "Updated", "status": "waiting"})
	expect(t, r, 200, "")
	var updated item
	r.JSON(t, &updated)
	if updated.Note == nil || *updated.Note != "Updated" || updated.Status != "waiting" {
		t.Fatalf("updated %+v", updated)
	}

	r = e.Do("GET", "/v1/watchlist", token, nil)
	expect(t, r, 200, "")
	var items []item
	r.JSON(t, &items)
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("items %s", r.Body)
	}

	// The default list was created on first use.
	r = e.Do("GET", "/v1/watchlist/lists", token, nil)
	var lists []list
	r.JSON(t, &lists)
	if len(lists) != 1 || lists[0].Name != "Main Watchlist" || !lists[0].IsDefault || lists[0].ID != items[0].WatchlistListID {
		t.Fatalf("lists %s", r.Body)
	}
}

func TestWatchlistValidationErrors(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)
	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "  "}), 400, "Symbol is required.")
	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": 5}), 400,
		"Value was not of type 'String' at path 'symbol'. Expected to decode String but found number instead.")
	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{}), 400,
		`No such key 'symbol' at path ''. No value associated with key CodingKeys(stringValue: "symbol", intValue: nil) ("symbol").`)
	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "X", "nextReviewAt": "2026-02-29"}), 400,
		"Invalid nextReviewAt. Expected YYYY-MM-DD.")
	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "X", "watchlistListId": "nope"}), 400,
		"Invalid watchlistListId.")
	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "X", "watchlistListId": uuid.NewString()}), 404,
		"Watchlist list not found.")
	expect(t, e.Do("POST", "/v1/watchlist", token, []any{}), 400,
		"Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found an array instead.")
	expect(t, e.post("/v1/watchlist", token, "", `{"symbol":"X"}`), 415, "Can't decode data without a content type")
	expect(t, e.Do("DELETE", "/v1/watchlist/"+uuid.NewString(), token, nil), 404, "Watchlist item not found.")
}

func TestWatchlistStatusIsLenient(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)
	r := e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "TSLA", "status": "bogus"})
	expect(t, r, 201, "")
	var it item
	r.JSON(t, &it)
	if it.Status != "active" {
		t.Fatalf("status %q", it.Status)
	}
}

// "a followed watchlist is never promoted to default; a fresh Main Watchlist
// is created".
func TestFollowedWatchlistIsNeverDefault(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(true)
	followed := uuid.New()
	pilot := uuid.New()
	e.exec(`INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at, updated_at) VALUES ($1, $2, 'Main Watchlist', false, now(), now())`, followed, userID)
	e.exec(`INSERT INTO pilots (id, kind, slug, display_name, created_at) VALUES ($1, 'fund', $2, 'Pilot', now())`, pilot, "p-"+pilot.String())
	e.exec(`INSERT INTO pilot_follows (id, user_id, pilot_id, target_kind, watchlist_list_id, created_at) VALUES ($1, $2, $3, 'watchlist', $4, now())`, uuid.New(), userID, pilot, followed)

	r := e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "AAPL"})
	expect(t, r, 201, "")
	var it item
	r.JSON(t, &it)
	if strings.EqualFold(it.WatchlistListID, followed.String()) {
		t.Fatal("item landed in the followed list")
	}
	var lists []list
	e.Do("GET", "/v1/watchlist/lists", token, nil).JSON(t, &lists)
	var names []string
	for _, l := range lists {
		names = append(names, l.Name)
		if l.IsDefault && strings.EqualFold(l.ID, followed.String()) {
			t.Fatal("followed list promoted to default")
		}
	}
	if strings.Join(names, ",") != "Main Watchlist 2,Main Watchlist" {
		t.Fatalf("lists %v", names)
	}
}

func TestWatchlistListsLifecycle(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)
	expect(t, e.Do("GET", "/v1/watchlist/lists", token, nil), 200, "") // creates the default list
	r := e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": "  Tech  "})
	expect(t, r, 201, "")
	var tech list
	r.JSON(t, &tech)
	if tech.Name != "Tech" || tech.IsDefault {
		t.Fatalf("tech %+v", tech)
	}
	expect(t, e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": " "}), 400, "name is required.")
	expect(t, e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": strings.Repeat("x", 81)}), 400, "name must be at most 80 characters.")

	r = e.Do("PATCH", "/v1/watchlist/lists/"+tech.ID, token, map[string]any{"name": "Chips"})
	expect(t, r, 200, "")
	expect(t, e.Do("PATCH", "/v1/watchlist/lists/"+uuid.NewString(), token, map[string]any{"name": "X"}), 404, "Watchlist list not found.")

	// Items of a deleted list merge into the default list.
	expect(t, e.Do("POST", "/v1/watchlist", token, map[string]any{"symbol": "NVDA", "watchlistListId": tech.ID}), 201, "")
	var lists []list
	e.Do("GET", "/v1/watchlist/lists", token, nil).JSON(t, &lists)
	var def list
	for _, l := range lists {
		if l.IsDefault {
			def = l
		}
	}
	if def.ID == "" {
		t.Fatalf("no default list: %+v", lists)
	}
	expect(t, e.Do("DELETE", "/v1/watchlist/lists/"+def.ID, token, nil), 400, "Default watchlist list cannot be deleted.")
	expect(t, e.Do("DELETE", "/v1/watchlist/lists/"+tech.ID, token, nil), 204, "")
	var items []item
	e.Do("GET", "/v1/watchlist?watchlistListId="+def.ID, token, nil).JSON(t, &items)
	if len(items) != 1 || items[0].Symbol != "NVDA" {
		t.Fatalf("merged items %+v", items)
	}
}

type csvPreview struct {
	WatchlistListID string `json:"watchlistListId"`
	Items           []struct {
		Line               int     `json:"line"`
		Symbol             string  `json:"symbol"`
		Note               *string `json:"note"`
		WillUpdateExisting bool    `json:"willUpdateExisting"`
	} `json:"items"`
	Errors []struct {
		Line    int    `json:"line"`
		Message string `json:"message"`
	} `json:"errors"`
}

type csvCommit struct {
	Inserted []item `json:"inserted"`
	Updated  []item `json:"updated"`
}

// WatchlistCsvImportTests.
func TestCSVPreviewAndCommit(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(true)

	// "Preview parses symbol and notes with header aliases".
	r := e.post("/v1/watchlist/import/csv/preview", token, "text/csv", "Ticker,Notes\naapl,Core\n,missing\nmsft,\n")
	expect(t, r, 200, "")
	var p csvPreview
	r.JSON(t, &p)
	if len(p.Items) != 2 || p.Items[0].Symbol != "AAPL" || p.Items[0].Note == nil || *p.Items[0].Note != "Core" || p.Items[1].Symbol != "MSFT" {
		t.Fatalf("preview %s", r.Body)
	}
	if len(p.Errors) != 1 || p.Errors[0].Message != "Missing symbol." {
		t.Fatalf("errors %s", r.Body)
	}

	// "Commit inserts watchlist items into the target list".
	r = e.post("/v1/watchlist/import/csv/commit", token, "text/csv", "symbol,notes\nAAPL,Core\nMSFT,Cloud\n")
	expect(t, r, 200, "")
	var c csvCommit
	r.JSON(t, &c)
	if len(c.Inserted) != 2 || len(c.Updated) != 0 {
		t.Fatalf("commit %s", r.Body)
	}

	// "Commit updates existing symbol notes in the same list" and
	// "Duplicate symbols in CSV keep the last row".
	r = e.post("/v1/watchlist/import/csv/commit", token, "text/csv", "symbol,notes\nAAPL,First\nAAPL,Last\n")
	expect(t, r, 200, "")
	c = csvCommit{}
	r.JSON(t, &c)
	if len(c.Inserted) != 0 || len(c.Updated) != 1 || c.Updated[0].Note == nil || *c.Updated[0].Note != "Last" {
		t.Fatalf("update commit %s", r.Body)
	}

	// "Imports are scoped to the requested watchlist list".
	var other list
	e.Do("POST", "/v1/watchlist/lists", token, map[string]any{"name": "Other"}).JSON(t, &other)
	r = e.post("/v1/watchlist/import/csv/commit?watchlistListId="+other.ID, token, "text/csv", "symbol\nNVDA\n")
	expect(t, r, 200, "")
	var items []item
	e.Do("GET", "/v1/watchlist?watchlistListId="+other.ID, token, nil).JSON(t, &items)
	if len(items) != 1 || items[0].Symbol != "NVDA" {
		t.Fatalf("scoped items %+v", items)
	}

	// "Empty CSV body is rejected" and "Import rejects a watchlist list ID
	// outside the user's themes".
	expect(t, e.post("/v1/watchlist/import/csv/preview", token, "text/csv", ""), 400, "Missing CSV body.")
	expect(t, e.post("/v1/watchlist/import/csv/preview", token, "text/csv", "name\nx\n"), 400, "CSV header must include a symbol column (symbol/ticker).")
	expect(t, e.post("/v1/watchlist/import/csv/commit?watchlistListId="+uuid.NewString(), token, "text/csv", "symbol\nX\n"), 404, "Watchlist list not found.")
}

// "Free users cannot import more watchlist symbols than the resource limit".
func TestCSVImportRespectsFreeLimit(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)
	var b strings.Builder
	b.WriteString("symbol\n")
	for i := range 200 {
		b.WriteString("S" + strings.Repeat("X", i%3) + string(rune('A'+i%26)) + string(rune('A'+i/26)) + "\n")
	}
	r := e.post("/v1/watchlist/import/csv/commit", token, "text/csv", b.String())
	if r.Status != http.StatusForbidden && r.Status != http.StatusPaymentRequired {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	var items []item
	e.Do("GET", "/v1/watchlist", token, nil).JSON(t, &items)
	if len(items) != 0 {
		t.Fatalf("partial import: %d items", len(items))
	}
}

type target struct {
	ID          string  `json:"id"`
	Symbol      string  `json:"symbol"`
	Scenario    string  `json:"scenario"`
	TargetPrice float64 `json:"targetPrice"`
	TargetDate  *string `json:"targetDate"`
}

func TestTargets(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(true)

	r := e.Do("POST", "/v1/targets", token, map[string]any{"symbol": "aapl", "scenario": "BULL", "targetPrice": 250.5, "targetDate": "2026-12-31"})
	expect(t, r, 201, "")
	var created target
	r.JSON(t, &created)
	if created.Symbol != "AAPL" || created.Scenario != "bull" || created.TargetDate == nil || *created.TargetDate != "2026-12-31" {
		t.Fatalf("created %s", r.Body)
	}
	expect(t, e.Do("POST", "/v1/targets", token, map[string]any{"symbol": "AAPL", "scenario": "moon", "targetPrice": 1}), 400, "scenario must be one of: bear, base, bull.")
	expect(t, e.Do("PUT", "/v1/targets/nope", token, map[string]any{"symbol": "AAPL", "scenario": "bull", "targetPrice": 1}), 400, "Invalid target ID")
	expect(t, e.Do("PUT", "/v1/targets/"+uuid.NewString(), token, map[string]any{"symbol": "AAPL", "scenario": "bull", "targetPrice": 1}), 404, "Target not found.")

	// "Updating target resets alert-triggered state".
	e.exec(`UPDATE targets SET alert_triggered_at = now(), alert_triggered_price = 100 WHERE user_id = $1`, userID)
	expect(t, e.Do("PUT", "/v1/targets/"+created.ID, token, map[string]any{"symbol": "AAPL", "scenario": "bull", "targetPrice": 120}), 200, "")
	var triggered int
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM targets WHERE user_id = $1 AND (alert_triggered_at IS NOT NULL OR alert_triggered_price IS NOT NULL)`, userID).Scan(&triggered); err != nil {
		t.Fatal(err)
	}
	if triggered != 0 {
		t.Fatal("alert state not reset")
	}

	var list []target
	e.Do("GET", "/v1/targets?symbol=aapl", token, nil).JSON(t, &list)
	if len(list) != 1 || list[0].TargetPrice != 120 {
		t.Fatalf("list %+v", list)
	}
	expect(t, e.Do("DELETE", "/v1/targets/"+created.ID, token, nil), 204, "")
}

// "Free users are blocked from ... target alert routes".
func TestTargetsUpdateNeedsPremium(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)
	r := e.Do("PUT", "/v1/targets/"+uuid.NewString(), token, map[string]any{"symbol": "AAPL", "scenario": "bull", "targetPrice": 1})
	expect(t, r, 403, "")
	if !strings.Contains(string(r.Body), "target_alerts") {
		t.Fatalf("body %s", r.Body)
	}
}

type riskProfile map[string]any

func TestRiskProfiles(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(true)
	portfolio, holding := uuid.New(), uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main', true, now())`, portfolio, userID)
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, category)
		VALUES ($1, $2, 'BND', 1, 70, '2026-01-01', now(), $3, 'bond')`, holding, userID, portfolio)

	body := map[string]any{
		"holdingId": holding.String(), "assetCategory": "bond", "sector": "  ", "region": " EU ",
		"benchmarkProxy": " agg ", "duration": 6.5, "factorOverrides": map[string]any{"values": map[string]any{"rate_shock": 1.5}},
	}
	r := e.Do("POST", "/v1/holding-risk-profiles", token, body)
	expect(t, r, 200, "")
	var p riskProfile
	r.JSON(t, &p)
	if p["sector"] != nil || p["region"] != "EU" || p["benchmarkProxy"] != "AGG" || p["manualValue"] != nil {
		t.Fatalf("profile %s", r.Body)
	}
	if _, ok := p["manualValue"]; !ok {
		t.Fatalf("nil optional omitted: %s", r.Body)
	}
	if got, _ := json.Marshal(p["factorOverrides"]); string(got) != `{"values":{"rate_shock":1.5}}` {
		t.Fatalf("factorOverrides %s", got)
	}

	// Saving again updates the same row.
	body["factorOverrides"] = nil
	r = e.Do("POST", "/v1/holding-risk-profiles", token, body)
	expect(t, r, 200, "")
	var again riskProfile
	r.JSON(t, &again)
	if again["id"] != p["id"] {
		t.Fatal("upsert created a second profile")
	}
	if got, _ := json.Marshal(again["factorOverrides"]); string(got) != `{"values":{}}` {
		t.Fatalf("default factorOverrides %s", got)
	}

	var all []riskProfile
	e.Do("GET", "/v1/holding-risk-profiles", token, nil).JSON(t, &all)
	if len(all) != 1 {
		t.Fatalf("list %+v", all)
	}

	body["assetCategory"] = "art"
	expect(t, e.Do("POST", "/v1/holding-risk-profiles", token, body), 400, "Invalid risk profile")
	body["assetCategory"] = "bond"
	body["manualValue"] = 0
	expect(t, e.Do("POST", "/v1/holding-risk-profiles", token, body), 400, "Invalid risk profile")
	body["holdingId"] = uuid.NewString()
	expect(t, e.Do("POST", "/v1/holding-risk-profiles", token, body), 404, "Not Found")
	body["holdingId"] = "nope"
	expect(t, e.Do("POST", "/v1/holding-risk-profiles", token, body), 400,
		"Data corrupted at path 'holdingId'. Attempted to decode UUID from invalid UUID string.")
	expect(t, e.Do("POST", "/v1/holding-risk-profiles", token, map[string]any{"holdingId": holding.String(), "assetCategory": "bond", "factorOverrides": map[string]any{}}), 400,
		`No such key 'values' at path 'factorOverrides'. No value associated with key CodingKeys(stringValue: "values", intValue: nil) ("values").`)

	expect(t, e.Do("DELETE", "/v1/holding-risk-profiles/nope", token, nil), 400, "Invalid id")
	expect(t, e.Do("DELETE", "/v1/holding-risk-profiles/"+uuid.NewString(), token, nil), 404, "Not Found")
	expect(t, e.Do("DELETE", "/v1/holding-risk-profiles/"+p["id"].(string), token, nil), 204, "")

	// A row Fluent cannot load (factor_overrides without "values") is a 500.
	e.exec(`INSERT INTO holding_risk_profiles (id, user_id, holding_id, asset_category, factor_overrides, created_at, updated_at)
		VALUES ($1, $2, $3, 'bond', '{}', now(), now())`, uuid.New(), userID, holding)
	expect(t, e.Do("GET", "/v1/holding-risk-profiles", token, nil), 500, "")
}

// "Scenario routes require Pro access", plus the feature flag.
func TestRiskProfilesGates(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)
	r := e.Do("GET", "/v1/holding-risk-profiles", token, nil)
	expect(t, r, 403, "")
	if !strings.Contains(string(r.Body), "scenario_planning") {
		t.Fatalf("body %s", r.Body)
	}

	t.Setenv("SCENARIO_PLANNING_ENABLED", "0")
	off := &env{testutil.NewApp(t, []server.Feature{watchlist.Feature{}})}
	_, proToken := off.user(true)
	expect(t, off.Do("GET", "/v1/holding-risk-profiles", proToken, nil), 404, "Not Found")
}

// ---- pollers ----

type fakeSender struct {
	mu      sync.Mutex
	sent    []push.Notification
	results []push.Summary
}

func (f *fakeSender) Send(_ context.Context, n push.Notification, devices []push.Device) push.Summary {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	if len(f.results) > 0 {
		s := f.results[0]
		f.results = f.results[1:]
		return s
	}
	return push.Summary{Delivered: len(devices)}
}

func (f *fakeSender) Enabled() bool { return true }

type fixedQuotes map[string]float64

func (q fixedQuotes) Quote(_ context.Context, symbol string) (marketdata.QuoteResponse, error) {
	p, ok := q[symbol]
	if !ok {
		return marketdata.QuoteResponse{}, errors.New("no quote")
	}
	return marketdata.QuoteResponse{Symbol: symbol, CurrentPrice: p}, nil
}

func (e *env) pushService(sender push.Sender) *push.Service {
	return &push.Service{Sender: sender, Devices: &push.Devices{Q: pushdb.New(e.Deps.DB)}}
}

func (e *env) device(t *testing.T, userID uuid.UUID) {
	t.Helper()
	_, err := e.pushService(nil).Devices.Upsert(context.Background(), userID, push.Registration{
		DeviceToken: strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")[:64], Platform: "ios", APNSEnvironment: "development", AuthorizationStatus: "authorized",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// "Target alert evaluator triggers bull/bear and does not duplicate after
// trigger".
func TestTargetAlertsTriggerAndDedupe(t *testing.T) {
	e := newEnv(t)
	userID, _ := e.user(true)
	e.device(t, userID)
	for _, tg := range []struct {
		symbol, scenario string
		price            float64
	}{{"AAPL", "bull", 100}, {"MSFT", "bear", 105}, {"TSLA", "base", 120}} {
		e.exec(`INSERT INTO targets (id, user_id, symbol, scenario, target_price, created_at) VALUES ($1, $2, $3, $4, $5, now())`,
			uuid.New(), userID, tg.symbol, tg.scenario, tg.price)
	}
	sender := &fakeSender{}
	sweep := watchlist.TargetAlerts{
		DB: e.Deps.DB, Quotes: fixedQuotes{"AAPL": 101.25, "MSFT": 101.25, "TSLA": 101.25},
		Push: e.pushService(sender), Logger: e.Deps.Logger,
	}
	sweep.Run(context.Background())
	sweep.Run(context.Background())

	if len(sender.sent) != 2 {
		t.Fatalf("sent %d pushes, want 2", len(sender.sent))
	}
	for _, n := range sender.sent {
		if s := n.Payload["symbol"]; s != "AAPL" && s != "MSFT" {
			t.Fatalf("pushed %v", s)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM targets WHERE user_id = $1 AND alert_triggered_price = 101.25 AND alert_triggered_at IS NOT NULL`, userID); n != 2 {
		t.Fatalf("triggered %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM targets WHERE user_id = $1 AND symbol = 'TSLA' AND alert_triggered_at IS NULL`, userID); n != 1 {
		t.Fatal("base target triggered")
	}
	var body string
	if err := e.Deps.DB.QueryRow(context.Background(),
		`SELECT body FROM notification_events WHERE user_id = $1 AND kind = 'price_target' AND body LIKE 'AAPL%'`, userID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "AAPL reached 101.25." {
		t.Fatalf("inbox body %q", body)
	}
}

// Vapor's "does not mark target triggered when all deliveries fail" is
// reversed on purpose: v2 marks before it sends, so a failed mark can no
// longer re-send the same push every poll. A failed send is not retried.
func TestTargetAlertsMarkBeforeSendAndNeverRepeat(t *testing.T) {
	e := newEnv(t)
	userID, _ := e.user(true)
	e.device(t, userID)
	e.exec(`INSERT INTO targets (id, user_id, symbol, scenario, target_price, created_at) VALUES ($1, $2, 'NVDA', 'bull', 100, now())`, uuid.New(), userID)
	sender := &fakeSender{results: []push.Summary{{Delivered: 0, Failed: 1}}}
	sweep := watchlist.TargetAlerts{DB: e.Deps.DB, Quotes: fixedQuotes{"NVDA": 150}, Push: e.pushService(sender), Logger: e.Deps.Logger}
	sweep.Run(context.Background())
	sweep.Run(context.Background())
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d, want 1", len(sender.sent))
	}
	if n := e.count(t, `SELECT count(*) FROM targets WHERE user_id = $1 AND alert_triggered_at IS NOT NULL AND alert_triggered_price = 150`, userID); n != 1 {
		t.Fatal("target not marked before the send")
	}
	if n := e.count(t, `SELECT count(*) FROM notification_events WHERE user_id = $1 AND kind = 'price_target'`, userID); n != 1 {
		t.Fatalf("inbox events %d", n)
	}
}

// The earnings delivery row is written only after a push was delivered, so
// a failed send is retried on the next poll instead of counting as sent.
func TestEarningsReminderRetriesAFailedSend(t *testing.T) {
	e := newEnv(t)
	holder, _ := e.user(true)
	e.device(t, holder)
	portfolio := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main', true, now())`, portfolio, holder)
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, category)
		VALUES ($1, $2, 'AAPL', 1, 100, '2026-01-01', now(), $3, 'stock')`, uuid.New(), holder, portfolio)
	now := time.Date(2026, 10, 29, 23, 30, 0, 0, time.UTC)
	sender := &fakeSender{results: []push.Summary{{Delivered: 0, Failed: 1}}}
	sweep := watchlist.EarningsReminders{
		DB: e.Deps.DB, Calendar: &stubCalendar{byDate: map[string][]string{"2026-10-30": {"AAPL"}}},
		Push: e.pushService(sender), Logger: e.Deps.Logger, Now: func() time.Time { return now },
		Premium: premiumSet{holder: true},
	}
	sweep.Run(context.Background())
	if n := e.count(t, `SELECT count(*) FROM earnings_notification_deliveries WHERE user_id = $1`, holder); n != 0 {
		t.Fatalf("failed send recorded as delivered (%d rows)", n)
	}
	sweep.Run(context.Background())
	sweep.Run(context.Background())
	if len(sender.sent) != 2 {
		t.Fatalf("sent %d, want the failure and one retry", len(sender.sent))
	}
	if n := e.count(t, `SELECT count(*) FROM earnings_notification_deliveries WHERE user_id = $1`, holder); n != 1 {
		t.Fatalf("deliveries %d", n)
	}
}

type stubCalendar struct {
	mu      sync.Mutex
	queries []earnings.Query
	byDate  map[string][]string
}

func (c *stubCalendar) Fetch(_ context.Context, q earnings.Query) ([]earnings.ItemResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, q)
	var out []earnings.ItemResponse
	for _, s := range c.byDate[*q.From] {
		out = append(out, earnings.ItemResponse{Symbol: &s})
	}
	return out, nil
}

type premiumSet map[uuid.UUID]bool

func (p premiumSet) RequirePremium(_ context.Context, f entitlements.Feature, userID uuid.UUID) error {
	if f != entitlements.FeatureTargetAlerts || !p[userID] {
		return errors.New("upgrade required")
	}
	return nil
}

func TestEarningsReminders(t *testing.T) {
	e := newEnv(t)
	holder, _ := e.user(true)
	watcher, _ := e.user(true)
	archived, _ := e.user(true)
	free, _ := e.user(false)
	optedOut, _ := e.user(true)
	for _, u := range []uuid.UUID{holder, watcher} {
		e.device(t, u)
	}
	portfolio := uuid.New()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main', true, now())`, portfolio, holder)
	e.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, category)
		VALUES ($1, $2, 'AAPL', 1, 100, '2026-01-01', now(), $3, 'stock')`, uuid.New(), holder, portfolio)
	for _, w := range []struct {
		user   uuid.UUID
		status string
	}{{watcher, "active"}, {archived, "archived"}, {free, "active"}, {optedOut, "active"}} {
		l := uuid.New()
		e.exec(`INSERT INTO watchlist_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main Watchlist', true, now())`, l, w.user)
		e.exec(`INSERT INTO watchlist_items (id, user_id, watchlist_list_id, symbol, status, created_at) VALUES ($1, $2, $3, 'AAPL', $4, now())`, uuid.New(), w.user, l, w.status)
	}
	e.exec(`INSERT INTO earnings_notification_preferences (id, user_id, enabled, created_at, updated_at) VALUES ($1, $2, false, now(), now())`, uuid.New(), optedOut)

	now := time.Date(2026, 10, 29, 23, 30, 0, 0, time.UTC)
	cal := &stubCalendar{byDate: map[string][]string{"2026-11-05": {" aapl ", "AAPL"}, "2026-10-30": {"AAPL"}}}
	sender := &fakeSender{}
	sweep := watchlist.EarningsReminders{
		DB: e.Deps.DB, Calendar: cal, Push: e.pushService(sender), Logger: e.Deps.Logger, Now: func() time.Time { return now },
		Premium: premiumSet{holder: true, watcher: true, archived: true, optedOut: true},
	}
	sweep.Run(context.Background())
	sweep.Run(context.Background())

	if len(cal.queries) != 4 || *cal.queries[0].From != "2026-11-05" || *cal.queries[0].To != "2026-11-05" || *cal.queries[1].From != "2026-10-30" {
		t.Fatalf("calendar queries %+v", cal.queries)
	}
	// Holder and active watcher, at 7 and 1 days, once each.
	if len(sender.sent) != 4 {
		t.Fatalf("sent %d pushes, want 4", len(sender.sent))
	}
	if n := e.count(t, `SELECT count(*) FROM earnings_notification_deliveries WHERE symbol = 'AAPL'`); n != 4 {
		t.Fatalf("deliveries %d", n)
	}
	for _, u := range []uuid.UUID{archived, free, optedOut} {
		if n := e.count(t, `SELECT count(*) FROM notification_events WHERE user_id = $1`, u); n != 0 {
			t.Fatalf("user %s notified", u)
		}
	}
	var bodies []string
	rows, err := e.Deps.DB.Query(context.Background(), `SELECT body || '|' || deduplication_key FROM notification_events WHERE user_id = $1 ORDER BY deduplication_key`, holder)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, s)
	}
	want := "AAPL reports earnings in 1 day.|earnings:AAPL:2026-10-30:1,AAPL reports earnings in 7 days.|earnings:AAPL:2026-11-05:7"
	if strings.Join(bodies, ",") != want {
		t.Fatalf("bodies %v", bodies)
	}
}

func TestJobNamesMatchVaporLocks(t *testing.T) {
	if (watchlist.TargetAlertJob{}).Name() != "target_alert_poller" || (watchlist.EarningsReminderJob{}).Name() != "earnings_notification_poller" {
		t.Fatal("job names drifted from the Vapor JobLock names")
	}
}

// The earnings calendar wrapper reads the same Finnhub endpoint as GET
// /v1/earnings.
func TestEarningsCalendarFetch(t *testing.T) {
	var gotQuery string
	stub := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		gotQuery = r.URL.RawQuery
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewBufferString(`{"earningsCalendar":[{"symbol":"AAPL","date":"2026-11-05"}]}`))}, nil
	})}
	cal := earnings.NewCalendar(earnings.Settings{FinnhubAPIKey: "test", FinnhubBaseURL: "http://finnhub.invalid"}, stub)
	d := "2026-11-05"
	items, err := cal.Fetch(context.Background(), earnings.Query{From: &d, To: &d})
	if err != nil || len(items) != 1 || *items[0].Symbol != "AAPL" {
		t.Fatalf("items %+v err %v", items, err)
	}
	if gotQuery != "from=2026-11-05&to=2026-11-05" {
		t.Fatalf("query %q", gotQuery)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The assistant reads targets without the HTTP route's Pro gate, by decision:
// see the quick assistant plan. This records that it is deliberate.
func TestActionsListTargetsSkipsTheProGateAndStaysWithTheCaller(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user(true)
	other, _ := e.user(false)
	expect(t, e.Do("POST", "/v1/targets", token, map[string]any{"symbol": "aapl", "scenario": "BULL", "targetPrice": 250.5}), 201, "")
	e.exec(`DELETE FROM entitlements WHERE user_id = $1`, userID)

	actions := watchlist.NewActions(e.Deps)
	mine, err := actions.ListTargets(context.Background(), userID, 50)
	if err != nil || len(mine) != 1 {
		t.Fatalf("mine = %+v, %v", mine, err)
	}
	theirs, err := actions.ListTargets(context.Background(), other, 50)
	if err != nil || len(theirs) != 0 {
		t.Fatalf("theirs = %+v, %v", theirs, err)
	}
}
