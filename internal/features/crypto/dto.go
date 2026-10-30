package crypto

import "norviq-api/internal/platform/marketdata"

// The FMP DTOs shared with the market platform.
type (
	// QuoteResponse is StockPlanShared.CryptoQuoteResponse.
	QuoteResponse = marketdata.CryptoQuoteResponse
	// HistoricalLightPoint is StockPlanShared.CryptoHistoricalLightPoint.
	HistoricalLightPoint = marketdata.CryptoHistoricalLightPoint
	// HistoricalPoint is StockPlanShared.CryptoHistoricalPoint.
	HistoricalPoint = marketdata.CryptoHistoricalPoint
	// NewsItem is FMPMarketNewsItem.
	NewsItem = marketdata.FMPMarketNewsItem
)

// AssetResponse is StockPlanShared.CryptoAssetResponse.
type AssetResponse struct {
	Symbol            string   `json:"symbol"`
	Name              string   `json:"name"`
	Exchange          *string  `json:"exchange,omitempty"`
	IcoDate           *string  `json:"icoDate,omitempty"`
	CirculatingSupply *float64 `json:"circulatingSupply,omitempty"`
	TotalSupply       *float64 `json:"totalSupply,omitempty"`
}

// QuoteShortResponse is StockPlanShared.CryptoQuoteShortResponse.
type QuoteShortResponse struct {
	Symbol string   `json:"symbol"`
	Price  float64  `json:"price"`
	Change float64  `json:"change"`
	Volume *float64 `json:"volume,omitempty"`
}

// HistoricalFullPoint is StockPlanShared.CryptoHistoricalFullPoint.
type HistoricalFullPoint struct {
	Symbol        string   `json:"symbol"`
	Date          string   `json:"date"`
	Open          float64  `json:"open"`
	High          float64  `json:"high"`
	Low           float64  `json:"low"`
	Close         float64  `json:"close"`
	Volume        *float64 `json:"volume,omitempty"`
	Change        *float64 `json:"change,omitempty"`
	ChangePercent *float64 `json:"changePercent,omitempty"`
	Vwap          *float64 `json:"vwap,omitempty"`
}

// PortfolioItemRequest is StockPlanShared.CryptoPortfolioItemRequest. Its
// CodingKeys spell the last key "average_buy_price"; Vapor's backendAPI
// decoder rewrites every snake_case body key to camelCase before that lookup,
// so a JSON body can never supply it (see the package doc).
type PortfolioItemRequest struct {
	Symbol          string  `json:"symbol"`
	Name            string  `json:"name"`
	Quantity        float64 `json:"quantity"`
	AverageBuyPrice float64 `json:"average_buy_price"`
}

// PortfolioItemResponse is StockPlanShared.CryptoPortfolioItemResponse.
type PortfolioItemResponse struct {
	ID              string  `json:"id"`
	Symbol          string  `json:"symbol"`
	Name            string  `json:"name"`
	Quantity        float64 `json:"quantity"`
	AverageBuyPrice float64 `json:"averageBuyPrice"`
	CreatedAt       *string `json:"createdAt,omitempty"`
	UpdatedAt       *string `json:"updatedAt,omitempty"`
}

// StockNews is StockPlanShared.StockNews.
type StockNews struct {
	Title    string  `json:"title"`
	URL      string  `json:"url"`
	Date     string  `json:"date"`
	ImageURL *string `json:"imageURL,omitempty"`
	Source   *string `json:"source,omitempty"`
	Summary  *string `json:"summary,omitempty"`
	NewsID   *string `json:"newsId,omitempty"`
	Symbol   *string `json:"symbol,omitempty"`
}

// --- /v1/crypto/markets (StockPlanShared CryptoMarketsDTOs) ---

// Timeframe is CryptoMarketsTimeframe.
type Timeframe string

// Timeframes, in CaseIterable order.
const (
	OneDay     Timeframe = "1d"
	OneWeek    Timeframe = "1w"
	OneMonth   Timeframe = "1m"
	YearToDate Timeframe = "ytd"
	OneYear    Timeframe = "1y"
	AllTime    Timeframe = "all"
)

// AllTimeframes is CryptoMarketsTimeframe.allCases.
var AllTimeframes = []Timeframe{OneDay, OneWeek, OneMonth, YearToDate, OneYear, AllTime}

// ParseTimeframe is CryptoMarketsTimeframe(rawValue:).
func ParseTimeframe(raw string) (Timeframe, bool) {
	for _, t := range AllTimeframes {
		if string(t) == raw {
			return t, true
		}
	}
	return "", false
}

// ColorMode is CryptoMarketsColorMode.
type ColorMode string

// Color modes.
const (
	ColorChange      ColorMode = "change"
	ColorAthDistance ColorMode = "athDistance"
)

// TimeframeReturns is CryptoTimeframeReturns (CodingKeys "1d", "1w", ...).
type TimeframeReturns struct {
	OneDay     *float64 `json:"1d,omitempty"`
	OneWeek    *float64 `json:"1w,omitempty"`
	OneMonth   *float64 `json:"1m,omitempty"`
	YearToDate *float64 `json:"ytd,omitempty"`
	OneYear    *float64 `json:"1y,omitempty"`
}

// Value is CryptoTimeframeReturns.value(for:); nil for AllTime.
func (r TimeframeReturns) Value(t Timeframe) *float64 {
	switch t {
	case OneDay:
		return r.OneDay
	case OneWeek:
		return r.OneWeek
	case OneMonth:
		return r.OneMonth
	case YearToDate:
		return r.YearToDate
	case OneYear:
		return r.OneYear
	}
	return nil
}

// MarketCoin is CryptoMarketCoin.
type MarketCoin struct {
	ID           string           `json:"id"`
	Symbol       string           `json:"symbol"`
	FmpSymbol    *string          `json:"fmpSymbol,omitempty"`
	Name         string           `json:"name"`
	ImageURL     *string          `json:"imageUrl,omitempty"`
	Rank         *int             `json:"rank,omitempty"`
	Sector       string           `json:"sector"`
	Price        float64          `json:"price"`
	MarketCap    *float64         `json:"marketCap,omitempty"`
	Volume24h    *float64         `json:"volume24h,omitempty"`
	ChangePct    *float64         `json:"changePct,omitempty"`
	Returns      TimeframeReturns `json:"returns"`
	Ath          *float64         `json:"ath,omitempty"`
	AthChangePct *float64         `json:"athChangePct,omitempty"`
	AthDate      *string          `json:"athDate,omitempty"`
	Atl          *float64         `json:"atl,omitempty"`
	AtlChangePct *float64         `json:"atlChangePct,omitempty"`
	AtlDate      *string          `json:"atlDate,omitempty"`
	Sparkline7d  []float64        `json:"sparkline7d"`
}

// MarketsSummary is CryptoMarketsSummary.
type MarketsSummary struct {
	TotalMarketCap  *float64 `json:"totalMarketCap,omitempty"`
	BtcDominancePct *float64 `json:"btcDominancePct,omitempty"`
	Advancers       int      `json:"advancers"`
	Decliners       int      `json:"decliners"`
}

// AthBoard is CryptoAthBoard.
type AthBoard struct {
	RecentAths       []MarketCoin `json:"recentAths"`
	NearAth          []MarketCoin `json:"nearAth"`
	DeepestDrawdowns []MarketCoin `json:"deepestDrawdowns"`
}

// MarketsResponse is CryptoMarketsResponse.
type MarketsResponse struct {
	Timeframe           Timeframe      `json:"timeframe"`
	SupportedTimeframes []Timeframe    `json:"supportedTimeframes"`
	Source              string         `json:"source"`
	AsOf                string         `json:"asOf"`
	IsStale             bool           `json:"isStale"`
	ColorMode           ColorMode      `json:"colorMode"`
	ColorScaleMaxPct    float64        `json:"colorScaleMaxPct"`
	Attribution         *string        `json:"attribution,omitempty"`
	Summary             MarketsSummary `json:"summary"`
	Coins               []MarketCoin   `json:"coins"`
	Gainers             []MarketCoin   `json:"gainers"`
	Losers              []MarketCoin   `json:"losers"`
	AthBoard            AthBoard       `json:"athBoard"`
}

// MarketSnapshot is CryptoMarketSnapshot: what the refresh captures and the
// "crypto_markets" Redis cache holds (shared with Vapor, so the keys and
// shape are Swift's default JSONEncoder output).
type MarketSnapshot struct {
	Source          string       `json:"source"`
	Attribution     *string      `json:"attribution,omitempty"`
	AsOf            string       `json:"asOf"`
	TotalMarketCap  *float64     `json:"totalMarketCap,omitempty"`
	BtcDominancePct *float64     `json:"btcDominancePct,omitempty"`
	Coins           []MarketCoin `json:"coins"`
}
