package scenarios

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/config"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/metrics"
	"norviq-api/internal/server"
	"norviq-api/internal/store/scenariosdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// WorkerSettings are ScenarioRunWorker's environment variables
// (configure.swift) plus the feature flag its didBoot checks.
type WorkerSettings struct {
	ScenarioPlanningEnabled string `env:"SCENARIO_PLANNING_ENABLED"`
	IntervalSeconds         string `env:"SCENARIO_WORKER_INTERVAL_SECONDS"`
	MaxConcurrent           string `env:"SCENARIO_WORKER_MAX_CONCURRENT"`
}

// parseSwiftInt is `Environment.get(name).flatMap(Int64.init)`.
func parseSwiftInt(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil
}

// RunWorkerJob is ScenarioRunWorker: every SCENARIO_WORKER_INTERVAL_SECONDS
// (default 2, at least 1) it claims up to SCENARIO_WORKER_MAX_CONCURRENT
// (default 2, clamped to 1...8) queued or lease-expired runs and executes
// them concurrently. No-op unless SCENARIO_PLANNING_ENABLED.
//
// Like Vapor's, it takes no JobLock: each run is claimed with FOR UPDATE
// SKIP LOCKED and a 60s lease renewed every 20s, which already keeps any
// number of workers (Vapor's and v2's replicas) off the same run. An
// advisory lock around the tick would only serialize workers.
type RunWorkerJob struct {
	// Settings overrides the environment (tests).
	Settings *WorkerSettings
	// HeartbeatInterval overrides the 20s lease renewal period (tests).
	HeartbeatInterval time.Duration
}

// Name implements features.Job.
func (RunWorkerJob) Name() string { return "scenario_run_worker" }

func workerSettings(s *WorkerSettings) (WorkerSettings, error) {
	if s != nil {
		return *s, nil
	}
	return config.Load[WorkerSettings]()
}

// Run implements features.Job.
func (j RunWorkerJob) Run(ctx context.Context, d *server.Deps) {
	s, err := workerSettings(j.Settings)
	if err != nil {
		d.Logger.Error("scenario_run_worker config", "error", err)
		return
	}
	if !config.Bool(s.ScenarioPlanningEnabled, false) {
		return
	}
	w := NewRunWorker(d, s)
	if j.HeartbeatInterval > 0 {
		w.heartbeat = j.HeartbeatInterval
	}
	var inflight sync.WaitGroup
	defer inflight.Wait()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if w.running.CompareAndSwap(false, true) {
			inflight.Add(1)
			go func() {
				defer inflight.Done()
				defer w.running.Store(false)
				w.RunOnce(ctx)
			}()
		}
		timer.Reset(w.interval)
	}
}

// RunWorker is one process's ScenarioRunWorker state.
type RunWorker struct {
	q             *scenariosdb.Queries
	redis         redis.UniversalClient
	logger        *slog.Logger
	processor     Processor
	workerID      string
	interval      time.Duration
	maxConcurrent int
	heartbeat     time.Duration
	renewTimeout  time.Duration
	running       atomic.Bool
	now           func() time.Time
}

// NewRunWorker builds the worker from its settings.
func NewRunWorker(d *server.Deps, s WorkerSettings) *RunWorker {
	interval := int64(2)
	if n, ok := parseSwiftInt(s.IntervalSeconds); ok {
		interval = n
	}
	maxConcurrent := int64(2)
	if n, ok := parseSwiftInt(s.MaxConcurrent); ok {
		maxConcurrent = n
	}
	q := scenariosdb.New(d.DB)
	return &RunWorker{
		q: q, redis: d.Redis, logger: d.Logger, processor: Processor{Store: q},
		workerID: apijson.FormatUUID(uuid.New()), interval: time.Duration(max(1, interval)) * time.Second,
		maxConcurrent: int(max(1, min(maxConcurrent, 8))), heartbeat: 20 * time.Second, renewTimeout: 20 * time.Second, now: time.Now,
	}
}

// RunOnce is ScenarioRunWorker.runOnce: claim up to maxConcurrent runs,
// execute them concurrently, wait for all. Queue depth is published to
// Prometheus before claiming so a stuck queue is visible.
func (w *RunWorker) RunOnce(ctx context.Context) {
	if metrics.Enabled() {
		if depth, err := w.q.CountQueuedScenarioRuns(ctx); err == nil {
			metrics.Shared.SetScenarioQueueDepth(int(depth))
		}
	}
	var wg sync.WaitGroup
	for range w.maxConcurrent {
		id, err := w.q.ClaimScenarioRun(ctx, w.workerID)
		if err != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.execute(ctx, id)
		}()
	}
	wg.Wait()
}

// Describe is `String(describing: error)` for the errors the worker
// stores in error_message.
func Describe(err error) string { return err.Error() }

// errLeaseUnconfirmed ends a run whose heartbeat could not reach the database.
// The run stays leased, not failed: this worker no longer knows it owns the run,
// and once the lease expires another worker claims it.
var errLeaseUnconfirmed = errors.New("scenario lease not confirmed")

func (w *RunWorker) execute(ctx context.Context, id uuid.UUID) {
	err := w.process(ctx, id)
	if err == nil || ctx.Err() != nil {
		// On shutdown Vapor's process simply exited: the run keeps its
		// lease and is reclaimed once it expires.
		return
	}
	if errors.Is(err, errLeaseUnconfirmed) {
		w.logger.Warn("scenario_run stopped; lease not confirmed", "id", apijson.FormatUUID(id))
		return
	}
	run, ferr := w.q.GetScenarioRunByID(ctx, id)
	if ferr != nil || run.State == stateCanceled {
		return
	}
	// The heartbeat lost the lease: another worker reclaimed the run and is
	// still running it. Vapor marked it failed with "CancellationError()"
	// under that worker; leave it alone.
	if run.State == "running" && run.LeaseOwner != nil && *run.LeaseOwner != w.workerID {
		return
	}
	now := w.now()
	// Fenced on owner and state: a cancel or a reclaim that lands after the
	// check above is not overwritten.
	_, _ = w.q.FailScenarioRun(ctx, scenariosdb.FailScenarioRunParams{ID: id, LeaseOwner: &w.workerID, ErrorMessage: ptr(Describe(err)), CompletedAt: &now})
	w.logger.Error("scenario_run failed", "id", apijson.FormatUUID(id), "error", Describe(err))
}

// process is execute's do-block; a returned error is its catch.
func (w *RunWorker) process(ctx context.Context, id uuid.UUID) error {
	run, err := w.q.GetScenarioRunByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	scenario, err := w.q.GetScenarioDefinitionByID(ctx, run.ScenarioID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	snapshot, err := w.q.GetScenarioSnapshotByID(ctx, run.SnapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.State == stateCanceled {
		return nil
	}
	if err := w.q.SetScenarioRunProgress(ctx, scenariosdb.SetScenarioRunProgressParams{ID: id, Progress: 0.15}); err != nil {
		return err
	}
	configuration, err := ParseScenarioJSON(scenario.Configuration)
	if err != nil {
		return err
	}
	payload, err := ParseScenarioJSON(snapshot.Payload)
	if err != nil {
		return err
	}

	simCtx, cancelSimulation := context.WithCancelCause(ctx)
	defer cancelSimulation(nil)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		w.renewLease(ctx, id, cancelSimulation, stopHeartbeat)
	}()
	processor := w.processor
	processor.Engine = Engine{Canceled: func() bool { return simCtx.Err() != nil }}
	result, err := processor.Process(ctx, RunInput{
		UserID: run.UserID, Seed: run.Seed, Kind: scenario.Kind, FinancialGoalID: scenario.FinancialGoalID,
		Configuration: configuration.Values, Payload: payload.Values, ValuationTimestamp: snapshot.ValuationTimestamp,
	})
	if err == nil && simCtx.Err() != nil {
		err = errCancelled // try Task.checkCancellation()
	}
	close(stopHeartbeat)
	<-heartbeatDone
	if errors.Is(context.Cause(simCtx), errLeaseUnconfirmed) {
		return errLeaseUnconfirmed
	}
	if err != nil {
		return err
	}

	current, err := w.q.GetScenarioRunByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.State == stateCanceled {
		return nil
	}
	encoded, err := apijson.Marshal(ScenarioJSON{Values: result})
	if err != nil {
		return err
	}
	now := w.now()
	completed, err := w.q.CompleteScenarioRun(ctx, scenariosdb.CompleteScenarioRunParams{
		ID: id, LeaseOwner: &w.workerID, Result: encoded, CompletedAt: &now,
	})
	if err != nil {
		return err
	}
	if completed == 0 {
		// Canceled or reclaimed since the read above: the run is not ours
		// to complete, award or cache.
		return nil
	}
	achievementhooks.Schedule(run.UserID)
	w.cache(ctx, encoded, id)
	return nil
}

// renewLease is the heartbeat task: every 20s it extends the lease by 60s,
// or cancels the simulation once the run is no longer running under this
// worker (canceled by the user, or reclaimed after an expired lease). The
// renew is fenced on owner and state, so zero rows is that signal. An error
// or a renewal that hangs past renewTimeout also cancels: without a confirmed
// lease another worker may claim the run, and aborting on a database blip beats
// two workers writing one run.
func (w *RunWorker) renewLease(ctx context.Context, id uuid.UUID, cancelSimulation context.CancelCauseFunc, stop <-chan struct{}) {
	t := time.NewTimer(w.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
		renewCtx, cancelRenew := context.WithTimeout(ctx, w.renewTimeout)
		renewed, err := w.q.RenewScenarioRunLease(renewCtx, scenariosdb.RenewScenarioRunLeaseParams{
			ID: id, LeaseOwner: &w.workerID, LeaseExpiresAt: ptr(w.now().Add(60 * time.Second)),
		})
		cancelRenew()
		if err != nil {
			cancelSimulation(errLeaseUnconfirmed)
			return
		}
		if renewed == 0 {
			cancelSimulation(nil)
			return
		}
		t.Reset(w.heartbeat)
	}
}

// cache is ScenarioRunWorker.cache: the encoded result under
// scenario:result:<lowercase id> for 24h.
func (w *RunWorker) cache(ctx context.Context, encoded []byte, id uuid.UUID) {
	if w.redis == nil {
		return
	}
	key := ResultCacheKey(id)
	_ = w.redis.Set(ctx, key, string(encoded), 0).Err()
	_ = w.redis.Expire(ctx, key, 86400*time.Second).Err()
}

// RetentionSettings is ScenarioRetentionJob's flag.
type RetentionSettings struct {
	ScenarioPlanningEnabled string `env:"SCENARIO_PLANNING_ENABLED"`
}

// RetentionJob is ScenarioRetentionJob: 5 minutes after boot, then every
// 24h, deletes expired terminal runs and unsaved scenarios without runs.
// No-op unless SCENARIO_PLANNING_ENABLED. Vapor's took no JobLock and
// neither does this one: both statements are idempotent deletes.
type RetentionJob struct {
	Settings *RetentionSettings
}

// Name implements features.Job.
func (RetentionJob) Name() string { return "scenario_retention" }

// Run implements features.Job.
func (j RetentionJob) Run(ctx context.Context, d *server.Deps) {
	var s RetentionSettings
	if j.Settings != nil {
		s = *j.Settings
	} else {
		var err error
		if s, err = config.Load[RetentionSettings](); err != nil {
			d.Logger.Error("scenario_retention config", "error", err)
			return
		}
	}
	if !config.Bool(s.ScenarioPlanningEnabled, false) {
		return
	}
	q := scenariosdb.New(d.DB)
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		RetentionOnce(ctx, q, d.Logger, time.Now())
		timer.Reset(24 * time.Hour)
	}
}

// unsavedScenarioGrace keeps a fresh unsaved scenario that has no run yet.
// Vapor deleted any unsaved scenario without runs, including one created a
// moment before the pass, between the client's create and its first run.
const unsavedScenarioGrace = 24 * time.Hour

// RetentionOnce is ScenarioRetentionJob.runOnce, except that unsaved
// scenarios younger than unsavedScenarioGrace are kept.
func RetentionOnce(ctx context.Context, q *scenariosdb.Queries, logger *slog.Logger, now time.Time) {
	err := func() error {
		if err := q.DeleteExpiredScenarioRuns(ctx, &now); err != nil {
			return err
		}
		unsaved, err := q.ListUnsavedScenarioDefinitionIDs(ctx, now.Add(-unsavedScenarioGrace))
		if err != nil {
			return err
		}
		for _, id := range unsaved {
			count, err := q.CountScenarioRunsForDefinition(ctx, id)
			if err != nil {
				return err
			}
			if count == 0 {
				if err := q.DeleteScenarioDefinition(ctx, id); err != nil {
					return err
				}
			}
		}
		return nil
	}()
	if err != nil {
		logger.Warn("scenario_retention failed", "error", err)
	}
}
