// Package admin ports the /v1/admin moderation controllers:
// CommunityAdminController (board deletion, mutes and bans, the board and
// group-message report queue) and SocialModerationController (the social
// report queue and social suspensions). Both sit behind the AdminGuard
// allowlist (NORVIQ_ADMIN_USER_IDS).
//
// PUT /v1/admin/articles/:ref/visibility lives with the articles port, which
// owns the article lookup and the community viewer it needs.
package admin

import (
	"context"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/server"
	"norviq-api/internal/store/admindb"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/admin/{boards,community,social}.
type Feature struct {
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "admin" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{d: d, q: admindb.New(d.DB), now: f.Now}
	if h.now == nil {
		h.now = time.Now
	}

	// CommunityAdminController.boot: ScopedBearerAuthenticator, guard,
	// FirstPartyOnlyMiddleware, then AdminOnlyMiddleware.
	community := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.FirstPartyOnly(), d.Admin.Middleware()}
	v1 := r.V1
	v1.DELETE("/admin/boards/:slug", h.deleteBoard, community...)
	v1.GET("/admin/community/sanctions", h.listSanctions, community...)
	v1.POST("/admin/community/sanctions", h.createSanction, community...)
	v1.DELETE("/admin/community/sanctions/:sanctionId", h.revokeSanction, community...)
	v1.GET("/admin/community/reports", h.listReports, community...)
	v1.POST("/admin/community/reports/:reportId/resolve", h.resolveReport, community...)

	// SocialModerationController.boot: SessionToken.authenticator, guard,
	// SocialModeratorMiddleware (AdminGuard.requireAdmin).
	social := []echo.MiddlewareFunc{d.Auth.Session(), auth.Guard(), d.Admin.Middleware()}
	v1.GET("/admin/social/reports", h.socialReports, social...)
	v1.POST("/admin/social/reports/:reportId/resolve", h.resolveSocialReport, social...)
	v1.POST("/admin/social/users/:userId/suspend", h.suspend, social...)
	v1.DELETE("/admin/social/users/:userId/suspend", h.unsuspend, social...)
	return nil
}

type handler struct {
	d   *server.Deps
	q   *admindb.Queries
	now func() time.Time
}

// inTx runs fn in one transaction, as `req.db.transaction` did.
func (h *handler) inTx(ctx context.Context, fn func(q *admindb.Queries) error) error {
	return pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error { return fn(h.q.WithTx(tx)) })
}
