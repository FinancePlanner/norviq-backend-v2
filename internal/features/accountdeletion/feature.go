// Package accountdeletion ports UserProfileService.delete behind
// DELETE /v1/users and DELETE /v1/users/:id (UserProfileController).
//
// Vapor deletes the account with one `user.delete(on:)`, a hard
// `DELETE FROM users WHERE id = $1`, and lets the schema's foreign keys do the
// rest: every ON DELETE CASCADE / SET NULL constraint across every domain
// (see deletion_test.go for the full list, asserted against the schema).
// Before that it revokes each bank connection at its aggregator (best
// effort) and removes the user's export files from disk, which no cascade
// reaches.
//
// Deliberate fix: two RESTRICT foreign keys (allocation_models and
// rebalance_plans created_by_user_id) made the delete fail with a 500 for any
// user who had created a model or plan, after the bank links were already
// revoked. v2 clears those rows first, deletes in one transaction, and
// revokes the bank links only once the delete has committed.
package accountdeletion

import (
	"context"
	"log/slog"
	"net/http"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/dataexport"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/bankrevoke"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/accountdeletiondb"
	"norviq-api/internal/store/dataexportdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

// BankRevoker is BankConnectionRevoker: best effort, never fails.
type BankRevoker interface {
	Revoke(ctx context.Context, c bankrevoke.Connection)
}

// Feature mounts the two DELETE routes.
type Feature struct {
	// Revoker overrides the environment-built bank revoker (tests).
	Revoker BankRevoker
}

// Name implements server.Feature.
func (Feature) Name() string { return "accountdeletion" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	revoker := f.Revoker
	if revoker == nil {
		vault, err := crypto.VaultFromEnv(d.Config.IsProduction(), d.Logger)
		if err != nil {
			// Plaid revocations then fail (and are logged); deletion still
			// goes through, as it would after any provider failure.
			d.Logger.Warn("account deletion: token vault unavailable", "error", err)
			vault = nil
		}
		revoker = bankrevoke.FromEnv(vault, nil, d.Logger)
	}
	h := &handler{db: d.DB, q: accountdeletiondb.New(d.DB), exports: dataexportdb.New(d.DB), revoker: revoker, logger: d.Logger}

	// UserProfileController.boot: the first-party session authenticator,
	// guard, FirstPartyOnlyMiddleware. No rate limit.
	firstParty := []echo.MiddlewareFunc{d.Auth.Session(), auth.Guard(), auth.FirstPartyOnly()}
	r.V1.DELETE("/users", h.deleteProfile, firstParty...)
	r.V1.DELETE("/users/:id", h.deleteProfileByID, firstParty...)
	return nil
}

type handler struct {
	db      *pgxpool.Pool
	q       *accountdeletiondb.Queries
	exports *dataexportdb.Queries
	revoker BankRevoker
	logger  *slog.Logger
}

// deleteResponse is DeleteUserProfileResponse.
type deleteResponse struct {
	Success bool    `json:"success"`
	Message *string `json:"message,omitempty"`
}

// DELETE /v1/users
func (h *handler) deleteProfile(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	return h.delete(c, s.UserID)
}

// DELETE /v1/users/:id — only the caller's own id.
func (h *handler) deleteProfileByID(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return httpx.BadRequest("Invalid user ID")
	}
	if id != s.UserID {
		return httpx.Forbidden("You are not allowed to access this user profile")
	}
	return h.delete(c, id)
}

// delete is UserProfileService.delete.
func (h *handler) delete(c *echo.Context, userID uuid.UUID) error {
	ctx := c.Request().Context()
	exists, err := h.q.UserExistsForDeletion(ctx, userID)
	if err != nil {
		return err
	}
	if !exists {
		return httpx.Abort(http.StatusUnauthorized, "User not found")
	}
	if err := Delete(ctx, h.db, h.exports, h.revoker, h.logger, userID); err != nil {
		return err
	}
	msg := "User account deleted"
	return httpx.OK(c, deleteResponse{Success: true, Message: &msg})
}

// Delete removes the user: the users row and everything the schema cascades
// from it, in one transaction, then the export files and the bank links at
// their aggregators. Revocation comes after the commit so a failed delete
// never leaves a live account with its banks disconnected; the connection
// rows (and their tokens) are read before they cascade away.
func Delete(ctx context.Context, db *pgxpool.Pool, exports *dataexportdb.Queries, revoker BankRevoker, logger *slog.Logger, userID uuid.UUID) error {
	q := accountdeletiondb.New(db)
	conns, err := q.BankConnectionsForUser(ctx, userID)
	if err != nil {
		return err
	}
	// Export files sit on disk outside the database; the rows cascade, the
	// files would not.
	files, err := exports.ExportFilesForUser(ctx, userID)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		q := q.WithTx(tx)
		for _, step := range []func(context.Context, uuid.UUID) error{
			q.DeleteOwnRebalancePlans,
			q.DeleteOwnAllocationModels,
			q.ReassignRebalancePlans,
			q.ReassignAllocationModels,
			q.DeleteUser,
		} {
			if err := step(ctx, userID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, f := range files {
		if !dataexport.RemoveFile(f.FilePath) {
			logger.Warn("user.delete export_file_not_removed export_id=" + apijson.FormatUUID(f.ID))
		}
	}
	// Sign in with Apple stores no Apple refresh token, so there is nothing
	// to revoke at Apple.
	for _, conn := range conns {
		revoker.Revoke(ctx, bankrevoke.Connection{
			ID: conn.ID, Provider: conn.Provider, ProviderItemID: conn.ProviderItemID, AccessTokenEnc: conn.AccessTokenEnc,
		})
	}
	return nil
}
