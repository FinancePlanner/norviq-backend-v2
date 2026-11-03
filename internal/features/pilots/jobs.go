package pilots

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
	"norviq-api/internal/store/pilotsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job lock names, as Vapor's JobLock.runAsLeader used them.
const (
	IngestionLockName = "pilot_ingestion_job"
	MirrorLockName    = "pilot_mirror_job"
)

// Job timing (PilotIngestionJob / PilotMirrorJob and configure.swift).
const (
	// DefaultIngestionIntervalSeconds is PilotIngestionJob.defaultIntervalSeconds.
	DefaultIngestionIntervalSeconds = 7200
	MinIngestionIntervalSeconds     = 900
	DefaultMirrorIntervalSeconds    = 3600
	MinMirrorIntervalSeconds        = 300
	// FundRefresh: funds file quarterly, so each is read at most once a day.
	FundRefresh = 86400 * time.Second
)

// JobSettings are the environment variables configure.swift read for the
// pilot jobs. Both jobs are registered only when PILOTS_ENABLED.
type JobSettings struct {
	PilotsEnabled     string `env:"PILOTS_ENABLED"`
	IngestionInterval string `env:"PILOT_INGESTION_INTERVAL_SECONDS"`
	MirrorInterval    string `env:"PILOT_MIRROR_INTERVAL_SECONDS"`
	SECUserAgent      string `env:"SEC_EDGAR_USER_AGENT"`
}

// swiftInt64 is Environment.get(...).flatMap(Int64.init).
func swiftInt64(raw string, def int64) int64 {
	if raw == "" || strings.ContainsAny(raw, " \t\n_") {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// runEvery is scheduleRepeatedTask(initialDelay:delay:) with
// BackgroundJobState: a tick that finds the previous run still busy is
// dropped.
func runEvery(ctx context.Context, initial, interval time.Duration, tick func(ctx context.Context)) {
	timer := time.NewTimer(initial)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// IngestionJob is PilotIngestionJob: pulls disclosures for every active
// pilot, every two hours by default.
type IngestionJob struct {
	// Build overrides the live ingestion service built per run (tests).
	Build        func(d *server.Deps) *Ingestion
	InitialDelay time.Duration // default 240 s
}

// Name implements features.Job.
func (IngestionJob) Name() string { return IngestionLockName }

// Run implements features.Job. A no-op unless PILOTS_ENABLED, as Vapor only
// registered the job then.
func (j IngestionJob) Run(ctx context.Context, d *server.Deps) {
	s, err := config.Load[JobSettings]()
	if err != nil {
		d.Logger.Error("pilot_ingestion config", "error", err)
		return
	}
	if !config.Bool(s.PilotsEnabled, false) {
		return
	}
	interval := max(MinIngestionIntervalSeconds, swiftInt64(s.IngestionInterval, DefaultIngestionIntervalSeconds))
	delay := j.InitialDelay
	if delay == 0 {
		delay = 240 * time.Second
	}
	build := j.Build
	if build == nil {
		build = func(d *server.Deps) *Ingestion { return LiveIngestion(d, s.SECUserAgent, Endpoints{}) }
	}
	runEvery(ctx, delay, time.Duration(interval)*time.Second, func(ctx context.Context) {
		svc := build(d)
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, IngestionLockName, func(ctx context.Context) {
			RunIngestionOnce(ctx, d.DB, d.Logger, svc, time.Now())
		})
	})
}

// RunIngestionOnce is PilotIngestionJob.runOnceAsLeader.
func RunIngestionOnce(ctx context.Context, db *pgxpool.Pool, logger *slog.Logger, svc *Ingestion, now time.Time) {
	pilots, err := pilotsdb.New(db).ListActivePilots(ctx)
	if err != nil {
		logger.Error("pilot_ingestion run failed", "error", err)
		return
	}
	for i := range pilots {
		p := pilots[i]
		if ctx.Err() != nil {
			return
		}
		if pilotKind(p.Kind) == KindFund && p.LastIngestedAt != nil && now.Sub(*p.LastIngestedAt) < FundRefresh {
			continue
		}
		v, err := svc.Ingest(ctx, p, now)
		if err != nil {
			logger.Warn("pilot_ingestion failed", "pilot", p.Slug, "error", err)
			continue
		}
		if v > 0 {
			logger.Info("pilot_ingestion new_version", "pilot", p.Slug, "version", v)
		}
	}
}

// MirrorJob is PilotMirrorJob: brings every active follow up to its pilot's
// latest book, then records one value snapshot per portfolio follow per day.
type MirrorJob struct {
	// Build overrides the live mirror built per run (tests).
	Build        func(d *server.Deps) *Mirror
	InitialDelay time.Duration // default 300 s
}

// Name implements features.Job.
func (MirrorJob) Name() string { return MirrorLockName }

// Run implements features.Job. A no-op unless PILOTS_ENABLED.
func (j MirrorJob) Run(ctx context.Context, d *server.Deps) {
	s, err := config.Load[JobSettings]()
	if err != nil {
		d.Logger.Error("pilot_mirror config", "error", err)
		return
	}
	if !config.Bool(s.PilotsEnabled, false) {
		return
	}
	interval := max(MinMirrorIntervalSeconds, swiftInt64(s.MirrorInterval, DefaultMirrorIntervalSeconds))
	delay := j.InitialDelay
	if delay == 0 {
		delay = 300 * time.Second
	}
	build := j.Build
	if build == nil {
		build = LiveMirror
	}
	runEvery(ctx, delay, time.Duration(interval)*time.Second, func(ctx context.Context) {
		m := build(d)
		joblock.RunAsLeaderWithSession(ctx, d.DB, d.LockSession, d.Logger, MirrorLockName, func(ctx context.Context) {
			RunMirrorOnce(ctx, d.DB, d.Logger, m, time.Now())
		})
	})
}

// RunMirrorOnce is PilotMirrorJob.runOnceAsLeader. It applies only the
// latest version, never the ones in between.
func RunMirrorOnce(ctx context.Context, db *pgxpool.Pool, logger *slog.Logger, m *Mirror, now time.Time) {
	q := pilotsdb.New(db)
	follows, err := q.ListActiveFollows(ctx)
	if err != nil {
		logger.Error("pilot_mirror run failed", "error", err)
		return
	}
	for i := range follows {
		f := follows[i]
		if ctx.Err() != nil {
			return
		}
		err := catchUp(ctx, db, q, m, f, now)
		if err == nil && targetOf(f) == TargetPortfolio {
			err = snapshot(ctx, q, logger, f, now)
		}
		if err != nil {
			logger.Warn("pilot_mirror failed", "follow_id", strings.ToUpper(f.ID.String()), "error", err)
		}
	}
}

func catchUp(ctx context.Context, db *pgxpool.Pool, q *pilotsdb.Queries, m *Mirror, f pilotsdb.PilotFollow, now time.Time) error {
	pilot, err := q.PilotByID(ctx, f.PilotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	latest, err := q.LatestBookVersion(ctx, f.PilotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if latest.Version <= f.AppliedVersion {
		return nil
	}
	var previous *pilotsdb.PilotBookVersion
	if f.AppliedVersion > 0 {
		p, err := q.BookVersion(ctx, pilotsdb.BookVersionParams{PilotID: f.PilotID, Version: f.AppliedVersion})
		if err == nil {
			previous = &p
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	_, err = m.Apply(ctx, db, f, pilot, latest, previous, now)
	return err
}

// snapshot writes one row per follow per day, kept apart from
// portfolio_value_snapshots on purpose: that table holds only real
// portfolios.
func snapshot(ctx context.Context, q *pilotsdb.Queries, logger *slog.Logger, f pilotsdb.PilotFollow, now time.Time) error {
	if f.PortfolioListID == nil {
		return nil
	}
	day := dateOf(now)
	exists, err := q.FollowSnapshotExists(ctx, pilotsdb.FollowSnapshotExistsParams{FollowID: f.ID, CapturedOn: day})
	if err != nil || exists {
		return err
	}
	v, err := valueLive(ctx, q, f.UserID, *f.PortfolioListID, now)
	if err != nil {
		return err
	}
	if v.missingSymbols != 0 {
		// Writing now would pin a partial value for the day.
		logger.Warn("pilot_snapshot skipped: incomplete pricing", "follow_id", strings.ToUpper(f.ID.String()), "missing", v.missingSymbols)
		return nil
	}
	return q.InsertFollowSnapshot(ctx, pilotsdb.InsertFollowSnapshotParams{
		ID: uuid.New(), FollowID: f.ID, CapturedOn: day, Value: v.marketValue + v.cashBalance, Cash: v.cashBalance,
	})
}

// valuation is the part of PortfolioSnapshotValuation the snapshot reads.
type valuation struct {
	marketValue    float64
	cashBalance    float64
	missingSymbols int
}

// closeLookbackDays is PortfolioSnapshotValuator.closeLookbackDays.
const closeLookbackDays = 10

// valueLive is PortfolioSnapshotValuator.value(pricing: .live): stored
// prices only (bars, then the quote cache, which wins), never the network;
// an unpriced position counts as missing and is never valued at cost.
func valueLive(ctx context.Context, q *pilotsdb.Queries, userID, listID uuid.UUID, asOf time.Time) (valuation, error) {
	day := dateOf(asOf)
	stocks, err := q.StocksInList(ctx, pilotsdb.StocksInListParams{UserID: userID, PortfolioListID: listID})
	if err != nil {
		return valuation{}, err
	}
	cash, err := portfolioCash(ctx, q, userID, listID)
	if err != nil {
		return valuation{}, err
	}
	if len(stocks) == 0 {
		return valuation{cashBalance: round2(cash)}, nil
	}
	seen := map[string]bool{}
	symbols := []string{}
	for i := range stocks {
		n := normalizeSymbol(stocks[i].Symbol)
		if n != "" && !seen[n] {
			seen[n] = true
			symbols = append(symbols, n)
		}
	}
	prices := map[string]float64{}
	from := day.AddDate(0, 0, -closeLookbackDays)
	for _, s := range symbols {
		bars, err := q.AdjustedCloses(ctx, pilotsdb.AdjustedClosesParams{InstrumentKey: strings.ToUpper(s), FromDate: from, ToDate: day})
		if err != nil {
			return valuation{}, err
		}
		if len(bars) > 0 && bars[len(bars)-1].AdjustedClose > 0 {
			prices[s] = bars[len(bars)-1].AdjustedClose
		}
	}
	quotes, err := q.LatestCachedQuotes(ctx, symbols)
	if err != nil {
		return valuation{}, err
	}
	for _, row := range quotes {
		if row.Price > 0 {
			prices[normalizeSymbol(row.Symbol)] = row.Price
		}
	}
	market, missing := 0.0, 0
	for i := range stocks {
		if p := prices[normalizeSymbol(stocks[i].Symbol)]; p > 0 {
			market += stocks[i].Shares * p
		} else {
			missing++
		}
	}
	return valuation{marketValue: round2(market), cashBalance: round2(cash), missingSymbols: missing}, nil
}

// portfolioCash is PortfolioSnapshotValuator.cashBalance: the latest balance
// per account and currency, plus manual cash positions, negatives floored.
func portfolioCash(ctx context.Context, q *pilotsdb.Queries, userID, listID uuid.UUID) (float64, error) {
	ids, err := q.AccountIDsForUserPortfolio(ctx, pilotsdb.AccountIDsForUserPortfolioParams{UserID: userID, PortfolioID: &listID})
	if err != nil {
		return 0, err
	}
	total := 0.0
	if len(ids) > 0 {
		balances, err := q.CashBalancesForAccounts(ctx, ids)
		if err != nil {
			return 0, err
		}
		latest := map[string]pilotsdb.CashBalance{}
		order := []string{}
		for _, b := range balances {
			key := strings.ToLower(b.AccountID.String()) + "::" + strings.ToUpper(b.Currency)
			e, ok := latest[key]
			if !ok {
				order = append(order, key)
				latest[key] = b
			} else if b.AsOf.After(e.AsOf) {
				latest[key] = b
			}
		}
		for _, k := range order {
			total += max(0, latest[k].Balance)
		}
	}
	positions, err := q.CashPositionBalances(ctx, listID)
	if err != nil {
		return 0, err
	}
	manual := 0.0
	for _, b := range positions {
		manual += max(0, b)
	}
	return total + manual, nil
}

// normalizeSymbol is normalizePortfolioSymbol.
func normalizeSymbol(raw string) string { return strings.ToUpper(strings.TrimSpace(raw)) }
