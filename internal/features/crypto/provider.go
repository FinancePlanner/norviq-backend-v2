package crypto

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
)

// DataProvider is CryptoDataProvider: the market-data side of /v1/crypto.
type DataProvider interface {
	CryptocurrencyList(ctx context.Context) ([]AssetResponse, error)
	Quote(ctx context.Context, symbols string) ([]QuoteResponse, error)
	QuoteShort(ctx context.Context, symbol string) ([]QuoteShortResponse, error)
	BatchQuotes(ctx context.Context, short bool) ([]QuoteShortResponse, error)
	HistoricalLight(ctx context.Context, symbol string, from, to *string) ([]HistoricalLightPoint, error)
	HistoricalFull(ctx context.Context, symbol string, from, to *string) ([]HistoricalFullPoint, error)
	// Intraday serves the 1min, 5min and 1hour charts.
	Intraday(ctx context.Context, interval, symbol string, from, to *string) ([]HistoricalPoint, error)
	News(ctx context.Context, symbol *string, page, limit *int, from, to *string) ([]NewsItem, error)
}

// SelectProvider is makeCryptoDataProvider: FMP when configured, the mock in
// development and testing, otherwise the disabled provider (503s), so
// production never shows fake prices.
func SelectProvider(fmp *marketdata.FMP, env config.Environment) DataProvider {
	if fmp != nil {
		return FMPProvider{FMP: fmp}
	}
	switch env {
	case config.Development, config.Testing:
		return MockProvider{}
	}
	return DisabledProvider{}
}

// --- FMP (LiveFMPMarketDataProvider's CryptoDataProvider conformance) ---

// FMPProvider reads crypto data from FMP's /stable API.
type FMPProvider struct{ FMP *marketdata.FMP }

func fmpGet[T any](ctx context.Context, f *marketdata.FMP, path string, q marketdata.Query) ([]T, error) {
	var out []T
	if err := f.Get(ctx, path, q, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []T{}
	}
	return out, nil
}

// normalizeSymbol is LiveFMPMarketDataProvider.normalizeSymbol.
func normalizeSymbol(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", httpx.BadRequest("Symbol is required.")
	}
	return s, nil
}

// normalizeSymbols is LiveFMPMarketDataProvider.normalizeSymbols.
func normalizeSymbols(raw string) (string, error) {
	var parts []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "", httpx.BadRequest("At least one symbol is required.")
	}
	return strings.Join(parts, ","), nil
}

func intPtrString(v *int) *string {
	if v == nil {
		return nil
	}
	s := strconv.Itoa(*v)
	return &s
}

func (p FMPProvider) CryptocurrencyList(ctx context.Context) ([]AssetResponse, error) {
	return fmpGet[AssetResponse](ctx, p.FMP, "/stable/cryptocurrency-list", nil)
}

func (p FMPProvider) Quote(ctx context.Context, symbols string) ([]QuoteResponse, error) {
	s, err := normalizeSymbols(symbols)
	if err != nil {
		return nil, err
	}
	return fmpGet[QuoteResponse](ctx, p.FMP, "/stable/quote", marketdata.Query{marketdata.Q("symbol", s)})
}

func (p FMPProvider) QuoteShort(ctx context.Context, symbol string) ([]QuoteShortResponse, error) {
	s, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[QuoteShortResponse](ctx, p.FMP, "/stable/quote-short", marketdata.Query{marketdata.Q("symbol", s)})
}

func (p FMPProvider) BatchQuotes(ctx context.Context, short bool) ([]QuoteShortResponse, error) {
	var q marketdata.Query
	if short {
		q = append(q, marketdata.Q("short", "true"))
	}
	return fmpGet[QuoteShortResponse](ctx, p.FMP, "/stable/batch-crypto-quotes", q)
}

func (p FMPProvider) HistoricalLight(ctx context.Context, symbol string, from, to *string) ([]HistoricalLightPoint, error) {
	s, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[HistoricalLightPoint](ctx, p.FMP, "/stable/historical-price-eod/light",
		marketdata.Query{marketdata.Q("symbol", s), marketdata.QOpt("from", from), marketdata.QOpt("to", to)})
}

func (p FMPProvider) HistoricalFull(ctx context.Context, symbol string, from, to *string) ([]HistoricalFullPoint, error) {
	s, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[HistoricalFullPoint](ctx, p.FMP, "/stable/historical-price-eod/full",
		marketdata.Query{marketdata.Q("symbol", s), marketdata.QOpt("from", from), marketdata.QOpt("to", to)})
}

func (p FMPProvider) Intraday(ctx context.Context, interval, symbol string, from, to *string) ([]HistoricalPoint, error) {
	s, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[HistoricalPoint](ctx, p.FMP, "/stable/historical-chart/"+interval,
		marketdata.Query{marketdata.Q("symbol", s), marketdata.QOpt("from", from), marketdata.QOpt("to", to)})
}

func (p FMPProvider) News(ctx context.Context, symbol *string, page, limit *int, from, to *string) ([]NewsItem, error) {
	var q marketdata.Query
	if symbol != nil {
		s, err := normalizeSymbol(*symbol)
		if err != nil {
			return nil, err
		}
		q = append(q, marketdata.Q("symbol", s))
	}
	q = append(q, marketdata.QOpt("page", intPtrString(page)), marketdata.QOpt("limit", intPtrString(limit)),
		marketdata.QOpt("from", from), marketdata.QOpt("to", to))
	return fmpGet[NewsItem](ctx, p.FMP, "/stable/news/crypto-latest", q)
}

// --- Mock (MockCryptoDataProvider) ---

// MockProvider serves fixed sample data in development and testing.
type MockProvider struct {
	// Now stamps the quote; nil means time.Now.
	Now func() time.Time
}

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

func (MockProvider) CryptocurrencyList(context.Context) ([]AssetResponse, error) {
	return []AssetResponse{
		{Symbol: "BTCUSD", Name: "Bitcoin", Exchange: str("CRYPTO"), IcoDate: str("2009-01-03"), CirculatingSupply: f64(19_000_000), TotalSupply: f64(21_000_000)},
		{Symbol: "ETHUSD", Name: "Ethereum", Exchange: str("CRYPTO"), IcoDate: str("2015-07-30"), CirculatingSupply: f64(120_000_000)},
		{Symbol: "SOLUSD", Name: "Solana", Exchange: str("CRYPTO"), IcoDate: str("2020-03-16"), CirculatingSupply: f64(400_000_000)},
	}, nil
}

func (m MockProvider) Quote(context.Context, string) ([]QuoteResponse, error) {
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	return []QuoteResponse{{
		Symbol: "BTCUSD", Name: "Bitcoin", Price: 118_741.16, ChangePercentage: 1.25, Change: 1450.50,
		Volume: f64(75_302_985_728), MarketCap: f64(2_344_693_699_320), Timestamp: int(now().Unix()),
	}}, nil
}

func (MockProvider) QuoteShort(_ context.Context, symbol string) ([]QuoteShortResponse, error) {
	return []QuoteShortResponse{{Symbol: strings.ToUpper(symbol), Price: 118_741.16, Change: -37.93, Volume: f64(75_302_985_728)}}, nil
}

func (MockProvider) BatchQuotes(context.Context, bool) ([]QuoteShortResponse, error) {
	return []QuoteShortResponse{
		{Symbol: "BTCUSD", Price: 118_741.16, Change: -37.93, Volume: f64(75_302_985_728)},
		{Symbol: "ETHUSD", Price: 4250.80, Change: -36.20, Volume: f64(25_302_985_728)},
	}, nil
}

func (MockProvider) HistoricalLight(_ context.Context, symbol string, _, _ *string) ([]HistoricalLightPoint, error) {
	return []HistoricalLightPoint{{Symbol: strings.ToUpper(symbol), Date: "2025-07-24", Price: 118_741.16, Volume: f64(75_302_985_728)}}, nil
}

func (MockProvider) HistoricalFull(_ context.Context, symbol string, _, _ *string) ([]HistoricalFullPoint, error) {
	return []HistoricalFullPoint{{
		Symbol: strings.ToUpper(symbol), Date: "2025-07-24", Open: 118_779.09, High: 119_535.45, Low: 117_435.22,
		Close: 118_741.16, Volume: f64(75_302_985_728), Change: f64(-37.93), ChangePercent: f64(-0.03193323), Vwap: f64(118_570.61),
	}}, nil
}

func (MockProvider) Intraday(context.Context, string, string, *string, *string) ([]HistoricalPoint, error) {
	return []HistoricalPoint{{
		Date: "2025-07-24 12:00:00", Open: f64(118_779.09), Low: f64(117_435.22), High: f64(119_535.45),
		Close: 118_741.16, Volume: f64(75_302_985_728),
	}}, nil
}

func (MockProvider) News(_ context.Context, symbol *string, _, _ *int, _, _ *string) ([]NewsItem, error) {
	all := []NewsItem{
		{
			Symbol: str("BTCUSD"), PublishedDate: str("2026-04-04 12:00:00"),
			Title: str("Bitcoin Surges Past $118k as Institutional Adoption Grows"), Image: str("https://example.com/btc.jpg"),
			Site: str("CryptoNews"), Publisher: str("CryptoNews"),
			Text: str("The leading cryptocurrency continues its bull run as more companies add it to their balance sheet."),
			URL:  str("https://example.com/1"),
		},
		{
			Symbol: str("ETHUSD"), PublishedDate: str("2026-04-04 11:30:00"),
			Title: str("Ethereum Upgrade Successfully Completed on Testnet"), Image: str("https://example.com/eth.jpg"),
			Site: str("TheBlock"), Publisher: str("TheBlock"),
			Text: str("The upcoming hard fork aims to improve scalability and reduce transaction fees."),
			URL:  str("https://example.com/2"),
		},
	}
	if symbol == nil {
		return all, nil
	}
	out := []NewsItem{}
	for _, n := range all {
		// Swift compared uppercased() values; EqualFold differs on some
		// Unicode case mappings (ß, final sigma), so keep the exact form.
		if n.Symbol != nil && strings.ToUpper(*n.Symbol) == strings.ToUpper(*symbol) { //nolint:gocritic // Swift uppercased() parity.
			out = append(out, n)
		}
	}
	return out, nil
}

// --- Disabled (DisabledCryptoDataProvider) ---

// DisabledProvider answers every call with a 503.
type DisabledProvider struct{}

func errDisabled() error {
	return httpx.Abort(http.StatusServiceUnavailable, "Crypto market data is not configured.")
}

func (DisabledProvider) CryptocurrencyList(context.Context) ([]AssetResponse, error) {
	return nil, errDisabled()
}

func (DisabledProvider) Quote(context.Context, string) ([]QuoteResponse, error) {
	return nil, errDisabled()
}

func (DisabledProvider) QuoteShort(context.Context, string) ([]QuoteShortResponse, error) {
	return nil, errDisabled()
}

func (DisabledProvider) BatchQuotes(context.Context, bool) ([]QuoteShortResponse, error) {
	return nil, errDisabled()
}

func (DisabledProvider) HistoricalLight(context.Context, string, *string, *string) ([]HistoricalLightPoint, error) {
	return nil, errDisabled()
}

func (DisabledProvider) HistoricalFull(context.Context, string, *string, *string) ([]HistoricalFullPoint, error) {
	return nil, errDisabled()
}

func (DisabledProvider) Intraday(context.Context, string, string, *string, *string) ([]HistoricalPoint, error) {
	return nil, errDisabled()
}

func (DisabledProvider) News(context.Context, *string, *int, *int, *string, *string) ([]NewsItem, error) {
	return nil, errDisabled()
}
