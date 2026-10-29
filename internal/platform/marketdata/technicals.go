package marketdata

import (
	"context"
	"math"
	"sort"
	"strings"
)

const (
	technicalsMaxSessions     = 300
	technicalsFiftyTwoWeek    = 252
	technicalsCrossLookback   = 10
	technicalsHistoryLookback = 600
)

// ComputeTechnicals is TechnicalSignals.compute; nil when there are no bars.
func ComputeTechnicals(symbol string, bars []PriceBarResponse) *TechnicalSignalsResponse {
	window := append([]PriceBarResponse(nil), bars...)
	sort.SliceStable(window, func(i, j int) bool { return window[i].Date < window[j].Date })
	if len(window) > technicalsMaxSessions {
		window = window[len(window)-technicalsMaxSessions:]
	}
	if len(window) == 0 {
		return nil
	}
	last := window[len(window)-1]
	closes := make([]float64, len(window))
	for i, b := range window {
		closes[i] = b.Close
	}
	fast := MovingAverageSeries(closes, 50)
	slow := MovingAverageSeries(closes, 200)
	sma50, sma200 := fast[len(fast)-1], slow[len(slow)-1]
	return &TechnicalSignalsResponse{
		Symbol:       strings.ToUpper(strings.TrimSpace(symbol)),
		AsOf:         last.Date,
		Close:        last.Close,
		SMA50:        sma50,
		SMA200:       sma200,
		Trend:        technicalTrend(last.Close, sma50, sma200),
		Cross:        technicalCross(fast, slow),
		RSI14:        WilderRSI(closes, 14),
		MACD:         technicalMACD(closes),
		FiftyTwoWeek: fiftyTwoWeek(window, last.Close),
	}
}

// MovingAverageSeries is the trailing simple moving average; nil until a
// full period is available.
func MovingAverageSeries(values []float64, period int) []*float64 {
	series := make([]*float64, len(values))
	if period <= 0 {
		return series
	}
	running := 0.0
	for i, v := range values {
		running += v
		if i >= period {
			running -= values[i-period]
		}
		if i >= period-1 {
			series[i] = ptr(running / float64(period))
		}
	}
	return series
}

// ExponentialMovingAverageSeries is seeded with the first period's SMA.
func ExponentialMovingAverageSeries(values []float64, period int) []*float64 {
	series := make([]*float64, len(values))
	if period <= 0 || len(values) < period {
		return series
	}
	prev := 0.0
	for _, v := range values[:period] {
		prev += v
	}
	prev /= float64(period)
	series[period-1] = ptr(prev)
	mult := 2.0 / (float64(period) + 1.0)
	for i := period; i < len(values); i++ {
		prev = (values[i]-prev)*mult + prev
		series[i] = ptr(prev)
	}
	return series
}

func technicalTrend(closeValue float64, sma50, sma200 *float64) string {
	if sma50 == nil || sma200 == nil {
		return "mixed"
	}
	if closeValue > *sma50 && closeValue > *sma200 {
		return "aboveBoth"
	}
	if closeValue < *sma50 && closeValue < *sma200 {
		return "belowBoth"
	}
	return "mixed"
}

func technicalCross(fast, slow []*float64) string {
	n := min(len(fast), len(slow))
	spreads := make([]*float64, n)
	for i := range n {
		if fast[i] != nil && slow[i] != nil {
			spreads[i] = ptr(*fast[i] - *slow[i])
		}
	}
	if n < 2 {
		return "none"
	}
	oldest := max(1, n-technicalsCrossLookback)
	for i := n - 1; i >= oldest; i-- {
		cur, prev := spreads[i], spreads[i-1]
		if cur == nil || prev == nil {
			continue
		}
		if *prev <= 0 && *cur > 0 {
			return "goldenCross"
		}
		if *prev > 0 && *cur <= 0 {
			return "deathCross"
		}
	}
	return "none"
}

// WilderRSI is Wilder's smoothed RSI; nil without period+1 closes.
func WilderRSI(closes []float64, period int) *float64 {
	if period <= 0 || len(closes) <= period {
		return nil
	}
	gains := make([]float64, 0, len(closes)-1)
	losses := make([]float64, 0, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		change := closes[i] - closes[i-1]
		gains = append(gains, math.Max(change, 0))
		losses = append(losses, math.Max(-change, 0))
	}
	avgGain, avgLoss := 0.0, 0.0
	for i := range period {
		avgGain += gains[i]
		avgLoss += losses[i]
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)
	weight := float64(period - 1)
	for i := period; i < len(gains); i++ {
		avgGain = (avgGain*weight + gains[i]) / float64(period)
		avgLoss = (avgLoss*weight + losses[i]) / float64(period)
	}
	if avgLoss <= 0 {
		if avgGain > 0 {
			return ptr(100.0)
		}
		return ptr(50.0)
	}
	return ptr(100 - 100/(1+avgGain/avgLoss))
}

func technicalMACD(closes []float64) *TechnicalMACD {
	fast := ExponentialMovingAverageSeries(closes, 12)
	slow := ExponentialMovingAverageSeries(closes, 26)
	var lines []float64
	for i := range fast {
		if fast[i] != nil && slow[i] != nil {
			lines = append(lines, *fast[i]-*slow[i])
		}
	}
	if len(lines) < 9 {
		return nil
	}
	sig := ExponentialMovingAverageSeries(lines, 9)
	signal := sig[len(sig)-1]
	if signal == nil {
		return nil
	}
	line := lines[len(lines)-1]
	hist := line - *signal
	sign := "negative"
	if hist >= 0 {
		sign = "positive"
	}
	return &TechnicalMACD{Line: line, Signal: *signal, Histogram: hist, Sign: sign}
}

func fiftyTwoWeek(window []PriceBarResponse, closeValue float64) TechnicalFiftyTwoWeek {
	year := window
	if len(year) > technicalsFiftyTwoWeek {
		year = year[len(year)-technicalsFiftyTwoWeek:]
	}
	high, low := closeValue, closeValue
	if len(year) > 0 {
		high, low = year[0].High, year[0].Low
		for _, b := range year[1:] {
			high = math.Max(high, b.High)
			low = math.Min(low, b.Low)
		}
	}
	pos := 50.0
	if high > low {
		pos = math.Min(100, math.Max(0, (closeValue-low)/(high-low)*100))
	}
	return TechnicalFiftyTwoWeek{High: high, Low: low, PositionPct: pos}
}

// Technicals is the technicals route body: cached under
// market:technicals:SYM, computed from ~600 days of daily history.
func (s *Service) Technicals(ctx context.Context, symbol string) (TechnicalSignalsResponse, error) {
	key := "market:technicals:" + symbol
	var resp TechnicalSignalsResponse
	if s.Cache.Get(ctx, key, &resp) {
		return resp, nil
	}
	from := FormatDateOnly(s.now().UTC().AddDate(0, 0, -technicalsHistoryLookback))
	history, err := s.History(ctx, symbol, &from, nil)
	if err != nil {
		return resp, err
	}
	signals := ComputeTechnicals(symbol, history.Bars)
	if signals == nil {
		return resp, notFound("No price history available for " + symbol + ".")
	}
	s.Cache.Set(ctx, key, *signals, s.Config.TechnicalsTTL)
	return *signals, nil
}
