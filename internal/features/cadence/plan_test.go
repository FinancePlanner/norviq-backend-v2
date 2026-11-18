package cadence_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/cadence"
)

var planNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func fp(v float64) *float64 { return &v }

func zoneOf(zone string, mult float64) *cadence.ZoneDTO {
	return &cadence.ZoneDTO{Zone: zone, CappedZone: zone, Multiplier: mult, PeriodEnd: "2026-10-31"}
}

func eurQuote() *cadence.PlanQuote {
	return &cadence.PlanQuote{Price: 100, Currency: "EUR", AsOf: planNow}
}

func twoSymbols() []cadence.PlanSymbolInput {
	return []cadence.PlanSymbolInput{
		{Symbol: "VWCE", Held: true, Baseline: fp(200), Zone: zoneOf("x3", 3), Quote: eurQuote(), QuoteToPlan: fp(1)},
		{Symbol: "AAPL", Held: true, Baseline: fp(100), Zone: zoneOf("steady", 1), Quote: eurQuote(), QuoteToPlan: fp(1)},
	}
}

func baseInput(surplus *float64, syms []cadence.PlanSymbolInput) cadence.PlanInput {
	return cadence.PlanInput{Now: planNow, Currency: "EUR", Surplus: surplus, Symbols: syms}
}

func line(t *testing.T, p cadence.PlanDTO, sym string) cadence.PlanLineDTO {
	t.Helper()
	for i := range p.Lines {
		if p.Lines[i].Symbol == sym {
			return p.Lines[i]
		}
	}
	t.Fatalf("no line %s", sym)
	return cadence.PlanLineDTO{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func hasNote(notes []string, n string) bool {
	for _, x := range notes {
		if x == n {
			return true
		}
	}
	return false
}

func TestBuildPlanScalesToSurplus(t *testing.T) {
	p := cadence.BuildPlan(baseInput(fp(500), twoSymbols()))
	v, a := line(t, p, "VWCE"), line(t, p, "AAPL")
	if *v.Amount != 428.57 || *a.Amount != 71.42 {
		t.Fatalf("amounts %v %v", *v.Amount, *a.Amount)
	}
	if p.Totals.Planned != 499.99 || *p.Totals.Unallocated != 0.01 {
		t.Fatalf("totals %+v", p.Totals)
	}
	if p.Totals.Baseline != 300 || p.Totals.Wanted != 700 {
		t.Fatalf("totals %+v", p.Totals)
	}
	if !p.Scaled || p.Scale == nil || !near(*p.Scale, 500.0/700.0) || !hasNote(p.Notes, cadence.NoteScaledToSurplus) {
		t.Fatalf("scaled %v %v %v", p.Scaled, p.Scale, p.Notes)
	}
}

func TestBuildPlanZeroSurplus(t *testing.T) {
	p := cadence.BuildPlan(baseInput(fp(0), twoSymbols()))
	for _, l := range p.Lines {
		if *l.Amount != 0 {
			t.Fatalf("amount %v", *l.Amount)
		}
	}
	if !p.Scaled || *p.Totals.Unallocated != 0 {
		t.Fatalf("scaled %v unalloc %v", p.Scaled, *p.Totals.Unallocated)
	}
}

func TestBuildPlanUnknownSurplus(t *testing.T) {
	in := baseInput(nil, twoSymbols())
	in.SurplusNote = cadence.NoteNoBudget
	p := cadence.BuildPlan(in)
	if *line(t, p, "VWCE").Amount != 600 || *line(t, p, "AAPL").Amount != 100 {
		t.Fatal("amounts")
	}
	if p.Scaled || p.Scale != nil || p.Totals.Unallocated != nil || !hasNote(p.Notes, cadence.NoteNoBudget) {
		t.Fatalf("%+v", p)
	}
}

func TestBuildPlanExtendedPausesLine(t *testing.T) {
	syms := []cadence.PlanSymbolInput{{Symbol: "VWCE", Baseline: fp(200), Zone: zoneOf("extended", 0), Quote: eurQuote(), QuoteToPlan: fp(1)}}
	p := cadence.BuildPlan(baseInput(fp(500), syms))
	l := line(t, p, "VWCE")
	if l.Multiplier != 0 || *l.Amount != 0 || p.Totals.Planned != 0 || *p.Totals.Unallocated != 500 {
		t.Fatalf("%+v %+v", l, p.Totals)
	}
}

func TestBuildPlanNoZoneKeepsBaseline(t *testing.T) {
	syms := []cadence.PlanSymbolInput{
		{Symbol: "AAA", Baseline: fp(200)},
		{Symbol: "BBB", Baseline: fp(200), Zone: zoneOf("insufficient", 0)},
	}
	p := cadence.BuildPlan(baseInput(fp(500), syms))
	a, b := line(t, p, "AAA"), line(t, p, "BBB")
	if a.ZoneStatus != "pending" || a.Multiplier != 1 || *a.Amount != 200 || !hasNote(a.Notes, cadence.NoteNoZoneYet) {
		t.Fatalf("%+v", a)
	}
	if b.ZoneStatus != "insufficient" || b.Multiplier != 1 || *b.Amount != 200 || !hasNote(b.Notes, cadence.NoteInsufficientHistory) {
		t.Fatalf("%+v", b)
	}
}

func TestBuildPlanMissingAutobuy(t *testing.T) {
	syms := append(twoSymbols(), cadence.PlanSymbolInput{Symbol: "MSFT", Held: true, Zone: zoneOf("x2", 2), Quote: eurQuote(), QuoteToPlan: fp(1)})
	p := cadence.BuildPlan(baseInput(fp(500), syms))
	l := line(t, p, "MSFT")
	if l.Zone == nil || l.Wanted != nil || l.Amount != nil || l.Shares != nil || !hasNote(l.Notes, cadence.NoteNoRecurringBuy) {
		t.Fatalf("%+v", l)
	}
	if p.Totals.Wanted != 700 || p.Totals.Baseline != 300 {
		t.Fatalf("%+v", p.Totals)
	}
}

func TestBuildPlanConvertsQuoteCurrency(t *testing.T) {
	mk := func(rate *float64, inc float64) cadence.PlanLineDTO {
		in := baseInput(fp(1000), []cadence.PlanSymbolInput{{
			Symbol: "AAPL", Baseline: fp(428.57), Zone: zoneOf("steady", 1),
			Quote:       &cadence.PlanQuote{Price: 100, Currency: "USD", AsOf: planNow},
			QuoteToPlan: rate,
		}})
		in.QuantityIncrement = inc
		return line(t, cadence.BuildPlan(in), "AAPL")
	}
	if l := mk(fp(0.9), 1); *l.Shares != 4 {
		t.Fatalf("inc1 %v", *l.Shares)
	}
	if l := mk(fp(0.9), 0); *l.Shares != 4.7618 {
		t.Fatalf("inc0 %v", *l.Shares)
	}
	l := mk(nil, 0)
	if l.Shares != nil || !hasNote(l.Notes, cadence.NoteFxUnavailable) || *l.Price != 100 || *l.PriceCurrency != "USD" {
		t.Fatalf("%+v", l)
	}
}

func TestBuildPlanMinimumTrade(t *testing.T) {
	in := baseInput(fp(1000), []cadence.PlanSymbolInput{{Symbol: "AAPL", Baseline: fp(40), Zone: zoneOf("steady", 1), Quote: eurQuote(), QuoteToPlan: fp(1)}})
	in.MinimumTrade = 50
	l := line(t, cadence.BuildPlan(in), "AAPL")
	if *l.Shares != 0 || !hasNote(l.Notes, cadence.NoteBelowMinimumTrade) || *l.Amount != 40 {
		t.Fatalf("%+v", l)
	}
}

func TestBuildPlanQuoteStaleness(t *testing.T) {
	mk := func(q *cadence.PlanQuote) cadence.PlanLineDTO {
		return line(t, cadence.BuildPlan(baseInput(fp(1000), []cadence.PlanSymbolInput{{Symbol: "AAPL", Baseline: fp(100), Zone: zoneOf("steady", 1), Quote: q, QuoteToPlan: fp(1)}})), "AAPL")
	}
	fresh := mk(&cadence.PlanQuote{Price: 100, Currency: "EUR", AsOf: planNow.Add(-71 * time.Hour)})
	if hasNote(fresh.Notes, cadence.NoteQuoteStale) {
		t.Fatal("71h stale")
	}
	stale := mk(&cadence.PlanQuote{Price: 100, Currency: "EUR", AsOf: planNow.Add(-73 * time.Hour)})
	if !hasNote(stale.Notes, cadence.NoteQuoteStale) || stale.Shares == nil {
		t.Fatalf("%+v", stale)
	}
	none := mk(nil)
	if none.Shares != nil || !hasNote(none.Notes, cadence.NoteQuoteUnavailable) {
		t.Fatalf("%+v", none)
	}
}

func TestBuildPlanShape(t *testing.T) {
	syms := twoSymbols() // VWCE, AAPL: unsorted
	p := cadence.BuildPlan(baseInput(fp(1000), syms))
	if p.Lines[0].Symbol != "AAPL" || p.Lines[1].Symbol != "VWCE" {
		t.Fatal("not sorted")
	}
	if p.Month != "2026-11" || p.RuleVersion != cadence.DefaultRules.Version || p.Disclaimer != budgeting.SpendDisclaimer {
		t.Fatalf("%+v", p)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"notes":[]`) {
		t.Fatalf("notes not []: %s", raw)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				lk := strings.ToLower(k)
				for _, bad := range []string{"buy", "sell", "trim", "order"} {
					if strings.Contains(lk, bad) {
						t.Errorf("key %q contains %q", k, bad)
					}
				}
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(tree)
}

func zoneEnding(zone string, mult float64, periodEnd string) *cadence.ZoneDTO {
	z := zoneOf(zone, mult)
	z.PeriodEnd = periodEnd
	return z
}

func TestBuildPlanOutdatedZoneKeepsBaseline(t *testing.T) {
	mk := func(now time.Time, zone *cadence.ZoneDTO) cadence.PlanLineDTO {
		in := baseInput(fp(1000), []cadence.PlanSymbolInput{{Symbol: "AAPL", Baseline: fp(100), Zone: zone, Quote: eurQuote(), QuoteToPlan: fp(1)}})
		in.Now = now
		return line(t, cadence.BuildPlan(in), "AAPL")
	}
	l := mk(planNow, zoneEnding("x4", 4, "2026-08-31"))
	if l.Multiplier != 1 || *l.Amount != 100 || l.ZoneStatus != "outdated" || !hasNote(l.Notes, cadence.NoteZoneOutdated) || l.Zone == nil {
		t.Fatalf("aug %+v", l)
	}
	if l := mk(planNow, zoneEnding("x4", 4, "2026-09-30")); l.Multiplier != 4 || l.ZoneStatus != "ready" || hasNote(l.Notes, cadence.NoteZoneOutdated) {
		t.Fatalf("sep %+v", l)
	}
	if l := mk(planNow, zoneEnding("extended", 0, "2026-10-31")); l.Multiplier != 0 || *l.Amount != 0 {
		t.Fatalf("oct %+v", l)
	}
	dec := time.Date(2026, 12, 1, 0, 5, 0, 0, time.UTC)
	if l := mk(dec, zoneEnding("x4", 4, "2026-10-31")); l.Multiplier != 4 {
		t.Fatalf("dec oct %+v", l)
	}
	if l := mk(dec, zoneEnding("x4", 4, "2026-09-30")); l.Multiplier != 1 || l.ZoneStatus != "outdated" {
		t.Fatalf("dec sep %+v", l)
	}
	if l := mk(planNow, zoneEnding("x4", 4, "garbage")); l.Multiplier != 1 || l.ZoneStatus != "outdated" {
		t.Fatalf("garbage %+v", l)
	}
}

func TestBuildPlanIncrementEpsilon(t *testing.T) {
	mk := func(inc float64) cadence.PlanLineDTO {
		in := baseInput(fp(1000), []cadence.PlanSymbolInput{{
			Symbol: "AAPL", Baseline: fp(110), Zone: zoneOf("steady", 1),
			Quote:       &cadence.PlanQuote{Price: 100, Currency: "USD", AsOf: planNow},
			QuoteToPlan: fp(1.1),
		}})
		in.QuantityIncrement = inc
		return line(t, cadence.BuildPlan(in), "AAPL")
	}
	if l := mk(1); *l.Shares != 1 {
		t.Fatalf("inc1 %v", *l.Shares)
	}
	if l := mk(0.001); *l.Shares != 1 {
		t.Fatalf("inc.001 %v", *l.Shares)
	}
}

func TestBuildPlanZeroPlanPrice(t *testing.T) {
	in := baseInput(fp(1000), []cadence.PlanSymbolInput{{
		Symbol: "AAPL", Baseline: fp(100), Zone: zoneOf("steady", 1),
		Quote: &cadence.PlanQuote{Price: 0, Currency: "EUR", AsOf: planNow}, QuoteToPlan: fp(1),
	}})
	l := line(t, cadence.BuildPlan(in), "AAPL")
	if l.Shares != nil || !hasNote(l.Notes, cadence.NoteQuoteUnavailable) {
		t.Fatalf("%+v", l)
	}
}

func TestBuildPlanUnallocatedNeverNegative(t *testing.T) {
	p := cadence.BuildPlan(baseInput(fp(699.996), twoSymbols()))
	if p.Scaled || p.Totals.Unallocated == nil || *p.Totals.Unallocated != 0 || math.Signbit(*p.Totals.Unallocated) {
		t.Fatalf("%+v %v", p.Totals, p.Scaled)
	}
}
