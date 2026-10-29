// Package onboarding owns the onboarding_state row: the funnel position the
// clients write, and the guided-start latches other domains stamp when a user
// first adds a holding, sets a budget or creates a goal
// (Onboarding/OnboardingLatches.swift, OnboardingStateService.swift).
package onboarding

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"norviq-api/internal/store/onboardingdb"

	"github.com/google/uuid"
)

// Latch is a guided-start step and the column it stamps once.
type Latch string

const (
	LatchHolding Latch = "first_holding_at"
	LatchBudget  Latch = "first_budget_at"
	LatchGoal    Latch = "first_goal_at"
)

// Stamp records the latch the first time; later calls keep the first
// timestamp. The row may not exist yet, so this upserts.
func Stamp(ctx context.Context, db onboardingdb.DBTX, latch Latch, userID uuid.UUID) error {
	q := onboardingdb.New(db)
	switch latch {
	case LatchHolding:
		return q.LatchFirstHolding(ctx, onboardingdb.LatchFirstHoldingParams{ID: uuid.New(), UserID: userID})
	case LatchBudget:
		return q.LatchFirstBudget(ctx, onboardingdb.LatchFirstBudgetParams{ID: uuid.New(), UserID: userID})
	case LatchGoal:
		return q.LatchFirstGoal(ctx, onboardingdb.LatchFirstGoalParams{ID: uuid.New(), UserID: userID})
	}
	return errors.New("unknown onboarding latch " + string(latch))
}

// StampBestEffort is Request.latchOnboarding: the user's action already
// succeeded, so a failed latch is logged and never returned.
func StampBestEffort(ctx context.Context, db onboardingdb.DBTX, logger *slog.Logger, latch Latch, userID uuid.UUID) {
	if err := Stamp(ctx, db, latch, userID); err != nil {
		logger.Warn("onboarding latch failed", "latch", string(latch), "user", userID.String(), "error", err)
	}
}

// FetchOrCreate reads the user's row, creating an empty one first. The
// insert is ON CONFLICT DO NOTHING so two first reads racing both succeed.
func FetchOrCreate(ctx context.Context, db onboardingdb.DBTX, userID uuid.UUID) (onboardingdb.OnboardingState, error) {
	q := onboardingdb.New(db)
	if err := q.EnsureOnboardingState(ctx, onboardingdb.EnsureOnboardingStateParams{ID: uuid.New(), UserID: userID}); err != nil {
		return onboardingdb.OnboardingState{}, err
	}
	return q.OnboardingStateByUser(ctx, userID)
}

// Patch is OnboardingPatchRequest after validation.
type Patch struct {
	FunnelStep           *string
	FunnelCompleted      *bool
	GuidedStartDismissed *bool
}

// Apply writes a validated patch: the step as given, completion one-way
// (first timestamp kept), dismissal two-way.
func Apply(ctx context.Context, db onboardingdb.DBTX, userID uuid.UUID, p Patch, now time.Time) (onboardingdb.OnboardingState, error) {
	row, err := FetchOrCreate(ctx, db, userID)
	if err != nil {
		return row, err
	}
	// Fluent writes only when a field was assigned, and then touches updated_at.
	changed := false
	if p.FunnelStep != nil {
		row.FunnelStep = p.FunnelStep
		changed = true
	}
	if p.FunnelCompleted != nil && *p.FunnelCompleted && row.FunnelCompletedAt == nil {
		row.FunnelCompletedAt = &now
		changed = true
	}
	if p.GuidedStartDismissed != nil {
		changed = true
		switch {
		case !*p.GuidedStartDismissed:
			row.GuidedStartDismissedAt = nil
		case row.GuidedStartDismissedAt == nil:
			row.GuidedStartDismissedAt = &now
		}
	}
	if !changed {
		return row, nil
	}
	row.UpdatedAt = &now
	err = onboardingdb.New(db).SaveOnboardingState(ctx, onboardingdb.SaveOnboardingStateParams{
		ID: row.ID, FunnelStep: row.FunnelStep, FunnelCompletedAt: row.FunnelCompletedAt,
		GuidedStartDismissedAt: row.GuidedStartDismissedAt, UpdatedAt: row.UpdatedAt,
	})
	return row, err
}
