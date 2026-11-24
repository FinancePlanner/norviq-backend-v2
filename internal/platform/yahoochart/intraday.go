package yahoochart

import (
	"context"
	"time"
)

// Point is one intraday print: a bar's close at its start time.
type Point struct {
	Time  time.Time
	Value float64
}

// Intraday is one session's bars for a symbol, oldest first.
type Intraday struct {
	Symbol string
	// Interval is Yahoo's bar width ("5m").
	Interval string
	// Timezone is the exchange's IANA zone ("America/New_York"), "" when
	// Yahoo does not say.
	Timezone string
	Points   []Point
}

// Last is the latest point, or nil.
func (d Intraday) Last() *Point {
	if len(d.Points) == 0 {
		return nil
	}
	return &d.Points[len(d.Points)-1]
}

// IntradayProvider fetches the current or latest session's bars.
type IntradayProvider interface {
	// Intraday never fails: nil when the symbol fails, has no bars, or its
	// latest bar is older than MaxAge.
	Intraday(ctx context.Context, symbol, interval string, now time.Time) *Intraday
}

// IntradayInterval is the bar width the market brief charts use: about 80
// points for a US session, light enough to store with every brief.
const IntradayInterval = "5m"

// Intraday implements IntradayProvider over range=1d, regular hours only.
func (c *Client) Intraday(ctx context.Context, symbol, interval string, now time.Time) *Intraday {
	body, err := c.get(ctx, symbol, "range=1d&interval="+interval)
	if err == nil && body != nil {
		var d *Intraday
		if d, err = ParseIntraday(body, interval); err == nil {
			if d != nil && IsFresh(Quote{MarketTime: d.Last().Time}, now) {
				return d
			}
			c.Logger.Info("market_brief_intraday_missing", "symbol", symbol)
			return nil
		}
	}
	if err != nil {
		c.Logger.Warn("market_brief_intraday_failed", "symbol", symbol, "error", err)
	}
	return nil
}

// ParseIntraday reads a chart response's timestamps and closes. Null and
// non-positive closes (Yahoo pads gaps with null) are dropped; nil, no
// error, when nothing usable is left.
func ParseIntraday(data []byte, interval string) (*Intraday, error) {
	type meta struct {
		Symbol               *string `json:"symbol"`
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
	if r.Indicators == nil || len(r.Indicators.Quote) == 0 {
		return nil, nil
	}
	closes := r.Indicators.Quote[0].Close
	out := &Intraday{Symbol: *r.Meta.Symbol, Interval: interval}
	if r.Meta.ExchangeTimezoneName != nil {
		if _, ok := loadZone(*r.Meta.ExchangeTimezoneName); ok {
			out.Timezone = *r.Meta.ExchangeTimezoneName
		}
	}
	for i := 0; i < len(r.Timestamp) && i < len(closes); i++ {
		v := closes[i]
		if v == nil || !finitePositive(*v) {
			continue
		}
		out.Points = append(out.Points, Point{Time: unixSeconds(r.Timestamp[i]), Value: *v})
	}
	if len(out.Points) == 0 {
		return nil, nil
	}
	return out, nil
}
