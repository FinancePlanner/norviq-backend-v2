// Code generated from StockPlanShared v5.25.0 (Market/MarketDTOs.swift,
// Market/CryptoDTOs.swift). Field names and JSON keys are the Swift ones.

package marketdata

// RatiosTTMResponse mirrors the Swift DTO of the same name.
type RatiosTTMResponse struct {
	Symbol                                     string   `json:"symbol"`
	GrossProfitMarginTTM                       *float64 `json:"grossProfitMarginTTM,omitempty"`
	EbitMarginTTM                              *float64 `json:"ebitMarginTTM,omitempty"`
	EbitdaMarginTTM                            *float64 `json:"ebitdaMarginTTM,omitempty"`
	OperatingProfitMarginTTM                   *float64 `json:"operatingProfitMarginTTM,omitempty"`
	PretaxProfitMarginTTM                      *float64 `json:"pretaxProfitMarginTTM,omitempty"`
	ContinuousOperationsProfitMarginTTM        *float64 `json:"continuousOperationsProfitMarginTTM,omitempty"`
	NetProfitMarginTTM                         *float64 `json:"netProfitMarginTTM,omitempty"`
	BottomLineProfitMarginTTM                  *float64 `json:"bottomLineProfitMarginTTM,omitempty"`
	ReceivablesTurnoverTTM                     *float64 `json:"receivablesTurnoverTTM,omitempty"`
	PayablesTurnoverTTM                        *float64 `json:"payablesTurnoverTTM,omitempty"`
	InventoryTurnoverTTM                       *float64 `json:"inventoryTurnoverTTM,omitempty"`
	FixedAssetTurnoverTTM                      *float64 `json:"fixedAssetTurnoverTTM,omitempty"`
	AssetTurnoverTTM                           *float64 `json:"assetTurnoverTTM,omitempty"`
	CurrentRatioTTM                            *float64 `json:"currentRatioTTM,omitempty"`
	QuickRatioTTM                              *float64 `json:"quickRatioTTM,omitempty"`
	SolvencyRatioTTM                           *float64 `json:"solvencyRatioTTM,omitempty"`
	CashRatioTTM                               *float64 `json:"cashRatioTTM,omitempty"`
	PriceToEarningsRatioTTM                    *float64 `json:"priceToEarningsRatioTTM,omitempty"`
	PriceToEarningsGrowthRatioTTM              *float64 `json:"priceToEarningsGrowthRatioTTM,omitempty"`
	ForwardPriceToEarningsGrowthRatioTTM       *float64 `json:"forwardPriceToEarningsGrowthRatioTTM,omitempty"`
	PriceToBookRatioTTM                        *float64 `json:"priceToBookRatioTTM,omitempty"`
	PriceToSalesRatioTTM                       *float64 `json:"priceToSalesRatioTTM,omitempty"`
	PriceToFreeCashFlowRatioTTM                *float64 `json:"priceToFreeCashFlowRatioTTM,omitempty"`
	PriceToOperatingCashFlowRatioTTM           *float64 `json:"priceToOperatingCashFlowRatioTTM,omitempty"`
	DebtToAssetsRatioTTM                       *float64 `json:"debtToAssetsRatioTTM,omitempty"`
	DebtToEquityRatioTTM                       *float64 `json:"debtToEquityRatioTTM,omitempty"`
	DebtToCapitalRatioTTM                      *float64 `json:"debtToCapitalRatioTTM,omitempty"`
	LongTermDebtToCapitalRatioTTM              *float64 `json:"longTermDebtToCapitalRatioTTM,omitempty"`
	FinancialLeverageRatioTTM                  *float64 `json:"financialLeverageRatioTTM,omitempty"`
	WorkingCapitalTurnoverRatioTTM             *float64 `json:"workingCapitalTurnoverRatioTTM,omitempty"`
	OperatingCashFlowRatioTTM                  *float64 `json:"operatingCashFlowRatioTTM,omitempty"`
	OperatingCashFlowSalesRatioTTM             *float64 `json:"operatingCashFlowSalesRatioTTM,omitempty"`
	FreeCashFlowOperatingCashFlowRatioTTM      *float64 `json:"freeCashFlowOperatingCashFlowRatioTTM,omitempty"`
	DebtServiceCoverageRatioTTM                *float64 `json:"debtServiceCoverageRatioTTM,omitempty"`
	InterestCoverageRatioTTM                   *float64 `json:"interestCoverageRatioTTM,omitempty"`
	ShortTermOperatingCashFlowCoverageRatioTTM *float64 `json:"shortTermOperatingCashFlowCoverageRatioTTM,omitempty"`
	OperatingCashFlowCoverageRatioTTM          *float64 `json:"operatingCashFlowCoverageRatioTTM,omitempty"`
	CapitalExpenditureCoverageRatioTTM         *float64 `json:"capitalExpenditureCoverageRatioTTM,omitempty"`
	DividendPaidAndCapexCoverageRatioTTM       *float64 `json:"dividendPaidAndCapexCoverageRatioTTM,omitempty"`
	DividendPayoutRatioTTM                     *float64 `json:"dividendPayoutRatioTTM,omitempty"`
	DividendYieldTTM                           *float64 `json:"dividendYieldTTM,omitempty"`
	EnterpriseValueTTM                         *float64 `json:"enterpriseValueTTM,omitempty"`
	RevenuePerShareTTM                         *float64 `json:"revenuePerShareTTM,omitempty"`
	NetIncomePerShareTTM                       *float64 `json:"netIncomePerShareTTM,omitempty"`
	InterestDebtPerShareTTM                    *float64 `json:"interestDebtPerShareTTM,omitempty"`
	CashPerShareTTM                            *float64 `json:"cashPerShareTTM,omitempty"`
	BookValuePerShareTTM                       *float64 `json:"bookValuePerShareTTM,omitempty"`
	TangibleBookValuePerShareTTM               *float64 `json:"tangibleBookValuePerShareTTM,omitempty"`
	ShareholdersEquityPerShareTTM              *float64 `json:"shareholdersEquityPerShareTTM,omitempty"`
	OperatingCashFlowPerShareTTM               *float64 `json:"operatingCashFlowPerShareTTM,omitempty"`
	CapexPerShareTTM                           *float64 `json:"capexPerShareTTM,omitempty"`
	FreeCashFlowPerShareTTM                    *float64 `json:"freeCashFlowPerShareTTM,omitempty"`
	NetIncomePerEBTTTM                         *float64 `json:"netIncomePerEBTTTM,omitempty"`
	EbtPerEbitTTM                              *float64 `json:"ebtPerEbitTTM,omitempty"`
	PriceToFairValueTTM                        *float64 `json:"priceToFairValueTTM,omitempty"`
	DebtToMarketCapTTM                         *float64 `json:"debtToMarketCapTTM,omitempty"`
	EffectiveTaxRateTTM                        *float64 `json:"effectiveTaxRateTTM,omitempty"`
	EnterpriseValueMultipleTTM                 *float64 `json:"enterpriseValueMultipleTTM,omitempty"`
}

// BalanceSheetStatementResponse mirrors the Swift DTO of the same name.
type BalanceSheetStatementResponse struct {
	Date                                    string   `json:"date"`
	Symbol                                  string   `json:"symbol"`
	ReportedCurrency                        *string  `json:"reportedCurrency,omitempty"`
	Cik                                     *string  `json:"cik,omitempty"`
	FilingDate                              *string  `json:"filingDate,omitempty"`
	AcceptedDate                            *string  `json:"acceptedDate,omitempty"`
	FiscalYear                              *string  `json:"fiscalYear,omitempty"`
	Period                                  *string  `json:"period,omitempty"`
	CashAndCashEquivalents                  *float64 `json:"cashAndCashEquivalents,omitempty"`
	ShortTermInvestments                    *float64 `json:"shortTermInvestments,omitempty"`
	CashAndShortTermInvestments             *float64 `json:"cashAndShortTermInvestments,omitempty"`
	NetReceivables                          *float64 `json:"netReceivables,omitempty"`
	AccountsReceivables                     *float64 `json:"accountsReceivables,omitempty"`
	OtherReceivables                        *float64 `json:"otherReceivables,omitempty"`
	Inventory                               *float64 `json:"inventory,omitempty"`
	Prepaids                                *float64 `json:"prepaids,omitempty"`
	OtherCurrentAssets                      *float64 `json:"otherCurrentAssets,omitempty"`
	TotalCurrentAssets                      *float64 `json:"totalCurrentAssets,omitempty"`
	PropertyPlantEquipmentNet               *float64 `json:"propertyPlantEquipmentNet,omitempty"`
	Goodwill                                *float64 `json:"goodwill,omitempty"`
	IntangibleAssets                        *float64 `json:"intangibleAssets,omitempty"`
	GoodwillAndIntangibleAssets             *float64 `json:"goodwillAndIntangibleAssets,omitempty"`
	LongTermInvestments                     *float64 `json:"longTermInvestments,omitempty"`
	TaxAssets                               *float64 `json:"taxAssets,omitempty"`
	OtherNonCurrentAssets                   *float64 `json:"otherNonCurrentAssets,omitempty"`
	TotalNonCurrentAssets                   *float64 `json:"totalNonCurrentAssets,omitempty"`
	OtherAssets                             *float64 `json:"otherAssets,omitempty"`
	TotalAssets                             *float64 `json:"totalAssets,omitempty"`
	TotalPayables                           *float64 `json:"totalPayables,omitempty"`
	AccountPayables                         *float64 `json:"accountPayables,omitempty"`
	OtherPayables                           *float64 `json:"otherPayables,omitempty"`
	AccruedExpenses                         *float64 `json:"accruedExpenses,omitempty"`
	ShortTermDebt                           *float64 `json:"shortTermDebt,omitempty"`
	CapitalLeaseOblationsCurrent            *float64 `json:"capitalLeaseOblationsCurrent,omitempty"`
	TaxPayables                             *float64 `json:"taxPayables,omitempty"`
	DeferredRevenue                         *float64 `json:"deferredRevenue,omitempty"`
	OtherCurrentLiabilities                 *float64 `json:"otherCurrentLiabilities,omitempty"`
	TotalCurrentLiabilities                 *float64 `json:"totalCurrentLiabilities,omitempty"`
	LongTermDebt                            *float64 `json:"longTermDebt,omitempty"`
	DeferredRevenueNonCurrent               *float64 `json:"deferredRevenueNonCurrent,omitempty"`
	DeferredTaxLiabilitiesNonCurrent        *float64 `json:"deferredTaxLiabilitiesNonCurrent,omitempty"`
	OtherNonCurrentLiabilities              *float64 `json:"otherNonCurrentLiabilities,omitempty"`
	TotalNonCurrentLiabilities              *float64 `json:"totalNonCurrentLiabilities,omitempty"`
	OtherLiabilities                        *float64 `json:"otherLiabilities,omitempty"`
	CapitalLeaseObligations                 *float64 `json:"capitalLeaseObligations,omitempty"`
	TotalLiabilities                        *float64 `json:"totalLiabilities,omitempty"`
	TreasuryStock                           *float64 `json:"treasuryStock,omitempty"`
	PreferredStock                          *float64 `json:"preferredStock,omitempty"`
	CommonStock                             *float64 `json:"commonStock,omitempty"`
	RetainedEarnings                        *float64 `json:"retainedEarnings,omitempty"`
	AdditionalPaidInCapital                 *float64 `json:"additionalPaidInCapital,omitempty"`
	AccumulatedOtherComprehensiveIncomeLoss *float64 `json:"accumulatedOtherComprehensiveIncomeLoss,omitempty"`
	OtherTotalStockholdersEquity            *float64 `json:"otherTotalStockholdersEquity,omitempty"`
	TotalStockholdersEquity                 *float64 `json:"totalStockholdersEquity,omitempty"`
	TotalEquity                             *float64 `json:"totalEquity,omitempty"`
	MinorityInterest                        *float64 `json:"minorityInterest,omitempty"`
	TotalLiabilitiesAndTotalEquity          *float64 `json:"totalLiabilitiesAndTotalEquity,omitempty"`
	TotalInvestments                        *float64 `json:"totalInvestments,omitempty"`
	TotalDebt                               *float64 `json:"totalDebt,omitempty"`
	NetDebt                                 *float64 `json:"netDebt,omitempty"`
}

// IncomeStatementResponse mirrors the Swift DTO of the same name.
type IncomeStatementResponse struct {
	Date                                    string   `json:"date"`
	Symbol                                  string   `json:"symbol"`
	ReportedCurrency                        *string  `json:"reportedCurrency,omitempty"`
	Cik                                     *string  `json:"cik,omitempty"`
	FilingDate                              *string  `json:"filingDate,omitempty"`
	AcceptedDate                            *string  `json:"acceptedDate,omitempty"`
	FiscalYear                              *string  `json:"fiscalYear,omitempty"`
	Period                                  *string  `json:"period,omitempty"`
	Revenue                                 *float64 `json:"revenue,omitempty"`
	CostOfRevenue                           *float64 `json:"costOfRevenue,omitempty"`
	GrossProfit                             *float64 `json:"grossProfit,omitempty"`
	ResearchAndDevelopmentExpenses          *float64 `json:"researchAndDevelopmentExpenses,omitempty"`
	GeneralAndAdministrativeExpenses        *float64 `json:"generalAndAdministrativeExpenses,omitempty"`
	SellingAndMarketingExpenses             *float64 `json:"sellingAndMarketingExpenses,omitempty"`
	SellingGeneralAndAdministrativeExpenses *float64 `json:"sellingGeneralAndAdministrativeExpenses,omitempty"`
	OtherExpenses                           *float64 `json:"otherExpenses,omitempty"`
	OperatingExpenses                       *float64 `json:"operatingExpenses,omitempty"`
	CostAndExpenses                         *float64 `json:"costAndExpenses,omitempty"`
	NetInterestIncome                       *float64 `json:"netInterestIncome,omitempty"`
	InterestIncome                          *float64 `json:"interestIncome,omitempty"`
	InterestExpense                         *float64 `json:"interestExpense,omitempty"`
	DepreciationAndAmortization             *float64 `json:"depreciationAndAmortization,omitempty"`
	Ebitda                                  *float64 `json:"ebitda,omitempty"`
	Ebit                                    *float64 `json:"ebit,omitempty"`
	NonOperatingIncomeExcludingInterest     *float64 `json:"nonOperatingIncomeExcludingInterest,omitempty"`
	OperatingIncome                         *float64 `json:"operatingIncome,omitempty"`
	TotalOtherIncomeExpensesNet             *float64 `json:"totalOtherIncomeExpensesNet,omitempty"`
	IncomeBeforeTax                         *float64 `json:"incomeBeforeTax,omitempty"`
	IncomeTaxExpense                        *float64 `json:"incomeTaxExpense,omitempty"`
	NetIncomeFromContinuingOperations       *float64 `json:"netIncomeFromContinuingOperations,omitempty"`
	NetIncomeFromDiscontinuedOperations     *float64 `json:"netIncomeFromDiscontinuedOperations,omitempty"`
	OtherAdjustmentsToNetIncome             *float64 `json:"otherAdjustmentsToNetIncome,omitempty"`
	NetIncome                               *float64 `json:"netIncome,omitempty"`
	NetIncomeDeductions                     *float64 `json:"netIncomeDeductions,omitempty"`
	BottomLineNetIncome                     *float64 `json:"bottomLineNetIncome,omitempty"`
	Eps                                     *float64 `json:"eps,omitempty"`
	EpsDiluted                              *float64 `json:"epsDiluted,omitempty"`
	WeightedAverageShsOut                   *float64 `json:"weightedAverageShsOut,omitempty"`
	WeightedAverageShsOutDil                *float64 `json:"weightedAverageShsOutDil,omitempty"`
}

// CashFlowStatementResponse mirrors the Swift DTO of the same name.
type CashFlowStatementResponse struct {
	Date                                   string   `json:"date"`
	Symbol                                 string   `json:"symbol"`
	ReportedCurrency                       *string  `json:"reportedCurrency,omitempty"`
	Cik                                    *string  `json:"cik,omitempty"`
	FilingDate                             *string  `json:"filingDate,omitempty"`
	AcceptedDate                           *string  `json:"acceptedDate,omitempty"`
	FiscalYear                             *string  `json:"fiscalYear,omitempty"`
	Period                                 *string  `json:"period,omitempty"`
	NetIncome                              *float64 `json:"netIncome,omitempty"`
	DepreciationAndAmortization            *float64 `json:"depreciationAndAmortization,omitempty"`
	DeferredIncomeTax                      *float64 `json:"deferredIncomeTax,omitempty"`
	StockBasedCompensation                 *float64 `json:"stockBasedCompensation,omitempty"`
	ChangeInWorkingCapital                 *float64 `json:"changeInWorkingCapital,omitempty"`
	AccountsReceivables                    *float64 `json:"accountsReceivables,omitempty"`
	Inventory                              *float64 `json:"inventory,omitempty"`
	AccountsPayables                       *float64 `json:"accountsPayables,omitempty"`
	OtherWorkingCapital                    *float64 `json:"otherWorkingCapital,omitempty"`
	OtherNonCashItems                      *float64 `json:"otherNonCashItems,omitempty"`
	NetCashProvidedByOperatingActivities   *float64 `json:"netCashProvidedByOperatingActivities,omitempty"`
	InvestmentsInPropertyPlantAndEquipment *float64 `json:"investmentsInPropertyPlantAndEquipment,omitempty"`
	AcquisitionsNet                        *float64 `json:"acquisitionsNet,omitempty"`
	PurchasesOfInvestments                 *float64 `json:"purchasesOfInvestments,omitempty"`
	SalesMaturitiesOfInvestments           *float64 `json:"salesMaturitiesOfInvestments,omitempty"`
	OtherInvestingActivities               *float64 `json:"otherInvestingActivities,omitempty"`
	NetCashProvidedByInvestingActivities   *float64 `json:"netCashProvidedByInvestingActivities,omitempty"`
	NetDebtIssuance                        *float64 `json:"netDebtIssuance,omitempty"`
	LongTermNetDebtIssuance                *float64 `json:"longTermNetDebtIssuance,omitempty"`
	ShortTermNetDebtIssuance               *float64 `json:"shortTermNetDebtIssuance,omitempty"`
	NetStockIssuance                       *float64 `json:"netStockIssuance,omitempty"`
	NetCommonStockIssuance                 *float64 `json:"netCommonStockIssuance,omitempty"`
	CommonStockIssuance                    *float64 `json:"commonStockIssuance,omitempty"`
	CommonStockRepurchased                 *float64 `json:"commonStockRepurchased,omitempty"`
	NetPreferredStockIssuance              *float64 `json:"netPreferredStockIssuance,omitempty"`
	NetDividendsPaid                       *float64 `json:"netDividendsPaid,omitempty"`
	CommonDividendsPaid                    *float64 `json:"commonDividendsPaid,omitempty"`
	PreferredDividendsPaid                 *float64 `json:"preferredDividendsPaid,omitempty"`
	OtherFinancingActivities               *float64 `json:"otherFinancingActivities,omitempty"`
	NetCashProvidedByFinancingActivities   *float64 `json:"netCashProvidedByFinancingActivities,omitempty"`
	EffectOfForexChangesOnCash             *float64 `json:"effectOfForexChangesOnCash,omitempty"`
	NetChangeInCash                        *float64 `json:"netChangeInCash,omitempty"`
	CashAtEndOfPeriod                      *float64 `json:"cashAtEndOfPeriod,omitempty"`
	CashAtBeginningOfPeriod                *float64 `json:"cashAtBeginningOfPeriod,omitempty"`
	OperatingCashFlow                      *float64 `json:"operatingCashFlow,omitempty"`
	CapitalExpenditure                     *float64 `json:"capitalExpenditure,omitempty"`
	FreeCashFlow                           *float64 `json:"freeCashFlow,omitempty"`
	IncomeTaxesPaid                        *float64 `json:"incomeTaxesPaid,omitempty"`
	InterestPaid                           *float64 `json:"interestPaid,omitempty"`
}

// FinancialGrowthResponse mirrors the Swift DTO of the same name.
type FinancialGrowthResponse struct {
	Symbol                                  string   `json:"symbol"`
	Date                                    string   `json:"date"`
	FiscalYear                              *string  `json:"fiscalYear,omitempty"`
	Period                                  *string  `json:"period,omitempty"`
	ReportedCurrency                        *string  `json:"reportedCurrency,omitempty"`
	RevenueGrowth                           *float64 `json:"revenueGrowth,omitempty"`
	GrossProfitGrowth                       *float64 `json:"grossProfitGrowth,omitempty"`
	Ebitgrowth                              *float64 `json:"ebitgrowth,omitempty"`
	OperatingIncomeGrowth                   *float64 `json:"operatingIncomeGrowth,omitempty"`
	NetIncomeGrowth                         *float64 `json:"netIncomeGrowth,omitempty"`
	Epsgrowth                               *float64 `json:"epsgrowth,omitempty"`
	EpsdilutedGrowth                        *float64 `json:"epsdilutedGrowth,omitempty"`
	WeightedAverageSharesGrowth             *float64 `json:"weightedAverageSharesGrowth,omitempty"`
	WeightedAverageSharesDilutedGrowth      *float64 `json:"weightedAverageSharesDilutedGrowth,omitempty"`
	DividendsPerShareGrowth                 *float64 `json:"dividendsPerShareGrowth,omitempty"`
	OperatingCashFlowGrowth                 *float64 `json:"operatingCashFlowGrowth,omitempty"`
	ReceivablesGrowth                       *float64 `json:"receivablesGrowth,omitempty"`
	InventoryGrowth                         *float64 `json:"inventoryGrowth,omitempty"`
	AssetGrowth                             *float64 `json:"assetGrowth,omitempty"`
	BookValueperShareGrowth                 *float64 `json:"bookValueperShareGrowth,omitempty"`
	DebtGrowth                              *float64 `json:"debtGrowth,omitempty"`
	RdexpenseGrowth                         *float64 `json:"rdexpenseGrowth,omitempty"`
	SgaexpensesGrowth                       *float64 `json:"sgaexpensesGrowth,omitempty"`
	FreeCashFlowGrowth                      *float64 `json:"freeCashFlowGrowth,omitempty"`
	TenYRevenueGrowthPerShare               *float64 `json:"tenYRevenueGrowthPerShare,omitempty"`
	FiveYRevenueGrowthPerShare              *float64 `json:"fiveYRevenueGrowthPerShare,omitempty"`
	ThreeYRevenueGrowthPerShare             *float64 `json:"threeYRevenueGrowthPerShare,omitempty"`
	TenYOperatingCFGrowthPerShare           *float64 `json:"tenYOperatingCFGrowthPerShare,omitempty"`
	FiveYOperatingCFGrowthPerShare          *float64 `json:"fiveYOperatingCFGrowthPerShare,omitempty"`
	ThreeYOperatingCFGrowthPerShare         *float64 `json:"threeYOperatingCFGrowthPerShare,omitempty"`
	TenYNetIncomeGrowthPerShare             *float64 `json:"tenYNetIncomeGrowthPerShare,omitempty"`
	FiveYNetIncomeGrowthPerShare            *float64 `json:"fiveYNetIncomeGrowthPerShare,omitempty"`
	ThreeYNetIncomeGrowthPerShare           *float64 `json:"threeYNetIncomeGrowthPerShare,omitempty"`
	TenYShareholdersEquityGrowthPerShare    *float64 `json:"tenYShareholdersEquityGrowthPerShare,omitempty"`
	FiveYShareholdersEquityGrowthPerShare   *float64 `json:"fiveYShareholdersEquityGrowthPerShare,omitempty"`
	ThreeYShareholdersEquityGrowthPerShare  *float64 `json:"threeYShareholdersEquityGrowthPerShare,omitempty"`
	TenYDividendperShareGrowthPerShare      *float64 `json:"tenYDividendperShareGrowthPerShare,omitempty"`
	FiveYDividendperShareGrowthPerShare     *float64 `json:"fiveYDividendperShareGrowthPerShare,omitempty"`
	ThreeYDividendperShareGrowthPerShare    *float64 `json:"threeYDividendperShareGrowthPerShare,omitempty"`
	EbitdaGrowth                            *float64 `json:"ebitdaGrowth,omitempty"`
	GrowthCapitalExpenditure                *float64 `json:"growthCapitalExpenditure,omitempty"`
	TenYBottomLineNetIncomeGrowthPerShare   *float64 `json:"tenYBottomLineNetIncomeGrowthPerShare,omitempty"`
	FiveYBottomLineNetIncomeGrowthPerShare  *float64 `json:"fiveYBottomLineNetIncomeGrowthPerShare,omitempty"`
	ThreeYBottomLineNetIncomeGrowthPerShare *float64 `json:"threeYBottomLineNetIncomeGrowthPerShare,omitempty"`
}

// AnalystEstimatesResponse mirrors the Swift DTO of the same name.
type AnalystEstimatesResponse struct {
	Symbol             string   `json:"symbol"`
	Date               string   `json:"date"`
	RevenueLow         *float64 `json:"revenueLow,omitempty"`
	RevenueHigh        *float64 `json:"revenueHigh,omitempty"`
	RevenueAvg         *float64 `json:"revenueAvg,omitempty"`
	EbitdaLow          *float64 `json:"ebitdaLow,omitempty"`
	EbitdaHigh         *float64 `json:"ebitdaHigh,omitempty"`
	EbitdaAvg          *float64 `json:"ebitdaAvg,omitempty"`
	EbitLow            *float64 `json:"ebitLow,omitempty"`
	EbitHigh           *float64 `json:"ebitHigh,omitempty"`
	EbitAvg            *float64 `json:"ebitAvg,omitempty"`
	NetIncomeLow       *float64 `json:"netIncomeLow,omitempty"`
	NetIncomeHigh      *float64 `json:"netIncomeHigh,omitempty"`
	NetIncomeAvg       *float64 `json:"netIncomeAvg,omitempty"`
	SgaExpenseLow      *float64 `json:"sgaExpenseLow,omitempty"`
	SgaExpenseHigh     *float64 `json:"sgaExpenseHigh,omitempty"`
	SgaExpenseAvg      *float64 `json:"sgaExpenseAvg,omitempty"`
	EpsAvg             *float64 `json:"epsAvg,omitempty"`
	EpsHigh            *float64 `json:"epsHigh,omitempty"`
	EpsLow             *float64 `json:"epsLow,omitempty"`
	NumAnalystsRevenue *int     `json:"numAnalystsRevenue,omitempty"`
	NumAnalystsEps     *int     `json:"numAnalystsEps,omitempty"`
}

// RatiosResponse mirrors the Swift DTO of the same name.
type RatiosResponse struct {
	Symbol                                  string   `json:"symbol"`
	Date                                    string   `json:"date"`
	FiscalYear                              *string  `json:"fiscalYear,omitempty"`
	Period                                  *string  `json:"period,omitempty"`
	ReportedCurrency                        *string  `json:"reportedCurrency,omitempty"`
	GrossProfitMargin                       *float64 `json:"grossProfitMargin,omitempty"`
	EbitMargin                              *float64 `json:"ebitMargin,omitempty"`
	EbitdaMargin                            *float64 `json:"ebitdaMargin,omitempty"`
	OperatingProfitMargin                   *float64 `json:"operatingProfitMargin,omitempty"`
	PretaxProfitMargin                      *float64 `json:"pretaxProfitMargin,omitempty"`
	ContinuousOperationsProfitMargin        *float64 `json:"continuousOperationsProfitMargin,omitempty"`
	NetProfitMargin                         *float64 `json:"netProfitMargin,omitempty"`
	BottomLineProfitMargin                  *float64 `json:"bottomLineProfitMargin,omitempty"`
	ReceivablesTurnover                     *float64 `json:"receivablesTurnover,omitempty"`
	PayablesTurnover                        *float64 `json:"payablesTurnover,omitempty"`
	InventoryTurnover                       *float64 `json:"inventoryTurnover,omitempty"`
	FixedAssetTurnover                      *float64 `json:"fixedAssetTurnover,omitempty"`
	AssetTurnover                           *float64 `json:"assetTurnover,omitempty"`
	CurrentRatio                            *float64 `json:"currentRatio,omitempty"`
	QuickRatio                              *float64 `json:"quickRatio,omitempty"`
	SolvencyRatio                           *float64 `json:"solvencyRatio,omitempty"`
	CashRatio                               *float64 `json:"cashRatio,omitempty"`
	PriceToEarningsRatio                    *float64 `json:"priceToEarningsRatio,omitempty"`
	PriceToEarningsGrowthRatio              *float64 `json:"priceToEarningsGrowthRatio,omitempty"`
	ForwardPriceToEarningsGrowthRatio       *float64 `json:"forwardPriceToEarningsGrowthRatio,omitempty"`
	PriceToBookRatio                        *float64 `json:"priceToBookRatio,omitempty"`
	PriceToSalesRatio                       *float64 `json:"priceToSalesRatio,omitempty"`
	PriceToFreeCashFlowRatio                *float64 `json:"priceToFreeCashFlowRatio,omitempty"`
	PriceToOperatingCashFlowRatio           *float64 `json:"priceToOperatingCashFlowRatio,omitempty"`
	DebtToAssetsRatio                       *float64 `json:"debtToAssetsRatio,omitempty"`
	DebtToEquityRatio                       *float64 `json:"debtToEquityRatio,omitempty"`
	DebtToCapitalRatio                      *float64 `json:"debtToCapitalRatio,omitempty"`
	LongTermDebtToCapitalRatio              *float64 `json:"longTermDebtToCapitalRatio,omitempty"`
	FinancialLeverageRatio                  *float64 `json:"financialLeverageRatio,omitempty"`
	WorkingCapitalTurnoverRatio             *float64 `json:"workingCapitalTurnoverRatio,omitempty"`
	OperatingCashFlowRatio                  *float64 `json:"operatingCashFlowRatio,omitempty"`
	OperatingCashFlowSalesRatio             *float64 `json:"operatingCashFlowSalesRatio,omitempty"`
	FreeCashFlowOperatingCashFlowRatio      *float64 `json:"freeCashFlowOperatingCashFlowRatio,omitempty"`
	DebtServiceCoverageRatio                *float64 `json:"debtServiceCoverageRatio,omitempty"`
	InterestCoverageRatio                   *float64 `json:"interestCoverageRatio,omitempty"`
	ShortTermOperatingCashFlowCoverageRatio *float64 `json:"shortTermOperatingCashFlowCoverageRatio,omitempty"`
	OperatingCashFlowCoverageRatio          *float64 `json:"operatingCashFlowCoverageRatio,omitempty"`
	CapitalExpenditureCoverageRatio         *float64 `json:"capitalExpenditureCoverageRatio,omitempty"`
	DividendPaidAndCapexCoverageRatio       *float64 `json:"dividendPaidAndCapexCoverageRatio,omitempty"`
	DividendPayoutRatio                     *float64 `json:"dividendPayoutRatio,omitempty"`
	DividendYield                           *float64 `json:"dividendYield,omitempty"`
	DividendYieldPercentage                 *float64 `json:"dividendYieldPercentage,omitempty"`
	RevenuePerShare                         *float64 `json:"revenuePerShare,omitempty"`
	NetIncomePerShare                       *float64 `json:"netIncomePerShare,omitempty"`
	InterestDebtPerShare                    *float64 `json:"interestDebtPerShare,omitempty"`
	CashPerShare                            *float64 `json:"cashPerShare,omitempty"`
	BookValuePerShare                       *float64 `json:"bookValuePerShare,omitempty"`
	TangibleBookValuePerShare               *float64 `json:"tangibleBookValuePerShare,omitempty"`
	ShareholdersEquityPerShare              *float64 `json:"shareholdersEquityPerShare,omitempty"`
	OperatingCashFlowPerShare               *float64 `json:"operatingCashFlowPerShare,omitempty"`
	CapexPerShare                           *float64 `json:"capexPerShare,omitempty"`
	FreeCashFlowPerShare                    *float64 `json:"freeCashFlowPerShare,omitempty"`
	NetIncomePerEBT                         *float64 `json:"netIncomePerEBT,omitempty"`
	EbtPerEbit                              *float64 `json:"ebtPerEbit,omitempty"`
	PriceToFairValue                        *float64 `json:"priceToFairValue,omitempty"`
	DebtToMarketCap                         *float64 `json:"debtToMarketCap,omitempty"`
	EffectiveTaxRate                        *float64 `json:"effectiveTaxRate,omitempty"`
	EnterpriseValueMultiple                 *float64 `json:"enterpriseValueMultiple,omitempty"`
}

// CryptoQuoteResponse mirrors the Swift DTO of the same name.
type CryptoQuoteResponse struct {
	Symbol           string   `json:"symbol"`
	Name             string   `json:"name"`
	Price            float64  `json:"price"`
	ChangePercentage float64  `json:"changePercentage"`
	Change           float64  `json:"change"`
	Volume           *float64 `json:"volume,omitempty"`
	DayLow           *float64 `json:"dayLow,omitempty"`
	DayHigh          *float64 `json:"dayHigh,omitempty"`
	YearHigh         *float64 `json:"yearHigh,omitempty"`
	YearLow          *float64 `json:"yearLow,omitempty"`
	MarketCap        *float64 `json:"marketCap,omitempty"`
	PriceAvg50       *float64 `json:"priceAvg50,omitempty"`
	PriceAvg200      *float64 `json:"priceAvg200,omitempty"`
	Exchange         *string  `json:"exchange,omitempty"`
	Open             *float64 `json:"open,omitempty"`
	PreviousClose    *float64 `json:"previousClose,omitempty"`
	Timestamp        int      `json:"timestamp"`
}

// CryptoHistoricalLightPoint mirrors the Swift DTO of the same name.
type CryptoHistoricalLightPoint struct {
	Symbol string   `json:"symbol"`
	Date   string   `json:"date"`
	Price  float64  `json:"price"`
	Volume *float64 `json:"volume,omitempty"`
}

// CryptoHistoricalPoint mirrors the Swift DTO of the same name.
type CryptoHistoricalPoint struct {
	Date   string   `json:"date"`
	Open   *float64 `json:"open,omitempty"`
	Low    *float64 `json:"low,omitempty"`
	High   *float64 `json:"high,omitempty"`
	Close  float64  `json:"close"`
	Volume *float64 `json:"volume,omitempty"`
}
