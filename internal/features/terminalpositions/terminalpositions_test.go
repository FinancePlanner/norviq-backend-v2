package terminalpositions_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/terminalpositions"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// MARK: - Harness

type env struct {
	*testutil.App
	svc *terminalpositions.Service
}

// newEnv starts the feature (plus the consent routes) with an AI platform
// built from aiEnv only, so a developer's shell keys never leak in.
func newEnv(t *testing.T, aiEnv map[string]string) *env {
	t.Helper()
	app := testutil.NewApp(t, []server.Feature{aiplatform.Feature{}, terminalpositions.Feature{}}, func(d *server.Deps) {
		p, err := ai.New(ai.Options{Env: ai.MapEnv(aiEnv), Logger: slog.New(slog.DiscardHandler), DB: d.DB, HTTP: &http.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		aiplatform.SetPlatform(d, p)
	})
	return &env{App: app, svc: terminalpositions.NewService(app.Deps.DB)}
}

func (e *env) exec(sql string, args ...any) {
	e.T.Helper()
	if _, err := e.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.T.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) user(pro bool) (uuid.UUID, string) {
	e.T.Helper()
	id := e.CreateUser("tps_" + uuid.NewString()[:8] + "@example.com")
	if pro {
		e.exec(`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), id)
	}
	return id, e.Token(id)
}

func (e *env) pat(userID uuid.UUID, scopes ...string) string {
	e.T.Helper()
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	e.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		 VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`,
		uuid.New(), userID, auth.SHA256Hex(token), scopes)
	return token
}

func (e *env) do(method, path, token string, body any) *testutil.Response {
	e.T.Helper()
	return e.Do(method, path, token, body)
}

func expectStatus(t *testing.T, r *testutil.Response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}

func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var body struct{ Reason string }
	r.JSON(t, &body)
	return body.Reason
}

func f(v float64) *float64 { return &v }
func s(v string) *string   { return &v }

func amzn() terminalpositions.CreateRequest {
	return terminalpositions.CreateRequest{
		Ticker: "amzn", SharesOutstanding: f(10_600_000_000), TerminalShareCount: 11_000_000_000,
		TerminalMarketCap: 10_000_000_000_000, ValueWanted: 1_000_000, SharesOwned: f(750),
	}
}

func vg() terminalpositions.CreateRequest {
	return terminalpositions.CreateRequest{Ticker: "VG", TerminalShareCount: 200_000_000, TerminalMarketCap: 12_500_000_000, ValueWanted: 500_000}
}

func statusOf(err error) int {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Status
	}
	if err != nil {
		return 500
	}
	return 0
}

// MARK: - Routes (TerminalPositionsRouteTests)

func TestAuthMatrix(t *testing.T) {
	e := newEnv(t, nil)
	id, _ := e.user(false)
	wrong := e.pat(id, "expenses:read")
	expectStatus(t, e.do("GET", "/v1/terminal-positions", "", nil), 401)
	expectStatus(t, e.do("GET", "/v1/terminal-positions", wrong, nil), 403)
	expectStatus(t, e.do("POST", "/v1/terminal-positions/ai/share-facts", wrong, map[string]string{"ticker": "x"}), 403)

	readOnly := e.pat(id, "planning:read")
	expectStatus(t, e.do("GET", "/v1/terminal-positions", readOnly, nil), 200)
	expectStatus(t, e.do("POST", "/v1/terminal-positions", readOnly, amzn()), 403)
}

func TestLifecycle(t *testing.T) {
	e := newEnv(t, nil)
	_, tok := e.user(false)

	r := e.do("POST", "/v1/terminal-positions", tok, amzn())
	expectStatus(t, r, 201)
	var created terminalpositions.PositionResponse
	r.JSON(t, &created)
	if created.Ticker != "AMZN" || created.SharesNeeded == nil || math.Abs(*created.SharesNeeded-1100) > 1e-9 {
		t.Fatalf("created = %s", r.Body)
	}
	if created.ID != strings.ToUpper(created.ID) {
		t.Fatalf("id not uppercase: %s", created.ID)
	}
	expectStatus(t, e.do("POST", "/v1/terminal-positions", tok, vg()), 201)

	r = e.do("GET", "/v1/terminal-positions?ticker=amzn", tok, nil)
	expectStatus(t, r, 200)
	var list terminalpositions.ListResponse
	r.JSON(t, &list)
	if len(list.Positions) != 1 || list.Positions[0].Ticker != "AMZN" || list.Currency == "" {
		t.Fatalf("filtered list = %s", r.Body)
	}

	r = e.do("PATCH", "/v1/terminal-positions/"+created.ID, tok, map[string]any{"terminalShareCount": 0})
	expectStatus(t, r, 200)
	if strings.Contains(string(r.Body), "sharesNeeded") || !strings.Contains(string(r.Body), `"scenarioError":"share_count_not_positive"`) {
		t.Fatalf("patched = %s", r.Body)
	}

	expectStatus(t, e.do("POST", "/v1/terminal-positions/"+created.ID+"/duplicate", tok, nil), 201)
	r = e.do("GET", "/v1/terminal-positions", tok, nil)
	r.JSON(t, &list)
	if len(list.Positions) != 3 {
		t.Fatalf("list = %s", r.Body)
	}
	ids := make([]string, 0, 3)
	for i := len(list.Positions) - 1; i >= 0; i-- {
		ids = append(ids, list.Positions[i].ID)
	}
	r = e.do("PUT", "/v1/terminal-positions/order", tok, map[string]any{"ids": ids})
	expectStatus(t, r, 200)
	r.JSON(t, &list)
	for i, p := range list.Positions {
		if p.ID != ids[i] {
			t.Fatalf("reordered = %s", r.Body)
		}
	}

	r = e.do("GET", "/v1/terminal-positions/summary", tok, nil)
	expectStatus(t, r, 200)
	var sum terminalpositions.SummaryResponse
	r.JSON(t, &sum)
	if sum.PositionCount != 3 {
		t.Fatalf("summary = %s", r.Body)
	}
	expectStatus(t, e.do("DELETE", "/v1/terminal-positions/"+created.ID, tok, nil), 204)
}

func TestOtherUserAndBadIDs(t *testing.T) {
	e := newEnv(t, nil)
	_, owner := e.user(false)
	_, other := e.user(false)
	r := e.do("POST", "/v1/terminal-positions", owner, vg())
	var row terminalpositions.PositionResponse
	r.JSON(t, &row)
	r = e.do("DELETE", "/v1/terminal-positions/"+row.ID, other, nil)
	expectStatus(t, r, 404)
	if reason(t, r) != "Terminal position not found." {
		t.Fatalf("reason = %s", r.Body)
	}
	r = e.do("DELETE", "/v1/terminal-positions/not-a-uuid", owner, nil)
	expectStatus(t, r, 400)
	if reason(t, r) != "invalid id" {
		t.Fatalf("reason = %s", r.Body)
	}
	// The body is decoded before the id is read.
	r = e.do("PATCH", "/v1/terminal-positions/nope", owner, []byte(`{"valueWanted":"x"}`))
	expectStatus(t, r, 400)
	if reason(t, r) == "invalid id" {
		t.Fatalf("decode should fail first: %s", r.Body)
	}
}

func TestCapsInputs(t *testing.T) {
	e := newEnv(t, nil)
	_, tok := e.user(false)
	r := e.do("POST", "/v1/terminal-positions", tok, terminalpositions.CreateRequest{Ticker: "BIG", TerminalShareCount: 1, TerminalMarketCap: 1, ValueWanted: 1e19})
	expectStatus(t, r, 422)
	if reason(t, r) != "valueWanted is too large" {
		t.Fatalf("reason = %s", r.Body)
	}
	atCap := terminalpositions.CreateRequest{
		Ticker: "CAP", SharesOutstanding: f(1e18), TerminalShareCount: 1e18, TerminalMarketCap: 1e18,
		ValueWanted: 1e18, SharesOwned: f(1e18), CurrentSharePrice: f(1e18),
	}
	expectStatus(t, e.do("POST", "/v1/terminal-positions", tok, atCap), 201)
	expectStatus(t, e.do("POST", "/v1/terminal-positions", tok, atCap), 201)
	r = e.do("GET", "/v1/terminal-positions/summary", tok, nil)
	expectStatus(t, r, 200)
	var sum terminalpositions.SummaryResponse
	r.JSON(t, &sum)
	if sum.TotalCapitalAtTodayPrice == nil || math.IsInf(*sum.TotalCapitalAtTodayPrice, 0) {
		t.Fatalf("summary = %s", r.Body)
	}
}

func TestDecodeErrors(t *testing.T) {
	e := newEnv(t, nil)
	_, tok := e.user(false)
	// terminalShareCount is required.
	r := e.do("POST", "/v1/terminal-positions", tok, map[string]any{"ticker": "X", "terminalMarketCap": 1, "valueWanted": 1})
	expectStatus(t, r, 400)
	r = e.do("PUT", "/v1/terminal-positions/order", tok, map[string]any{})
	expectStatus(t, r, 400)
	r = e.do("PUT", "/v1/terminal-positions/order", tok, map[string]any{"ids": []string{"nope"}})
	expectStatus(t, r, 422)
	if reason(t, r) != "invalid id nope" {
		t.Fatalf("reason = %s", r.Body)
	}
}

// MARK: - Service (TerminalPositionsServiceTests)

func TestCreateAppends(t *testing.T) {
	e := newEnv(t, nil)
	id, _ := e.user(false)
	ctx := context.Background()
	first, err := e.svc.Create(ctx, id, amzn())
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.Create(ctx, id, vg())
	if err != nil {
		t.Fatal(err)
	}
	if first.Ticker != "AMZN" || first.SortOrder != 0 || second.SortOrder != 1 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t, nil)
	id, _ := e.user(false)
	ctx := context.Background()
	_, err := e.svc.Create(ctx, id, terminalpositions.CreateRequest{Ticker: "  $$ ", TerminalShareCount: 1, TerminalMarketCap: 1, ValueWanted: 1})
	if statusOf(err) != 422 {
		t.Fatalf("bad ticker: %v", err)
	}
	_, err = e.svc.Create(ctx, id, terminalpositions.CreateRequest{Ticker: "VG", TerminalShareCount: 1, TerminalMarketCap: 1, ValueWanted: -1})
	if statusOf(err) != 422 {
		t.Fatalf("negative: %v", err)
	}
	stored, err := e.svc.Create(ctx, id, terminalpositions.CreateRequest{Ticker: "VG", TerminalShareCount: 0, TerminalMarketCap: 12_500_000_000, ValueWanted: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if got := terminalpositions.ToResponse(stored).ScenarioError; got == nil || *got != "share_count_not_positive" {
		t.Fatalf("scenarioError = %v", got)
	}
	if _, err := terminalpositions.NormalisedTicker(" brk.b "); err != nil {
		t.Fatal(err)
	}
	if _, err := terminalpositions.NormalisedTicker("ABCDEFGHIJKLM"); statusOf(err) != 422 {
		t.Fatalf("13 chars: %v", err)
	}
}

func TestPatchAndClear(t *testing.T) {
	e := newEnv(t, nil)
	uid, _ := e.user(false)
	ctx := context.Background()
	created, err := e.svc.Create(ctx, uid, terminalpositions.CreateRequest{
		Ticker: "AMZN", TerminalShareCount: 11_000_000_000, TerminalMarketCap: 10_000_000_000_000,
		ValueWanted: 1_000_000, CurrentSharePrice: f(200), Notes: s("base case"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := e.svc.Update(ctx, uid, created.ID, terminalpositions.UpdateRequest{Clear: &[]string{"currentSharePrice"}})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.CurrentSharePrice != nil || cleared.Notes == nil || *cleared.Notes != "base case" || cleared.ValueWanted != 1_000_000 {
		t.Fatalf("cleared = %+v", cleared)
	}
	changed, err := e.svc.Update(ctx, uid, created.ID, terminalpositions.UpdateRequest{ValueWanted: f(2_000_000)})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ValueWanted != 2_000_000 || changed.TerminalShareCount != 11_000_000_000 {
		t.Fatalf("changed = %+v", changed)
	}
	_, err = e.svc.Update(ctx, uid, created.ID, terminalpositions.UpdateRequest{Clear: &[]string{"valueWanted", "abc"}})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 422 || he.Reason != "cannot clear: abc, valueWanted" {
		t.Fatalf("unknown clear: %v", err)
	}
}

// Fluent skips the UPDATE (and the updatedAt touch) when nothing was assigned.
func TestEmptyPatchKeepsUpdatedAt(t *testing.T) {
	e := newEnv(t, nil)
	uid, tok := e.user(false)
	row, err := e.svc.Create(context.Background(), uid, vg())
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE terminal_positions SET updated_at = '2020-01-02T03:04:05Z' WHERE id = $1`, row.ID)
	r := e.do("PATCH", "/v1/terminal-positions/"+row.ID.String(), tok, map[string]any{})
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.Body), `"updatedAt":"2020-01-02T03:04:05Z"`) {
		t.Fatalf("updatedAt touched: %s", r.Body)
	}
	r = e.do("PATCH", "/v1/terminal-positions/"+row.ID.String(), tok, map[string]any{"notes": "x"})
	if strings.Contains(string(r.Body), `"updatedAt":"2020-01-02T03:04:05Z"`) {
		t.Fatalf("updatedAt not touched: %s", r.Body)
	}
}

func TestScoping(t *testing.T) {
	e := newEnv(t, nil)
	owner, _ := e.user(false)
	other, _ := e.user(false)
	ctx := context.Background()
	row, err := e.svc.Create(ctx, owner, vg())
	if err != nil {
		t.Fatal(err)
	}
	_, errU := e.svc.Update(ctx, other, row.ID, terminalpositions.UpdateRequest{ValueWanted: f(1)})
	errD := e.svc.Delete(ctx, other, row.ID)
	_, errDup := e.svc.Duplicate(ctx, other, row.ID)
	if statusOf(errU) != 404 || statusOf(errD) != 404 || statusOf(errDup) != 404 {
		t.Fatalf("update=%v delete=%v duplicate=%v", errU, errD, errDup)
	}
}

func TestDuplicatePlacement(t *testing.T) {
	e := newEnv(t, nil)
	uid, _ := e.user(false)
	ctx := context.Background()
	a, _ := e.svc.Create(ctx, uid, amzn())
	if _, err := e.svc.Create(ctx, uid, vg()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Duplicate(ctx, uid, a.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := e.svc.List(ctx, uid, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, r := range rows {
		got = append(got, r.Ticker)
		if r.SortOrder != int64(i) {
			t.Fatalf("sortOrder %d at %d", r.SortOrder, i)
		}
	}
	if strings.Join(got, ",") != "AMZN,AMZN,VG" {
		t.Fatalf("tickers = %v", got)
	}
}

func TestReorder(t *testing.T) {
	e := newEnv(t, nil)
	uid, _ := e.user(false)
	ctx := context.Background()
	a, _ := e.svc.Create(ctx, uid, amzn())
	b, _ := e.svc.Create(ctx, uid, vg())
	ids := []string{b.ID.String(), strings.ToUpper(a.ID.String())}
	rows, err := e.svc.Reorder(ctx, uid, ids)
	if err != nil || len(rows) != 2 || rows[0].Ticker != "VG" {
		t.Fatalf("reorder = %v %v", rows, err)
	}
	for _, bad := range [][]string{{ids[0]}, {ids[0], ids[0]}, {ids[0], uuid.NewString()}} {
		if _, err := e.svc.Reorder(ctx, uid, bad); statusOf(err) != 422 {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	after, _ := e.svc.List(ctx, uid, nil)
	if after[0].Ticker != "VG" || after[1].Ticker != "AMZN" {
		t.Fatalf("after = %v", after)
	}
}

func TestSummary(t *testing.T) {
	e := newEnv(t, nil)
	uid, _ := e.user(false)
	ctx := context.Background()
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, base_currency) VALUES ($1, $2, 'Main', true, now(), 'EUR')`, uuid.New(), uid)
	mustCreate := func(in terminalpositions.CreateRequest) {
		if _, err := e.svc.Create(ctx, uid, in); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate(terminalpositions.CreateRequest{
		Ticker: "AMZN", TerminalShareCount: 11_000_000_000, TerminalMarketCap: 10_000_000_000_000,
		ValueWanted: 1_000_000, SharesOwned: f(750), CurrentSharePrice: f(200),
	})
	mustCreate(vg())
	mustCreate(terminalpositions.CreateRequest{Ticker: "BAD", TerminalShareCount: 0, TerminalMarketCap: 1, ValueWanted: 99})
	e.exec(`INSERT INTO autobuys (id, user_id, label, amount, cadence, active, created_at, updated_at) VALUES ($1, $2, 'AMZN', 275, 'bimonthly', true, now(), now())`, uuid.New(), uid)
	e.exec(`INSERT INTO autobuys (id, user_id, label, amount, cadence, active, created_at, updated_at) VALUES ($1, $2, 'Paused', 999, 'monthly', false, now(), now())`, uuid.New(), uid)

	sum, err := e.svc.Summary(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	expectedGap := 350*(10_000_000_000_000.0/11_000_000_000) + 500_000
	if sum.Currency != "EUR" || sum.PositionCount != 3 || sum.TotalValueWanted != 1_500_000 ||
		math.Abs(sum.TotalGapValueAtTerminal-expectedGap) > 1e-3 ||
		sum.TotalCapitalAtTodayPrice == nil || math.Abs(*sum.TotalCapitalAtTodayPrice-220_000) > 1e-6 ||
		sum.PricedPositionCount != 1 || sum.MonthlyAutobuyTotal != 137.5 ||
		len(sum.TopPositions) != 2 || sum.TopPositions[0].Ticker != "AMZN" || sum.TopPositions[1].Ticker != "VG" {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestCurrencyFallback(t *testing.T) {
	e := newEnv(t, nil)
	uid, _ := e.user(false)
	t.Setenv("MARKET_DEFAULT_CURRENCY", "GBP")
	if c, _ := e.svc.Currency(context.Background(), uid); c != "GBP" {
		t.Fatalf("currency = %s", c)
	}
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, base_currency) VALUES ($1, $2, 'Side', false, now(), 'CHF')`, uuid.New(), uid)
	if c, _ := e.svc.Currency(context.Background(), uid); c != "CHF" {
		t.Fatalf("currency = %s", c)
	}
}

func TestUpsertScenario(t *testing.T) {
	e := newEnv(t, nil)
	uid, _ := e.user(false)
	ctx := context.Background()
	_, err := e.svc.UpsertScenario(ctx, uid, "SOFI", terminalpositions.ScenarioFields{ValueWanted: f(250_000)})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 422 || he.Reason != "a new scenario needs terminalShareCount, terminalMarketCap and valueWanted" {
		t.Fatalf("incomplete: %v", err)
	}
	if rows, _ := e.svc.List(ctx, uid, nil); len(rows) != 0 {
		t.Fatalf("rows = %v", rows)
	}
	created, err := e.svc.UpsertScenario(ctx, uid, "sofi", terminalpositions.ScenarioFields{
		TerminalShareCount: f(1_750_000_000), TerminalMarketCap: f(150_000_000_000), ValueWanted: f(250_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := e.svc.UpsertScenario(ctx, uid, "SOFI", terminalpositions.ScenarioFields{SharesOwned: f(1000)})
	if err != nil {
		t.Fatal(err)
	}
	p := terminalpositions.ToResponse(updated).Progress
	if updated.ID != created.ID || p == nil || math.Abs(*p-0.3428571429) > 1e-9 {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := e.svc.UpsertScenario(ctx, uid, "$$", terminalpositions.ScenarioFields{}); statusOf(err) != 422 {
		t.Fatalf("bad ticker: %v", err)
	}
}

// MARK: - Advisor (TerminalAIAdvisorTests)

const factsJSON = "Sure:\n```json\n{\"sharesOutstanding\": 10600000000, \"currentSharePrice\": 221.3, \"currency\": \"usd\", \"asOf\": \"2026-09-30\",\n" +
	" \"sources\": [\"https://www.sec.gov/amzn-10q\", \"http://insecure.example\"]}\n```"

const scenarioJSON = `{"terminalShareCount": 1750000000, "terminalMarketCap": 150000000000, "rationale": "Consensus growth.", "sources": ["https://a.example"]}`

func TestParseShareFacts(t *testing.T) {
	facts, err := terminalpositions.ParseShareFacts(factsJSON, "AMZN")
	if err != nil {
		t.Fatal(err)
	}
	if *facts.SharesOutstanding != 10_600_000_000 || *facts.CurrentSharePrice != 221.3 || *facts.Currency != "USD" ||
		len(facts.Sources) != 1 || facts.Sources[0] != "https://www.sec.gov/amzn-10q" {
		t.Fatalf("facts = %+v", facts)
	}
	for _, bad := range []string{
		`{"sharesOutstanding": 1000, "currentSharePrice": 10, "sources": []}`,
		`{"sharesOutstanding": -5, "currentSharePrice": 0, "sources": ["https://x.example"]}`,
		"no json",
		`{"SharesOutstanding": 1000, "sources": ["https://x.example"]}`, // keys are case-sensitive
		`{"sharesOutstanding": "1000", "sources": ["https://x.example"]}`,
	} {
		_, err := terminalpositions.ParseShareFacts(bad, "X")
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != 422 {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	// A currency that is not three characters is dropped, not an error.
	facts, err = terminalpositions.ParseShareFacts(`{"currentSharePrice": 3, "currency": "euro", "sources": ["https://x.example"]}`, "X")
	if err != nil || facts.Currency != nil {
		t.Fatalf("facts = %+v %v", facts, err)
	}
}

func TestParseScenario(t *testing.T) {
	sc, err := terminalpositions.ParseScenario(scenarioJSON, "SOFI", 10)
	if err != nil || sc.TerminalShareCount != 1_750_000_000 || sc.HorizonYears != 10 {
		t.Fatalf("scenario = %+v %v", sc, err)
	}
	for _, bad := range []string{
		`{"terminalShareCount": 0, "terminalMarketCap": 1, "rationale": "x", "sources": ["https://a.example"]}`,
		`{"terminalShareCount": 1, "terminalMarketCap": 1, "rationale": " ", "sources": ["https://a.example"]}`,
		`{"terminalShareCount": 1, "terminalMarketCap": 1, "sources": ["https://a.example"]}`,
		`{"terminalShareCount": 1, "terminalMarketCap": 1, "rationale": "` + strings.Repeat("a", 601) + `", "sources": ["https://a.example"]}`,
	} {
		if _, err := terminalpositions.ParseScenario(bad, "X", 10); statusOf(err) != 422 {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

func TestLiveClientEligibility(t *testing.T) {
	u := "https://example.invalid/v1"
	cfg := func(provider, key string) ai.ProviderConfig {
		return ai.ProviderConfig{Provider: provider, APIKey: key, BaseURL: u}
	}
	cases := []struct {
		cfg   ai.ProviderConfig
		model *string
		want  bool
	}{
		{cfg(ai.ProviderOpenRouter, "k"), nil, true},
		{cfg(ai.ProviderOpenAI, "k"), nil, false},
		{cfg(ai.ProviderOpenAI, "k"), s("  "), false},
		{cfg(ai.ProviderOpenAI, "k"), s("gpt-x"), true},
		{cfg(ai.ProviderOpenRouter, ""), nil, false},
	}
	for i, c := range cases {
		if got := terminalpositions.LiveClient(c.cfg, c.model, nil, nil) != nil; got != c.want {
			t.Fatalf("case %d: %v", i, got)
		}
	}
	if r := terminalpositions.UnavailableReason(ai.ProviderCustom, "k", u, nil); !strings.Contains(r, "TERMINAL_AI_MODEL") {
		t.Fatalf("reason = %q", r)
	}
}

// scripted replies in order, recording every user message.
type scripted struct {
	mu      sync.Mutex
	replies []string
	err     error
	users   []string
}

func (c *scripted) Chat(_ context.Context, messages []ai.Message, _ []ai.Tool, _ string) (ai.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range messages {
		if m.Role == "user" {
			c.users = append(c.users, m.ContentString())
		}
	}
	if c.err != nil {
		return ai.Message{}, c.err
	}
	if len(c.replies) == 0 {
		return ai.Message{}, errors.New("no reply")
	}
	r := c.replies[0]
	if len(c.replies) > 1 {
		c.replies = c.replies[1:]
	}
	return ai.Text("assistant", r), nil
}

func (e *env) postAI(route, token string, body any, headers map[string]string) *testutil.Response {
	e.T.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.Server.URL+"/v1/terminal-positions/ai/"+route, strings.NewReader(string(raw)))
	if err != nil {
		e.T.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.Server.Client().Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

func TestFreeIsUpgrade(t *testing.T) {
	e := newEnv(t, nil)
	terminalpositions.SetAIClient(e.Deps, &scripted{replies: []string{factsJSON}})
	_, tok := e.user(false)
	r := e.postAI("share-facts", tok, map[string]string{"ticker": "amzn"}, nil)
	expectStatus(t, r, 403)
	if !strings.Contains(string(r.Body), "upgrade_required") {
		t.Fatalf("body = %s", r.Body)
	}
}

func TestProPaths(t *testing.T) {
	e := newEnv(t, nil)
	_, tok := e.user(true)
	terminalpositions.SetAIClient(e.Deps, &scripted{replies: []string{factsJSON}})
	r := e.postAI("share-facts", tok, map[string]string{"ticker": "amzn"}, nil)
	expectStatus(t, r, 200)
	var facts terminalpositions.ShareFactsSuggestion
	r.JSON(t, &facts)
	if facts.Ticker != "AMZN" || *facts.SharesOutstanding != 10_600_000_000 {
		t.Fatalf("facts = %s", r.Body)
	}

	terminalpositions.SetAIClient(e.Deps, &scripted{err: &ai.UpstreamError{Status: 402}})
	r = e.postAI("share-facts", tok, map[string]string{"ticker": "amzn"}, nil)
	expectStatus(t, r, 503)
	if reason(t, r) != "AI lookup unavailable. Try again later or enter the numbers yourself." {
		t.Fatalf("reason = %s", r.Body)
	}

	terminalpositions.SetAIClient(e.Deps, nil)
	expectStatus(t, e.postAI("share-facts", tok, map[string]string{"ticker": "amzn"}, nil), 503)
	if _, ok := terminalpositions.AIClient(e.Deps); ok {
		t.Fatal("client should be unavailable")
	}

	terminalpositions.SetAIClient(e.Deps, &scripted{replies: []string{"nothing useful"}})
	expectStatus(t, e.postAI("share-facts", tok, map[string]string{"ticker": "amzn"}, nil), 422)
	expectStatus(t, e.postAI("share-facts", tok, map[string]string{"ticker": "$$"}, nil), 422)
}

func TestNoProviderKeysIs503(t *testing.T) {
	e := newEnv(t, map[string]string{})
	_, tok := e.user(true)
	if _, ok := terminalpositions.AIClient(e.Deps); ok {
		t.Fatal("no keys should mean no client")
	}
	expectStatus(t, e.postAI("scenario", tok, map[string]any{"ticker": "sofi"}, nil), 503)
}

func TestScenarioClampsHorizon(t *testing.T) {
	e := newEnv(t, nil)
	_, tok := e.user(true)
	terminalpositions.SetAIClient(e.Deps, &scripted{replies: []string{scenarioJSON}})
	for _, c := range []struct{ in, want int }{{5, 5}, {99, 30}, {0, 1}} {
		r := e.postAI("scenario", tok, map[string]any{"ticker": "sofi", "horizonYears": c.in}, nil)
		expectStatus(t, r, 200)
		var sc terminalpositions.ScenarioSuggestion
		r.JSON(t, &sc)
		if sc.Ticker != "SOFI" || sc.HorizonYears != c.want || sc.TerminalMarketCap != 150_000_000_000 {
			t.Fatalf("%d: %s", c.in, r.Body)
		}
	}
	r := e.postAI("scenario", tok, map[string]any{"ticker": "sofi"}, nil)
	var sc terminalpositions.ScenarioSuggestion
	r.JSON(t, &sc)
	if sc.HorizonYears != 10 {
		t.Fatalf("default horizon: %s", r.Body)
	}
}

func TestPromptsNameUserCurrency(t *testing.T) {
	e := newEnv(t, nil)
	uid, tok := e.user(true)
	e.exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at, base_currency) VALUES ($1, $2, 'Euro', true, now(), 'EUR')`, uuid.New(), uid)
	client := &scripted{replies: []string{scenarioJSON}}
	terminalpositions.SetAIClient(e.Deps, client)
	expectStatus(t, e.postAI("scenario", tok, map[string]any{"ticker": "sofi", "horizonYears": 10}, nil), 200)
	want := "Ticker: SOFI. Horizon: 10 years. Currency: EUR (give terminalMarketCap in EUR)."
	if len(client.users) != 1 || client.users[0] != want {
		t.Fatalf("users = %q", client.users)
	}
}

const (
	legacyUA  = "Norviq/96 CFNetwork/1568.100.1 Darwin/24.0.0"
	currentUA = "Norviq/97 CFNetwork/1568.100.1 Darwin/24.0.0"
)

func TestConsentGatesTerminalAI(t *testing.T) {
	e := newEnv(t, map[string]string{"AI_CONSENT_ENFORCED": "true"})
	_, tok := e.user(true)
	client := &scripted{replies: []string{scenarioJSON}}
	terminalpositions.SetAIClient(e.Deps, client)
	for _, route := range []string{"share-facts", "scenario"} {
		r := e.postAI(route, tok, map[string]any{"ticker": "sofi"}, nil)
		expectStatus(t, r, 403)
		if reason(t, r) != "AI_CONSENT_REQUIRED" {
			t.Fatalf("reason = %s", r.Body)
		}
	}
	if len(client.users) != 0 {
		t.Fatal("no prompt may reach the provider without consent")
	}
	expectStatus(t, e.do("POST", "/v1/users/ai-consent", tok, nil), 200)
	expectStatus(t, e.postAI("scenario", tok, map[string]any{"ticker": "sofi"}, nil), 200)
	terminalpositions.SetAIClient(e.Deps, &scripted{replies: []string{factsJSON}})
	expectStatus(t, e.postAI("share-facts", tok, map[string]any{"ticker": "amzn"}, nil), 200)
}

func TestConsentLegacyGrace(t *testing.T) {
	e := newEnv(t, map[string]string{"AI_CONSENT_ENFORCED": "true", "AI_CONSENT_LEGACY_IOS_GRACE": "true"})
	_, tok := e.user(true)
	terminalpositions.SetAIClient(e.Deps, &scripted{replies: []string{factsJSON}})
	p, _ := aiplatform.Platform(e.Deps)
	before := p.Consent.LegacyGraceCount.Load()
	expectStatus(t, e.postAI("share-facts", tok, map[string]any{"ticker": "amzn"}, map[string]string{"User-Agent": legacyUA}), 200)
	if p.Consent.LegacyGraceCount.Load() <= before {
		t.Fatal("legacy grace not counted")
	}
	for _, h := range []map[string]string{
		{"User-Agent": currentUA, "X-StockPlan-Client-Capabilities": "mfa-auth-v1, ai-consent-v1"},
		nil,
	} {
		r := e.postAI("share-facts", tok, map[string]any{"ticker": "amzn"}, h)
		expectStatus(t, r, 403)
	}
}

// Pro and consent are checked before the body is read, as in Vapor.
func TestChecksRunBeforeDecode(t *testing.T) {
	e := newEnv(t, nil)
	_, free := e.user(false)
	r := e.Do("POST", "/v1/terminal-positions/ai/share-facts", free, []byte(`{"nope":1}`))
	expectStatus(t, r, 403)
	_, pro := e.user(true)
	terminalpositions.SetAIClient(e.Deps, &scripted{replies: []string{factsJSON}})
	expectStatus(t, e.Do("POST", "/v1/terminal-positions/ai/share-facts", pro, []byte(`{"nope":1}`)), 400)
}

// The live client against a stub OpenRouter: one chat completion in JSON
// mode with the :online model and no tools.
func TestLiveClientAgainstStubProvider(t *testing.T) {
	var got map[string]any
	var auth string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		content, _ := json.Marshal(factsJSON)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+string(content)+`},"finish_reason":"stop"}]}`)
	}))
	defer stub.Close()

	// app.terminalAIClient is built at startup from AI_PROVIDER's key and
	// base URL.
	t.Setenv("TERMINAL_AI_MODEL", "")
	e := newEnv(t, map[string]string{"AI_PROVIDER": "openrouter", "OPENROUTER_API_KEY": "test-key", "AI_BASE_URL": stub.URL})
	if _, ok := terminalpositions.AIClient(e.Deps); !ok {
		t.Fatal("expected a live client")
	}
	_, tok := e.user(true)
	r := e.postAI("share-facts", tok, map[string]any{"ticker": "amzn"}, nil)
	expectStatus(t, r, 200)
	if auth != "Bearer test-key" || got["model"] != terminalpositions.DefaultAIModel || got["max_tokens"] != float64(1200) {
		t.Fatalf("request = %v auth=%q", got, auth)
	}
	if rf, _ := got["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Fatalf("response_format = %v", got["response_format"])
	}
	if _, ok := got["tools"]; ok {
		t.Fatal("tools must not be sent")
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	user, _ := msgs[1].(map[string]any)
	if user["content"] != "Ticker: AMZN. Currency: USD." {
		t.Fatalf("user message = %v", user["content"])
	}
}

func TestAdvisorExported(t *testing.T) {
	adv := terminalpositions.Advisor{Client: &scripted{err: errors.New("boom")}}
	_, err := adv.ShareFacts(context.Background(), "AMZN", "USD")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 503 {
		t.Fatalf("err = %v", err)
	}
}
