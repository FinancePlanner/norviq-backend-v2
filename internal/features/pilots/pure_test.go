package pilots

import (
	"math"
	"reflect"
	"testing"
	"time"
)

// 2026-10-01T00:00:00Z
var testNow = time.Unix(1_790_812_800, 0).UTC()

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

func trade(symbol, side string, lo, hi *float64, date, instrument string) BookEntry {
	if date == "" {
		date = "2026-06-01"
	}
	if instrument == "" {
		instrument = InstrumentStock
	}
	return BookEntry{Symbol: symbol, Side: side, Instrument: instrument, TransactionDate: &date, AmountMin: lo, AmountMax: hi}
}

func wantWeights(t *testing.T, got, want map[string]float64) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("weights %v, want %v", got, want)
	}
}

func TestEstimatedValue(t *testing.T) {
	if *EstimatedValue(f64(1001), f64(15000)) != 8000.5 || *EstimatedValue(f64(5_000_000), nil) != 5_000_000 ||
		*EstimatedValue(nil, f64(1000)) != 1000 || EstimatedValue(nil, nil) != nil {
		t.Fatal("estimatedValue")
	}
}

func TestBookSameDayInputOrder(t *testing.T) {
	b := PoliticianBook([]BookEntry{
		trade("MSFT", SideBuy, f64(1000), f64(1000), "2026-05-01", ""),
		trade("AAPL", SideBuy, f64(1000), f64(1000), "2026-06-01", ""),
		trade("AAPL", SideSellFull, f64(1000), f64(1000), "2026-06-01", ""),
	}, testNow)
	wantWeights(t, b.Weights, map[string]float64{"MSFT": 1})
	b = PoliticianBook([]BookEntry{
		trade("AAPL", SideSellFull, f64(1000), f64(1000), "2026-06-01", ""),
		trade("AAPL", SideBuy, f64(1000), f64(1000), "2026-06-01", ""),
		trade("MSFT", SideBuy, f64(1000), f64(1000), "2026-05-01", ""),
	}, testNow)
	wantWeights(t, b.Weights, map[string]float64{"AAPL": 0.5, "MSFT": 0.5})
}

func TestBookBuysSalesOptionsLookback(t *testing.T) {
	wantWeights(t, PoliticianBook([]BookEntry{
		trade("AAPL", SideBuy, f64(15000), f64(15000), "", ""),
		trade("MSFT", SideBuy, f64(5000), f64(5000), "", ""),
	}, testNow).Weights, map[string]float64{"AAPL": 0.75, "MSFT": 0.25})

	wantWeights(t, PoliticianBook([]BookEntry{
		trade("AAPL", SideBuy, f64(20000), f64(20000), "2026-01-01", ""),
		trade("AAPL", SideSell, f64(10000), f64(10000), "2026-02-01", ""),
		trade("MSFT", SideBuy, f64(10000), f64(10000), "2026-01-01", ""),
		trade("NVDA", SideBuy, f64(10000), f64(10000), "2026-01-01", ""),
		trade("NVDA", SideSellFull, f64(1001), f64(15000), "2026-03-01", ""),
	}, testNow).Weights, map[string]float64{"AAPL": 0.5, "MSFT": 0.5})

	wantWeights(t, PoliticianBook([]BookEntry{
		trade("TSLA", SideSell, f64(50000), f64(100_000), "", ""),
		trade("AAPL", SideBuy, f64(1000), f64(1000), "", ""),
	}, testNow).Weights, map[string]float64{"AAPL": 1})

	b := PoliticianBook([]BookEntry{
		trade("NVDA", SideBuy, f64(1000), f64(1000), "", InstrumentCall),
		trade("AAPL", SideBuy, f64(1000), f64(1000), "", ""),
		trade("SPY", SideBuy, f64(50000), f64(50000), "", InstrumentPut),
	}, testNow)
	wantWeights(t, b.Weights, map[string]float64{"NVDA": 0.5, "AAPL": 0.5})
	if b.SkippedPuts != 1 {
		t.Fatal(b.SkippedPuts)
	}

	wantWeights(t, PoliticianBook([]BookEntry{
		trade("AAPL", SideSellFull, f64(1), f64(1), "2026-05-01", ""),
		trade("AAPL", SideBuy, f64(10000), f64(10000), "2026-04-01", ""),
		trade("KO", SideBuy, f64(10000), f64(10000), "2024-01-01", ""),
		trade("MSFT", SideBuy, f64(10000), f64(10000), "2026-04-01", ""),
	}, testNow).Weights, map[string]float64{"MSFT": 1})
}

func TestFundBookAndEmpty(t *testing.T) {
	hold := func(s string, v float64, p string) BookEntry {
		return BookEntry{Symbol: s, Side: SideHold, Instrument: InstrumentStock, MarketValue: &v, Period: &p}
	}
	wantWeights(t, FundBook([]BookEntry{hold("AAPL", 300, "2026Q2"), hold("KO", 100, "2026Q2"), hold("OXY", 999, "2026Q1")}).Weights,
		map[string]float64{"AAPL": 0.75, "KO": 0.25})
	if b := PoliticianBook(nil, testNow); len(b.Weights) != 0 || b.SkippedPuts != 0 {
		t.Fatal(b)
	}
	if b := FundBook(nil); len(b.Weights) != 0 {
		t.Fatal(b)
	}
	if got := cutoffDate(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), 1); got != "2026-02-28" {
		t.Fatal(got)
	}
}

func wantOrders(t *testing.T, got, want []Order) {
	t.Helper()
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orders %+v, want %+v", got, want)
	}
}

func TestPlanner(t *testing.T) {
	m := func(kv ...any) map[string]float64 {
		out := map[string]float64{}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1].(float64)
		}
		return out
	}
	p := PlanRebalance(m("AAPL", 0.5, "MSFT", 0.5), nil, 10000, m("AAPL", 100.0, "MSFT", 250.0))
	wantOrders(t, p.Orders, []Order{{"AAPL", SideBuy, 50, 100}, {"MSFT", SideBuy, 20, 250}})

	p = PlanRebalance(m("MSFT", 1.0), m("AAPL", 10.0), 0, m("AAPL", 100.0, "MSFT", 100.0))
	wantOrders(t, p.Orders, []Order{{"AAPL", SideSell, 10, 100}, {"MSFT", SideBuy, 10, 100}})

	p = PlanRebalance(m("AAPL", 1.0), m("AAPL", 99.99), 1, m("AAPL", 100.0))
	wantOrders(t, p.Orders, nil)

	p = PlanRebalance(m("AAPL", 0.5, "ZZZZ", 0.5), m("OLD", 5.0), 1000, m("AAPL", 100.0))
	if !reflect.DeepEqual(p.Unpriced, []string{"OLD", "ZZZZ"}) {
		t.Fatal(p.Unpriced)
	}
	wantOrders(t, p.Orders, []Order{{"AAPL", SideBuy, 5, 100}})

	p = PlanRebalance(m("AAPL", 1.0), nil, 100, m("AAPL", 30.0))
	spent := 0.0
	for _, o := range p.Orders {
		spent += o.Quantity * o.Price
	}
	if spent > 100+1e-6 {
		t.Fatal(spent)
	}

	p = PlanRebalance(m("MSFT", 1.0), m("AAPL", 1.0000006), 0, m("AAPL", 100.0, "MSFT", 100.0))
	if p.Orders[0] != (Order{"AAPL", SideSell, 1.0000006, 100}) {
		t.Fatal(p.Orders)
	}

	p = PlanRebalance(m("MSFT", 1.0), m("AAPL", 0.01), 1000, m("AAPL", 100.0, "MSFT", 100.0))
	found := false
	for _, o := range p.Orders {
		found = found || o == (Order{"AAPL", SideSell, 0.01, 100})
	}
	if !found {
		t.Fatal(p.Orders)
	}
	if math.Abs(floor6(1.9999999)-1.999999) > 1e-12 {
		t.Fatal("rounding")
	}
}
