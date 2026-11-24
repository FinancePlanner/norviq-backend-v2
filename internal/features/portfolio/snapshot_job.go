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
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, SnapshotJobLockName, func(ctx context.Context) {
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
		out, err := CaptureSnapshot(ctx, db, *l, now)
		if err != nil {
			logger.Warn("portfolio_snapshot capture failed", "portfolio_list_id", apijson.FormatUUID(l.ID), "error", err)
			continue
		}
		// The tick runs hourly, so no-ops (already captured, market closed,
		// nothing held) repeat all day and log at Debug; writes log at Info.
		// priced and missing count positions (stock rows), not symbols.
		level := slog.LevelDebug
		if out.Kind == CaptureCaptured || out.Kind == CaptureCapturedPartial {
			level = slog.LevelInfo
		}
		logger.Log(ctx, level, "portfolio_snapshot outcome", "portfolio_list_id", apijson.FormatUUID(l.ID),
			"outcome", out.Kind.String(), "priced", out.Priced, "missing", out.Missing)
	}
}

// CaptureOutcome is what one capture did, with how many positions (stock
// rows, not distinct symbols) it priced and could not price; both are zero
// unless a row was valued.
type CaptureOutcome struct {
	Kind    CaptureKind
	Priced  int
	Missing int
}

// CaptureKind enumerates why a capture did or did not write.
type CaptureKind int

// CaptureOutcome cases.
const (
	// CaptureCaptured wrote a fully priced row.
	CaptureCaptured CaptureKind = iota
	CaptureAlreadyCaptured
	CaptureMarketClosed
	// CaptureCapturedPartial wrote a row with some symbols unpriced; they
	// count as 0, as they do in live statistics.
	CaptureCapturedPartial
	CaptureEmpty
)

// String is the outcome as logged: captured, captured_partial,
// already_captured, market_closed or no_holdings.
func (k CaptureKind) String() string {
	switch k {
	case CaptureCaptured:
		return "captured"
	case CaptureAlreadyCaptured:
		return "already_captured"
	case CaptureMarketClosed:
		return "market_closed"
	case CaptureCapturedPartial:
		return "captured_partial"
	case CaptureEmpty:
		return "no_holdings"
	}
	return "unknown"
}

// CaptureSnapshot is PortfolioSnapshotJob.capture(list:now:on:logger:).
func CaptureSnapshot(ctx context.Context, db portfoliodb.DBTX, list portfoliodb.PortfolioList, now time.Time) (CaptureOutcome, error) {
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
		traded, err := marketTraded(ctx, q, list.UserID, list.ID, now)
		if err != nil {
			return CaptureOutcome{}, err
		}
		if !traded {
			return CaptureOutcome{Kind: CaptureMarketClosed}, nil
		}
	}
	// An unpriced symbol counts as 0, as in live statistics, and the row
	// records how many there were. Skipping the whole list instead froze
	// the series at the last fully priced day.
	written := CaptureOutcome{Kind: CaptureCaptured, Priced: v.PricedSymbols, Missing: v.MissingSymbols}
	if !v.IsFullyPriced() {
		written.Kind = CaptureCapturedPartial
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
	return written, nil
}

// weekdayCloseHourUTC is the hour (UTC) from which a weekday counts as
// traded without a bar or quote dated it. US markets have closed by then
// (21:00 UTC in winter, 20:00 in summer).
const weekdayCloseHourUTC = 22

// marketTraded: some held symbol has a bar or a cached quote dated now's
// day (UTC). Failing that, a Monday-Friday counts as traded from
// weekdayCloseHourUTC, so a symbol the provider never prices cannot stop
// capture; before then the day has no evidence yet and is not captured.
func marketTraded(ctx context.Context, q *portfoliodb.Queries, userID, listID uuid.UUID, now time.Time) (bool, error) {
	day := StartOfDay(now)
	stocks, err := q.StocksForUserList(ctx, portfoliodb.StocksForUserListParams{UserID: userID, PortfolioListID: listID})
	if err != nil {
		return false, err
	}
	symbols := UniqueSymbols(stockSymbols(stocks))
	if len(symbols) > 0 {
		next := AddDays(day, 1)
		bars, err := q.CountBarsOnDay(ctx, portfoliodb.CountBarsOnDayParams{Symbols: symbols, Day: day, NextDay: next})
		if err != nil || bars > 0 {
			return bars > 0, err
		}
		quotes, err := q.CountQuotesInRange(ctx, portfoliodb.CountQuotesInRangeParams{Symbols: symbols, FromTime: day, ToTime: next})
		if err != nil || quotes > 0 {
			return quotes > 0, err
		}
	}
	u := now.UTC()
	weekday := u.Weekday() != time.Saturday && u.Weekday() != time.Sunday
	return weekday && u.Hour() >= weekdayCloseHourUTC, nil
}
