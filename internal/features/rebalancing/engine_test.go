package rebalancing

import (
	"errors"
	"strings"
	"testing"
)

// Ported from RebalancingEngineTests.swift.

func sp(s string) *string { return &s }

func fixtureModel() AllocationModel {
	return AllocationModel{
		ID: "model", PortfolioID: "portfolio", Name: "60 / 40", GroupingMode: GroupingCustom, IsActive: true,
		Revision: 2, BaseCurrency: "USD",
		DefaultTargetThresholdBasisPoints: 500, TotalThresholdBasisPoints: 300,
		FractionalSharesEnabled: true, QuantityIncrement: 0.001, MinimumTradeAmount: 1,
		Buckets: []AllocationTargetBucket{
			{ID: "stocks", Name: "Stocks", TargetBasisPoints: 6000, Leaves: []AllocationTargetLeaf{
				{ID: "stock", Kind: KindSecurity, Symbol: sp("STOCK"), Name: "Stocks", TargetBasisPoints: 6000},
			}},
			{ID: "bonds", Name: "Bonds", TargetBasisPoints: 4000, Leaves: []AllocationTargetLeaf{
				{ID: "bond", Kind: KindSecurity, Symbol: sp("BOND"), Name: "Bonds", TargetBasisPoints: 4000},
			}},
		},
		CreatedAt: "2026-07-14T12:00:00Z",
	}
}

func findTrade(ts []Trade, symbol string) *Trade {
	for i := range ts {
		if ts[i].Symbol == symbol {
			return &ts[i]
		}
	}
	return nil
}

func TestSixtyFortyAtSixtyEightThirtyTwo(t *testing.T) {
	e := Engine{}
	m := fixtureModel()
	snap := Snapshot{Holdings: []Holding{
		{Symbol: "STOCK", Name: "Stocks", Quantity: 68, Price: 1, AverageCost: 0.8},
		{Symbol: "BOND", Name: "Bonds", Quantity: 32, Price: 1, AverageCost: 0.9},
	}, BaseCurrency: "USD", PriceQuality: QualityLive, PricedAt: sp("2026-07-14T12:00:00Z")}
	ov, err := e.Overview("portfolio", &m, snap, 0)
	if err != nil {
		t.Fatal(err)
	}
	sim, err := e.Simulate("portfolio", m, SimulationRequest{ModelID: m.ID, ModelRevision: m.Revision}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if ov.TotalDriftBasisPoints != 800 || sim.DriftBeforeBasisPoints != 800 || sim.DriftAfterBasisPoints != 0 {
		t.Fatalf("drift %d %d %d", ov.TotalDriftBasisPoints, sim.DriftBeforeBasisPoints, sim.DriftAfterBasisPoints)
	}
	if len(sim.Trades) != 2 {
		t.Fatalf("trades %+v", sim.Trades)
	}
	if st := findTrade(sim.Trades, "STOCK"); st.Side != SideSell || st.Notional != 8 {
		t.Fatalf("stock %+v", st)
	}
	if b := findTrade(sim.Trades, "BOND"); b.Side != SideBuy {
		t.Fatalf("bond %+v", b)
	}
	if sim.EstimatedRealizedGainLoss != 1.6 {
		t.Fatalf("gain %v", sim.EstimatedRealizedGainLoss)
	}
	// Sells sort first.
	if sim.Trades[0].Side != SideSell {
		t.Fatalf("order %+v", sim.Trades)
	}
}

func TestUnassignedHoldingContributesToDrift(t *testing.T) {
	e := Engine{}
	m := fixtureModel()
	snap := Snapshot{Holdings: []Holding{
		{Symbol: "STOCK", Quantity: 50, Price: 1, AverageCost: 1},
		{Symbol: "BOND", Quantity: 40, Price: 1, AverageCost: 1},
		{Symbol: "OTHER", Quantity: 10, Price: 1, AverageCost: 1},
	}, BaseCurrency: "USD", PriceQuality: QualityLive}
	ov, err := e.Overview("portfolio", &m, snap, 0)
	if err != nil {
		t.Fatal(err)
	}
	sim, err := e.Simulate("portfolio", m, SimulationRequest{ModelID: m.ID, ModelRevision: m.Revision}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if ov.TotalDriftBasisPoints != 1000 {
		t.Fatalf("drift %d", ov.TotalDriftBasisPoints)
	}
	last := ov.Rows[len(ov.Rows)-1]
	if last.ID != "unassigned" || *last.Children[0].Symbol != "OTHER" {
		t.Fatalf("rows %+v", last)
	}
	if o := findTrade(sim.Trades, "OTHER"); o.Side != SideSell || o.Notional != 10 {
		t.Fatalf("other %+v", o)
	}
	if sim.DriftAfterBasisPoints != 0 {
		t.Fatalf("after %d", sim.DriftAfterBasisPoints)
	}
}

func TestStaleModelRevisionIsRejected(t *testing.T) {
	m := fixtureModel()
	snap := Snapshot{Holdings: []Holding{{Symbol: "STOCK", Quantity: 60, Price: 1}, {Symbol: "BOND", Quantity: 40, Price: 1}}, PriceQuality: QualityLive}
	_, err := Engine{}.Simulate("portfolio", m, SimulationRequest{ModelID: m.ID, ModelRevision: m.Revision - 1}, snap)
	if !errors.Is(err, errStaleModel) {
		t.Fatalf("err %v", err)
	}
	if mapped := MapEngineError(err); !strings.Contains(mapped.Error(), "Reload before simulating") {
		t.Fatalf("mapped %v", mapped)
	}
}

func TestModelRequiresTotalsToMatch(t *testing.T) {
	m := fixtureModel()
	m.Buckets = []AllocationTargetBucket{{ID: "stocks", Name: "Stocks", TargetBasisPoints: 6000, Leaves: []AllocationTargetLeaf{
		{ID: "stock", Kind: KindSecurity, Symbol: sp("STOCK"), Name: "Stocks", TargetBasisPoints: 5000},
	}}}
	if err := (Engine{}).Validate(m); err == nil {
		t.Fatal("expected error")
	}
}

func TestTickerValidationRejectsSpreadsheetFormulas(t *testing.T) {
	m := fixtureModel()
	m.Buckets = []AllocationTargetBucket{{ID: "all", Name: "All", TargetBasisPoints: 10000, Leaves: []AllocationTargetLeaf{
		{ID: "formula", Kind: KindSecurity, Symbol: sp(`=HYPERLINK("https://example.invalid")`), Name: "Unsafe", TargetBasisPoints: 10000},
	}}}
	err := Engine{}.Validate(m)
	var e *EngineError
	if !errors.As(err, &e) || e.Detail != "Security targets require a valid ticker of at most 24 characters." {
		t.Fatalf("err %v", err)
	}
}

func TestValidateReasons(t *testing.T) {
	cases := []struct {
		mutate func(*AllocationModel)
		want   string
	}{
		{func(m *AllocationModel) { m.Name = "  " }, "Model name is required."},
		{func(m *AllocationModel) { m.Buckets = nil }, "At least one allocation bucket is required."},
		{func(m *AllocationModel) { m.TotalThresholdBasisPoints = 0 }, "Thresholds and trade constraints must be positive."},
		{func(m *AllocationModel) { m.Buckets[0].Name = "" }, "Bucket names are required."},
		{func(m *AllocationModel) { z := 0; m.Buckets[0].AlertThresholdBasisPoints = &z }, "Bucket alert thresholds must be between 0.01% and 100%."},
		{func(m *AllocationModel) { m.Buckets[1].Leaves[0].Symbol = sp("stock ") }, "Each security may appear only once."},
		{func(m *AllocationModel) {
			m.Buckets[1].Leaves = []AllocationTargetLeaf{
				{ID: "c1", Kind: KindCash, Name: "Cash", TargetBasisPoints: 2000},
				{ID: "c2", Kind: KindCash, Name: "Cash", TargetBasisPoints: 2000},
			}
		}, "A model can contain only one cash target."},
		{func(m *AllocationModel) { m.Buckets[1].Leaves[0].Kind = KindCash }, "Cash targets cannot have a symbol."},
	}
	for _, c := range cases {
		m := fixtureModel()
		c.mutate(&m)
		err := Engine{}.Validate(m)
		var e *EngineError
		if !errors.As(err, &e) || e.Detail != c.want {
			t.Errorf("want %q got %v", c.want, err)
		}
	}
}

func TestOverviewWithoutModelIsUnavailable(t *testing.T) {
	ov, err := Engine{}.Overview("p", nil, Snapshot{Cash: 10, Holdings: []Holding{{Symbol: "A", Quantity: 2, Price: 2.5}}, PriceQuality: QualityLive}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Severity != SeverityUnavailable || ov.TotalValue != 15 || ov.OpenAlertCount != 3 || ov.Rows == nil || ov.Warnings == nil {
		t.Fatalf("%+v", ov)
	}
}

func TestFundBuysScalesDownAndOversellIsRejected(t *testing.T) {
	m := fixtureModel()
	m.FlatFee = 1
	m.VariableFeeBasisPoints = 25
	snap := Snapshot{Holdings: []Holding{{Symbol: "STOCK", Quantity: 0, Price: 100}, {Symbol: "BOND", Quantity: 0, Price: 50}}, PriceQuality: QualityLive}
	sim, err := Engine{}.Simulate("p", m, SimulationRequest{ModelID: m.ID, ModelRevision: m.Revision, CashFlow: 1000}, snap)
	if err != nil {
		t.Fatal(err)
	}
	total := sim.EstimatedFees
	for _, tr := range sim.Trades {
		total += tr.Notional
	}
	if total > 1000.01 || sim.EstimatedFees <= 0 {
		t.Fatalf("spent %v fees %v", total, sim.EstimatedFees)
	}
	held := Snapshot{Holdings: []Holding{{Symbol: "STOCK", Quantity: 1, Price: 100}, {Symbol: "BOND", Quantity: 1, Price: 50}}, PriceQuality: QualityLive}
	_, err = Engine{}.Simulate("p", m, SimulationRequest{ModelID: m.ID, ModelRevision: m.Revision, Overrides: []TradeOverride{{Symbol: "stock", Amount: -500}}}, held)
	if mapped := MapEngineError(err); mapped.Error() != "A sell override exceeds the available STOCK holding." {
		t.Fatalf("oversell %v", mapped)
	}
	_, err = Engine{}.Simulate("p", m, SimulationRequest{ModelID: m.ID, ModelRevision: m.Revision, Overrides: []TradeOverride{{Symbol: "zzz", Amount: 5}}}, held)
	if mapped := MapEngineError(err); mapped.Error() != "Unknown or duplicate trade override: ZZZ." {
		t.Fatalf("override %v", mapped)
	}
	_, err = Engine{}.Simulate("p", m, SimulationRequest{ModelID: m.ID, ModelRevision: m.Revision, CashFlow: -1000}, held)
	if !errors.Is(err, errInsufficientCash) {
		t.Fatalf("cash %v", err)
	}
}

func TestPDFIsDeterministic(t *testing.T) {
	p := Plan{
		ID: "ID", PortfolioID: "P", Status: PlanDraft, BaseCurrency: "USD", DriftBeforeBasisPoints: 812, EstimatedFees: 1.5,
		Trades: []Trade{{Symbol: "A(B)", Side: SideSell, Quantity: 1, Price: 2, Notional: 2, Currency: "USD"}},
	}
	pdf := string(MinimalPDF(PlanPDFLines(p)))
	for _, want := range []string{"%PDF-1.4\n", `(SELL A\(B\)  1.0000 @ 2.0000  USD 2.0000) Tj ET`, "(Drift: 8.12% -> 0.00%)", "%%EOF", "xref\n0 6\n0000000000 65535 f \n0000000009 00000 n \n"} {
		if !strings.Contains(pdf, want) {
			t.Errorf("missing %q in\n%s", want, pdf)
		}
	}
	if !strings.HasSuffix(pdf, "%%EOF") {
		t.Error("suffix")
	}
}
