package marketdata

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/cache"
	"norviq-api/internal/store/marketdatadb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

// Service is DefaultMarketDataService: Redis hot cache, then the Postgres
// cache tables Vapor owns, then the provider. A provider failure falls back to
// stale rows when there are any. Degraded upstream answers (an all-zero quote,
// a provider error) are never written to either cache.
type Service struct {
	Provider Provider
	// FMP is nil when FMP_API_KEY is unset; FMP-backed endpoints then answer
	// 503 "FMP_API_KEY is not configured." (or degrade, where Vapor did).
	FMP    *FMP
	Config CacheConfig
	Tier   FMPAccessTier
	Q      marketdatadb.Querier
	Cache  cache.Cache
	Logger *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	basicFinancialsFlight singleflight.Group
}

// baseURLOr returns raw trimmed, or def when it is blank.
func baseURLOr(raw, def string) string {
	if t := strings.TrimSpace(raw); t != "" {
		return t
	}
	return def
}

// NewFromSettings builds the service the way Vapor's configure.swift did:
// provider selection from MARKET_PROVIDER and the credentials present, FMP
// when FMP_API_KEY is set. rdb may be nil (no Redis: every read is a miss).
func NewFromSettings(s Settings, q marketdatadb.Querier, rdb redis.UniversalClient, client *http.Client, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	cfg := s.CacheConfig()
	redisCache := cache.New(rdb, "market", logger)
	var provider Provider = Disabled{}
	switch SelectProvider(s.MarketProvider, strings.TrimSpace(s.FinnhubAPIKey) != "", strings.TrimSpace(s.IBKRAPIBaseURL) != "") {
	case KindFinnhub:
		provider = &Finnhub{BaseURL: baseURLOr(s.FinnhubBaseURL, DefaultFinnhubBaseURL), APIKey: strings.TrimSpace(s.FinnhubAPIKey), DefaultCurrency: s.providerCurrency(), Client: client, Logger: logger}
	case KindIBKR:
		provider = &IBKR{BaseURL: strings.TrimSpace(s.IBKRAPIBaseURL), DefaultCurrency: s.providerCurrency(), Client: client}
	case KindDisabled:
	}
	var fmp *FMP
	if key := strings.TrimSpace(s.FMPAPIKey); key != "" {
		fmp = &FMP{BaseURL: baseURLOr(s.FMPBaseURL, DefaultFMPBaseURL), APIKey: key, Cache: redisCache, EODTTL: cfg.EODTTL, Client: client, Logger: logger}
	}
	return &Service{
		Provider: provider,
		FMP:      fmp,
		Config:   cfg,
		Tier:     ParseTier(s.FMPSymbolTier),
		Q:        q,
		Cache:    redisCache,
		Logger:   logger,
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func isFresh(at *time.Time, ttlSeconds int, now time.Time) bool {
	if at == nil {
		return false
	}
	return now.Sub(*at) <= time.Duration(ttlSeconds)*time.Second
}

func firstTime(a, b *time.Time) *time.Time {
	if a != nil {
		return a
	}
	return b
}

func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// RequireFMP is requireFMPProvider.
func (s *Service) RequireFMP() (*FMP, error) {
	if s.FMP == nil {
		return nil, unavailable("FMP_API_KEY is not configured.")
	}
	return s.FMP, nil
}

// ValidateFMPSymbolAccess is validateFMPSymbolAccess: on the free tier only
// the hardcoded symbol list is licensed.
func (s *Service) ValidateFMPSymbolAccess(symbol, operation string) error {
	if s.Tier.AllowsAllSymbols() || IsSupportedOnFreeTier(symbol) {
		return nil
	}
	return paymentRequired("FMP free-tier coverage for " + operation + " does not include " + symbol +
		". Use a supported symbol or upgrade the market data tier.")
}

// ---- Quote ----------------------------------------------------------------

func quoteResponseFromRow(r marketdatadb.GetQuoteCacheRow) QuoteResponse {
	return QuoteResponse{
		Symbol:        r.Symbol,
		Currency:      r.Currency,
		CurrentPrice:  r.Price,
		Change:        r.Change,
		PercentChange: r.PercentChange,
		High:          r.High,
		Low:           r.Low,
		Open:          r.Open,
		PreviousClose: r.PreviousClose,
		Timestamp:     float64(r.AsOf.UnixMicro()) / 1e6,
	}
}

// IsEmptyQuote is isEmptyQuote: no usable price.
func IsEmptyQuote(q Quote) bool {
	if q.Price > 0 {
		return false
	}
	for _, v := range []*float64{q.PreviousClose, q.Open, q.High, q.Low} {
		if v != nil && *v != 0 {
			return false
		}
	}
	return true
}

// Quote is quote(symbol:).
func (s *Service) Quote(ctx context.Context, rawSymbol string) (QuoteResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return QuoteResponse{}, err
	}
	now := s.now()
	providerName := s.Provider.Name()
	key := "market:quote:" + providerName + ":" + symbol
	ttl := s.Config.QuoteTTL

	var hot QuoteResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}

	var existing *marketdatadb.GetQuoteCacheRow
	row, err := s.Q.GetQuoteCache(ctx, marketdatadb.GetQuoteCacheParams{Provider: providerName, Symbol: symbol})
	switch {
	case err == nil:
		existing = &row
	case !noRows(err):
		return QuoteResponse{}, err
	}
	if existing != nil && isFresh(&existing.AsOf, ttl, now) {
		resp := quoteResponseFromRow(*existing)
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}

	resp, err := s.fetchQuote(ctx, symbol, providerName, existing)
	if err == nil {
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	if errors.Is(err, errEmptyQuoteServedStale) {
		return quoteResponseFromRow(*existing), nil
	}
	if existing != nil {
		s.logger().Warn("market.quote stale fallback", "symbol", symbol, "provider", providerName)
		resp := quoteResponseFromRow(*existing)
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	if IsQuoteUnavailable(err) {
		return QuoteResponse{}, err
	}
	return QuoteResponse{}, mapProviderError(err, "quote")
}

// errEmptyQuoteServedStale marks the empty-quote path that answers with the
// stale row without re-caching it (Vapor returned it straight from the guard).
var errEmptyQuoteServedStale = errors.New("empty quote; serve stale row")

func (s *Service) fetchQuote(ctx context.Context, symbol, providerName string, existing *marketdatadb.GetQuoteCacheRow) (QuoteResponse, error) {
	fresh, err := s.Provider.Quote(ctx, symbol)
	if err != nil {
		return QuoteResponse{}, err
	}
	if IsEmptyQuote(fresh) {
		if fallback := s.fmpFallbackQuote(ctx, symbol); fallback != nil {
			s.logger().Info("market.quote fmp_fallback", "symbol", symbol, "primary", providerName)
			fresh = *fallback
		}
	}
	if IsEmptyQuote(fresh) {
		s.logger().Warn("market.quote empty", "symbol", symbol, "provider", providerName, "fmp_available", s.FMP != nil)
		if existing != nil {
			return QuoteResponse{}, errEmptyQuoteServedStale
		}
		return QuoteResponse{}, QuoteUnavailableError(symbol)
	}
	saved, err := s.Q.UpsertQuoteCache(ctx, marketdatadb.UpsertQuoteCacheParams{
		ID: uuid.New(), Provider: providerName, Symbol: fresh.Symbol, Currency: fresh.Currency,
		Price: fresh.Price, AsOf: fresh.AsOf, Change: fresh.Change, PercentChange: fresh.PercentChange,
		High: fresh.High, Low: fresh.Low, Open: fresh.Open, PreviousClose: fresh.PreviousClose,
	})
	if err != nil {
		return QuoteResponse{}, err
	}
	return quoteResponseFromRow(marketdatadb.GetQuoteCacheRow(saved)), nil
}

func (s *Service) fmpFallbackQuote(ctx context.Context, symbol string) *Quote {
	if s.FMP == nil {
		return nil
	}
	if s.ValidateFMPSymbolAccess(symbol, "quote") != nil {
		s.logger().Info("market.quote fmp_fallback_skipped", "symbol", symbol, "reason", "tier", "tier", string(s.Tier))
		return nil
	}
	quotes, err := s.FMP.StockQuotes(ctx, []string{symbol})
	if err != nil {
		s.logger().Warn("market.quote fmp_fallback_failed", "symbol", symbol, "error", err)
		return nil
	}
	if len(quotes) == 0 {
		return nil
	}
	match := quotes[0]
	for i := range quotes {
		if strings.EqualFold(quotes[i].Symbol, symbol) {
			match = quotes[i]
			break
		}
	}
	asOf := s.now()
	if match.Timestamp > 0 {
		asOf = time.Unix(int64(match.Timestamp), 0)
	}
	mapped := Quote{
		Symbol: symbol, Price: match.Price, Change: ptr(match.Change), PercentChange: ptr(match.ChangePercentage),
		High: match.DayHigh, Low: match.DayLow, Open: match.Open, PreviousClose: match.PreviousClose,
		Currency: s.Config.DefaultCurrency, AsOf: asOf,
	}
	if IsEmptyQuote(mapped) {
		return nil
	}
	return &mapped
}

// QuoteBatch is quoteBatch: chunks of ten fetched concurrently, order kept,
// any failure fails the batch.
func (s *Service) QuoteBatch(ctx context.Context, rawSymbols []string) (QuoteBatchResponse, error) {
	normalized := make([]string, 0, len(rawSymbols))
	for _, raw := range rawSymbols {
		sym, err := normalizeSymbol(raw)
		if err != nil {
			return QuoteBatchResponse{}, err
		}
		normalized = append(normalized, sym)
	}
	normalized = uniqued(normalized)
	if len(normalized) == 0 {
		return QuoteBatchResponse{}, badRequest("At least one symbol is required.")
	}
	if len(normalized) > 100 {
		return QuoteBatchResponse{}, badRequest("Maximum 100 symbols per batch request.")
	}
	quotes := make([]QuoteResponse, len(normalized))
	for start := 0; start < len(normalized); start += 10 {
		end := min(start+10, len(normalized))
		g, gctx := errgroup.WithContext(ctx)
		for i := start; i < end; i++ {
			g.Go(func() error {
				q, err := s.Quote(gctx, normalized[i])
				quotes[i] = q
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return QuoteBatchResponse{}, err
		}
	}
	return QuoteBatchResponse{Quotes: quotes}, nil
}

// ---- History --------------------------------------------------------------

func (s *Service) loadCachedHistory(ctx context.Context, symbol string, from, to *time.Time) ([]marketdatadb.ListPriceHistoryRow, error) {
	var f, t *time.Time
	if from != nil {
		f = ptr(StartOfDay(*from))
	}
	if to != nil {
		t = ptr(StartOfDay(*to))
	}
	return s.Q.ListPriceHistory(ctx, marketdatadb.ListPriceHistoryParams{Symbol: symbol, FromDate: f, ToDate: t})
}

func priceBarFromRow(r marketdatadb.ListPriceHistoryRow) PriceBarResponse {
	var vol *int
	if r.Volume != nil {
		vol = ptr(int(*r.Volume))
	}
	return PriceBarResponse{Date: FormatDateOnly(r.Date), Open: r.Open, High: r.High, Low: r.Low, Close: r.Close, Volume: vol}
}

func priceBarsFromRows(rows []marketdatadb.ListPriceHistoryRow) []PriceBarResponse {
	out := make([]PriceBarResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, priceBarFromRow(r))
	}
	return out
}

func priceBarsFromProvider(bars []PriceBar) []PriceBarResponse {
	out := make([]PriceBarResponse, 0, len(bars))
	for _, b := range bars {
		out = append(out, PriceBarResponse{Date: FormatDateOnly(StartOfDay(b.Date)), Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume})
	}
	return out
}

func (s *Service) isHistoryFresh(rows []marketdatadb.ListPriceHistoryRow, now time.Time) bool {
	if len(rows) == 0 {
		return false
	}
	return isFresh(&rows[len(rows)-1].Date, s.Config.HistoryTTL, now)
}

func historyKey(provider, symbol string, from, to *time.Time) string {
	part := func(t *time.Time) string {
		if t == nil {
			return "none"
		}
		return FormatDateOnly(*t)
	}
	return "market:history:" + provider + ":" + symbol + ":" + part(from) + ":" + part(to)
}

func parseHistoryRange(rawFrom, rawTo *string) (from, to *time.Time, err error) {
	if from, err = parseOptionalDateOnly(rawFrom, "from"); err != nil {
		return nil, nil, err
	}
	if to, err = parseOptionalDateOnly(rawTo, "to"); err != nil {
		return nil, nil, err
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, badRequest("`from` must be on or before `to`.")
	}
	return from, to, nil
}

// History is history(symbol:from:to:).
func (s *Service) History(ctx context.Context, rawSymbol string, rawFrom, rawTo *string) (HistoryResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return HistoryResponse{}, err
	}
	from, to, err := parseHistoryRange(rawFrom, rawTo)
	if err != nil {
		return HistoryResponse{}, err
	}
	now := s.now()
	providerName := s.Provider.Name()
	key := historyKey(providerName, symbol, from, to)
	ttl := s.Config.HistoryTTL

	var hot HistoryResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	cached, err := s.loadCachedHistory(ctx, symbol, from, to)
	if err != nil {
		return HistoryResponse{}, err
	}
	if s.isHistoryFresh(cached, now) {
		resp := HistoryResponse{Symbol: symbol, Currency: s.Config.DefaultCurrency, Bars: priceBarsFromRows(cached)}
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	resp, err := s.fetchAndStoreHistory(ctx, symbol, from, to)
	if err == nil {
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	if len(cached) > 0 {
		s.logger().Warn("market.history stale fallback", "symbol", symbol, "provider", providerName)
		resp := HistoryResponse{Symbol: symbol, Currency: s.Config.DefaultCurrency, Bars: priceBarsFromRows(cached)}
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	return HistoryResponse{}, mapProviderError(err, "history")
}

func (s *Service) fetchAndStoreHistory(ctx context.Context, symbol string, from, to *time.Time) (HistoryResponse, error) {
	fresh, err := s.Provider.History(ctx, symbol, from, to)
	if err != nil {
		return HistoryResponse{}, err
	}
	if err := s.upsertHistoryBars(ctx, symbol, fresh.Currency, s.Provider.Name(), fresh.Bars); err != nil {
		return HistoryResponse{}, err
	}
	merged, err := s.loadCachedHistory(ctx, symbol, from, to)
	if err != nil {
		return HistoryResponse{}, err
	}
	bars := priceBarsFromRows(merged)
	if len(merged) == 0 {
		bars = priceBarsFromProvider(fresh.Bars)
	}
	return HistoryResponse{Symbol: symbol, Currency: fresh.Currency, Bars: bars}, nil
}

// ArchivedHistory is archivedHistory: stored bars only.
func (s *Service) ArchivedHistory(ctx context.Context, rawSymbol string, rawFrom, rawTo *string) (HistoryResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return HistoryResponse{}, err
	}
	from, to, err := parseHistoryRange(rawFrom, rawTo)
	if err != nil {
		return HistoryResponse{}, err
	}
	rows, err := s.loadCachedHistory(ctx, symbol, from, to)
	if err != nil {
		return HistoryResponse{}, err
	}
	return HistoryResponse{Symbol: symbol, Currency: s.Config.DefaultCurrency, Bars: priceBarsFromRows(rows)}, nil
}

// RefreshHistory is refreshHistory: provider fetch and store, no caches and
// no error mapping.
func (s *Service) RefreshHistory(ctx context.Context, rawSymbol string, rawFrom, rawTo *string) (HistoryResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return HistoryResponse{}, err
	}
	from, to, err := parseHistoryRange(rawFrom, rawTo)
	if err != nil {
		return HistoryResponse{}, err
	}
	return s.fetchAndStoreHistory(ctx, symbol, from, to)
}

func (s *Service) upsertHistoryBars(ctx context.Context, symbol, currency, provider string, bars []PriceBar) error {
	for _, b := range bars {
		var vol *int64
		if b.Volume != nil {
			vol = ptr(int64(*b.Volume))
		}
		if err := s.Q.UpsertPriceHistory(ctx, marketdatadb.UpsertPriceHistoryParams{
			ID: uuid.New(), Symbol: symbol, Date: StartOfDay(b.Date),
			Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: vol,
		}); err != nil {
			return err
		}
	}
	// Best-effort dual-write into the canonical scenario store.
	if err := s.UpsertMarketPriceBars(ctx, symbol, currency, provider, priceBarsFromProvider(bars)); err != nil {
		s.logger().Warn("market history canonical dual-write failed", "symbol", symbol, "error", err)
	}
	return nil
}

// UpsertMarketPriceBars is MarketPriceBarRepository.upsert.
func (s *Service) UpsertMarketPriceBars(ctx context.Context, instrumentKey, currency, provider string, bars []PriceBarResponse) error {
	for _, b := range bars {
		day, err := time.Parse("2006-01-02", b.Date)
		if err != nil {
			return badRequest("Invalid price bar date " + b.Date + ".")
		}
		var vol *float64
		if b.Volume != nil {
			vol = ptr(float64(*b.Volume))
		}
		if err := s.Q.UpsertMarketPriceBar(ctx, marketdatadb.UpsertMarketPriceBarParams{
			InstrumentKey: strings.ToUpper(instrumentKey), Date: day,
			Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: vol,
			Currency: strings.ToUpper(currency), Provider: provider,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ---- Search ---------------------------------------------------------------

// Search is search(query:).
func (s *Service) Search(ctx context.Context, rawQuery string) ([]SearchResultResponse, error) {
	query := strings.ToUpper(strings.TrimSpace(rawQuery))
	if query == "" {
		return nil, badRequest("Query parameter `q` is required.")
	}
	now := s.now()
	providerName := s.Provider.Name()
	key := "market:search:" + providerName + ":" + query
	ttl := s.Config.SearchTTL

	var hot []SearchResultResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	var existing *marketdatadb.GetSearchCacheRow
	row, err := s.Q.GetSearchCache(ctx, marketdatadb.GetSearchCacheParams{Provider: providerName, NormalizedQuery: query})
	switch {
	case err == nil:
		existing = &row
	case !noRows(err):
		return nil, err
	}
	if existing != nil && isFresh(firstTime(existing.UpdatedAt, &existing.CreatedAt), ttl, now) {
		if cached, ok := decodePayload[[]SearchResultResponse](existing.Payload); ok {
			s.Cache.Set(ctx, key, cached, ttl)
			return cached, nil
		}
	}
	mapped, err := s.fetchSearch(ctx, query, providerName)
	if err == nil {
		s.Cache.Set(ctx, key, mapped, ttl)
		return mapped, nil
	}
	if existing != nil {
		if cached, ok := decodePayload[[]SearchResultResponse](existing.Payload); ok {
			s.logger().Warn("market.search stale fallback", "query", query, "provider", providerName)
			s.Cache.Set(ctx, key, cached, ttl)
			return cached, nil
		}
	}
	return nil, mapProviderError(err, "search")
}

func (s *Service) fetchSearch(ctx context.Context, query, providerName string) ([]SearchResultResponse, error) {
	fresh, err := s.Provider.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	mapped := make([]SearchResultResponse, 0, len(fresh))
	for _, r := range fresh {
		mapped = append(mapped, SearchResultResponse{Symbol: r.Symbol, Name: r.Name, Exchange: r.Exchange, Currency: r.Currency, Conid: r.Conid})
	}
	payload, err := apijson.Marshal(mapped)
	if err != nil {
		return nil, err
	}
	if err := s.Q.UpsertSearchCache(ctx, marketdatadb.UpsertSearchCacheParams{
		ID: uuid.New(), Provider: providerName, NormalizedQuery: query, Payload: string(payload),
	}); err != nil {
		return nil, err
	}
	return mapped, nil
}

// decodePayload is the decode*Payload helpers: Swift's strict JSONDecoder on
// a cache row, nil on any failure.
func decodePayload[T any](raw string) (T, bool) {
	var out T
	if err := decodeStrict([]byte(raw), &out); err != nil {
		return out, false
	}
	return out, true
}

// ---- FX -------------------------------------------------------------------

func normalizePair(raw string) (base, quote string, err error) {
	compact := strings.TrimSpace(raw)
	compact = strings.ReplaceAll(compact, "/", "")
	compact = strings.ReplaceAll(compact, "-", "")
	compact = strings.ToUpper(compact)
	runes := []rune(compact)
	if len(runes) != 6 {
		return "", "", badRequest("Invalid FX pair. Use `EURUSD` or `EUR/USD`.")
	}
	return string(runes[:3]), string(runes[3:]), nil
}

// Fx is fx(pair:).
func (s *Service) Fx(ctx context.Context, rawPair string) (FxRateResponse, error) {
	base, quote, err := normalizePair(rawPair)
	if err != nil {
		return FxRateResponse{}, err
	}
	now := s.now()
	key := "market:fx:" + s.Provider.Name() + ":" + base + quote
	ttl := s.Config.FxTTL

	var hot FxRateResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	var existing *marketdatadb.LatestFxRateRow
	row, err := s.Q.LatestFxRate(ctx, marketdatadb.LatestFxRateParams{Base: base, Quote: quote})
	switch {
	case err == nil:
		existing = &row
	case !noRows(err):
		return FxRateResponse{}, err
	}
	toResp := func(r marketdatadb.LatestFxRateRow) FxRateResponse {
		return FxRateResponse{Base: r.Base, Quote: r.Quote, Rate: r.Rate, Date: FormatDateOnly(r.Date)}
	}
	if existing != nil && isFresh(&existing.Date, ttl, now) {
		resp := toResp(*existing)
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	fresh, err := s.Provider.Fx(ctx, base, quote)
	if err == nil {
		var saved marketdatadb.UpsertFxRateRow
		saved, err = s.Q.UpsertFxRate(ctx, marketdatadb.UpsertFxRateParams{
			ID: uuid.New(), Date: StartOfDay(fresh.AsOf), Base: fresh.Base, Quote: fresh.Quote, Rate: fresh.Rate,
		})
		if err == nil {
			resp := toResp(marketdatadb.LatestFxRateRow(saved))
			s.Cache.Set(ctx, key, resp, ttl)
			return resp, nil
		}
	}
	if existing != nil {
		s.logger().Warn("market.fx stale fallback", "pair", base+quote, "provider", s.Provider.Name())
		resp := toResp(*existing)
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	return FxRateResponse{}, mapProviderError(err, "fx")
}

// ---- Profile --------------------------------------------------------------

func profileResponseFromRow(r marketdatadb.GetProfileCacheRow) CompanyProfileResponse {
	return CompanyProfileResponse{
		Country: r.Country, Currency: r.Currency, EstimateCurrency: r.EstimateCurrency, Exchange: r.Exchange,
		FinnhubIndustry: r.FinnhubIndustry, IPO: r.Ipo, Logo: r.Logo, MarketCapitalization: r.MarketCapitalization,
		Name: r.Name, Phone: r.Phone, ShareOutstanding: r.ShareOutstanding, Ticker: r.Ticker, Weburl: r.Weburl,
	}
}

// Profile is profile(symbol:).
func (s *Service) Profile(ctx context.Context, rawSymbol string) (CompanyProfileResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return CompanyProfileResponse{}, err
	}
	now := s.now()
	providerName := s.Provider.Name()
	key := "market:profile:" + providerName + ":" + symbol
	ttl := s.Config.ProfileTTL

	var hot CompanyProfileResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	var existing *marketdatadb.GetProfileCacheRow
	row, err := s.Q.GetProfileCache(ctx, marketdatadb.GetProfileCacheParams{Provider: providerName, Symbol: symbol})
	switch {
	case err == nil:
		existing = &row
	case !noRows(err):
		return CompanyProfileResponse{}, err
	}
	if existing != nil && isFresh(firstTime(existing.UpdatedAt, existing.CreatedAt), ttl, now) {
		resp := profileResponseFromRow(*existing)
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	resp, err := s.fetchProfile(ctx, symbol, providerName)
	if err == nil {
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	if existing != nil {
		s.logger().Warn("market.profile stale fallback", "symbol", symbol, "provider", providerName)
		resp := profileResponseFromRow(*existing)
		s.Cache.Set(ctx, key, resp, ttl)
		return resp, nil
	}
	return CompanyProfileResponse{}, mapProviderError(err, "profile")
}

func (s *Service) fetchProfile(ctx context.Context, symbol, providerName string) (CompanyProfileResponse, error) {
	fresh, err := s.Provider.Profile(ctx, symbol)
	if err != nil {
		return CompanyProfileResponse{}, err
	}
	if fresh == nil {
		return CompanyProfileResponse{}, notFound("Profile not found for " + symbol + ".")
	}
	saved, err := s.Q.UpsertProfileCache(ctx, marketdatadb.UpsertProfileCacheParams{
		ID: uuid.New(), Provider: providerName, Symbol: fresh.Symbol, Country: fresh.Country, Currency: fresh.Currency,
		EstimateCurrency: fresh.EstimateCurrency, Exchange: fresh.Exchange, FinnhubIndustry: fresh.FinnhubIndustry,
		Ipo: fresh.IPO, Logo: fresh.Logo, MarketCapitalization: fresh.MarketCapitalization, Name: fresh.Name,
		Phone: fresh.Phone, ShareOutstanding: fresh.ShareOutstanding, Ticker: fresh.Ticker, Weburl: fresh.Weburl,
	})
	if err != nil {
		return CompanyProfileResponse{}, err
	}
	return profileResponseFromRow(marketdatadb.GetProfileCacheRow(saved)), nil
}

// ---- Basic financials -------------------------------------------------------

// BasicFinancials is basicFinancials(symbol:). Concurrent misses for one
// symbol share a single upstream call.
func (s *Service) BasicFinancials(ctx context.Context, rawSymbol string) (BasicFinancialsResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return BasicFinancialsResponse{}, err
	}
	now := s.now()
	providerName := s.Provider.Name()
	key := "market:basic-financials:" + providerName + ":" + symbol
	ttl := s.Config.BasicFinancialsTTL

	var hot BasicFinancialsResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	var existing *marketdatadb.GetBasicFinancialsCacheRow
	row, err := s.Q.GetBasicFinancialsCache(ctx, marketdatadb.GetBasicFinancialsCacheParams{Provider: providerName, Symbol: symbol})
	switch {
	case err == nil:
		existing = &row
	case !noRows(err):
		return BasicFinancialsResponse{}, err
	}
	if existing != nil && isFresh(firstTime(existing.UpdatedAt, existing.CreatedAt), ttl, now) {
		if cached, ok := decodePayload[BasicFinancialsResponse](existing.Payload); ok {
			s.Cache.Set(ctx, key, cached, ttl)
			return cached, nil
		}
	}
	v, err, _ := s.basicFinancialsFlight.Do(providerName+":"+symbol, func() (any, error) {
		return s.fetchBasicFinancials(ctx, symbol, providerName, key)
	})
	if err == nil {
		return v.(BasicFinancialsResponse), nil
	}
	if existing != nil {
		if cached, ok := decodePayload[BasicFinancialsResponse](existing.Payload); ok {
			s.logger().Warn("market.basic-financials stale fallback", "symbol", symbol, "provider", providerName)
			s.Cache.Set(ctx, key, cached, ttl)
			return cached, nil
		}
	}
	return BasicFinancialsResponse{}, mapProviderError(err, "basic financials")
}

func (s *Service) fetchBasicFinancials(ctx context.Context, symbol, providerName, key string) (BasicFinancialsResponse, error) {
	fresh, err := s.Provider.BasicFinancials(ctx, symbol)
	if err != nil {
		return BasicFinancialsResponse{}, err
	}
	if fresh == nil {
		return BasicFinancialsResponse{}, notFound("Basic financials not found for " + symbol + ".")
	}
	resp := BasicFinancialsResponse{Symbol: fresh.Symbol, MetricType: fresh.MetricType, Metric: fresh.Metric, Series: fresh.Series}
	payload, err := apijson.Marshal(resp)
	if err != nil {
		return BasicFinancialsResponse{}, err
	}
	if _, err := s.Q.UpsertBasicFinancialsCache(ctx, marketdatadb.UpsertBasicFinancialsCacheParams{
		ID: uuid.New(), Provider: providerName, Symbol: resp.Symbol, Payload: string(payload),
	}); err != nil {
		return BasicFinancialsResponse{}, err
	}
	s.Cache.Set(ctx, key, resp, s.Config.BasicFinancialsTTL)
	return resp, nil
}
