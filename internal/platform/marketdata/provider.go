package marketdata

import (
	"context"
	"strings"
	"time"
)

// Quote is MarketProviderQuote. Price maps to Finnhub's `c`.
type Quote struct {
	Symbol        string
	Price         float64
	Change        *float64
	PercentChange *float64
	High          *float64
	Low           *float64
	Open          *float64
	PreviousClose *float64
	Currency      string
	AsOf          time.Time
}

// PriceBar is MarketProviderPriceBar.
type PriceBar struct {
	Date   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume *int
}

// History is MarketProviderHistory.
type History struct {
	Symbol   string
	Currency string
	Bars     []PriceBar
}

// SearchResult is MarketProviderSearchResult.
type SearchResult struct {
	Symbol   string
	Name     string
	Exchange string
	Currency string
	Conid    string
}

// FxRate is MarketProviderFxRate.
type FxRate struct {
	Base  string
	Quote string
	Rate  float64
	AsOf  time.Time
}

// CompanyProfile is MarketProviderCompanyProfile.
type CompanyProfile struct {
	Symbol               string
	Country              *string
	Currency             *string
	EstimateCurrency     *string
	Exchange             *string
	FinnhubIndustry      *string
	IPO                  *string
	Logo                 *string
	MarketCapitalization *float64
	Name                 *string
	Phone                *string
	ShareOutstanding     *float64
	Ticker               *string
	Weburl               *string
}

// BasicFinancials is MarketProviderBasicFinancials.
type BasicFinancials struct {
	Symbol     string
	MetricType string
	Metric     map[string]MetricValue
	Series     map[string]map[string][]BasicFinancialSeriesPoint
}

// Provider is the MarketDataProvider protocol: the primary quote/history
// source (Finnhub, IBKR, or disabled).
type Provider interface {
	Name() string
	Quote(ctx context.Context, symbol string) (Quote, error)
	History(ctx context.Context, symbol string, from, to *time.Time) (History, error)
	Search(ctx context.Context, query string) ([]SearchResult, error)
	Fx(ctx context.Context, base, quote string) (FxRate, error)
	// Profile returns nil when the provider has no profile for the symbol.
	Profile(ctx context.Context, symbol string) (*CompanyProfile, error)
	// BasicFinancials returns nil when the provider has nothing.
	BasicFinancials(ctx context.Context, symbol string) (*BasicFinancials, error)
}

// Disabled is DisabledMarketDataProvider. Quote answers an all-zero quote
// (which the service treats as "no price"); everything else is a 503.
type Disabled struct{}

func (Disabled) Name() string { return "disabled" }

func (Disabled) Quote(_ context.Context, symbol string) (Quote, error) {
	return Quote{Symbol: symbol, Currency: "USD", AsOf: time.Now()}, nil
}

func (Disabled) History(context.Context, string, *time.Time, *time.Time) (History, error) {
	return History{}, DisabledError()
}

func (Disabled) Search(context.Context, string) ([]SearchResult, error) {
	return nil, DisabledError()
}

func (Disabled) Fx(context.Context, string, string) (FxRate, error) {
	return FxRate{}, DisabledError()
}

func (Disabled) Profile(context.Context, string) (*CompanyProfile, error) {
	return nil, DisabledError()
}

func (Disabled) BasicFinancials(context.Context, string) (*BasicFinancials, error) {
	return nil, DisabledError()
}

// ProviderKind is MarketDataProviderKind.
type ProviderKind string

// Kinds.
const (
	KindFinnhub  ProviderKind = "finnhub"
	KindIBKR     ProviderKind = "ibkr"
	KindDisabled ProviderKind = "disabled"
)

// SelectProvider is MarketDataProviderKind.select: MARKET_PROVIDER wins when
// its credentials exist; otherwise Finnhub, then IBKR, then disabled.
func SelectProvider(configured string, hasFinnhubKey, hasIBKRBaseURL bool) ProviderKind {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "finnhub":
		if hasFinnhubKey {
			return KindFinnhub
		}
		return KindDisabled
	case "ibkr":
		if hasIBKRBaseURL {
			return KindIBKR
		}
		return KindDisabled
	}
	switch {
	case hasFinnhubKey:
		return KindFinnhub
	case hasIBKRBaseURL:
		return KindIBKR
	}
	return KindDisabled
}

func normalizeProviderSymbol(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", badRequest("Symbol is required.")
	}
	return s, nil
}

func fallbackValue(raw *string, fallback string) string {
	if raw == nil {
		return fallback
	}
	if t := strings.TrimSpace(*raw); t != "" {
		return t
	}
	return fallback
}
