package marketdata

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultFinnhubBaseURL is FinnhubMarketDataProvider's default base URL.
const DefaultFinnhubBaseURL = "https://finnhub.io/api/v1"

// Finnhub is FinnhubMarketDataProvider.
type Finnhub struct {
	BaseURL         string
	APIKey          string
	DefaultCurrency string
	Client          *http.Client
	Logger          *slog.Logger
}

func (f *Finnhub) Name() string { return "finnhub" }

func (f *Finnhub) Quote(ctx context.Context, rawSymbol string) (Quote, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return Quote{}, err
	}
	var payload finnhubQuotePayload
	if err := f.fetchJSON(ctx, "/quote", [][2]string{{"symbol", symbol}}, &payload); err != nil {
		return Quote{}, err
	}
	currency := f.DefaultCurrency
	if profile, err := f.fetchCompanyProfile(ctx, symbol); err == nil && profile != nil && profile.Currency != nil {
		currency = *profile.Currency
	}
	price := payload.CurrentPrice
	if price == nil {
		price = payload.PreviousClose
	}
	if price == nil {
		return Quote{}, notFound("Finnhub quote response did not include a price for " + symbol + ".")
	}
	asOf := time.Now()
	if payload.Timestamp != nil {
		asOf = timeFromUnixFloat(*payload.Timestamp)
	}
	return Quote{
		Symbol:        symbol,
		Price:         *price,
		Change:        payload.Change,
		PercentChange: payload.PercentChange,
		High:          payload.High,
		Low:           payload.Low,
		Open:          payload.Open,
		PreviousClose: payload.PreviousClose,
		Currency:      fallbackValue(&currency, f.DefaultCurrency),
		AsOf:          asOf,
	}, nil
}

func (f *Finnhub) History(ctx context.Context, rawSymbol string, from, to *time.Time) (History, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return History{}, err
	}
	effectiveTo := time.Now()
	if to != nil {
		effectiveTo = *to
	}
	effectiveFrom := addYears(effectiveTo.UTC(), -1)
	if from != nil {
		effectiveFrom = *from
	}
	var payload finnhubCandlePayload
	fetchErr := f.fetchJSON(ctx, "/stock/candle", [][2]string{
		{"symbol", symbol},
		{"resolution", "D"},
		{"from", strconv.FormatInt(effectiveFrom.Unix(), 10)},
		{"to", strconv.FormatInt(effectiveTo.Unix(), 10)},
	}, &payload)
	currency := f.DefaultCurrency
	if profile, err := f.fetchCompanyProfile(ctx, symbol); err == nil && profile != nil && profile.Currency != nil {
		currency = *profile.Currency
	}
	if fetchErr != nil {
		f.logger().Warn("Finnhub history request failed. Falling back to empty history array.", "symbol", symbol, "error", fetchErr)
		return History{Symbol: symbol, Currency: currency, Bars: []PriceBar{}}, nil
	}
	return f.mapHistory(symbol, &payload, currency), nil
}

func (f *Finnhub) Search(ctx context.Context, rawQuery string) ([]SearchResult, error) {
	query := strings.TrimSpace(rawQuery)
	if query == "" {
		return nil, badRequest("Query is required.")
	}
	var payload finnhubSymbolLookupPayload
	if err := f.fetchJSON(ctx, "/search", [][2]string{{"q", query}}, &payload); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, item := range payload.Result {
		display := ""
		if item.DisplaySymbol != nil {
			display = *item.DisplaySymbol
		}
		symbol := fallbackValue(item.Symbol, display)
		if symbol == "" {
			continue
		}
		displaySymbol := fallbackValue(item.DisplaySymbol, symbol)
		out = append(out, SearchResult{
			Symbol:   strings.ToUpper(symbol),
			Name:     fallbackValue(item.Description, displaySymbol),
			Exchange: inferExchange(symbol, item.Type),
			Currency: f.DefaultCurrency,
			Conid:    displaySymbol,
		})
	}
	return out, nil
}

func (f *Finnhub) Fx(ctx context.Context, rawBase, rawQuote string) (FxRate, error) {
	base, err := normalizeCurrencyCode(rawBase, "base")
	if err != nil {
		return FxRate{}, err
	}
	quote, err := normalizeCurrencyCode(rawQuote, "quote")
	if err != nil {
		return FxRate{}, err
	}
	if base == quote {
		return FxRate{Base: base, Quote: quote, Rate: 1, AsOf: time.Now()}, nil
	}
	var payload finnhubForexRatesPayload
	if err := f.fetchJSON(ctx, "/forex/rates", [][2]string{{"base", base}}, &payload); err != nil {
		return FxRate{}, err
	}
	rate, ok := payload.Quote[quote]
	if payload.Quote == nil || !ok {
		return FxRate{}, notFound("Finnhub FX response did not include a rate for " + base + "/" + quote + ".")
	}
	return FxRate{Base: strings.ToUpper(fallbackValue(payload.Base, base)), Quote: quote, Rate: rate, AsOf: time.Now()}, nil
}

func (f *Finnhub) Profile(ctx context.Context, rawSymbol string) (*CompanyProfile, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	p, err := f.fetchCompanyProfile(ctx, symbol)
	if err != nil || p == nil {
		return nil, err
	}
	return &CompanyProfile{
		Symbol: symbol, Country: p.Country, Currency: p.Currency, EstimateCurrency: p.EstimateCurrency,
		Exchange: p.Exchange, FinnhubIndustry: p.FinnhubIndustry, IPO: p.IPO, Logo: p.Logo,
		MarketCapitalization: p.MarketCapitalization, Name: p.Name, Phone: p.Phone,
		ShareOutstanding: p.ShareOutstanding, Ticker: p.Ticker, Weburl: p.Weburl,
	}, nil
}

func (f *Finnhub) BasicFinancials(ctx context.Context, rawSymbol string) (*BasicFinancials, error) {
	symbol, err := normalizeProviderSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	var payload finnhubBasicFinancialsPayload
	if err := f.fetchJSON(ctx, "/stock/metric", [][2]string{{"symbol", symbol}, {"metric", "all"}}, &payload); err != nil {
		return nil, err
	}
	if payload.Metric == nil && payload.Series == nil && payload.Symbol == nil && payload.MetricType == nil {
		return nil, nil
	}
	metric := payload.Metric
	if metric == nil {
		metric = map[string]MetricValue{}
	}
	series := payload.Series
	if series == nil {
		series = map[string]map[string][]BasicFinancialSeriesPoint{}
	}
	return &BasicFinancials{
		Symbol:     strings.ToUpper(fallbackValue(payload.Symbol, symbol)),
		MetricType: fallbackValue(payload.MetricType, "all"),
		Metric:     metric,
		Series:     series,
	}, nil
}

func (f *Finnhub) logger() *slog.Logger {
	if f.Logger != nil {
		return f.Logger
	}
	return slog.Default()
}

func (f *Finnhub) mapHistory(symbol string, p *finnhubCandlePayload, currency string) History {
	cur := fallbackValue(&currency, f.DefaultCurrency)
	if p.Status != nil && strings.EqualFold(*p.Status, "no_data") {
		return History{Symbol: symbol, Currency: cur, Bars: []PriceBar{}}
	}
	count := min(len(p.Open), len(p.High), len(p.Low), len(p.Close), len(p.Timestamps))
	bars := make([]PriceBar, 0, count)
	for i := range count {
		var volume *int
		if i < len(p.Volume) {
			volume = ptr(int(math.Round(p.Volume[i])))
		}
		bars = append(bars, PriceBar{
			Date: timeFromUnixFloat(p.Timestamps[i]),
			Open: p.Open[i], High: p.High[i], Low: p.Low[i], Close: p.Close[i], Volume: volume,
		})
	}
	sort.SliceStable(bars, func(a, b int) bool { return bars[a].Date.Before(bars[b].Date) })
	return History{Symbol: symbol, Currency: cur, Bars: bars}
}

func (f *Finnhub) fetchCompanyProfile(ctx context.Context, symbol string) (*finnhubCompanyProfilePayload, error) {
	var p finnhubCompanyProfilePayload
	if err := f.fetchJSON(ctx, "/stock/profile2", [][2]string{{"symbol", symbol}}, &p); err != nil {
		return nil, err
	}
	if p.Ticker == nil && p.Name == nil && p.Currency == nil && p.Exchange == nil {
		return nil, nil
	}
	return &p, nil
}

// fetchJSON is FinnhubMarketDataProvider.fetchJSON.
func (f *Finnhub) fetchJSON(ctx context.Context, path string, query [][2]string, out any) error {
	if strings.TrimSpace(f.APIKey) == "" {
		return unavailable("FINNHUB_API_KEY is not configured.")
	}
	u, err := buildURL(f.BaseURL, path, query)
	if err != nil {
		return abort(http.StatusInternalServerError, "Invalid Finnhub base URL configuration.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("X-Finnhub-Token", f.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := doRequest(ctx, f.Client, req, 15*time.Second)
	if err != nil {
		return err
	}
	switch resp.status {
	case http.StatusOK:
		if err := decodeBody(resp.body, out); err != nil {
			return badGateway("Failed to decode Finnhub response for " + path + ".")
		}
		return nil
	case http.StatusNotFound:
		return notFound("Finnhub resource not found for " + path + ".")
	case http.StatusUnauthorized, http.StatusForbidden:
		return badGateway("Finnhub rejected the request. Check FINNHUB_API_KEY.")
	case http.StatusTooManyRequests:
		retryAfter := 30
		if v, err := strconv.Atoi(resp.header.Get("Retry-After")); err == nil {
			retryAfter = v
		}
		f.logger().Warn("Finnhub rate limited; backing off", "path", path, "retry_after", retryAfter)
		return RateLimitedError(retryAfter)
	}
	body := resp.bodyString()
	reason := "Finnhub request failed for " + path + " with status " + strconv.Itoa(resp.status) + "."
	if body != "" {
		reason = "Finnhub request failed for " + path + " with status " + strconv.Itoa(resp.status) + ": " + body
	}
	return badGateway(reason)
}

// buildURL joins base and path and appends the query in order.
func buildURL(base, path string, query [][2]string) (string, error) {
	base = strings.TrimSuffix(base, "/")
	u, err := url.Parse(base + path)
	if err != nil {
		return "", err
	}
	if len(query) > 0 {
		parts := make([]string, 0, len(query))
		for _, kv := range query {
			parts = append(parts, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
		}
		u.RawQuery = strings.Join(parts, "&")
	}
	return u.String(), nil
}

func timeFromUnixFloat(seconds float64) time.Time {
	whole := math.Floor(seconds)
	return time.Unix(int64(whole), int64((seconds-whole)*1e9)).UTC()
}

func normalizeCurrencyCode(raw, field string) (string, error) {
	n := strings.ToUpper(strings.TrimSpace(raw))
	if len([]rune(n)) != 3 {
		return "", badRequest("Invalid " + field + " currency code.")
	}
	return n, nil
}

func inferExchange(symbol string, securityType *string) string {
	if strings.Contains(symbol, ".") {
		parts := strings.Split(symbol, ".")
		if last := parts[len(parts)-1]; last != "" {
			return strings.ToUpper(last)
		}
	}
	t := ""
	if securityType != nil {
		t = strings.TrimSpace(*securityType)
	}
	if t == "" {
		return "UNKNOWN"
	}
	return t
}

type finnhubQuotePayload struct {
	CurrentPrice  *float64 `json:"c,omitempty"`
	Change        *float64 `json:"d,omitempty"`
	PercentChange *float64 `json:"dp,omitempty"`
	High          *float64 `json:"h,omitempty"`
	Low           *float64 `json:"l,omitempty"`
	Open          *float64 `json:"o,omitempty"`
	PreviousClose *float64 `json:"pc,omitempty"`
	Timestamp     *float64 `json:"t,omitempty"`
}

type finnhubCandlePayload struct {
	Close      []float64 `json:"c,omitempty"`
	High       []float64 `json:"h,omitempty"`
	Low        []float64 `json:"l,omitempty"`
	Open       []float64 `json:"o,omitempty"`
	Status     *string   `json:"s,omitempty"`
	Timestamps []float64 `json:"t,omitempty"`
	Volume     []float64 `json:"v,omitempty"`
}

type finnhubSymbolLookupPayload struct {
	Count  *int                      `json:"count,omitempty"`
	Result []finnhubSymbolLookupItem `json:"result,omitempty"`
}

type finnhubSymbolLookupItem struct {
	Description   *string `json:"description,omitempty"`
	DisplaySymbol *string `json:"displaySymbol,omitempty"`
	Symbol        *string `json:"symbol,omitempty"`
	Type          *string `json:"type,omitempty"`
}

type finnhubCompanyProfilePayload struct {
	Country              *string  `json:"country,omitempty"`
	Currency             *string  `json:"currency,omitempty"`
	EstimateCurrency     *string  `json:"estimateCurrency,omitempty"`
	Exchange             *string  `json:"exchange,omitempty"`
	FinnhubIndustry      *string  `json:"finnhubIndustry,omitempty"`
	IPO                  *string  `json:"ipo,omitempty"`
	Logo                 *string  `json:"logo,omitempty"`
	MarketCapitalization *float64 `json:"marketCapitalization,omitempty"`
	Name                 *string  `json:"name,omitempty"`
	Phone                *string  `json:"phone,omitempty"`
	ShareOutstanding     *float64 `json:"shareOutstanding,omitempty"`
	Ticker               *string  `json:"ticker,omitempty"`
	Weburl               *string  `json:"weburl,omitempty"`
}

type finnhubForexRatesPayload struct {
	Base  *string            `json:"base,omitempty"`
	Quote map[string]float64 `json:"quote,omitempty"`
}

// finnhubBasicFinancialsPayload decodes lossily: a metric or series entry
// that is not a supported value is dropped instead of failing the payload.
type finnhubBasicFinancialsPayload struct {
	Metric     map[string]MetricValue
	MetricType *string
	Series     map[string]map[string][]BasicFinancialSeriesPoint
	Symbol     *string
}

func (p *finnhubBasicFinancialsPayload) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["symbol"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &p.Symbol); err != nil {
			return err
		}
	}
	if v, ok := raw["metricType"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &p.MetricType); err != nil {
			return err
		}
	}
	if v, ok := raw["metric"]; ok && string(v) != "null" {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(v, &entries); err != nil {
			return err
		}
		p.Metric = map[string]MetricValue{}
		for k, e := range entries {
			var mv MetricValue
			if json.Unmarshal(e, &mv) == nil {
				p.Metric[k] = mv
			}
		}
	}
	if v, ok := raw["series"]; ok && string(v) != "null" {
		var freqs map[string]json.RawMessage
		if err := json.Unmarshal(v, &freqs); err != nil {
			return err
		}
		p.Series = map[string]map[string][]BasicFinancialSeriesPoint{}
		for freq, fv := range freqs {
			var metrics map[string]json.RawMessage
			if json.Unmarshal(fv, &metrics) != nil {
				continue
			}
			out := map[string][]BasicFinancialSeriesPoint{}
			for name, mv := range metrics {
				var points []BasicFinancialSeriesPoint
				if decodeStrict(mv, &points) == nil && points != nil {
					out[name] = points
				}
			}
			p.Series[freq] = out
		}
	}
	return nil
}
