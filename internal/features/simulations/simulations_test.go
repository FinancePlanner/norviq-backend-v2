package simulations_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/features/simulations"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ported from PortfolioSimulationControllerTests.swift, plus compute,
// preview and paging.

type fakeMarket map[string]float64

func (f fakeMarket) Quote(_ context.Context, symbol string) (marketdata.QuoteResponse, error) {
	p, ok := f[strings.ToUpper(symbol)]
	if !ok {
		return marketdata.QuoteResponse{}, errors.New("no quote")
	}
	return marketdata.QuoteResponse{Symbol: strings.ToUpper(symbol), Currency: "USD", CurrentPrice: p, Timestamp: float64(time.Now().Unix())}, nil
}

func (fakeMarket) Fx(context.Context, string) (marketdata.FxRateResponse, error) {
	return marketdata.FxRateResponse{}, errors.New("no fx")
}

func newApp(t *testing.T, prices fakeMarket) *testutil.App {
	t.Helper()
	return testutil.NewApp(t, []server.Feature{simulations.Feature{
		Market: func(*server.Deps) (rebalancing.MarketData, error) { return prices, nil },
	}})
}

func user(t *testing.T, app *testutil.App) (uuid.UUID, string) {
	t.Helper()
	id := app.CreateUser(uuid.NewString() + "@example.com")
	return id, app.Token(id)
}

type leg struct {
	s string
	b int
}

func upsert(legs []leg, expected *int) map[string]any {
	ls := []any{}
	for _, l := range legs {
		ls = append(ls, map[string]any{"symbol": l.s, "targetBasisPoints": l.b})
	}
	b := map[string]any{
		"name": "Simulation", "mode": "fromScratch", "baseCurrency": "usd", "targetCapital": 10000,
		"fractionalSharesEnabled": false, "legs": ls,
	}
	if expected != nil {
		b["expectedRevision"] = *expected
	}
	return b
}

func expect(t *testing.T, r *testutil.Response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d want %d: %s", r.Status, status, r.Body)
	}
}

func create(t *testing.T, app *testutil.App, token string, body map[string]any) simulations.Simulation {
	t.Helper()
	r := app.Do("POST", "/v1/portfolio/simulations", token, body)
	expect(t, r, http.StatusCreated)
	var s simulations.Simulation
	r.JSON(t, &s)
	return s
}

func TestUnauthenticatedIsRejected(t *testing.T) {
	app := newApp(t, nil)
	expect(t, app.Do("GET", "/v1/portfolio/simulations", "", nil), http.StatusUnauthorized)
}

func TestCreateListDetailRoundTrip(t *testing.T) {
	app := newApp(t, nil)
	_, tok := user(t, app)
	created := create(t, app, tok, upsert([]leg{{"AAPL", 4000}, {" msft ", 3500}}, nil))
	if len(created.Legs) != 2 || created.Revision != 1 || created.CashBasisPoints() != 2500 || created.ShareSlug != nil || created.ShareEnabled ||
		!created.ShareShowCapital || created.BaseCurrency != "USD" || created.QuantityIncrement != 0.001 || created.UpdatedAt == nil {
		t.Fatalf("%+v", created)
	}
	r := app.Do("GET", "/v1/portfolio/simulations", tok, nil)
	expect(t, r, http.StatusOK)
	var list simulations.ListResponse
	r.JSON(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != created.ID || list.NextCursor != nil {
		t.Fatalf("%+v", list)
	}
	r = app.Do("GET", "/v1/portfolio/simulations/"+created.ID, tok, nil)
	expect(t, r, http.StatusOK)
	var detail simulations.Simulation
	r.JSON(t, &detail)
	if detail.Legs[0].Symbol != "AAPL" || detail.Legs[1].Symbol != "MSFT" {
		t.Fatalf("%+v", detail.Legs)
	}
	expect(t, app.Do("GET", "/v1/portfolio/simulations/nope", tok, nil), http.StatusBadRequest)
}

func TestValidationErrors(t *testing.T) {
	app := newApp(t, nil)
	_, tok := user(t, app)
	cases := map[string]map[string]any{
		"Weights total more than 100%.":               upsert([]leg{{"AAPL", 6000}, {"MSFT", 5000}}, nil),
		"AAPL appears more than once.":                upsert([]leg{{"AAPL", 3000}, {"aapl", 3000}}, nil),
		`"=X" is not a valid ticker.`:                 upsert([]leg{{"=X", 3000}}, nil),
		"AAPL needs a weight between 0.01% and 100%.": upsert([]leg{{"AAPL", 0}}, nil),
		"Add at least one position to simulate.":      upsert(nil, nil),
		"Cloning a portfolio requires a source portfolio.": func() map[string]any {
			b := upsert([]leg{{"AAPL", 1000}}, nil)
			b["mode"] = "cloneCurrentPortfolio"
			return b
		}(),
	}
	for want, body := range cases {
		r := app.Do("POST", "/v1/portfolio/simulations", tok, body)
		expect(t, r, http.StatusUnprocessableEntity)
		var e struct{ Reason string }
		r.JSON(t, &e)
		if e.Reason != want {
			t.Errorf("want %q: %s", want, r.Body)
		}
	}
	b := upsert([]leg{{"AAPL", 1000}}, nil)
	b["mode"] = "other"
	r := app.Do("POST", "/v1/portfolio/simulations", tok, b)
	expect(t, r, http.StatusBadRequest)
	if !strings.Contains(string(r.Body), "Cannot initialize PortfolioSimulationMode from invalid String value other") {
		t.Fatalf("%s", r.Body)
	}
}

func TestStaleRevisionConflicts(t *testing.T) {
	app := newApp(t, nil)
	_, tok := user(t, app)
	created := create(t, app, tok, upsert([]leg{{"AAPL", 5000}}, nil))
	rev := created.Revision
	r := app.Do("PUT", "/v1/portfolio/simulations/"+created.ID, tok, upsert([]leg{{"AAPL", 6000}}, &rev))
	expect(t, r, http.StatusOK)
	var updated simulations.Simulation
	r.JSON(t, &updated)
	if updated.Revision != 2 || updated.Legs[0].TargetBasisPoints != 6000 {
		t.Fatalf("%+v", updated)
	}
	r = app.Do("PUT", "/v1/portfolio/simulations/"+created.ID, tok, upsert([]leg{{"MSFT", 9000}}, &rev))
	expect(t, r, http.StatusConflict)
}

func TestScopedToOwnerAndDeleteCascades(t *testing.T) {
	app := newApp(t, nil)
	_, owner := user(t, app)
	_, stranger := user(t, app)
	created := create(t, app, owner, upsert([]leg{{"AAPL", 3000}, {"MSFT", 3000}}, nil))
	expect(t, app.Do("GET", "/v1/portfolio/simulations/"+created.ID, stranger, nil), http.StatusNotFound)
	expect(t, app.Do("DELETE", "/v1/portfolio/simulations/"+created.ID, stranger, nil), http.StatusNotFound)
	expect(t, app.Do("GET", "/v1/portfolio/simulations/"+created.ID, owner, nil), http.StatusOK)
	expect(t, app.Do("DELETE", "/v1/portfolio/simulations/"+created.ID, owner, nil), http.StatusNoContent)
	expect(t, app.Do("GET", "/v1/portfolio/simulations/"+created.ID, owner, nil), http.StatusNotFound)
	var n int
	if err := app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM portfolio_simulation_legs WHERE simulation_id = $1`, uuid.MustParse(created.ID)).Scan(&n); err != nil || n != 0 {
		t.Fatalf("orphans %d %v", n, err)
	}
}

func TestUnpriceableTargetsFailCleanly(t *testing.T) {
	app := newApp(t, fakeMarket{})
	_, tok := user(t, app)
	created := create(t, app, tok, upsert([]leg{{"NOSUCHTICKER", 5000}}, nil))
	r := app.Do("POST", "/v1/portfolio/simulations/"+created.ID+"/compute", tok, map[string]any{})
	expect(t, r, http.StatusUnprocessableEntity)
	if !strings.Contains(string(r.Body), "No current price is available for NOSUCHTICKER.") {
		t.Fatalf("%s", r.Body)
	}
}

func TestCloningRequiresReadableSource(t *testing.T) {
	app := newApp(t, fakeMarket{"AAPL": 100})
	ownerID, owner := user(t, app)
	_, stranger := user(t, app)
	pid := uuid.New()
	if _, err := app.Deps.DB.Exec(context.Background(), `INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Real', true, now())`, pid, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Deps.DB.Exec(context.Background(), `INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES ($1, $2, 'TSLA', 10, 50, current_date, now(), $3)`, uuid.New(), ownerID, pid); err != nil {
		t.Fatal(err)
	}
	b := upsert([]leg{{"AAPL", 5000}}, nil)
	b["mode"] = "cloneCurrentPortfolio"
	b["sourcePortfolioId"] = apijson.FormatUUID(pid)
	expect(t, app.Do("POST", "/v1/portfolio/simulations", stranger, b), http.StatusNotFound)
	created := create(t, app, owner, b)
	if created.SourcePortfolioID == nil || *created.SourcePortfolioID != apijson.FormatUUID(pid) {
		t.Fatalf("%+v", created)
	}
	// TSLA has no quote: a held position is valued at cost, not dropped.
	r := app.Do("POST", "/v1/portfolio/simulations/"+created.ID+"/compute", owner, nil)
	expect(t, r, http.StatusOK)
	var res simulations.Result
	r.JSON(t, &res)
	if res.Simulation.PortfolioID != apijson.FormatUUID(pid) || len(res.Warnings) != 1 || res.Warnings[0].Code != "missing_price" {
		t.Fatalf("%+v", res)
	}
}

func TestComputeAndPreview(t *testing.T) {
	app := newApp(t, fakeMarket{"AAPL": 100, "MSFT": 50})
	_, tok := user(t, app)
	created := create(t, app, tok, upsert([]leg{{"AAPL", 5000}, {"MSFT", 2500}}, nil))
	r := app.Do("POST", "/v1/portfolio/simulations/"+created.ID+"/compute", tok, map[string]any{"targetCapitalOverride": 2000})
	expect(t, r, http.StatusOK)
	var res simulations.Result
	r.JSON(t, &res)
	if res.SimulationID != created.ID || res.CashBasisPoints != 2500 || res.TotalCashNeeded != 1500 || res.LeftoverCash != 500 ||
		len(res.Simulation.Trades) != 2 || len(res.Diff) != 2 || res.Diff[0].Symbol != "AAPL" || res.Diff[0].TargetValue != 1000 {
		t.Fatalf("%+v", res)
	}
	r = app.Do("POST", "/v1/portfolio/simulations/"+created.ID+"/compute", tok, map[string]any{"targetCapitalOverride": -1})
	expect(t, r, http.StatusUnprocessableEntity)

	r = app.Do("POST", "/v1/portfolio/simulations/preview", tok, upsert([]leg{{"MSFT", 10000}}, nil))
	expect(t, r, http.StatusOK)
	r.JSON(t, &res)
	if res.Revision != 1 || res.Simulation.Trades[0].Quantity != 200 || res.LeftoverCash != 0 {
		t.Fatalf("%+v", res)
	}
	var n int
	if err := app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM portfolio_simulations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("preview persisted: %d %v", n, err)
	}
}

func TestListPaging(t *testing.T) {
	app := newApp(t, nil)
	_, tok := user(t, app)
	for range 3 {
		create(t, app, tok, upsert([]leg{{"AAPL", 5000}}, nil))
	}
	r := app.Do("GET", "/v1/portfolio/simulations?limit=2", tok, nil)
	var page simulations.ListResponse
	r.JSON(t, &page)
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("%+v", page)
	}
	r = app.Do("GET", "/v1/portfolio/simulations?limit=2&cursor="+strings.ReplaceAll(*page.NextCursor, "+", "%2B"), tok, nil)
	var next simulations.ListResponse
	r.JSON(t, &next)
	if len(next.Items) != 1 || next.NextCursor != nil || next.Items[0].ID == page.Items[0].ID || next.Items[0].ID == page.Items[1].ID {
		t.Fatalf("%+v", next)
	}
	// A malformed cursor or limit is ignored, as Swift's optional reads were.
	r = app.Do("GET", "/v1/portfolio/simulations?limit=x&cursor=%25", tok, nil)
	r.JSON(t, &next)
	if len(next.Items) != 3 {
		t.Fatalf("%d", len(next.Items))
	}
}
