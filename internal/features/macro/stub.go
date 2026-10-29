package macro

import (
	"math"
	"time"
)

// Legacy Phase-1 stub data (MacroStubData), the last-resort fallback while
// MACRO_ALLOW_STUB_FALLBACK is on. Every response is marked source "stub".

func isoNow(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z07:00") }

// round2 is Swift's `(x * 100).rounded() / 100`.
func round2(x float64) float64 { return math.Round(x*100) / 100 }

func gauge(name string, now float64, official *float64, officialAsOf *string, gap *float64) InflationGauge {
	return InflationGauge{Name: name, NowValue: now, OfficialValue: official, OfficialAsOf: officialAsOf, Gap: gap, Unit: "percent"}
}

func comp(category string, ours float64, bls float64, weight *float64) InflationComponent {
	return InflationComponent{Category: category, OurYoY: ours, BlsYoY: ptr(bls), CpiWeight: weight}
}

func mover(category string, yoy float64, mom, weight *float64, direction string) TopMover {
	return TopMover{Category: category, ChangeYoY: yoy, ChangeMoM: mom, Weight: weight, Direction: ptr(direction)}
}

// StubSnapshot is MacroStubData.snapshot(country:).
func StubSnapshot(c Country, now time.Time) InflationSnapshot {
	switch c {
	case US:
		return stubUS(now)
	case BR:
		return stubBR(now)
	default:
		return stubEuro(now, c)
	}
}

// StubSeries is MacroStubData.series(country:seriesKey:).
func StubSeries(c Country, seriesKey string) SeriesResponse {
	base := 2.3
	switch c {
	case US:
		base = 1.71
	case BR:
		base = 4.3
	}
	dates := []string{"2026-02-01", "2026-03-01", "2026-04-01", "2026-05-01", "2026-06-01", "2026-07-01"}
	steps := []float64{0, 0.01, 0.02, 0.03, 0.04, 0.05}
	points := make([]SeriesPoint, len(dates))
	for i, d := range dates {
		v := base
		if i > 0 {
			v = base + steps[i]
		}
		points[i] = SeriesPoint{Date: d, Value: v, Series: seriesKey}
	}
	return SeriesResponse{Series: seriesKey, Points: points, Country: ptr(string(c)), Unit: ptr("percent")}
}

func stubIndicator(name string, value float64, asOf string) *Indicator {
	return &Indicator{Name: name, Value: value, Unit: "percent", AsOf: asOf, Source: "stub"}
}

// StubFedWatch is MacroStubData.fedWatch().
func StubFedWatch(now time.Time) FedWatchResponse {
	return FedWatchResponse{
		AsOf:      "2026-05-01",
		UpdatedAt: isoNow(now),
		Source:    "stub",
		CorePCE: Indicator{
			Name: "Core PCE", Value: 3.41, Unit: "percent", AsOf: "2026-05-01",
			PreviousValue: ptr(3.38), ChangeFromPrevious: ptr(0.03), Source: "stub",
		},
		FedTarget:        2.0,
		DistanceToTarget: 1.41,
		TrimmedMeanCPI:   stubIndicator("Trimmed Mean CPI", 2.41, "2026-05-01"),
		Treasury2Y:       stubIndicator("2Y Treasury", 4.19, "2026-07-07"),
		Treasury10Y:      stubIndicator("10Y Treasury", 4.55, "2026-07-07"),
		Spread10Y2Y:      ptr(0.36),
		Real10Y:          stubIndicator("Real 10Y (TIPS)", 0.48, "2026-07-07"),
		NextFOMC:         NextFOMCMeeting(now),
		Stance:           ptr("neutral"),
	}
}

type stubReading struct {
	price *float64
	yoy   float64
}

var stubReadings = map[string]stubReading{
	"eggs": {ptr(3.12), 4.1}, "milk": {ptr(4.05), 2.2}, "bread": {ptr(2.01), 1.4},
	"gasoline": {ptr(3.42), 21.3}, "chicken": {ptr(2.08), 3.0}, "ground-beef": {ptr(5.55), 4.6},
	"electricity": {ptr(0.182), 5.9}, "coffee": {ptr(7.90), 6.2},
}

// StubItems is MacroStubData.items(country:).
func StubItems(c Country, now time.Time) ItemsResponse {
	catalog := ItemsFor(c)
	items := make([]ItemDTO, len(catalog))
	for i, it := range catalog {
		reading, ok := stubReadings[it.ID]
		var price *float64
		yoy := 2.5
		if ok {
			if it.IsPrice() {
				price = reading.price
			}
			yoy = reading.yoy
		}
		items[i] = ItemDTO{
			ID: it.ID, Name: it.Name, Emoji: ptr(it.Emoji), Country: string(c), Currency: c.Currency(),
			Unit: it.Unit, LatestPrice: price, ChangeYoY: ptr(yoy), AsOf: ptr("2026-06-01"),
			Source: "stub", HasSeries: false,
		}
	}
	return ItemsResponse{Country: string(c), UpdatedAt: isoNow(now), Items: items}
}

func stubUS(now time.Time) InflationSnapshot {
	headline := InflationGauge{
		Name: "Nowflation CPI", NowValue: 1.74, OfficialValue: ptr(4.2), OfficialAsOf: ptr("2026-05"),
		Gap: ptr(-2.46), Unit: "percent", ColVariant: ptr(1.47), CumulativeSinceBase: ptr(33.9), BasePeriod: ptr("2018-01"),
	}
	may := ptr("2026-05")
	gauges := []InflationGauge{
		headline,
		gauge("Core CPI (Nowflation Forecast)", 2.87, ptr(2.9), may, ptr(-0.03)),
		gauge("Supercore (svcs ex-shelter)", 2.01, nil, nil, nil),
		gauge("PCE (PCE-weighted gauge)", 2.04, ptr(4.07), may, ptr(-2.03)),
		gauge("Core PCE (Fed 2% target)", 3.4, ptr(3.41), may, ptr(-0.01)),
	}
	components := []InflationComponent{
		comp("Shelter: Rent", 0.8, 2.9, ptr(7.5)),
		comp("Shelter: Owned", 3.3, 3.3, ptr(26.5)),
		comp("Motor Fuel", 41.6, 41.6, ptr(3.0)),
		comp("Used Vehicles", -2.0, -2.0, ptr(2.1)),
		comp("New Vehicles", 0.2, 0.2, ptr(3.6)),
		comp("Food at Home", 2.7, 2.7, ptr(8.2)),
		comp("Food Away", 3.5, 3.5, ptr(5.7)),
		comp("Electricity", 8.4, 5.9, ptr(2.8)),
		comp("Utility Gas", -5.6, 3.0, ptr(0.7)),
		comp("Medical Care", 2.6, 2.6, ptr(8.1)),
		comp("Apparel", 4.8, 4.8, ptr(2.5)),
		comp("Recreation", 2.6, 2.6, ptr(5.3)),
		comp("Education & Comm", 0.8, 0.8, ptr(5.5)),
		comp("Everything Else", 2.8, 2.8, ptr(18.5)),
	}
	movers := []TopMover{
		mover("Utility Gas", -5.6, nil, ptr(0.7), "down"),
		mover("Food at Home", 2.7, nil, ptr(8.2), "up"),
		mover("Apparel", 4.8, nil, ptr(2.5), "up"),
		mover("Shelter: Owned", 3.3, nil, ptr(26.5), "up"),
		mover("Everything Else", 2.8, nil, ptr(18.5), "up"),
		mover("Motor Fuel", 41.6, ptr(-12.12), ptr(3.0), "down"),
	}
	return InflationSnapshot{
		Country: "US", Currency: "USD", AsOf: "2026-07-08", UpdatedAt: isoNow(now), Source: "stub",
		Headline: headline, Gauges: gauges, Components: components, TopMovers: movers,
		Notes:              ptr("Stub data — configure FRED_API_KEY for live US macro data."),
		NextPrintCountdown: NextCPIPrint(now, ptr(4.2), BLSReleaseDates),
	}
}

func stubBR(now time.Time) InflationSnapshot {
	june := ptr("2026-06")
	headline := gauge("IPCA", 4.52, ptr(4.52), june, nil)
	gauges := []InflationGauge{
		headline,
		gauge("IPCA Ex-Food and Energy", 3.8, ptr(3.8), june, nil),
		gauge("IPCA Food", 6.1, ptr(6.1), june, nil),
	}
	components := []InflationComponent{
		comp("Alimentação e bebidas", 6.1, 6.1, nil),
		comp("Habitação", 3.9, 3.9, nil),
		comp("Transportes", 2.8, 2.8, nil),
		comp("Saúde e cuidados pessoais", 5.2, 5.2, nil),
		comp("Despesas pessoais", 4.1, 4.1, nil),
		comp("Educação", 5.5, 5.5, nil),
		comp("Comunicação", 1.2, 1.2, nil),
		comp("Vestuário", 3.4, 3.4, nil),
	}
	movers := []TopMover{
		mover("Alimentação e bebidas", 6.1, ptr(0.4), nil, "up"),
		mover("Transportes", 2.8, ptr(-0.3), nil, "up"),
		mover("Habitação", 3.9, ptr(0.1), nil, "up"),
		mover("Saúde e cuidados pessoais", 5.2, ptr(0.2), nil, "up"),
	}
	return InflationSnapshot{
		Country: "BR", Currency: "BRL", AsOf: "2026-07-08", UpdatedAt: isoNow(now), Source: "stub",
		Headline: headline, Gauges: gauges, Components: components, TopMovers: movers,
		Notes: ptr("Dados de demonstração — IPCA acumulado em 12 meses."),
	}
}

func stubEuro(now time.Time, c Country) InflationSnapshot {
	isPortugal := c == PT
	june := ptr("2026-06")
	name := "HICP Euro Area"
	notes := "Stub data — HICP Euro Area."
	if isPortugal {
		name = "HICP Portugal"
		notes = "Dados de demonstração — HICP Portugal."
	}
	headline := gauge(name, 2.5, ptr(2.5), june, nil)
	gauges := []InflationGauge{
		headline,
		gauge("HICP Core (ex food & energy)", 2.8, ptr(2.8), june, nil),
		gauge("HICP Energy", -1.2, ptr(-1.2), june, nil),
	}
	components := []InflationComponent{
		comp("Food and non-alcoholic beverages", 3.1, 3.1, nil),
		comp("Housing, water, electricity, gas", 2.9, 2.9, nil),
		comp("Transport", 1.4, 1.4, nil),
		comp("Restaurants and hotels", 4.2, 4.2, nil),
		comp("Recreation and culture", 2.1, 2.1, nil),
	}
	movers := []TopMover{
		mover("Restaurants and hotels", 4.2, nil, nil, "up"),
		mover("Food and non-alcoholic beverages", 3.1, nil, nil, "up"),
		mover("Housing, water, electricity, gas", 2.9, nil, nil, "up"),
		mover("Energy", -1.2, nil, nil, "down"),
	}
	return InflationSnapshot{
		Country: string(c), Currency: "EUR", AsOf: "2026-07-08", UpdatedAt: isoNow(now), Source: "stub",
		Headline: headline, Gauges: gauges, Components: components, TopMovers: movers,
		Notes: ptr(notes),
	}
}

// SahmRule is MacroHubMath.sahmRule: the 3-month average of unemployment
// minus the minimum 3-month average over the prior 12 months, rounded to
// 2dp. values are chronological; nil with fewer than 15 points.
func SahmRule(values []float64) *float64 {
	if len(values) < 15 {
		return nil
	}
	avgs := make([]float64, 0, len(values)-2)
	for end := 2; end < len(values); end++ {
		avgs = append(avgs, (0+values[end-2]+values[end-1]+values[end])/3.0)
	}
	if len(avgs) < 13 {
		return nil
	}
	current := avgs[len(avgs)-1]
	lookback := avgs[len(avgs)-13 : len(avgs)-1]
	trough := lookback[0]
	for _, v := range lookback[1:] {
		if v < trough {
			trough = v
		}
	}
	return ptr(round2(current - trough))
}

// RiskLabel is MacroHubMath.riskLabel.
func RiskLabel(sahm *float64, officialRecession *bool) string {
	if officialRecession != nil && *officialRecession {
		return "elevated"
	}
	if sahm == nil {
		return "low"
	}
	if *sahm >= 0.50 {
		return "elevated"
	}
	if *sahm >= 0.30 {
		return "watch"
	}
	return "low"
}
