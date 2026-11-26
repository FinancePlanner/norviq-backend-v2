package yahoochart

import (
	"context"
	"strconv"
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

// IntradayProvider fetches the bars of an explicit window.
type IntradayProvider interface {
	// Intraday never fails: nil when the symbol fails or no bar falls in
	// [from, to].
	Intraday(ctx context.Context, symbol, interval string, from, to time.Time) *Intraday
}

// IntradayInterval is the bar width the market brief charts use: about 80
// points for a US session, light enough to store with every brief.
const IntradayInterval = "5m"

// Intraday implements IntradayProvider over period1/period2, so the window is
// the caller's and not whatever session Yahoo calls "1d".
func (c *Client) Intraday(ctx context.Context, symbol, interval string, from, to time.Time) *Intraday {
	query := "period1=" + strconv.FormatInt(from.Unix(), 10) + "&period2=" + strconv.FormatInt(to.Unix(), 10) + "&interval=" + interval
	body, err := c.get(ctx, symbol, query)
	if err == nil && body != nil {
		var d *Intraday
		if d, err = ParseIntraday(body, interval); err == nil {
			if clipped := clipIntraday(d, from, to); clipped != nil {
				return clipped
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

// clipIntraday keeps the points with from <= Time <= to; nil when none are left.
func clipIntraday(d *Intraday, from, to time.Time) *Intraday {
	if d == nil {
		return nil
	}
	kept := make([]Point, 0, len(d.Points))
	for _, p := range d.Points {
		if !p.Time.Before(from) && !p.Time.After(to) {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	out := *d
	out.Points = kept
	return &out
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
