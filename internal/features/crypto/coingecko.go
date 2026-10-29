package crypto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/httpx"
)

// MarketsProvider is CryptoMarketsProvider: the ranked crypto universe.
type MarketsProvider interface {
	Name() string
	Attribution() *string
	// FetchUniverse returns the top coins by market cap, FmpSymbol nil and
	// Sector "Other"; the service fills both in.
	FetchUniverse(ctx context.Context, limit int) ([]MarketCoin, error)
	// FetchExcludedIDs returns the provider ids kept out of every list.
	FetchExcludedIDs(ctx context.Context) (map[string]struct{}, error)
}

// ReferenceSource is CryptoReferenceDataSource: FMP's side of the markets
// view (which coins FMP carries, and the year-start price for YTD).
type ReferenceSource interface {
	KnownSymbols(ctx context.Context) (map[string]struct{}, error)
	YearStartPrice(ctx context.Context, symbol string, year int) (*float64, error)
}

// FMPReference is FMPCryptoReferenceDataSource.
type FMPReference struct{ Provider DataProvider }

// KnownSymbols is the upper-cased FMP cryptocurrency list.
func (r FMPReference) KnownSymbols(ctx context.Context) (map[string]struct{}, error) {
	list, err := r.Provider.CryptocurrencyList(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(list))
	for _, a := range list {
		out[strings.ToUpper(a.Symbol)] = struct{}{}
	}
	return out, nil
}

// YearStartPrice is the last close before 1 January of year (or the first
// point of the window), nil when FMP has none or it is not positive.
func (r FMPReference) YearStartPrice(ctx context.Context, symbol string, year int) (*float64, error) {
	from, to := fmt.Sprintf("%d-12-26", year-1), fmt.Sprintf("%d-01-05", year)
	points, err := r.Provider.HistoricalLight(ctx, symbol, &from, &to)
	if err != nil {
		return nil, err
	}
	boundary := fmt.Sprintf("%d-01-01", year)
	sorted := append([]HistoricalLightPoint{}, points...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Date < sorted[j].Date })
	var pick *HistoricalLightPoint
	for i := len(sorted) - 1; i >= 0; i-- {
		if sorted[i].Date < boundary {
			pick = &sorted[i]
			break
		}
	}
	if pick == nil && len(sorted) > 0 {
		pick = &sorted[0]
	}
	if pick == nil || pick.Price <= 0 {
		return nil, nil
	}
	p := pick.Price
	return &p, nil
}

// CoinGeckoPlan is CoinGeckoV3CryptoMarketsProvider.Plan.
type CoinGeckoPlan string

// Plans.
const (
	CoinGeckoPublic CoinGeckoPlan = "public"
	CoinGeckoDemo   CoinGeckoPlan = "demo"
	CoinGeckoPro    CoinGeckoPlan = "pro"
)

// ParseCoinGeckoPlan is COINGECKO_API_PLAN lowercased; unknown or unset is
// demo.
func ParseCoinGeckoPlan(raw *string) CoinGeckoPlan {
	if raw == nil {
		return CoinGeckoDemo
	}
	switch p := CoinGeckoPlan(strings.ToLower(*raw)); p {
	case CoinGeckoPublic, CoinGeckoDemo, CoinGeckoPro:
		return p
	}
	return CoinGeckoDemo
}

// CoinGecko is CoinGeckoV3CryptoMarketsProvider.
type CoinGecko struct {
	APIKey *string
	Plan   CoinGeckoPlan
	// BaseURL overrides the plan's host (tests).
	BaseURL string
	Client  *http.Client
	Logger  *slog.Logger
}

// NewCoinGecko trims the key; a plan that needs a key without one degrades
// to the public API.
func NewCoinGecko(apiKey *string, plan CoinGeckoPlan) *CoinGecko {
	var key *string
	if apiKey != nil {
		if t := strings.TrimSpace(*apiKey); t != "" {
			key = &t
		}
	}
	if key == nil {
		plan = CoinGeckoPublic
	}
	return &CoinGecko{APIKey: key, Plan: plan}
}

func (*CoinGecko) Name() string { return "coingecko" }

func (*CoinGecko) Attribution() *string { return str("Data provided by CoinGecko") }

func (g *CoinGecko) baseURL() string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	if g.Plan == CoinGeckoPro {
		return "https://pro-api.coingecko.com/api/v3"
	}
	return "https://api.coingecko.com/api/v3"
}

func (g *CoinGecko) logger() *slog.Logger {
	if g.Logger != nil {
		return g.Logger
	}
	return slog.Default()
}

// FetchUniverse is /coins/markets ordered by market cap with sparklines and
// the 24h/7d/30d/1y changes.
func (g *CoinGecko) FetchUniverse(ctx context.Context, limit int) ([]MarketCoin, error) {
	var items []coinGeckoMarketItem
	err := g.fetch(ctx, "/coins/markets", [][2]string{
		{"vs_currency", "usd"},
		{"order", "market_cap_desc"},
		{"per_page", strconv.Itoa(min(max(limit, 1), 250))},
		{"page", "1"},
		{"sparkline", "true"},
		{"price_change_percentage", "24h,7d,30d,1y"},
	}, &items)
	if err != nil {
		return nil, err
	}
	out := []MarketCoin{}
	for i := range items {
		if c, ok := items[i].marketCoin(); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// FetchExcludedIDs is the stablecoins category: /coins/markets already
// leaves out wrapped and liquid-staking derivatives.
func (g *CoinGecko) FetchExcludedIDs(ctx context.Context) (map[string]struct{}, error) {
	var items []coinGeckoMarketItem
	err := g.fetch(ctx, "/coins/markets", [][2]string{
		{"vs_currency", "usd"}, {"category", "stablecoins"}, {"per_page", "250"}, {"page", "1"},
	}, &items)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(items))
	for i := range items {
		out[*items[i].ID] = struct{}{}
	}
	return out, nil
}

func (g *CoinGecko) fetch(ctx context.Context, path string, query [][2]string, out *[]coinGeckoMarketItem) error {
	parts := make([]string, len(query))
	for i, q := range query {
		parts[i] = url.QueryEscape(q[0]) + "=" + strings.ReplaceAll(url.QueryEscape(q[1]), "%2C", ",")
	}
	u := g.baseURL() + path + "?" + strings.Join(parts, "&")
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return httpx.Abort(http.StatusInternalServerError, "Invalid CoinGecko URL.")
	}
	req.Header.Set("Accept", "application/json")
	// CoinGecko's edge answers 403 to requests with no User-Agent.
	req.Header.Set("User-Agent", "Norviq/1.0 (+https://norviq.org)")
	if g.APIKey != nil {
		header := "x-cg-demo-api-key"
		if g.Plan == CoinGeckoPro {
			header = "x-cg-pro-api-key"
		}
		req.Header.Set(header, *g.APIKey)
	}
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if err := decodeCoinGecko(body, out); err != nil {
			return httpx.Abort(http.StatusBadGateway, "Failed to decode CoinGecko response for "+path+".")
		}
		return nil
	case http.StatusTooManyRequests:
		retry := resp.Header.Get("Retry-After")
		if retry == "" {
			retry = "-"
		}
		g.logger().Warn(fmt.Sprintf("coingecko rate limited path=%s retry_after=%s", path, retry))
		return httpx.Abort(http.StatusTooManyRequests, "Crypto market data is rate limited.")
	}
	g.logger().Error(fmt.Sprintf("coingecko request failed path=%s status=%d", path, resp.StatusCode))
	return httpx.Abort(http.StatusBadGateway, "Crypto market data isn’t available right now.")
}

// decodeCoinGecko is a plain JSONDecoder (no snake_case rewrite): the
// explicit CodingKeys below are CoinGecko's own keys. Every field but the id
// is optional.
func decodeCoinGecko(body []byte, out *[]coinGeckoMarketItem) error {
	if err := json.Unmarshal(body, out); err != nil {
		return err
	}
	if *out == nil {
		return errors.New("null array")
	}
	for i := range *out {
		if (*out)[i].ID == nil {
			return errors.New("missing id")
		}
	}
	return nil
}

// coinGeckoMarketItem is CoinGeckoMarketItem.
type coinGeckoMarketItem struct {
	ID                                 *string  `json:"id"`
	Symbol                             *string  `json:"symbol"`
	Name                               *string  `json:"name"`
	Image                              *string  `json:"image"`
	MarketCapRank                      *int     `json:"market_cap_rank"`
	CurrentPrice                       *float64 `json:"current_price"`
	MarketCap                          *float64 `json:"market_cap"`
	TotalVolume                        *float64 `json:"total_volume"`
	PriceChangePercentage24h           *float64 `json:"price_change_percentage_24h"`
	PriceChangePercentage24hInCurrency *float64 `json:"price_change_percentage_24h_in_currency"`
	PriceChangePercentage7dInCurrency  *float64 `json:"price_change_percentage_7d_in_currency"`
	PriceChangePercentage30dInCurrency *float64 `json:"price_change_percentage_30d_in_currency"`
	PriceChangePercentage1yInCurrency  *float64 `json:"price_change_percentage_1y_in_currency"`
	Ath                                *float64 `json:"ath"`
	AthChangePercentage                *float64 `json:"ath_change_percentage"`
	AthDate                            *string  `json:"ath_date"`
	Atl                                *float64 `json:"atl"`
	AtlChangePercentage                *float64 `json:"atl_change_percentage"`
	AtlDate                            *string  `json:"atl_date"`
	SparklineIn7d                      *struct {
		Price []float64 `json:"price"`
	} `json:"sparkline_in_7d"`
}

// marketCoin is CoinGeckoMarketItem.marketCoin: false when the item has no
// price or ticker.
func (it *coinGeckoMarketItem) marketCoin() (MarketCoin, bool) {
	if it.CurrentPrice == nil || it.Symbol == nil || *it.Symbol == "" {
		return MarketCoin{}, false
	}
	symbol := strings.ToUpper(*it.Symbol)
	name := symbol
	if it.Name != nil {
		name = *it.Name
	}
	oneDay := it.PriceChangePercentage24hInCurrency
	if oneDay == nil {
		oneDay = it.PriceChangePercentage24h
	}
	spark := []float64{}
	if it.SparklineIn7d != nil && it.SparklineIn7d.Price != nil {
		spark = it.SparklineIn7d.Price
	}
	return MarketCoin{
		ID:        *it.ID,
		Symbol:    symbol,
		Name:      name,
		ImageURL:  it.Image,
		Rank:      it.MarketCapRank,
		Sector:    OtherSector,
		Price:     *it.CurrentPrice,
		MarketCap: it.MarketCap,
		Volume24h: it.TotalVolume,
		Returns: TimeframeReturns{
			OneDay:   oneDay,
			OneWeek:  it.PriceChangePercentage7dInCurrency,
			OneMonth: it.PriceChangePercentage30dInCurrency,
			OneYear:  it.PriceChangePercentage1yInCurrency,
		},
		Ath:          it.Ath,
		AthChangePct: it.AthChangePercentage,
		AthDate:      it.AthDate,
		Atl:          it.Atl,
		AtlChangePct: it.AtlChangePercentage,
		AtlDate:      it.AtlDate,
		Sparkline7d:  Downsample(spark, SparklinePoints),
	}, true
}
