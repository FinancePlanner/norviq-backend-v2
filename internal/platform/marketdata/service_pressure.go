package marketdata

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pressureInsiderWindowDays = 90
	pressureInsiderFetchLimit = 100
	pressureHistorySessions   = 30
	pressureNotableLimit      = 5
)

// Pressure is marketPressure: relative volume, the day's move and 90-day
// insider flow blended into a 0-100 temperature.
func (s *Service) Pressure(ctx context.Context, rawSymbol string) (MarketPressureResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return MarketPressureResponse{}, err
	}
	key := "market:pressure:v1:" + symbol
	var resp MarketPressureResponse
	if s.Cache.Get(ctx, key, &resp) {
		return resp, nil
	}
	if s.FMP == nil {
		return resp, unavailable("Market pressure requires the FMP provider.")
	}
	now := s.now().UTC()
	var (
		wg                sync.WaitGroup
		quotes            []CryptoQuoteResponse
		quotesErr         error
		history           []CryptoHistoricalLightPoint
		historyErr        error
		insider           *MarketPressureInsider
		fromDay, todayDay = FormatDateOnly(now.AddDate(0, 0, -75)), FormatDateOnly(now)
	)
	wg.Add(3)
	go func() { defer wg.Done(); quotes, quotesErr = s.FMP.StockQuotes(ctx, []string{symbol}) }()
	go func() {
		defer wg.Done()
		history, historyErr = s.FMP.StockHistoricalEOD(ctx, symbol, &fromDay, &todayDay)
	}()
	go func() { defer wg.Done(); insider = s.pressureInsider(ctx, symbol, now) }()
	wg.Wait()
	if quotesErr != nil {
		return MarketPressureResponse{}, quotesErr
	}
	if len(quotes) == 0 {
		return MarketPressureResponse{}, notFound("No quote available for " + symbol + ".")
	}
	quote := quotes[0]
	if historyErr != nil {
		return MarketPressureResponse{}, historyErr
	}
	filtered := make([]CryptoHistoricalLightPoint, 0, len(history))
	for _, p := range history {
		if orDefault(p.Volume, 0) > 0 {
			filtered = append(filtered, p)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Date < filtered[j].Date })

	volumes := make([]float64, 0, len(filtered))
	for _, p := range filtered {
		if p.Volume != nil {
			volumes = append(volumes, *p.Volume)
		}
	}
	todayVolume := 0.0
	switch {
	case quote.Volume != nil:
		todayVolume = *quote.Volume
	case len(volumes) > 0:
		todayVolume = volumes[len(volumes)-1]
	}
	baseline := volumes
	if len(baseline) > pressureHistorySessions {
		baseline = baseline[len(baseline)-pressureHistorySessions:]
	}
	average := 0.0
	if len(baseline) > 0 {
		sum := 0.0
		for _, v := range baseline {
			sum += v
		}
		average = sum / float64(len(baseline))
	}
	relative := 0.0
	if average > 0 {
		relative = todayVolume / average
	}
	temperature := PressureTemperature(relative, quote.ChangePercentage, insider)
	resp = MarketPressureResponse{
		Symbol:      symbol,
		AsOf:        formatISODateTime(s.now()),
		Temperature: temperature,
		Label:       PressureLabel(temperature),
		Volume: MarketPressureVolume{
			Today: todayVolume, Average30d: average,
			Relative: math.Round(relative*100) / 100, ChangePct: quote.ChangePercentage,
		},
		Insider: insider,
		History: relativeVolumeHistory(filtered),
	}
	s.Cache.Set(ctx, key, resp, s.Config.PressureTTL)
	return resp, nil
}

// pressureInsider degrades to nil (the section hides) on any failure.
func (s *Service) pressureInsider(ctx context.Context, symbol string, now time.Time) *MarketPressureInsider {
	trades, err := s.FMP.InsiderTrades(ctx, symbol, 0, pressureInsiderFetchLimit)
	if err != nil {
		s.logger().Warn("market.pressure insider failed", "symbol", symbol, "error", err)
		return nil
	}
	cutoff := FormatDateOnly(now.AddDate(0, 0, -pressureInsiderWindowDays))
	out := &MarketPressureInsider{WindowDays: pressureInsiderWindowDays, Notable: []MarketPressureInsiderTrade{}}
	for _, t := range trades {
		if t.TransactionDate == nil || *t.TransactionDate < cutoff {
			continue
		}
		date := *t.TransactionDate
		shares := orDefault(t.SecuritiesTransacted, 0)
		if shares <= 0 {
			continue
		}
		isBuy := pressureIsBuy(t)
		side := "sell"
		if isBuy {
			out.BuyCount++
			out.NetShares += shares
			side = "buy"
		} else {
			out.SellCount++
			out.NetShares -= shares
		}
		if out.LastActivityAt == nil || date > *out.LastActivityAt {
			out.LastActivityAt = ptr(date)
		}
		name := "Insider"
		if t.ReportingName != nil {
			name = *t.ReportingName
		}
		out.Notable = append(out.Notable, MarketPressureInsiderTrade{Name: name, Role: t.TypeOfOwner, Side: side, Shares: shares, Date: date})
	}
	sort.SliceStable(out.Notable, func(i, j int) bool { return out.Notable[i].Shares > out.Notable[j].Shares })
	if len(out.Notable) > pressureNotableLimit {
		out.Notable = out.Notable[:pressureNotableLimit]
	}
	return out
}

func relativeVolumeHistory(history []CryptoHistoricalLightPoint) []MarketPressureHistoryPoint {
	sessions := history
	if len(sessions) > pressureHistorySessions+1 {
		sessions = sessions[len(sessions)-(pressureHistorySessions+1):]
	}
	points := []MarketPressureHistoryPoint{}
	if len(sessions) <= 1 {
		return points
	}
	for i := 1; i < len(sessions); i++ {
		sum, n := 0.0, 0
		for _, p := range sessions[max(0, i-pressureHistorySessions):i] {
			if p.Volume != nil {
				sum += *p.Volume
				n++
			}
		}
		if n == 0 || sessions[i].Volume == nil {
			continue
		}
		avg := sum / float64(n)
		if avg <= 0 {
			continue
		}
		points = append(points, MarketPressureHistoryPoint{Date: sessions[i].Date, RelativeVolume: math.Round(*sessions[i].Volume/avg*100) / 100})
	}
	return points
}

// PressureTemperature is pressureTemperature: 50 is balanced; direction from
// the day's move, intensity from relative volume, tilted by insider flow.
func PressureTemperature(relativeVolume, changePct float64, insider *MarketPressureInsider) float64 {
	intensity := math.Min(math.Max(relativeVolume, 0), 3) / 3
	direction := math.Max(-1, math.Min(1, changePct/2))
	score := 50 + direction*intensity*35
	if insider != nil && insider.BuyCount+insider.SellCount > 0 {
		if gross := math.Abs(insider.NetShares); gross > 0 {
			tilt := insider.NetShares / gross
			weight := math.Min(float64(insider.BuyCount+insider.SellCount), 10) / 10
			score += tilt * weight * 15
		}
	}
	return math.Min(100, math.Max(0, math.Round(score*10)/10))
}

// PressureLabel is pressureLabel.
func PressureLabel(t float64) string {
	switch {
	case t < 20:
		return "heavy selling"
	case t < 40:
		return "selling"
	case t < 60:
		return "balanced"
	case t < 80:
		return "buying"
	}
	return "heavy buying"
}

func pressureIsBuy(t FMPInsiderTrade) bool {
	if t.AcquisitionOrDisposition != nil {
		a := strings.ToUpper(*t.AcquisitionOrDisposition)
		if strings.HasPrefix(a, "A") {
			return true
		}
		if strings.HasPrefix(a, "D") {
			return false
		}
	}
	typ := ""
	if t.TransactionType != nil {
		typ = strings.ToUpper(*t.TransactionType)
	}
	return strings.HasPrefix(typ, "P") || strings.Contains(typ, "PURCHASE") || strings.Contains(typ, "BUY")
}
