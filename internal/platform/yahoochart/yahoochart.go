// Package yahoochart ports Vapor's two clients for Yahoo's public chart
// endpoint: YahooChartQuoteProvider (index levels for the market brief) and
// YahooDailyBarsProvider (two years of closes for the daily board post).
//
// The endpoint is unofficial and keyless: it can rate-limit or start
// demanding a cookie, so every failure degrades to a missing row or series,
// never an error.
package yahoochart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// DefaultBaseURL is YahooChartQuoteProvider.defaultBaseURL; both providers
// append the percent-encoded symbol to it.
const DefaultBaseURL = "https://query1.finance.yahoo.com/v8/finance/chart/"

// Settings are the environment variables the providers read.
type Settings struct {
	// BaseURL is v2-only, for test stubs and parity runs (Vapor hard-coded
	// the URL). Unset means the real endpoint.
	BaseURL string `env:"YAHOO_CHART_BASE_URL"`
}

// ResolveBaseURL is s.BaseURL, or DefaultBaseURL when unset.
func (s Settings) ResolveBaseURL() string {
	if b := strings.TrimSpace(s.BaseURL); b != "" {
		return b
	}
	return DefaultBaseURL
}

// MaxAge is YahooChartQuoteProvider.maxAge: older than this, a row would
// show a previous session's move as today's. It also drops a market that is
// shut for a holiday.
const MaxAge = 18 * time.Hour

// UserAgent is the Safari user agent Vapor sent.
const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

// requestTimeout is clientRequest.timeout.
const requestTimeout = 8 * time.Second

// Quote is IndexQuote.
type Quote struct {
	Symbol        string
	Price         float64
	PreviousClose float64
	// MarketTime is when the price was last updated at its exchange.
	MarketTime time.Time
	// Currency is the quote currency Yahoo reports ("USD", "EUR"), nil when
	// absent. Index levels are points, but the field still names the market.
	Currency *string
	// DayHigh, DayLow, YearHigh and YearLow are Yahoo's regular-session and
	// 52-week range, nil when absent, non-finite or not positive. Volume is
	// nil when absent or not positive: Yahoo zero-fills it for indices.
	DayHigh, DayLow, YearHigh, YearLow *float64
	Volume                             *int64
}

// ChangePercent is IndexQuote.changePercent.
func (q Quote) ChangePercent() float64 { return (q.Price/q.PreviousClose - 1) * 100 }

// QuoteProvider is IndexQuoteProvider.
type QuoteProvider interface {
	// Quotes never fails. A symbol that fails, is unknown or is stale is
	// left out, so one bad row never costs the whole brief.
	Quotes(ctx context.Context, symbols []string, now time.Time) []Quote
}

// Client is YahooChartQuoteProvider and YahooDailyBarsProvider over one
// base URL.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Logger  *slog.Logger
}

// New builds a client for baseURL ("" means DefaultBaseURL).
func New(baseURL string, client *http.Client, logger *slog.Logger) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if client == nil {
		client = &http.Client{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{BaseURL: baseURL, HTTP: client, Logger: logger}
}

// Quotes implements QuoteProvider. Sequential on purpose: about a dozen
// calls twice a day does not need concurrency, and a burst is what gets an
// IP throttled.
func (c *Client) Quotes(ctx context.Context, symbols []string, now time.Time) []Quote {
	out := []Quote{}
	for _, symbol := range symbols {
		q, err := c.fetchQuote(ctx, symbol)
		switch {
		case err != nil:
			c.Logger.Warn("market_brief_quote_failed", "symbol", symbol, "error", err)
		case q == nil:
			c.Logger.Warn("market_brief_quote_missing", "symbol", symbol)
		case !IsFresh(*q, now):
			c.Logger.Info("market_brief_quote_stale", "symbol", symbol)
		default:
			out = append(out, *q)
		}
	}
	return out
}

// IsFresh is YahooChartQuoteProvider.isFresh.
func IsFresh(q Quote, now time.Time) bool {
	return now.Sub(q.MarketTime) <= MaxAge
}

func (c *Client) fetchQuote(ctx context.Context, symbol string) (*Quote, error) {
	body, err := c.get(ctx, symbol, "range=1d&interval=1d")
	if err != nil || body == nil {
		return nil, err
	}
	return ParseQuote(body)
}

// get fetches the chart for symbol. Like Vapor it ignores the status code:
// Yahoo's 404 carries the JSON error envelope, which parses to nothing. An
// empty body is (nil, nil).
func (c *Client) get(ctx context.Context, symbol, query string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+EncodeSymbol(symbol)+"?"+query, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	return body, nil
}

// EncodeSymbol is addingPercentEncoding(withAllowedCharacters: .alphanumerics):
// every byte that is not an ASCII letter or digit is percent-encoded
// ("^GDAXI" -> "%5EGDAXI", "NQ=F" -> "NQ%3DF").
func EncodeSymbol(symbol string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(symbol); i++ {
		ch := symbol[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[ch>>4])
		b.WriteByte(hex[ch&15])
	}
	return b.String()
}

// errMissingKey is a Swift keyNotFound while decoding the envelope.
var errMissingKey = errors.New("yahoochart: required key missing")

type envelope struct {
	Chart *struct {
		Result []json.RawMessage `json:"result"`
	} `json:"chart"`
}

type quoteMeta struct {
	Symbol             *string  `json:"symbol"`
	RegularMarketPrice *float64 `json:"regularMarketPrice"`
	ChartPreviousClose *float64 `json:"chartPreviousClose"`
	FulldayChange      *float64 `json:"fulldayChange"`
	FulldayPrice       *float64 `json:"fulldayPrice"`
	RegularMarketTime  *float64 `json:"regularMarketTime"`
	Currency           *string  `json:"currency"`
	DayHigh            *float64 `json:"regularMarketDayHigh"`
	DayLow             *float64 `json:"regularMarketDayLow"`
	YearHigh           *float64 `json:"fiftyTwoWeekHigh"`
	YearLow            *float64 `json:"fiftyTwoWeekLow"`
	Volume             *float64 `json:"regularMarketVolume"`
}

// decodeResults decodes the chart envelope into one T per result, failing
// like JSONDecoder when `chart` or any result's meta.symbol is missing.
func decodeResults[T any](data []byte, symbolOf func(*T) *string) ([]T, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Chart == nil {
		return nil, fmt.Errorf("%w: chart", errMissingKey)
	}
	out := make([]T, 0, len(env.Chart.Result))
	for _, raw := range env.Chart.Result {
		var r T
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if symbolOf(&r) == nil {
			return nil, fmt.Errorf("%w: meta.symbol", errMissingKey)
		}
		out = append(out, r)
	}
	return out, nil
}

// ParseQuote is YahooChartQuoteProvider.parse: nil (no error) for Yahoo's
// error envelope or a meta without a positive price, a usable baseline and a
// time.
//
// The baseline is fulldayPrice - fulldayChange, Yahoo's own day change, when
// that is present, non-zero and leaves a positive number. fulldayChange is
// relative to fulldayPrice, which can differ from regularMarketPrice; without
// a positive fulldayPrice, regularMarketPrice stands in. Quote.Price stays
// regularMarketPrice. meta.chartPreviousClose is
// not trusted on its own: on a range=1d chart it can be the close from two
// sessions back (on 2026-11-26 Hang Seng reported 22219.7 against a true
// previous close of 21966.01, so the brief showed +0.14% for a +1.19% day).
// Yahoo zero-fills fields it lacks, so a zero fulldayChange means unknown,
// not flat; it falls back to chartPreviousClose like an absent one.
func ParseQuote(data []byte) (*Quote, error) {
	type result struct {
		Meta *quoteMeta `json:"meta"`
	}
	results, err := decodeResults(data, func(r *result) *string {
		if r.Meta == nil {
			return nil
		}
		return r.Meta.Symbol
	})
	if err != nil || len(results) == 0 {
		return nil, err
	}
	m := results[0].Meta
	if m.RegularMarketPrice == nil || !finitePositive(*m.RegularMarketPrice) || m.RegularMarketTime == nil {
		return nil, nil
	}
	fullday := *m.RegularMarketPrice
	if m.FulldayPrice != nil && finitePositive(*m.FulldayPrice) {
		fullday = *m.FulldayPrice
	}
	var prev float64
	switch {
	case m.FulldayChange != nil && *m.FulldayChange != 0 && finitePositive(fullday-*m.FulldayChange):
		prev = fullday - *m.FulldayChange
	case m.ChartPreviousClose != nil && finitePositive(*m.ChartPreviousClose):
		prev = *m.ChartPreviousClose
	default:
		return nil, nil
	}
	return &Quote{
		Symbol:        *m.Symbol,
		Price:         *m.RegularMarketPrice,
		PreviousClose: prev,
		MarketTime:    unixSeconds(*m.RegularMarketTime),
		Currency:      nonEmpty(m.Currency),
		DayHigh:       positive(m.DayHigh),
		DayLow:        positive(m.DayLow),
		YearHigh:      positive(m.YearHigh),
		YearLow:       positive(m.YearLow),
		Volume:        positiveInt(m.Volume),
	}, nil
}

// positive is v when present, finite and > 0, else nil.
func positive(v *float64) *float64 {
	if v == nil || !finitePositive(*v) {
		return nil
	}
	return v
}

// positiveInt is a whole-number volume when v is present, finite and > 0.
func positiveInt(v *float64) *int64 {
	if v == nil || !finitePositive(*v) || *v >= math.MaxInt64 {
		return nil
	}
	n := int64(math.Round(*v))
	if n <= 0 {
		return nil
	}
	return &n
}

func nonEmpty(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	return s
}

func finitePositive(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) && v > 0 }

// unixSeconds is Date(timeIntervalSince1970:).
func unixSeconds(s float64) time.Time {
	sec, frac := math.Modf(s)
	return time.Unix(int64(sec), int64(math.Round(frac*1e9))).UTC()
}

// Bar is DailyBar: one session's close, dated in the exchange's own time
// zone.
type Bar struct {
	Date  string // yyyy-MM-dd
	Close float64
}

// Series is DailyBarSeries: up to two years of closes, oldest first.
type Series struct {
	Symbol   string
	Currency *string
	Bars     []Bar
}

// Last is the latest bar, or nil.
func (s Series) Last() *Bar {
	if len(s.Bars) == 0 {
		return nil
	}
	return &s.Bars[len(s.Bars)-1]
}

// PercentChangeSessionsBack is percentChange(sessionsBack:): from the close
// `sessions` bars back to the latest close.
func (s Series) PercentChangeSessionsBack(sessions int) *float64 {
	last := s.Last()
	if sessions <= 0 || len(s.Bars) <= sessions || last == nil {
		return nil
	}
	base := s.Bars[len(s.Bars)-1-sessions].Close
	if base <= 0 {
		return nil
	}
	v := (last.Close/base - 1) * 100
	return &v
}

// PercentChangeSinceCloseBefore is percentChange(sinceCloseBefore:): from
// the last close before day (yyyy-MM-dd) to the latest close.
func (s Series) PercentChangeSinceCloseBefore(day string) *float64 {
	last := s.Last()
	if last == nil {
		return nil
	}
	var base *Bar
	for i := len(s.Bars) - 1; i >= 0; i-- {
		if s.Bars[i].Date < day {
			base = &s.Bars[i]
			break
		}
	}
	if base == nil || base.Close <= 0 {
		return nil
	}
	v := (last.Close/base.Close - 1) * 100
	return &v
}

// High52 is the closing high over the year to the latest bar.
func (s Series) High52() *float64 { return s.extreme(func(a, b float64) bool { return a > b }) }

// Low52 is the closing low over the year to the latest bar.
func (s Series) Low52() *float64 { return s.extreme(func(a, b float64) bool { return a < b }) }

func (s Series) extreme(better func(a, b float64) bool) *float64 {
	var out *float64
	for _, b := range s.lastYear() {
		if out == nil || better(b.Close, *out) {
			v := b.Close
			out = &v
		}
	}
	return out
}

func (s Series) lastYear() []Bar {
	last := s.Last()
	if last == nil {
		return nil
	}
	cutoff, ok := dayOneYearBefore(last.Date)
	if !ok {
		return nil
	}
	var out []Bar
	for _, b := range s.Bars {
		if b.Date > cutoff {
			out = append(out, b)
		}
	}
	return out
}

// dayOneYearBefore is Calendar.date(byAdding: .year, value: -1): 29 February
// clamps to the 28th rather than rolling into March.
func dayOneYearBefore(day string) (string, bool) {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return "", false
	}
	y, m, d := t.Date()
	if m == time.February && d == 29 {
		d = 28
	}
	return time.Date(y-1, m, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), true
}

// SeriesProvider is DailyBarsProvider.
type SeriesProvider interface {
	// Series never fails: nil when the symbol fails or has no bars.
	Series(ctx context.Context, symbol string) *Series
}

// Series implements SeriesProvider over two years of daily bars. No
// freshness cutoff: weekend and holiday posts are written from the last
// session on purpose.
func (c *Client) Series(ctx context.Context, symbol string) *Series {
	body, err := c.get(ctx, symbol, "range=2y&interval=1d")
	if err == nil && body != nil {
		var s *Series
		if s, err = ParseSeries(body); err == nil {
			return s
		}
	}
	if err != nil {
		c.Logger.Warn("daily_bars_failed", "symbol", symbol, "error", err)
	}
	return nil
}

// ParseSeries is YahooDailyBarsProvider.parse: closes dated in the
// exchange's time zone (UTC when it is missing or unknown), null and
// non-positive closes dropped, sorted by date.
func ParseSeries(data []byte) (*Series, error) {
	type meta struct {
		Symbol               *string `json:"symbol"`
		Currency             *string `json:"currency"`
		ExchangeTimezoneName *string `json:"exchangeTimezoneName"`
	}
	type result struct {
		Meta       *meta     `json:"meta"`
		Timestamp  []float64 `json:"timestamp"`
		Indicators *struct {
			Quote []struct {
				Close []*float64 `json:"close"`
			} `json:"quote"`
		} `json:"indicators"`
	}
	results, err := decodeResults(data, func(r *result) *string {
		if r.Meta == nil {
			return nil
		}
		return r.Meta.Symbol
	})
	if err != nil || len(results) == 0 {
		return nil, err
	}
	r := results[0]
	if r.Timestamp == nil || r.Indicators == nil || len(r.Indicators.Quote) == 0 || r.Indicators.Quote[0].Close == nil {
		return nil, nil
	}
	closes := r.Indicators.Quote[0].Close
	loc := time.UTC
	if r.Meta.ExchangeTimezoneName != nil {
		if l, ok := loadZone(*r.Meta.ExchangeTimezoneName); ok {
			loc = l
		}
	}
	bars := []Bar{}
	for i := 0; i < len(r.Timestamp) && i < len(closes); i++ {
		v := closes[i]
		if v == nil || !finitePositive(*v) {
			continue
		}
		bars = append(bars, Bar{Date: unixSeconds(r.Timestamp[i]).In(loc).Format("2006-01-02"), Close: *v})
	}
	if len(bars) == 0 {
		return nil, nil
	}
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	return &Series{Symbol: *r.Meta.Symbol, Currency: r.Meta.Currency, Bars: bars}, nil
}

// loadZone is TimeZone(identifier:), which knows neither "" nor "Local".
func loadZone(id string) (*time.Location, bool) {
	if id == "" || id == "Local" {
		return nil, false
	}
	l, err := time.LoadLocation(id)
	return l, err == nil
}
