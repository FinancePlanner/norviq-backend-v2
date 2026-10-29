package assets_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/assets"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type fakeSearch struct {
	results []assets.SearchResult
	err     error
	queries []string
}

func (f *fakeSearch) Search(_ context.Context, q string) ([]assets.SearchResult, error) {
	f.queries = append(f.queries, q)
	return f.results, f.err
}

func newApp(t *testing.T, s *fakeSearch) *testutil.App {
	t.Helper()
	return testutil.NewApp(t, []server.Feature{assets.Feature{Search: func(*server.Deps) (assets.SymbolSearch, error) { return s, nil }}})
}

func addStock(t *testing.T, app *testutil.App, user uuid.UUID, symbol string, notes *string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	list := uuid.New()
	if _, err := app.Deps.DB.Exec(ctx, `INSERT INTO portfolio_lists (id, user_id, name, created_at) VALUES ($1, $2, $3, now())`, list, user, "List "+list.String()); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := app.Deps.DB.Exec(ctx, `
		INSERT INTO stocks (id, user_id, symbol, notes, shares, buy_price, buy_date, created_at, portfolio_list_id)
		VALUES ($1, $2, $3, $4, 1, 1, '2026-01-02', now(), $5)`, id, user, symbol, notes, list); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSearchMergesOwnedFirst(t *testing.T) {
	s := &fakeSearch{results: []assets.SearchResult{
		{Symbol: "APP", Name: "AppLovin", Exchange: "NASDAQ", Currency: "USD", Conid: "1"},
		{Symbol: "AAPL", Name: "Apple Inc.", Exchange: "NASDAQ", Currency: "USD", Conid: "2"},
		{Symbol: "APPN", Name: "Appian", Exchange: "NASDAQ", Currency: "USD", Conid: "3"},
	}}
	app := newApp(t, s)
	user := app.CreateUser("a@example.com")
	notes := "my apple position"
	addStock(t, app, user, "aapl", nil)
	owned := addStock(t, app, user, "XYZ", &notes) // matches by notes only, not in market results
	token := app.Token(user)

	var got []assets.SearchResult
	r := app.Do("GET", "/v1/assets/search?q=%20Apple%20", token, nil)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r.JSON(t, &got)
	if s.queries[0] != "Apple" {
		t.Fatalf("market search must get the trimmed query: %q", s.queries[0])
	}
	syms := []string{}
	for _, g := range got {
		syms = append(syms, g.Symbol)
	}
	// Owned matches first (market-matched AAPL, notes-matched XYZ, by sort key
	// then symbol), then the remaining market results.
	if strings.Join(syms, ",") != "AAPL,XYZ,APP,APPN" {
		t.Fatalf("order %v", syms)
	}
	if got[1] != (assets.SearchResult{Symbol: "XYZ", Name: "XYZ - owned asset", Exchange: "PORTFOLIO", Currency: "USD", Conid: "owned-" + apijson.FormatUUID(owned)}) {
		t.Fatalf("owned fallback %+v", got[1])
	}

	got = nil
	app.Do("GET", "/v1/assets/search?q=app&limit=2", token, nil).JSON(t, &got)
	if len(got) != 2 {
		t.Fatalf("limit: %v", got)
	}
}

func TestSearchErrors(t *testing.T) {
	s := &fakeSearch{err: httpx.Abort(http.StatusBadGateway, "Market data unavailable.")}
	app := newApp(t, s)
	user := app.CreateUser("a@example.com")
	token := app.Token(user)
	for _, q := range []string{"", "?q=", "?q=%20%20"} {
		r := app.Do("GET", "/v1/assets/search"+q, token, nil)
		if r.Status != 400 || !strings.Contains(string(r.Body), "Missing query parameter `q`.") {
			t.Fatalf("%q: %d %s", q, r.Status, r.Body)
		}
	}
	// Nothing matched and the market failed: the market's error.
	if r := app.Do("GET", "/v1/assets/search?q=zzz", token, nil); r.Status != 502 {
		t.Fatalf("market error: %d %s", r.Status, r.Body)
	}
	// An owned match still answers when the market is down.
	addStock(t, app, user, "ZZZ", nil)
	if r := app.Do("GET", "/v1/assets/search?q=zzz", token, nil); r.Status != 200 || !strings.Contains(string(r.Body), "ZZZ - owned asset") {
		t.Fatalf("fallback: %d %s", r.Status, r.Body)
	}
	if r := app.Do("GET", "/v1/assets/search?q=a", "", nil); r.Status != 401 {
		t.Fatalf("auth: %d", r.Status)
	}
}

func TestSearchRouteNotServedWithoutMarketData(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{assets.Feature{}})
	if r := app.Do("GET", "/v1/assets/search?q=a", app.Token(app.CreateUser("a@example.com")), nil); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
}
