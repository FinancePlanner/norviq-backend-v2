// Package market ports MarketDataController (/v1/market/*). Market data
// itself lives in internal/platform/marketdata so the later News,
// MarketBrief, Macro and Crypto waves can share the providers and caches.
package market

import (
	"context"
	"hash/fnv"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/store/marketdatadb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Billing features the controller gated with usageCounterService.requirePremium.
const (
	PremiumMarketFundamentals = "market_fundamentals"
	PremiumPeerComparison     = "peer_comparison"
	PremiumEarningsText       = "earnings_text"
)

// PremiumGate is UsageCounterService.requirePremium: nil when userID's plan
// includes feature, otherwise the billing upgrade error to return as-is.
// The billing port provides it; adapt with
//
//	func(ctx context.Context, f string, id uuid.UUID) error {
//		return entitlements.RequirePremium(ctx, entitlements.Feature(f), id)
//	}
type PremiumGate func(ctx context.Context, feature string, userID uuid.UUID) error

// Feature mounts the market routes.
type Feature struct {
	// Service overrides the environment-built service (tests).
	Service *marketdata.Service
	// Premium gates the Pro-only routes. While it is nil those routes are
	// not registered, so the strangler keeps sending them to Vapor.
	Premium PremiumGate
}

// Name implements server.Feature.
func (Feature) Name() string { return "market" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	svc := f.Service
	if svc == nil {
		settings, err := config.Load[marketdata.Settings]()
		if err != nil {
			return err
		}
		svc = marketdata.NewFromSettings(settings, marketdatadb.New(d.DB), d.Redis, &http.Client{}, d.Logger)
	}
	h := &handlers{svc: svc, premium: f.Premium}

	// routes.swift wraps the controller in RateLimitMiddleware(100/60s)
	// outside its authenticator, so that bucket is per client IP; the
	// controller's own 120/60s limiter runs after auth and is per user.
	g := r.V1.Group("/market",
		d.Limiter.Limit(100, 60*time.Second, "ratelimit:market"),
		d.Auth.Scoped(), auth.Guard(),
		auth.RequireScope(auth.ScopeMarketRead),
		d.Limiter.Limit(120, 60*time.Second, "ratelimit:market"),
	)
	gated := func(path string, feature string, fn echo.HandlerFunc) {
		if h.premium == nil {
			return
		}
		g.GET(path, h.requirePremium(feature, fn))
	}

	// Registration order follows MarketDataController.boot. Unported routes
	// (news, chart-builder) stay with Vapor.
	g.GET("/overview", h.overview)
	g.GET("/pressure/:symbol", h.pressure)
	g.GET("/returns/batch", h.periodReturnsBatch)
	g.GET("/returns/:symbol", h.periodReturns)
	g.GET("/details", h.details)
	g.GET("/history", h.stockHistory)
	g.GET("/history/archive", h.archivedStockHistory)
	g.POST("/history/archive/sync", h.syncArchivedStockHistory)
	g.GET("/quote/batch", h.quoteBatch)
	g.GET("/quote/:symbol", h.quote)
	g.GET("/profile/:symbol", h.profile)
	gated("/basic-financials/:symbol", PremiumMarketFundamentals, h.basicFinancials)
	gated("/analysis/:symbol", PremiumMarketFundamentals, h.analysis)
	gated("/compare", PremiumPeerComparison, h.compare)
	gated("/cash-flow-statement/:symbol", PremiumMarketFundamentals, h.cashFlowStatement)
	gated("/income-statement/:symbol", PremiumMarketFundamentals, h.incomeStatement)
	gated("/balance-sheet-statement/:symbol", PremiumMarketFundamentals, h.balanceSheetStatement)
	gated("/ratios-ttm/:symbol", PremiumMarketFundamentals, h.ratiosTTM)
	gated("/grades-consensus/:symbol", PremiumMarketFundamentals, h.gradesConsensus)
	gated("/financial-growth/:symbol", PremiumMarketFundamentals, h.financialGrowth)
	g.GET("/earnings/:symbol/recent", h.recentEarnings)
	gated("/earnings/:symbol/transcript", PremiumEarningsText, h.earningsTranscript)
	gated("/earnings/:symbol", PremiumEarningsText, h.earnings)
	g.GET("/earnings-calendar", h.earningsCalendar)
	gated("/analyst-estimates/:symbol", PremiumMarketFundamentals, h.analystEstimates)
	gated("/ratios/:symbol", PremiumMarketFundamentals, h.ratios)
	gated("/historical-sector-performance", PremiumMarketFundamentals, h.historicalSectorPerformance)
	g.GET("/history/:symbol", h.history)
	g.GET("/technicals/:symbol", h.technicals)
	g.GET("/insider/:symbol", h.insiderActivity)
	g.GET("/congress/recent", h.recentCongressTrades)
	g.GET("/congress/:symbol", h.congressTrades)
	g.GET("/institutional/:symbol", h.institutionalOwnership)
	g.GET("/search", h.search)
	g.GET("/fx", h.fx)
	g.GET("/price-chart/compare", h.priceChartComparison)
	g.GET("/price-chart/:symbol", h.priceChart)
	return nil
}

type handlers struct {
	svc     *marketdata.Service
	premium PremiumGate
}

// requirePremium is the handler-level `req.auth.require` + requirePremium
// pair every gated Vapor handler opened with.
func (h *handlers) requirePremium(feature string, next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		s, err := auth.Require(c)
		if err != nil {
			return err
		}
		if err := h.premium(c.Request().Context(), feature, s.UserID); err != nil {
			return err
		}
		return next(c)
	}
}

// ---- request helpers ----------------------------------------------------------

// query is `req.query[String.self, at:]`: nil when the key is absent.
func query(c *echo.Context, name string) *string {
	values, ok := c.QueryParams()[name]
	if !ok || len(values) == 0 {
		return nil
	}
	return &values[0]
}

// queryInt is `req.query[Int.self, at:]`, which was `try?`: a malformed value
// reads as absent rather than failing the request.
func queryInt(c *echo.Context, name string) *int {
	raw := query(c, name)
	if raw == nil {
		return nil
	}
	v, err := strconv.Atoi(*raw)
	if err != nil {
		return nil
	}
	return &v
}

// queryDouble is `req.query[Double.self, at:]` (malformed reads as absent).
func queryDouble(c *echo.Context, name string) *float64 {
	raw := query(c, name)
	if raw == nil {
		return nil
	}
	v, err := strconv.ParseFloat(*raw, 64)
	if err != nil {
		return nil
	}
	return &v
}

// param is `req.parameters.get(_:)`, percent-decoded.
func param(c *echo.Context, name string) string {
	raw := c.Param(name)
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}

func requirePathSymbol(c *echo.Context) (string, error) {
	raw := strings.TrimSpace(param(c, "symbol"))
	if raw == "" {
		return "", httpx.BadRequest("Missing symbol.")
	}
	return strings.ToUpper(raw), nil
}

func requireSymbolQuery(c *echo.Context) (string, error) {
	raw := query(c, "symbol")
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return "", httpx.BadRequest("Missing query parameter `symbol`.")
	}
	return strings.TrimSpace(*raw), nil
}

// splitSymbols is the controllers' split(",") + trim + drop-empty.
func splitSymbols(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func ctx(c *echo.Context) context.Context { return c.Request().Context() }

// ---- handlers -------------------------------------------------------------------

func (h *handlers) overview(c *echo.Context) error {
	resp, err := h.svc.Overview(ctx(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) pressure(c *echo.Context) error {
	resp, err := h.svc.Pressure(ctx(c), param(c, "symbol"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) periodReturns(c *echo.Context) error {
	symbol := param(c, "symbol")
	if symbol == "" {
		return httpx.BadRequest("Missing symbol.")
	}
	resp, err := h.svc.PeriodReturns(ctx(c), symbol)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) periodReturnsBatch(c *echo.Context) error {
	raw := query(c, "symbols")
	if raw == nil {
		return httpx.BadRequest("Missing query parameter `symbols`.")
	}
	parsed := splitSymbols(*raw)
	if len(parsed) == 0 {
		return httpx.BadRequest("At least one symbol is required.")
	}
	if len(parsed) > 20 {
		return httpx.BadRequest("Period-returns batch supports at most 20 symbols.")
	}
	resp, err := h.svc.PeriodReturnsBatch(ctx(c), parsed)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) details(c *echo.Context) error {
	symbol, err := requireSymbolQuery(c)
	if err != nil {
		return err
	}
	q, err := h.svc.Quote(ctx(c), symbol)
	if err != nil {
		if marketdata.IsDisabled(err) || marketdata.IsQuoteUnavailable(err) {
			upper := strings.ToUpper(symbol)
			return httpx.OK(c, marketdata.StockDetailsResponse{Symbol: upper, Company: upper})
		}
		return err
	}
	return httpx.OK(c, marketdata.StockDetailsResponse{
		Symbol:        q.Symbol,
		Company:       h.resolveCompanyName(c, q.Symbol),
		LatestPrice:   q.CurrentPrice,
		ChangePercent: h.resolveChangePercent(c, q.Symbol, q.CurrentPrice),
	})
}

func (h *handlers) resolveCompanyName(c *echo.Context, symbol string) string {
	results, err := h.svc.Search(ctx(c), symbol)
	if err != nil {
		return symbol
	}
	for _, r := range results {
		if strings.EqualFold(r.Symbol, symbol) {
			return r.Name
		}
	}
	if len(results) > 0 {
		return results[0].Name
	}
	return symbol
}

func (h *handlers) resolveChangePercent(c *echo.Context, symbol string, latest float64) float64 {
	history, err := h.svc.History(ctx(c), symbol, nil, nil)
	if err != nil {
		return 0
	}
	bars := append([]marketdata.PriceBarResponse(nil), history.Bars...)
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	if len(bars) >= 2 {
		prev := bars[len(bars)-2].Close
		if prev == 0 {
			return 0
		}
		return (latest - prev) / prev
	}
	if len(bars) == 1 && bars[0].Open != 0 {
		return (bars[0].Close - bars[0].Open) / bars[0].Open
	}
	return 0
}

func makeStockHistory(resp marketdata.HistoryResponse) []marketdata.StockHistory {
	bars := append([]marketdata.PriceBarResponse(nil), resp.Bars...)
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date > bars[j].Date })
	out := make([]marketdata.StockHistory, 0, len(bars))
	for _, b := range bars {
		vol := 0
		if b.Volume != nil {
			vol = *b.Volume
		}
		out = append(out, marketdata.StockHistory{Date: b.Date, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: vol})
	}
	return out
}

func (h *handlers) stockHistory(c *echo.Context) error {
	symbol, err := requireSymbolQuery(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.History(ctx(c), symbol, nil, nil)
	if err != nil {
		if marketdata.IsDisabled(err) {
			return httpx.OK(c, []marketdata.StockHistory{})
		}
		return err
	}
	return httpx.OK(c, makeStockHistory(resp))
}

func (h *handlers) archivedStockHistory(c *echo.Context) error {
	symbol, err := requireSymbolQuery(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.ArchivedHistory(ctx(c), symbol, query(c, "from"), query(c, "to"))
	if err != nil {
		return err
	}
	return httpx.OK(c, makeStockHistory(resp))
}

func (h *handlers) syncArchivedStockHistory(c *echo.Context) error {
	symbol, err := requireSymbolQuery(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.RefreshHistory(ctx(c), symbol, query(c, "from"), query(c, "to"))
	if err != nil {
		return err
	}
	return httpx.OK(c, makeStockHistory(resp))
}

func (h *handlers) quoteBatch(c *echo.Context) error {
	raw := query(c, "symbols")
	if raw == nil {
		return httpx.BadRequest("Missing query parameter `symbols`.")
	}
	parsed := splitSymbols(*raw)
	if len(parsed) == 0 {
		return httpx.BadRequest("At least one symbol is required.")
	}
	if len(parsed) > 50 {
		return httpx.BadRequest("Quote batch supports at most 50 symbols.")
	}
	resp, err := h.svc.QuoteBatch(ctx(c), parsed)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// notModifiedSince is the If-Modified-Since half of the conditional GETs.
func notModifiedSince(c *echo.Context, lastMod time.Time) bool {
	raw := c.Request().Header.Get("If-Modified-Since")
	if raw == "" {
		return false
	}
	since, err := time.Parse(http.TimeFormat, raw)
	return err == nil && !lastMod.After(since)
}

func unixFloat(ts float64) time.Time {
	sec, frac := math.Modf(ts)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

func (h *handlers) quote(c *echo.Context) error {
	symbol := param(c, "symbol")
	q, err := h.svc.Quote(ctx(c), symbol)
	if err != nil {
		return err
	}
	lastMod := unixFloat(q.Timestamp)
	etag := `W/"` + symbol + "-" + marketdata.SwiftDoubleString(q.Timestamp) + `"`
	if c.Request().Header.Get("If-None-Match") == etag && etag != "" {
		return c.NoContent(http.StatusNotModified)
	}
	if notModifiedSince(c, lastMod) {
		return c.NoContent(http.StatusNotModified)
	}
	hdr := c.Response().Header()
	hdr.Set("Last-Modified", lastMod.Format(http.TimeFormat))
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "public, max-age=60")
	return httpx.OK(c, q)
}

// profileHash replaces Swift's Hasher, which is seeded per process: Vapor's
// profile ETags never matched across restarts or replicas either. This one is
// stable.
func profileHash(p marketdata.CompanyProfileResponse) int64 {
	str := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	num := func(f *float64) string {
		if f == nil {
			return "0"
		}
		return strconv.FormatFloat(*f, 'g', -1, 64)
	}
	h := fnv.New64a()
	for _, part := range []string{
		str(p.Ticker), str(p.Name), num(p.MarketCapitalization), num(p.ShareOutstanding),
		str(p.Exchange), str(p.FinnhubIndustry), str(p.IPO), str(p.Logo), str(p.Currency), str(p.Country),
	} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return int64(h.Sum64()) //nolint:gosec // reinterpreting the hash bits as Swift's Int did
}

func (h *handlers) profile(c *echo.Context) error {
	symbol := param(c, "symbol")
	p, err := h.svc.Profile(ctx(c), symbol)
	if err != nil {
		return err
	}
	etag := `W/"` + symbol + "-" + strconv.FormatInt(profileHash(p), 10) + `"`
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}
	hdr := c.Response().Header()
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "public, max-age=300")
	return httpx.OK(c, p)
}

func (h *handlers) history(c *echo.Context) error {
	symbol := param(c, "symbol")
	resp, err := h.svc.History(ctx(c), symbol, query(c, "from"), query(c, "to"))
	if err != nil {
		return err
	}
	var lastMod *time.Time
	if n := len(resp.Bars); n > 0 {
		if t, err := time.Parse(time.DateOnly, resp.Bars[n-1].Date); err == nil {
			lastMod = &t
		}
	}
	base := 0.0
	if lastMod != nil {
		base = float64(lastMod.Unix())
	}
	etag := `W/"` + symbol + "-" + marketdata.SwiftDoubleString(base) + `"`
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}
	if lastMod != nil && notModifiedSince(c, *lastMod) {
		return c.NoContent(http.StatusNotModified)
	}
	hdr := c.Response().Header()
	if lastMod != nil {
		hdr.Set("Last-Modified", lastMod.Format(http.TimeFormat))
	}
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "public, max-age=300")
	return httpx.OK(c, resp)
}

func (h *handlers) basicFinancials(c *echo.Context) error {
	resp, err := h.svc.BasicFinancials(ctx(c), param(c, "symbol"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func analysisParams(c *echo.Context) marketdata.AnalysisParams {
	return marketdata.AnalysisParams{
		WACC:                queryDouble(c, "wacc"),
		TerminalGrowthRate:  queryDouble(c, "terminalGrowthRate"),
		TerminalMargin:      queryDouble(c, "terminalMargin"),
		FCFMarginAssumption: queryDouble(c, "fcfMarginAssumption"),
	}
}

func (h *handlers) analysis(c *echo.Context) error {
	resp, err := h.svc.Analysis(ctx(c), param(c, "symbol"), analysisParams(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) compare(c *echo.Context) error {
	raw := query(c, "symbols")
	if raw == nil {
		return httpx.BadRequest("Missing symbols query parameter.")
	}
	symbols := splitSymbols(*raw)
	if len(symbols) == 0 {
		return httpx.BadRequest("At least one symbol is required.")
	}
	if len(symbols) > 10 {
		return httpx.BadRequest("Compare supports at most 10 symbols.")
	}
	return httpx.OK(c, h.svc.Compare(ctx(c), symbols, analysisParams(c)))
}

func (h *handlers) cashFlowStatement(c *echo.Context) error {
	resp, err := h.svc.CashFlowStatement(ctx(c), param(c, "symbol"), queryInt(c, "limit"), query(c, "period"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) incomeStatement(c *echo.Context) error {
	resp, err := h.svc.IncomeStatement(ctx(c), param(c, "symbol"), queryInt(c, "limit"), query(c, "period"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) balanceSheetStatement(c *echo.Context) error {
	resp, err := h.svc.BalanceSheetStatement(ctx(c), param(c, "symbol"), queryInt(c, "limit"), query(c, "period"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) ratiosTTM(c *echo.Context) error {
	resp, err := h.svc.RatiosTTM(ctx(c), param(c, "symbol"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) gradesConsensus(c *echo.Context) error {
	resp, err := h.svc.GradesConsensus(ctx(c), param(c, "symbol"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) financialGrowth(c *echo.Context) error {
	resp, err := h.svc.FinancialGrowth(ctx(c), param(c, "symbol"), queryInt(c, "limit"), query(c, "period"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) earnings(c *echo.Context) error {
	resp, err := h.svc.Earnings(ctx(c), param(c, "symbol"), queryInt(c, "limit"))
	if err != nil {
		return err
	}
	return httpx.OK(c, marketdata.AnnotateEarningsStreaks(resp))
}

func (h *handlers) recentEarnings(c *echo.Context) error {
	resp, err := h.svc.RecentEarnings(ctx(c), param(c, "symbol"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) earningsCalendar(c *echo.Context) error {
	resp, err := h.svc.EarningsCalendar(ctx(c), query(c, "from"), query(c, "to"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) earningsTranscript(c *echo.Context) error {
	resp, err := h.svc.EarningsTranscript(ctx(c), param(c, "symbol"), query(c, "date"), queryInt(c, "year"), queryInt(c, "quarter"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) analystEstimates(c *echo.Context) error {
	period := "annual"
	if p := query(c, "period"); p != nil {
		period = *p
	}
	resp, err := h.svc.AnalystEstimates(ctx(c), param(c, "symbol"), period, queryInt(c, "page"), queryInt(c, "limit"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) ratios(c *echo.Context) error {
	resp, err := h.svc.Ratios(ctx(c), param(c, "symbol"), queryInt(c, "limit"), query(c, "period"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) historicalSectorPerformance(c *echo.Context) error {
	sector := query(c, "sector")
	if sector == nil {
		return httpx.BadRequest("Missing query parameter `sector`.")
	}
	resp, err := h.svc.HistoricalSectorPerformance(ctx(c), *sector, query(c, "exchange"), query(c, "from"), query(c, "to"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) technicals(c *echo.Context) error {
	symbol, err := requirePathSymbol(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.Technicals(ctx(c), symbol)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) insiderActivity(c *echo.Context) error {
	symbol, err := requirePathSymbol(c)
	if err != nil {
		return err
	}
	days := marketdata.InsiderDefaultWindowDays
	if d := queryInt(c, "days"); d != nil {
		days = *d
	}
	if days < marketdata.InsiderMinWindowDays || days > marketdata.InsiderMaxWindowDays {
		return httpx.BadRequest("`days` must be between 30 and 1825.")
	}
	resp, err := h.svc.InsiderActivity(ctx(c), symbol, days)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) congressTrades(c *echo.Context) error {
	symbol, err := requirePathSymbol(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.CongressTrades(ctx(c), symbol)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) recentCongressTrades(c *echo.Context) error {
	limit := marketdata.CongressDefaultRecentLimit
	if l := queryInt(c, "limit"); l != nil {
		limit = *l
	}
	if limit < marketdata.CongressMinRecentLimit || limit > marketdata.CongressMaxRecentLimit {
		return httpx.BadRequest("`limit` must be between 1 and 200.")
	}
	resp, err := h.svc.RecentCongressTrades(ctx(c), limit)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) institutionalOwnership(c *echo.Context) error {
	symbol, err := requirePathSymbol(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.InstitutionalOwnership(ctx(c), symbol)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) search(c *echo.Context) error {
	q := query(c, "q")
	if q == nil {
		return httpx.BadRequest("Missing query parameter `q`.")
	}
	resp, err := h.svc.Search(ctx(c), *q)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) fx(c *echo.Context) error {
	pair := "EURUSD"
	if p := query(c, "pair"); p != nil {
		pair = *p
	}
	resp, err := h.svc.Fx(ctx(c), pair)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func chartRange(c *echo.Context) string {
	if r := query(c, "range"); r != nil {
		return *r
	}
	return "1D"
}

func (h *handlers) priceChart(c *echo.Context) error {
	resp, err := h.svc.PriceChart(ctx(c), param(c, "symbol"), chartRange(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) priceChartComparison(c *echo.Context) error {
	raw := query(c, "symbols")
	if raw == nil {
		return httpx.BadRequest("Missing query parameter `symbols`.")
	}
	// Swift split on "," without trimming; empty pieces are dropped by split.
	var symbols []string
	for _, p := range strings.Split(*raw, ",") {
		if p != "" {
			symbols = append(symbols, p)
		}
	}
	resp, err := h.svc.PriceChartComparison(ctx(c), symbols, chartRange(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

