package boards

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// DailyBarsProvider.swift: up to two years of daily closes from Yahoo's chart
// endpoint, and the returns the ETF and week-recap posts work out from them.

// DailyBar is one session's close, dated in the exchange's own time zone.
type DailyBar struct {
	Date  string // yyyy-MM-dd
	Close float64
}

// DailyBarSeries is closes oldest first.
type DailyBarSeries struct {
	Symbol   string
	Currency *string
	Bars     []DailyBar
}

// Last is the latest bar.
func (s DailyBarSeries) Last() (DailyBar, bool) {
	if len(s.Bars) == 0 {
		return DailyBar{}, false
	}
	return s.Bars[len(s.Bars)-1], true
}

// PercentChangeSessions is percentChange(sessionsBack:): from the close
// sessions bars back to the latest close.
func (s DailyBarSeries) PercentChangeSessions(sessions int) *float64 {
	last, ok := s.Last()
	if sessions <= 0 || len(s.Bars) <= sessions || !ok {
		return nil
	}
	base := s.Bars[len(s.Bars)-1-sessions].Close
	if base <= 0 {
		return nil
	}
	return f64((last.Close/base - 1) * 100)
}

// PercentChangeSince is percentChange(sinceCloseBefore:): from the last close
// before day to the latest close.
func (s DailyBarSeries) PercentChangeSince(day string) *float64 {
	last, ok := s.Last()
	if !ok {
		return nil
	}
	for i := len(s.Bars) - 1; i >= 0; i-- {
		if s.Bars[i].Date < day {
			if s.Bars[i].Close <= 0 {
				return nil
			}
			return f64((last.Close/s.Bars[i].Close - 1) * 100)
		}
	}
	return nil
}

// High52 is the closing high over the year to the latest bar.
func (s DailyBarSeries) High52() *float64 {
	year := s.lastYear()
	if len(year) == 0 {
		return nil
	}
	high := year[0].Close
	for _, b := range year[1:] {
		high = math.Max(high, b.Close)
	}
	return &high
}

// Low52 is the closing low over the year to the latest bar.
func (s DailyBarSeries) Low52() *float64 {
	year := s.lastYear()
	if len(year) == 0 {
		return nil
	}
	low := year[0].Close
	for _, b := range year[1:] {
		low = math.Min(low, b.Close)
	}
	return &low
}

func (s DailyBarSeries) lastYear() []DailyBar {
	last, ok := s.Last()
	if !ok {
		return nil
	}
	cutoff, ok := dayOneYearBefore(last.Date)
	if !ok {
		return nil
	}
	out := []DailyBar{}
	for _, b := range s.Bars {
		if b.Date > cutoff {
			out = append(out, b)
		}
	}
	return out
}

// dayOneYearBefore is Calendar.date(byAdding: .year, value: -1): Feb 29
// clamps to Feb 28, where Go's AddDate would roll over to March 1.
func dayOneYearBefore(day string) (string, bool) {
	t, ok := dailyDate(day)
	if !ok {
		return "", false
	}
	y, m, d := t.Date()
	earlier := time.Date(y-1, m, d, 0, 0, 0, 0, time.UTC)
	if earlier.Month() != m {
		earlier = time.Date(y-1, m+1, 0, 0, 0, 0, 0, time.UTC)
	}
	return earlier.Format(dailyDayLayout), true
}

// DefaultYahooChartBaseURL is YahooChartQuoteProvider.defaultBaseURL.
const DefaultYahooChartBaseURL = "https://query1.finance.yahoo.com/v8/finance/chart/"

// yahooUserAgent is YahooChartQuoteProvider.userAgent.
const yahooUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

// YahooDailyBars is YahooDailyBarsProvider: two years of daily bars per
// symbol. Never fails: an error or an empty chart is nil.
type YahooDailyBars struct {
	// BaseURL defaults to DefaultYahooChartBaseURL. Vapor hard-coded it; v2
	// reads YAHOO_CHART_BASE_URL (v2-only) so tests can point at a stub.
	BaseURL string
	Client  *http.Client
	Logger  *slog.Logger
}

// Series is series(symbol:on:).
func (y YahooDailyBars) Series(ctx context.Context, symbol string) *DailyBarSeries {
	base := y.BaseURL
	if base == "" {
		base = DefaultYahooChartBaseURL
	}
	client := y.Client
	if client == nil {
		client = &http.Client{}
	}
	logger := y.Logger
	if logger == nil {
		logger = slog.Default()
	}
	fail := func(err error) *DailyBarSeries {
		logger.Warn("daily_bars_failed", "symbol", symbol, "error", err.Error())
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+yahooEncode(symbol)+"?range=2y&interval=1d", http.NoBody)
	if err != nil {
		return fail(err)
	}
	req.Header.Set("User-Agent", yahooUserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Vapor read the body whatever the status: an error page that is not a
	// chart fails to decode, and a chart without a result is nil.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fail(err)
	}
	series, err := ParseYahooDailyBars(body)
	if err != nil {
		return fail(err)
	}
	return series
}

// yahooEncode is addingPercentEncoding(withAllowedCharacters: .alphanumerics).
func yahooEncode(symbol string) string {
	var b strings.Builder
	for _, c := range []byte(symbol) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

type yahooEnvelope struct {
	Chart *struct {
		Result []struct {
			Meta *struct {
				Symbol               *string `json:"symbol"`
				Currency             *string `json:"currency"`
				ExchangeTimezoneName *string `json:"exchangeTimezoneName"`
			} `json:"meta"`
			Timestamp  []float64 `json:"timestamp"`
			Indicators *struct {
				Quote []struct {
					Close []*float64 `json:"close"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
	} `json:"chart"`
}

// ParseYahooDailyBars is YahooDailyBarsProvider.parse: nil without bars, an
// error when the envelope does not decode (meta.symbol is required).
func ParseYahooDailyBars(data []byte) (*DailyBarSeries, error) {
	var env yahooEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Chart == nil {
		return nil, fmt.Errorf("yahoo chart: no chart key")
	}
	for _, r := range env.Chart.Result {
		if r.Meta == nil || r.Meta.Symbol == nil {
			return nil, fmt.Errorf("yahoo chart: result without meta.symbol")
		}
	}
	if len(env.Chart.Result) == 0 {
		return nil, nil
	}
	result := env.Chart.Result[0]
	if result.Timestamp == nil || result.Indicators == nil || len(result.Indicators.Quote) == 0 || result.Indicators.Quote[0].Close == nil {
		return nil, nil
	}
	loc := time.UTC
	// TimeZone(identifier:) is nil for an unknown name; Go would also
	// accept "" and "Local".
	if tz := result.Meta.ExchangeTimezoneName; tz != nil && *tz != "" && *tz != "Local" {
		if l, err := time.LoadLocation(*tz); err == nil {
			loc = l
		}
	}
	closes := result.Indicators.Quote[0].Close
	bars := []DailyBar{}
	for i := 0; i < len(result.Timestamp) && i < len(closes); i++ {
		c := closes[i]
		if c == nil || math.IsNaN(*c) || math.IsInf(*c, 0) || *c <= 0 {
			continue
		}
		sec, frac := math.Modf(result.Timestamp[i])
		at := time.Unix(int64(sec), int64(frac*1e9)).In(loc)
		bars = append(bars, DailyBar{Date: at.Format(dailyDayLayout), Close: *c})
	}
	if len(bars) == 0 {
		return nil, nil
	}
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	return &DailyBarSeries{Symbol: *result.Meta.Symbol, Currency: result.Meta.Currency, Bars: bars}, nil
}
