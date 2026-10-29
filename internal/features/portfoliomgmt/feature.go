// Package portfoliomgmt ports Vapor's PortfolioManagementController: the
// /v1/portfolios CRUD (create, clone, archive, delete, compare), joint
// portfolio members and invitations, manual cash positions, account
// assignment, and POST /v1/portfolio-invitations/accept. Access rules,
// default-list resolution and the pilot-follow guard come from
// internal/platform/portfolios.
package portfoliomgmt

import (
	"log/slog"

	"norviq-api/internal/auth"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

// Feature mounts the routes. Zero values use the environment's mailer
// (Resend or console) and entitlement resolver; tests override them.
type Feature struct {
	Mailer       mailer.Mailer
	Entitlements portfolios.Resolver
}

// Name implements server.Feature.
func (Feature) Name() string { return "portfoliomgmt" }

type handlers struct {
	db     *pgxpool.Pool
	ent    portfolios.Resolver
	access portfolios.Access
	mail   mailer.Mailer
	log    *slog.Logger
}

// Register implements server.Feature, mirroring
// PortfolioManagementController.boot: ScopedBearerAuthenticator + guard, then
// portfolio:read or portfolio:write. The middleware is attached per route,
// not with a group Use: a group's Use would register catch-all routes under
// /v1/portfolios/:portfolioId and swallow the rebalancing and retirement
// routes other packages mount there.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	ent := f.Entitlements
	if ent == nil {
		ent = entitlements.NewFromEnv(d.DB)
	}
	mail := f.Mailer
	if mail == nil {
		mail = mailer.New(mailer.SettingsFromEnv(), d.Logger)
	}
	h := &handlers{db: d.DB, ent: ent, access: portfolios.Access{Entitlements: ent}, mail: mail, log: d.Logger}

	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePortfolioRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePortfolioWrite)}

	g := r.V1.Group("/portfolios")
	g.GET("", h.list, read...)
	g.GET("/compare", h.compare, read...)
	g.GET("/:portfolioId", h.get, read...)
	g.GET("/:portfolioId/members", h.members, read...)
	g.GET("/:portfolioId/invitations", h.invitations, read...)
	g.GET("/:portfolioId/cash", h.cashPositions, read...)
	g.GET("/:portfolioId/accounts", h.accounts, read...)

	g.POST("", h.create, write...)
	g.PATCH("/:portfolioId", h.update, write...)
	g.DELETE("/:portfolioId", h.delete, write...)
	g.POST("/:portfolioId/archive", h.archive, write...)
	g.POST("/:portfolioId/clone", h.clone, write...)
	g.DELETE("/:portfolioId/members/:membershipId", h.revokeMember, write...)
	g.POST("/:portfolioId/leave", h.leave, write...)
	g.POST("/:portfolioId/invitations", h.invite, write...)
	g.DELETE("/:portfolioId/invitations/:invitationId", h.revokeInvitation, write...)
	g.POST("/:portfolioId/cash", h.createCashPosition, write...)
	g.PUT("/:portfolioId/cash/:cashId", h.updateCashPosition, write...)
	g.DELETE("/:portfolioId/cash/:cashId", h.deleteCashPosition, write...)
	g.PUT("/:portfolioId/accounts/:accountId", h.assignAccount, write...)

	r.V1.POST("/portfolio-invitations/accept", h.acceptInvitation, write...)
	return nil
}
