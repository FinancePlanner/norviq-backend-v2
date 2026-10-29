package portfolio

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/config"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
)

// SnapshotJobLockName is the advisory-lock name Vapor's job used.
const SnapshotJobLockName = "portfolio_snapshot_job"

// SnapshotJobSettings are PortfolioSnapshotJob's environment variables.
type SnapshotJobSettings struct {
	IntervalSeconds string `env:"PORTFOLIO_SNAPSHOT_INTERVAL_SECONDS"`
}

// SnapshotJob is PortfolioSnapshotJob: one portfolio_value_snapshots row per
// actual, unarchived portfolio list per trading day. The hourly tick is a
// retry; the (user, list, day) unique constraint makes repeats no-ops.
// Vapor registers its copy unconditionally (no disable flag).
type SnapshotJob struct {
	// InitialDelay defaults to Vapor's 180s.
	InitialDelay time.Duration
}

// Name implements features.Job.
func (SnapshotJob) Name() string { return SnapshotJobLockName }

// SnapshotInterval is the tick interval: PORTFOLIO_SNAPSHOT_INTERVAL_SECONDS
// (Int64.init, default 3600), at least 300.
func SnapshotInterval(raw string) time.Duration {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		n = 3600
	}
	return time.Duration(max(300, n)) * time.Second
}

// Run implements features.Job.
func (j SnapshotJob) Run(ctx context.Context, d *server.Deps) {
	s, err := config.Load[SnapshotJobSettings]()
	if err != nil {
		d.Logger.Error("portfolio_snapshot config", "error", err)
		return
	}
	interval := SnapshotInterval(s.IntervalSeconds)
	delay := j.InitialDelay
	if delay == 0 {
		delay = 180 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		joblock.RunAsLeader(ctx, d.DB, d.Logger, SnapshotJobLockName, func(ctx context.Context) {
			RunSnapshotTick(ctx, d.DB, d.Logger, portfolios.Now())
		})
		timer.Reset(interval)
	}
}

// RunSnapshotTick is runOnceAsLeader: captures every actual, unarchived
// list, logging (not failing on) per-list errors.
func RunSnapshotTick(ctx context.Context, db portfoliodb.DBTX, logger *slog.Logger, now time.Time) {
	lists, err := portfoliodb.New(db).SnapshotCaptureLists(ctx)
	if err != nil {
		logger.Error("portfolio_snapshot run failed", "error", err)
		return
	}
	for iL := range lists {
		l := &lists[iL]
		if ctx.Err() != nil {
			return
		}
		if _, err := CaptureSnapshot(ctx, db, logger, *l, now); err != nil {
			logger.Warn("portfolio_snapshot capture failed", "portfolio_list_id", apijson.FormatUUID(l.ID), "error", err)
		}
	}
}

// CaptureOutcome is PortfolioSnapshotJob.CaptureOutcome.
type CaptureOutcome struct {
	Kind    CaptureKind
	Missing int // for CaptureIncompletePricing
}

// CaptureKind enumerates why a capture did or did not write.
type CaptureKind int

// CaptureOutcome cases.
const (
	CaptureCaptured CaptureKind = iota
	CaptureAlreadyCaptured
	CaptureMarketClosed
	CaptureIncompletePricing
	CaptureEmpty
)

// CaptureSnapshot is PortfolioSnapshotJob.capture(list:now:on:logger:).
func CaptureSnapshot(ctx context.Context, db portfoliodb.DBTX, logger *slog.Logger, list portfoliodb.PortfolioList, now time.Time) (CaptureOutcome, error) {
	q := portfoliodb.New(db)
	day := StartOfDay(now)
	exists := func() (bool, error) {
		return q.SnapshotExists(ctx, portfoliodb.SnapshotExistsParams{UserID: list.UserID, PortfolioListID: list.ID, CapturedOn: day})
	}
	if ok, err := exists(); err != nil || ok {
		return CaptureOutcome{Kind: CaptureAlreadyCaptured}, err
	}
	v, err := SnapshotValuator{DB: db}.Value(ctx, list.UserID, list.ID, now, PricingLive)
	if err != nil {
		return CaptureOutcome{}, err
	}
	if v.IsEmpty() {
		return CaptureOutcome{Kind: CaptureEmpty}, nil
	}
	// A cash-only portfolio has no symbol to date the market by.
	if v.PositionCount > 0 {
		traded, err := marketTraded(ctx, q, list.UserID, list.ID, day)
		if err != nil {
			return CaptureOutcome{}, err
		}
		if !traded {
			return CaptureOutcome{Kind: CaptureMarketClosed}, nil
		}
	}
	if !v.IsFullyPriced() {
		if logger != nil {
			logger.Warn("portfolio_snapshot skipped: incomplete pricing",
				"portfolio_list_id", apijson.FormatUUID(list.ID),
				"priced", strconv.Itoa(v.PricedSymbols), "missing", strconv.Itoa(v.MissingSymbols))
		}
		return CaptureOutcome{Kind: CaptureIncompletePricing, Missing: v.MissingSymbols}, nil
	}
	err = q.InsertSnapshot(ctx, portfoliodb.InsertSnapshotParams{
		ID:              uuid.New(),
		UserID:          list.UserID,
		PortfolioListID: list.ID,
		CapturedOn:      day,
		// The list's own base currency, not the quote currency.
		Currency:       list.BaseCurrency,
		MarketValue:    v.MarketValue,
		CostBasis:      v.CostBasis,
		CashBalance:    v.CashBalance,
		PositionCount:  int64(v.PositionCount),
		Source:         SourceLive,
		PricedSymbols:  int64(v.PricedSymbols),
		MissingSymbols: int64(v.MissingSymbols),
		CreatedAt:      portfolios.Now(),
	})
	if err != nil {
		// Another replica may have won the race; the row exists either way.
		if ok, rerr := exists(); rerr != nil || !ok {
			if rerr != nil {
				return CaptureOutcome{}, rerr
			}
			return CaptureOutcome{}, err
		}
		return CaptureOutcome{Kind: CaptureAlreadyCaptured}, nil
	}
	return CaptureOutcome{Kind: CaptureCaptured}, nil
}

// marketTraded: some held symbol has a bar or a cached quote dated day.
func marketTraded(ctx context.Context, q *portfoliodb.Queries, userID, listID uuid.UUID, day time.Time) (bool, error) {
	stocks, err := q.StocksForUserList(ctx, portfoliodb.StocksForUserListParams{UserID: userID, PortfolioListID: listID})
	if err != nil {
		return false, err
	}
	symbols := UniqueSymbols(stockSymbols(stocks))
	if len(symbols) == 0 {
		return false, nil
	}
	next := AddDays(day, 1)
	bars, err := q.CountBarsOnDay(ctx, portfoliodb.CountBarsOnDayParams{Symbols: symbols, Day: day, NextDay: next})
	if err != nil || bars > 0 {
		return bars > 0, err
	}
	quotes, err := q.CountQuotesInRange(ctx, portfoliodb.CountQuotesInRangeParams{Symbols: symbols, FromTime: day, ToTime: next})
	return quotes > 0, err
}
