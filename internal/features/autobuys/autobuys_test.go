package autobuys_test

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/autobuys"
	"norviq-api/internal/server"
	"norviq-api/internal/store/autobuysdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func pat(t *testing.T, a *testutil.App, userID uuid.UUID, scopes ...string) string {
	t.Helper()
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := a.Deps.DB.Exec(context.Background(),
		`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		 VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`,
		uuid.New(), userID, auth.SHA256Hex(token), scopes); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMonthlyEquivalent(t *testing.T) {
	pct := 0.04
	cases := []struct {
		amount  float64
		cadence string
		percent *float64
		want    float64
		ok      bool
	}{
		{120, "weekly", nil, 120 * 52.0 / 12, true},
		{120, "biweekly", nil, 260, true},
		{275, "bimonthly", nil, 137.5, true},
		{50, "monthly", nil, 50, true},
		{5000, "percentOfContribution", &pct, 200, true},
		{0, "percentOfContribution", &pct, 0, false},
		{5000, "percentOfContribution", nil, 0, false},
		{-1, "monthly", nil, 0, false},
		{1, "unknown", nil, 0, false},
		{math.MaxFloat64, "weekly", nil, 0, false},
	}
	for i, c := range cases {
		got := autobuys.MonthlyEquivalent(c.amount, c.cadence, c.percent)
		if (got != nil) != c.ok || (got != nil && *got != c.want) {
			t.Errorf("%d: %v", i, got)
		}
	}
	total := autobuys.MonthlyTotal([]autobuysdb.Autobuy{
		{Amount: 275, Cadence: "bimonthly", Active: true},
		{Amount: 999, Cadence: "monthly", Active: false},
		{Amount: 1, Cadence: "someday", Active: true},
	})
	if total != 137.5 {
		t.Fatal(total)
	}
}

func TestAutobuysRoutes(t *testing.T) {
	a := testutil.NewApp(t, []server.Feature{autobuys.Feature{}})
	u := a.CreateUser("autobuys@example.com")
	tok := a.Token(u)

	if r := a.Do("GET", "/v1/autobuys", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatal(r.Status)
	}
	if r := a.Do("GET", "/v1/autobuys", pat(t, a, u, "expenses:read"), nil); r.Status != http.StatusForbidden {
		t.Fatal(r.Status)
	}
	if r := a.Do("DELETE", "/v1/autobuys/"+uuid.NewString(), pat(t, a, u, "planning:read"), nil); r.Status != http.StatusForbidden {
		t.Fatal(r.Status)
	}

	r := a.Do("POST", "/v1/autobuys", tok, map[string]any{"label": " Beat the SPY ", "amount": 50, "cadence": "weekly", "ticker": " spy "})
	if r.Status != http.StatusCreated {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var created autobuys.Response
	r.JSON(t, &created)
	if created.Label != "Beat the SPY" || created.Ticker == nil || *created.Ticker != "SPY" || !created.Active ||
		created.ID != strings.ToUpper(created.ID) || created.CreatedAt != created.UpdatedAt || strings.Contains(string(r.Body), "percent") {
		t.Fatalf("%s", r.Body)
	}

	r = a.Do("PATCH", "/v1/autobuys/"+created.ID, tok, map[string]any{"cadence": "monthly", "clear": []string{"ticker"}})
	var patched autobuys.Response
	r.JSON(t, &patched)
	if r.Status != 200 || patched.MonthlyEquivalent == nil || *patched.MonthlyEquivalent != 50 || patched.Ticker != nil {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var list autobuys.ListResponse
	a.Do("GET", "/v1/autobuys", tok, nil).JSON(t, &list)
	if list.MonthlyTotal != 50 || list.Currency != "USD" || len(list.Autobuys) != 1 {
		t.Fatalf("%+v", list)
	}

	for _, c := range []struct {
		body   map[string]any
		status int
		reason string
	}{
		{map[string]any{"label": "401k", "amount": 5000, "cadence": "percentOfContribution"}, 422, "percentOfContribution needs a percent"},
		{map[string]any{"label": "401k", "amount": 5000, "cadence": "percentOfContribution", "percent": 4}, 422, "percent must be between 0 and 1 (0.04 = 4%)"},
		{map[string]any{"label": "x", "amount": 1, "cadence": "fortnightly"}, 422, "unknown cadence"},
		{map[string]any{"label": "big", "amount": 1e19, "cadence": "weekly"}, 422, "amount is too large"},
		{map[string]any{"label": "neg", "amount": -1, "cadence": "weekly"}, 422, "amount must be zero or more"},
		{map[string]any{"label": "  ", "amount": 1, "cadence": "weekly"}, 422, "label must be 1-80 characters"},
		{map[string]any{"label": "t", "amount": 1, "cadence": "weekly", "ticker": "BAD TICKER"}, 422, "ticker must be 1-12 letters"},
		{map[string]any{"amount": 1, "cadence": "weekly"}, 400, ""},
	} {
		r := a.Do("POST", "/v1/autobuys", tok, c.body)
		if r.Status != c.status || !strings.Contains(string(r.Body), c.reason) {
			t.Errorf("%v: %d %s", c.body, r.Status, r.Body)
		}
	}
	ok := a.Do("POST", "/v1/autobuys", tok, map[string]any{"label": "401k Contributions", "amount": 5000, "cadence": "percentOfContribution", "percent": 0.04})
	var okBody autobuys.Response
	ok.JSON(t, &okBody)
	if okBody.MonthlyEquivalent == nil || *okBody.MonthlyEquivalent != 200 {
		t.Fatalf("%s", ok.Body)
	}
	if r := a.Do("PATCH", "/v1/autobuys/"+okBody.ID, tok, map[string]any{"clear": []string{"percent"}}); r.Status != 422 {
		t.Fatal(r.Status)
	}
	if r := a.Do("PATCH", "/v1/autobuys/"+okBody.ID, tok, map[string]any{"clear": []string{"label", "amount"}}); r.Status != 422 ||
		!strings.Contains(string(r.Body), "cannot clear: amount, label") {
		t.Fatalf("%s", r.Body)
	}
	if r := a.Do("PATCH", "/v1/autobuys/nope", tok, map[string]any{}); r.Status != 400 || !strings.Contains(string(r.Body), "invalid id") {
		t.Fatalf("%s", r.Body)
	}
	other := a.Token(a.CreateUser("autobuys-other@example.com"))
	if r := a.Do("DELETE", "/v1/autobuys/"+created.ID, other, nil); r.Status != 404 || !strings.Contains(string(r.Body), "Autobuy not found.") {
		t.Fatalf("%s", r.Body)
	}
	if r := a.Do("DELETE", "/v1/autobuys/"+created.ID, tok, nil); r.Status != http.StatusNoContent {
		t.Fatal(r.Status)
	}
}
