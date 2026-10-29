// Package portfolios holds the portfolio plumbing several Vapor controllers
// shared: PortfolioAccessService (who may see or edit a portfolio), the
// default-list resolution from Stocks/ListSupport.swift, PilotFollowGuard,
// PortfolioCashResolver, ManualAccountResolver and PortfolioAccountResolver.
// Feature packages (stocks, portfolio, portfoliomgmt, rebalancing, retirement,
// simulations, …) import it instead of restating the rules.
package portfolios

import (
	"context"
	"errors"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/portfoliocoredb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PortfolioPurpose, PortfolioOwnership, PortfolioMode and PortfolioRole raw
// values (StockPlanShared.PortfolioManagementDTOs).
const (
	PurposePersonal   = "personal"
	PurposeRetirement = "retirement"

	OwnershipIndividual = "individual"
	OwnershipJoint      = "joint"

	ModeActual       = "actual"
	ModeHypothetical = "hypothetical"

	RoleOwner  = "owner"
	RoleEditor = "editor"
)

// Capabilities is StockPlanShared.PortfolioCapabilities.
type Capabilities struct {
	CanView              bool `json:"canView"`
	CanEdit              bool `json:"canEdit"`
	CanManageMembers     bool `json:"canManageMembers"`
	CanManageConnections bool `json:"canManageConnections"`
	CanArchive           bool `json:"canArchive"`
	CanDelete            bool `json:"canDelete"`
}

// AccessContext is PortfolioAccessContext.
type AccessContext struct {
	Portfolio        portfoliocoredb.PortfolioList
	Role             string
	OwnerEntitlement entitlements.Snapshot
}

// IsOwner is PortfolioAccessContext.isOwner.
func (c AccessContext) IsOwner() bool { return c.Role == RoleOwner }

// IsAdvanced is PortfolioAccessContext.isAdvanced: anything but a personal,
// individual, actual portfolio.
func (c AccessContext) IsAdvanced() bool {
	return IsAdvancedPortfolio(c.Portfolio)
}

// IsAdvancedPortfolio is the isAdvanced rule on a bare row.
func IsAdvancedPortfolio(p portfoliocoredb.PortfolioList) bool {
	return p.Purpose != PurposePersonal || p.Ownership != OwnershipIndividual || p.Mode != ModeActual
}

// CanEdit is PortfolioAccessContext.canEdit.
func (c AccessContext) CanEdit() bool {
	if c.IsAdvanced() && !c.OwnerEntitlement.IsPro() {
		return false
	}
	return c.Role == RoleOwner || c.Role == RoleEditor
}

// Capabilities is PortfolioAccessContext.capabilities.
func (c AccessContext) Capabilities() Capabilities {
	return Capabilities{
		CanView:              true,
		CanEdit:              c.CanEdit(),
		CanManageMembers:     c.IsOwner() && c.OwnerEntitlement.IsPro(),
		CanManageConnections: c.IsOwner() && c.CanEdit() && c.Portfolio.Mode == ModeActual,
		CanArchive:           c.IsOwner(),
		CanDelete:            c.IsOwner(),
	}
}

// Resolver is the entitlement lookup Access needs; *entitlements.Service
// satisfies it.
type Resolver interface {
	Resolve(ctx context.Context, userID uuid.UUID) (entitlements.Snapshot, error)
}

// Access is PortfolioAccessService.
type Access struct {
	Entitlements Resolver
}

// NewAccess builds the service on the environment's entitlement config, as
// configure.swift did with app.entitlementResolver.
func NewAccess(db portfoliocoredb.DBTX) Access {
	return Access{Entitlements: entitlements.NewFromEnv(db)}
}

// RequireOptions are the editing/ownerOnly flags of require(...).
type RequireOptions struct {
	Editing   bool
	OwnerOnly bool
}

// ErrPortfolioNotFound is Abort(.notFound, reason: "Portfolio not found.").
func ErrPortfolioNotFound() *httpx.Error { return httpx.NotFound("Portfolio not found.") }

// Require is PortfolioAccessService.require: 404 when the portfolio does not
// exist or the user is neither its owner nor an active member, 403 for
// ownerOnly, and BillingUpgradeRequiredError(.advancedPortfolios) when editing
// an advanced portfolio whose owner is not Pro.
func (a Access) Require(ctx context.Context, db portfoliocoredb.DBTX, portfolioID, userID uuid.UUID, opt RequireOptions) (AccessContext, error) {
	q := portfoliocoredb.New(db)
	p, err := q.GetPortfolioList(ctx, portfolioID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessContext{}, ErrPortfolioNotFound()
	}
	if err != nil {
		return AccessContext{}, err
	}
	var role string
	if p.UserID == userID {
		role = RoleOwner
	} else {
		roles, err := q.ActiveMembershipRole(ctx, portfoliocoredb.ActiveMembershipRoleParams{PortfolioID: portfolioID, UserID: userID})
		if err != nil {
			return AccessContext{}, err
		}
		if len(roles) == 0 {
			return AccessContext{}, ErrPortfolioNotFound()
		}
		// PortfolioRole(rawValue:) ?? .editor
		role = roles[0]
		if role != RoleOwner && role != RoleEditor {
			role = RoleEditor
		}
	}
	snap, err := a.Entitlements.Resolve(ctx, p.UserID)
	if err != nil {
		return AccessContext{}, err
	}
	c := AccessContext{Portfolio: p, Role: role, OwnerEntitlement: snap}
	if opt.OwnerOnly && !c.IsOwner() {
		return AccessContext{}, httpx.Forbidden("Only the portfolio owner can perform this action.")
	}
	if opt.Editing && !c.CanEdit() {
		return AccessContext{}, entitlements.UpgradeRequired(entitlements.FeatureAdvancedPortfolios, snap.Level)
	}
	return c, nil
}
