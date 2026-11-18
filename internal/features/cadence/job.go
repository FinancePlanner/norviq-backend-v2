package cadence

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/store/cadencedb"
	"norviq-api/internal/store/marketdatadb"

	"github.com/jackc/pgx/v5"
)

// ZonesJobLockName is the advisory-lock name of the zones job.
const ZonesJobLockName = "cadence_zones_job"

const (
	defaultZonesInterval = 21600
	minZonesInterval     = 3600
	zonesHistoryYears    = 21
)

// ZonesJobSettings are the zones job's environment variables.
type ZonesJobSettings struct {
	Enabled         string `env:"CADENCE_ENABLED"`
	IntervalSeconds string `env:"CADENCE_ZONES_INTERVAL_SECONDS"`
}

// ZonesJob computes the contribution zone of every held, watched and
// benchmark symbol and stores it in cadence_zones. It is a no-op unless
// CADENCE_ENABLED is true.
type ZonesJob struct {
	// Service overrides the shared market data service (tests).
	Service *marketdata.Service
	// InitialDelay defaults to 60s.
	InitialDelay time.Duration
}

// Name implements features.Job.
func (ZonesJob) Name() string { return "cadence_zones" }

// Run implements features.Job.
func (j ZonesJob) Run(ctx context.Context, d *server.Deps) {
	s, err := config.Load[ZonesJobSettings]()
	if err != nil {
		d.Logger.Error("cadence_zones config", "error", err)
		return
	}
	if !config.Bool(s.Enabled, false) {
		return
	}
	interval := defaultZonesInterval
	if n, err := strconv.Atoi(strings.TrimSpace(s.IntervalSeconds)); err == nil {
		interval = n
	}
	interval = max(interval, minZonesInterval)
	svc := j.Service
	if svc == nil {
		if svc, err = market.SharedService(d); err != nil {
			d.Logger.Error("cadence_zones service", "error", err)
			return
		}
	}
	delay := j.InitialDelay
	if delay == 0 {
		delay = 60 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, ZonesJobLockName, func(ctx context.Context) {
			sweep(ctx, d, svc, time.Now())
		})
		timer.Reset(time.Duration(interval) * time.Second)
	}
}

// sweep is one pass over every symbol; a failing symbol is logged and skipped.
func sweep(ctx context.Context, d *server.Deps, svc *marketdata.Service, now time.Time) {
	raw, err := marketdatadb.New(d.DB).HistoryIngestionSymbols(ctx, true)
	if err != nil {
		d.Logger.Warn("cadence_zones symbols failed", "error", err)
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
		if err := ComputeSymbol(ctx, d, svc, symbol, now); err != nil {
			d.Logger.Warn("cadence_zones failed", "symbol", symbol, "error", err)
		}
	}
}

// ComputeSymbol recomputes every timeframe of one symbol and stores it in one
// transaction. The fundamentals guard applies to the newest confirmed reading
// and the newest reading (the forming one) of each timeframe, the two rows the
// API serves; every other row is stored uncapped, so a row that stopped being
// one of them loses its guard on the next run. Periods stored but no
// longer computed (the 21 year window slid forward, or the history became
// shorter for any reason) are deleted.
//
// A symbol whose stored history is not verified by the ingestion job's anchor
// is skipped with its zone rows left as they are: during a split the history
// can mix pre and post split closes, which reads as a crash. It returns nil.
// A growth lookup that fails leaves the guard unchecked; it never fails the
// symbol.
func ComputeSymbol(ctx context.Context, d *server.Deps, svc *marketdata.Service, symbol string, now time.Time) error {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	today := utcDay(now)
	closes, err := marketdatadb.New(d.DB).MarketPriceBarCloses(ctx, marketdatadb.MarketPriceBarClosesParams{
		InstrumentKey: symbol,
		FromDate:      today.AddDate(-zonesHistoryYears, 0, 0),
		ToDate:        today,
	})
	if err != nil {
		return err
	}
	if reason := historyInconsistent(ctx, marketdatadb.New(d.DB), symbol, closes); reason != "" {
		d.Logger.Warn("cadence_zones skipped: history inconsistent", "symbol", symbol, "reason", reason)
		return nil
	}
	bars := make([]Bar, len(closes))
	for i, c := range closes {
		bars[i] = Bar{Date: c.Date, Close: c.AdjustedClose}
	}
	rules := DefaultRules
	readings := map[Timeframe][]Reading{}
	hasReadings := false
	for _, tf := range AllTimeframes() {
		readings[tf] = Compute(bars, tf, rules, now)
		// Nothing to judge when the newest reading has no trend.
		if rs := readings[tf]; len(rs) > 0 && rs[len(rs)-1].Zone != ZoneInsufficient {
			hasReadings = true
		}
	}

	var reason string
	var checked bool
	if hasReadings {
		reason, checked, err = guardFor(ctx, d, svc, symbol)
		if err != nil {
			return err
		}
	}

	return pgx.BeginFunc(ctx, d.DB, func(tx pgx.Tx) error {
		q := cadencedb.New(tx)
		for _, tf := range AllTimeframes() {
			rs := readings[tf]
			keep := make([]time.Time, 0, len(rs))
			// The guard verdict covers the two rows the API reads: the newest
			// confirmed reading and the newest reading (the forming one).
			newestConfirmed := -1
			for i := range rs {
				if rs[i].Confirmed {
					newestConfirmed = i
				}
			}
			for i := range rs {
				r := &rs[i]
				keep = append(keep, r.PeriodEnd)
				p := zoneParams(symbol, r, rules.Version)
				if i == len(rs)-1 || i == newestConfirmed {
					p.CappedZone = string(Capped(r.Zone, reason))
					p.GuardChecked = checked
					if reason != "" {
						p.GuardReason = &reason
					}
				}
				if err := q.UpsertZone(ctx, p); err != nil {
					return err
				}
			}
			if err := q.DeleteZonesNotIn(ctx, cadencedb.DeleteZonesNotInParams{
				Symbol: symbol, Timeframe: string(tf), Keep: keep,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// historyInconsistent returns why the stored closes cannot be trusted, or "".
// The ingestion job keeps an anchor, the close of its newest completed day
// as the provider reported it. A stored close that is missing or differs from
// it by more than the job's tolerance means the history is half rewritten
// (a split) or was never verified.
func historyInconsistent(ctx context.Context, q *marketdatadb.Queries, symbol string, closes []marketdatadb.MarketPriceBarClosesRow) string {
	anchors, err := q.MarketHistoryAnchor(ctx, symbol)
	if err != nil {
		return "anchor lookup failed: " + err.Error()
	}
	if len(anchors) == 0 {
		return "no anchor"
	}
	key := marketdata.FormatDateOnly(anchors[0].AnchorDate)
	for _, c := range closes {
		if marketdata.FormatDateOnly(c.Date) != key {
			continue
		}
		stored := map[string]float64{key: anchors[0].AnchorClose}
		if market.HistoryDiverged(stored, map[string]float64{key: c.AdjustedClose}) {
			return "anchor close differs from stored close"
		}
		return ""
	}
	return "no stored bar on anchor date"
}

func zoneParams(symbol string, r *Reading, ruleVersion int) cadencedb.UpsertZoneParams {
	p := cadencedb.UpsertZoneParams{
		Symbol:      symbol,
		Timeframe:   string(r.Timeframe),
		PeriodEnd:   r.PeriodEnd,
		AsOf:        r.AsOf,
		Close:       r.Close,
		Trend:       r.Trend,
		Distance:    r.Distance,
		Percentile:  r.Percentile,
		SampleCount: int32(r.SampleCount), //nolint:gosec // a sample count is far below int32 range
		Zone:        string(r.Zone),
		CappedZone:  string(r.Zone),
		Confirmed:   r.Confirmed,
		RuleVersion: int32(ruleVersion), //nolint:gosec // a rule version is a small constant
	}
	if r.TouchZone != nil {
		z := string(*r.TouchZone)
		p.TouchZone = &z
	}
	return p
}

// guardFor evaluates the fundamentals guard from the newest annual growth
// row. A provider that is missing, failing or silent yields ("", false).
func guardFor(ctx context.Context, d *server.Deps, svc *marketdata.Service, symbol string) (reason string, checked bool, err error) {
	isETF, err := cadencedb.New(d.DB).SymbolIsETF(ctx, symbol)
	if err != nil {
		return "", false, err
	}
	f := Fundamentals{IsETF: isETF}
	if !isETF {
		f.RevenueGrowth, f.FreeCashFlowGrowth = annualGrowth(ctx, d, svc, symbol)
	}
	reason, checked = FundamentalsGuard(f)
	return reason, checked, nil
}

// annualGrowth returns revenue and free cash flow growth of the newest
// annual row, nil for a figure that is absent or not a finite number.
func annualGrowth(ctx context.Context, d *server.Deps, svc *marketdata.Service, symbol string) (rev, fcf *float64) {
	limit, period := 5, "FY"
	rows, err := svc.FinancialGrowth(ctx, symbol, &limit, &period)
	if err != nil {
		d.Logger.Warn("cadence_zones growth unavailable", "symbol", symbol, "error", err)
		return nil, nil
	}
	var best *marketdata.FinancialGrowthResponse
	for i := range rows {
		if p := rows[i].Period; p != nil && !strings.EqualFold(*p, "FY") {
			continue
		}
		if best == nil || rows[i].Date > best.Date {
			best = &rows[i]
		}
	}
	if best == nil {
		return nil, nil
	}
	return finite(best.RevenueGrowth), finite(best.FreeCashFlowGrowth)
}

func finite(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	return v
}
