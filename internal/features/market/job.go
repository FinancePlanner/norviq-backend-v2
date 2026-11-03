package market

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/store/marketdatadb"
)

// HistoryJobLockName is the advisory-lock name Vapor's job used.
const HistoryJobLockName = "scenario_history_ingestion"

// HistoryJobSettings are MarketHistoryIngestionJob's environment variables.
type HistoryJobSettings struct {
	ScenarioPlanningEnabled string `env:"SCENARIO_PLANNING_ENABLED"`
	RefreshSeconds          string `env:"SCENARIO_HISTORY_REFRESH_SECONDS"`
}

// HistoryIngestionJob is MarketHistoryIngestionJob: keeps market_price_bars
// filled for every held symbol and risk-profile benchmark proxy. It is a
// no-op unless SCENARIO_PLANNING_ENABLED is true, as in Vapor.
type HistoryIngestionJob struct {
	// Service overrides the environment-built service (tests).
	Service *marketdata.Service
	// InitialDelay defaults to 30s (Vapor's).
	InitialDelay time.Duration
}

// Name implements features.Job.
func (HistoryIngestionJob) Name() string { return "market_history_ingestion" }

// Run implements features.Job.
func (j HistoryIngestionJob) Run(ctx context.Context, d *server.Deps) {
	s, err := config.Load[HistoryJobSettings]()
	if err != nil {
		d.Logger.Error("market_history_ingestion config", "error", err)
		return
	}
	if !config.Bool(s.ScenarioPlanningEnabled, false) {
		return
	}
	interval := 86400
	if n, ok := parseInt(s.RefreshSeconds); ok {
		interval = n
	}
	interval = max(interval, 3600)
	svc := j.Service
	if svc == nil {
		if svc, err = SharedService(d); err != nil {
			d.Logger.Error("market_history_ingestion service", "error", err)
			return
		}
	}
	delay := j.InitialDelay
	if delay == 0 {
		delay = 30 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, HistoryJobLockName, func(ctx context.Context) {
			Sweep(ctx, d, svc)
		})
		timer.Reset(time.Duration(interval) * time.Second)
	}
}

func parseInt(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	return n, err == nil
}

// Sweep is one pass over every held symbol. A rate-limited provider stops
// the sweep; any other per-symbol failure is logged and skipped.
func Sweep(ctx context.Context, d *server.Deps, svc *marketdata.Service) {
	q := marketdatadb.New(d.DB)
	raw, err := q.HistoryIngestionSymbols(ctx)
	if err != nil {
		d.Logger.Warn("scenario_history_ingestion failed", "error", err)
		return
	}
	set := map[string]bool{}
	for _, s := range raw {
		set[s] = true
	}
	symbols := make([]string, 0, len(set))
	for s := range set {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	for _, symbol := range symbols {
		if ctx.Err() != nil {
			return
		}
		if err := ingest(ctx, d, q, svc, symbol, time.Now()); err != nil {
			if rl, ok := marketdata.AsRateLimited(err); ok {
				d.Logger.Warn("scenario_history_ingestion stopped: provider rate limited", "symbol", symbol, "retry_after", rl.RetryAfterSeconds)
				return
			}
			d.Logger.Warn("scenario_history_ingestion failed", "symbol", symbol, "error", err)
		}
	}
}

// FetchStart is MarketHistoryIngestionJob.fetchStart: the day after the
// last stored bar, or the 30-year backfill start when there is none.
func FetchStart(lastDate *time.Time, fallbackStart time.Time) time.Time {
	if lastDate == nil {
		return fallbackStart
	}
	return marketdata.StartOfDay(*lastDate).AddDate(0, 0, 1)
}

func ingest(ctx context.Context, d *server.Deps, q *marketdatadb.Queries, svc *marketdata.Service, symbol string, now time.Time) error {
	today := marketdata.StartOfDay(now)
	fallbackStart := today.AddDate(-30, 0, 0)
	dates, err := q.MarketPriceBarLastDate(ctx, marketdatadb.MarketPriceBarLastDateParams{InstrumentKey: symbol, FromDate: fallbackStart, ToDate: today})
	if err != nil {
		return err
	}
	var last *time.Time
	if len(dates) > 0 {
		last = &dates[0]
	}
	start := FetchStart(last, fallbackStart)
	if start.After(today) {
		return nil
	}
	from, to := marketdata.FormatDateOnly(start), marketdata.FormatDateOnly(today)
	resp, err := svc.RefreshHistory(ctx, symbol, &from, &to)
	if err != nil {
		return err
	}
	bars := resp.Bars
	provider := "market"
	if len(bars) == 0 && svc.FMP != nil {
		points, err := svc.FMP.StockHistoricalEOD(ctx, symbol, &from, &to)
		if err != nil {
			return err
		}
		for _, p := range points {
			var vol *int
			if p.Volume != nil {
				v := int(*p.Volume)
				vol = &v
			}
			bars = append(bars, marketdata.PriceBarResponse{Date: p.Date, Open: p.Price, High: p.Price, Low: p.Price, Close: p.Price, Volume: vol})
		}
		provider = svc.FMP.Name()
	}
	if len(bars) == 0 {
		d.Logger.Warn("scenario_history_ingestion returned no bars", "symbol", symbol)
		return nil
	}
	return svc.UpsertMarketPriceBars(ctx, symbol, resp.Currency, provider, bars)
}
