package marketdata

import (
	"strconv"
	"strings"
)

// Settings are the environment variables configure.swift and the market
// services read. Integers are read leniently, as Swift's `Int.init` did: an
// unparsable value falls back to the default instead of failing boot.
type Settings struct {
	MarketProvider  string `env:"MARKET_PROVIDER"`
	FinnhubAPIKey   string `env:"FINNHUB_API_KEY"`
	FMPAPIKey       string `env:"FMP_API_KEY"`
	IBKRAPIBaseURL  string `env:"IBKR_API_BASE_URL"`
	FMPSymbolTier   string `env:"FMP_SYMBOL_ACCESS_TIER"`
	DefaultCurrency string `env:"MARKET_DEFAULT_CURRENCY"`

	TTLQuote           string `env:"MARKET_TTL_QUOTE_SECONDS"`
	TTLHistory         string `env:"MARKET_TTL_HISTORY_SECONDS"`
	TTLSearch          string `env:"MARKET_TTL_SEARCH_SECONDS"`
	TTLFx              string `env:"MARKET_TTL_FX_SECONDS"`
	TTLProfile         string `env:"MARKET_TTL_PROFILE_SECONDS"`
	TTLBasicFinancials string `env:"MARKET_TTL_BASIC_FINANCIALS_SECONDS"`
	TTLFMP             string `env:"MARKET_TTL_FMP_SECONDS"`
	TTLOwnership       string `env:"MARKET_TTL_OWNERSHIP_SECONDS"`
	TTLRecentEarnings  string `env:"MARKET_TTL_RECENT_EARNINGS_SECONDS"`
	TTLEOD             string `env:"MARKET_TTL_EOD_SECONDS"`
	TTLTechnicals      string `env:"MARKET_TTL_TECHNICALS_SECONDS"`
	OverviewTTL        string `env:"MARKET_OVERVIEW_TTL_SECONDS"`
	OverviewUniverse   string `env:"MARKET_OVERVIEW_UNIVERSE_TTL_SECONDS"`
	PressureTTL        string `env:"MARKET_PRESSURE_TTL_SECONDS"`

	// Base URLs are not configurable in Vapor (they were constructor
	// defaults). v2 reads them so tests and parity runs can point the
	// providers at stub servers; production leaves them unset.
	FinnhubBaseURL string `env:"FINNHUB_BASE_URL"`
	FMPBaseURL     string `env:"FMP_BASE_URL"`
}

// envInt is `Environment.get(name).flatMap(Int.init(_:)) ?? def`.
func envInt(raw string, def int) int {
	if v, err := strconv.Atoi(raw); err == nil {
		return v
	}
	return def
}

// CacheConfig is MarketDataCacheConfig plus the per-feature TTLs the Swift
// extensions read from the environment.
type CacheConfig struct {
	QuoteTTL           int
	HistoryTTL         int
	SearchTTL          int
	FxTTL              int
	ProfileTTL         int
	BasicFinancialsTTL int
	FMPTTL             int
	OwnershipTTL       int
	RecentEarningsTTL  int
	DefaultCurrency    string

	OverviewTTL         int
	OverviewUniverseTTL int
	PressureTTL         int
	TechnicalsTTL       int
	EODTTL              int
}

// CacheConfig is MarketDataCacheConfig.fromEnvironment().
func (s Settings) CacheConfig() CacheConfig {
	currency := s.DefaultCurrency
	if currency == "" {
		currency = "USD"
	}
	return CacheConfig{
		QuoteTTL:           max(1, envInt(s.TTLQuote, 20)),
		HistoryTTL:         max(60, envInt(s.TTLHistory, 86400)),
		SearchTTL:          max(60, envInt(s.TTLSearch, 3600)),
		FxTTL:              max(60, envInt(s.TTLFx, 86400)),
		ProfileTTL:         max(60, envInt(s.TTLProfile, 86400)),
		BasicFinancialsTTL: max(60, envInt(s.TTLBasicFinancials, 86400)),
		FMPTTL:             max(60, envInt(s.TTLFMP, 86400)),
		OwnershipTTL:       max(60, envInt(s.TTLOwnership, 21600)),
		RecentEarningsTTL:  max(60, envInt(s.TTLRecentEarnings, 3600)),
		DefaultCurrency:    strings.ToUpper(currency),

		OverviewTTL:         envInt(s.OverviewTTL, 60),
		OverviewUniverseTTL: envInt(s.OverviewUniverse, 3600),
		PressureTTL:         envInt(s.PressureTTL, 300),
		TechnicalsTTL:       max(60, envInt(s.TTLTechnicals, 3600)),
		EODTTL:              max(60, envInt(s.TTLEOD, 3600)),
	}
}

// providerCurrency is the providers' own `MARKET_DEFAULT_CURRENCY ?? "USD"`,
// which, unlike CacheConfig's, is not uppercased.
func (s Settings) providerCurrency() string {
	if s.DefaultCurrency == "" {
		return "USD"
	}
	return s.DefaultCurrency
}

// FMPAccessTier is FMP_SYMBOL_ACCESS_TIER: free (default), starter, premium.
type FMPAccessTier string

// Tiers.
const (
	TierFree    FMPAccessTier = "free"
	TierStarter FMPAccessTier = "starter"
	TierPremium FMPAccessTier = "premium"
)

// ParseTier is FMPAccessTier.fromEnvironment().
func ParseTier(raw string) FMPAccessTier {
	switch t := FMPAccessTier(strings.ToLower(strings.TrimSpace(raw))); t {
	case TierStarter, TierPremium, TierFree:
		return t
	}
	return TierFree
}

// AllowsAllSymbols is false on the free tier.
func (t FMPAccessTier) AllowsAllSymbols() bool { return t != TierFree }

var freeTierSymbols = func() map[string]bool {
	out := map[string]bool{}
	for _, s := range strings.Fields(`AAPL TSLA AMZN MSFT NVDA GOOGL META NFLX JPM V
		BAC PYPL DIS T PFE COST INTC KO TGT NKE SPY BA BABA XOM WMT GE CSCO VZ JNJ CVX
		PLTR SQ SHOP SBUX SOFI HOOD RBLX SNAP AMD UBER FDX ABBV ETSY MRNA LMT GM F LCID CCL DAL
		UAL AAL TSM SONY ET MRO COIN RIVN RIOT CPRX VWO SPYG NOK ROKU VIAC ATVI BIDU DOCU ZM PINS
		TLRY WBA MGM NIO C GS WFC ADBE PEP UNH CARR HCA TWTR BILI SIRI FUBO RKT`) {
		out[s] = true
	}
	return out
}()

// IsSupportedOnFreeTier is FMPSymbolPlanAccess.isSupportedOnFreeTier.
func IsSupportedOnFreeTier(symbol string) bool { return freeTierSymbols[strings.ToUpper(symbol)] }
