// Package users is the `users` table as every domain needs it: load a user
// with its PII decrypted (Vapor's User + hydrateProtectedFields), create
// one, change one field group at a time, hash and verify passwords, and end
// every session a user has (AuthRepository.revokeAllTokens).
package users

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/store/usersdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// User is a row of `users` with its protected fields decrypted.
type User struct {
	usersdb.User

	// Decrypted values; fall back to the legacy plaintext columns.
	Bio                         *string
	HouseholdPartnerDisplayName *string
	DateOfBirth                 *time.Time
}

// Store reads and writes users. It is cheap; build one per process.
type Store struct {
	DB  usersdb.DBTX
	PII *crypto.PII
}

// NewStore binds the store to a pool (or any DBTX) and the PII engine.
func NewStore(db usersdb.DBTX, pii *crypto.PII) *Store { return &Store{DB: db, PII: pii} }

// Q returns the queries bound to db, or to the store's own pool when db is nil.
func (s *Store) Q(db usersdb.DBTX) *usersdb.Queries {
	if db == nil {
		db = s.DB
	}
	return usersdb.New(db)
}

// ByID loads a user; (nil, nil) when there is none.
func (s *Store) ByID(ctx context.Context, db usersdb.DBTX, id uuid.UUID) (*User, error) {
	return s.hydrate(s.Q(db).UserByID(ctx, id))
}

// ByEmail loads by the stored (already normalized) email.
func (s *Store) ByEmail(ctx context.Context, db usersdb.DBTX, email string) (*User, error) {
	return s.hydrate(s.Q(db).UserByEmail(ctx, email))
}

// ByUsername loads by the stored username.
func (s *Store) ByUsername(ctx context.Context, db usersdb.DBTX, username string) (*User, error) {
	u := username
	return s.hydrate(s.Q(db).UserByUsername(ctx, &u))
}

// Exists reports whether a user row exists, without decrypting anything.
func (s *Store) Exists(ctx context.Context, db usersdb.DBTX, id uuid.UUID) (bool, error) {
	_, err := s.Q(db).UserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) hydrate(row usersdb.User, err error) (*User, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u := &User{User: row, Bio: row.Bio, HouseholdPartnerDisplayName: row.HouseholdPartnerDisplayName, DateOfBirth: row.DateOfBirth}
	if row.BioEncrypted != nil {
		v, err := s.PII.DecryptString(row.BioEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt bio: %w", err)
		}
		u.Bio = &v
	}
	if row.HouseholdPartnerDisplayNameEncrypted != nil {
		v, err := s.PII.DecryptString(row.HouseholdPartnerDisplayNameEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt partner name: %w", err)
		}
		u.HouseholdPartnerDisplayName = &v
	}
	if row.DateOfBirthEncrypted != nil {
		v, err := s.PII.DecryptString(row.DateOfBirthEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt date of birth: %w", err)
		}
		millis, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return nil, crypto.ErrInvalidPayload
		}
		dob := time.UnixMilli(millis).UTC()
		u.DateOfBirth = &dob
	}
	return u, nil
}

// encryptedPII seals the decrypted fields the way encryptProtectedFields
// does: date of birth as milliseconds since 1970, rounded.
func (s *Store) encryptedPII(bio, partner *string, dob *time.Time) (bioEnc, partnerEnc, dobEnc []byte, err error) {
	if bio != nil {
		if bioEnc, err = s.PII.EncryptString(*bio); err != nil {
			return nil, nil, nil, err
		}
	}
	if partner != nil {
		if partnerEnc, err = s.PII.EncryptString(*partner); err != nil {
			return nil, nil, nil, err
		}
	}
	if dob != nil {
		millis := int64(math.Round(float64(dob.UnixNano()) / 1e6))
		if dobEnc, err = s.PII.EncryptString(strconv.FormatInt(millis, 10)); err != nil {
			return nil, nil, nil, err
		}
	}
	return bioEnc, partnerEnc, dobEnc, nil
}

// RewritePII re-seals u's protected fields and clears the plaintext
// columns. Vapor does this on every save of a hydrated user, so ports call
// it wherever Vapor's persistUser or repository save ran.
func (s *Store) RewritePII(ctx context.Context, db usersdb.DBTX, u *User, now time.Time) error {
	bio, partner, dob, err := s.encryptedPII(u.Bio, u.HouseholdPartnerDisplayName, u.DateOfBirth)
	if err != nil {
		return err
	}
	return s.Q(db).RewriteUserPII(ctx, usersdb.RewriteUserPIIParams{
		ID: u.ID, BioEncrypted: bio, HouseholdPartnerDisplayNameEncrypted: partner,
		DateOfBirthEncrypted: dob, UpdatedAt: &now,
	})
}

// NewUser is what account creation sets; everything else starts at its default.
type NewUser struct {
	Username        string
	Email           string
	PasswordHash    string
	PasswordSetAt   *time.Time
	EmailVerifiedAt *time.Time
	DateOfBirth     time.Time
}

// Create inserts the user (DatabaseAuthRepository.createUser) and returns it hydrated.
func (s *Store) Create(ctx context.Context, db usersdb.DBTX, n NewUser, now time.Time) (*User, error) {
	_, _, dobEnc, err := s.encryptedPII(nil, nil, &n.DateOfBirth)
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	username := n.Username
	if err := s.Q(db).InsertUser(ctx, usersdb.InsertUserParams{
		ID: id, Email: n.Email, PasswordHash: n.PasswordHash, Username: &username,
		DateOfBirthEncrypted: dobEnc, EmailVerifiedAt: n.EmailVerifiedAt,
		PasswordSetAt: n.PasswordSetAt, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	return s.ByID(ctx, db, id)
}

// System identity of the Daily board account (Community/DailyBoardPost.swift).
const (
	SystemUsername = "norviq"
	SystemEmail    = "daily-board@system.norviq.invalid"
)

// IsSystemUsername reports whether raw names the Daily board account.
func IsSystemUsername(raw string) bool {
	return strings.ToLower(strings.TrimSpace(raw)) == SystemUsername
}

// IsSystemEmail reports whether raw is the Daily board account's address.
func IsSystemEmail(raw string) bool { return strings.ToLower(strings.TrimSpace(raw)) == SystemEmail }

// NormalizeEmail trims and lowercases (normalizeEmail in Vapor).
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
