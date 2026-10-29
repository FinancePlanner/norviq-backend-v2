package marketdata

import (
	"context"
	"sort"
	"sync"
	"time"
)

const periodReturnsBatchLimit = 20

// PeriodReturns is periodReturns(symbol:). It never fails on upstream
// trouble: a missing FMP key or an FMP error answers empty windows, and an
// answer without a usable window is not cached.
func (s *Service) PeriodReturns(ctx context.Context, rawSymbol string) (StockPeriodReturnsResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return StockPeriodReturnsResponse{}, err
	}
	return s.loadPeriodReturns(ctx, symbol), nil
}

// PeriodReturnsBatch is periodReturnsBatch: up to 20 symbols, concurrent.
func (s *Service) PeriodReturnsBatch(ctx context.Context, rawSymbols []string) (StockPeriodReturnsBatchResponse, error) {
	normalized := make([]string, 0, len(rawSymbols))
	for _, raw := range rawSymbols {
		sym, err := normalizeSymbol(raw)
		if err != nil {
			return StockPeriodReturnsBatchResponse{}, err
		}
		normalized = append(normalized, sym)
	}
	normalized = uniqued(normalized)
	if len(normalized) == 0 {
		return StockPeriodReturnsBatchResponse{}, badRequest("At least one symbol is required.")
	}
	if len(normalized) > periodReturnsBatchLimit {
		return StockPeriodReturnsBatchResponse{}, badRequest("Period-returns batch supports at most 20 symbols.")
	}
	out := make([]StockPeriodReturnsResponse, len(normalized))
	var wg sync.WaitGroup
	for i, sym := range normalized {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = s.loadPeriodReturns(ctx, sym)
		}()
	}
	wg.Wait()
	return StockPeriodReturnsBatchResponse{Returns: out}, nil
}

func (s *Service) loadPeriodReturns(ctx context.Context, symbol string) StockPeriodReturnsResponse {
	key := "market:returns:fmp:" + symbol
	var cached StockPeriodReturnsResponse
	if s.Cache.Get(ctx, key, &cached) {
		return cached
	}
	empty := StockPeriodReturnsResponse{Symbol: symbol}
	if s.FMP == nil {
		s.logger().Warn("market.returns missing_fmp", "symbol", symbol)
		return empty
	}
	items, err := s.FMP.StockPriceChange(ctx, symbol)
	if err != nil {
		s.logger().Warn("market.returns degraded", "symbol", symbol, "error", err)
		return empty
	}
	resp := empty
	if len(items) > 0 {
		resp.ThreeMonth, resp.SixMonth, resp.YearToDate = items[0].ThreeMonth, items[0].SixMonth, items[0].YearToDate
	}
	if !resp.HasUsableWindows() {
		s.logger().Warn("market.returns skip_cache empty", "symbol", symbol)
		return resp
	}
	s.Cache.Set(ctx, key, resp, s.Config.HistoryTTL)
	return resp
}

// ---- Recent earnings --------------------------------------------------------

// IsReportedEarnings is EarningsStreak.isReported: both EPS actual and estimate.
func IsReportedEarnings(q EarningsResponse) bool { return q.EpsActual != nil && q.EpsEstimated != nil }

// AnnotateEarningsStreaks is EarningsStreak.annotate: input order kept, each
// row carrying the beat/miss run it closes (counted oldest first).
func AnnotateEarningsStreaks(quarters []EarningsResponse) []EarningsResponse {
	idx := make([]int, len(quarters))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return quarters[idx[a]].Date < quarters[idx[b]].Date })
	out := append([]EarningsResponse(nil), quarters...)
	run, runIsBeat := 0, true
	for _, i := range idx {
		q := quarters[i]
		if !IsReportedEarnings(q) {
			out[i].BeatStreak, out[i].MissStreak = 0, 0
			run = 0
			continue
		}
		beat := *q.EpsActual >= *q.EpsEstimated
		if run > 0 && beat == runIsBeat {
			run++
		} else {
			run, runIsBeat = 1, beat
		}
		if runIsBeat {
			out[i].BeatStreak, out[i].MissStreak = run, 0
		} else {
			out[i].BeatStreak, out[i].MissStreak = 0, run
		}
	}
	return out
}

// BuildRecentEarnings is RecentEarnings.build: the four newest reported
// quarters and the earliest unreported one after them (or, with nothing
// reported, the earliest not before today).
func BuildRecentEarnings(symbol string, history []EarningsResponse, today string) RecentEarningsResponse {
	newestFirst := append([]EarningsResponse(nil), history...)
	sort.SliceStable(newestFirst, func(i, j int) bool { return newestFirst[i].Date > newestFirst[j].Date })
	var reported []EarningsResponse
	for _, r := range newestFirst {
		if IsReportedEarnings(r) {
			reported = append(reported, r)
		}
	}
	var scheduled *EarningsResponse
	for i := len(newestFirst) - 1; i >= 0; i-- {
		row := newestFirst[i]
		if IsReportedEarnings(row) {
			continue
		}
		ok := row.Date >= today
		if len(reported) > 0 {
			ok = row.Date > reported[0].Date
		}
		if ok {
			scheduled = &row
			break
		}
	}
	quarter := func(r EarningsResponse, status string) RecentEarningsQuarter {
		return RecentEarningsQuarter{
			Date: r.Date, EpsEstimated: r.EpsEstimated, EpsActual: r.EpsActual, SurprisePercent: r.SurprisePercent,
			HasTranscript: r.HasTranscript, BeatStreak: r.BeatStreak, MissStreak: r.MissStreak, Status: status,
		}
	}
	resp := RecentEarningsResponse{Symbol: symbol, Quarters: []RecentEarningsQuarter{}}
	for i, r := range reported {
		if i == 4 {
			break
		}
		resp.Quarters = append(resp.Quarters, quarter(r, "reported"))
	}
	if scheduled != nil {
		resp.NextScheduled = ptr(quarter(*scheduled, "scheduled"))
	}
	return resp
}

// RecentEarnings is recentEarnings(symbol:): the ungated earnings teaser.
func (s *Service) RecentEarnings(ctx context.Context, rawSymbol string) (RecentEarningsResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return RecentEarningsResponse{}, err
	}
	key := "market:earnings-recent:fmp:" + symbol
	var cached RecentEarningsResponse
	if s.Cache.Get(ctx, key, &cached) {
		return cached, nil
	}
	history, err := s.Earnings(ctx, symbol, nil)
	if err != nil {
		return RecentEarningsResponse{}, err
	}
	resp := BuildRecentEarnings(symbol, AnnotateEarningsStreaks(history), s.now().UTC().Format(time.DateOnly))
	s.Cache.Set(ctx, key, resp, s.Config.RecentEarningsTTL)
	return resp, nil
}
