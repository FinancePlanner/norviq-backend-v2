package users_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/testutil"
)

// vaporHash is what Vapor's `.bcrypt(cost: 11)` stored for "Str0ng!Pass",
// read back from a user registered through the running Vapor backend.
const vaporHash = "$2b$11$tC9a6D7mNxanY5Tn1lRvp.TMVLtgAZfe3yNJtosmgtB3C1r21p9Ru"

func TestPasswordsInteroperateWithVapor(t *testing.T) {
	if ok, err := users.VerifyPassword("Str0ng!Pass", vaporHash); err != nil || !ok {
		t.Fatalf("Vapor hash rejected: %v %v", ok, err)
	}
	if ok, _ := users.VerifyPassword("str0ng!Pass", vaporHash); ok {
		t.Fatal("wrong password accepted")
	}
	h, err := users.HashPassword("Str0ng!Pass")
	if err != nil || !strings.HasPrefix(h, "$2b$11$") {
		t.Fatalf("%q %v", h, err)
	}
	long := strings.Repeat("a", 80)
	h, _ = users.HashPassword(long)
	if ok, _ := users.VerifyPassword(long[:72]+"different", h); !ok {
		t.Fatal("bcrypt reads only the first 72 bytes")
	}
	if _, err := users.VerifyPassword("x", "not-a-hash"); err == nil {
		t.Fatal("a malformed hash is an error")
	}
}

func TestStoreRoundTripsPII(t *testing.T) {
	db := testutil.DB(t)
	pii, err := crypto.PIIFromEnv(false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	s := users.NewStore(db, pii)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	dob := time.Date(1991, 2, 3, 0, 0, 0, 0, time.UTC)
	u, err := s.Create(ctx, nil, users.NewUser{Username: "store", Email: "store@example.com", PasswordHash: "h", DateOfBirth: dob}, now)
	if err != nil {
		t.Fatal(err)
	}
	bio := "hello"
	u.Bio = &bio
	if err := s.RewritePII(ctx, nil, u, now); err != nil {
		t.Fatal(err)
	}
	for _, get := range []func() (*users.User, error){
		func() (*users.User, error) { return s.ByID(ctx, nil, u.ID) },
		func() (*users.User, error) { return s.ByEmail(ctx, nil, "store@example.com") },
		func() (*users.User, error) { return s.ByUsername(ctx, nil, "store") },
	} {
		got, err := get()
		if err != nil || got == nil || got.Bio == nil || *got.Bio != "hello" || got.DateOfBirth == nil || !got.DateOfBirth.Equal(dob) {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if missing, err := s.ByEmail(ctx, nil, "nobody@example.com"); missing != nil || err != nil {
		t.Fatal("missing user is (nil, nil)")
	}
	if ok, _ := s.Exists(ctx, nil, u.ID); !ok {
		t.Fatal("exists")
	}
	if !users.IsSystemUsername(" Norviq ") || !users.IsSystemEmail("Daily-Board@system.norviq.invalid") || users.NormalizeEmail(" A@B.C ") != "a@b.c" {
		t.Fatal("identity helpers")
	}
}

func TestRevokeAllTokensSparesTheCaller(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	pii, _ := crypto.PIIFromEnv(false, slog.New(slog.DiscardHandler))
	s := users.NewStore(db, pii)
	now := time.Now().UTC().Truncate(time.Second)
	u, err := s.Create(ctx, nil, users.NewUser{Username: "revoker", Email: "revoker@example.com", PasswordHash: "h"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"keep", "drop"} {
		if _, err := db.Exec(ctx, `INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at) VALUES (gen_random_uuid(), $1, $2, $3, $4)`,
			u.ID, users.HashToken(tok), now.Add(time.Hour), now); err != nil {
			t.Fatal(err)
		}
	}
	keep := users.HashToken("keep")
	iat := now.Add(-time.Minute)
	if err := users.RevokeAllTokens(ctx, db, u.ID, &users.SparedSession{RefreshTokenHash: &keep, AccessTokenIssuedAt: &iat}, now); err != nil {
		t.Fatal(err)
	}
	var live int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE user_id=$1 AND revoked_at IS NULL`, u.ID).Scan(&live)
	got, _ := s.ByID(ctx, nil, u.ID)
	if live != 1 || got.SessionsValidAfter == nil || !got.SessionsValidAfter.Equal(iat) {
		t.Fatalf("live=%d cutoff=%v", live, got.SessionsValidAfter)
	}
	// The cutoff never moves back.
	earlier := now.Add(-time.Hour)
	_ = users.RevokeAllTokens(ctx, db, u.ID, &users.SparedSession{AccessTokenIssuedAt: &earlier}, now)
	got, _ = s.ByID(ctx, nil, u.ID)
	if !got.SessionsValidAfter.Equal(iat) {
		t.Fatal("cutoff moved back")
	}
	if users.HashToken("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("sha256 hex")
	}
}
