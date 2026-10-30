package macro

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Ported from MacroHubMathTests, MacroProviderDecodingTests,
// MacroEuroCoverageTests and MacroProviderSelectionTests.

func TestSahmRule(t *testing.T) {
	if SahmRule([]float64{4.0, 4.1, 4.2}) != nil {
		t.Fatal("short history must be nil")
	}
	values := make([]float64, 14)
	for i := range values {
		values[i] = 3.5
	}
	values = append(values, 3.8, 4.0, 4.2)
	sahm := SahmRule(values)
	if sahm == nil || *sahm < 0.5 {
		t.Fatalf("sahm %v", sahm)
	}
	if RiskLabel(sahm, ptr(false)) != "elevated" {
		t.Fatal("elevated")
	}
	if RiskLabel(ptr(0.1), ptr(true)) != "elevated" || RiskLabel(ptr(0.35), ptr(false)) != "watch" ||
		RiskLabel(ptr(0.1), ptr(false)) != "low" || RiskLabel(nil, nil) != "low" {
		t.Fatal("risk labels")
	}
}

func TestFREDParse(t *testing.T) {
	obs := ParseFREDObservations([][2]string{{"2026-04-01", "3.412345"}, {"2026-05-01", "."}, {"2026-06-01", "2.9"}})
	want := []Observation{{"2026-04-01", 3.41}, {"2026-06-01", 2.9}}
	if !reflect.DeepEqual(obs, want) {
		t.Fatalf("%v", obs)
	}
}

const jsonStatFixture = `{
  "version": "2.0", "class": "dataset",
  "id": ["freq", "unit", "coicop", "geo", "time"],
  "size": [1, 1, 2, 1, 3],
  "dimension": {
    "freq": {"category": {"index": {"M": 0}, "label": {"M": "Monthly"}}},
    "unit": {"category": {"index": {"RCH_A": 0}}},
    "coicop": {"category": {"index": {"CP00": 0, "NRG": 1}}},
    "geo": {"category": {"index": {"PT": 0}}},
    "time": {"category": {"index": {"2026-04": 0, "2026-05": 1, "2026-06": 2}}}
  },
  "value": {"0": 2.2, "1": 2.4, "2": 2.5, "3": -1.0, "5": -1.2}
}`

func TestJSONStatLookup(t *testing.T) {
	ds, err := DecodeJSONStat([]byte(jsonStatFixture))
	if err != nil {
		t.Fatal(err)
	}
	check := func(coicop, time string, want float64, ok bool) {
		t.Helper()
		v, got := ds.Value(map[string]string{"coicop": coicop, "time": time})
		if got != ok || (ok && v != want) {
			t.Fatalf("%s %s: %v %v", coicop, time, v, got)
		}
	}
	check("CP00", "2026-06", 2.5, true)
	check("NRG", "2026-04", -1.0, true)
	check("NRG", "2026-05", 0, false)
	check("CP99", "2026-06", 0, false)
	if !reflect.DeepEqual(ds.CategoryIDs("time"), []string{"2026-04", "2026-05", "2026-06"}) {
		t.Fatal(ds.CategoryIDs("time"))
	}
	// Snake-case category ids survive decoding (map keys are not
	// normalized), so TOT_X_NRG_FOOD stays addressable.
	core, err := DecodeJSONStat([]byte(`{"id":["coicop","time"],"size":[1,1],"dimension":{"s_adj":{"category":{}},"coicop":{"category":{"index":{"TOT_X_NRG_FOOD":0}}},"time":{"category":{"index":{"2026-01":0}}}},"value":{"0":1.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := core.Value(map[string]string{"coicop": "TOT_X_NRG_FOOD", "time": "2026-01"}); !ok || v != 1.5 {
		t.Fatalf("core %v %v", v, ok)
	}
	for _, bad := range []string{`{}`, `{"id":[],"size":[],"dimension":{},"value":{"0":null}}`, `{"id":[],"size":[],"dimension":{"x":{}},"value":{}}`} {
		if _, err := DecodeJSONStat([]byte(bad)); err == nil {
			t.Fatalf("%s decoded", bad)
		}
	}
}

func TestEurostatSnapshot(t *testing.T) {
	ds, _ := DecodeJSONStat([]byte(jsonStatFixture))
	r, err := EurostatBuildResult(ds, PT, "eurostat", time.Unix(1_783_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot
	if s.Country != "PT" || s.Currency != "EUR" || s.Headline.NowValue != 2.5 || *s.Headline.OfficialAsOf != "2026-06" {
		t.Fatalf("%+v", s)
	}
	if s.AsOf != "2026-06-01" || s.Source != "Eurostat HICP (Portugal) (2026-06)" || s.Headline.Name != "HICP Portugal" {
		t.Fatalf("%+v", s)
	}
	var headline []Point
	for _, p := range r.Points {
		if p.SeriesKey == KeyHeadlineCPI {
			headline = append(headline, p)
		}
	}
	if len(headline) != 3 || headline[0].PeriodDate != "2026-04-01" {
		t.Fatalf("%v", headline)
	}
	found := false
	for _, g := range s.Gauges {
		if g.Name == "HICP Energy" && g.NowValue == -1.2 {
			found = true
		}
	}
	if !found {
		t.Fatal("energy gauge")
	}
	ea, _ := EurostatBuildResult(ds, EA, "eurostat", time.Now())
	if ea.Snapshot.Source != "Eurostat HICP + ECB (2026-06)" {
		t.Fatal(ea.Snapshot.Source)
	}
	empty, _ := DecodeJSONStat([]byte(`{"id":[],"size":[],"dimension":{},"value":{}}`))
	if _, err := EurostatBuildResult(empty, PT, "eurostat", time.Now()); err == nil || err.Error() != "Eurostat returned no time periods." {
		t.Fatal(err)
	}
}

func TestSidraParsing(t *testing.T) {
	series := ParseSidraSeries([]map[string]string{
		{"V": "Valor", "D3C": "Mês (Código)"},
		{"V": "4.52", "D3C": "202607"},
		{"V": "...", "D3C": "202606"},
		{"V": "4.42", "D3C": "202605"},
	}, "D3C")
	if !reflect.DeepEqual(series, []SidraReading{{"2026-05-01", 4.42}, {"2026-07-01", 4.52}}) {
		t.Fatalf("%v", series)
	}
	groups := ParseSidraGroups([]map[string]string{
		{"V": "Valor", "D2C": "Variável", "D4C": "Grupo"},
		{"V": "0.4", "D2C": "63", "D4C": "7170"},
		{"V": "6.1", "D2C": "2265", "D4C": "7170"},
		{"V": "-0.3", "D2C": "63", "D4C": "7625"},
		{"V": "-", "D2C": "2265", "D4C": "7625"},
	})
	if *groups["7170"].MoM != 0.4 || *groups["7170"].YoY != 6.1 || *groups["7625"].MoM != -0.3 || groups["7625"].YoY != nil {
		t.Fatalf("%v", groups)
	}
	if _, err := DecodeSidraRows([]byte(`[{"V":null}]`)); err == nil {
		t.Fatal("null value must fail like [[String: String]]")
	}
}

func TestNowflation(t *testing.T) {
	p, err := DecodeNowflation([]byte(`{"as_of":"2026-07-08","gauge":{"yoy":1.74},"col_yoy":"1.47","cumulative":33.9,
		"narrative":"Motor fuel remains the swing factor.","unrelated_new_field":{"x":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if *p.GaugeYoY != 1.74 || *p.ColYoY != 1.47 || *p.CumulativeSinceBase != 33.9 || *p.AsOf != "2026-07-08" ||
		*p.Notes != "Motor fuel remains the swing factor." || p.Forecast != nil {
		t.Fatalf("%+v", p)
	}
	if _, err := DecodeNowflation([]byte(`[1]`)); err == nil {
		t.Fatal("array must fail")
	}

	official := InflationGauge{Name: "CPI (official, BLS)", NowValue: 4.2, OfficialValue: ptr(4.2), OfficialAsOf: ptr("2026-05"), Unit: "percent"}
	base := ProviderResult{Snapshot: InflationSnapshot{
		Country: "US", Currency: "USD", AsOf: "2026-05-01", UpdatedAt: "now", Source: "BLS/BEA via FRED (2026-05)",
		Headline: official, Gauges: []InflationGauge{official}, Components: []InflationComponent{}, TopMovers: []TopMover{},
	}}
	merged := ApplyNowflation(NowflationPayload{GaugeYoY: ptr(1.74), ColYoY: ptr(1.47), AsOf: ptr("2026-07-08")}, base, "nowflation", time.Now())
	h := merged.Snapshot.Headline
	if h.Name != "Nowflation CPI" || h.NowValue != 1.74 || *h.OfficialValue != 4.2 || *h.Gap != -2.46 || *h.BasePeriod != "2018-01" {
		t.Fatalf("%+v", h)
	}
	if merged.Snapshot.Source != "nowflation.com + BLS/BEA via FRED (2026-05)" || len(merged.Snapshot.Gauges) != 1 {
		t.Fatalf("%+v", merged.Snapshot)
	}
	if len(merged.Points) != 1 || merged.Points[0].SeriesKey != KeyNowflationGauge || merged.Points[0].Value != 1.74 {
		t.Fatalf("%v", merged.Points)
	}
	untouched := ApplyNowflation(NowflationPayload{}, base, "nowflation", time.Now())
	if untouched.Snapshot.Headline.Name != "CPI (official, BLS)" || len(untouched.Points) != 0 {
		t.Fatal("no gauge must leave the result")
	}
}

func TestCalendars(t *testing.T) {
	today, _ := time.ParseInLocation("2006-01-02", "2026-07-09", newYork)
	m := NextFOMCMeeting(today)
	if m == nil || m.StartDate != "2026-07-28" || m.DaysRemaining != 19 || m.Odds != nil || !*m.HasPressConference {
		t.Fatalf("%+v", m)
	}
	p := NextCPIPrint(today, ptr(4.2), BLSReleaseDates)
	if p == nil || *p.Date != "2026-07-14" || *p.DaysRemaining != 5 || *p.LastOfficial != 4.2 {
		t.Fatalf("%+v", p)
	}
	if NextFOMCMeeting(time.Date(2028, 6, 1, 0, 0, 0, 0, time.UTC)) != nil {
		t.Fatal("expired table must be nil")
	}
	cal := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nDTSTART;TZID=America/New_York:20260812T083000\nSUMMARY:Consumer Price Index\nEND:VEVENT\n" +
		"BEGIN:VEVENT\nDTSTART:20260814T083000Z\nSUMMARY:Producer Price Index\nEND:VEVENT\n" +
		"BEGIN:VEVENT\r\nDTSTART:20260911T083000Z\r\nSUMMARY:Consumer Price Index\r\nEND:VEVENT\nEND:VCALENDAR"
	if got := ParseBLSReleaseDates(cal); !reflect.DeepEqual(got, []string{"2026-08-12", "2026-09-11"}) {
		t.Fatal(got)
	}
	events := WorldEvents("2026-10-28", "2026-10-30")
	want := []WorldEvent{{"2026-10-28", "Fed rate decision (FOMC)"}, {"2026-10-29", "ECB rate decision"}, {"2026-10-30", "Bank of Japan rate decision"}}
	if !reflect.DeepEqual(events, want) {
		t.Fatal(events)
	}
}

func TestPeriodNormalization(t *testing.T) {
	for in, want := range map[string]string{"2026-04": "2026-04-01", "2026-Q1": "2026-01-01", "2026-Q3": "2026-07-01", "2026-Q4": "2026-10-01", "2026": "2026-01-01"} {
		if got, ok := EurostatPeriodDate(in); !ok || got != want {
			t.Fatalf("%s: %s", in, got)
		}
	}
	for _, in := range []string{"2026-Q5", "2026-13", "nonsense", "2026-06-12"} {
		if _, ok := EurostatPeriodDate(in); ok {
			t.Fatalf("%s accepted", in)
		}
	}
	for in, want := range map[string]string{"2026-06-12": "2026-06-12", "2026-6-2": "2026-06-02"} {
		if got, ok := ECBISODate(in); !ok || got != want {
			t.Fatalf("%s: %s", in, got)
		}
	}
	for _, in := range []string{"2026-06", ""} {
		if _, ok := ECBISODate(in); ok {
			t.Fatalf("%s accepted", in)
		}
	}
}

func TestEurostatHubs(t *testing.T) {
	ds, err := DecodeJSONStat([]byte(`{"id":["freq","purchase","unit","geo","time"],"size":[1,1,1,1,3],
		"dimension":{"freq":{"category":{"index":{"Q":0}}},"purchase":{"category":{"index":{"TOTAL":0}}},
		"unit":{"category":{"index":{"RCH_A_AVG":0}}},"geo":{"category":{"index":{"DE":0}}},
		"time":{"category":{"index":{"2025-Q3":0,"2025-Q4":1,"2026-Q1":2}}}},"value":{"0":3.1,"2":4.7}}`))
	if err != nil {
		t.Fatal(err)
	}
	hub := HubDataset{KeyHPIYoY, "/prc_hpi_q", [][2]string{{"purchase", "TOTAL"}, {"unit", "RCH_A"}}, "percent", "EA20"}
	pts := EurostatHubPoints(ds, hub, DE, "eurostat", time.Now())
	if len(pts) != 2 || pts[0].Country != "DE" || pts[0].SeriesKey != "hpi_yoy" || pts[0].PeriodDate != "2025-07-01" ||
		pts[0].Value != 3.1 || pts[1].PeriodDate != "2026-01-01" || pts[1].Value != 4.7 {
		t.Fatalf("%v", pts)
	}
	geos := map[string]string{KeyUnemployment: "EA21", KeyGDPGrowth: "EA20", KeyGovBond10Y: "EA", KeyHPIYoY: "EA20", KeyConsumerConfidence: "EA20", KeyWageGrowth: "EA20"}
	if len(EurostatHubDatasets) != len(geos) {
		t.Fatal("catalog size")
	}
	for _, h := range EurostatHubDatasets {
		if h.Geo(EA) != geos[h.Key] || h.Geo(DE) != "DE" || h.Geo(PT) != "PT" {
			t.Fatalf("%s geo", h.Key)
		}
	}
	if EA.EurostatGeo() != "EA20" || DE.EurostatGeo() != "DE" {
		t.Fatal("eurostat geo")
	}
}

const sdmxFixture = `{"dataSets":[{"series":{"0:0:0:0:0:0:0":{"observations":{"0":[4.5,0],"1":[null,0],"2":[3.15,0]}}}}],
 "structure":{"dimensions":{"observation":[{"id":"TIME_PERIOD","values":[{"id":"2024-06-12"},{"id":"2025-01-05"},{"id":"2026-03-11"}]}]}}}`

func TestECB(t *testing.T) {
	payload, err := DecodeSDMX([]byte(sdmxFixture))
	if err != nil {
		t.Fatal(err)
	}
	obs := payload.Observations()
	if !reflect.DeepEqual(obs, []SDMXObservation{{"2024-06-12", 4.5}, {"2026-03-11", 3.15}}) {
		t.Fatalf("%v", obs)
	}
	now := time.Now()
	in := ProviderResult{Points: []Point{
		{"ES", KeyPolicyRate, "2020-01-01", 0, "percent", "fred", now},
		{"ES", KeyUnemployment, "2026-01-01", 11.2, "percent", "eurostat", now},
	}}
	merged := ApplyECB(payload, in, ES, "ecb", now)
	policy := 0
	for _, p := range merged.Points {
		if p.SeriesKey == KeyPolicyRate {
			policy++
			if p.Source != "ecb" || p.Country != "ES" {
				t.Fatalf("%+v", p)
			}
		}
	}
	if policy != 2 || len(merged.Points) != 3 {
		t.Fatalf("%v", merged.Points)
	}
	empty, err := DecodeSDMX([]byte(`{"dataSets": [], "structure": null}`))
	if err != nil {
		t.Fatal(err)
	}
	if out := ApplyECB(empty, ProviderResult{}, US, "ecb", now); len(out.Points) != 0 {
		t.Fatal("empty payload")
	}
	if _, err := DecodeSDMX([]byte(`{"structure": null}`)); err == nil {
		t.Fatal("dataSets is required")
	}
}

func TestPlanSelection(t *testing.T) {
	for _, c := range AllCountries {
		if p := Plan(false, true, true)[c]; p.Primary != KindDisabled || p.NowflationEnrichment {
			t.Fatalf("%s %+v", c, p)
		}
	}
	full := Plan(true, true, true)
	if full[US] != (CountryPlan{Primary: KindFRED, NowflationEnrichment: true}) ||
		full[BR] != (CountryPlan{Primary: KindIBGE, Fallback: KindFRED}) {
		t.Fatalf("%+v", full)
	}
	for _, c := range []Country{PT, ES, DE, FR, IT, EA} {
		if full[c] != (CountryPlan{Primary: KindEurostat, Fallback: KindFRED, ECBEnrichment: true}) {
			t.Fatalf("%s %+v", c, full[c])
		}
	}
	noKey := Plan(true, false, true)
	if noKey[US].Primary != KindDisabled || noKey[US].NowflationEnrichment ||
		noKey[BR] != (CountryPlan{Primary: KindIBGE}) || noKey[PT] != (CountryPlan{Primary: KindEurostat, ECBEnrichment: true}) {
		t.Fatalf("%+v", noKey)
	}
	if Plan(true, true, false)[US] != (CountryPlan{Primary: KindFRED}) {
		t.Fatal("fred only")
	}
}

func TestCountryAndSeriesKeys(t *testing.T) {
	for in, want := range map[string]Country{"us": US, " BR ": BR, "EURO": EA, "EZ": EA, "de": DE, " ES ": ES, "FR": FR, "it": IT} {
		if got, ok := ParseCountry(ptr(in)); !ok || got != want {
			t.Fatalf("%q -> %s", in, got)
		}
	}
	if _, ok := ParseCountry(ptr("XX")); ok {
		t.Fatal("XX")
	}
	if _, ok := ParseCountry(nil); ok {
		t.Fatal("nil")
	}
	for _, code := range []string{"US", "PT", "ES", "DE", "FR", "IT"} {
		c, ok := ParseCountry(ptr(code))
		if !ok || Plan(true, true, true)[c].Primary == KindDisabled || c.Currency() == "" || c.DisplayName() == "" {
			t.Fatalf("%s must be a live macro country", code)
		}
	}
	for in, want := range map[string]string{"nowflation_cpi": "nowflation_gauge", "official_cpi": "headline_cpi", "headline": "headline_cpi", "core": "core_cpi", "dgs10": "dgs10", " CPI ": "headline_cpi", "  ": ""} {
		if got := ResolveSeriesKey(ptr(in)); got != want {
			t.Fatalf("%q -> %q", in, got)
		}
	}
	if ResolveSeriesKey(nil) != "headline_cpi" || ResolveSeriesKey(ptr("")) != "headline_cpi" || ItemKey("eggs") != "item.eggs" {
		t.Fatal("defaults")
	}
}

func TestSubtractMonthsClampsDay(t *testing.T) {
	cases := map[string]string{
		"2026-03-31T10:00:00Z": "2026-02-28T10:00:00Z",
		"2026-10-29T12:00:00Z": "2025-10-29T12:00:00Z",
		"2024-12-31T00:00:00Z": "2024-02-29T00:00:00Z",
	}
	months := map[string]int{"2026-03-31T10:00:00Z": 1, "2026-10-29T12:00:00Z": 12, "2024-12-31T00:00:00Z": 10}
	for in, want := range cases {
		ti, _ := time.Parse(time.RFC3339, in)
		if got := subtractMonths(ti, months[in]).Format(time.RFC3339); got != want {
			t.Fatalf("%s -%d: %s", in, months[in], got)
		}
	}
}

func TestDecodeSnapshotStrict(t *testing.T) {
	full, _ := json.Marshal(StubSnapshot(PT, time.Now()))
	if _, ok := decodeSnapshot(full); !ok {
		t.Fatal("full snapshot must decode")
	}
	for _, bad := range []string{`{}`, `{"country":"US"}`, `not json`} {
		if _, ok := decodeSnapshot([]byte(bad)); ok {
			t.Fatalf("%s decoded", bad)
		}
	}
	var m map[string]any
	_ = json.Unmarshal(full, &m)
	m["headline"].(map[string]any)["unit"] = nil
	broken, _ := json.Marshal(m)
	if _, ok := decodeSnapshot(broken); ok {
		t.Fatal("null required gauge key must fail")
	}
}

func TestPersonalCalculator(t *testing.T) {
	snap := StubSnapshot(BR, time.Now())
	start := time.Date(2025, 10, 29, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	r := CalculatePersonalInflation([]ExpenseInput{
		{Category: ptr("Mercado"), Title: "semana", Amount: 100},
		{Category: ptr("  "), Title: "Aluguel", Amount: 300},
		{Title: "Saúde consulta", Amount: 50},
		{Title: "Pets", Amount: 50},
	}, snap, BR, 12, start, end)
	if r.ExpenseCount != 4 || r.TotalSpend != 500 || r.MappedSpend != 450 || r.CoveragePercent != 90 || r.SampleStart != "2025-10-29" {
		t.Fatalf("%+v", r)
	}
	if len(r.Components) != 3 || r.Components[0].Category != "Habitação" || r.Components[0].Spend != 300 ||
		r.Components[2].Category != "Saúde e cuidados pessoais" {
		t.Fatalf("%+v", r.Components)
	}
	if r.PersonalRate == nil || r.Difference == nil || r.EstimatedAnnualImpact == nil {
		t.Fatal("rates")
	}
	none := CalculatePersonalInflation(nil, snap, BR, 3, start, end)
	if none.PersonalRate != nil || none.CoveragePercent != 0 || none.Components == nil {
		t.Fatalf("%+v", none)
	}
}
