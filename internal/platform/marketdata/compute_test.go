package marketdata

import (
	"math"
	"testing"
	"time"
)

// ---- Technical signals (TechnicalSignalsTests.swift) --------------------------

func candles(closes []float64) []PriceBarResponse {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]PriceBarResponse, len(closes))
	for i, c := range closes {
		vol := 1000
		out[i] = PriceBarResponse{Date: FormatDateOnly(start.AddDate(0, 0, i)), Open: c, High: c, Low: c, Close: c, Volume: &vol}
	}
	return out
}

func seq(from, to int) []float64 {
	out := []float64{}
	for i := from; i <= to; i++ {
		out = append(out, float64(i))
	}
	return out
}

func decliningThenRising() []float64 {
	out := []float64{}
	for i := range 200 {
		out = append(out, 300-float64(i))
	}
	for s := 1; s <= 60; s++ {
		out = append(out, 100+4*float64(s))
	}
	return out
}

func risingThenDeclining() []float64 {
	out := []float64{}
	for i := range 200 {
		out = append(out, 100+float64(i))
	}
	for s := 1; s <= 60; s++ {
		out = append(out, 300-4*float64(s))
	}
	return out
}

var wilderCloses = []float64{
	44.3389, 44.0902, 44.1497, 43.6124, 44.3278, 44.8264, 45.0955, 45.4245,
	45.8433, 46.0826, 45.8931, 46.0328, 45.6140, 46.2820, 46.2820,
}

func compoundingAdvance() []float64 {
	out := make([]float64, 60)
	for i := range out {
		out[i] = 100 * math.Pow(1.01, float64(i))
	}
	return out
}

func mustCompute(t *testing.T, closes []float64) TechnicalSignalsResponse {
	t.Helper()
	s := ComputeTechnicals("aapl ", candles(closes))
	if s == nil {
		t.Fatal("no signals")
	}
	return *s
}

func near(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestTechnicalsMovingAveragesAndTrend(t *testing.T) {
	s := mustCompute(t, seq(1, 300))
	if s.Symbol != "AAPL" || s.Close != 300 || s.AsOf != "2024-10-26" {
		t.Fatalf("header %+v", s)
	}
	if *s.SMA50 != 275.5 || *s.SMA200 != 200.5 || s.Trend != "aboveBoth" || s.Cross != "none" {
		t.Fatalf("averages %v %v %s %s", *s.SMA50, *s.SMA200, s.Trend, s.Cross)
	}
	rev := seq(1, 300)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if got := mustCompute(t, rev).Trend; got != "belowBoth" {
		t.Fatalf("trend %s", got)
	}
}

func TestTechnicalsCrosses(t *testing.T) {
	s := mustCompute(t, decliningThenRising()[:250])
	if *s.SMA50 != 202.0 || *s.SMA200 != 182.125 || s.Cross != "goldenCross" {
		t.Fatalf("golden %v %v %s", *s.SMA50, *s.SMA200, s.Cross)
	}
	if got := mustCompute(t, risingThenDeclining()[:250]).Cross; got != "deathCross" {
		t.Fatalf("death %s", got)
	}
	if got := mustCompute(t, decliningThenRising()).Cross; got != "none" {
		t.Fatalf("stale cross %s", got)
	}
}

func TestTechnicalsRSI(t *testing.T) {
	if r := mustCompute(t, wilderCloses).RSI14; r == nil || !near(*r, 70.5328, 0.0005) {
		t.Fatalf("seed rsi %v", r)
	}
	ext := append(append([]float64{}, wilderCloses...),
		46.0000, 46.0300, 46.4100, 46.2200, 45.6400, 46.2100, 46.2500, 45.7100,
		46.4500, 45.7800, 45.3500, 44.0300, 44.1800, 44.2200, 44.5700, 43.4200,
		42.6600, 43.1300)
	if r := mustCompute(t, ext).RSI14; r == nil || !near(*r, 37.7877, 0.0005) {
		t.Fatalf("smoothed rsi %v", r)
	}
	if r := mustCompute(t, seq(1, 20)).RSI14; r == nil || *r != 100 {
		t.Fatalf("all gains rsi %v", r)
	}
}

func TestTechnicalsMACD(t *testing.T) {
	m := mustCompute(t, compoundingAdvance()).MACD
	if m == nil || !near(m.Line, 10.561288, 0.0005) || !near(m.Signal, 10.179556, 0.0005) || m.Sign != "positive" {
		t.Fatalf("macd %+v", m)
	}
	adv := compoundingAdvance()
	peak := adv[59]
	for i := range 20 {
		adv = append(adv, peak*math.Pow(0.99, float64(i+1)))
	}
	if m := mustCompute(t, adv).MACD; m == nil || m.Histogram >= 0 || m.Sign != "negative" {
		t.Fatalf("reversal macd %+v", m)
	}
}

func TestTechnicalsFiftyTwoWeekAndThinData(t *testing.T) {
	s := mustCompute(t, seq(1, 300))
	if s.FiftyTwoWeek.High != 300 || s.FiftyTwoWeek.Low != 49 || s.FiftyTwoWeek.PositionPct != 100 {
		t.Fatalf("52w %+v", s.FiftyTwoWeek)
	}
	if s := mustCompute(t, []float64{10, 30, 20}); s.FiftyTwoWeek.PositionPct != 50 {
		t.Fatalf("mid %+v", s.FiftyTwoWeek)
	}
	thin := mustCompute(t, seq(1, 30))
	if thin.SMA50 != nil || thin.SMA200 != nil || thin.MACD != nil || thin.RSI14 == nil || thin.Trend != "mixed" {
		t.Fatalf("thin %+v", thin)
	}
	one := mustCompute(t, []float64{42})
	if one.Close != 42 || one.AsOf != "2024-01-01" || one.RSI14 != nil || one.FiftyTwoWeek.High != 42 || one.FiftyTwoWeek.PositionPct != 50 {
		t.Fatalf("single %+v", one)
	}
	if ComputeTechnicals("AAPL", nil) != nil {
		t.Fatal("empty should be nil")
	}
	// Newest-first input is ordered before computing.
	bars := candles(seq(1, 300))
	for i, j := 0, len(bars)-1; i < j; i, j = i+1, j-1 {
		bars[i], bars[j] = bars[j], bars[i]
	}
	if s := ComputeTechnicals("AAPL", bars); s.Close != 300 {
		t.Fatalf("unsorted close %v", s.Close)
	}
	// Only the last 300 sessions count.
	withPrehistory := append(make([]float64, 0, 400), seq(1, 300)...)
	pre := make([]float64, 100)
	for i := range pre {
		pre[i] = 5000
	}
	full := mustCompute(t, append(pre, withPrehistory...))
	capped := mustCompute(t, seq(1, 300))
	if *full.SMA200 != *capped.SMA200 || full.FiftyTwoWeek != capped.FiftyTwoWeek {
		t.Fatal("prehistory leaked into the window")
	}
}

// ---- Earnings streaks and the recent-earnings teaser ----------------------------

func er(date string, actual, estimate *float64) EarningsResponse {
	return EarningsResponse{Symbol: "AAPL", Date: date, EpsActual: actual, EpsEstimated: estimate}
}

func f(v float64) *float64 { return &v }

func TestAnnotateEarningsStreaks(t *testing.T) {
	// Newest first: beat, beat, beat, miss, beat, beat.
	rows := []EarningsResponse{
		er("2024-06-01", f(2), f(1)), er("2024-03-01", f(2), f(1)), er("2023-12-01", f(2), f(1)),
		er("2023-09-01", f(1), f(2)), er("2023-06-01", f(2), f(1)), er("2023-03-01", f(1), f(1)),
	}
	got := AnnotateEarningsStreaks(rows)
	wantBeat := []int{3, 2, 1, 0, 2, 1}
	wantMiss := []int{0, 0, 0, 1, 0, 0}
	for i := range got {
		if got[i].BeatStreak != wantBeat[i] || got[i].MissStreak != wantMiss[i] || got[i].Date != rows[i].Date {
			t.Fatalf("row %d: %+v", i, got[i])
		}
	}
	// An unreported quarter carries nothing and breaks the run.
	gap := AnnotateEarningsStreaks([]EarningsResponse{
		er("2024-03-01", f(2), f(1)), er("2023-12-01", f(2), nil), er("2023-09-01", f(2), f(1)),
	})
	if gap[0].BeatStreak != 1 || gap[1].BeatStreak != 0 || gap[1].MissStreak != 0 || gap[2].BeatStreak != 1 {
		t.Fatalf("gap %+v", gap)
	}
	if len(AnnotateEarningsStreaks(nil)) != 0 {
		t.Fatal("empty")
	}
}

func TestBuildRecentEarnings(t *testing.T) {
	history := AnnotateEarningsStreaks([]EarningsResponse{
		er("2025-01-30", nil, f(2)), // next scheduled
		er("2025-04-30", nil, f(2)), // later scheduled, not "next"
		er("2024-10-30", f(2), f(1)), er("2024-07-30", f(2), f(1)), er("2024-04-30", f(2), f(1)),
		er("2024-01-30", f(2), f(1)), er("2023-10-30", f(2), f(1)),
		er("2023-07-30", nil, nil), // gap before last report: ignored
	})
	got := BuildRecentEarnings("AAPL", history, "2025-01-01")
	if len(got.Quarters) != 4 || got.Quarters[0].Date != "2024-10-30" || got.Quarters[0].BeatStreak != 5 || got.Quarters[0].Status != "reported" {
		t.Fatalf("quarters %+v", got.Quarters)
	}
	if got.NextScheduled == nil || got.NextScheduled.Date != "2025-01-30" || got.NextScheduled.Status != "scheduled" {
		t.Fatalf("next %+v", got.NextScheduled)
	}
	// Nothing reported: next is the earliest row not before today.
	fresh := BuildRecentEarnings("NEW", []EarningsResponse{er("2020-01-01", nil, f(1)), er("2025-02-01", nil, f(1)), er("2025-05-01", nil, f(1))}, "2025-01-01")
	if len(fresh.Quarters) != 0 || fresh.NextScheduled == nil || fresh.NextScheduled.Date != "2025-02-01" {
		t.Fatalf("fresh %+v", fresh)
	}
}

// ---- Ownership calculations ------------------------------------------------------

func TestCongressAmountBounds(t *testing.T) {
	cases := []struct {
		in     string
		lo, hi *float64
	}{
		{"$1,001 - $15,000", f(1001), f(15000)},
		{"$50,000,001 +", f(50000001), f(50000001)},
		{"Over $50,000,000", f(50000000), nil},
		{"Under $1,000", nil, f(1000)},
		{"", nil, nil},
		{"Spouse/DC", nil, nil},
		{"$15,000 - $1,001", f(1001), f(15000)},
	}
	for _, c := range cases {
		lo, hi := CongressAmountBounds(c.in)
		if !eqPtr(lo, c.lo) || !eqPtr(hi, c.hi) {
			t.Errorf("%q: got %v %v", c.in, deref(lo), deref(hi))
		}
	}
	if CongressTradeType(strPtr("Sale (Partial)")) != "sale" || CongressTradeType(strPtr("Purchase")) != "purchase" ||
		CongressTradeType(strPtr("Exchange")) != "exchange" || CongressTradeType(nil) != "other" {
		t.Fatal("trade types")
	}
}

func eqPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func strPtr(s string) *string { return &s }

func TestCongressTradeFromWireAndMerge(t *testing.T) {
	if congressTradeFromWire(FMPCongressTrade{Symbol: strPtr(" "), TransactionDate: strPtr("2024-01-01")}, "senate") != nil {
		t.Fatal("blank symbol kept")
	}
	tr := congressTradeFromWire(FMPCongressTrade{
		Symbol: strPtr(" nvda "), TransactionDate: strPtr("2024-01-01"), Office: strPtr("Rep. X"),
		District: strPtr("CA12"), Amount: strPtr("$1,001 - $15,000"), Type: strPtr("purchase"),
	}, "house")
	if tr.Symbol != "NVDA" || tr.Politician != "Rep. X" || *tr.State != "CA" || tr.Type != "purchase" || tr.DisclosureDate != "" {
		t.Fatalf("trade %+v", tr)
	}
	merged := MergeCongressTrades(
		[]CongressTrade{{TransactionDate: "2024-01-01", Politician: "B"}},
		[]CongressTrade{{TransactionDate: "2024-02-01"}, {TransactionDate: "2024-01-01", Politician: "A"}},
	)
	if merged[0].TransactionDate != "2024-02-01" || merged[1].Politician != "A" {
		t.Fatalf("merge %+v", merged)
	}
}

func TestInsiderActivityBuild(t *testing.T) {
	asOf := time.Date(2025, 1, 31, 12, 0, 0, 0, time.UTC)
	trade := func(date, name, code string, shares, price float64) FMPInsiderTrade {
		return FMPInsiderTrade{TransactionDate: &date, ReportingName: &name, TransactionType: &code, SecuritiesTransacted: &shares, Price: &price}
	}
	wire := []FMPInsiderTrade{
		trade("2025-01-20", "Alice", "P-Purchase", 100, 10),
		trade("2025-01-10", "Bob", "P", 50, 10),
		trade("2025-01-01", "carol ", "P-Purchase", 10, 0),
		trade("2025-01-15", "Dan", "S-Sale", 30, 20),
		trade("2025-01-05", "Eve", "A-Award", 1000, 0),
		trade("2023-01-01", "Old", "P", 5, 1), // outside a 365-day window
		{TransactionDate: nil},
	}
	resp := BuildInsiderActivity("AAPL", 365, wire, asOf)
	if len(resp.Trades) != 5 || resp.Trades[0].Date != "2025-01-20" {
		t.Fatalf("trades %+v", resp.Trades)
	}
	if resp.Summary.Buys != 3 || resp.Summary.Sells != 1 || resp.Summary.NetShares != 130 || resp.Summary.NetValue != 1500-600 {
		t.Fatalf("summary %+v", resp.Summary)
	}
	if resp.Trades[4].Kind != "buy" || resp.Trades[4].PricePerShare != nil || resp.Trades[3].Kind != "award" {
		t.Fatalf("kinds %+v", resp.Trades)
	}
	c := resp.ClusterBuy
	if c == nil || c.InsiderCount != 3 || c.WindowStart != "2025-01-01" || c.WindowEnd != "2025-01-20" || c.TotalValue != 1500 {
		t.Fatalf("cluster %+v", c)
	}
	if ClampInsiderWindow(5) != 30 || ClampInsiderWindow(5000) != 1825 {
		t.Fatal("clamp")
	}
	if InsiderKind(strPtr(" s ")) != "sell" || InsiderKind(strPtr("")) != "other" || InsiderKind(nil) != "other" {
		t.Fatal("kind")
	}
}

func TestInstitutionalQuartersAndBuild(t *testing.T) {
	if q := LatestReportedQuarter(time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC)); q != (MarketQuarter{2024, 4}) || q.Label() != "2024Q4" {
		t.Fatalf("quarter %+v", q)
	}
	if q := LatestReportedQuarter(time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)); q != (MarketQuarter{2025, 2}) {
		t.Fatalf("quarter %+v", q)
	}
	holders := make([]FMPInstitutionalHolder, 0, 12)
	for i := range 12 {
		name := "H" + string(rune('A'+i))
		holders = append(holders, FMPInstitutionalHolder{InvestorName: &name, SharesNumber: f(float64(i))})
	}
	holders = append(holders, FMPInstitutionalHolder{InvestorName: strPtr(" "), SharesNumber: f(999)})
	n := 7
	resp := BuildInstitutionalOwnership("AAPL", MarketQuarter{2024, 4}, holders, &FMPInstitutionalPositionsSummary{InvestorsHolding: &n})
	if len(resp.TopHolders) != 10 || resp.TopHolders[0].Shares != 11 || *resp.HoldersCount != 7 || resp.AsOfQuarter != "2024Q4" {
		t.Fatalf("holders %+v", resp)
	}
}

// ---- Pressure, DCF, tiers, provider selection --------------------------------------

func TestPressureTemperature(t *testing.T) {
	if got := PressureTemperature(1, 0, nil); got != 50 {
		t.Fatalf("flat %v", got)
	}
	if got := PressureTemperature(3, 5, nil); got != 85 {
		t.Fatalf("hot %v", got)
	}
	ins := &MarketPressureInsider{BuyCount: 10, NetShares: 100}
	if got := PressureTemperature(0, 0, ins); got != 65 {
		t.Fatalf("insider tilt %v", got)
	}
	for temp, label := range map[float64]string{10: "heavy selling", 30: "selling", 50: "balanced", 70: "buying", 90: "heavy buying"} {
		if PressureLabel(temp) != label {
			t.Errorf("label %v", temp)
		}
	}
}

func TestCalculateDCFPrice(t *testing.T) {
	if CalculateDCFPrice(nil, f(1), 0.09, 0.025, 0) != nil {
		t.Fatal("no projections")
	}
	p := []YearlyProjectionResponse{{FCF: f(100)}}
	got := CalculateDCFPrice(p, f(10), 0.1, 0, 0)
	// 100/1.1 + (100/0.1)/1.1 = 1000 → per share 100.
	if got == nil || !near(*got, 100, 1e-9) {
		t.Fatalf("dcf %v", got)
	}
}

func TestNormalizeFMPResultLimit(t *testing.T) {
	free := &Service{Tier: TierFree}
	if l, _ := free.NormalizeFMPResultLimit(f2i(50), nil); *l != 5 {
		t.Fatalf("free clamp %d", *l)
	}
	if l, _ := free.NormalizeFMPResultLimit(nil, nil); l != nil {
		t.Fatal("nil limit")
	}
	if _, err := free.NormalizeFMPResultLimit(f2i(0), nil); Status(err) != 400 {
		t.Fatal("zero limit")
	}
	prem := &Service{Tier: TierPremium}
	if l, _ := prem.NormalizeFMPResultLimit(nil, f2i(100)); *l != 100 {
		t.Fatal("premium default")
	}
}

func f2i(v int) *int { return &v }

func TestSelectProviderAndTier(t *testing.T) {
	cases := []struct {
		configured   string
		finnhub, ibk bool
		want         ProviderKind
	}{
		{"", true, true, KindFinnhub},
		{"", false, true, KindIBKR},
		{"", false, false, KindDisabled},
		{"ibkr", true, true, KindIBKR},
		{"IBKR", true, false, KindDisabled},
		{"finnhub", false, true, KindDisabled},
		{"bogus", false, true, KindIBKR},
	}
	for _, c := range cases {
		if got := SelectProvider(c.configured, c.finnhub, c.ibk); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
	if ParseTier(" Premium ") != TierPremium || ParseTier("gold") != TierFree {
		t.Fatal("tier parse")
	}
	if !IsSupportedOnFreeTier("aapl") || IsSupportedOnFreeTier("ASML") {
		t.Fatal("free list")
	}
}

func TestSwiftDoubleString(t *testing.T) {
	for in, want := range map[float64]string{1: "1.0", 0: "0.0", 1730000000: "1730000000.0", 1.5: "1.5", 1730000000.25: "1730000000.25"} {
		if got := SwiftDoubleString(in); got != want {
			t.Errorf("%v: %s", in, got)
		}
	}
}
