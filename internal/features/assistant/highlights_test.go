package assistant

import (
	"math"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/expenses"
	"norviq-api/internal/features/tax"
	"norviq-api/internal/platform/marketdata"
	dec "norviq-api/internal/platform/nsdecimal"
)

func TestMoneyNumber(t *testing.T) {
	cases := map[float64]string{
		0:            "0.00",
		12345.6:      "12,345.60",
		-0.8:         "-0.80",
		1234.5:       "1,234.50",
		999.995:      "1,000.00", // shortest decimal 999.995, half-even rounds the 9 up
		0.125:        "0.12",     // half-even: ties to the even digit
		0.135:        "0.14",
		2.675:        "2.68", // ICU rounds the shortest decimal, not the binary value
		1e6:          "1,000,000.00",
		-1234567.891: "-1,234,567.89",
		0.005:        "0.00",
		0.015:        "0.02",
		100:          "100.00",
		-0.001:       "-0.00",
		1e21:         "1,000,000,000,000,000,000,000.00",
	}
	for in, want := range cases {
		if got := moneyNumber(in); got != want {
			t.Errorf("moneyNumber(%v) = %q, want %q", in, got, want)
		}
	}
	if got := moneyNumber(math.NaN()); got != "NaN" {
		t.Errorf("NaN = %q", got)
	}
	if moneyPercent(17.25) != "17.25%" || moneyPercent(-0.8) != "-0.80%" {
		t.Error("percent")
	}
	if moneyTrend(1) != "up" || moneyTrend(-1) != "down" || moneyTrend(0) != "flat" {
		t.Error("trend")
	}
	got := moneyMonthStart(time.Date(2026, 3, 1, 0, 30, 0, 0, time.FixedZone("x", 3600)))
	if !got.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("month start %v: an instant east of UTC on the 1st is still last month in UTC", got)
	}
}

func TestInsightSystemPromptContinuations(t *testing.T) {
	// The Swift literal's line continuations keep the next line's indentation.
	for _, frag := range []string{
		"You explain a single user's OWN",
		"title or body;   the server renders",
		"Do not give   financial advice",
		"allocation or   rebalancing suggestions",
		"{\n  \"title\": \"<short card title, max ~6 words>\",\n  \"body\": \"<2-4 sentence plain-language summary>\"\n}\n",
	} {
		if !strings.Contains(insightSystemPrompt, frag) {
			t.Errorf("system prompt lacks %q", frag)
		}
	}
	if !strings.HasSuffix(insightSystemPrompt, "the server adds both.") {
		t.Error("no trailing newline after the last line")
	}
}

func TestInsightPortfolioHighlightsFromServerData(t *testing.T) {
	ds := &insightDataset{
		Dashboard: map[string]any{"totalValue": 12345.6, "dailyChange": -100, "dailyChangePercent": -0.8},
		DashboardInsights: map[string]any{
			"savingsRate": 17.25, "budgetStreak": 2, "watchlistCount": 4, "cashBuffer": 1000,
			"financialHealth": map[string]any{"score": 72, "maxScore": 100, "status": "healthy"},
		},
	}
	h := insightPortfolioHighlights(ds)
	if len(h) != 4 || h[0].Value != "12,345.60" || h[1].Value != "-0.80%" || *h[1].Trend != "down" ||
		h[2].Label != "Financial health" || h[2].Value != "72/100" || h[2].Trend != nil || h[3].Value != "17.25%" {
		t.Fatalf("highlights %+v", h)
	}
	if got := insightPortfolioHighlights(&insightDataset{}); len(got) != 0 || got == nil {
		t.Fatalf("no dashboard is an empty, non-nil list: %#v", got)
	}
	// Dashboard without insights: just the two portfolio figures.
	if got := insightPortfolioHighlights(&insightDataset{Dashboard: ds.Dashboard}); len(got) != 2 {
		t.Fatalf("dashboard only %+v", got)
	}
}

func TestInsightExpenseHighlights(t *testing.T) {
	// No report: this month's pillar actuals, or nothing when they sum to 0.
	plans := []expenses.PillarPlanningSummaryResponse{{ActualAmount: 1000}, {ActualAmount: 234.5}}
	if h := insightExpenseHighlights(&insightDataset{BudgetPlanning: plans}); len(h) != 1 || h[0].Value != "1,234.50" || h[0].Label != "Month spending" {
		t.Fatalf("planning only %+v", h)
	}
	if h := insightExpenseHighlights(&insightDataset{BudgetPlanning: []expenses.PillarPlanningSummaryResponse{{ActualAmount: 0}}}); len(h) != 0 {
		t.Fatalf("zero spend shows nothing %+v", h)
	}
	// The last report is the one read; the savings rate floors at 0.
	reports := []expenses.BudgetMonthSummaryResponse{
		{Planned: 1, Actual: 1, Salary: 10},
		{Planned: 2000, Actual: 2500, Salary: 2000},
	}
	h := insightExpenseHighlights(&insightDataset{ExpenseReports: reports})
	if len(h) != 3 || h[0].Value != "2,500.00" || h[1].Label != "Month plan" || h[1].Value != "2,000.00" || h[2].Value != "0.00%" {
		t.Fatalf("reports %+v", h)
	}
	reports[1] = expenses.BudgetMonthSummaryResponse{Planned: 2000, Actual: 1500, Salary: 4000}
	if h := insightExpenseHighlights(&insightDataset{ExpenseReports: reports}); h[2].Value != "62.50%" {
		t.Fatalf("savings rate %+v", h)
	}
	reports[1].Salary = 0
	if h := insightExpenseHighlights(&insightDataset{ExpenseReports: reports}); len(h) != 2 {
		t.Fatalf("no salary, no savings rate %+v", h)
	}
}

func TestInsightCombinedKeepsFour(t *testing.T) {
	ds := &insightDataset{
		Dashboard: map[string]any{"totalValue": 1, "dailyChangePercent": 0},
		DashboardInsights: map[string]any{
			"savingsRate": 1, "financialHealth": map[string]any{"score": 1, "maxScore": 2},
		},
		ExpenseReports: []expenses.BudgetMonthSummaryResponse{{Planned: 5, Actual: 4, Salary: 10}},
	}
	h := insightCombinedHighlights(ds)
	if len(h) != 4 || h[3].Label != "Savings rate" || h[3].Value != "1.00%" || *h[1].Trend != "flat" {
		t.Fatalf("combined %+v", h)
	}
	ds.DashboardInsights = nil
	h = insightCombinedHighlights(ds)
	if len(h) != 4 || h[2].Label != "Month spending" || h[3].Label != "Month plan" {
		t.Fatalf("combined without insights %+v", h)
	}
}

func TestViewSummaryEmptyDatasetsShowNothing(t *testing.T) {
	for _, s := range viewSummaryScopes {
		if h := viewSummaryHighlights(s.name, &viewSummaryDataset{}); h == nil || len(h) != 0 {
			t.Errorf("%s invented a highlight from nothing: %#v", s.name, h)
		}
	}
}

func TestViewSummaryCryptoUsesLiveQuotes(t *testing.T) {
	ds := &viewSummaryDataset{
		CryptoHoldings: []map[string]any{
			{"id": "1", "symbol": "btc", "name": "Bitcoin", "quantity": 2, "averageBuyPrice": 100},
		},
		CryptoQuotes: []map[string]any{
			{"symbol": "BTC", "price": 150, "change": 1},
			{"symbol": "btc", "price": 999, "change": 1}, // first quote per symbol wins
		},
	}
	h := viewSummaryHighlights("crypto", ds)
	if len(h) != 3 || h[0].Value != "1" || h[1].Value != "300.00" || h[2].Value != "50.00%" || *h[2].Trend != "up" {
		t.Fatalf("crypto %+v", h)
	}
	// A missing quote falls back to the purchase price.
	ds.CryptoHoldings = []map[string]any{{"symbol": "ETH", "quantity": 3, "averageBuyPrice": 10}}
	h = viewSummaryHighlights("crypto", ds)
	if len(h) != 3 || h[0].Value != "1" || h[1].Value != "30.00" || h[2].Value != "0.00%" || *h[2].Trend != "flat" {
		t.Fatalf("missing quote %+v", h)
	}
	// Zero cost: no "Since purchase".
	ds.CryptoHoldings = []map[string]any{{"symbol": "X", "quantity": 3, "averageBuyPrice": 0}}
	if h = viewSummaryHighlights("crypto", ds); len(h) != 2 {
		t.Fatalf("zero cost %+v", h)
	}
	ds.CryptoHoldings = []map[string]any{}
	if h = viewSummaryHighlights("crypto", ds); len(h) != 0 {
		t.Fatalf("no holdings %+v", h)
	}
}

func TestViewSummaryMarketsEconomyTax(t *testing.T) {
	ds := &viewSummaryDataset{Market: &marketdata.MarketOverviewResponse{Indices: []marketdata.MarketIndexQuote{
		{Label: "S&P 500", ChangePct: 1.234}, {Label: "Nasdaq", ChangePct: -0.5}, {Label: "Dow", ChangePct: 0}, {Label: "VIX", ChangePct: 9},
	}}}
	h := viewSummaryHighlights("markets", ds)
	if len(h) != 3 || h[0].Label != "S&P 500" || h[0].Value != "1.23%" || *h[1].Trend != "down" || *h[2].Trend != "flat" {
		t.Fatalf("markets %+v", h)
	}

	official := 2.9
	ds = &viewSummaryDataset{Inflation: map[string]any{"headline": map[string]any{"name": "Nowflation", "nowValue": 3.14159, "officialValue": official}}}
	h = viewSummaryHighlights("economy", ds)
	if len(h) != 2 || h[0].Label != "Nowflation" || h[0].Value != "3.14%" || h[1].Label != "Official" || h[1].Value != "2.90%" || h[0].Trend != nil {
		t.Fatalf("economy %+v", h)
	}
	ds.Inflation = map[string]any{"headline": map[string]any{"name": "CPI", "nowValue": 1}}
	if h = viewSummaryHighlights("economy", ds); len(h) != 1 {
		t.Fatalf("no official %+v", h)
	}

	d := tax.DashboardResponse{}
	d.Summary.RealizedEstimatedLiability.Amount = dec.MustParse("1234.565")
	d.Summary.HarvestableLosses.Amount = dec.MustParse("-50")
	h = viewSummaryHighlights("tax", &viewSummaryDataset{Tax: &d})
	if len(h) != 2 || h[0].Label != "Estimated liability" || h[0].Value != "1,234.56" || h[1].Value != "-50.00" {
		t.Fatalf("tax %+v", h)
	}
}

func TestViewSummaryScopesAreDistinct(t *testing.T) {
	names, instr := map[string]bool{}, map[string]bool{}
	for _, s := range viewSummaryScopes {
		names[s.displayName], instr[s.instruction] = true, true
	}
	if len(names) != 8 || len(instr) != 8 {
		t.Fatal("a copy-paste would blur two screens")
	}
	if err := viewSummaryUnknownScope(); err == nil || !strings.Contains(err.Error(), "Unknown scope. Valid scopes: home, portfolio, expenses, crypto, markets, economy, reports, tax") {
		t.Fatalf("unknown scope %v", err)
	}
}

func TestViewSummaryDecodeQuery(t *testing.T) {
	ok := map[string]viewSummaryOptions{
		"":                                {},
		"refresh=true":                    {refresh: true},
		"refresh=ON":                      {refresh: true},
		"refresh=1&refresh=0":             {},
		"refresh":                         {refresh: true},
		"refresh=false":                   {},
		"country=+euro+":                  {country: "EA"},
		"country=br":                      {country: "BR"},
		"country=XX":                      {},
		"country=":                        {},
		"jurisdiction=PT&taxYear=2024":    {},
		"jurisdiction=PT&jurisdiction=ES": {},
		"unknown=1&&":                     {},
		"taxYear=%2B2025":                 {},
	}
	for raw := range ok {
		if _, err := viewSummaryDecodeQuery(raw); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	o, _ := viewSummaryDecodeQuery("jurisdiction=PT&jurisdiction=ES&taxYear=2024")
	if *o.jurisdiction != tax.ES || *o.taxYear != 2024 {
		t.Fatalf("last value wins %+v", o)
	}
	for k, v := range ok {
		got, _ := viewSummaryDecodeQuery(k)
		if got.refresh != v.refresh || got.country != v.country {
			t.Errorf("%q: %+v, want %+v", k, got, v)
		}
	}

	bad := map[string]string{
		"refresh=maybe":         "Value was not of type 'Bool' at path 'refresh'. Data found at 'refresh' was not Bool.",
		"refresh=off":           "Value was not of type 'Bool' at path 'refresh'. Data found at 'refresh' was not Bool.",
		"refresh=":              "Value was not of type 'Bool' at path 'refresh'. Data found at 'refresh' was not Bool.",
		"country":               "No value found (expected type 'String') at path 'country'. No String was found at 'country'.",
		"jurisdiction=us":       "Data corrupted at path 'jurisdiction'. Cannot initialize TaxJurisdiction from invalid String value us.",
		"jurisdiction":          "No value found (expected type 'String') at path 'jurisdiction'. No String was found at 'jurisdiction'.",
		"taxYear=abc":           "Value was not of type 'Int' at path 'taxYear'. Data found at 'taxYear' was not Int.",
		"taxYear":               "No value found (expected type 'Int') at path 'taxYear'. No Int was found at 'taxYear'.",
		"taxYear=x&refresh=bad": "Value was not of type 'Bool' at path 'refresh'. Data found at 'refresh' was not Bool.",
	}
	for raw, want := range bad {
		_, err := viewSummaryDecodeQuery(raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", raw, err, want)
		}
	}
}

func TestInsightParseNarrative(t *testing.T) {
	s := func(v string) *string { return &v }
	if insightParseNarrative(nil) != nil || insightParseNarrative(s("")) != nil || insightParseNarrative(s("[]")) != nil ||
		insightParseNarrative(s(`{"title":"a"}`)) != nil || insightParseNarrative(s(`{"title":"a","body":null}`)) != nil ||
		insightParseNarrative(s(`{"title":"a","body":1}`)) != nil || insightParseNarrative(s(`{"Title":"a","Body":"b"}`)) != nil ||
		insightParseNarrative(s(`{"title":"  \n","body":"b"}`)) != nil || insightParseNarrative(s(`{"title":"a","body":"b"} x`)) != nil {
		t.Fatal("unusable replies must be nil")
	}
	n := insightParseNarrative(s(`{"title":"  Hi  ","body":"\n Body \n","highlights":[{"label":"x","value":"$9"}]}`))
	if n == nil || n.title != "Hi" || n.body != "Body" {
		t.Fatalf("parsed %+v", n)
	}
	long := strings.Repeat("é", 100)
	if n = insightParseNarrative(s(`{"title":"` + long + `","body":"` + strings.Repeat("x", 1300) + `"}`)); graphemeLen(n.title) != 80 || len(n.body) != 1200 {
		t.Fatalf("clamped %d %d", graphemeLen(n.title), len(n.body))
	}
}
