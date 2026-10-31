package simulations

import (
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/store/simulationsdb"

	"github.com/google/uuid"
)

// Ported from PortfolioSimulationEngineTests.swift.

type legSpec struct {
	symbol string
	bps    int
}

type fixtureOpts struct {
	mode         string
	fractional   bool
	minimumTrade float64
	flatFee      float64
	variableFee  int
}

func fixture(legs []legSpec, o fixtureOpts) Simulation {
	if o.mode == "" {
		o.mode = ModeFromScratch
	}
	if o.minimumTrade == 0 {
		o.minimumTrade = 1
	}
	s := Simulation{
		ID: "11111111-1111-1111-1111-111111111111", Name: "Simulation", Mode: o.mode, BaseCurrency: "USD",
		TargetCapital: 10000, FractionalSharesEnabled: o.fractional, QuantityIncrement: 0.001,
		MinimumTradeAmount: o.minimumTrade, FlatFee: o.flatFee, VariableFeeBasisPoints: o.variableFee,
		Revision: 1, CreatedAt: "2026-09-12T12:00:00Z",
	}
	if o.mode == ModeCloneCurrentPortfolio {
		p := "portfolio"
		s.SourcePortfolioID = &p
	}
	for i, l := range legs {
		s.Legs = append(s.Legs, Leg{Symbol: l.symbol, TargetBasisPoints: l.bps, SortOrder: i})
	}
	return s
}

func emptySnapshot(priced map[string]float64) rebalancing.Snapshot {
	snap := rebalancing.Snapshot{BaseCurrency: "USD", PriceQuality: rebalancing.QualityLive}
	for sym, p := range priced {
		snap.Holdings = append(snap.Holdings, rebalancing.Holding{Symbol: sym, Name: sym, Price: p})
	}
	return snap
}

func trade(r rebalancing.Simulation, sym string) *rebalancing.Trade {
	for i := range r.Trades {
		if r.Trades[i].Symbol == sym {
			return &r.Trades[i]
		}
	}
	return nil
}

func simulate(t *testing.T, sim Simulation, cashFlow float64, snap rebalancing.Snapshot) (rebalancing.Simulation, error) {
	t.Helper()
	m, err := MakeModel(sim)
	if err != nil {
		t.Fatal(err)
	}
	return rebalancing.Engine{}.Simulate(sim.ID, m, MakeRequest(m, cashFlow), snap)
}

func TestRemainderBecomesCashBucket(t *testing.T) {
	m, err := MakeModel(fixture([]legSpec{{"AAPL", 3000}, {"MSFT", 2000}}, fixtureOpts{}))
	if err != nil {
		t.Fatal(err)
	}
	last := m.Buckets[len(m.Buckets)-1]
	if len(m.Buckets) != 3 || last.ID != CashLeafIdentifier || last.TargetBasisPoints != 5000 || last.Leaves[0].Kind != rebalancing.KindCash || last.Leaves[0].Symbol != nil {
		t.Fatalf("%+v", m.Buckets)
	}
}

func TestFullWeightsOmitCashAndValidate(t *testing.T) {
	m, err := MakeModel(fixture([]legSpec{{"AAPL", 6000}, {"MSFT", 4000}}, fixtureOpts{}))
	if err != nil || len(m.Buckets) != 2 {
		t.Fatalf("%v %+v", err, m.Buckets)
	}
	if err := (rebalancing.Engine{}).Validate(m); err != nil {
		t.Fatal(err)
	}
	m, _ = MakeModel(fixture([]legSpec{{"AAPL", 4000}, {"MSFT", 3500}}, fixtureOpts{}))
	if err := (rebalancing.Engine{}).Validate(m); err != nil {
		t.Fatal(err)
	}
}

func TestOverweightRejected(t *testing.T) {
	_, err := MakeModel(fixture([]legSpec{{"AAPL", 6000}, {"MSFT", 5000}}, fixtureOpts{}))
	var me *ModelError
	if !errors.As(err, &me) || me.Case != "weightsExceedTotal" || me.Total != 11000 {
		t.Fatalf("%v", err)
	}
	if got := abortFor(me).Error(); got != "Weights total 110.0%, which is more than 100%." {
		t.Fatal(got)
	}
}

func TestFractionalToggleMapsToIncrement(t *testing.T) {
	whole, _ := MakeModel(fixture([]legSpec{{"AAPL", 10000}}, fixtureOpts{}))
	frac, _ := MakeModel(fixture([]legSpec{{"AAPL", 10000}}, fixtureOpts{fractional: true}))
	if whole.QuantityIncrement != 1 || frac.QuantityIncrement != 0.001 {
		t.Fatalf("%v %v", whole.QuantityIncrement, frac.QuantityIncrement)
	}
}

func TestFromScratchOnlyBuys(t *testing.T) {
	r, err := simulate(t, fixture([]legSpec{{"AAPL", 4000}, {"MSFT", 3500}, {"NVDA", 2500}}, fixtureOpts{fractional: true}), 10000,
		emptySnapshot(map[string]float64{"AAPL": 100, "MSFT": 50, "NVDA": 25}))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Trades) != 3 || r.TotalValueBefore != 10000 || r.DriftAfterBasisPoints != 0 {
		t.Fatalf("%+v", r)
	}
	for sym, n := range map[string]float64{"AAPL": 4000, "MSFT": 3500, "NVDA": 2500} {
		tr := trade(r, sym)
		if tr.Side != rebalancing.SideBuy || tr.Notional != n || tr.EstimatedRealizedGainLoss != nil {
			t.Fatalf("%s %+v", sym, tr)
		}
	}
}

func TestWholeShareFloors(t *testing.T) {
	r, err := simulate(t, fixture([]legSpec{{"AAPL", 10000}}, fixtureOpts{}), 1000, emptySnapshot(map[string]float64{"AAPL": 231.40}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Trades[0].Quantity != 4 || r.Trades[0].Notional != 925.60 || r.DriftAfterBasisPoints <= 0 {
		t.Fatalf("%+v", r)
	}
}

func TestClonedSellsOverweightBuysUnderweight(t *testing.T) {
	snap := rebalancing.Snapshot{BaseCurrency: "USD", PriceQuality: rebalancing.QualityLive, Holdings: []rebalancing.Holding{
		{Symbol: "AAPL", Quantity: 80, Price: 100, AverageCost: 60},
		{Symbol: "MSFT", Quantity: 20, Price: 100, AverageCost: 90},
	}}
	r, err := simulate(t, fixture([]legSpec{{"AAPL", 5000}, {"MSFT", 5000}}, fixtureOpts{mode: ModeCloneCurrentPortfolio, fractional: true}), 0, snap)
	if err != nil {
		t.Fatal(err)
	}
	a, m := trade(r, "AAPL"), trade(r, "MSFT")
	if a.Side != rebalancing.SideSell || m.Side != rebalancing.SideBuy || a.EstimatedRealizedGainLoss == nil || m.EstimatedRealizedGainLoss != nil || r.DriftAfterBasisPoints != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestClonedSellsNonTargetHolding(t *testing.T) {
	snap := rebalancing.Snapshot{BaseCurrency: "USD", PriceQuality: rebalancing.QualityLive, Holdings: []rebalancing.Holding{
		{Symbol: "AAPL", Quantity: 50, Price: 100, AverageCost: 100},
		{Symbol: "TSLA", Quantity: 10, Price: 100, AverageCost: 100},
	}}
	r, err := simulate(t, fixture([]legSpec{{"AAPL", 10000}}, fixtureOpts{mode: ModeCloneCurrentPortfolio, fractional: true}), 0, snap)
	if err != nil {
		t.Fatal(err)
	}
	if ts := trade(r, "TSLA"); ts.Side != rebalancing.SideSell || ts.Notional != 1000 {
		t.Fatalf("%+v", ts)
	}
}

func TestUnpricedLegFails(t *testing.T) {
	_, err := simulate(t, fixture([]legSpec{{"AAPL", 5000}, {"DELISTED", 5000}}, fixtureOpts{fractional: true}), 10000, emptySnapshot(map[string]float64{"AAPL": 100}))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBelowMinimumNoTrades(t *testing.T) {
	r, err := simulate(t, fixture([]legSpec{{"AAPL", 10000}}, fixtureOpts{fractional: true, minimumTrade: 500}), 100, emptySnapshot(map[string]float64{"AAPL": 100}))
	if err != nil || len(r.Trades) != 0 {
		t.Fatalf("%v %+v", err, r.Trades)
	}
}

func TestFeesReduceDeployableCapital(t *testing.T) {
	r, err := simulate(t, fixture([]legSpec{{"AAPL", 10000}}, fixtureOpts{fractional: true, flatFee: 1, variableFee: 25}), 1000, emptySnapshot(map[string]float64{"AAPL": 100}))
	if err != nil {
		t.Fatal(err)
	}
	if r.EstimatedFees <= 0 || r.Trades[0].Notional+r.EstimatedFees > 1000.01 {
		t.Fatalf("%+v", r)
	}
}

func TestOneHundredLegs(t *testing.T) {
	var legs []legSpec
	prices := map[string]float64{}
	for i := range 100 {
		s := fmt.Sprintf("SYM%d", i)
		legs = append(legs, legSpec{s, 100})
		prices[s] = 10
	}
	r, err := simulate(t, fixture(legs, fixtureOpts{fractional: true}), 100000, emptySnapshot(prices))
	if err != nil || len(r.Trades) != 100 || r.DriftAfterBasisPoints != 0 {
		t.Fatalf("%v %d %d", err, len(r.Trades), r.DriftAfterBasisPoints)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at, id, ok := decodeCursor(base64.StdEncoding.EncodeToString([]byte("1761734400.123456:11111111-1111-1111-1111-111111111111")))
	if !ok || id.String() != "11111111-1111-1111-1111-111111111111" || at.Unix() != 1761734400 {
		t.Fatalf("%v %v %v", at, id, ok)
	}
	for _, bad := range []string{"%%%", "OjE=", "YWJjOmRlZg=="} {
		if _, _, ok := decodeCursor(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// The cursor carries created_at as float seconds. Vapor truncated the
// decoded microseconds, so an anchor could land a microsecond early and the
// next page skipped rows sharing its created_at; v2 rounds back to the exact
// microsecond.
func TestCursorAnchorIsExactMicrosecond(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for i := range 200000 {
		at := base.Add(time.Duration(i*7919) * time.Microsecond)
		raw := encodeCursor(&simulationsdb.PortfolioSimulation{ID: id, CreatedAt: &at})
		got, gotID, ok := decodeCursor(*raw)
		if !ok || gotID != id || !got.Equal(at) {
			t.Fatalf("%s: decoded %s", at.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
		}
	}
}
