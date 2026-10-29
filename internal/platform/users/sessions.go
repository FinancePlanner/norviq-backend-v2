package users

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"norviq-api/internal/store/usersdb"

	"github.com/google/uuid"
)

// SparedSession is the caller's own session, kept alive through a revocation
// it triggered (password or email change). Both halves are optional: an
// older client may send no refresh token, and a pre-1.4.1 access token has
// no `iat`.
type SparedSession struct {
	RefreshTokenHash    *string
	AccessTokenIssuedAt *time.Time
}

// RevokeAllTokens ends every session the user has: refresh tokens, personal
// access (MCP) tokens, OAuth server tokens and approved-but-unredeemed OAuth
// codes, and moves users.sessions_valid_after forward so access JWTs issued
// before it are refused too.
//
// With spared, the caller's refresh token survives and the cutoff becomes
// its access token's iat instead of now (no cutoff at all when that iat is
// unknown). Run it inside the caller's transaction: db must be a pgx.Tx when
// it has to commit together with another write.
func RevokeAllTokens(ctx context.Context, db usersdb.DBTX, userID uuid.UUID, spared *SparedSession, now time.Time) error {
	q := usersdb.New(db)
	cutoff := &now
	var sparedHash *string
	if spared != nil {
		cutoff = spared.AccessTokenIssuedAt
		sparedHash = spared.RefreshTokenHash
	}
	if cutoff != nil {
		if err := q.AdvanceSessionsValidAfter(ctx, usersdb.AdvanceSessionsValidAfterParams{
			Cutoff: *cutoff, Now: now, ID: userID,
		}); err != nil {
			return err
		}
	}
	if err := q.RevokeUserRefreshTokens(ctx, usersdb.RevokeUserRefreshTokensParams{
		Now: now, UserID: userID, SparedHash: sparedHash,
	}); err != nil {
		return err
	}
	if err := q.RevokeUserPersonalAccessTokens(ctx, usersdb.RevokeUserPersonalAccessTokensParams{UserID: userID, RevokedAt: &now}); err != nil {
		return err
	}
	if err := q.RevokeUserOAuthTokens(ctx, usersdb.RevokeUserOAuthTokensParams{UserID: userID, RevokedAt: &now}); err != nil {
		return err
	}
	return q.DenyUserApprovedOAuthFlows(ctx, &userID)
}

// HashToken is the SHA-256 hex digest Vapor stores for refresh tokens, reset
// codes, MFA codes and opaque third-party tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
