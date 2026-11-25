package portfolio_test

import (
	"math"
	"net/http"
	"testing"
	"time"

	"norviq-api/internal/platform/marketdata"

	"github.com/google/uuid"
)

// fxToday seeds today's EUR-based ECB fixing for quote.
func (h *harness) fxToday(quote, rate string) {
	h.T.Helper()
	h.exec(`INSERT INTO fx_daily_rates (id, date, base, quote, rate) VALUES (gen_random_uuid(), $1, 'EUR', $2, $3::numeric)`,
		time.Now().UTC().Format("2006-01-02"), quote, rate)
}

func (h *harness) stockCcy(user, list uuid.UUID, symbol string, shares, price float64, currency string) {
	h.T.Helper()
	h.exec(`INSERT INTO stocks (id, user_id, symbol, shares, buy_price, buy_date, created_at, portfolio_list_id, currency)
		VALUES ($1, $2, $3, $4, $5, '2023-11-14', now(), $6, $7)`, uuid.New(), user, symbol, shares, price, list, currency)
}

func (h *harness) cash(user uuid.UUID, list uuid.UUID, amount float64) {
	h.T.Helper()
	acct := h.account(user, &list, "manual")
	h.exec(`INSERT INTO cash_balances (id, account_id, currency, balance, as_of, created_at) VALUES ($1, $2, 'USD', $3, now(), now())`, uuid.New(), acct, amount)
}

type liveAlloc struct {
	Symbol       string   `json:"symbol"`
	Value        float64  `json:"value"`
	Currency     string   `json:"currency"`
	ValueInBase  *float64 `json:"valueInBase"`
	BaseCurrency string   `json:"baseCurrency"`
}

type liveSummary struct {
	TotalValue    float64     `json:"totalValue"`
	TotalCost     float64     `json:"totalCost"`
	UnrealizedPnl float64     `json:"unrealizedPnl"`
	RealizedPnl   float64     `json:"realizedPnl"`
	Allocation    []liveAlloc `json:"allocation"`
	Fx            struct {
		Source    *string `json:"source"`
		AsOf      *string `json:"asOf"`
		Converted bool    `json:"converted"`
	} `json:"fx"`
	FxMissing []string `json:"fxMissing"`
}

type livePnl struct {
	Items []struct {
		Symbol              string   `json:"symbol"`
		Currency            string   `json:"currency"`
		BaseCurrency        string   `json:"baseCurrency"`
		CostBasis           float64  `json:"costBasis"`
		MarketValue         float64  `json:"marketValue"`
		UnrealizedPnl       float64  `json:"unrealizedPnl"`
		UnrealizedPnlInBase *float64 `json:"unrealizedPnlInBase"`
		ValueInBase         *float64 `json:"valueInBase"`
	} `json:"items"`
}

func (h *harness) summary(t *testing.T, user uuid.UUID, list uuid.UUID) liveSummary {
	t.Helper()
	r := h.Do(http.MethodGet, "/v1/portfolio/summary?portfolioId="+list.String(), h.Token(user), nil)
	ok(t, r, 200)
	var s liveSummary
	r.JSON(t, &s)
	return s
}

func (h *harness) quoteSet(q map[string]marketdata.QuoteResponse) {
	h.quotes.fail = false
	h.quotes.quotes = q
}

func near2(a, b float64) bool { return math.Abs(a-b) < 0.011 }

func alloc(s liveSummary, symbol string) liveAlloc {
	for _, a := range s.Allocation {
		if a.Symbol == symbol {
			return a
		}
	}
	return liveAlloc{Symbol: "?"}
}

func TestSummaryMultiCurrency(t *testing.T) {
	t.Setenv("FX_VALUATION_ENABLED", "true")
	h := newHarness(t)
	user := h.CreateUser("fx-multi@example.com")
	list := h.list(user, "Main", true)
	h.stock(user, list, "AAPL", 10, 150)
	h.stock(user, list, "NOVO", 10, 600)
	h.cash(user, list, 100)
	h.fxToday("USD", "1.05")
	h.fxToday("DKK", "7.46")
	h.quoteSet(map[string]marketdata.QuoteResponse{
		"AAPL": {Symbol: "AAPL", Currency: "USD", CurrentPrice: 200},
		"NOVO": {Symbol: "NOVO", Currency: "DKK", CurrentPrice: 700},
	})
	s := h.summary(t, user, list)
	novoBase := math.Round(7000*1.05/7.46*100) / 100
	if !near2(s.TotalValue, 2000+novoBase+100) {
		t.Fatalf("totalValue %v, want %v", s.TotalValue, 2000+novoBase+100)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if s.Fx.Source == nil || *s.Fx.Source != "ECB" || s.Fx.AsOf == nil || *s.Fx.AsOf != today || !s.Fx.Converted {
		t.Fatalf("fx %+v", s.Fx)
	}
	n := alloc(s, "NOVO")
	if n.Currency != "DKK" || n.Value != 7000 || n.ValueInBase == nil || !near2(*n.ValueInBase, novoBase) || n.BaseCurrency != "USD" {
		t.Fatalf("novo %+v", n)
	}
	// Sorted by value in base (985 USD), not by the raw 7000 DKK.
	if s.Allocation[0].Symbol != "AAPL" || s.Allocation[1].Symbol != "NOVO" || s.Allocation[len(s.Allocation)-1].Symbol != "CASH" {
		t.Fatalf("order %+v", s.Allocation)
	}
	r := h.Do(http.MethodGet, "/v1/pnl?portfolioId="+list.String(), h.Token(user), nil)
	ok(t, r, 200)
	var p livePnl
	r.JSON(t, &p)
	for _, it := range p.Items {
		if it.Symbol == "NOVO" && (it.Currency != "DKK" || it.BaseCurrency != "USD" || it.MarketValue != 7000 || it.ValueInBase == nil ||
			!near2(*it.ValueInBase, novoBase) || it.UnrealizedPnlInBase == nil) {
			t.Fatalf("pnl novo %s", r.Body)
		}
	}
}

func TestSummaryFlagOffUnchanged(t *testing.T) {
	h := newHarness(t)
	user := h.CreateUser("fx-off@example.com")
	list := h.list(user, "Main", true)
	h.stock(user, list, "AAPL", 10, 150)
	h.stock(user, list, "NOVO", 10, 600)
	h.stockCcy(user, list, "MISM", 1, 80, "USD")
	h.stock(user, list, "LSE", 3, 400)
	h.cash(user, list, 100)
	h.quoteSet(map[string]marketdata.QuoteResponse{
		"AAPL": {Symbol: "AAPL", Currency: "USD", CurrentPrice: 200},
		"NOVO": {Symbol: "NOVO", Currency: "DKK", CurrentPrice: 700},
		"MISM": {Symbol: "MISM", Currency: "CAD", CurrentPrice: 90},
		"LSE":  {Symbol: "LSE", Currency: "GBX", CurrentPrice: 500},
	})
	s := h.summary(t, user, list)
	// Every holding at its raw quote, no guard, no factor, no conversion.
	if s.TotalValue != 2000+7000+90+1500+100 || s.TotalCost != 1500+6000+80+1200 || s.UnrealizedPnl != (2000+7000+90+1500)-(1500+6000+80+1200) {
		t.Fatalf("totals %+v", s)
	}
	if s.Fx.Converted || s.Fx.Source != nil || s.Fx.AsOf != nil || len(s.FxMissing) != 0 {
		t.Fatalf("fx %+v %v", s.Fx, s.FxMissing)
	}
	if got := alloc(s, "NOVO"); got.Currency != "DKK" || got.Value != 7000 {
		t.Fatalf("novo %+v", got)
	}
	// R10: raw minor units keep the base label; the stored currency labels MISM.
	if got := alloc(s, "LSE"); got.Currency != "USD" || got.Value != 1500 {
		t.Fatalf("lse %+v", got)
	}
	if got := alloc(s, "MISM"); got.Currency != "USD" || got.Value != 90 {
		t.Fatalf("mism %+v", got)
	}
}

func TestSummaryFxMissing(t *testing.T) {
	t.Setenv("FX_VALUATION_ENABLED", "true")
	h := newHarness(t)
	user := h.CreateUser("fx-missing@example.com")
	list := h.list(user, "Main", true)
	h.stock(user, list, "AAPL", 10, 150)
	h.stock(user, list, "LSE", 3, 400)
	h.fxToday("USD", "1.05")
	h.quoteSet(map[string]marketdata.QuoteResponse{
		"AAPL": {Symbol: "AAPL", Currency: "USD", CurrentPrice: 200},
		"LSE":  {Symbol: "LSE", Currency: "GBp", CurrentPrice: 500},
	})
	s := h.summary(t, user, list)
	if s.TotalValue != 2000 || s.TotalCost != 1500 || len(s.FxMissing) != 1 || s.FxMissing[0] != "GBP" {
		t.Fatalf("%+v", s)
	}
	l := alloc(s, "LSE")
	if l.Currency != "GBP" || l.ValueInBase != nil || l.Value != 15 {
		t.Fatalf("lse %+v", l)
	}
	if s.Allocation[len(s.Allocation)-1].Symbol != "LSE" {
		t.Fatalf("nil last: %+v", s.Allocation)
	}
}

// A stored GBX currency means buy_price is in pence: cost and value both come
// out in pounds, with no listing mismatch against the GBp quote.
func TestSummaryStoredGBXCostInMajorUnits(t *testing.T) {
	t.Setenv("FX_VALUATION_ENABLED", "true")
	h := newHarness(t)
	user := h.CreateUser("fx-gbx@example.com")
	list := h.list(user, "Main", true)
	h.stockCcy(user, list, "LSE", 10, 250, "GBX")
	h.fxToday("USD", "1.10")
	h.fxToday("GBP", "0.85")
	h.quoteSet(map[string]marketdata.QuoteResponse{"LSE": {Symbol: "LSE", Currency: "GBp", CurrentPrice: 300}})
	s := h.summary(t, user, list)
	rate := 1.10 / 0.85
	if !near2(s.TotalCost, 25*rate) || !near2(s.TotalValue, 30*rate) || len(s.FxMissing) != 0 {
		t.Fatalf("%+v", s)
	}
	if a := alloc(s, "LSE"); a.Currency != "GBP" || a.Value != 30 || a.ValueInBase == nil {
		t.Fatalf("alloc %+v", a)
	}
	r := h.Do(http.MethodGet, "/v1/pnl?portfolioId="+list.String(), h.Token(user), nil)
	ok(t, r, 200)
	var p livePnl
	r.JSON(t, &p)
	if len(p.Items) != 1 || p.Items[0].Currency != "GBP" || p.Items[0].CostBasis != 25 || p.Items[0].MarketValue != 30 || p.Items[0].UnrealizedPnl != 5 {
		t.Fatalf("%s", r.Body)
	}
}
