package sharing

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ported from PublicPortfolioShareBuilderTests.swift.

func ptr(v float64) *float64 { return &v }

func hv(symbol string, value float64) portfolio.HoldingValuation {
	return portfolio.HoldingValuation{
		Symbol: symbol, Shares: 1, CostBasis: value, AverageBuyPrice: value, CurrentPrice: ptr(value),
		MarketValue: value, UnrealizedPnlPercent: ptr(10), DayChangePercent: ptr(1),
	}
}

func val(holdings []portfolio.HoldingValuation, cash float64) portfolio.Valuation {
	total := 0.0
	for _, h := range holdings {
		total += h.MarketValue
	}
	return portfolio.Valuation{
		Holdings: holdings, HoldingsMarketValue: total, TotalCost: total, UnrealizedPnlPercent: ptr(5),
		CashBalance: cash, TotalValue: total + math.Max(0, cash), DayChangePercent: ptr(0.5), AsOf: time.Now(),
	}
}

func sum(out response) float64 {
	s := 0.0
	for _, h := range out.Holdings {
		s += h.WeightPercent
	}
	if out.OtherWeightPercent != nil {
		s += *out.OtherWeightPercent
	}
	return s
}

func TestWeights(t *testing.T) {
	out := Build(val([]portfolio.HoldingValuation{hv("MSFT", 300), hv("AAPL", 600)}, 100), nil, "2026-09-24")
	if len(out.Holdings) != 3 || out.Holdings[0].Symbol != "AAPL" || out.Holdings[1].Symbol != "MSFT" || out.Holdings[2].Symbol != "CASH" {
		t.Fatalf("%+v", out.Holdings)
	}
	if out.Holdings[0].WeightPercent != 60 || out.Holdings[1].WeightPercent != 30 || out.Holdings[2].WeightPercent != 10 {
		t.Fatalf("%+v", out.Holdings)
	}
	if out.Holdings[2].UnrealizedPnlPercent != nil || out.OtherWeightPercent != nil || *out.Totals.UnrealizedPnlPercent != 5 {
		t.Fatalf("%+v", out)
	}
}

func TestFoldsIntoOther(t *testing.T) {
	var many []portfolio.HoldingValuation
	for i := 0; i < 20; i++ {
		many = append(many, hv("S", float64(100-i)))
	}
	out := Build(val(many, 0), nil, "2026-09-24")
	if len(out.Holdings) != 12 || out.OtherWeightPercent == nil || math.Abs(sum(out)-100) >= 0.2 {
		t.Fatalf("%d %v %v", len(out.Holdings), out.OtherWeightPercent, sum(out))
	}
}

func TestCashDoesNotTakeAStockSlot(t *testing.T) {
	var stocks []portfolio.HoldingValuation
	for i := 0; i < 13; i++ {
		stocks = append(stocks, hv("S", float64(100-i)))
	}
	out := Build(val(stocks, 500), nil, "2026-09-24")
	if len(out.Holdings) != 13 || out.Holdings[12].Symbol != "CASH" || out.OtherWeightPercent == nil || math.Abs(sum(out)-100) >= 0.2 {
		t.Fatalf("%+v", out)
	}
}

func TestEmptyAndCashOnly(t *testing.T) {
	out := Build(val(nil, 0), nil, "2026-09-24")
	raw, _ := json.Marshal(out)
	if string(raw) != `{"asOf":"2026-09-24","totals":{"unrealizedPnlPercent":5,"dayChangePercent":0.5},"holdings":[]}` {
		t.Fatalf("empty %s", raw)
	}
	out = Build(val(nil, 500), nil, "2026-09-24")
	if len(out.Holdings) != 1 || out.Holdings[0] != (holding{Symbol: "CASH", WeightPercent: 100}) {
		t.Fatalf("cash only %+v", out.Holdings)
	}
}

func TestYTDIsPercent(t *testing.T) {
	ytd := &portfolio.PortfolioChange{Percent: 0.081, Absolute: 810, FromDate: "2026-01-01", ToDate: "2026-09-24", Basis: "ytd"}
	out := Build(val([]portfolio.HoldingValuation{hv("AAPL", 100)}, 0), &portfolio.PortfolioChanges{YTD: ytd}, "2026-09-24")
	if out.Totals.YTDPercent == nil || *out.Totals.YTDPercent != 8.1 {
		t.Fatalf("%v", out.Totals.YTDPercent)
	}
}

func TestNoMoneyKeys(t *testing.T) {
	out := Build(val([]portfolio.HoldingValuation{hv("AAPL", 123_456.78)}, 0), nil, "2026-09-24")
	raw, _ := json.Marshal(out)
	for _, banned := range []string{"value", "Value", "cost", "shares", "price", "currency", "123456"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("leaked %s: %s", banned, raw)
		}
	}
}

// Ported from PortfolioShareRouteTests.swift.

type noQuotes struct{}

func (noQuotes) QuoteBatch(context.Context, []string) (marketdata.QuoteBatchResponse, error) {
	return marketdata.QuoteBatchResponse{Quotes: []marketdata.QuoteResponse{}}, nil
}

func TestPublicShareRoute(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{Feature{Quotes: noQuotes{}}})
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := app.Deps.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	owner := app.CreateUser("share_f@example.com")
	stranger := app.CreateUser("share_i@example.com")
	list, strangerList := uuid.New(), uuid.New()
	exec(`INSERT INTO portfolio_lists (id, user_id, name, is_default, created_at) VALUES ($1, $2, 'Main', true, now()), ($3, $4, 'Main', true, now())`, list, owner, strangerList, stranger)
	exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id) VALUES
		($1, $2, 'AAPL', 3, 200, '2026-01-02', now(), $3), ($4, $2, 'MSFT', 1, 200, '2026-01-02', now(), $3)`, uuid.New(), owner, list, uuid.New())
	exec(`INSERT INTO portfolio_share_links (id, user_id, portfolio_list_id, slug, created_at) VALUES
		($1, $2, NULL, 'pallowner00000000000000', now()),
		($3, $2, $4, 'pmoved000000000000000000', now()),
		($5, $2, NULL, 'prevoked0000000000000000', now())`, uuid.New(), owner, uuid.New(), strangerList, uuid.New())
	exec(`UPDATE portfolio_share_links SET revoked_at = now() WHERE slug = 'prevoked0000000000000000'`)

	r := app.Do(http.MethodGet, "/v1/public/portfolio-shares/pallowner00000000000000", "", nil)
	if r.Status != 200 || r.Header.Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("share: %d %s %v", r.Status, r.Body, r.Header)
	}
	body := string(r.Body)
	if strings.Contains(body, "600") || strings.Contains(body, "200") || strings.Contains(body, "share_f") {
		t.Fatalf("leak: %s", body)
	}
	var dto response
	r.JSON(t, &dto)
	if len(dto.Holdings) != 2 || dto.Holdings[0].Symbol != "AAPL" || dto.Holdings[0].WeightPercent != 75 || dto.Holdings[1].WeightPercent != 25 {
		t.Fatalf("dto %s", body)
	}
	for _, slug := range []string{"prevoked0000000000000000", "pdoesnotexist0000000000", "pmoved000000000000000000"} {
		if r := app.Do(http.MethodGet, "/v1/public/portfolio-shares/"+slug, "", nil); r.Status != 404 {
			t.Errorf("%s: %d %s", slug, r.Status, r.Body)
		}
	}
}
