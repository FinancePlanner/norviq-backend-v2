package onboarding_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"norviq-api/internal/platform/onboarding"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func TestLatchesStampOnce(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES ($1, 'l@example.com', 'h', now(), true)`, id); err != nil {
		t.Fatal(err)
	}
	if err := onboarding.Stamp(ctx, db, onboarding.LatchHolding, id); err != nil {
		t.Fatal(err)
	}
	first, _ := onboarding.FetchOrCreate(ctx, db, id)
	time.Sleep(1100 * time.Millisecond)
	onboarding.StampBestEffort(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), onboarding.LatchHolding, id)
	onboarding.StampBestEffort(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), onboarding.LatchGoal, id)
	again, _ := onboarding.FetchOrCreate(ctx, db, id)
	if first.FirstHoldingAt == nil || !again.FirstHoldingAt.Equal(*first.FirstHoldingAt) || again.FirstGoalAt == nil || again.FirstBudgetAt != nil {
		t.Fatalf("%+v", again)
	}
	// A failing latch never fails the caller: an unknown user only logs.
	onboarding.StampBestEffort(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), onboarding.LatchBudget, uuid.New())
	unchanged, err := onboarding.Apply(ctx, db, id, onboarding.Patch{}, time.Now())
	if err != nil || unchanged.UpdatedAt == nil && again.UpdatedAt != nil {
		t.Fatalf("%v", err)
	}
}
