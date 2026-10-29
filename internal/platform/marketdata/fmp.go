package marketdata

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/platform/cache"

	"golang.org/x/sync/singleflight"
)

// DefaultFMPBaseURL is LiveFMPMarketDataProvider's default base URL.
const DefaultFMPBaseURL = "https://financialmodelingprep.com"

// FMP cache keys shared with Vapor.
const (
	FMPRateLimitedKey      = "fmp:rate-limited:v1"
	fmpClosedRangeTTL      = 7 * 86400
	fmpEODKeyPrefix        = "fmp:eod:v1:"
	fmpRequestTimeout      = 30 * time.Second
	fmpRejectedReason      = "Market data isn’t available right now. Please try again later."
	fmpPaymentReason       = "Chart data isn’t available for this symbol right now. Try another symbol or check back later."
	fmpUnavailableReason   = "Market data isn’t available for this symbol right now. Please try again later."
	fmpNotFoundReason      = "No market data found for this symbol."
	fmpKeyNotConfiguredMsg = "FMP_API_KEY is not configured."
)

// FMP is LiveFMPMarketDataProvider: Financial Modeling Prep's /stable API.
// Every call goes through Get, so new endpoints (crypto, news) are one method.
type FMP struct {
	BaseURL string
	APIKey  string
	// Cache holds EOD history and the "FMP is rate limiting us" marker
	// (Vapor's RedisJSONCache(label: "fmp")).
	Cache cache.Cache
	// EODTTL is the TTL for EOD ranges reaching today (MARKET_TTL_EOD_SECONDS).
	EODTTL int
	Client *http.Client
	Logger *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	eodFlight singleflight.Group
}

// Name is "fmp".
func (f *FMP) Name() string { return "fmp" }

func (f *FMP) logger() *slog.Logger {
	if f.Logger != nil {
		return f.Logger
	}
	return slog.Default()
}

func (f *FMP) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Query is an ordered list of query parameters; a nil value is skipped.
type Query []QueryParam

// QueryParam is one name/value pair.
type QueryParam struct {
	Name  string
	Value *string
}

// Q builds a parameter with a value.
func Q(name, value string) QueryParam { return QueryParam{Name: name, Value: &value} }

// QOpt builds a parameter that is skipped when value is nil.
func QOpt(name string, value *string) QueryParam { return QueryParam{Name: name, Value: value} }

// Get is LiveFMPMarketDataProvider.fetchJSON: GET path with the query plus
// apikey, decoded strictly into out, with Vapor's status mapping.
func (f *FMP) Get(ctx context.Context, path string, query Query, out any) error {
	if strings.TrimSpace(f.APIKey) == "" {
		return unavailable(fmpKeyNotConfiguredMsg)
	}
	var retryAfter int
	if f.Cache != nil && f.Cache.Get(ctx, FMPRateLimitedKey, &retryAfter) {
		return RateLimitedError(retryAfter)
	}
	base := strings.TrimSuffix(f.BaseURL, "/")
	u, err := url.Parse(base + path)
	if err != nil {
		return abort(http.StatusInternalServerError, "Invalid FMP base URL configuration.")
	}
	parts := make([]string, 0, len(query)+1)
	for _, q := range query {
		if q.Value != nil {
			parts = append(parts, url.QueryEscape(q.Name)+"="+url.QueryEscape(*q.Value))
		}
	}
	parts = append(parts, "apikey="+url.QueryEscape(f.APIKey))
	u.RawQuery = strings.Join(parts, "&")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := doRequest(ctx, f.Client, req, fmpRequestTimeout)
	if err != nil {
		return err
	}
	switch resp.status {
	case http.StatusOK:
		if err := decodeBody(resp.body, out); err != nil {
			return badGateway("Failed to decode FMP response for " + path + ".")
		}
		return nil
	case http.StatusNotFound:
		return notFound(fmpNotFoundReason)
	case http.StatusUnauthorized, http.StatusForbidden:
		f.logger().Error("FMP rejected the request.", "status", resp.status, "path", u.Path,
			"apiKeyLength", len(f.APIKey), "response", strings.ReplaceAll(resp.bodyString(), f.APIKey, "<redacted>"))
		return badGateway(fmpRejectedReason)
	case http.StatusPaymentRequired:
		f.logger().Warn("FMP coverage/subscription limit", "path", path, "status", resp.status, "response", resp.bodyString())
		return paymentRequired(fmpPaymentReason)
	case http.StatusTooManyRequests:
		retry := 60
		if v, err := strconv.Atoi(resp.header.Get("Retry-After")); err == nil {
			retry = max(1, v)
		}
		f.logger().Error("FMP request failed", "path", path, "status", resp.status, "retry_after", retry, "response", resp.bodyString())
		if f.Cache != nil {
			f.Cache.Set(ctx, FMPRateLimitedKey, retry, retry)
		}
		return RateLimitedError(retry)
	}
	f.logger().Error("FMP request failed", "path", path, "status", resp.status, "response", resp.bodyString())
	return badGateway(fmpUnavailableReason)
}

func fmpGet[T any](ctx context.Context, f *FMP, path string, query Query) (T, error) {
	var out T
	err := f.Get(ctx, path, query, &out)
	return out, err
}

func strPtrInt(v *int) *string {
	if v == nil {
		return nil
	}
	s := strconv.Itoa(*v)
	return &s
}

func statementQuery(symbol string, limit *int, period *string) Query {
	return Query{Q("symbol", symbol), QOpt("limit", strPtrInt(limit)), QOpt("period", normalizeOptionalText(period))}
}

// CashFlowStatement is /stable/cash-flow-statement.
func (f *FMP) CashFlowStatement(ctx context.Context, rawSymbol string, limit *int, period *string) ([]CashFlowStatementResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]CashFlowStatementResponse](ctx, f, "/stable/cash-flow-statement", statementQuery(symbol, limit, period))
}

// IncomeStatement is /stable/income-statement.
func (f *FMP) IncomeStatement(ctx context.Context, rawSymbol string, limit *int, period *string) ([]IncomeStatementResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]IncomeStatementResponse](ctx, f, "/stable/income-statement", statementQuery(symbol, limit, period))
}

// BalanceSheetStatement is /stable/balance-sheet-statement.
func (f *FMP) BalanceSheetStatement(ctx context.Context, rawSymbol string, limit *int, period *string) ([]BalanceSheetStatementResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]BalanceSheetStatementResponse](ctx, f, "/stable/balance-sheet-statement", statementQuery(symbol, limit, period))
}

// RatiosTTM is /stable/ratios-ttm.
func (f *FMP) RatiosTTM(ctx context.Context, rawSymbol string) ([]RatiosTTMResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]RatiosTTMResponse](ctx, f, "/stable/ratios-ttm", Query{Q("symbol", symbol)})
}

// GradesConsensus is /stable/grades-consensus.
func (f *FMP) GradesConsensus(ctx context.Context, rawSymbol string) ([]GradesConsensusResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]GradesConsensusResponse](ctx, f, "/stable/grades-consensus", Query{Q("symbol", symbol)})
}

// FinancialGrowth is /stable/financial-growth.
func (f *FMP) FinancialGrowth(ctx context.Context, rawSymbol string, limit *int, period *string) ([]FinancialGrowthResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]FinancialGrowthResponse](ctx, f, "/stable/financial-growth", statementQuery(symbol, limit, period))
}

// AnalystEstimates is /stable/analyst-estimates.
func (f *FMP) AnalystEstimates(ctx context.Context, rawSymbol, rawPeriod string, page, limit *int) ([]AnalystEstimatesResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	period := strings.TrimSpace(rawPeriod)
	if period == "" {
		return nil, badRequest("Period is required.")
	}
	return fmpGet[[]AnalystEstimatesResponse](ctx, f, "/stable/analyst-estimates",
		Query{Q("symbol", symbol), Q("period", period), QOpt("page", strPtrInt(page)), QOpt("limit", strPtrInt(limit))})
}

// Ratios is /stable/ratios.
func (f *FMP) Ratios(ctx context.Context, rawSymbol string, limit *int, period *string) ([]RatiosResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]RatiosResponse](ctx, f, "/stable/ratios", statementQuery(symbol, limit, period))
}

type fmpEarningsItem struct {
	Symbol           string   `json:"symbol"`
	Date             string   `json:"date"`
	EpsActual        *float64 `json:"epsActual,omitempty"`
	EpsEstimated     *float64 `json:"epsEstimated,omitempty"`
	RevenueActual    *float64 `json:"revenueActual,omitempty"`
	RevenueEstimated *float64 `json:"revenueEstimated,omitempty"`
	LastUpdated      *string  `json:"lastUpdated,omitempty"`
}

func (it fmpEarningsItem) response(hasTranscript bool) EarningsResponse {
	return EarningsResponse{
		Symbol: it.Symbol, Date: it.Date,
		EpsActual: it.EpsActual, EpsEstimated: it.EpsEstimated,
		RevenueActual: it.RevenueActual, RevenueEstimated: it.RevenueEstimated,
		LastUpdated:     it.LastUpdated,
		SurprisePercent: earningsSurprisePercent(it.EpsActual, it.EpsEstimated),
		HasTranscript:   hasTranscript,
	}
}

func earningsSurprisePercent(actual, estimate *float64) *float64 {
	if actual == nil || estimate == nil || *estimate == 0 {
		return nil
	}
	return ptr(((*actual - *estimate) / math.Abs(*estimate)) * 100)
}

// Earnings is /stable/earnings plus transcript availability.
func (f *FMP) Earnings(ctx context.Context, rawSymbol string, limit *int) ([]EarningsResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	items, err := fmpGet[[]fmpEarningsItem](ctx, f, "/stable/earnings", Query{Q("symbol", symbol), QOpt("limit", strPtrInt(limit))})
	if err != nil {
		return nil, err
	}
	availability, err := f.transcriptAvailability(ctx, symbol)
	if err != nil {
		availability = map[transcriptKey]bool{}
	}
	out := make([]EarningsResponse, 0, len(items))
	for _, it := range items {
		out = append(out, it.response(hasTranscript(transcriptKeyFor(it.Date), availability)))
	}
	return out, nil
}

// EarningsCalendar is /stable/earnings-calendar.
func (f *FMP) EarningsCalendar(ctx context.Context, from, to *time.Time) ([]EarningsResponse, error) {
	var q Query
	if from != nil {
		q = append(q, Q("from", FormatDateOnly(*from)))
	}
	if to != nil {
		q = append(q, Q("to", FormatDateOnly(*to)))
	}
	items, err := fmpGet[[]fmpEarningsItem](ctx, f, "/stable/earnings-calendar", q)
	if err != nil {
		return nil, err
	}
	out := make([]EarningsResponse, 0, len(items))
	for _, it := range items {
		out = append(out, it.response(false))
	}
	return out, nil
}

// Swift's decodeIntIfPresent tried Int, then String; but decodeIfPresent
// throws on a type mismatch instead of returning nil, so a string year or
// quarter failed the whole decode. Plain *int reproduces that.
type fmpTranscriptDateItem struct {
	Date    *string `json:"date,omitempty"`
	Quarter *int    `json:"quarter,omitempty"`
	Year    *int    `json:"year,omitempty"`
}

type fmpTranscriptItem struct {
	Symbol  *string `json:"symbol,omitempty"`
	Date    *string `json:"date,omitempty"`
	Period  *string `json:"period,omitempty"`
	Year    *int    `json:"year,omitempty"`
	Quarter *int    `json:"quarter,omitempty"`
	Content *string `json:"content,omitempty"`
}

// transcriptKey is TranscriptAvailabilityKey; the has* flags stand for
// non-nil.
type transcriptKey struct {
	date          string
	hasDate       bool
	year, quarter int
	hasYear       bool
	hasQuarter    bool
}

func normalizedDateOnly(raw *string) *string {
	if raw == nil {
		return nil
	}
	t := strings.TrimSpace(*raw)
	if t == "" {
		return nil
	}
	r := []rune(t)
	if len(r) > 10 {
		r = r[:10]
	}
	s := string(r)
	return &s
}

func transcriptKeyFor(rawDate string) transcriptKey {
	k := transcriptKey{}
	d := normalizedDateOnly(&rawDate)
	if d == nil {
		return k
	}
	k.date, k.hasDate = *d, true
	parts := strings.Split(*d, "-")
	if y, err := strconv.Atoi(parts[0]); err == nil && parts[0] != "" {
		k.year, k.hasYear = y, true
	}
	if len(parts) >= 2 {
		if m, err := strconv.Atoi(parts[1]); err == nil {
			k.quarter, k.hasQuarter = ((m-1)/3)+1, true
		}
	}
	return k
}

func (f *FMP) transcriptAvailability(ctx context.Context, symbol string) (map[transcriptKey]bool, error) {
	items, err := fmpGet[[]fmpTranscriptDateItem](ctx, f, "/stable/earning-call-transcript-dates", Query{Q("symbol", symbol)})
	if err != nil {
		return nil, err
	}
	out := map[transcriptKey]bool{}
	for _, it := range items {
		k := transcriptKey{}
		if d := normalizedDateOnly(it.Date); d != nil {
			k.date, k.hasDate = *d, true
		}
		if it.Year != nil {
			k.year, k.hasYear = *it.Year, true
		}
		if it.Quarter != nil {
			k.quarter, k.hasQuarter = *it.Quarter, true
		}
		out[k] = true
	}
	return out, nil
}

func hasTranscript(event transcriptKey, availability map[transcriptKey]bool) bool {
	if availability[event] {
		return true
	}
	if event.hasDate && availability[transcriptKey{date: event.date, hasDate: true}] {
		return true
	}
	if event.hasYear && event.hasQuarter &&
		availability[transcriptKey{year: event.year, hasYear: true, quarter: event.quarter, hasQuarter: true}] {
		return true
	}
	return false
}

// EarningsTranscript is /stable/earning-call-transcript.
func (f *FMP) EarningsTranscript(ctx context.Context, rawSymbol string, rawDate *string, year, quarter *int) (EarningsTranscriptResponse, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return EarningsTranscriptResponse{}, err
	}
	resolvedDate, resolvedYear, resolvedQuarter, err := f.resolveTranscriptPeriod(ctx, symbol, rawDate, year, quarter)
	if err != nil {
		return EarningsTranscriptResponse{}, err
	}
	items, err := fmpGet[[]fmpTranscriptItem](ctx, f, "/stable/earning-call-transcript", Query{
		Q("symbol", symbol), Q("year", strconv.Itoa(resolvedYear)), Q("quarter", strconv.Itoa(resolvedQuarter)),
	})
	if err != nil {
		return EarningsTranscriptResponse{}, err
	}
	label := symbol + " " + strconv.Itoa(resolvedYear) + " Q" + strconv.Itoa(resolvedQuarter)
	if len(items) == 0 {
		return EarningsTranscriptResponse{}, notFound("No earnings transcript found for " + label + ".")
	}
	item := items[0]
	content := ""
	if item.Content != nil {
		content = strings.TrimSpace(*item.Content)
	}
	if content == "" {
		return EarningsTranscriptResponse{}, notFound("No earnings transcript content found for " + label + ".")
	}
	sym := symbol
	if item.Symbol != nil {
		sym = *item.Symbol
	}
	date := ""
	if d := normalizedDateOnly(item.Date); d != nil {
		date = *d
	} else if resolvedDate != nil {
		date = *resolvedDate
	}
	y, q := resolvedYear, resolvedQuarter
	if item.Year != nil {
		y = *item.Year
	}
	if item.Quarter != nil {
		q = *item.Quarter
	}
	return EarningsTranscriptResponse{
		Symbol: strings.ToUpper(sym), Date: date, Year: y, Quarter: q,
		Period: item.Period, Content: content, Provider: f.Name(),
	}, nil
}

func validateTranscriptQuarter(q int) error {
	if q < 1 || q > 4 {
		return badRequest("Query parameter `quarter` must be between 1 and 4.")
	}
	return nil
}

func (f *FMP) resolveTranscriptPeriod(ctx context.Context, symbol string, rawDate *string, year, quarter *int) (*string, int, int, error) {
	if year != nil && quarter != nil {
		if err := validateTranscriptQuarter(*quarter); err != nil {
			return nil, 0, 0, err
		}
		return normalizedDateOnly(rawDate), *year, *quarter, nil
	}
	date := normalizedDateOnly(rawDate)
	if date == nil {
		return nil, 0, 0, badRequest("Query parameter `date` is required unless `year` and `quarter` are provided.")
	}
	availability, err := f.transcriptAvailability(ctx, symbol)
	if err != nil {
		return nil, 0, 0, err
	}
	// Swift took `Set.first(where:)`, whose order is unspecified; any exact
	// match carries the same date, so the first found is as good.
	for k := range availability {
		if k.hasDate && k.date == *date && k.hasYear && k.hasQuarter {
			if err := validateTranscriptQuarter(k.quarter); err != nil {
				return nil, 0, 0, err
			}
			return date, k.year, k.quarter, nil
		}
	}
	fallback := transcriptKeyFor(*date)
	if !fallback.hasYear || !fallback.hasQuarter {
		return nil, 0, 0, badRequest("Invalid date. Expected YYYY-MM-DD.")
	}
	if err := validateTranscriptQuarter(fallback.quarter); err != nil {
		return nil, 0, 0, err
	}
	return date, fallback.year, fallback.quarter, nil
}

// HistoricalSectorPerformance is /stable/historical-sector-performance.
func (f *FMP) HistoricalSectorPerformance(ctx context.Context, rawSector string, exchange *string, from, to *time.Time) ([]HistoricalSectorPerformanceResponse, error) {
	sector := strings.TrimSpace(rawSector)
	if sector == "" {
		return nil, badRequest("Sector is required.")
	}
	var fromS, toS *string
	if from != nil {
		fromS = ptr(FormatDateOnly(*from))
	}
	if to != nil {
		toS = ptr(FormatDateOnly(*to))
	}
	return fmpGet[[]HistoricalSectorPerformanceResponse](ctx, f, "/stable/historical-sector-performance", Query{
		Q("sector", sector), QOpt("exchange", normalizeOptionalText(exchange)), QOpt("from", fromS), QOpt("to", toS),
	})
}

// GeneralMarketNews is /stable/news/stock-latest.
func (f *FMP) GeneralMarketNews(ctx context.Context, page, limit *int, from, to *time.Time) ([]FMPMarketNewsItem, error) {
	var fromS, toS *string
	if from != nil {
		fromS = ptr(FormatDateOnly(*from))
	}
	if to != nil {
		toS = ptr(FormatDateOnly(*to))
	}
	return fmpGet[[]FMPMarketNewsItem](ctx, f, "/stable/news/stock-latest", Query{
		QOpt("page", strPtrInt(page)), QOpt("limit", strPtrInt(limit)), QOpt("from", fromS), QOpt("to", toS),
	})
}

// StockQuotes is /stable/quote for a comma-joined symbol list.
func (f *FMP) StockQuotes(ctx context.Context, symbols []string) ([]CryptoQuoteResponse, error) {
	var parts []string
	for _, s := range symbols {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return []CryptoQuoteResponse{}, nil
	}
	return fmpGet[[]CryptoQuoteResponse](ctx, f, "/stable/quote", Query{Q("symbol", strings.Join(parts, ","))})
}

// BiggestGainers is /stable/biggest-gainers.
func (f *FMP) BiggestGainers(ctx context.Context) ([]FMPMoverItem, error) {
	return fmpGet[[]FMPMoverItem](ctx, f, "/stable/biggest-gainers", nil)
}

// BiggestLosers is /stable/biggest-losers.
func (f *FMP) BiggestLosers(ctx context.Context) ([]FMPMoverItem, error) {
	return fmpGet[[]FMPMoverItem](ctx, f, "/stable/biggest-losers", nil)
}

// TopMarketCapUniverse is the US large-cap company screener.
func (f *FMP) TopMarketCapUniverse(ctx context.Context, limit int) ([]FMPScreenerItem, error) {
	return fmpGet[[]FMPScreenerItem](ctx, f, "/stable/company-screener", Query{
		Q("marketCapMoreThan", "10000000000"), Q("country", "US"), Q("isEtf", "false"),
		Q("isFund", "false"), Q("isActivelyTrading", "true"), Q("limit", strconv.Itoa(limit)),
	})
}

// InsiderTrades is one page of /stable/insider-trading/search.
func (f *FMP) InsiderTrades(ctx context.Context, rawSymbol string, page, limit int) ([]FMPInsiderTrade, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]FMPInsiderTrade](ctx, f, "/stable/insider-trading/search", Query{
		Q("symbol", symbol), Q("page", strconv.Itoa(max(0, page))), Q("limit", strconv.Itoa(limit)),
	})
}

// StockPriceChange is /stable/stock-price-change.
func (f *FMP) StockPriceChange(ctx context.Context, rawSymbol string) ([]FMPStockPriceChange, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]FMPStockPriceChange](ctx, f, "/stable/stock-price-change", Query{Q("symbol", symbol)})
}

// SenateTrades is /stable/senate-trades.
func (f *FMP) SenateTrades(ctx context.Context, rawSymbol string) ([]FMPCongressTrade, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]FMPCongressTrade](ctx, f, "/stable/senate-trades", Query{Q("symbol", symbol)})
}

// HouseTrades is /stable/house-trades.
func (f *FMP) HouseTrades(ctx context.Context, rawSymbol string) ([]FMPCongressTrade, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]FMPCongressTrade](ctx, f, "/stable/house-trades", Query{Q("symbol", symbol)})
}

// LatestSenateTrades is /stable/senate-latest.
func (f *FMP) LatestSenateTrades(ctx context.Context, limit int) ([]FMPCongressTrade, error) {
	return fmpGet[[]FMPCongressTrade](ctx, f, "/stable/senate-latest", Query{Q("page", "0"), Q("limit", strconv.Itoa(limit))})
}

// LatestHouseTrades is /stable/house-latest.
func (f *FMP) LatestHouseTrades(ctx context.Context, limit int) ([]FMPCongressTrade, error) {
	return fmpGet[[]FMPCongressTrade](ctx, f, "/stable/house-latest", Query{Q("page", "0"), Q("limit", strconv.Itoa(limit))})
}

// InstitutionalHolders is the 13F holder extract for one quarter.
func (f *FMP) InstitutionalHolders(ctx context.Context, rawSymbol string, year, quarter int) ([]FMPInstitutionalHolder, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]FMPInstitutionalHolder](ctx, f, "/stable/institutional-ownership/extract-analytics/holder", Query{
		Q("symbol", symbol), Q("year", strconv.Itoa(year)), Q("quarter", strconv.Itoa(quarter)),
	})
}

// InstitutionalPositionsSummary is the 13F summary for one quarter.
func (f *FMP) InstitutionalPositionsSummary(ctx context.Context, rawSymbol string, year, quarter int) ([]FMPInstitutionalPositionsSummary, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]FMPInstitutionalPositionsSummary](ctx, f, "/stable/institutional-ownership/symbol-positions-summary", Query{
		Q("symbol", symbol), Q("year", strconv.Itoa(year)), Q("quarter", strconv.Itoa(quarter)),
	})
}

// StockIntraday is /stable/historical-chart/{interval}.
func (f *FMP) StockIntraday(ctx context.Context, interval, rawSymbol string, from, to *string) ([]CryptoHistoricalPoint, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return fmpGet[[]CryptoHistoricalPoint](ctx, f, "/stable/historical-chart/"+interval, Query{Q("symbol", symbol), QOpt("from", from), QOpt("to", to)})
}

// StockHistoricalEOD is /stable/historical-price-eod/light, cached under
// fmp:eod:v1:{symbol}:{from|-}:{to|-}. Concurrent identical requests share
// one upstream call.
func (f *FMP) StockHistoricalEOD(ctx context.Context, rawSymbol string, from, to *string) ([]CryptoHistoricalLightPoint, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	fromPart, toPart := "-", "-"
	if from != nil {
		fromPart = *from
	}
	if to != nil {
		toPart = *to
	}
	key := fmpEODKeyPrefix + symbol + ":" + fromPart + ":" + toPart
	var cached []CryptoHistoricalLightPoint
	if f.Cache != nil && f.Cache.Get(ctx, key, &cached) {
		return cached, nil
	}
	v, err, _ := f.eodFlight.Do(key, func() (any, error) {
		points, err := fmpGet[[]CryptoHistoricalLightPoint](ctx, f, "/stable/historical-price-eod/light",
			Query{Q("symbol", symbol), QOpt("from", from), QOpt("to", to)})
		if err != nil {
			return nil, err
		}
		if f.Cache != nil {
			f.Cache.Set(ctx, key, points, f.EODCacheTTL(to, f.now()))
		}
		return points, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]CryptoHistoricalLightPoint), nil
}

// EODCacheTTL is eodCacheTTL: a range ending before today (UTC) can no longer
// change and is kept a week.
func (f *FMP) EODCacheTTL(to *string, now time.Time) int {
	ttl := f.EODTTL
	if ttl == 0 {
		ttl = 3600
	}
	if to == nil {
		return ttl
	}
	if *to < FormatDateOnly(now) {
		return fmpClosedRangeTTL
	}
	return ttl
}
