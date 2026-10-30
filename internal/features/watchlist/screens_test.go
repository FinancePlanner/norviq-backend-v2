package watchlist_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/watchlist"
	"norviq-api/internal/platform/marketdata"

	"github.com/google/uuid"
)

func screenBody(listID string) map[string]any {
	return map[string]any{
		"name": " Cheap ", "watchlistListIds": []string{listID}, "logicalOperator": "all", "alertsEnabled": true,
		"groups": []any{map[string]any{"id": "g1", "logicalOperator": "any", "conditions": []any{
			map[string]any{"id": "c1", "metric": "pe_ratio", "comparison": "less_than", "period": "ttm", "value": 15},
		}}},
	}
}

// stubScreenMarket answers TTM P/E ratios from a map; everything else fails.
type stubScreenMarket struct{ pe map[string]float64 }

func (s *stubScreenMarket) Quote(context.Context, string) (marketdata.QuoteResponse, error) {
	return marketdata.QuoteResponse{}, errors.New("no quotes")
}

func (s *stubScreenMarket) Profile(_ context.Context, symbol string) (marketdata.CompanyProfileResponse, error) {
	name := symbol + " Inc."
	return marketdata.CompanyProfileResponse{Name: &name}, nil
}

func (s *stubScreenMarket) RatiosTTM(_ context.Context, symbol string) ([]marketdata.RatiosTTMResponse, error) {
	v, ok := s.pe[symbol]
	if !ok {
		return nil, errors.New("unknown")
	}
	return []marketdata.RatiosTTMResponse{{Symbol: symbol, PriceToEarningsRatioTTM: &v}}, nil
}

func (s *stubScreenMarket) FinancialGrowth(context.Context, string, *int, *string) ([]marketdata.FinancialGrowthResponse, error) {
	return nil, errors.New("no growth")
}

func (s *stubScreenMarket) Ratios(context.Context, string, *int, *string) ([]marketdata.RatiosResponse, error) {
	return nil, errors.New("no ratios")
}

func TestScreenValidation(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(true)
	var lists []list
	e.Do("GET", "/v1/watchlist/lists", token, nil).JSON(t, &lists)
	listID := lists[0].ID

	r := e.Do("GET", "/v1/watchlist/screens/catalog", token, nil)
	expect(t, r, 200, "")
	var catalog []map[string]any
	r.JSON(t, &catalog)
	if len(catalog) != 11 || catalog[0]["id"] != "price" || catalog[0]["favorableDirection"] != nil || catalog[2]["favorableDirection"] != "lower" {
		t.Fatalf("catalog %s", r.Body)
	}
	r = e.Do("GET", "/v1/watchlist/screens", token, nil)
	if r.Status != 200 || strings.TrimSpace(string(r.Body)) != "[]" {
		t.Fatalf("list %d %s", r.Status, r.Body)
	}
	bad := screenBody(listID)
	bad["name"] = " "
	expect(t, e.Do("POST", "/v1/watchlist/screens", token, bad), 400, "Invalid screen: invalidName")
	bad = screenBody(listID)
	bad["groups"] = []any{}
	expect(t, e.Do("POST", "/v1/watchlist/screens", token, bad), 400, "Invalid screen: invalidScreen")
	bad = screenBody(listID)
	bad["groups"].([]any)[0].(map[string]any)["conditions"].([]any)[0].(map[string]any)["value"] = nil
	expect(t, e.Do("POST", "/v1/watchlist/screens", token, bad), 400, "Invalid screen: invalidCondition")
	bad = screenBody(listID)
	bad["watchlistListIds"] = []string{"nope"}
	expect(t, e.Do("POST", "/v1/watchlist/screens", token, bad), 400, "Invalid watchlist id.")
	bad = screenBody(uuid.NewString())
	expect(t, e.Do("POST", "/v1/watchlist/screens", token, bad), 404, "Watchlist not found.")
	bad = screenBody(listID)
	bad["groups"].([]any)[0].(map[string]any)["conditions"].([]any)[0].(map[string]any)["metric"] = "revenue_growth"
	expect(t, e.Do("POST", "/v1/watchlist/screens", token, bad), 400, "Unsupported metric, period, or comparison.")
	bad = screenBody(listID)
	bad["logicalOperator"] = "bogus"
	expect(t, e.Do("POST", "/v1/watchlist/screens", token, bad), 400,
		"Data corrupted at path 'logicalOperator'. Cannot initialize ScreenLogicalOperator from invalid String value bogus.")
	expect(t, e.Do("GET", "/v1/watchlist/screens/nope", token, nil), 404, "Not Found")
	expect(t, e.Do("GET", "/v1/watchlist/screens/"+uuid.NewString(), token, nil), 404, "Not Found")
}

type screenJSON struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	WatchlistListIDs []string `json:"watchlistListIds"`
	LogicalOperator  string   `json:"logicalOperator"`
	AlertsEnabled    bool     `json:"alertsEnabled"`
	Groups           []struct {
		ID         string `json:"id"`
		Conditions []struct {
			Metric string   `json:"metric"`
			Value  *float64 `json:"value"`
		} `json:"conditions"`
	} `json:"groups"`
	LastEvaluatedAt *string `json:"lastEvaluatedAt"`
	CreatedAt       *string `json:"createdAt"`
}

type evaluationJSON struct {
	ID              string `json:"id"`
	ScreenID        string `json:"screenId"`
	SymbolCount     int    `json:"symbolCount"`
	IsAlertBaseline bool   `json:"isAlertBaseline"`
	Matches         []struct {
		Symbol           string  `json:"symbol"`
		Name             *string `json:"name"`
		IsNew            bool    `json:"isNew"`
		ConditionResults []struct {
			ConditionID string   `json:"conditionId"`
			Matched     bool     `json:"matched"`
			Value       *float64 `json:"value"`
		} `json:"conditionResults"`
	} `json:"matches"`
}

// Regression: in Vapor 37128c4 create/update answered 400 "No such key
// 'values'" without saving, every stored screen answered 400 "No such key
// 'groups'", and evaluation was unreachable.
func TestScreensPersistAndEvaluate(t *testing.T) {
	market := &stubScreenMarket{pe: map[string]float64{"AAPL": 10, "MSFT": 30}}
	defer watchlist.SetScreenMarket(market)()
	e := newEnv(t)
	userID, token := e.user(true)
	var lists []list
	e.Do("GET", "/v1/watchlist/lists", token, nil).JSON(t, &lists)
	listID := lists[0].ID
	for _, sym := range []string{"aapl", "MSFT"} {
		e.exec(`INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id) VALUES ($1, $2, $3, now(), 'active', $4)`,
			uuid.New(), userID, sym, listID)
	}

	r := e.Do("POST", "/v1/watchlist/screens", token, screenBody(listID))
	expect(t, r, 200, "")
	var created screenJSON
	r.JSON(t, &created)
	if created.Name != "Cheap" || created.LogicalOperator != "all" || !created.AlertsEnabled || len(created.Groups) != 1 ||
		len(created.Groups[0].Conditions) != 1 || created.Groups[0].Conditions[0].Value == nil ||
		*created.Groups[0].Conditions[0].Value != 15 || !strings.EqualFold(created.WatchlistListIDs[0], listID) ||
		created.CreatedAt == nil || created.LastEvaluatedAt != nil {
		t.Fatalf("created %s", r.Body)
	}
	var stored string
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT groups::text FROM watchlist_screens WHERE user_id = $1`, userID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, `"values"`) || !strings.Contains(stored, `"logical_operator"`) {
		t.Fatalf("stored groups %s", stored)
	}
	one := "/v1/watchlist/screens/" + created.ID
	g := e.Do("GET", one, token, nil)
	if g.Status != 200 || string(g.Body) != string(r.Body) {
		t.Fatalf("get:\n%s\nwant\n%s", g.Body, r.Body)
	}
	if l := e.Do("GET", "/v1/watchlist/screens", token, nil); l.Status != 200 || string(l.Body) != "["+strings.TrimSpace(string(r.Body))+"]" {
		t.Fatalf("list %s", l.Body)
	}

	// First evaluation: the baseline. AAPL (P/E 10) matches "< 15".
	r = e.Do("POST", one+"/evaluate", token, nil)
	expect(t, r, 200, "")
	var first evaluationJSON
	r.JSON(t, &first)
	if first.SymbolCount != 2 || !first.IsAlertBaseline || len(first.Matches) != 1 || first.Matches[0].Symbol != "AAPL" ||
		first.Matches[0].IsNew || first.Matches[0].Name == nil || *first.Matches[0].Name != "AAPL Inc." ||
		len(first.Matches[0].ConditionResults) != 1 || !first.Matches[0].ConditionResults[0].Matched ||
		*first.Matches[0].ConditionResults[0].Value != 10 {
		t.Fatalf("first evaluation %s", r.Body)
	}
	// MSFT drops to 12: a second evaluation flags it as new.
	market.pe["MSFT"] = 12
	r = e.Do("POST", one+"/evaluate", token, nil)
	var second evaluationJSON
	r.JSON(t, &second)
	if second.IsAlertBaseline || len(second.Matches) != 2 || second.Matches[0].IsNew || !second.Matches[1].IsNew {
		t.Fatalf("second evaluation %s", r.Body)
	}
	h := e.Do("GET", one+"/history", token, nil)
	var history []evaluationJSON
	h.JSON(t, &history)
	if h.Status != 200 || len(history) != 2 || history[0].ID != second.ID || len(history[0].Matches) != 2 || !history[0].Matches[1].IsNew {
		t.Fatalf("history %s", h.Body)
	}
	var got screenJSON
	e.Do("GET", one, token, nil).JSON(t, &got)
	if got.LastEvaluatedAt == nil {
		t.Fatal("lastEvaluatedAt not set")
	}

	// Update: saved, evaluations cleared, lastEvaluatedAt reset.
	body := screenBody(listID)
	body["name"] = "Cheaper"
	body["groups"].([]any)[0].(map[string]any)["conditions"].([]any)[0].(map[string]any)["value"] = 11
	r = e.Do("PUT", one, token, body)
	var updated screenJSON
	r.JSON(t, &updated)
	if r.Status != 200 || updated.Name != "Cheaper" || *updated.Groups[0].Conditions[0].Value != 11 || updated.LastEvaluatedAt != nil {
		t.Fatalf("update %d %s", r.Status, r.Body)
	}
	h = e.Do("GET", one+"/history", token, nil)
	if strings.TrimSpace(string(h.Body)) != "[]" {
		t.Fatalf("history after update %s", h.Body)
	}

	// A row whose jsonb is not ScenarioJSON-shaped is still a 500.
	unwrapped := uuid.New()
	e.exec(`INSERT INTO watchlist_screens (id, user_id, name, watchlist_list_ids, logical_operator, groups, alerts_enabled, created_at, updated_at)
		VALUES ($1, $2, 'S', ARRAY[$3::uuid], 'all', '{"groups": []}', false, now(), now())`, unwrapped, userID, listID)
	expect(t, e.Do("GET", "/v1/watchlist/screens/"+unwrapped.String(), token, nil), 500, "")
	e.exec(`DELETE FROM watchlist_screens WHERE id = $1`, unwrapped)

	expect(t, e.Do("DELETE", one, token, nil), 204, "")
	expect(t, e.Do("GET", one, token, nil), 404, "")
}

// The daily job's screen pass alerts on symbols that newly match.
func TestScreenJobAlertsNewMatches(t *testing.T) {
	market := &stubScreenMarket{pe: map[string]float64{"AAPL": 10, "MSFT": 30}}
	e := newEnv(t)
	userID, token := e.user(true)
	var lists []list
	e.Do("GET", "/v1/watchlist/lists", token, nil).JSON(t, &lists)
	listID := lists[0].ID
	e.exec(`INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id) VALUES ($1, $2, 'AAPL', now(), 'active', $3)`, uuid.New(), userID, listID)
	e.exec(`INSERT INTO watchlist_items (id, user_id, symbol, created_at, status, watchlist_list_id) VALUES ($1, $2, 'MSFT', now(), 'active', $3)`, uuid.New(), userID, listID)
	defer watchlist.SetScreenMarket(market)()
	r := e.Do("POST", "/v1/watchlist/screens", token, screenBody(listID))
	var created screenJSON
	r.JSON(t, &created)
	screenID := uuid.MustParse(created.ID)

	job, err := watchlist.NewScreenJob(e.Deps, market, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	count := func() int {
		var n int
		if err := e.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM notification_events WHERE user_id = $1 AND kind = 'watchlist_screen'`, userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := job.Evaluate(ctx, screenID, userID); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("baseline alerted: %d", n)
	}
	market.pe["MSFT"] = 12
	if err := job.Evaluate(ctx, screenID, userID); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("alerts %d", n)
	}
	var body string
	if err := e.Deps.DB.QueryRow(ctx, `SELECT body FROM notification_events WHERE user_id = $1 AND kind = 'watchlist_screen'`, userID).Scan(&body); err != nil || body != "MSFT entered Cheap." {
		t.Fatalf("body %q %v", body, err)
	}
}

func TestScreensNeedPremium(t *testing.T) {
	e := newEnv(t)
	_, token := e.user(false)
	r := e.Do("GET", "/v1/watchlist/screens/catalog", token, nil)
	expect(t, r, 403, "")
	if !strings.Contains(string(r.Body), "smart_screening") {
		t.Fatalf("body %s", r.Body)
	}
}
