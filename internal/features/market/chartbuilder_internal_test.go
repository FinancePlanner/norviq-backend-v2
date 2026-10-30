package market

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }

func vals(vs ...any) []*float64 {
	out := make([]*float64, len(vs))
	for i, v := range vs {
		if v != nil {
			out[i] = f64(float64(v.(int)))
		}
	}
	return out
}

func floats(t *testing.T, got []*float64) []any {
	t.Helper()
	out := make([]any, len(got))
	for i, v := range got {
		if v != nil {
			out[i] = *v
		}
	}
	return out
}

// statement decodes a fixture row the way the Swift tests built theirs.
func statement[T any](t *testing.T, fields map[string]any) T {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func base(symbol, date, fy, period string) map[string]any {
	return map[string]any{"symbol": symbol, "date": date, "reportedCurrency": "USD", "fiscalYear": fy, "period": period}
}

func income(t *testing.T, symbol, date, fy, period string, extra map[string]any) marketdata.IncomeStatementResponse {
	f := base(symbol, date, fy, period)
	for k, v := range extra {
		f[k] = v
	}
	return statement[marketdata.IncomeStatementResponse](t, f)
}

// ---- ChartBuilderRegistryTests ----

func TestChartCatalogAndBindingsCoverSameKeys(t *testing.T) {
	if len(chartCatalog) != len(chartBindings) {
		t.Fatalf("catalog %d keys, bindings %d", len(chartCatalog), len(chartBindings))
	}
	for _, d := range chartCatalog {
		if _, ok := chartBindings[d.Key]; !ok {
			t.Errorf("no binding for %s", d.Key)
		}
	}
}

// fullRow sets every *float64 field of every statement to 1.
func fullRow() *chartPeriodRow {
	fill := func(v any) {
		rv := reflect.ValueOf(v).Elem()
		for i := range rv.NumField() {
			if rv.Field(i).Type() == reflect.TypeFor[*float64]() {
				rv.Field(i).Set(reflect.ValueOf(f64(1)))
			}
		}
	}
	r := &chartPeriodRow{
		income: &marketdata.IncomeStatementResponse{}, balance: &marketdata.BalanceSheetStatementResponse{},
		cashFlow: &marketdata.CashFlowStatementResponse{}, ratios: &marketdata.RatiosResponse{}, growth: &marketdata.FinancialGrowthResponse{},
	}
	fill(r.income)
	fill(r.balance)
	fill(r.cashFlow)
	fill(r.ratios)
	fill(r.growth)
	return r
}

func TestEveryCatalogKeyExtracts(t *testing.T) {
	row := fullRow()
	for _, d := range chartCatalog {
		if chartBindings[d.Key].extract(row) == nil {
			t.Errorf("extractor returned nil: %s", d.Key)
		}
	}
}

func TestTTMRecipes(t *testing.T) {
	for _, d := range chartCatalog {
		b := chartBindings[d.Key]
		if d.Aggregation == aggRatio && d.SupportsTTM && b.ttm == nil {
			t.Errorf("%s supports TTM but has no component recipe", d.Key)
		}
		if b.ttm != nil {
			for _, ck := range b.ttm {
				if chartCatalogByKey[ck].Aggregation != aggFlow {
					t.Errorf("%s: TTM component %s is not a flow metric", d.Key, ck)
				}
			}
		}
	}
	if got := requiredChartSources([]string{"grossProfitMargin"}, chartPeriodTTM); got != srcIncome {
		t.Fatalf("grossProfitMargin ttm sources = %b", got)
	}
	if got := requiredChartSources([]string{"grossProfitMargin"}, chartPeriodAnnual); got != srcRatios {
		t.Fatalf("grossProfitMargin annual sources = %b", got)
	}
	if got := requiredChartSources([]string{"fcfMargin"}, chartPeriodTTM); got != srcIncome|srcCashFlow {
		t.Fatalf("fcfMargin ttm sources = %b", got)
	}
}

func TestChartCatalogResponse(t *testing.T) {
	r := chartCatalogResponse()
	keys := []string{}
	for _, g := range r.Groups {
		keys = append(keys, g.Key)
	}
	if !reflect.DeepEqual(keys, []string{"incomeStatement", "balanceSheet", "cashFlow", "ratios", "growth", "derived"}) {
		t.Fatalf("groups = %v", keys)
	}
	labels := map[string]string{}
	for _, d := range chartCatalog {
		labels[d.Key] = d.Label
	}
	for key, want := range map[string]string{
		"revenue":         "Revenue",
		"eps":             "EPS",
		"costAndExpenses": "Cost and Expenses",
		// Swift splits every capital, so "CF" reads "C F" (ported as-is).
		"tenYOperatingCFGrowthPerShare":        "10Y Operating C F Growth per Share",
		"netCashProvidedByOperatingActivities": "Net Cash from Operating Activities",
		"tenYRevenueGrowthPerShare":            "10Y Revenue Growth per Share",
		"totalStockholdersEquity":              "Total Stockholders Equity",
		"changeInWorkingCapital":               "Change in Working Capital",
		"fiveYRevenueGrowthPerShare":           "5Y Revenue Growth per Share",
		"priceToFairValue":                     "Price to Fair Value",
		"fcfMargin":                            "FCF Margin",
		"freeCashFlowPerShare":                 "Free Cash Flow per Share",
	} {
		if labels[key] != want {
			t.Errorf("label(%s) = %q, want %q", key, labels[key], want)
		}
	}
}

// ---- request parsing ----

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 400 {
		t.Fatalf("want 400, got %v", err)
	}
	return he.Reason
}

func TestParseChartRequest(t *testing.T) {
	r, err := parseChartRequest("aapl", sp("revenue, freeCashFlow"), sp("Quarter"), ip(8), sp("msft, AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	if r.symbol != "AAPL" || !reflect.DeepEqual(r.metricKeys, []string{"revenue", "freeCashFlow"}) ||
		r.period != chartPeriodQuarter || r.limit != 8 || !reflect.DeepEqual(r.compare, []string{"MSFT"}) {
		t.Fatalf("request = %+v", r)
	}
	r, _ = parseChartRequest("AAPL", sp("revenue,grossProfitMargin"), sp("ttm"), nil, nil)
	if r.period != chartPeriodTTM || r.limit != 20 {
		t.Fatalf("ttm = %+v", r)
	}
	r, _ = parseChartRequest("AAPL", sp("revenue"), nil, ip(500), nil)
	if r.limit != chartMaxLimit || r.period != chartPeriodAnnual {
		t.Fatalf("clamp = %+v", r)
	}
	cases := []struct {
		metrics, period, compare *string
		reason                   string
	}{
		{sp(" , "), nil, nil, "At least one metric is required (metrics=key1,key2)."},
		{nil, nil, nil, "At least one metric is required (metrics=key1,key2)."},
		{sp("revenue,bogus,nope"), nil, nil, "Unknown metric keys: bogus, nope."},
		{sp("revenue"), sp(""), nil, "Invalid period. Use annual, quarter, or ttm."},
		{sp("revenue,currentRatio"), sp("ttm"), nil, "Metrics not supported for TTM: currentRatio."},
		{sp("revenue"), nil, sp("A,B,C,D"), "Chart builder supports at most 3 compare symbols."},
		{sp(strings.Repeat("revenue,", 21)), nil, nil, "Chart builder supports at most 20 metrics per request."},
	}
	for _, c := range cases {
		_, err := parseChartRequest("AAPL", c.metrics, c.period, nil, c.compare)
		if got := reasonOf(t, err); got != c.reason {
			t.Errorf("reason = %q, want %q", got, c.reason)
		}
	}
}

// ---- TTM math and growth ----

func TestRollingFourQuarterSums(t *testing.T) {
	got := rollingFourQuarterSums(vals(10, 20, 30, 40, 50), []string{"2023-12-30", "2024-03-30", "2024-06-29", "2024-09-28", "2024-12-28"})
	if !reflect.DeepEqual(floats(t, got), []any{nil, nil, nil, 100.0, 140.0}) {
		t.Fatalf("sums = %v", floats(t, got))
	}
	got = rollingFourQuarterSums(vals(10, nil, 30, 40), []string{"2023-12-30", "2024-03-30", "2024-06-29", "2024-09-28"})
	if !reflect.DeepEqual(floats(t, got), []any{nil, nil, nil, nil}) {
		t.Fatalf("missing = %v", floats(t, got))
	}
	got = rollingFourQuarterSums(vals(10, 20, 30, 40), []string{"2023-06-30", "2024-03-30", "2024-06-29", "2024-09-28"})
	if !reflect.DeepEqual(floats(t, got), []any{nil, nil, nil, nil}) {
		t.Fatalf("gap = %v", floats(t, got))
	}
}

func annualPeriods(from, to int) []ChartBuilderPeriod {
	out := []ChartBuilderPeriod{}
	for y := from; y <= to; y++ {
		ys := strconv.Itoa(y)
		out = append(out, ChartBuilderPeriod{Label: "FY" + ys, FiscalYear: sp(ys), FiscalPeriod: sp("FY"), EndDate: ys + "-09-28"})
	}
	return out
}

func TestChartGrowthStats(t *testing.T) {
	values := []*float64{f64(100), f64(110), f64(121), f64(133.1), f64(146.41)}
	s := chartGrowthStats(values, annualPeriods(2020, 2024), chartPeriodAnnual)
	if len(s.Yoy) != 4 || *s.Yoy[0].Absolute != 10 || math.Abs(*s.Yoy[0].Percent-0.1) > 1e-4 ||
		math.Abs(*s.TotalChange-46.41) > 1e-4 || math.Abs(*s.TotalChangePercent-0.4641) > 1e-4 || math.Abs(*s.Cagr-0.1) > 1e-3 {
		t.Fatalf("stats = %+v", s)
	}
	s = chartGrowthStats(vals(-50, 10, 20, 30), annualPeriods(2021, 2024), chartPeriodAnnual)
	if *s.TotalChange != 80 || s.TotalChangePercent != nil || s.Cagr != nil || s.Yoy[0].Percent != nil {
		t.Fatalf("negative base = %+v", s)
	}
	periods := []ChartBuilderPeriod{}
	for range 8 {
		periods = append(periods, ChartBuilderPeriod{Label: "Q", EndDate: "2023-03-28"})
	}
	s = chartGrowthStats(vals(10, 20, 30, 40, 15, 25, 35, 45), periods, chartPeriodQuarter)
	if len(s.Yoy) != 4 || *s.Yoy[0].Absolute != 5 {
		t.Fatalf("quarterly = %+v", s)
	}
	if chartGrowthStats(vals(nil, nil), periods, chartPeriodAnnual) != nil {
		t.Fatal("all-nil series should have no stats")
	}
}

// ---- assembly ----

func usd() map[string]ChartBuilderCompany {
	return map[string]ChartBuilderCompany{
		"AAPL": {Symbol: "AAPL", Name: sp("Apple Inc."), Currency: sp("USD")},
		"MSFT": {Symbol: "MSFT", Name: sp("Microsoft"), Currency: sp("USD")},
	}
}

func TestAssembleAnnual(t *testing.T) {
	b := chartBundle{}
	for _, row := range []struct {
		date, fy string
		rev, fcf float64
	}{{"2022-09-24", "2022", 394e9, 111e9}, {"2023-09-30", "2023", 383e9, 99e9}, {"2024-09-28", "2024", 391e9, 108e9}} {
		b.income = append(b.income, income(t, "AAPL", row.date, row.fy, "FY", map[string]any{"revenue": row.rev}))
		f := base("AAPL", row.date, row.fy, "FY")
		f["freeCashFlow"] = row.fcf
		b.cashFlow = append(b.cashFlow, statement[marketdata.CashFlowStatementResponse](t, f))
	}
	r := chartRequest{symbol: "AAPL", metricKeys: []string{"revenue", "freeCashFlow"}, period: chartPeriodAnnual, limit: 10}
	resp := assembleChart(r, map[string]chartBundle{"AAPL": b}, usd())
	labels := []string{}
	for _, p := range resp.Periods {
		labels = append(labels, p.Label)
	}
	if !reflect.DeepEqual(labels, []string{"FY2022", "FY2023", "FY2024"}) || len(resp.Series) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
	rev := resp.Series[0]
	if !reflect.DeepEqual(floats(t, rev.Values), []any{394e9, 383e9, 391e9}) || *rev.Currency != "USD" || rev.Growth == nil {
		t.Fatalf("revenue = %+v", rev)
	}
}

func TestAssemblePeerAlignsByFiscalYear(t *testing.T) {
	primary := chartBundle{income: []marketdata.IncomeStatementResponse{
		income(t, "AAPL", "2023-09-30", "2023", "FY", map[string]any{"revenue": 383}),
		income(t, "AAPL", "2024-09-28", "2024", "FY", map[string]any{"revenue": 391}),
	}}
	peer := chartBundle{income: []marketdata.IncomeStatementResponse{
		income(t, "MSFT", "2023-06-30", "2023", "FY", map[string]any{"revenue": 211}),
	}}
	r := chartRequest{symbol: "AAPL", metricKeys: []string{"revenue"}, period: chartPeriodAnnual, limit: 10, compare: []string{"MSFT"}}
	resp := assembleChart(r, map[string]chartBundle{"AAPL": primary, "MSFT": peer}, usd())
	if got := floats(t, resp.Series[1].Values); resp.Series[1].Symbol != "MSFT" || !reflect.DeepEqual(got, []any{211.0, nil}) {
		t.Fatalf("peer = %v", got)
	}
	if len(resp.Companies) != 2 {
		t.Fatalf("companies = %+v", resp.Companies)
	}
}

func TestAssembleTTM(t *testing.T) {
	b := chartBundle{}
	for _, q := range []struct {
		date, fy, p string
		rev, gross  float64
	}{
		{"2023-07-01", "2023", "Q3", 100, 20},
		{"2023-09-30", "2023", "Q4", 110, 22},
		{"2023-12-30", "2024", "Q1", 120, 24},
		{"2024-03-30", "2024", "Q2", 130, 26},
		{"2024-06-29", "2024", "Q3", 140, 28},
	} {
		b.income = append(b.income, income(t, "AAPL", q.date, q.fy, q.p, map[string]any{"revenue": q.rev, "grossProfit": q.gross}))
		f := base("AAPL", q.date, q.fy, q.p)
		f["totalAssets"] = q.rev * 10
		b.balance = append(b.balance, statement[marketdata.BalanceSheetStatementResponse](t, f))
	}
	r := chartRequest{symbol: "AAPL", metricKeys: []string{"revenue", "totalAssets", "grossProfitMargin"}, period: chartPeriodTTM, limit: 2}
	resp := assembleChart(r, map[string]chartBundle{"AAPL": b}, usd())
	if resp.Periods[0].Label != "TTM Q2 2024" || resp.Periods[1].Label != "TTM Q3 2024" || len(resp.Periods) != 2 {
		t.Fatalf("periods = %+v", resp.Periods)
	}
	if got := floats(t, resp.Series[0].Values); !reflect.DeepEqual(got, []any{460.0, 500.0}) {
		t.Fatalf("revenue = %v", got)
	}
	if got := floats(t, resp.Series[1].Values); !reflect.DeepEqual(got, []any{1300.0, 1400.0}) {
		t.Fatalf("assets = %v", got)
	}
	for _, v := range resp.Series[2].Values {
		if v == nil || math.Abs(*v-0.2) > 1e-4 {
			t.Fatalf("margin = %v", floats(t, resp.Series[2].Values))
		}
	}
	if resp.Series[2].Currency != nil {
		t.Fatal("percent series carries no currency")
	}
}

// ---- CSV ----

func csvResponse(comparing bool) ChartBuilderResponse {
	series := []ChartBuilderSeries{
		{Symbol: "AAPL", MetricKey: "revenue", Label: "Revenue", Format: fmtCurrency, Values: []*float64{f64(394e9), nil}},
		{Symbol: "AAPL", MetricKey: "netProfitMargin", Label: "Net Profit Margin", Format: fmtPercent, Values: []*float64{f64(0.2531), f64(0.24)}},
	}
	if comparing {
		series = append(series, ChartBuilderSeries{Symbol: "MSFT", MetricKey: "revenue", Label: "Revenue", Format: fmtCurrency, Values: []*float64{f64(211e9), f64(245e9)}})
	}
	return ChartBuilderResponse{Period: chartPeriodAnnual, Series: series, Periods: []ChartBuilderPeriod{
		{Label: "FY2022", FiscalYear: sp("2022"), FiscalPeriod: sp("FY"), EndDate: "2022-09-24"},
		{Label: "FY2023", FiscalYear: sp("2023"), FiscalPeriod: sp("FY"), EndDate: "2023-09-30"},
	}}
}

func TestChartCSV(t *testing.T) {
	data := makeChartCSV(csvResponse(false))
	if !reflect.DeepEqual(data[:3], []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("missing BOM")
	}
	lines := strings.Split(string(data[3:]), "\n")
	if lines[0] != "Period,Fiscal Year,Revenue,Net Profit Margin (%)" || lines[1] != "FY2022,2022,394000000000,0.2531" ||
		lines[2] != "FY2023,2023,,0.24" || lines[3] != "" {
		t.Fatalf("csv = %q", lines)
	}
	header := strings.Split(string(makeChartCSV(csvResponse(true))[3:]), "\n")[0]
	if header != "Period,Fiscal Year,AAPL Revenue,AAPL Net Profit Margin (%),MSFT Revenue" {
		t.Fatalf("header = %q", header)
	}
	if joinCSV([]string{`a,b`, `say "hi"`}) != `"a,b","say ""hi"""` {
		t.Fatal("escaping")
	}
	if formatCSVValue(1e15) != "1000000000000000.0" || formatCSVValue(1e16) != "1e+16" || formatCSVValue(-3) != "-3" || formatCSVValue(1234567.5) != "1234567.5" {
		t.Fatalf("values %s %s %s %s", formatCSVValue(1e15), formatCSVValue(1e16), formatCSVValue(-3), formatCSVValue(1234567.5))
	}
}
