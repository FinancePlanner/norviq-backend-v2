package marketdata

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// AnalysisParams are the DCF query overrides analysis read off the request
// (`wacc`, `terminalGrowthRate`, `terminalMargin`, `fcfMarginAssumption`).
type AnalysisParams struct {
	WACC                *float64
	TerminalGrowthRate  *float64
	TerminalMargin      *float64
	FCFMarginAssumption *float64
}

func orDefault(v *float64, d float64) float64 {
	if v != nil {
		return *v
	}
	return d
}

func firstNonNil(values ...*float64) *float64 {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func metricDouble(key string, r BasicFinancialsResponse) *float64 {
	if v, ok := r.Metric[key]; ok {
		if n, isNum := v.Number(); isNum {
			return &n
		}
	}
	return nil
}

func metricPercent(key string, r BasicFinancialsResponse) *float64 {
	if v := metricDouble(key, r); v != nil {
		return ptr(*v / 100)
	}
	return nil
}

func latestSeriesValue(freq, metric string, r BasicFinancialsResponse) *float64 {
	if points := r.Series[freq][metric]; len(points) > 0 {
		return ptr(points[0].Value)
	}
	return nil
}

func impliedForwardGrowth(ttmPE, forwardPE *float64) *float64 {
	if ttmPE == nil || forwardPE == nil || *ttmPE <= 0 || *forwardPE <= 0 {
		return nil
	}
	return ptr(*ttmPE / *forwardPE - 1)
}

func twoYearForwardPE(forwardPE, nextYearEPSGrowth *float64) *float64 {
	if forwardPE == nil || nextYearEPSGrowth == nil || *forwardPE <= 0 || *nextYearEPSGrowth <= -1 {
		return nil
	}
	return ptr(*forwardPE / (1 + *nextYearEPSGrowth))
}

func stackedGrowth(first, second *float64) *float64 {
	if first == nil || second == nil {
		return nil
	}
	return ptr((1+*first)*(1+*second) - 1)
}

func delta(lhs, rhs *float64) *float64 {
	if lhs == nil || rhs == nil {
		return nil
	}
	return ptr(*lhs - *rhs)
}

// CalculateDCFPrice is calculateDCFPrice.
func CalculateDCFPrice(projections []YearlyProjectionResponse, sharesOutstanding *float64, wacc, terminalGrowthRate, netDebt float64) *float64 {
	if sharesOutstanding == nil || *sharesOutstanding <= 0 || len(projections) == 0 {
		return nil
	}
	explicit := 0.0
	for i, p := range projections {
		explicit += orDefault(p.FCF, 0) / math.Pow(1+wacc, float64(i+1))
	}
	finalFCF := orDefault(projections[len(projections)-1].FCF, 0)
	terminal := finalFCF * (1 + terminalGrowthRate) / (wacc - terminalGrowthRate)
	return ptr((explicit + terminal/math.Pow(1+wacc, float64(len(projections))) - netDebt) / *sharesOutstanding)
}

// Analysis is analysis(symbol:): valuation metrics plus a five-year DCF.
func (s *Service) Analysis(ctx context.Context, rawSymbol string, params AnalysisParams) (StockAnalysisMetricsResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return StockAnalysisMetricsResponse{}, err
	}
	if err := s.ValidateFMPSymbolAccess(symbol, "analysis"); err != nil {
		return StockAnalysisMetricsResponse{}, err
	}
	basic, err := s.BasicFinancials(ctx, symbol)
	if err != nil {
		s.logger().Error("market.analysis basicFinancials failed", "symbol", symbol, "error", err)
		return StockAnalysisMetricsResponse{}, err
	}
	ratios, err := s.RatiosTTM(ctx, symbol)
	if err != nil {
		s.logger().Error("market.analysis ratiosTTM failed", "symbol", symbol, "error", err)
		return StockAnalysisMetricsResponse{}, err
	}
	growth, err := s.FinancialGrowth(ctx, symbol, ptr(5), ptr("FY"))
	if err != nil {
		s.logger().Error("market.analysis financialGrowth failed", "symbol", symbol, "error", err)
		return StockAnalysisMetricsResponse{}, err
	}

	var latestRatio *RatiosTTMResponse
	if len(ratios) > 0 {
		latestRatio = &ratios[0]
	}
	sortedGrowth := append([]FinancialGrowthResponse(nil), growth...)
	sort.SliceStable(sortedGrowth, func(i, j int) bool { return sortedGrowth[i].Date > sortedGrowth[j].Date })
	var latestGrowth, priorGrowth *FinancialGrowthResponse
	if len(sortedGrowth) > 0 {
		latestGrowth = &sortedGrowth[0]
	}
	if len(sortedGrowth) > 1 {
		priorGrowth = &sortedGrowth[1]
	}
	ratioField := func(f func(*RatiosTTMResponse) *float64) *float64 {
		if latestRatio == nil {
			return nil
		}
		return f(latestRatio)
	}
	growthField := func(g *FinancialGrowthResponse, f func(*FinancialGrowthResponse) *float64) *float64 {
		if g == nil {
			return nil
		}
		return f(g)
	}
	eps := func(g *FinancialGrowthResponse) *float64 { return g.Epsgrowth }
	rev := func(g *FinancialGrowthResponse) *float64 { return g.RevenueGrowth }

	ttmPE := firstNonNil(
		ratioField(func(r *RatiosTTMResponse) *float64 { return r.PriceToEarningsRatioTTM }),
		metricDouble("peTTM", basic), metricDouble("peBasicExclExtraTTM", basic),
		metricDouble("peExclExtraTTM", basic), metricDouble("peAnnual", basic))
	forwardPE := metricDouble("forwardPE", basic)
	ttmEPSGrowth := firstNonNil(metricPercent("epsGrowthTTMYoy", basic), growthField(latestGrowth, eps))
	currentYearEPSGrowth := firstNonNil(impliedForwardGrowth(ttmPE, forwardPE), growthField(latestGrowth, eps))
	nextYearEPSGrowth := firstNonNil(growthField(priorGrowth, eps), growthField(latestGrowth, eps))
	ttmRevenueGrowth := firstNonNil(metricPercent("revenueGrowthTTMYoy", basic), growthField(latestGrowth, rev))
	currentYearRevenueGrowth := firstNonNil(growthField(latestGrowth, rev), ttmRevenueGrowth)
	nextYearRevenueGrowth := firstNonNil(growthField(priorGrowth, rev), growthField(latestGrowth, rev))
	grossMargin := firstNonNil(
		ratioField(func(r *RatiosTTMResponse) *float64 { return r.GrossProfitMarginTTM }),
		metricPercent("grossMarginTTM", basic), metricPercent("grossMarginAnnual", basic),
		latestSeriesValue("quarterly", "grossMargin", basic), latestSeriesValue("annual", "grossMargin", basic))
	netMargin := firstNonNil(
		ratioField(func(r *RatiosTTMResponse) *float64 { return r.NetProfitMarginTTM }),
		metricPercent("netProfitMarginTTM", basic), metricPercent("netProfitMarginAnnual", basic),
		latestSeriesValue("quarterly", "netMargin", basic), latestSeriesValue("annual", "netMargin", basic))

	// DCF inputs, fetched concurrently and best-effort (Swift's `async let try?`).
	var (
		wg           sync.WaitGroup
		currentQuote *QuoteResponse
		profile      *CompanyProfileResponse
		balanceSheet *BalanceSheetStatementResponse
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		if q, err := s.Quote(ctx, symbol); err == nil {
			currentQuote = &q
		}
	}()
	go func() {
		defer wg.Done()
		if p, err := s.Profile(ctx, symbol); err == nil {
			profile = &p
		}
	}()
	go func() {
		defer wg.Done()
		if rows, err := s.BalanceSheetStatement(ctx, symbol, ptr(1), ptr("FY")); err == nil && len(rows) > 0 {
			balanceSheet = &rows[0]
		}
	}()
	wg.Wait()

	var currentPrice, sharesOutstanding, marketCap *float64
	if currentQuote != nil {
		currentPrice = ptr(currentQuote.CurrentPrice)
	}
	if profile != nil && profile.ShareOutstanding != nil {
		sharesOutstanding = ptr(*profile.ShareOutstanding * 1_000_000)
	}
	if currentPrice != nil && sharesOutstanding != nil {
		marketCap = ptr(*currentPrice * *sharesOutstanding)
	}
	netDebt := 0.0
	if balanceSheet != nil {
		netDebt = (orDefault(balanceSheet.ShortTermDebt, 0) + orDefault(balanceSheet.LongTermDebt, 0)) - orDefault(balanceSheet.CashAndCashEquivalents, 0)
	}

	ttmRevenue := 0.0
	if base := orDefault(metricDouble("revenueTTM", basic), 0); base > 0 {
		ttmRevenue = base * 1_000_000
	} else if marketCap != nil && ttmPE != nil && *ttmPE > 0 && netMargin != nil && *netMargin > 0 {
		ttmRevenue = (*marketCap / *ttmPE) / *netMargin
	}
	ttmNetIncome := ttmRevenue * orDefault(netMargin, 0.1)

	wacc := orDefault(params.WACC, 0.09)
	terminalGrowthRate := orDefault(params.TerminalGrowthRate, 0.025)
	terminalMargin := orDefault(params.TerminalMargin, 0.22)
	fcfMarginAssumption := orDefault(params.FCFMarginAssumption, 1.0)

	safeRevenueGrowth1 := orDefault(firstNonNil(currentYearRevenueGrowth, ttmRevenueGrowth), 0.1)
	safeRevenueGrowth2 := orDefault(nextYearRevenueGrowth, safeRevenueGrowth1)
	safeEPSGrowth1 := orDefault(firstNonNil(currentYearEPSGrowth, ttmEPSGrowth), 0.1)
	safeEPSGrowth2 := orDefault(nextYearEPSGrowth, safeEPSGrowth1)
	currentYear := s.now().Year()

	build := func(shift float64) []YearlyProjectionResponse {
		projections := make([]YearlyProjectionResponse, 0, 5)
		currentRev := ttmRevenue
		currentNetInc := ttmNetIncome
		for i := 1; i <= 5; i++ {
			var revGrowth, niGrowth float64
			switch i {
			case 1:
				revGrowth, niGrowth = safeRevenueGrowth1+shift, safeEPSGrowth1+shift
			case 2:
				revGrowth, niGrowth = safeRevenueGrowth2+shift, safeEPSGrowth2+shift
			default:
				last := projections[len(projections)-1]
				revGrowth = math.Max(last.RevenueGrowth-0.03, terminalGrowthRate)
				niGrowth = math.Max(last.NetIncomeGrowth-0.05, terminalGrowthRate)
			}
			currentRev *= 1 + revGrowth
			currentNetInc *= 1 + niGrowth
			baseMargin := orDefault(netMargin, 0.1)
			step := (terminalMargin - baseMargin) / 5.0
			target := baseMargin + step*float64(i)
			actualNetInc := currentRev * target
			fcf := actualNetInc * fcfMarginAssumption
			actualEPS := 0.0
			if sharesOutstanding != nil && *sharesOutstanding > 0 {
				actualEPS = actualNetInc / *sharesOutstanding
			}
			projections = append(projections, YearlyProjectionResponse{
				Year: currentYear + i, Revenue: currentRev, RevenueGrowth: revGrowth,
				NetIncome: actualNetInc, NetIncomeGrowth: niGrowth, NetMargin: target,
				EPS: actualEPS, FCF: ptr(fcf), FCFMargin: ptr(target * fcfMarginAssumption),
			})
		}
		_ = currentNetInc // tracked as in Swift; the projection uses the margin path
		return projections
	}

	resp := StockAnalysisMetricsResponse{
		Symbol:                                symbol,
		TtmPE:                                 ttmPE,
		ForwardPE:                             forwardPE,
		TwoYearForwardPE:                      twoYearForwardPE(forwardPE, nextYearEPSGrowth),
		TtmEPSGrowth:                          ttmEPSGrowth,
		CurrentYearExpectedEPSGrowth:          currentYearEPSGrowth,
		NextYearEPSGrowth:                     nextYearEPSGrowth,
		TtmRevenueGrowth:                      ttmRevenueGrowth,
		CurrentYearExpectedRevenueGrowth:      currentYearRevenueGrowth,
		NextYearRevenueGrowth:                 nextYearRevenueGrowth,
		GrossMargin:                           grossMargin,
		NetMargin:                             netMargin,
		TtmPEGRatio:                           ratioField(func(r *RatiosTTMResponse) *float64 { return r.PriceToEarningsGrowthRatioTTM }),
		LastYearEPSGrowth:                     growthField(priorGrowth, eps),
		TtmVsNTMEPSGrowth:                     delta(currentYearEPSGrowth, ttmEPSGrowth),
		CurrentQuarterEPSGrowthVsPreviousYear: metricPercent("epsGrowthQuarterlyYoy", basic),
		TwoYearStackExpectedEPSGrowth:         stackedGrowth(currentYearEPSGrowth, nextYearEPSGrowth),
		LastYearRevenueGrowth:                 growthField(priorGrowth, rev),
		TtmVsNTMRevenueGrowth:                 delta(currentYearRevenueGrowth, ttmRevenueGrowth),
		CurrentQuarterRevenueGrowthVsPreviousYear: metricPercent("revenueGrowthQuarterlyYoy", basic),
		TwoYearStackExpectedRevenueGrowth:         stackedGrowth(currentYearRevenueGrowth, nextYearRevenueGrowth),
		CurrentPrice:                              currentPrice,
		MarketCap:                                 marketCap,
		SharesOutstanding:                         sharesOutstanding,
		BaseYear:                                  ptr(currentYear),
		WACC:                                      ptr(wacc),
		TerminalGrowthRate:                        ptr(terminalGrowthRate),
		TerminalMargin:                            ptr(terminalMargin),
		NetDebt:                                   ptr(netDebt),
	}
	if ttmRevenue > 0 {
		base := build(0)
		resp.YearlyProjections = base
		resp.DCFBasePrice = CalculateDCFPrice(base, sharesOutstanding, wacc, terminalGrowthRate, netDebt)
		resp.DCFBearPrice = CalculateDCFPrice(build(-0.03), sharesOutstanding, wacc, terminalGrowthRate, netDebt)
		resp.DCFBullPrice = CalculateDCFPrice(build(0.03), sharesOutstanding, wacc, terminalGrowthRate, netDebt)
	}
	return resp, nil
}

// Compare is compare(symbols:): at most three distinct symbols, failures
// skipped. Swift took `Array(Set(symbols)).prefix(3)`, an arbitrary three;
// v2 keeps the first three distinct in request order.
func (s *Service) Compare(ctx context.Context, symbols []string, params AnalysisParams) []StockAnalysisMetricsResponse {
	unique := uniqued(symbols)
	if len(unique) > 3 {
		unique = unique[:3]
	}
	out := []StockAnalysisMetricsResponse{}
	for _, sym := range unique {
		if r, err := s.Analysis(ctx, sym, params); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// ---- Price chart ----------------------------------------------------------

var priceChartRanges = map[string]bool{"1H": true, "1D": true, "1W": true, "1M": true, "3M": true, "1Y": true, "5Y": true}

// PriceChart is priceChart(symbol:range:).
func (s *Service) PriceChart(ctx context.Context, rawSymbol, rawRange string) (PriceChartSeries, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return PriceChartSeries{}, err
	}
	rng := strings.ToUpper(strings.TrimSpace(rawRange))
	if !priceChartRanges[rng] {
		return PriceChartSeries{}, badRequest("Invalid range '" + rawRange + "'. Allowed: 1H, 1D, 1W, 1M, 3M, 1Y, 5Y.")
	}
	fmp, err := s.RequireFMP()
	if err != nil {
		return PriceChartSeries{}, err
	}
	now := s.now().UTC()
	var series PriceChartSeries
	switch rng {
	case "1H", "1D", "1W":
		interval, from := "1hour", now
		switch rng {
		case "1H":
			interval, from = "1min", now.Add(-time.Hour)
		case "1D":
			interval, from = "5min", now.AddDate(0, 0, -1)
		case "1W":
			interval, from = "1hour", now.AddDate(0, 0, -7)
		}
		var points []CryptoHistoricalPoint
		points, err = fmp.StockIntraday(ctx, interval, symbol, ptr(formatISODateTime(from)), ptr(formatISODateTime(now)))
		if err == nil {
			series = s.intradayChart(symbol, rng, points)
		}
	default:
		series, err = s.eodChart(ctx, fmp, symbol, rng, rng, now)
	}
	if err != nil && Status(err) == 402 {
		s.logger().Info("market.price-chart intraday fallback to EOD", "symbol", symbol, "range", rng)
		fallback := rng
		if rng == "1H" {
			fallback = "1D"
		}
		return s.eodChart(ctx, fmp, symbol, rng, fallback, now)
	}
	return series, err
}

func (s *Service) eodChart(ctx context.Context, fmp *FMP, symbol, rng, chartRange string, now time.Time) (PriceChartSeries, error) {
	var from time.Time
	switch chartRange {
	case "1H", "1D":
		from = now.AddDate(0, 0, -1)
	case "1W":
		from = now.AddDate(0, 0, -7)
	case "1M":
		from = addMonths(now, -1)
	case "3M":
		from = addMonths(now, -3)
	case "1Y":
		from = addYears(now, -1)
	default:
		from = addYears(now, -5)
	}
	points, err := fmp.StockHistoricalEOD(ctx, symbol, ptr(FormatDateOnly(from)), ptr(FormatDateOnly(now)))
	if err != nil {
		return PriceChartSeries{}, err
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].Date < points[j].Date })
	out := make([]PriceChartPoint, 0, len(points))
	for _, p := range points {
		out = append(out, PriceChartPoint{Date: p.Date, Close: p.Price, Volume: floatToIntPtr(p.Volume)})
	}
	return PriceChartSeries{Symbol: symbol, Currency: s.Config.DefaultCurrency, Range: rng, Points: out}, nil
}

func (s *Service) intradayChart(symbol, rng string, points []CryptoHistoricalPoint) PriceChartSeries {
	sort.SliceStable(points, func(i, j int) bool { return points[i].Date < points[j].Date })
	out := make([]PriceChartPoint, 0, len(points))
	for _, p := range points {
		out = append(out, PriceChartPoint{Date: p.Date, Close: p.Close, Open: p.Open, High: p.High, Low: p.Low, Volume: floatToIntPtr(p.Volume)})
	}
	return PriceChartSeries{Symbol: symbol, Currency: s.Config.DefaultCurrency, Range: rng, Points: out}
}

func floatToIntPtr(v *float64) *int {
	if v == nil {
		return nil
	}
	return ptr(int(*v))
}

// PriceChartComparison is priceChartComparison: one to five symbols, failed
// symbols skipped.
func (s *Service) PriceChartComparison(ctx context.Context, rawSymbols []string, rng string) (PriceChartComparisonResponse, error) {
	normalized := make([]string, 0, len(rawSymbols))
	for _, raw := range rawSymbols {
		sym, err := normalizeSymbol(raw)
		if err != nil {
			return PriceChartComparisonResponse{}, err
		}
		normalized = append(normalized, sym)
	}
	normalized = uniqued(normalized)
	if len(normalized) == 0 {
		return PriceChartComparisonResponse{}, badRequest("At least one symbol is required.")
	}
	if len(normalized) > 5 {
		return PriceChartComparisonResponse{}, badRequest("Maximum 5 symbols per comparison.")
	}
	series := []PriceChartSeries{}
	for _, sym := range normalized {
		one, err := s.PriceChart(ctx, sym, rng)
		if err != nil {
			s.logger().Warn("market.price-chart-compare skipped", "symbol", sym, "error", err)
			continue
		}
		series = append(series, one)
	}
	return PriceChartComparisonResponse{Series: series, Range: rng}, nil
}
