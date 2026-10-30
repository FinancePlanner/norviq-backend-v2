package assistant

import (
	"encoding/json"
	"strconv"
	"strings"

	"norviq-api/internal/features/expenses"
	"norviq-api/internal/features/tax"
	"norviq-api/internal/platform/marketdata"
)

// insightHighlight is StockPlanShared.AIInsightHighlight. Trend is "up",
// "down" or "flat", and omitted when nil.
type insightHighlight struct {
	Label string  `json:"label"`
	Value string  `json:"value"`
	Trend *string `json:"trend,omitempty"`
}

func insightTrended(label, value, trend string) insightHighlight {
	return insightHighlight{Label: label, Value: value, Trend: &trend}
}

// insightDataset is AIInsightDataset: the server-selected facts a narrative
// is written from. Each surface loads only what it needs; JSONEncoder omitted
// the nil members, so the model saw a small object. Dashboard and
// DashboardInsights are whatever Sources returned (encoded as-is); the
// slices are omitted only when not loaded (an empty load is still "[]").
type insightDataset struct {
	Dashboard         any                                      `json:"dashboard,omitempty"`
	DashboardInsights any                                      `json:"dashboardInsights,omitempty"`
	ExpenseReports    []expenses.BudgetMonthSummaryResponse    `json:"expenseReports,omitzero"`
	BudgetPlanning    []expenses.PillarPlanningSummaryResponse `json:"budgetPlanning,omitzero"`
}

// insightDecode re-decodes a Sources value (whose JSON is the Swift DTO's)
// into the few fields a highlight needs. A value that will not decode counts
// as absent, which shows no highlight rather than a zero.
func insightDecode(v, out any) bool {
	if v == nil {
		return false
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// AIHighlightBuilders.portfolio.
func insightPortfolioHighlights(ds *insightDataset) []insightHighlight {
	var dashboard struct {
		TotalValue         float64 `json:"totalValue"`
		DailyChangePercent float64 `json:"dailyChangePercent"`
	}
	if ds == nil || !insightDecode(ds.Dashboard, &dashboard) {
		return []insightHighlight{}
	}
	values := []insightHighlight{
		{Label: "Portfolio value", Value: moneyNumber(dashboard.TotalValue)},
		insightTrended("Daily change", moneyPercent(dashboard.DailyChangePercent), moneyTrend(dashboard.DailyChangePercent)),
	}
	var insights struct {
		SavingsRate     float64 `json:"savingsRate"`
		FinancialHealth struct {
			Score    int64 `json:"score"`
			MaxScore int64 `json:"maxScore"`
		} `json:"financialHealth"`
	}
	if insightDecode(ds.DashboardInsights, &insights) {
		values = append(values,
			insightHighlight{
				Label: "Financial health",
				Value: strconv.FormatInt(insights.FinancialHealth.Score, 10) + "/" + strconv.FormatInt(insights.FinancialHealth.MaxScore, 10),
			},
			insightHighlight{Label: "Savings rate", Value: moneyPercent(insights.SavingsRate)},
		)
	}
	return values
}

// AIHighlightBuilders.expenses. Without a report it sums this month's pillar
// actuals; with one it reads the last (most recent) report.
func insightExpenseHighlights(ds *insightDataset) []insightHighlight {
	if ds == nil || len(ds.ExpenseReports) == 0 {
		actual := 0.0
		if ds != nil {
			for i := range ds.BudgetPlanning {
				actual += ds.BudgetPlanning[i].ActualAmount
			}
		}
		if actual == 0 {
			return []insightHighlight{}
		}
		return []insightHighlight{{Label: "Month spending", Value: moneyNumber(actual)}}
	}
	latest := &ds.ExpenseReports[len(ds.ExpenseReports)-1]
	values := []insightHighlight{
		{Label: "Month spending", Value: moneyNumber(latest.Actual)},
		{Label: "Month plan", Value: moneyNumber(latest.Planned)},
	}
	if latest.Salary > 0 {
		savingsRate := max(0, ((latest.Salary-latest.Actual)/latest.Salary)*100)
		values = append(values, insightHighlight{Label: "Savings rate", Value: moneyPercent(savingsRate)})
	}
	return values
}

// AIHighlightBuilders.combined: both sides, first four.
func insightCombinedHighlights(ds *insightDataset) []insightHighlight {
	all := append(insightPortfolioHighlights(ds), insightExpenseHighlights(ds)...)
	if len(all) > 4 {
		all = all[:4]
	}
	return all
}

// viewSummaryCryptoHighlights is cryptoHighlights: holdings valued at the
// live quote, falling back to the average buy price for any symbol the quote
// batch did not cover. The first quote for a symbol wins.
func viewSummaryCryptoHighlights(ds *viewSummaryDataset) []insightHighlight {
	var holdings []struct {
		Symbol          string  `json:"symbol"`
		Quantity        float64 `json:"quantity"`
		AverageBuyPrice float64 `json:"averageBuyPrice"`
	}
	if !insightDecode(ds.CryptoHoldings, &holdings) || len(holdings) == 0 {
		return []insightHighlight{}
	}
	var quotes []struct {
		Symbol string  `json:"symbol"`
		Price  float64 `json:"price"`
	}
	insightDecode(ds.CryptoQuotes, &quotes)
	prices := make(map[string]float64, len(quotes))
	for _, q := range quotes {
		key := strings.ToUpper(q.Symbol)
		if _, seen := prices[key]; !seen {
			prices[key] = q.Price
		}
	}
	value, cost := 0.0, 0.0
	for _, h := range holdings {
		price, ok := prices[strings.ToUpper(h.Symbol)]
		if !ok {
			price = h.AverageBuyPrice
		}
		// Explicit conversions keep each product rounded on its own (no fused
		// multiply-add), as Swift computed it.
		value += float64(h.Quantity * price)
		cost += float64(h.Quantity * h.AverageBuyPrice)
	}
	values := []insightHighlight{
		{Label: "Holdings", Value: strconv.Itoa(len(holdings))},
		{Label: "Crypto value", Value: moneyNumber(value)},
	}
	if !(cost > 0) {
		return values
	}
	change := ((value - cost) / cost) * 100
	return append(values, insightTrended("Since purchase", moneyPercent(change), moneyTrend(change)))
}

// viewSummaryMarketHighlights is marketHighlights: the first three indices.
func viewSummaryMarketHighlights(ds *viewSummaryDataset) []insightHighlight {
	if ds.Market == nil {
		return []insightHighlight{}
	}
	indices := ds.Market.Indices
	if len(indices) > 3 {
		indices = indices[:3]
	}
	out := make([]insightHighlight, 0, len(indices))
	for i := range indices {
		idx := &indices[i]
		out = append(out, insightTrended(idx.Label, moneyPercent(idx.ChangePct), moneyTrend(idx.ChangePct)))
	}
	return out
}

// viewSummaryEconomyHighlights is economyHighlights: the inflation headline
// and, when present, its official figure.
func viewSummaryEconomyHighlights(ds *viewSummaryDataset) []insightHighlight {
	var inflation struct {
		Headline struct {
			Name          string   `json:"name"`
			NowValue      float64  `json:"nowValue"`
			OfficialValue *float64 `json:"officialValue"`
		} `json:"headline"`
	}
	if !insightDecode(ds.Inflation, &inflation) {
		return []insightHighlight{}
	}
	values := []insightHighlight{{Label: inflation.Headline.Name, Value: moneyPercent(inflation.Headline.NowValue)}}
	if inflation.Headline.OfficialValue != nil {
		values = append(values, insightHighlight{Label: "Official", Value: moneyPercent(*inflation.Headline.OfficialValue)})
	}
	return values
}

// viewSummaryTaxHighlights is taxHighlights: Decimal amounts through
// NSDecimalNumber.doubleValue, then MoneyFormat.number.
func viewSummaryTaxHighlights(ds *viewSummaryDataset) []insightHighlight {
	if ds.Tax == nil {
		return []insightHighlight{}
	}
	s := &ds.Tax.Summary
	return []insightHighlight{
		{Label: "Estimated liability", Value: moneyNumber(s.RealizedEstimatedLiability.Amount.Float64())},
		{Label: "Harvestable losses", Value: moneyNumber(s.HarvestableLosses.Amount.Float64())},
	}
}

// viewSummaryDataset is DefaultAIViewSummaryService.ViewDataset.
type viewSummaryDataset struct {
	Insight        *insightDataset                    `json:"insight,omitempty"`
	CryptoHoldings any                                `json:"cryptoHoldings,omitempty"`
	CryptoQuotes   any                                `json:"cryptoQuotes,omitempty"`
	Market         *marketdata.MarketOverviewResponse `json:"market,omitempty"`
	Inflation      any                                `json:"inflation,omitempty"`
	Economy        any                                `json:"economy,omitempty"`
	PolicyWatch    any                                `json:"policyWatch,omitempty"`
	Tax            *tax.DashboardResponse             `json:"tax,omitempty"`
}

// viewSummaryHighlights is DefaultAIViewSummaryService.highlights(for:dataset:).
func viewSummaryHighlights(scope string, ds *viewSummaryDataset) []insightHighlight {
	insight := ds.Insight
	if insight == nil {
		insight = &insightDataset{}
	}
	switch scope {
	case "home":
		return insightCombinedHighlights(insight)
	case "portfolio":
		return insightPortfolioHighlights(insight)
	case "expenses", "reports":
		return insightExpenseHighlights(insight)
	case "crypto":
		return viewSummaryCryptoHighlights(ds)
	case "markets":
		return viewSummaryMarketHighlights(ds)
	case "economy":
		return viewSummaryEconomyHighlights(ds)
	case "tax":
		return viewSummaryTaxHighlights(ds)
	}
	return []insightHighlight{}
}
