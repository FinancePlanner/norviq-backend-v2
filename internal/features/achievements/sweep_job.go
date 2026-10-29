package achievements

import (
	"context"
	"math"
	"strconv"
	"time"

	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/server"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
)

// SweepJobLockName is AchievementSweepJob.lockName, and the V2_JOBS name.
// Vapor registers its copy whenever the environment is not testing: there is
// no flag that turns it off (its ticks no-op while ACHIEVEMENTS_ENABLED is
// off).
const SweepJobLockName = "achievement_sweep"

// sweepStaleAfter is AchievementSweepJob.staleAfter.
const sweepStaleAfter = 24 * time.Hour

// SweepJob is AchievementSweepJob: hourly (first tick after 60s), it
// re-evaluates with sweep scope the users whose last sweep is more than a
// day old, 200 at a time. Leader-locked; a no-op while ACHIEVEMENTS_ENABLED
// is off.
type SweepJob struct {
	Interval     time.Duration // zero means 1h; clamped to at least 60s
	InitialDelay time.Duration // zero means 60s
	BatchSize    int           // zero means 200
}

// Name implements features.Job.
func (SweepJob) Name() string { return SweepJobLockName }

// Run implements features.Job.
func (j SweepJob) Run(ctx context.Context, d *server.Deps) {
	svc, err := NewService(d)
	if err != nil {
		d.Logger.Warn("achievement_sweep disabled", "error", err)
		return
	}
	interval, delay := j.Interval, j.InitialDelay
	if interval <= 0 {
		interval = time.Hour
	}
	interval = max(interval, time.Minute)
	if delay <= 0 {
		delay = time.Minute
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		SweepOnce(ctx, d, svc, time.Now(), j.BatchSize)
		timer.Reset(interval)
	}
}

// SweepOnce is AchievementSweepJob.runOnce: one tick at now. Unlocks are
// pushed after the leader lock is released, as in Swift.
func SweepOnce(ctx context.Context, d *server.Deps, svc *Service, now time.Time, batchSize int) {
	if !achievementsEnabled() {
		return
	}
	if batchSize <= 0 {
		batchSize = 200
	}
	type found struct {
		userID  uuid.UUID
		unlocks []Unlock
	}
	var unlocked []found
	joblock.RunAsLeader(ctx, d.DB, d.Logger, SweepJobLockName, func(ctx context.Context) {
		if err := svc.ensureActivated(ctx, now); err != nil {
			d.Logger.Warn("achievement_sweep activation failed", "error", err)
			return
		}
		q := achievementsdb.New(d.DB)
		cutoff := now.Add(-sweepStaleAfter)
		userIDs, err := q.DueSweepUsers(ctx, achievementsdb.DueSweepUsersParams{Cutoff: &cutoff, BatchLimit: int32(min(batchSize, math.MaxInt32))}) //nolint:gosec // clamped
		if err != nil {
			d.Logger.Warn("achievement_sweep select failed", "error", err)
			return
		}
		if len(userIDs) == 0 {
			return
		}
		failed := 0
		for _, id := range userIDs {
			unlocks, err := svc.Evaluate(ctx, id, ScopeSweep, now)
			if err != nil {
				failed++
				d.Logger.Warn("achievement_sweep user failed", "user", id, "error", err)
				stampSwept(ctx, d, id, now)
				continue
			}
			if len(unlocks) > 0 {
				unlocked = append(unlocked, found{id, unlocks})
			}
		}
		d.Logger.Info("achievement_sweep ok users=" + strconv.Itoa(len(userIDs)) + " failed=" + strconv.Itoa(failed))
	})
	for _, f := range unlocked {
		if ctx.Err() != nil {
			return
		}
		svc.NotifyUnlocks(ctx, f.unlocks, f.userID)
	}
}

// stampSwept: evaluate stamps last_swept_at only on success, so after a
// failure stamp it here (one broken user is retried daily, not every tick).
// A row created here follows the activation rule. Best effort.
func stampSwept(ctx context.Context, d *server.Deps, userID uuid.UUID, now time.Time) {
	err := achievementsdb.New(d.DB).StampSwept(ctx, achievementsdb.StampSweptParams{ID: uuid.New(), Now: now, UserID: userID})
	if err != nil {
		d.Logger.Warn("achievement_sweep stamp failed", "user", userID, "error", err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
