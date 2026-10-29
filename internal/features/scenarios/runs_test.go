package scenarios_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/scenarios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/scenariosdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ported from ScenarioControllerTests.swift (scenario and run routes), plus
// the error paths, the worker and the retention job.

type runJSON struct {
	ID                string          `json:"id"`
	State             string          `json:"state"`
	Progress          float64         `json:"progress"`
	Seed              string          `json:"seed"`
	DeduplicationHash string          `json:"deduplicationHash"`
	Scenario          map[string]any  `json:"scenario"`
	Result            json.RawMessage `json:"result"`
	ErrorMessage      *string         `json:"errorMessage"`
	CompletedAt       *string         `json:"completedAt"`
	ExpiresAt         *string         `json:"expiresAt"`
	EngineVersion     string          `json:"engineVersion"`
	CatalogVersion    string          `json:"catalogVersion"`
}

func (e *env) snapshot(owner, list uuid.UUID, payload map[string]any) string {
	e.t.Helper()
	r := e.Do("POST", "/v1/portfolio/scenario-snapshots", e.Token(owner), map[string]any{
		"portfolioListId": list, "baseCurrency": "USD", "valuationTimestamp": "2026-10-28T12:00:00Z",
		"payload": map[string]any{"values": payload}, "warnings": map[string]any{"values": map[string]any{}},
	})
	if r.Status != 200 {
		e.t.Fatalf("snapshot %d %s", r.Status, r.Body)
	}
	var out struct{ ID string }
	r.JSON(e.t, &out)
	return out.ID
}

func (e *env) scenario(owner uuid.UUID, body map[string]any) map[string]any {
	e.t.Helper()
	r := e.Do("POST", "/v1/scenarios", e.Token(owner), body)
	if r.Status != 200 {
		e.t.Fatalf("scenario %d %s", r.Status, r.Body)
	}
	var out map[string]any
	r.JSON(e.t, &out)
	return out
}

func (e *env) createRun(owner uuid.UUID, scenarioID, snapshotID string, seed uint64, status int) runJSON {
	e.t.Helper()
	r := e.Do("POST", "/v1/scenarios/"+scenarioID+"/runs", e.Token(owner), map[string]any{"snapshotId": snapshotID, "seed": seed})
	if r.Status != status {
		e.t.Fatalf("run %d %s", r.Status, r.Body)
	}
	var out runJSON
	r.JSON(e.t, &out)
	return out
}

func (e *env) worker() *scenarios.RunWorker {
	return scenarios.NewRunWorker(e.Deps, scenarios.WorkerSettings{MaxConcurrent: "1"})
}

func (e *env) getRun(owner uuid.UUID, id string) runJSON {
	e.t.Helper()
	var out runJSON
	r := e.Do("GET", "/v1/scenario-runs/"+id, e.Token(owner), nil)
	if r.Status != 200 {
		e.t.Fatalf("get run %d %s", r.Status, r.Body)
	}
	r.JSON(e.t, &out)
	return out
}

func (e *env) goal(owner, list uuid.UUID, target float64, targetDate time.Time, monthly, growth, inflation float64) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO financial_goals (id, user_id, portfolio_list_id, name, target_amount, target_date, base_currency,
		monthly_contribution, annual_contribution_growth, inflation_assumption, created_at, updated_at)
		VALUES ($1, $2, $3, 'Goal', $4, $5, 'USD', $6, $7, $8, now(), now())`, id, owner, list, target, targetDate, monthly, growth, inflation)
	return id
}

func TestScenarioRoutesRequireFlagAndPro(t *testing.T) {
	e := newEnv(t, true)
	free := e.CreateUser("free@example.com")
	for _, path := range []string{"/v1/scenarios/catalog", "/v1/scenarios", "/v1/scenario-runs"} {
		r := e.Do("GET", path, e.Token(free), nil)
		if r.Status != 403 || !strings.Contains(string(r.Body), "scenario_planning") {
			t.Fatalf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	t.Setenv("SCENARIO_PLANNING_ENABLED", "false")
	pro := e.proUser("pro@example.com")
	if r := e.Do("GET", "/v1/scenarios/catalog", e.Token(pro), nil); r.Status != 404 {
		t.Fatalf("flag off: %d", r.Status)
	}
}

func TestScenarioCatalog(t *testing.T) {
	e := newEnv(t, true)
	pro := e.proUser("pro@example.com")
	r := e.Do("GET", "/v1/scenarios/catalog", e.Token(pro), nil)
	var out scenarios.CatalogResponse
	r.JSON(t, &out)
	if r.Status != 200 || out.Version != "2026.2" || len(out.HistoricalScenarios) != 5 || out.HistoricalScenarios[2].ID != "covid_crash" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestScenarioCRUDAndValidation(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("owner@example.com")
	other := e.proUser("other@example.com")
	list := e.portfolio(owner)
	otherList := e.portfolio(owner)
	tok := e.Token(owner)

	created := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "Shock", "kind": "custom",
		"configuration": map[string]any{"values": map[string]any{"horizon_months": 12, "recovery": "linear"}},
	})
	if created["isSaved"] != true || created["financialGoalId"] != nil || created["kind"] != "custom" ||
		created["portfolioListId"] != strings.ToUpper(list.String()) || created["createdAt"] == nil {
		t.Fatalf("created %v", created)
	}
	if _, ok := created["financialGoalId"]; !ok {
		t.Fatal("financialGoalId must be encoded as null")
	}
	id := created["id"].(string)
	var list1 []map[string]any
	e.Do("GET", "/v1/scenarios", tok, nil).JSON(t, &list1)
	if len(list1) != 1 || list1[0]["id"] != id {
		t.Fatalf("list %v", list1)
	}
	if r := e.Do("GET", "/v1/scenarios/"+id, e.Token(other), nil); r.Status != 404 {
		t.Fatalf("stranger get %d", r.Status)
	}
	if r := e.Do("GET", "/v1/scenarios/nope", tok, nil); r.Status != 400 || !strings.Contains(string(r.Body), "Invalid id") {
		t.Fatalf("bad id %d %s", r.Status, r.Body)
	}
	r := e.Do("PUT", "/v1/scenarios/"+id, tok, map[string]any{
		"portfolio_list_id": list, "name": "Renamed", "kind": "historical", "is_saved": false,
		"configuration": map[string]any{"values": map[string]any{"catalog_id": "covid_crash"}},
	})
	var updated map[string]any
	r.JSON(t, &updated)
	if r.Status != 200 || updated["name"] != "Renamed" || updated["isSaved"] != false || updated["id"] != id {
		t.Fatalf("update %d %s", r.Status, r.Body)
	}

	gid := e.goal(owner, list, 1000, time.Now().AddDate(2, 0, 0), 10, 0, 0.02)
	bad := []struct {
		body   map[string]any
		status int
		reason string
	}{
		{map[string]any{"portfolioListId": list, "name": "x", "kind": "other", "configuration": map[string]any{"values": map[string]any{}}}, 400, "Invalid scenario"},
		{map[string]any{"portfolioListId": list, "name": "  ", "kind": "custom", "configuration": map[string]any{"values": map[string]any{}}}, 400, "Invalid scenario"},
		{map[string]any{"portfolioListId": list, "name": "x", "kind": "historical", "configuration": map[string]any{"values": map[string]any{"catalogId": "crash_of_1929"}}}, 400, "Unknown historical scenario"},
		{map[string]any{"portfolioListId": list, "name": "x", "kind": "custom", "configuration": map[string]any{"values": map[string]any{"horizon_months": 1.5}}}, 400, "Invalid custom scenario configuration"},
		{map[string]any{"portfolioListId": list, "name": "x", "kind": "custom", "configuration": map[string]any{"values": map[string]any{"sector_shocks": []any{map[string]any{"target": " ", "percentage": 0.1}}}}}, 400, "Invalid custom scenario configuration"},
		{map[string]any{"portfolioListId": list, "name": "x", "kind": "monte_carlo", "configuration": map[string]any{"values": map[string]any{"degrees_of_freedom": 2}}}, 400, "Invalid Monte Carlo configuration"},
		{map[string]any{"portfolioListId": list, "name": "x", "kind": "monte_carlo", "configuration": map[string]any{"values": map[string]any{
			"asset_weights": []any{0.5, 0.4}, "asset_annual_returns": []any{0.05, 0.05}, "annual_covariance": []any{[]any{0.04, 0}, []any{0, 0.04}},
		}}}, 400, "Invalid Monte Carlo configuration"},
		{map[string]any{"portfolioListId": list, "name": "x", "kind": "custom"}, 400, "No such key 'configuration'"},
		{map[string]any{"portfolioListId": uuid.New(), "name": "x", "kind": "custom", "configuration": map[string]any{"values": map[string]any{}}}, 404, "Not Found"},
		{map[string]any{"portfolioListId": list, "financialGoalId": uuid.New(), "name": "x", "kind": "custom", "configuration": map[string]any{"values": map[string]any{}}}, 404, "Financial goal not found"},
		{map[string]any{"portfolioListId": otherList, "financialGoalId": gid, "name": "x", "kind": "custom", "configuration": map[string]any{"values": map[string]any{}}}, 422, "Financial goal and scenario must use the same portfolio"},
	}
	for i, c := range bad {
		r := e.Do("POST", "/v1/scenarios", tok, c.body)
		if r.Status != c.status || !strings.Contains(string(r.Body), c.reason) {
			t.Errorf("case %d: %d %s", i, r.Status, r.Body)
		}
	}
	if r := e.Do("PUT", "/v1/scenarios/"+uuid.NewString(), tok, map[string]any{}); r.Status != 404 {
		t.Fatalf("put missing (404 before decoding): %d", r.Status)
	}
	if r := e.Do("DELETE", "/v1/scenarios/"+id, e.Token(other), nil); r.Status != 404 {
		t.Fatalf("stranger delete %d", r.Status)
	}
	if r := e.Do("DELETE", "/v1/scenarios/"+id, tok, nil); r.Status != 204 {
		t.Fatalf("delete %d", r.Status)
	}
	if r := e.Do("GET", "/v1/scenarios/"+id, tok, nil); r.Status != 404 {
		t.Fatalf("after delete %d", r.Status)
	}
}

func TestRunCancellationAndOwnership(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("runner@example.com")
	other := e.proUser("observer@example.com")
	list := e.portfolio(owner)
	otherList := e.portfolio(owner)
	sc := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "Seeded normal", "kind": "monte_carlo", "isSaved": false,
		"configuration": map[string]any{"values": map[string]any{"distribution": "normal", "paths": 10000}},
	})
	snap := e.snapshot(owner, list, map[string]any{"totalValue": 100_000})
	mismatched := e.snapshot(owner, otherList, map[string]any{"totalValue": 100_000})
	sid := sc["id"].(string)
	if r := e.Do("POST", "/v1/scenarios/"+sid+"/runs", e.Token(owner), map[string]any{"snapshotId": mismatched, "seed": 7}); r.Status != 422 ||
		!strings.Contains(string(r.Body), "Snapshot and scenario must use the same portfolio") {
		t.Fatalf("mismatch %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/v1/scenarios/"+sid+"/runs", e.Token(owner), map[string]any{"snapshotId": uuid.New(), "seed": 7}); r.Status != 404 {
		t.Fatalf("missing snapshot %d", r.Status)
	}
	if r := e.Do("POST", "/v1/scenarios/"+sid+"/runs", e.Token(owner), map[string]any{"snapshotId": snap, "seed": -1}); r.Status != 400 {
		t.Fatalf("negative seed %d", r.Status)
	}
	run := e.createRun(owner, sid, snap, 7, 202)
	if run.State != "queued" || run.Seed != "7" || run.ExpiresAt == nil || run.Scenario["id"] != sid ||
		run.EngineVersion != "1.1.0" || run.CatalogVersion != "2026.2" || string(run.Result) != "null" {
		t.Fatalf("run %+v", run)
	}
	if r := e.Do("GET", "/v1/scenario-runs/"+run.ID, e.Token(other), nil); r.Status != 404 {
		t.Fatalf("stranger %d", r.Status)
	}
	if r := e.Do("GET", "/v1/scenario-runs/"+run.ID+"/result", e.Token(owner), nil); r.Status != 409 ||
		!strings.Contains(string(r.Body), "Result is not ready") {
		t.Fatalf("result early %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/v1/scenario-runs/compare", e.Token(owner), map[string]any{"runIds": []string{run.ID}}); r.Status != 409 ||
		!strings.Contains(string(r.Body), "Only completed runs can be compared") {
		t.Fatalf("compare queued %d %s", r.Status, r.Body)
	}
	if r := e.Do("DELETE", "/v1/scenario-runs/"+run.ID, e.Token(owner), nil); r.Status != 204 {
		t.Fatalf("cancel %d", r.Status)
	}
	stopped := e.getRun(owner, run.ID)
	if stopped.State != stateCanceledValue || stopped.CompletedAt == nil {
		t.Fatalf("stopped %+v", stopped)
	}
	if r := e.Do("DELETE", "/v1/scenario-runs/"+run.ID, e.Token(owner), nil); r.Status != 409 ||
		!strings.Contains(string(r.Body), "Run is already terminal") {
		t.Fatalf("cancel again %d %s", r.Status, r.Body)
	}
	// The worker leaves canceled runs alone.
	e.worker().RunOnce(context.Background())
	if got := e.getRun(owner, run.ID); got.State != stateCanceledValue {
		t.Fatalf("after worker %+v", got)
	}
}

func TestLinkedGoalDrivesMonteCarlo(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("goalworker@example.com")
	list := e.portfolio(owner)
	otherList := e.portfolio(owner)
	gid := e.goal(owner, list, 125_000, time.Now().AddDate(0, 18, 0), 750, 0.04, 0.025)
	if r := e.Do("POST", "/v1/scenarios", e.Token(owner), map[string]any{
		"portfolioListId": otherList, "financialGoalId": gid, "name": "Mismatched goal", "kind": "monte_carlo",
		"configuration": map[string]any{"values": map[string]any{"distribution": "normal"}}, "isSaved": true,
	}); r.Status != 422 {
		t.Fatalf("mismatch %d", r.Status)
	}
	sc := e.scenario(owner, map[string]any{
		"portfolioListId": list, "financialGoalId": gid, "name": "Goal simulation", "kind": "monte_carlo", "isSaved": true,
		"configuration": map[string]any{"values": map[string]any{
			"distribution": "normal", "path_count": 200, "horizon_months": 360, "annual_return": 0.06, "annual_volatility": 0.12,
		}},
	})
	snap := e.snapshot(owner, list, map[string]any{"total_value": 100_000, "holdings": []any{}})
	run := e.createRun(owner, sc["id"].(string), snap, 123, 202)
	if run.ExpiresAt != nil {
		t.Fatal("saved scenarios' runs do not expire")
	}

	e.worker().RunOnce(context.Background())
	completed := e.getRun(owner, run.ID)
	if completed.State != "completed" || completed.Progress != 1 || completed.CompletedAt == nil {
		t.Fatalf("completed %+v", completed)
	}
	var result struct {
		Values map[string]any `json:"values"`
	}
	if err := json.Unmarshal(completed.Result, &result); err != nil {
		t.Fatal(err)
	}
	v := result.Values
	if v["goal_probability"] == nil || v["horizon_months"] != 360.0 || v["path_count"] != 200.0 {
		t.Fatalf("result %v", v)
	}
	a := v["assumptions"].(map[string]any)
	if a["monthly_contribution"] != 750.0 || a["annual_contribution_growth"] != 0.04 || a["inflation"] != 0.025 ||
		a["financial_goal_id"] != strings.ToUpper(gid.String()) {
		t.Fatalf("assumptions %v", a)
	}

	r := e.Do("GET", "/v1/scenario-runs/"+run.ID+"/result", e.Token(owner), nil)
	var fetched struct {
		Values map[string]any `json:"values"`
	}
	r.JSON(t, &fetched)
	if r.Status != 200 || fetched.Values["path_count"] != 200.0 {
		t.Fatalf("result %d %s", r.Status, r.Body)
	}

	var cmp scenarios.ComparisonResponse
	r = e.Do("POST", "/v1/scenario-runs/compare", e.Token(owner), map[string]any{"runIds": []string{run.ID}})
	if r.Status != 200 {
		t.Fatalf("compare %d %s", r.Status, r.Body)
	}
	r.JSON(t, &cmp)
	if len(cmp.Series) != 1 || len(cmp.Runs) != 1 || cmp.Series[0].Points[0].ElapsedDays != 0 ||
		cmp.Series[0].Points[0].PercentageChange != 0 || cmp.Series[0].Points[len(cmp.Series[0].Points)-1].ElapsedDays <= 0 {
		t.Fatalf("compare %s", r.Body)
	}
	if last := cmp.Series[0].Points[360]; last.ElapsedDays != 360*30.4375 {
		t.Fatalf("last point %+v", last)
	}

	duplicate := e.createRun(owner, sc["id"].(string), snap, 123, 200)
	if duplicate.ID != run.ID || duplicate.DeduplicationHash != run.DeduplicationHash {
		t.Fatalf("duplicate %+v", duplicate)
	}
	for _, body := range []map[string]any{
		{"runIds": []string{}},
		{"runIds": []string{run.ID, run.ID, run.ID, run.ID, run.ID}},
	} {
		if r := e.Do("POST", "/v1/scenario-runs/compare", e.Token(owner), body); r.Status != 400 ||
			!strings.Contains(string(r.Body), "Compare between one and four runs") {
			t.Fatalf("compare count %d %s", r.Status, r.Body)
		}
	}
	if r := e.Do("POST", "/v1/scenario-runs/compare", e.Token(owner), map[string]any{"runIds": []string{run.ID, uuid.NewString()}}); r.Status != 404 {
		t.Fatalf("compare missing %d", r.Status)
	}
	// Duplicate ids are fine: one fetched row per distinct id.
	r = e.Do("POST", "/v1/scenario-runs/compare", e.Token(owner), map[string]any{"runIds": []string{run.ID, run.ID}})
	r.JSON(t, &cmp)
	if r.Status != 200 || len(cmp.Runs) != 2 || len(cmp.Series) != 2 {
		t.Fatalf("compare dup %d", r.Status)
	}
	var runs []runJSON
	e.Do("GET", "/v1/scenario-runs", e.Token(owner), nil).JSON(t, &runs)
	if len(runs) != 1 {
		t.Fatalf("runs %d", len(runs))
	}
}

// Seeds adjusted closes for one instrument.
func (e *env) bars(symbol string, points map[string]float64) {
	for date, close := range points {
		e.exec(`INSERT INTO market_price_bars (instrument_key, date, open, high, low, close, adjusted_close, currency, provider)
			VALUES ($1, $2::date, $3, $3, $3, $3, $3, 'USD', 'test')`, symbol, date, close)
	}
}

func TestHistoricalAndCustomRuns(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("hist@example.com")
	list := e.portfolio(owner)
	e.bars("AAPL", map[string]float64{"2020-02-19": 100, "2020-03-02": 90, "2020-03-23": 70, "2020-03-24": 1})
	e.bars("SPY", map[string]float64{"2020-02-19": 200, "2020-03-23": 150})
	payload := map[string]any{
		"total_value": 3000, "base_currency": "USD",
		"holdings": []any{
			map[string]any{"id": "h1", "instrument_key": "aapl", "value_in_base_currency": 1000},
			map[string]any{"id": "h2", "instrument_key": "XYZ", "benchmark_proxy": "SPY", "value_in_base_currency": 1000},
			map[string]any{"id": "h3", "instrument_key": "NONE", "value_in_base_currency": 1000},
		},
	}
	snap := e.snapshot(owner, list, payload)
	hist := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "Covid", "kind": "historical",
		"configuration": map[string]any{"values": map[string]any{"catalogId": "covid_crash", "monthly_spending": 1000}},
	})
	custom := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "Custom", "kind": "custom",
		"configuration": map[string]any{"values": map[string]any{
			"asset_class_shocks": []any{map[string]any{"target": "stock", "percentage": -0.2}},
			"horizon_months":     6, "recovery": "mean_reverting",
		}},
	})
	hr := e.createRun(owner, hist["id"].(string), snap, 1, 202)
	cr := e.createRun(owner, custom["id"].(string), snap, 1, 202)
	w := e.worker()
	w.RunOnce(context.Background())
	w.RunOnce(context.Background())

	var res struct {
		Values map[string]any `json:"values"`
	}
	got := e.getRun(owner, hr.ID)
	if got.State != "completed" {
		t.Fatalf("historical %+v %v", got, got.ErrorMessage)
	}
	if err := json.Unmarshal(got.Result, &res); err != nil {
		t.Fatal(err)
	}
	v := res.Values
	// h1: 1000 * 70/100, h2 via SPY: 1000 * 150/200, h3: no history.
	if v["ending_value"] != 700.0+750+1000 || v["catalog_id"] != "covid_crash" || v["catalog_version"] != "2026.2" {
		t.Fatalf("historical %v", v)
	}
	warnings := v["warnings"].([]any)
	if len(warnings) != 2 || warnings[0].(map[string]any)["code"] != "proxy_used" ||
		warnings[0].(map[string]any)["message"] != "Used benchmark proxy SPY." || warnings[1].(map[string]any)["code"] != "missing_history" {
		t.Fatalf("warnings %v", warnings)
	}
	timeline := v["timeline"].([]any)
	if len(timeline) != 3 {
		t.Fatalf("timeline %v", timeline)
	}
	mid := timeline[1].(map[string]any)
	// 2020-03-02: AAPL 90, SPY still at its first bar.
	if mid["date"] != "2020-03-02" || mid["elapsed_days"] != 12.0 || mid["value"] != 900.0+1000+1000 {
		t.Fatalf("mid %v", mid)
	}
	if a := v["assumptions"].(map[string]any); a["valuation_currency"] != "USD" || a["monthly_spending"] != 1000.0 || a["financial_goal_id"] != nil {
		t.Fatalf("assumptions %v", a)
	}
	if v["goal_delay_months"] != nil || v["recovery_months"] != nil {
		t.Fatalf("impact %v", v)
	}

	got = e.getRun(owner, cr.ID)
	if got.State != "completed" {
		t.Fatalf("custom %+v", got)
	}
	res.Values = nil
	if err := json.Unmarshal(got.Result, &res); err != nil {
		t.Fatal(err)
	}
	want, err := scenarios.Processor{}.Custom(map[string]any{"holdings": jsonRoundTrip(t, payload["holdings"])}, map[string]any{
		"asset_class_shocks": []any{map[string]any{"target": "stock", "percentage": -0.2}},
		"horizon_months":     6.0, "recovery": "mean_reverting",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(res.Values)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatalf("custom result\n got %s\nwant %s", gotJSON, wantJSON)
	}

	// Compare normalizes historical timelines by elapsed_days and custom
	// ones by elapsed_months * 30.4375.
	var cmp scenarios.ComparisonResponse
	r := e.Do("POST", "/v1/scenario-runs/compare", e.Token(owner), map[string]any{"runIds": []string{cr.ID, hr.ID}})
	r.JSON(t, &cmp)
	if r.Status != 200 || cmp.Series[0].RunID.String() != strings.ToUpper(cr.ID) || cmp.Series[0].Points[1].ElapsedDays != 30.4375 ||
		cmp.Series[1].Points[1].ElapsedDays != 12 {
		t.Fatalf("compare %d %s", r.Status, r.Body)
	}
}

func jsonRoundTrip(t *testing.T, v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWorkerFailsRunWithSwiftErrorDescription(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("fail@example.com")
	list := e.portfolio(owner)
	sc := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "MC", "kind": "monte_carlo",
		"configuration": map[string]any{"values": map[string]any{"distribution": "block_bootstrap"}},
	})
	empty := e.snapshot(owner, list, map[string]any{"total_value": 0})
	noBars := e.snapshot(owner, list, map[string]any{"total_value": 10, "holdings": []any{
		map[string]any{"id": "h", "instrument_key": "NOPE", "value_in_base_currency": 10},
	}})
	a := e.createRun(owner, sc["id"].(string), empty, 1, 202)
	b := e.createRun(owner, sc["id"].(string), noBars, 1, 202)
	w := e.worker()
	w.RunOnce(context.Background())
	w.RunOnce(context.Background())
	for id, want := range map[string]string{
		a.ID: "Abort.422: Snapshot total_value must be positive",
		// Vapor traps on `1 ..< 0` here (process crash); v2 fails the run.
		b.ID: "Fatal error: Range requires lowerBound <= upperBound",
	} {
		got := e.getRun(owner, id)
		if got.State != "failed" || got.ErrorMessage == nil || *got.ErrorMessage != want || got.CompletedAt == nil {
			t.Errorf("run %s: %+v %v", id, got, got.ErrorMessage)
		}
	}
}

func TestWorkerStopsWhenRunIsCanceledMidway(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("midway@example.com")
	list := e.portfolio(owner)
	sc := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "Big", "kind": "monte_carlo",
		"configuration": map[string]any{"values": map[string]any{"distribution": "normal", "path_count": 50000, "horizon_months": 600}},
	})
	snap := e.snapshot(owner, list, map[string]any{"total_value": 1000})
	run := e.createRun(owner, sc["id"].(string), snap, 1, 202)
	w := scenarios.NewRunWorker(e.Deps, scenarios.WorkerSettings{})
	scenarios.SetHeartbeat(w, 20*time.Millisecond)
	done := make(chan struct{})
	go func() { w.RunOnce(context.Background()); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for e.getRun(owner, run.ID).State != "running" {
		if time.Now().After(deadline) {
			t.Fatal("never claimed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r := e.Do("DELETE", "/v1/scenario-runs/"+run.ID, e.Token(owner), nil); r.Status != 204 {
		t.Fatalf("cancel %d", r.Status)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("simulation was not canceled")
	}
	if got := e.getRun(owner, run.ID); got.State != stateCanceledValue || string(got.Result) != "null" {
		t.Fatalf("run %+v", got)
	}
}

func TestRetentionJob(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("retention@example.com")
	list := e.portfolio(owner)
	unsavedEmpty := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "a", "kind": "custom", "isSaved": false,
		"configuration": map[string]any{"values": map[string]any{}},
	})
	unsavedWithRun := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "b", "kind": "custom", "isSaved": false,
		"configuration": map[string]any{"values": map[string]any{}},
	})
	saved := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "c", "kind": "custom",
		"configuration": map[string]any{"values": map[string]any{}},
	})
	snap := e.snapshot(owner, list, map[string]any{"total_value": 10})
	expired := e.createRun(owner, unsavedWithRun["id"].(string), snap, 1, 202)
	queuedExpired := e.createRun(owner, unsavedWithRun["id"].(string), snap, 2, 202)
	e.exec(`UPDATE scenario_runs SET expires_at = now() - interval '1 day'`)
	e.exec(`UPDATE scenario_runs SET state = 'completed' WHERE id = $1`, expired.ID)

	scenarios.RetentionOnce(context.Background(), scenariosdb.New(e.Deps.DB), e.Deps.Logger, time.Now())

	var ids []string
	rows, err := e.Deps.DB.Query(context.Background(), `SELECT id::text FROM scenario_runs UNION ALL SELECT id::text FROM scenario_definitions ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, strings.ToUpper(id))
	}
	got := strings.Join(ids, ",")
	for _, keep := range []string{queuedExpired.ID, unsavedWithRun["id"].(string), saved["id"].(string)} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s deleted", keep)
		}
	}
	for _, gone := range []string{expired.ID, unsavedEmpty["id"].(string)} {
		if strings.Contains(got, gone) {
			t.Errorf("%s kept", gone)
		}
	}
}

func TestJobsAreNoOpsWithFlagOff(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	scenarios.RunWorkerJob{Settings: &scenarios.WorkerSettings{ScenarioPlanningEnabled: "false"}}.Run(ctx, app.Deps)
	scenarios.RetentionJob{Settings: &scenarios.RetentionSettings{}}.Run(ctx, app.Deps)
	if time.Since(start) > time.Second {
		t.Fatal("jobs did not return immediately")
	}
	if fmt.Sprintln(scenarios.RunWorkerJob{}.Name(), scenarios.RetentionJob{}.Name()) != "scenario_run_worker scenario_retention\n" {
		t.Fatal("names")
	}
}

// stateCanceledValue is scenario_runs.state for a canceled run.
const stateCanceledValue = "cancelled" //nolint:misspell // database and wire value
