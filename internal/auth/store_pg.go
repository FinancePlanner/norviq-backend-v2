package auth

import (
	"context"
	"errors"
	"time"

	"norviq-api/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PGStore implements Store over the sqlc queries.
type PGStore struct{ Q store.Querier }

func (s PGStore) SessionsValidAfter(ctx context.Context, userID uuid.UUID) (*time.Time, bool, error) {
	v, err := s.Q.UserSessionsValidAfter(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (s PGStore) PATByHash(ctx context.Context, hash string) (*PATRecord, error) {
	row, err := s.Q.PersonalAccessTokenByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &PATRecord{
		ID: row.ID, UserID: row.UserID, Scopes: row.Scopes,
		LastUsedAt: row.LastUsedAt, ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt,
	}, nil
}

func (s PGStore) TouchPAT(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.Q.TouchPersonalAccessToken(ctx, store.TouchPersonalAccessTokenParams{ID: id, LastUsedAt: &at})
}

func (s PGStore) OAuthByAccessHash(ctx context.Context, hash string) (*OAuthRecord, error) {
	row, err := s.Q.OAuthTokenByAccessHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &OAuthRecord{
		ID: row.ID, UserID: row.UserID, Scopes: row.Scopes,
		AccessExpiresAt: row.AccessExpiresAt, RevokedAt: row.RevokedAt,
	}, nil
}
