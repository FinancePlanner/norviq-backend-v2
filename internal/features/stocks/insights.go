package stocks

import (
	"context"
	"math"
	"strconv"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/stocksdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// StockInsightsResponse and friends are StockDTO.swift's insight DTOs.
type StockInsightsResponse struct {
	GeneratedAt         string                 `json:"generatedAt"`
	Symbol              string                 `json:"symbol"`
	Profile             StockInsightProfile    `json:"profile"`
	Peers               []StockInsightPeer     `json:"peers"`
	ProjectionScenarios []StockInsightScenario `json:"projectionScenarios"`
}

type StockInsightProfile struct {
	Symbol            string             `json:"symbol"`
	CompanyName       string             `json:"companyName"`
	CurrentPrice      float64            `json:"currentPrice"`
	MarketCap         float64            `json:"marketCap"`
	SharesOutstanding float64            `json:"sharesOutstanding"`
	Metrics           map[string]float64 `json:"metrics"`
	DCFBasePrice      *float64           `json:"dcfBasePrice,omitempty"`
	DCFBearPrice      *float64           `json:"dcfBearPrice,omitempty"`
	DCFBullPrice      *float64           `json:"dcfBullPrice,omitempty"`
}

type StockInsightPeer struct {
	Symbol            string  `json:"symbol"`
	CompanyName       string  `json:"companyName"`
	CurrentPrice      float64 `json:"currentPrice"`
	MarketCap         float64 `json:"marketCap"`
	SharesOutstanding float64 `json:"sharesOutstanding"`
}

type StockInsightScenario struct {
	Kind  string                   `json:"kind"`
	Years []StockInsightProjection `json:"years"`
}

type StockInsightProjection struct {
	Year            int      `json:"year"`
	Revenue         float64  `json:"revenue"`
	RevenueGrowth   float64  `json:"revenueGrowth"`
	NetIncome       float64  `json:"netIncome"`
	NetIncomeGrowth float64  `json:"netIncomeGrowth"`
	NetMargin       float64  `json:"netMargin"`
	EPS             float64  `json:"eps"`
	PELowEstimate   float64  `json:"peLowEstimate"`
	PEHighEstimate  float64  `json:"peHighEstimate"`
	SharePriceLow   float64  `json:"sharePriceLow"`
	SharePriceHigh  float64  `json:"sharePriceHigh"`
	CAGRLow         *float64 `json:"cagrLow,omitempty"`
	CAGRHigh        *float64 `json:"cagrHigh,omitempty"`
}

// GET /v1/stocks/symbol/:symbol/insights
func (h *handlers) getStockInsights(c *echo.Context) error {
	userID, symbol, err := h.premiumThenSymbol(c, entitlements.FeatureAdvancedResearch)
	if err != nil {
		return err
	}
	res, err := h.insights(c, symbol, userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

// GET /v1/stocks/:symbol/insights. Vapor's trie router never backtracks: for
// GET, "symbol" and "id" are constants at this depth, so
// /v1/stocks/symbol/insights dead-ends (404) instead of reaching this route.
// ("/v1/stocks/id/insights" is GET /v1/stocks/id/:stockId on both.)
func (h *handlers) getStockInsightsLegacy(c *echo.Context) error {
	if c.Param("symbol") == "symbol" {
		return httpx.NotFound("")
	}
	return h.getStockInsights(c)
}

func queryDouble(c *echo.Context, name string) *float64 {
	raw := httpx.QueryString(c, name)
	if raw == nil {
		return nil
	}
	v, err := strconv.ParseFloat(*raw, 64)
	if err != nil {
		return nil
	}
	return &v
}

// insights is StockServiceImpl.getInsights. The DCF overrides
// (wacc, terminalGrowthRate, ...) are read off this request's query, for the
// primary symbol and every peer, as analysis(symbol:on: req) did.
func (h *handlers) insights(c *echo.Context, symbol string, userID uuid.UUID) (StockInsightsResponse, error) {
	ctx := c.Request().Context()
	normalized, err := validateSymbol(symbol)
	if err != nil {
		return StockInsightsResponse{}, err
	}
	params := marketdata.AnalysisParams{
		WACC:                queryDouble(c, "wacc"),
		TerminalGrowthRate:  queryDouble(c, "terminalGrowthRate"),
		TerminalMargin:      queryDouble(c, "terminalMargin"),
		FCFMarginAssumption: queryDouble(c, "fcfMarginAssumption"),
	}
	primary, err := h.market.Analysis(ctx, normalized, params)
	if err != nil {
		return StockInsightsResponse{}, err
	}
	var profile *marketdata.CompanyProfileResponse
	if p, err := h.market.Profile(ctx, normalized); err == nil {
		profile = &p
	}
	currentPrice := math.Max(0, deref(primary.CurrentPrice, 0))
	marketCap := math.Max(0, firstOf(primary.MarketCap, profileMarketCap(profile)))
	shares := math.Max(0, firstOf(primary.SharesOutstanding, profileShares(profile)))
	scenarios := makeProjectionScenarios(primary, currentPrice, marketCap, shares, time.Now())

	peerSymbols, err := h.prioritizedPeerSymbols(ctx, userID, normalized)
	if err != nil {
		return StockInsightsResponse{}, err
	}
	peers := make([]StockInsightPeer, 0, len(peerSymbols))
	for _, ps := range peerSymbols {
		var metrics *marketdata.StockAnalysisMetricsResponse
		if m, err := h.market.Analysis(ctx, ps, params); err == nil {
			metrics = &m
		}
		var pp *marketdata.CompanyProfileResponse
		if p, err := h.market.Profile(ctx, ps); err == nil {
			pp = &p
		}
		var price, mcap, so *float64
		if metrics != nil {
			price, mcap, so = metrics.CurrentPrice, metrics.MarketCap, metrics.SharesOutstanding
		}
		peers = append(peers, StockInsightPeer{
			Symbol:            ps,
			CompanyName:       resolvedCompanyName(profileName(pp), ps),
			CurrentPrice:      math.Max(0, deref(price, 0)),
			MarketCap:         math.Max(0, firstOf(mcap, profileMarketCap(pp))),
			SharesOutstanding: math.Max(0, firstOf(so, profileShares(pp))),
		})
	}
	return StockInsightsResponse{
		GeneratedAt: portfolios.FormatISODateTime(time.Now()),
		Symbol:      normalized,
		Profile: StockInsightProfile{
			Symbol:            normalized,
			CompanyName:       resolvedCompanyName(profileName(profile), normalized),
			CurrentPrice:      currentPrice,
			MarketCap:         marketCap,
			SharesOutstanding: shares,
			Metrics:           comparisonMetrics(primary),
			DCFBasePrice:      primary.DCFBasePrice,
			DCFBearPrice:      primary.DCFBearPrice,
			DCFBullPrice:      primary.DCFBullPrice,
		},
		Peers:               peers,
		ProjectionScenarios: scenarios,
	}, nil
}

func deref(p *float64, d float64) float64 {
	if p != nil {
		return *p
	}
	return d
}

// firstOf is `a ?? b ?? 0`.
func firstOf(a, b *float64) float64 {
	if a != nil {
		return *a
	}
	return deref(b, 0)
}

func profileMarketCap(p *marketdata.CompanyProfileResponse) *float64 {
	if p == nil {
		return nil
	}
	return p.MarketCapitalization
}

func profileShares(p *marketdata.CompanyProfileResponse) *float64 {
	if p == nil {
		return nil
	}
	return p.ShareOutstanding
}

func profileName(p *marketdata.CompanyProfileResponse) *string {
	if p == nil {
		return nil
	}
	return p.Name
}

func resolvedCompanyName(v *string, symbol string) string {
	if v == nil {
		return symbol
	}
	t := trim(*v)
	if t == "" {
		return symbol
	}
	return t
}

// prioritizedPeerSymbols: watchlist symbols then holdings, each most
// recently updated first, uppercased, without the primary, de-duplicated,
// at most eight.
func (h *handlers) prioritizedPeerSymbols(ctx context.Context, userID uuid.UUID, primary string) ([]string, error) {
	q := stocksdb.New(h.db)
	watch, err := q.WatchlistSymbolsByRecency(ctx, userID)
	if err != nil {
		return nil, err
	}
	held, err := q.StockSymbolsByRecency(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range append(watch, held...) {
		u := swiftUpper(s)
		if u == primary || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
		if len(out) == 8 {
			break
		}
	}
	return out, nil
}

// comparisonMetrics is comparisonMetricsMap: nil and non-finite values dropped.
func comparisonMetrics(m marketdata.StockAnalysisMetricsResponse) map[string]float64 {
	all := map[string]*float64{
		"ttmPE":                                     m.TtmPE,
		"forwardPE":                                 m.ForwardPE,
		"twoYearForwardPE":                          m.TwoYearForwardPE,
		"ttmEPSGrowth":                              m.TtmEPSGrowth,
		"currentYearExpectedEPSGrowth":              m.CurrentYearExpectedEPSGrowth,
		"nextYearEPSGrowth":                         m.NextYearEPSGrowth,
		"ttmRevenueGrowth":                          m.TtmRevenueGrowth,
		"currentYearExpectedRevenueGrowth":          m.CurrentYearExpectedRevenueGrowth,
		"nextYearRevenueGrowth":                     m.NextYearRevenueGrowth,
		"grossMargin":                               m.GrossMargin,
		"netMargin":                                 m.NetMargin,
		"ttmPEGRatio":                               m.TtmPEGRatio,
		"lastYearEPSGrowth":                         m.LastYearEPSGrowth,
		"ttmVsNTMEPSGrowth":                         m.TtmVsNTMEPSGrowth,
		"currentQuarterEPSGrowthVsPreviousYear":     m.CurrentQuarterEPSGrowthVsPreviousYear,
		"twoYearStackExpectedEPSGrowth":             m.TwoYearStackExpectedEPSGrowth,
		"lastYearRevenueGrowth":                     m.LastYearRevenueGrowth,
		"ttmVsNTMRevenueGrowth":                     m.TtmVsNTMRevenueGrowth,
		"currentQuarterRevenueGrowthVsPreviousYear": m.CurrentQuarterRevenueGrowthVsPreviousYear,
		"twoYearStackExpectedRevenueGrowth":         m.TwoYearStackExpectedRevenueGrowth,
		"dcfFairValue":                              m.DCFBasePrice,
	}
	out := map[string]float64{}
	for k, v := range all {
		if v != nil && !math.IsInf(*v, 0) && !math.IsNaN(*v) {
			out[k] = *v
		}
	}
	return out
}

// makeProjectionScenarios is StockServiceImpl.makeProjectionScenarios.
func makeProjectionScenarios(m marketdata.StockAnalysisMetricsResponse, fallbackPrice, fallbackCap, fallbackShares float64, now time.Time) []StockInsightScenario {
	base := m.YearlyProjections
	if len(base) == 0 || m.CurrentPrice == nil || *m.CurrentPrice <= 0 || m.MarketCap == nil || *m.MarketCap <= 0 ||
		m.SharesOutstanding == nil || *m.SharesOutstanding <= 0 {
		return fallbackProjectionScenarios(fallbackPrice, fallbackCap, fallbackShares, now)
	}
	currentPrice, sharesOutstanding := *m.CurrentPrice, *m.SharesOutstanding
	baseYear := base[0].Year - 1
	if m.BaseYear != nil {
		baseYear = *m.BaseYear
	}
	first := base[0]
	revenueDenominator := math.Max(1.0+first.RevenueGrowth, 0.1)
	incomeDenominator := math.Max(1.0+first.NetIncomeGrowth, 0.1)
	trailingRevenue := first.Revenue / revenueDenominator
	trailingNetIncome := first.NetIncome / incomeDenominator

	peLowBase := math.Max(deref(m.ForwardPE, 20)*0.9, 8)
	peHighBase := math.Max(deref(m.TtmPE, peLowBase)*1.05, peLowBase+1)
	terminalGrowthRate := deref(m.TerminalGrowthRate, 0.025)
	terminalMargin := math.Max(deref(m.TerminalMargin, 0.22), 0.08)
	baseNetMargin := math.Max(deref(m.NetMargin, first.NetMargin), 0.05)

	configs := []struct {
		kind                                 string
		growthShift, peLowShift, peHighShift float64
	}{
		{"bear", -0.03, -2, -2},
		{"base", 0, 0, 0},
		{"bull", 0.03, 2, 2},
	}
	out := make([]StockInsightScenario, 0, 3)
	for _, cfg := range configs {
		years := make([]StockInsightProjection, 0, len(base)+1)
		trailingEPS := trailingNetIncome / sharesOutstanding
		years = append(years, StockInsightProjection{
			Year: baseYear, Revenue: trailingRevenue, RevenueGrowth: deref(m.TtmRevenueGrowth, 0),
			NetIncome: trailingNetIncome, NetIncomeGrowth: deref(m.TtmEPSGrowth, 0), NetMargin: baseNetMargin,
			EPS: trailingEPS, PELowEstimate: peLowBase, PEHighEstimate: peHighBase,
			SharePriceLow: trailingEPS * peLowBase, SharePriceHigh: trailingEPS * peHighBase,
		})
		runningRevenue, runningNetIncome := trailingRevenue, trailingNetIncome
		for i, p := range base {
			revenueGrowth := math.Max(p.RevenueGrowth+cfg.growthShift, terminalGrowthRate)
			netIncomeGrowth := math.Max(p.NetIncomeGrowth+cfg.growthShift, terminalGrowthRate)
			runningRevenue *= 1 + revenueGrowth
			runningNetIncome *= 1 + netIncomeGrowth
			margin := math.Min(math.Max(baseNetMargin+float64(i+1)*0.01, 0.03), math.Max(terminalMargin, baseNetMargin))
			actualNetIncome := runningRevenue * margin
			eps := actualNetIncome / sharesOutstanding
			peLow := math.Max(8, peLowBase+cfg.peLowShift)
			peHigh := math.Max(peLow+1, peHighBase+cfg.peHighShift)
			priceLow, priceHigh := eps*peLow, eps*peHigh
			yearsForward := float64(i + 1)
			cagrLow := math.Pow(priceLow/currentPrice, 1.0/yearsForward) - 1
			cagrHigh := math.Pow(priceHigh/currentPrice, 1.0/yearsForward) - 1
			years = append(years, StockInsightProjection{
				Year: p.Year, Revenue: runningRevenue, RevenueGrowth: revenueGrowth,
				NetIncome: actualNetIncome, NetIncomeGrowth: netIncomeGrowth, NetMargin: margin,
				EPS: eps, PELowEstimate: peLow, PEHighEstimate: peHigh,
				SharePriceLow: priceLow, SharePriceHigh: priceHigh, CAGRLow: &cagrLow, CAGRHigh: &cagrHigh,
			})
		}
		_ = runningNetIncome
		out = append(out, StockInsightScenario{Kind: cfg.kind, Years: years})
	}
	return out
}

// fallbackProjectionScenarios is StockServiceImpl.fallbackProjectionScenarios.
// The start year is the server's local calendar year, as Calendar's was.
func fallbackProjectionScenarios(currentPrice, marketCap, sharesOutstanding float64, now time.Time) []StockInsightScenario {
	basePrice := math.Max(currentPrice, 1)
	baseShares := math.Max(sharesOutstanding, 1)
	baseRevenue := math.Max(marketCap*0.22, 1_000_000)
	baseNetIncome := baseRevenue * 0.12
	startYear := now.Year()
	configs := []struct {
		kind                  string
		growth, peLow, peHigh float64
	}{
		{"bear", 0.03, 12, 15},
		{"base", 0.07, 16, 20},
		{"bull", 0.11, 20, 25},
	}
	out := make([]StockInsightScenario, 0, 3)
	for _, s := range configs {
		years := make([]StockInsightProjection, 0, 4)
		revenue, netIncome := baseRevenue, baseNetIncome
		for offset := 1; offset <= 4; offset++ {
			revenue *= 1 + s.growth
			netIncome *= 1 + s.growth + 0.01
			margin := math.Min(math.Max(netIncome/revenue, 0.06), 0.35)
			eps := netIncome / baseShares
			low, high := eps*s.peLow, eps*s.peHigh
			yearsForward := float64(offset)
			cagrLow := math.Pow(low/basePrice, 1.0/yearsForward) - 1
			cagrHigh := math.Pow(high/basePrice, 1.0/yearsForward) - 1
			years = append(years, StockInsightProjection{
				Year: startYear + offset, Revenue: revenue, RevenueGrowth: s.growth,
				NetIncome: netIncome, NetIncomeGrowth: s.growth + 0.01, NetMargin: margin,
				EPS: eps, PELowEstimate: s.peLow, PEHighEstimate: s.peHigh,
				SharePriceLow: low, SharePriceHigh: high, CAGRLow: &cagrLow, CAGRHigh: &cagrHigh,
			})
		}
		out = append(out, StockInsightScenario{Kind: s.kind, Years: years})
	}
	return out
}
