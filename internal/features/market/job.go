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
// filled for every held symbol, watchlist symbol and risk-profile benchmark
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

// Sweep is one pass over every held, watchlisted and benchmark symbol. A rate-limited provider stops
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

// overlapDays is how far back an incremental fetch reaches into stored bars,
// so a split shows up as a disagreement on dates already held.
const overlapDays = 7

// divergenceTolerance is the relative close difference above which a stored
// bar and the provider's current bar are considered to disagree.
const divergenceTolerance = 0.005

// OverlapStart is where an incremental fetch begins: seven days before the
// last stored bar, so it re-reads bars that already exist.
func OverlapStart(last time.Time) time.Time {
	return marketdata.StartOfDay(last).AddDate(0, 0, -overlapDays)
}

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
	provider string
}

// fetchBars loads bars from the market provider, falling back to FMP end of
// day data when it returns none. RefreshHistory also upserts what it fetched.
func fetchBars(ctx context.Context, svc *marketdata.Service, symbol string, start, today time.Time) (fetchedBars, error) {
	from, to := marketdata.FormatDateOnly(start), marketdata.FormatDateOnly(today)
	resp, err := svc.RefreshHistory(ctx, symbol, &from, &to)
	if err != nil {
		return fetchedBars{}, err
	}
	out := fetchedBars{bars: resp.Bars, currency: resp.Currency, provider: "market"}
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
		out.provider = svc.FMP.Name()
	}
	return out, nil
}

func ingest(ctx context.Context, d *server.Deps, q *marketdatadb.Queries, svc *marketdata.Service, symbol string, now time.Time) error {
	today := marketdata.StartOfDay(now)
	fallbackStart := today.AddDate(-30, 0, 0)
	dates, err := q.MarketPriceBarLastDate(ctx, marketdatadb.MarketPriceBarLastDateParams{InstrumentKey: symbol, FromDate: fallbackStart, ToDate: today})
	if err != nil {
		return err
	}
	start := fallbackStart
	stored := map[string]float64{}
	if len(dates) > 0 {
		start = OverlapStart(dates[0])
		// Read before fetching: RefreshHistory overwrites the overlap window.
		rows, err := q.MarketPriceBarCloses(ctx, marketdatadb.MarketPriceBarClosesParams{InstrumentKey: symbol, FromDate: start, ToDate: today})
		if err != nil {
			return err
		}
		for _, r := range rows {
			stored[marketdata.FormatDateOnly(r.Date)] = r.AdjustedClose
		}
	}
	got, err := fetchBars(ctx, svc, symbol, start, today)
	if err != nil {
		return err
	}
	if len(got.bars) > 0 && len(stored) > 0 {
		fresh := make(map[string]float64, len(got.bars))
		for _, b := range got.bars {
			fresh[b.Date] = b.Close
		}
		if HistoryDiverged(stored, fresh) {
			d.Logger.Warn("scenario_history_ingestion rebackfill", "symbol", symbol)
			if got, err = fetchBars(ctx, svc, symbol, fallbackStart, today); err != nil {
				return err
			}
		}
	}
	if len(got.bars) == 0 {
		d.Logger.Warn("scenario_history_ingestion returned no bars", "symbol", symbol)
		return nil
	}
	return svc.UpsertMarketPriceBars(ctx, symbol, got.currency, got.provider, got.bars)
}
