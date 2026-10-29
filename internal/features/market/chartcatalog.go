package market

import (
	"reflect"
	"strings"
	"unicode"

	"norviq-api/internal/platform/marketdata"
)

// Chart builder metric catalog: a port of StockPlanShared's
// ChartBuilderMetrics.swift (v5.26.0, the version the backend pins) and the
// backend's ChartBuilderMetricResolver.

// Chart metric groups (ChartMetricGroup), in CaseIterable order.
const (
	groupIncomeStatement = "incomeStatement"
	groupBalanceSheet    = "balanceSheet"
	groupCashFlow        = "cashFlow"
	groupRatios          = "ratios"
	groupGrowth          = "growth"
	groupDerived         = "derived"
)

var chartGroups = []struct{ key, title string }{
	{groupIncomeStatement, "Income Statement"},
	{groupBalanceSheet, "Balance Sheet"},
	{groupCashFlow, "Cash Flow"},
	{groupRatios, "Ratios"},
	{groupGrowth, "Growth"},
	{groupDerived, "Derived Metrics"},
}

// ChartMetricAggregation and ChartMetricFormat raw values.
const (
	aggFlow        = "flow"
	aggPointInTime = "pointInTime"
	aggRatio       = "ratio"

	fmtCurrency = "currency"
	fmtPercent  = "percent"
	fmtRatio    = "ratio"
	fmtPerShare = "perShare"
	fmtShares   = "shares"
)

// ChartMetricDescriptor is StockPlanShared's ChartMetricDescriptor.
type ChartMetricDescriptor struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Group       string `json:"group"`
	Format      string `json:"format"`
	Aggregation string `json:"aggregation"`
	SupportsTTM bool   `json:"supportsTTM"`
}

// ChartBuilderMetricGroupDTO is one group of the catalog response.
type ChartBuilderMetricGroupDTO struct {
	Key     string                  `json:"key"`
	Label   string                  `json:"label"`
	Metrics []ChartMetricDescriptor `json:"metrics"`
}

// ChartBuilderMetricCatalogResponse is GET /v1/market/chart-builder/metrics.
type ChartBuilderMetricCatalogResponse struct {
	Groups []ChartBuilderMetricGroupDTO `json:"groups"`
}

var (
	chartCatalog      = buildChartCatalog()
	chartCatalogByKey = func() map[string]ChartMetricDescriptor {
		m := make(map[string]ChartMetricDescriptor, len(chartCatalog))
		for _, d := range chartCatalog {
			m[d.Key] = d
		}
		return m
	}()
)

// chartCatalogResponse is ChartBuilderMetricCatalogResponse.current().
func chartCatalogResponse() ChartBuilderMetricCatalogResponse {
	groups := []ChartBuilderMetricGroupDTO{}
	for _, g := range chartGroups {
		metrics := []ChartMetricDescriptor{}
		for _, d := range chartCatalog {
			if d.Group == g.key {
				metrics = append(metrics, d)
			}
		}
		if len(metrics) == 0 {
			continue
		}
		groups = append(groups, ChartBuilderMetricGroupDTO{Key: g.key, Label: g.title, Metrics: metrics})
	}
	return ChartBuilderMetricCatalogResponse{Groups: groups}
}

var (
	incomeCurrencyKeys = []string{
		"revenue", "costOfRevenue", "grossProfit",
		"researchAndDevelopmentExpenses", "generalAndAdministrativeExpenses",
		"sellingAndMarketingExpenses", "sellingGeneralAndAdministrativeExpenses",
		"otherExpenses", "operatingExpenses", "costAndExpenses",
		"netInterestIncome", "interestIncome", "interestExpense",
		"ebitda", "ebit", "nonOperatingIncomeExcludingInterest",
		"operatingIncome", "totalOtherIncomeExpensesNet",
		"incomeBeforeTax", "incomeTaxExpense",
		"netIncomeFromContinuingOperations", "netIncomeFromDiscontinuedOperations",
		"otherAdjustmentsToNetIncome", "netIncome", "netIncomeDeductions",
		"bottomLineNetIncome",
	}
	balanceKeys = []string{
		"cashAndCashEquivalents", "shortTermInvestments", "cashAndShortTermInvestments",
		"netReceivables", "accountsReceivables", "otherReceivables",
		"inventory", "prepaids", "otherCurrentAssets", "totalCurrentAssets",
		"propertyPlantEquipmentNet", "goodwill", "intangibleAssets",
		"goodwillAndIntangibleAssets", "longTermInvestments", "taxAssets",
		"otherNonCurrentAssets", "totalNonCurrentAssets", "otherAssets", "totalAssets",
		"totalPayables", "accountPayables", "otherPayables", "accruedExpenses",
		"shortTermDebt", "capitalLeaseOblationsCurrent", "taxPayables",
		"deferredRevenue", "otherCurrentLiabilities", "totalCurrentLiabilities",
		"longTermDebt", "deferredRevenueNonCurrent", "deferredTaxLiabilitiesNonCurrent",
		"otherNonCurrentLiabilities", "totalNonCurrentLiabilities", "otherLiabilities",
		"capitalLeaseObligations", "totalLiabilities",
		"treasuryStock", "preferredStock", "commonStock", "retainedEarnings",
		"additionalPaidInCapital", "accumulatedOtherComprehensiveIncomeLoss",
		"otherTotalStockholdersEquity", "totalStockholdersEquity", "totalEquity",
		"minorityInterest", "totalLiabilitiesAndTotalEquity", "totalInvestments",
		"totalDebt", "netDebt",
	}
	cashFlowKeys = []string{
		"depreciationAndAmortization", "deferredIncomeTax", "stockBasedCompensation",
		"changeInWorkingCapital", "changeInAccountsReceivables", "changeInInventory",
		"changeInAccountsPayables", "otherWorkingCapital", "otherNonCashItems",
		"netCashProvidedByOperatingActivities",
		"investmentsInPropertyPlantAndEquipment", "acquisitionsNet",
		"purchasesOfInvestments", "salesMaturitiesOfInvestments",
		"otherInvestingActivities", "netCashProvidedByInvestingActivities",
		"netDebtIssuance", "longTermNetDebtIssuance", "shortTermNetDebtIssuance",
		"netStockIssuance", "netCommonStockIssuance", "commonStockIssuance",
		"commonStockRepurchased", "netPreferredStockIssuance",
		"netDividendsPaid", "commonDividendsPaid", "preferredDividendsPaid",
		"otherFinancingActivities", "netCashProvidedByFinancingActivities",
		"effectOfForexChangesOnCash", "netChangeInCash",
		"operatingCashFlow", "capitalExpenditure", "freeCashFlow",
		"incomeTaxesPaid", "interestPaid",
	}
	// cashFlowFields maps the catalog's `changeIn*` keys to the cash-flow
	// statement property they read.
	cashFlowFields = map[string]string{
		"changeInAccountsReceivables": "accountsReceivables",
		"changeInInventory":           "inventory",
		"changeInAccountsPayables":    "accountsPayables",
	}
	ratioPercentKeys = []string{
		"grossProfitMargin", "ebitMargin", "ebitdaMargin", "operatingProfitMargin",
		"pretaxProfitMargin", "continuousOperationsProfitMargin", "netProfitMargin",
		"bottomLineProfitMargin", "dividendPayoutRatio", "dividendYield",
		"effectiveTaxRate",
	}
	// ttmMargins are the margins recomputable from TTM components.
	ttmMargins = map[string][2]string{
		"grossProfitMargin":     {"grossProfit", "revenue"},
		"ebitMargin":            {"ebit", "revenue"},
		"ebitdaMargin":          {"ebitda", "revenue"},
		"operatingProfitMargin": {"operatingIncome", "revenue"},
		"pretaxProfitMargin":    {"incomeBeforeTax", "revenue"},
		"netProfitMargin":       {"netIncome", "revenue"},
	}
	ratioRatioKeys = []string{
		"receivablesTurnover", "payablesTurnover", "inventoryTurnover",
		"fixedAssetTurnover", "assetTurnover", "currentRatio", "quickRatio",
		"solvencyRatio", "cashRatio", "priceToEarningsRatio",
		"priceToEarningsGrowthRatio", "forwardPriceToEarningsGrowthRatio",
		"priceToBookRatio", "priceToSalesRatio", "priceToFreeCashFlowRatio",
		"priceToOperatingCashFlowRatio", "debtToAssetsRatio", "debtToEquityRatio",
		"debtToCapitalRatio", "longTermDebtToCapitalRatio", "financialLeverageRatio",
		"workingCapitalTurnoverRatio", "operatingCashFlowRatio",
		"operatingCashFlowSalesRatio", "freeCashFlowOperatingCashFlowRatio",
		"debtServiceCoverageRatio", "interestCoverageRatio",
		"shortTermOperatingCashFlowCoverageRatio", "operatingCashFlowCoverageRatio",
		"capitalExpenditureCoverageRatio", "dividendPaidAndCapexCoverageRatio",
		"netIncomePerEBT", "ebtPerEbit", "priceToFairValue", "debtToMarketCap",
		"enterpriseValueMultiple",
	}
	ratioPerShareKeys = []string{
		"revenuePerShare", "netIncomePerShare", "interestDebtPerShare", "cashPerShare",
		"bookValuePerShare", "tangibleBookValuePerShare", "shareholdersEquityPerShare",
		"operatingCashFlowPerShare", "capexPerShare", "freeCashFlowPerShare",
	}
	growthKeys = []string{
		"revenueGrowth", "grossProfitGrowth", "ebitgrowth", "operatingIncomeGrowth",
		"netIncomeGrowth", "epsgrowth", "epsdilutedGrowth",
		"weightedAverageSharesGrowth", "weightedAverageSharesDilutedGrowth",
		"dividendsPerShareGrowth", "operatingCashFlowGrowth", "receivablesGrowth",
		"inventoryGrowth", "assetGrowth", "bookValueperShareGrowth", "debtGrowth",
		"rdexpenseGrowth", "sgaexpensesGrowth", "freeCashFlowGrowth", "ebitdaGrowth",
		"growthCapitalExpenditure",
		"tenYRevenueGrowthPerShare", "fiveYRevenueGrowthPerShare",
		"threeYRevenueGrowthPerShare",
		"tenYOperatingCFGrowthPerShare", "fiveYOperatingCFGrowthPerShare",
		"threeYOperatingCFGrowthPerShare",
		"tenYNetIncomeGrowthPerShare", "fiveYNetIncomeGrowthPerShare",
		"threeYNetIncomeGrowthPerShare",
		"tenYShareholdersEquityGrowthPerShare", "fiveYShareholdersEquityGrowthPerShare",
		"threeYShareholdersEquityGrowthPerShare",
		"tenYDividendperShareGrowthPerShare", "fiveYDividendperShareGrowthPerShare",
		"threeYDividendperShareGrowthPerShare",
		"tenYBottomLineNetIncomeGrowthPerShare", "fiveYBottomLineNetIncomeGrowthPerShare",
		"threeYBottomLineNetIncomeGrowthPerShare",
	}
)

// buildChartCatalog is ChartBuilderMetricCatalog.buildCatalog().
func buildChartCatalog() []ChartMetricDescriptor {
	out := []ChartMetricDescriptor{}
	add := func(key, group, format, aggregation string, ttm *bool, label string) {
		supports := aggregation != aggRatio
		if ttm != nil {
			supports = *ttm
		}
		if label == "" {
			label = chartMetricLabel(key)
		}
		out = append(out, ChartMetricDescriptor{
			Key: key, Label: label, Group: group, Format: format, Aggregation: aggregation, SupportsTTM: supports,
		})
	}
	yes, no := true, false
	for _, k := range incomeCurrencyKeys {
		add(k, groupIncomeStatement, fmtCurrency, aggFlow, nil, "")
	}
	add("eps", groupIncomeStatement, fmtPerShare, aggFlow, nil, "")
	add("epsDiluted", groupIncomeStatement, fmtPerShare, aggFlow, nil, "")
	add("weightedAverageShsOut", groupIncomeStatement, fmtShares, aggPointInTime, nil, "")
	add("weightedAverageShsOutDil", groupIncomeStatement, fmtShares, aggPointInTime, nil, "")
	for _, k := range balanceKeys {
		add(k, groupBalanceSheet, fmtCurrency, aggPointInTime, nil, "")
	}
	for _, k := range cashFlowKeys {
		add(k, groupCashFlow, fmtCurrency, aggFlow, nil, "")
	}
	for _, k := range ratioPercentKeys {
		_, ok := ttmMargins[k]
		add(k, groupRatios, fmtPercent, aggRatio, &[]bool{ok}[0], "")
	}
	for _, k := range ratioRatioKeys {
		add(k, groupRatios, fmtRatio, aggRatio, &no, "")
	}
	for _, k := range ratioPerShareKeys {
		add(k, groupRatios, fmtPerShare, aggRatio, &no, "")
	}
	for _, k := range growthKeys {
		add(k, groupGrowth, fmtPercent, aggRatio, &no, "")
	}
	add("fcfMargin", groupDerived, fmtPercent, aggRatio, &yes, "FCF Margin")
	add("fcfPerShare", groupDerived, fmtPerShare, aggRatio, &no, "FCF Per Share")
	return out
}

var chartLabelOverrides = map[string]string{
	"eps":                              "EPS",
	"epsDiluted":                       "EPS (Diluted)",
	"ebitda":                           "EBITDA",
	"ebit":                             "EBIT",
	"weightedAverageShsOut":            "Weighted Average Shares Outstanding",
	"weightedAverageShsOutDil":         "Weighted Average Shares Outstanding (Diluted)",
	"researchAndDevelopmentExpenses":   "R&D Expenses",
	"generalAndAdministrativeExpenses": "G&A Expenses",
	"sellingGeneralAndAdministrativeExpenses": "SG&A Expenses",
	"capitalLeaseOblationsCurrent":            "Capital Lease Obligations (Current)",
	"propertyPlantEquipmentNet":               "Property, Plant & Equipment (Net)",
	"accumulatedOtherComprehensiveIncomeLoss": "Accumulated Other Comprehensive Income/Loss",
	"totalLiabilitiesAndTotalEquity":          "Total Liabilities & Total Equity",
	"netCashProvidedByOperatingActivities":    "Net Cash from Operating Activities",
	"netCashProvidedByInvestingActivities":    "Net Cash from Investing Activities",
	"netCashProvidedByFinancingActivities":    "Net Cash from Financing Activities",
	"investmentsInPropertyPlantAndEquipment":  "Investments in Property, Plant & Equipment",
	"effectOfForexChangesOnCash":              "Effect of Forex Changes on Cash",
	"ebitMargin":                              "EBIT Margin",
	"ebitdaMargin":                            "EBITDA Margin",
	"priceToEarningsRatio":                    "P/E Ratio",
	"priceToEarningsGrowthRatio":              "PEG Ratio",
	"forwardPriceToEarningsGrowthRatio":       "Forward PEG Ratio",
	"priceToBookRatio":                        "P/B Ratio",
	"priceToSalesRatio":                       "P/S Ratio",
	"priceToFreeCashFlowRatio":                "Price to FCF Ratio",
	"priceToOperatingCashFlowRatio":           "Price to OCF Ratio",
	"netIncomePerEBT":                         "Net Income per EBT",
	"ebtPerEbit":                              "EBT per EBIT",
	"ebitgrowth":                              "EBIT Growth",
	"ebitdaGrowth":                            "EBITDA Growth",
	"epsgrowth":                               "EPS Growth",
	"epsdilutedGrowth":                        "EPS Diluted Growth",
	"rdexpenseGrowth":                         "R&D Expense Growth",
	"sgaexpensesGrowth":                       "SG&A Expenses Growth",
	"bookValueperShareGrowth":                 "Book Value per Share Growth",
	"growthCapitalExpenditure":                "Capital Expenditure Growth",
	"freeCashFlowOperatingCashFlowRatio":      "FCF to OCF Ratio",
}

// chartMetricLabel is ChartBuilderMetricCatalog.label(for:): camelCase split
// into title-cased words, with year prefixes, small words and acronyms.
func chartMetricLabel(key string) string {
	if o, ok := chartLabelOverrides[key]; ok {
		return o
	}
	working, prefix := key, ""
	for _, p := range [][2]string{{"tenY", "10Y "}, {"fiveY", "5Y "}, {"threeY", "3Y "}} {
		if strings.HasPrefix(working, p[0]) {
			prefix = p[1]
			working = working[len(p[0]):]
			break
		}
	}
	words := []string{}
	current := ""
	for _, r := range working {
		if unicode.IsUpper(r) && current != "" {
			words = append(words, current)
			current = string(r)
		} else {
			current += string(r)
		}
	}
	if current != "" {
		words = append(words, current)
	}
	lowercase := map[string]bool{"and": true, "of": true, "to": true, "per": true, "on": true, "from": true, "in": true}
	acronyms := map[string]bool{"cf": true, "fcf": true, "ocf": true, "ebt": true}
	for i, w := range words {
		lowered := strings.ToLower(w)
		switch {
		case acronyms[lowered]:
			words[i] = strings.ToUpper(lowered)
		case i > 0 && lowercase[lowered]:
			words[i] = lowered
		default:
			r := []rune(w)
			words[i] = strings.ToUpper(string(r[:1])) + string(r[1:])
		}
	}
	return prefix + strings.Join(words, " ")
}

// Chart builder statement sources (ChartBuilderSource), in fetch order.
type chartSource int

const (
	srcIncome chartSource = 1 << iota
	srcBalance
	srcCashFlow
	srcRatios
	srcGrowth
)

// chartPeriodRow is ChartBuilderPeriodRow.
type chartPeriodRow struct {
	income   *marketdata.IncomeStatementResponse
	balance  *marketdata.BalanceSheetStatementResponse
	cashFlow *marketdata.CashFlowStatementResponse
	ratios   *marketdata.RatiosResponse
	growth   *marketdata.FinancialGrowthResponse
}

// chartBinding is ChartBuilderMetricBinding.
type chartBinding struct {
	sources chartSource
	// ttm holds the numerator/denominator keys for a TTM ratio recompute.
	ttm     *[2]string
	extract func(*chartPeriodRow) *float64
}

// fieldReader returns a reader for T's *float64 field whose JSON name is
// name. It panics at init when the field is missing, so a catalog key can
// never silently read nothing.
func fieldReader[T any](name string) func(*T) *float64 {
	t := reflect.TypeFor[T]()
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag != name {
			continue
		}
		if f.Type != reflect.TypeFor[*float64]() {
			panic("chart builder: field " + name + " of " + t.Name() + " is not *float64")
		}
		idx := i
		return func(v *T) *float64 {
			return reflect.ValueOf(v).Elem().Field(idx).Interface().(*float64)
		}
	}
	panic("chart builder: no field " + name + " on " + t.Name())
}

func incomeBinding(field string) chartBinding {
	read := fieldReader[marketdata.IncomeStatementResponse](field)
	return chartBinding{sources: srcIncome, extract: func(r *chartPeriodRow) *float64 {
		if r.income == nil {
			return nil
		}
		return read(r.income)
	}}
}

func balanceBinding(field string) chartBinding {
	read := fieldReader[marketdata.BalanceSheetStatementResponse](field)
	return chartBinding{sources: srcBalance, extract: func(r *chartPeriodRow) *float64 {
		if r.balance == nil {
			return nil
		}
		return read(r.balance)
	}}
}

func cashFlowBinding(field string) chartBinding {
	read := fieldReader[marketdata.CashFlowStatementResponse](field)
	return chartBinding{sources: srcCashFlow, extract: func(r *chartPeriodRow) *float64 {
		if r.cashFlow == nil {
			return nil
		}
		return read(r.cashFlow)
	}}
}

func ratiosBinding(field string) chartBinding {
	read := fieldReader[marketdata.RatiosResponse](field)
	b := chartBinding{sources: srcRatios, extract: func(r *chartPeriodRow) *float64 {
		if r.ratios == nil {
			return nil
		}
		return read(r.ratios)
	}}
	if c, ok := ttmMargins[field]; ok {
		b.ttm = &c
	}
	return b
}

func growthBinding(field string) chartBinding {
	read := fieldReader[marketdata.FinancialGrowthResponse](field)
	return chartBinding{sources: srcGrowth, extract: func(r *chartPeriodRow) *float64 {
		if r.growth == nil {
			return nil
		}
		return read(r.growth)
	}}
}

// chartBindings is ChartBuilderMetricResolver.bindings.
var chartBindings = buildChartBindings()

func buildChartBindings() map[string]chartBinding {
	m := map[string]chartBinding{}
	for _, k := range incomeCurrencyKeys {
		m[k] = incomeBinding(k)
	}
	for _, k := range []string{"eps", "epsDiluted", "weightedAverageShsOut", "weightedAverageShsOutDil"} {
		m[k] = incomeBinding(k)
	}
	for _, k := range balanceKeys {
		m[k] = balanceBinding(k)
	}
	for _, k := range cashFlowKeys {
		field := k
		if f, ok := cashFlowFields[k]; ok {
			field = f
		}
		m[k] = cashFlowBinding(field)
	}
	for _, list := range [][]string{ratioPercentKeys, ratioRatioKeys, ratioPerShareKeys} {
		for _, k := range list {
			m[k] = ratiosBinding(k)
		}
	}
	for _, k := range growthKeys {
		m[k] = growthBinding(k)
	}
	m["fcfMargin"] = chartBinding{
		sources: srcIncome | srcCashFlow,
		ttm:     &[2]string{"freeCashFlow", "revenue"},
		extract: func(r *chartPeriodRow) *float64 {
			if r.cashFlow == nil || r.cashFlow.FreeCashFlow == nil || r.income == nil || r.income.Revenue == nil || *r.income.Revenue == 0 {
				return nil
			}
			v := *r.cashFlow.FreeCashFlow / *r.income.Revenue
			return &v
		},
	}
	m["fcfPerShare"] = chartBinding{
		sources: srcIncome | srcCashFlow,
		extract: func(r *chartPeriodRow) *float64 {
			if r.cashFlow == nil || r.cashFlow.FreeCashFlow == nil || r.income == nil ||
				r.income.WeightedAverageShsOutDil == nil || *r.income.WeightedAverageShsOutDil == 0 {
				return nil
			}
			v := *r.cashFlow.FreeCashFlow / *r.income.WeightedAverageShsOutDil
			return &v
		},
	}
	return m
}

// requiredChartSources is ChartBuilderMetricResolver.requiredSources.
func requiredChartSources(keys []string, period string) chartSource {
	var out chartSource
	for _, k := range keys {
		b, ok := chartBindings[k]
		if !ok {
			continue
		}
		if period == chartPeriodTTM && b.ttm != nil {
			for _, ck := range b.ttm {
				if cb, ok := chartBindings[ck]; ok {
					out |= cb.sources
				}
			}
			continue
		}
		out |= b.sources
	}
	return out
}
