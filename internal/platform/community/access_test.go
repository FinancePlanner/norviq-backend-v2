package community_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func TestViewerSanctionsAndGates(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES ($1, 'c@example.com', 'x', now(), true)`, id); err != nil {
		t.Fatal(err)
	}
	a := community.New(pool, func(uuid.UUID) bool { return false })

	v, err := a.Viewer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if v.IsBanned() || v.IsMuted() || v.ActiveSanction != nil {
		t.Fatalf("fresh viewer sanctioned: %+v", v)
	}
	var he *httpx.Error
	if err := v.RequireCanContribute(); !errors.As(err, &he) || he.Code != "username_required" {
		t.Fatalf("want username_required, got %v", err)
	}

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if _, err := pool.Exec(ctx, `INSERT INTO community_sanctions (id, user_id, kind, reason, expires_at, created_at) VALUES ($1, $2, 'mute', 'r', $3, now())`, uuid.New(), id, exp); err != nil {
		t.Fatal(err)
	}
	// Expired ban is ignored.
	if _, err := pool.Exec(ctx, `INSERT INTO community_sanctions (id, user_id, kind, reason, expires_at, created_at) VALUES ($1, $2, 'ban', 'r', now() - interval '1 hour', now())`, uuid.New(), id); err != nil {
		t.Fatal(err)
	}
	v, err = a.Viewer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsMuted() || v.IsBanned() {
		t.Fatalf("want muted only: %+v", v.ActiveSanction)
	}
	if err := v.RequireCanContribute(); !errors.As(err, &he) || he.Code != "community_muted" || he.Details["expiresAt"] != exp.Format("2006-01-02T15:04:05Z") {
		t.Fatalf("want community_muted with expiresAt, got %v %v", err, he.Details)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO community_sanctions (id, user_id, kind, reason, created_at) VALUES ($1, $2, 'ban', 'r', now())`, uuid.New(), id); err != nil {
		t.Fatal(err)
	}
	v, _ = a.Viewer(ctx, id)
	if !v.IsBanned() || v.ActiveSanction.Kind != "ban" {
		t.Fatalf("ban must outrank mute: %+v", v.ActiveSanction)
	}

	if _, err := a.Viewer(ctx, uuid.New()); !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("missing user: want 401, got %v", err)
	}
}
