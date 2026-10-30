package goalplanning

import (
	"context"
	"os"
	"strconv"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/config"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/goalplanningdb"
	"norviq-api/internal/store/notificationsdb"
)

// dailyJobLock is the JobLock name GoalPlanningDailyJob runs under.
const dailyJobLock = "goal_planning_daily_job"

// Publisher is NotificationEventPublisher.publishAndPush.
type Publisher interface {
	PublishAndPush(ctx context.Context, m inbox.Message) (inbox.Event, error)
}

// DailyJob is GoalPlanningDailyJob: configured only when
// GOAL_PLANNING_ENABLED and GOAL_EVALUATOR_ENABLED (both default on); every
// max(300, GOAL_EVALUATOR_INTERVAL_SECONDS) seconds (default 86400, first
// tick after 120 s) it snapshots each active goal once a day, refreshes its
// suggestions, and notifies milestones and sustained drift.
type DailyJob struct{}

// Name is the V2_JOBS key.
func (DailyJob) Name() string { return dailyJobLock }

// JobEnabled is configureGoalPlanningJob's guard.
func JobEnabled() bool {
	return config.Bool(os.Getenv("GOAL_PLANNING_ENABLED"), true) && config.Bool(os.Getenv("GOAL_EVALUATOR_ENABLED"), true)
}

// jobInterval is max(300, GOAL_EVALUATOR_INTERVAL_SECONDS ?? 86400).
func jobInterval() time.Duration {
	seconds := int64(86400)
	if v, err := strconv.ParseInt(os.Getenv("GOAL_EVALUATOR_INTERVAL_SECONDS"), 10, 64); err == nil {
		seconds = v
	}
	return time.Duration(max(300, seconds)) * time.Second
}

// Run blocks until ctx ends.
func (DailyJob) Run(ctx context.Context, d *server.Deps) {
	if !JobEnabled() {
		d.Logger.Info("goal-planning daily job disabled by GOAL_PLANNING_ENABLED/GOAL_EVALUATOR_ENABLED")
		return
	}
	svc, err := Feature{}.service(d)
	if err != nil {
		d.Logger.Error("goal-planning daily job disabled", "error", err)
		return
	}
	p, err := push.From(d)
	if err != nil {
		d.Logger.Error("goal-planning daily job disabled", "error", err)
		return
	}
	// Vapor offered the event to APNS then Telegram; the Telegram channel
	// arrives with the messaging port.
	job := &dailyRunner{svc: svc, pub: inbox.NewPublisher(notificationsdb.New(d.DB), p, d.Logger)}
	interval := jobInterval()
	select {
	case <-ctx.Done():
		return
	case <-time.After(120 * time.Second):
	}
	for {
		joblock.RunAsLeader(ctx, d.DB, d.Logger, dailyJobLock, func(ctx context.Context) {
			if err := job.runOnce(ctx); err != nil && ctx.Err() == nil {
				d.Logger.Error("goal-planning daily evaluation failed", "error", err)
			}
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

type dailyRunner struct {
	svc *service
	pub Publisher
}

// runOnce is GoalPlanningDailyJob.runOnceAsLeader: the first failing goal
// aborts the run (and the prune), as the Swift loop's `try` did.
func (j *dailyRunner) runOnce(ctx context.Context) error {
	goals, err := j.svc.q.ListAllActiveFinancialGoals(ctx)
	if err != nil {
		return err
	}
	for i := range goals {
		if ctx.Err() != nil {
			break
		}
		if err := j.evaluate(ctx, &goals[i]); err != nil {
			return err
		}
	}
	cutoff := j.svc.now().UTC().AddDate(0, 0, -400)
	return j.svc.q.PruneDailySnapshots(ctx, cutoff)
}

func (j *dailyRunner) evaluate(ctx context.Context, goal *goalplanningdb.FinancialGoal) error {
	exists, err := j.svc.q.SnapshotExistsSince(ctx, goalplanningdb.SnapshotExistsSinceParams{GoalID: goal.ID, Since: startOfDay(j.svc.now())})
	if err != nil || exists {
		return err
	}
	prior, err := j.svc.q.LatestSnapshots(ctx, goal.ID)
	if err != nil {
		return err
	}
	progress, err := j.svc.progress(ctx, goal, goal.UserID, overrides{})
	if err != nil {
		return err
	}
	if err := j.svc.persistSnapshot(ctx, &progress, goal, goal.UserID); err != nil {
		return err
	}
	if _, err := j.svc.suggestions(ctx, goal, goal.UserID); err != nil {
		return err
	}
	if len(progress.Warnings) > 0 {
		return nil
	}
	var previous *goalplanningdb.FinancialGoalProgressSnapshot
	if len(prior) > 0 {
		previous = &prior[0]
	}
	if err := j.publishMilestone(ctx, &progress, previous, goal); err != nil {
		return err
	}
	return j.publishDrift(ctx, &progress, previous, goal)
}

func deepLink(goal *goalplanningdb.FinancialGoal) *string {
	s := "norviq://financial-goals/" + apijson.FormatUUID(goal.ID)
	return &s
}

// publishMilestone is publishMilestoneIfNeeded.
func (j *dailyRunner) publishMilestone(ctx context.Context, p *Progress, previous *goalplanningdb.FinancialGoalProgressSnapshot, goal *goalplanningdb.FinancialGoal) error {
	previousPercent := 0.0
	if previous != nil {
		previousPercent = previous.CurrentValue / goal.TargetAmount
	}
	milestone := 0.0
	for _, m := range []float64{0.25, 0.5, 0.75, 1.0} {
		if previousPercent < m && p.PercentComplete >= m {
			milestone = m
		}
	}
	if milestone == 0 {
		return nil
	}
	percent := strconv.Itoa(int(milestone * 100))
	id := apijson.FormatUUID(goal.ID)
	_, err := j.pub.PublishAndPush(ctx, inbox.Message{
		UserID:           goal.UserID,
		Kind:             inbox.KindFinancialGoal,
		DeduplicationKey: "financial-goal:" + id + ":milestone:" + percent,
		Title:            goal.Name + ": " + percent + "% complete",
		Body:             "Your goal has reached a new progress milestone.",
		DeepLink:         deepLink(goal),
		Payload:          map[string]string{"goal_id": id, "milestone": percent},
	})
	return err
}

// publishDrift is publishDriftIfNeeded.
func (j *dailyRunner) publishDrift(ctx context.Context, p *Progress, latest *goalplanningdb.FinancialGoalProgressSnapshot, goal *goalplanningdb.FinancialGoal) error {
	if latest == nil || latest.DriftState != p.DriftState || (p.DriftState != driftAhead && p.DriftState != driftBehind) {
		return nil
	}
	period := dateString(j.svc.now())[:7]
	title := goal.Name + " is ahead"
	body := "The plan has remained ahead for two evaluations. You may have contribution headroom."
	if p.DriftState == driftBehind {
		title = goal.Name + " needs attention"
		body = "The plan has remained behind for two evaluations. Review deterministic adjustment options."
	}
	id := apijson.FormatUUID(goal.ID)
	_, err := j.pub.PublishAndPush(ctx, inbox.Message{
		UserID:           goal.UserID,
		Kind:             inbox.KindFinancialGoal,
		DeduplicationKey: "financial-goal:" + id + ":drift:" + p.DriftState + ":" + period,
		Title:            title,
		Body:             body,
		DeepLink:         deepLink(goal),
		Payload:          map[string]string{"goal_id": id, "drift_state": p.DriftState},
	})
	return err
}
