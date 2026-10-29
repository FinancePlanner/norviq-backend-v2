package achievements

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"norviq-api/internal/platform/push"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scope is AchievementScope: live skips sweepOnly definitions; sweep (the
// job, and every backfill) covers them all.
type Scope int

const (
	ScopeLive Scope = iota
	ScopeSweep
)

// AchievementSource values (stored as text).
const (
	SourceLive        = "live"
	SourceSweep       = "sweep"
	SourceBackfill    = "backfill"
	SourceLegacyBadge = "legacy_badge"
)

// activityKind is AchievementNotifier.activityKind: social_activity.kind of
// an unlock.
const activityKind = "achievement_unlocked"

// Unlock is one user_achievements row.
type Unlock = achievementsdb.UserAchievement

// Service is AchievementService plus the notifier.
type Service struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	// Push is nil when no push stack is available; nothing is sent then.
	Push *push.Service
}

func (s *Service) q() *achievementsdb.Queries { return achievementsdb.New(s.DB) }

// ensureActivated is AchievementService.ensureActivated: records that
// achievements are running, the first time only.
func (s *Service) ensureActivated(ctx context.Context, now time.Time) error {
	return s.q().EnsureActivated(ctx, now)
}

// Evaluate is AchievementService.evaluate: returns newly inserted unlocks.
// Idempotent; never pays XP twice. Evaluations of one user are serialized by
// a FOR UPDATE on their achievement_evaluations row, held for the whole
// evaluation, so needs_backfill is read and cleared under the lock.
func (s *Service) Evaluate(ctx context.Context, userID uuid.UUID, scope Scope, now time.Time) ([]Unlock, error) {
	q := s.q()
	if err := q.EnsureActivated(ctx, now); err != nil {
		return nil, err
	}
	if err := q.EnsureEvaluationRow(ctx, achievementsdb.EnsureEvaluationRowParams{ID: uuid.New(), UserID: userID}); err != nil {
		return nil, err
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tq := achievementsdb.New(tx)

	locked, err := tq.LockEvaluation(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(locked) == 0 {
		return nil, tx.Commit(ctx)
	}
	needsBackfill := locked[0]

	effective := scope
	source := SourceLive
	if scope == ScopeSweep {
		source = SourceSweep
	}
	if needsBackfill {
		effective, source = ScopeSweep, SourceBackfill
	}
	var definitions []*Definition
	keys := map[string]bool{}
	for i := range Catalog {
		d := &Catalog[i]
		if effective == ScopeSweep || !d.SweepOnly {
			definitions = append(definitions, d)
			keys[d.Key] = true
		}
	}

	m := measure(ctx, evalDB{db: tx, tx: tx}, s.Logger, userID, keys, now)
	heldRows, err := tq.UserAchievements(ctx, userID)
	if err != nil {
		return nil, err
	}
	held := map[[2]string]bool{}
	for i := range heldRows {
		held[[2]string{heldRows[i].AchievementKey, heldRows[i].Tier}] = true
	}
	payXP := leaderboardsEnabled()

	var inserted []Unlock
	for _, d := range definitions {
		value, ok := m.values[d.Key]
		if !ok {
			continue
		}
		for _, spec := range d.Tiers {
			if value < spec.Threshold || held[[2]string{d.Key, spec.Tier}] {
				continue
			}
			created := time.Now()
			unlock := Unlock{
				ID: uuid.New(), UserID: userID, AchievementKey: d.Key, Tier: spec.Tier,
				UnlockedAt: now, Source: source, CreatedAt: &created,
			}
			if needsBackfill {
				// Backfilled unlocks are history, not news: never celebrated.
				seen := now
				unlock.SeenAt = &seen
			}
			ok, err := insertIgnoringDuplicate(ctx, tx, unlock)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			inserted = append(inserted, unlock)
			if !needsBackfill {
				s.recordActivity(ctx, tx, unlock)
			}
			if payXP {
				s.award(ctx, tx, spec, d.Key, userID, needsBackfill)
			}
		}
	}

	// A backfill with a failed metric stays pending, so the next run retries
	// it as backfill; the run is still stamped as swept.
	backfillPending := needsBackfill && len(m.failed) > 0
	if err := tq.UpdateEvaluation(ctx, achievementsdb.UpdateEvaluationParams{
		NeedsBackfill: backfillPending, Now: &now, Swept: effective == ScopeSweep, UserID: userID,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return inserted, nil
}

// EvaluateBestEffort is AchievementService.evaluateBestEffort: no-op with
// the flag off; swallows errors (logged) and returns the new unlocks.
func (s *Service) EvaluateBestEffort(ctx context.Context, userID uuid.UUID, now time.Time) []Unlock {
	if !achievementsEnabled() {
		return nil
	}
	unlocks, err := s.Evaluate(ctx, userID, ScopeLive, now)
	if err != nil {
		s.Logger.Warn("achievements.evaluate failed", "error", err)
		return nil
	}
	return unlocks
}

// RecordLegacyBadge is AchievementService.recordLegacyBadge: mirrors a newly
// earned legacy badge as legacy.<raw>, already seen and with no XP. Written
// whatever the flag. Never fails (logged). db may be the caller's
// transaction: ON CONFLICT keeps it usable.
func RecordLegacyBadge(ctx context.Context, db achievementsdb.DBTX, logger *slog.Logger, userID uuid.UUID, badgeType, tier string, earnedAt time.Time) {
	err := achievementsdb.New(db).RecordLegacyBadge(ctx, achievementsdb.RecordLegacyBadgeParams{
		ID: uuid.New(), UserID: userID, AchievementKey: "legacy." + badgeType, Tier: tier, EarnedAt: earnedAt,
	})
	if err != nil && logger != nil {
		logger.Warn("achievements.legacy_badge failed", "error", err)
	}
}

// savepoint runs body inside a savepoint of tx: any error is rolled back to
// it and returned, leaving tx usable.
func savepoint(ctx context.Context, tx pgx.Tx, body func(q *achievementsdb.Queries) error) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := body(achievementsdb.New(sp)); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return nil
}

// isConstraintFailure is DatabaseError.isConstraintFailure for Postgres:
// SQLSTATE class 23 (integrity constraint violation).
func isConstraintFailure(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && len(pg.Code) >= 2 && pg.Code[:2] == "23"
}

// insertIgnoringDuplicate is XPService.insertIgnoringDuplicate inside a
// savepoint: a constraint failure is "already there", anything else fails
// the evaluation.
func insertIgnoringDuplicate(ctx context.Context, tx pgx.Tx, u Unlock) (bool, error) {
	err := savepoint(ctx, tx, func(q *achievementsdb.Queries) error {
		return q.InsertUserAchievement(ctx, achievementsdb.InsertUserAchievementParams{
			ID: u.ID, UserID: u.UserID, AchievementKey: u.AchievementKey, Tier: u.Tier,
			UnlockedAt: u.UnlockedAt, Source: u.Source, SeenAt: u.SeenAt, CreatedAt: u.CreatedAt,
		})
	})
	if err == nil {
		return true, nil
	}
	if isConstraintFailure(err) {
		return false, nil
	}
	return false, err
}

// recordActivity writes the friends' feed item for a live or sweep unlock. A
// failure is logged and rolled back to the savepoint.
func (s *Service) recordActivity(ctx context.Context, tx pgx.Tx, u Unlock) {
	err := savepoint(ctx, tx, func(q *achievementsdb.Queries) error {
		return q.InsertSocialActivity(ctx, achievementsdb.InsertSocialActivityParams{
			ID: uuid.New(), ActorID: u.UserID, Kind: activityKind, UserAchievementID: u.ID, CreatedAt: u.UnlockedAt,
		})
	})
	if err != nil {
		s.Logger.Warn("achievements.activity failed", "key", u.AchievementKey, "tier", u.Tier, "error", err)
	}
}

// award pays the unlock's XP once per dedupe key. A failure is logged and
// rolled back to the savepoint. mirror of XPService.award (social slice owns
// the route-facing copy).
func (s *Service) award(ctx context.Context, tx pgx.Tx, spec TierSpec, key string, userID uuid.UUID, excludeFromLeaderboard bool) {
	if spec.XP <= 0 {
		return
	}
	dedupe := "achievement:" + key + ":" + spec.Tier
	err := savepoint(ctx, tx, func(q *achievementsdb.Queries) error {
		exists, err := q.XPEventExists(ctx, achievementsdb.XPEventExistsParams{UserID: userID, DedupeKey: dedupe})
		if err != nil || exists {
			return err
		}
		return q.InsertXPEvent(ctx, achievementsdb.InsertXPEventParams{
			ID: uuid.New(), UserID: userID, Type: "badge_earned", Points: int64(spec.XP), DedupeKey: dedupe,
			ExcludeFromLeaderboard: excludeFromLeaderboard, CreatedAt: time.Now(),
		})
	})
	if err != nil {
		s.Logger.Warn("achievements.xp failed", "key", key, "tier", spec.Tier, "error", err)
	}
}
