package market

import (
	"context"
	"math"
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
	CadenceEnabled          string `env:"CADENCE_ENABLED"`
	RefreshSeconds          string `env:"SCENARIO_HISTORY_REFRESH_SECONDS"`
}

// HistoryIngestionJob is MarketHistoryIngestionJob: keeps market_price_bars
// filled for every held symbol, active watchlist symbol (Cadence on) and risk-profile benchmark
// proxy. It is a no-op unless SCENARIO_PLANNING_ENABLED or CADENCE_ENABLED is
// true.
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
	if !config.Bool(s.ScenarioPlanningEnabled, false) && !config.Bool(s.CadenceEnabled, false) {
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

// Sweep is one pass over every held and benchmark symbol, plus the active
// watchlist symbols when CADENCE_ENABLED is true. A rate-limited provider
// stops the sweep; any other per-symbol failure is logged and skipped.
func Sweep(ctx context.Context, d *server.Deps, svc *marketdata.Service) {
	q := marketdatadb.New(d.DB)
	includeWatchlist := false
	if s, err := config.Load[HistoryJobSettings](); err == nil {
		includeWatchlist = config.Bool(s.CadenceEnabled, false)
	}
	raw, err := q.HistoryIngestionSymbols(ctx, includeWatchlist)
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

// divergenceTolerance is the relative close difference above which an anchor
// close and the provider's current bar are considered to disagree.
const divergenceTolerance = 0.005

// HistoryDiverged reports whether any date present in both maps (keys are
// yyyy-MM-dd) differs by more than 0.5%, which is what a stock split in the
// provider's adjusted history looks like against bars stored before it.
func HistoryDiverged(stored, fresh map[string]float64) bool {
	for date, old := range stored {
		cur, ok := fresh[date]
		if !ok || old == 0 {
			continue
		}
		if math.Abs(cur/old-1) > divergenceTolerance {
			return true
		}
	}
	return false
}

type fetchedBars struct {
	bars     []marketdata.PriceBarResponse
	currency string
	// fallback is set when the bars came from the FMP fallback, which the
	// service has not stored; primary bars are already in both history tables.
	fallback string
}

// fetchBars loads bars from the market provider, which stores them in
// price_history and market_price_bars and returns only what it sent, never
// stored rows. When it sent none it falls back to FMP end of day data.
func fetchBars(ctx context.Context, svc *marketdata.Service, symbol string, start, today time.Time) (fetchedBars, error) {
	from, to := marketdata.FormatDateOnly(start), marketdata.FormatDateOnly(today)
	resp, err := svc.FetchAndStoreProviderHistory(ctx, symbol, &from, &to)
	if err != nil {
		return fetchedBars{}, err
	}
	out := fetchedBars{bars: resp.Bars, currency: resp.Currency}
	if len(out.bars) == 0 && svc.FMP != nil {
		points, err := svc.FMP.StockHistoricalEOD(ctx, symbol, &from, &to)
		if err != nil {
			return fetchedBars{}, err
		}
		for _, p := range points {
			var vol *int
			if p.Volume != nil {
				v := int(*p.Volume)
				vol = &v
			}
			out.bars = append(out.bars, marketdata.PriceBarResponse{Date: p.Date, Open: p.Price, High: p.Price, Low: p.Price, Close: p.Price, Volume: vol})
		}
		out.fallback = svc.FMP.Name()
	}
	return out, nil
}

func ingest(ctx context.Context, d *server.Deps, q *marketdatadb.Queries, svc *marketdata.Service, symbol string, now time.Time) error {
	today := marketdata.StartOfDay(now)
	fallbackStart := today.AddDate(-30, 0, 0)
	anchors, err := q.MarketHistoryAnchor(ctx, symbol)
	if err != nil {
		return err
	}
	// Without an anchor the stored history is unverified: backfill it all.
	start := fallbackStart
	if len(anchors) > 0 {
		start = marketdata.StartOfDay(anchors[0].AnchorDate)
	}
	got, err := fetchBars(ctx, svc, symbol, start, today)
	if err != nil {
		return err
	}
	if len(got.bars) == 0 {
		// Nothing to compare or store; the provider may be failing quietly.
		d.Logger.Warn("scenario_history_ingestion returned no bars", "symbol", symbol)
		return nil
	}
	if len(anchors) > 0 && start.After(fallbackStart) {
		key := marketdata.FormatDateOnly(start)
		var fresh map[string]float64
		for _, b := range got.bars {
			if b.Date == key {
				fresh = map[string]float64{key: b.Close}
			}
		}
		if fresh == nil || HistoryDiverged(map[string]float64{key: anchors[0].AnchorClose}, fresh) {
			d.Logger.Warn("scenario_history_ingestion rebackfill", "symbol", symbol)
			// Drop the anchor first: until the re-backfill is stored the
			// history is unverified, and the zones job skips symbols without
			// one, whatever order the new bars land in.
			if err := q.DeleteMarketHistoryAnchor(ctx, symbol); err != nil {
				return err
			}
			if got, err = fetchBars(ctx, svc, symbol, fallbackStart, today); err != nil {
				return err
			}
		}
	}
	if len(got.bars) == 0 {
		d.Logger.Warn("scenario_history_ingestion returned no bars", "symbol", symbol)
		return nil
	}
	if got.fallback != "" {
		if err := svc.UpsertMarketPriceBars(ctx, symbol, got.currency, got.fallback, got.bars); err != nil {
			return err
		}
	}
	// Anchor on the newest completed day: today's bar may be a partial one.
	var anchorDay string
	var anchorClose float64
	todayKey := marketdata.FormatDateOnly(today)
	for _, b := range got.bars {
		if b.Date < todayKey && b.Date > anchorDay {
			anchorDay, anchorClose = b.Date, b.Close
		}
	}
	if anchorDay == "" {
		return nil
	}
	day, err := time.Parse("2006-01-02", anchorDay)
	if err != nil {
		return err
	}
	return q.UpsertMarketHistoryAnchor(ctx, marketdatadb.UpsertMarketHistoryAnchorParams{InstrumentKey: symbol, AnchorDate: day, AnchorClose: anchorClose})
}
