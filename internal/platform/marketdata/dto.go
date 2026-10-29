package marketdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

// Wire DTOs. Field order and JSON keys follow the Swift types (StockPlanShared
// v5.25.0 and Market/*DTOs.swift). Optionals are pointers with omitempty:
// Swift omitted nil optionals rather than sending null.

// StockDetailsResponse is GET /v1/market/details.
type StockDetailsResponse struct {
	Symbol        string  `json:"symbol"`
	Company       string  `json:"company"`
	LatestPrice   float64 `json:"latestPrice"`
	ChangePercent float64 `json:"changePercent"`
}

// QuoteResponse uses Finnhub's single-letter keys, as the Swift CodingKeys do.
type QuoteResponse struct {
	Symbol        string   `json:"symbol"`
	Currency      string   `json:"currency"`
	CurrentPrice  float64  `json:"c"`
	Change        *float64 `json:"d,omitempty"`
	PercentChange *float64 `json:"dp,omitempty"`
	High          *float64 `json:"h,omitempty"`
	Low           *float64 `json:"l,omitempty"`
	Open          *float64 `json:"o,omitempty"`
	PreviousClose *float64 `json:"pc,omitempty"`
	Timestamp     float64  `json:"t"`
}

// CompanyProfileResponse is GET /v1/market/profile/:symbol.
type CompanyProfileResponse struct {
	Country              *string  `json:"country,omitempty"`
	Currency             *string  `json:"currency,omitempty"`
	EstimateCurrency     *string  `json:"estimateCurrency,omitempty"`
	Exchange             *string  `json:"exchange,omitempty"`
	FinnhubIndustry      *string  `json:"finnhubIndustry,omitempty"`
	IPO                  *string  `json:"ipo,omitempty"`
	Logo                 *string  `json:"logo,omitempty"`
	MarketCapitalization *float64 `json:"marketCapitalization,omitempty"`
	Name                 *string  `json:"name,omitempty"`
	Phone                *string  `json:"phone,omitempty"`
	ShareOutstanding     *float64 `json:"shareOutstanding,omitempty"`
	Ticker               *string  `json:"ticker,omitempty"`
	Weburl               *string  `json:"weburl,omitempty"`
	LastUpdated          *string  `json:"lastUpdated,omitempty"`
}

// PriceBarResponse is one daily bar; Date is yyyy-MM-dd.
type PriceBarResponse struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume *int    `json:"volume,omitempty"`
}

// HistoryResponse is GET /v1/market/history/:symbol.
type HistoryResponse struct {
	Symbol   string             `json:"symbol"`
	Currency string             `json:"currency"`
	Bars     []PriceBarResponse `json:"bars"`
}

// SearchResultResponse is one GET /v1/market/search row.
type SearchResultResponse struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	Currency string `json:"currency"`
	Conid    string `json:"conid"`
}

// FxRateResponse is GET /v1/market/fx.
type FxRateResponse struct {
	Base  string  `json:"base"`
	Quote string  `json:"quote"`
	Rate  float64 `json:"rate"`
	Date  string  `json:"date"`
}

// QuoteBatchResponse is GET /v1/market/quote/batch.
type QuoteBatchResponse struct {
	Quotes []QuoteResponse `json:"quotes"`
}

// StockHistory is the shared StockHistory DTO (newest first lists).
type StockHistory struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume int     `json:"volume"`
}

// MetricValue is BasicFinancialMetricValue: a number, string, bool or null.
type MetricValue struct {
	kind   byte // 'n' number, 's' string, 'b' bool, 0 null
	number float64
	text   string
	flag   bool
}

// Number returns the value when it is numeric.
func (m MetricValue) Number() (float64, bool) { return m.number, m.kind == 'n' }

// NumberValue builds a numeric metric.
func NumberValue(v float64) MetricValue { return MetricValue{kind: 'n', number: v} }

// StringValue builds a string metric.
func StringValue(v string) MetricValue { return MetricValue{kind: 's', text: v} }

func (m MetricValue) MarshalJSON() ([]byte, error) {
	switch m.kind {
	case 'n':
		return json.Marshal(m.number)
	case 's':
		return json.Marshal(m.text)
	case 'b':
		return json.Marshal(m.flag)
	}
	return []byte("null"), nil
}

var errUnsupportedMetric = errors.New("Unsupported basic financial metric value.")

func (m *MetricValue) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case bytes.Equal(data, []byte("null")):
		*m = MetricValue{}
	case bytes.Equal(data, []byte("true")), bytes.Equal(data, []byte("false")):
		*m = MetricValue{kind: 'b', flag: data[0] == 't'}
	case len(data) > 0 && data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*m = MetricValue{kind: 's', text: s}
	default:
		f, err := strconv.ParseFloat(string(data), 64)
		if err != nil {
			return errUnsupportedMetric
		}
		*m = MetricValue{kind: 'n', number: f}
	}
	return nil
}

// BasicFinancialSeriesPoint is one series sample; the value travels as "v".
type BasicFinancialSeriesPoint struct {
	Period string  `json:"period"`
	Value  float64 `json:"v"`
}

// BasicFinancialsResponse is GET /v1/market/basic-financials/:symbol.
type BasicFinancialsResponse struct {
	Symbol     string                                            `json:"symbol"`
	MetricType string                                            `json:"metricType"`
	Metric     map[string]MetricValue                            `json:"metric"`
	Series     map[string]map[string][]BasicFinancialSeriesPoint `json:"series"`
}

// HistoricalSectorPerformanceResponse is one FMP sector performance row.
type HistoricalSectorPerformanceResponse struct {
	Date          string  `json:"date"`
	Sector        string  `json:"sector"`
	Exchange      string  `json:"exchange"`
	AverageChange float64 `json:"averageChange"`
}

// GradesConsensusResponse is one FMP grades consensus row.
type GradesConsensusResponse struct {
	Symbol     string  `json:"symbol"`
	StrongBuy  *int    `json:"strongBuy,omitempty"`
	Buy        *int    `json:"buy,omitempty"`
	Hold       *int    `json:"hold,omitempty"`
	Sell       *int    `json:"sell,omitempty"`
	StrongSell *int    `json:"strongSell,omitempty"`
	Consensus  *string `json:"consensus,omitempty"`
}

// EarningsResponse is one earnings row, per symbol or from the calendar.
type EarningsResponse struct {
	Symbol           string   `json:"symbol"`
	Date             string   `json:"date"`
	EpsActual        *float64 `json:"epsActual,omitempty"`
	EpsEstimated     *float64 `json:"epsEstimated,omitempty"`
	RevenueActual    *float64 `json:"revenueActual,omitempty"`
	RevenueEstimated *float64 `json:"revenueEstimated,omitempty"`
	LastUpdated      *string  `json:"lastUpdated,omitempty"`
	SurprisePercent  *float64 `json:"surprisePercent,omitempty"`
	HasTranscript    bool     `json:"hasTranscript"`
	BeatStreak       int      `json:"beatStreak"`
	MissStreak       int      `json:"missStreak"`
}

// EarningsTranscriptResponse is GET /v1/market/earnings/:symbol/transcript.
type EarningsTranscriptResponse struct {
	Symbol   string  `json:"symbol"`
	Date     string  `json:"date"`
	Year     int     `json:"year"`
	Quarter  int     `json:"quarter"`
	Period   *string `json:"period,omitempty"`
	Content  string  `json:"content"`
	Provider string  `json:"provider"`
}

// YearlyProjectionResponse is one DCF projection year.
type YearlyProjectionResponse struct {
	Year            int      `json:"year"`
	Revenue         float64  `json:"revenue"`
	RevenueGrowth   float64  `json:"revenueGrowth"`
	NetIncome       float64  `json:"netIncome"`
	NetIncomeGrowth float64  `json:"netIncomeGrowth"`
	NetMargin       float64  `json:"netMargin"`
	EPS             float64  `json:"eps"`
	FCF             *float64 `json:"fcf,omitempty"`
	FCFMargin       *float64 `json:"fcfMargin,omitempty"`
}

// StockAnalysisMetricsResponse is GET /v1/market/analysis/:symbol.
type StockAnalysisMetricsResponse struct {
	Symbol                                    string                     `json:"symbol"`
	TtmPE                                     *float64                   `json:"ttmPE,omitempty"`
	ForwardPE                                 *float64                   `json:"forwardPE,omitempty"`
	TwoYearForwardPE                          *float64                   `json:"twoYearForwardPE,omitempty"`
	TtmEPSGrowth                              *float64                   `json:"ttmEPSGrowth,omitempty"`
	CurrentYearExpectedEPSGrowth              *float64                   `json:"currentYearExpectedEPSGrowth,omitempty"`
	NextYearEPSGrowth                         *float64                   `json:"nextYearEPSGrowth,omitempty"`
	TtmRevenueGrowth                          *float64                   `json:"ttmRevenueGrowth,omitempty"`
	CurrentYearExpectedRevenueGrowth          *float64                   `json:"currentYearExpectedRevenueGrowth,omitempty"`
	NextYearRevenueGrowth                     *float64                   `json:"nextYearRevenueGrowth,omitempty"`
	GrossMargin                               *float64                   `json:"grossMargin,omitempty"`
	NetMargin                                 *float64                   `json:"netMargin,omitempty"`
	TtmPEGRatio                               *float64                   `json:"ttmPEGRatio,omitempty"`
	LastYearEPSGrowth                         *float64                   `json:"lastYearEPSGrowth,omitempty"`
	TtmVsNTMEPSGrowth                         *float64                   `json:"ttmVsNTMEPSGrowth,omitempty"`
	CurrentQuarterEPSGrowthVsPreviousYear     *float64                   `json:"currentQuarterEPSGrowthVsPreviousYear,omitempty"`
	TwoYearStackExpectedEPSGrowth             *float64                   `json:"twoYearStackExpectedEPSGrowth,omitempty"`
	LastYearRevenueGrowth                     *float64                   `json:"lastYearRevenueGrowth,omitempty"`
	TtmVsNTMRevenueGrowth                     *float64                   `json:"ttmVsNTMRevenueGrowth,omitempty"`
	CurrentQuarterRevenueGrowthVsPreviousYear *float64                   `json:"currentQuarterRevenueGrowthVsPreviousYear,omitempty"`
	TwoYearStackExpectedRevenueGrowth         *float64                   `json:"twoYearStackExpectedRevenueGrowth,omitempty"`
	CurrentPrice                              *float64                   `json:"currentPrice,omitempty"`
	MarketCap                                 *float64                   `json:"marketCap,omitempty"`
	SharesOutstanding                         *float64                   `json:"sharesOutstanding,omitempty"`
	BaseYear                                  *int                       `json:"baseYear,omitempty"`
	YearlyProjections                         []YearlyProjectionResponse `json:"yearlyProjections,omitempty"`
	WACC                                      *float64                   `json:"wacc,omitempty"`
	TerminalGrowthRate                        *float64                   `json:"terminalGrowthRate,omitempty"`
	TerminalMargin                            *float64                   `json:"terminalMargin,omitempty"`
	ExitPELow                                 *float64                   `json:"exitPELow,omitempty"`
	ExitPEHigh                                *float64                   `json:"exitPEHigh,omitempty"`
	DCFBasePrice                              *float64                   `json:"dcfBasePrice,omitempty"`
	DCFBearPrice                              *float64                   `json:"dcfBearPrice,omitempty"`
	DCFBullPrice                              *float64                   `json:"dcfBullPrice,omitempty"`
	NetDebt                                   *float64                   `json:"netDebt,omitempty"`
}

// PriceChartPoint is one chart sample.
type PriceChartPoint struct {
	Date   string   `json:"date"`
	Close  float64  `json:"close"`
	Open   *float64 `json:"open,omitempty"`
	High   *float64 `json:"high,omitempty"`
	Low    *float64 `json:"low,omitempty"`
	Volume *int     `json:"volume,omitempty"`
}

// PriceChartSeries is GET /v1/market/price-chart/:symbol.
type PriceChartSeries struct {
	Symbol   string            `json:"symbol"`
	Currency string            `json:"currency"`
	Range    string            `json:"range"`
	Points   []PriceChartPoint `json:"points"`
}

// PriceChartComparisonResponse is GET /v1/market/price-chart/compare.
type PriceChartComparisonResponse struct {
	Series []PriceChartSeries `json:"series"`
	Range  string             `json:"range"`
}

// MarketOverviewResponse is GET /v1/market/overview.
type MarketOverviewResponse struct {
	Indices []MarketIndexQuote  `json:"indices"`
	Gainers []MarketMover       `json:"gainers"`
	Losers  []MarketMover       `json:"losers"`
	Heatmap []MarketHeatmapTile `json:"heatmap"`
	AsOf    string              `json:"asOf"`
}

// MarketIndexQuote is one index strip entry.
type MarketIndexQuote struct {
	Symbol    string  `json:"symbol"`
	Label     string  `json:"label"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"changePct"`
	IsProxy   bool    `json:"isProxy"`
}

// MarketMover is one gainer or loser.
type MarketMover struct {
	Symbol    string  `json:"symbol"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"changePct"`
}

// MarketHeatmapTile is one heatmap cell.
type MarketHeatmapTile struct {
	Symbol    string  `json:"symbol"`
	Name      string  `json:"name"`
	Sector    string  `json:"sector"`
	MarketCap float64 `json:"marketCap"`
	ChangePct float64 `json:"changePct"`
}

// FMPMoverItem is one /stable/biggest-gainers|losers row.
type FMPMoverItem struct {
	Symbol            string   `json:"symbol"`
	Name              *string  `json:"name,omitempty"`
	Price             *float64 `json:"price,omitempty"`
	Change            *float64 `json:"change,omitempty"`
	ChangesPercentage *float64 `json:"changesPercentage,omitempty"`
}

// FMPScreenerItem is one /stable/company-screener row. It is also what the
// overview caches under market:overview:universe:v1.
type FMPScreenerItem struct {
	Symbol            string   `json:"symbol"`
	CompanyName       *string  `json:"companyName,omitempty"`
	MarketCap         *float64 `json:"marketCap,omitempty"`
	Sector            *string  `json:"sector,omitempty"`
	IsEtf             *bool    `json:"isEtf,omitempty"`
	IsActivelyTrading *bool    `json:"isActivelyTrading,omitempty"`
}

// MarketPressureResponse is GET /v1/market/pressure/:symbol.
type MarketPressureResponse struct {
	Symbol      string                       `json:"symbol"`
	AsOf        string                       `json:"asOf"`
	Temperature float64                      `json:"temperature"`
	Label       string                       `json:"label"`
	Volume      MarketPressureVolume         `json:"volume"`
	Insider     *MarketPressureInsider       `json:"insider,omitempty"`
	History     []MarketPressureHistoryPoint `json:"history"`
}

// MarketPressureVolume is the volume block of the pressure view.
type MarketPressureVolume struct {
	Today      float64 `json:"today"`
	Average30d float64 `json:"average30d"`
	Relative   float64 `json:"relative"`
	ChangePct  float64 `json:"changePct"`
}

// MarketPressureInsider summarizes recent insider filings.
type MarketPressureInsider struct {
	WindowDays     int                          `json:"windowDays"`
	BuyCount       int                          `json:"buyCount"`
	SellCount      int                          `json:"sellCount"`
	NetShares      float64                      `json:"netShares"`
	LastActivityAt *string                      `json:"lastActivityAt,omitempty"`
	Notable        []MarketPressureInsiderTrade `json:"notable"`
}

// MarketPressureInsiderTrade is one notable insider trade; Side is buy|sell.
type MarketPressureInsiderTrade struct {
	Name   string  `json:"name"`
	Role   *string `json:"role,omitempty"`
	Side   string  `json:"side"`
	Shares float64 `json:"shares"`
	Date   string  `json:"date"`
}

// MarketPressureHistoryPoint is one relative-volume sample.
type MarketPressureHistoryPoint struct {
	Date           string  `json:"date"`
	RelativeVolume float64 `json:"relativeVolume"`
}

// FMPInsiderTrade is one /stable/insider-trading/search row.
type FMPInsiderTrade struct {
	Symbol                   *string  `json:"symbol,omitempty"`
	FilingDate               *string  `json:"filingDate,omitempty"`
	TransactionDate          *string  `json:"transactionDate,omitempty"`
	TransactionType          *string  `json:"transactionType,omitempty"`
	SecuritiesTransacted     *float64 `json:"securitiesTransacted,omitempty"`
	SecuritiesOwned          *float64 `json:"securitiesOwned,omitempty"`
	Price                    *float64 `json:"price,omitempty"`
	ReportingName            *string  `json:"reportingName,omitempty"`
	TypeOfOwner              *string  `json:"typeOfOwner,omitempty"`
	AcquisitionOrDisposition *string  `json:"acquisitionOrDisposition,omitempty"`
	URL                      *string  `json:"url,omitempty"`
}

// StockPeriodReturnsResponse is GET /v1/market/returns/:symbol.
type StockPeriodReturnsResponse struct {
	Symbol     string   `json:"symbol"`
	ThreeMonth *float64 `json:"threeMonth,omitempty"`
	SixMonth   *float64 `json:"sixMonth,omitempty"`
	YearToDate *float64 `json:"yearToDate,omitempty"`
	AsOf       *string  `json:"asOf,omitempty"`
}

// HasUsableWindows is true when at least one window is known.
func (r StockPeriodReturnsResponse) HasUsableWindows() bool {
	return r.ThreeMonth != nil || r.SixMonth != nil || r.YearToDate != nil
}

// StockPeriodReturnsBatchResponse is GET /v1/market/returns/batch.
type StockPeriodReturnsBatchResponse struct {
	Returns []StockPeriodReturnsResponse `json:"returns"`
}

// RecentEarningsResponse is GET /v1/market/earnings/:symbol/recent.
type RecentEarningsResponse struct {
	Symbol        string                  `json:"symbol"`
	Quarters      []RecentEarningsQuarter `json:"quarters"`
	NextScheduled *RecentEarningsQuarter  `json:"nextScheduled,omitempty"`
}

// RecentEarningsQuarter is one teaser quarter; Status is reported|scheduled.
type RecentEarningsQuarter struct {
	Date            string   `json:"date"`
	EpsEstimated    *float64 `json:"epsEstimated,omitempty"`
	EpsActual       *float64 `json:"epsActual,omitempty"`
	SurprisePercent *float64 `json:"surprisePercent,omitempty"`
	HasTranscript   bool     `json:"hasTranscript"`
	BeatStreak      int      `json:"beatStreak"`
	MissStreak      int      `json:"missStreak"`
	Status          string   `json:"status"`
}

// InsiderTrade is one insider filing; Kind is buy|sell|award|other.
type InsiderTrade struct {
	Date             string   `json:"date"`
	ReporterName     string   `json:"reporterName"`
	ReporterTitle    *string  `json:"reporterTitle,omitempty"`
	TransactionType  string   `json:"transactionType"`
	Kind             string   `json:"kind"`
	Shares           float64  `json:"shares"`
	PricePerShare    *float64 `json:"pricePerShare,omitempty"`
	Value            *float64 `json:"value,omitempty"`
	SharesOwnedAfter *float64 `json:"sharesOwnedAfter,omitempty"`
	FilingURL        *string  `json:"filingURL,omitempty"`
}

// InsiderActivitySummary nets buys against sells.
type InsiderActivitySummary struct {
	Buys      int     `json:"buys"`
	Sells     int     `json:"sells"`
	NetShares float64 `json:"netShares"`
	NetValue  float64 `json:"netValue"`
}

// ClusterBuySignal flags several insiders buying inside one window.
type ClusterBuySignal struct {
	InsiderCount int     `json:"insiderCount"`
	WindowStart  string  `json:"windowStart"`
	WindowEnd    string  `json:"windowEnd"`
	TotalValue   float64 `json:"totalValue"`
}

// InsiderActivityResponse is GET /v1/market/insider/:symbol.
type InsiderActivityResponse struct {
	Symbol     string                 `json:"symbol"`
	WindowDays int                    `json:"windowDays"`
	Trades     []InsiderTrade         `json:"trades"`
	Summary    InsiderActivitySummary `json:"summary"`
	ClusterBuy *ClusterBuySignal      `json:"clusterBuy,omitempty"`
}

// CongressTrade is one congressional disclosure. Chamber is senate|house;
// Type is purchase|sale|exchange|other.
type CongressTrade struct {
	Chamber          string   `json:"chamber"`
	Politician       string   `json:"politician"`
	Party            *string  `json:"party,omitempty"`
	State            *string  `json:"state,omitempty"`
	Symbol           string   `json:"symbol"`
	TransactionDate  string   `json:"transactionDate"`
	DisclosureDate   string   `json:"disclosureDate"`
	Type             string   `json:"type"`
	AmountRange      string   `json:"amountRange"`
	AmountMin        *float64 `json:"amountMin,omitempty"`
	AmountMax        *float64 `json:"amountMax,omitempty"`
	AssetDescription *string  `json:"assetDescription,omitempty"`
	Link             *string  `json:"link,omitempty"`
}

// CongressTradesResponse is GET /v1/market/congress/*.
type CongressTradesResponse struct {
	Trades []CongressTrade `json:"trades"`
}

// FMPCongressTrade is one /stable/senate-*|house-* row.
type FMPCongressTrade struct {
	Symbol           *string `json:"symbol,omitempty"`
	DisclosureDate   *string `json:"disclosureDate,omitempty"`
	TransactionDate  *string `json:"transactionDate,omitempty"`
	FirstName        *string `json:"firstName,omitempty"`
	LastName         *string `json:"lastName,omitempty"`
	Office           *string `json:"office,omitempty"`
	District         *string `json:"district,omitempty"`
	State            *string `json:"state,omitempty"`
	Party            *string `json:"party,omitempty"`
	Owner            *string `json:"owner,omitempty"`
	AssetDescription *string `json:"assetDescription,omitempty"`
	AssetType        *string `json:"assetType,omitempty"`
	Type             *string `json:"type,omitempty"`
	Amount           *string `json:"amount,omitempty"`
	Link             *string `json:"link,omitempty"`
	SenateID         *string `json:"senateID,omitempty"`
}

// InstitutionalHolder is one 13F holder.
type InstitutionalHolder struct {
	Name           string   `json:"name"`
	Shares         float64  `json:"shares"`
	MarketValue    *float64 `json:"marketValue,omitempty"`
	ChangeInShares *float64 `json:"changeInShares,omitempty"`
	ChangePct      *float64 `json:"changePct,omitempty"`
	WeightPct      *float64 `json:"weightPct,omitempty"`
}

// InstitutionalOwnershipResponse is GET /v1/market/institutional/:symbol.
type InstitutionalOwnershipResponse struct {
	Symbol                    string                `json:"symbol"`
	AsOfQuarter               string                `json:"asOfQuarter"`
	HoldersCount              *int                  `json:"holdersCount,omitempty"`
	InstitutionalOwnershipPct *float64              `json:"institutionalOwnershipPct,omitempty"`
	TotalShares               *float64              `json:"totalShares,omitempty"`
	TopHolders                []InstitutionalHolder `json:"topHolders"`
}

// FMPInstitutionalHolder is one extract-analytics/holder row.
type FMPInstitutionalHolder struct {
	InvestorName                   *string  `json:"investorName,omitempty"`
	SharesNumber                   *float64 `json:"sharesNumber,omitempty"`
	MarketValue                    *float64 `json:"marketValue,omitempty"`
	ChangeInSharesNumber           *float64 `json:"changeInSharesNumber,omitempty"`
	ChangeInSharesNumberPercentage *float64 `json:"changeInSharesNumberPercentage,omitempty"`
	Weight                         *float64 `json:"weight,omitempty"`
}

// FMPInstitutionalPositionsSummary is one symbol-positions-summary row.
type FMPInstitutionalPositionsSummary struct {
	Symbol            *string  `json:"symbol,omitempty"`
	Date              *string  `json:"date,omitempty"`
	InvestorsHolding  *int     `json:"investorsHolding,omitempty"`
	NumberOf13Fshares *float64 `json:"numberOf13Fshares,omitempty"`
	OwnershipPercent  *float64 `json:"ownershipPercent,omitempty"`
}

// TechnicalMACD is the MACD block; Sign is positive|negative.
type TechnicalMACD struct {
	Line      float64 `json:"line"`
	Signal    float64 `json:"signal"`
	Histogram float64 `json:"histogram"`
	Sign      string  `json:"sign"`
}

// TechnicalFiftyTwoWeek is the 52-week range block.
type TechnicalFiftyTwoWeek struct {
	High        float64 `json:"high"`
	Low         float64 `json:"low"`
	PositionPct float64 `json:"positionPct"`
}

// TechnicalSignalsResponse is GET /v1/market/technicals/:symbol. Trend is
// aboveBoth|belowBoth|mixed; Cross is goldenCross|deathCross|none.
type TechnicalSignalsResponse struct {
	Symbol       string                `json:"symbol"`
	AsOf         string                `json:"asOf"`
	Close        float64               `json:"close"`
	SMA50        *float64              `json:"sma50,omitempty"`
	SMA200       *float64              `json:"sma200,omitempty"`
	Trend        string                `json:"trend"`
	Cross        string                `json:"cross"`
	RSI14        *float64              `json:"rsi14,omitempty"`
	MACD         *TechnicalMACD        `json:"macd,omitempty"`
	FiftyTwoWeek TechnicalFiftyTwoWeek `json:"fiftyTwoWeek"`
}

// FMPMarketNewsItem is one FMP news row.
type FMPMarketNewsItem struct {
	Symbol        *string `json:"symbol,omitempty"`
	PublishedDate *string `json:"publishedDate,omitempty"`
	Title         *string `json:"title,omitempty"`
	Image         *string `json:"image,omitempty"`
	Site          *string `json:"site,omitempty"`
	Publisher     *string `json:"publisher,omitempty"`
	Text          *string `json:"text,omitempty"`
	URL           *string `json:"url,omitempty"`
}

// FMPStockPriceChange is one /stable/stock-price-change row. FMP spells the
// windows "3M"/"6M"/"ytd" (either case) and has sent them as numbers, ints
// or numeric strings; any other shape reads as absent.
type FMPStockPriceChange struct {
	Symbol     string
	ThreeMonth *float64
	SixMonth   *float64
	YearToDate *float64
}

func (p *FMPStockPriceChange) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	symRaw, ok := raw["symbol"]
	if !ok {
		return errors.New("missing symbol")
	}
	if err := json.Unmarshal(symRaw, &p.Symbol); err != nil {
		return err
	}
	p.ThreeMonth = flexibleDouble(raw, "3M", "3m")
	p.SixMonth = flexibleDouble(raw, "6M", "6m")
	p.YearToDate = flexibleDouble(raw, "ytd", "YTD")
	return nil
}

func flexibleDouble(raw map[string]json.RawMessage, keys ...string) *float64 {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			return &f
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if parsed, err := strconv.ParseFloat(s, 64); err == nil {
				return &parsed
			}
		}
	}
	return nil
}
