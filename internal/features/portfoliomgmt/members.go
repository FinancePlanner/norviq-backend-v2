package portfoliomgmt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliomgmtdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

const (
	statusActive  = "active"
	statusRevoked = "revoked"
	statusLeft    = "left"
	statusPending = "pending"
	statusExpired = "expired"
	// statusAccepted is what AcceptInvitation stores.
	statusAccepted = "accepted"

	// maxJointEditors is "A joint portfolio supports at most five editors."
	maxJointEditors = 5
	invitationTTL   = 7 * 24 * time.Hour
)

// sha256Hex is the controller's sha256(_:): lowercase hex.
func sha256Hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// displayName is `user.username ?? user.email`.
func displayName(username *string, email string) string {
	if username != nil {
		return *username
	}
	return email
}

// GET /v1/portfolios/:portfolioId/members
func (h *handlers) members(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	q := portfoliomgmtdb.New(h.db)
	p := ac.Portfolio
	owners, err := q.UserBasics(ctx, p.UserID)
	if err != nil {
		return err
	}
	owner := Membership{
		ID:          "owner:" + apijson.FormatUUID(p.UserID),
		PortfolioID: apijson.FormatUUID(p.ID),
		UserID:      apijson.FormatUUID(p.UserID),
		DisplayName: "Owner",
		Email:       "",
		Role:        portfolios.RoleOwner,
		Status:      statusActive,
		JoinedAt:    ptr(portfolios.FormatISODateTime(p.CreatedAt)),
		CreatedAt:   portfolios.FormatISODateTime(p.CreatedAt),
	}
	if len(owners) > 0 {
		owner.DisplayName = displayName(owners[0].Username, owners[0].Email)
		owner.Email = owners[0].Email
	}
	records, err := q.ActiveMemberships(ctx, p.ID)
	if err != nil {
		return err
	}
	out := []Membership{owner}
	if len(records) == 0 {
		return httpx.OK(c, out)
	}
	ids := make([]uuid.UUID, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.UserID)
	}
	users, err := q.UsersByIDs(ctx, ids)
	if err != nil {
		return err
	}
	byID := make(map[uuid.UUID]portfoliomgmtdb.UsersByIDsRow, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}
	for _, r := range records {
		u, ok := byID[r.UserID]
		if !ok {
			continue
		}
		out = append(out, Membership{
			ID:          apijson.FormatUUID(r.ID),
			PortfolioID: apijson.FormatUUID(r.PortfolioID),
			UserID:      apijson.FormatUUID(r.UserID),
			DisplayName: displayName(u.Username, u.Email),
			Email:       u.Email,
			Role:        orDefault(r.Role, roles, portfolios.RoleEditor),
			Status:      orDefault(r.Status, membershipState, statusActive),
			JoinedAt:    portfolios.FormatISODateTimePtr(r.JoinedAt),
			CreatedAt:   formatOptional(r.CreatedAt),
		})
	}
	return httpx.OK(c, out)
}

// GET /v1/portfolios/:portfolioId/invitations
func (h *handlers) invitations(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{OwnerOnly: true})
	if err != nil {
		return err
	}
	rows, err := portfoliomgmtdb.New(h.db).InvitationsForPortfolio(c.Request().Context(), ac.Portfolio.ID)
	if err != nil {
		return err
	}
	out := make([]Invitation, 0, len(rows))
	for i := range rows {
		out = append(out, makeInvitation(&rows[i]))
	}
	return httpx.OK(c, out)
}

// POST /v1/portfolios/:portfolioId/invitations
func (h *handlers) invite(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{OwnerOnly: true})
	if err != nil {
		return err
	}
	if !ac.Capabilities().CanManageMembers {
		return entitlements.UpgradeRequired(entitlements.FeatureJointPortfolios, ac.OwnerEntitlement.Level)
	}
	o, err := readObject(c)
	if err != nil {
		return err
	}
	rawEmail, err := o.String("email")
	if err != nil {
		return err
	}
	role, err := o.Enum("role", "PortfolioRole", roles)
	if err != nil {
		return err
	}
	if role != portfolios.RoleEditor {
		return httpx.BadRequest("Invitations can only grant editor access.")
	}
	email := strings.ToLower(portfolios.TrimSwift(rawEmail))
	if !strings.Contains(email, "@") {
		return httpx.BadRequest("A valid email is required.")
	}

	ctx := c.Request().Context()
	q := portfoliomgmtdb.New(h.db)
	listID := ac.Portfolio.ID
	invited, err := q.UserIDsByEmail(ctx, email)
	if err != nil {
		return err
	}
	if len(invited) > 0 {
		if invited[0] == ac.Portfolio.UserID {
			return httpx.Conflict("The portfolio owner is already a member.")
		}
		active, err := q.ActiveMembershipForUser(ctx, portfoliomgmtdb.ActiveMembershipForUserParams{PortfolioID: listID, UserID: invited[0]})
		if err != nil {
			return err
		}
		if len(active) > 0 {
			return httpx.Conflict("This person is already a portfolio member.")
		}
	}
	pending, err := q.PendingInvitationForEmail(ctx, portfoliomgmtdb.PendingInvitationForEmailParams{PortfolioID: listID, Email: email})
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return httpx.Conflict("An active invitation already exists for this email address.")
	}
	activeCount, err := q.CountActiveMemberships(ctx, listID)
	if err != nil {
		return err
	}
	pendingCount, err := q.CountPendingInvitations(ctx, listID)
	if err != nil {
		return err
	}
	if activeCount+pendingCount >= maxJointEditors {
		return httpx.Conflict("A joint portfolio supports at most five editors.")
	}

	token := apijson.FormatUUID(uuid.New()) + "." + apijson.FormatUUID(uuid.New())
	now := portfolios.Now()
	record, err := q.InsertInvitation(ctx, portfoliomgmtdb.InsertInvitationParams{
		ID:          uuid.New(),
		PortfolioID: listID,
		Email:       email,
		Role:        portfolios.RoleEditor,
		Status:      statusPending,
		TokenHash:   sha256Hex(token),
		ExpiresAt:   now.Add(invitationTTL),
		CreatedAt:   &now,
	})
	if err != nil {
		return err
	}
	if err := q.SetPortfolioListOwnership(ctx, portfoliomgmtdb.SetPortfolioListOwnershipParams{
		ID: listID, Ownership: portfolios.OwnershipJoint, UpdatedAt: ptr(portfolios.Now()),
	}); err != nil {
		return err
	}
	// Sent after both writes, as in Vapor: a mailer failure (503) leaves the
	// invitation and the joint ownership in place.
	if err := h.mail.Send(ctx, mailer.Message{
		To:          email,
		Subject:     "You were invited to a Norviq portfolio",
		Body:        "Open Norviq and accept this invitation token: " + token,
		Purpose:     "portfolio_invitation",
		ChallengeID: &record.ID,
	}); err != nil {
		return err
	}
	return httpx.Created(c, makeInvitation(&record))
}

// POST /v1/portfolio-invitations/accept
func (h *handlers) acceptInvitation(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	o, err := readObject(c)
	if err != nil {
		return err
	}
	token, err := o.String("token")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	q := portfoliomgmtdb.New(h.db)
	found, err := q.PendingInvitationByTokenHash(ctx, sha256Hex(token))
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return httpx.NotFound("Invitation not found.")
	}
	record := found[0]
	if !record.ExpiresAt.After(portfolios.Now()) {
		if err := q.SetInvitationStatus(ctx, portfoliomgmtdb.SetInvitationStatusParams{ID: record.ID, Status: statusExpired, UpdatedAt: ptr(portfolios.Now())}); err != nil {
			return err
		}
		return httpx.Abort(410, "Invitation expired.")
	}
	users, err := q.UserBasics(ctx, s.UserID)
	if err != nil {
		return err
	}
	if len(users) == 0 || !users[0].IsVerified || !strings.EqualFold(users[0].Email, record.Email) {
		return httpx.Forbidden("Use the verified account that received this invitation.")
	}
	user := users[0]

	membership, err := h.activateMembership(ctx, q, record.PortfolioID, s.UserID)
	if err != nil {
		return err
	}
	if err := q.AcceptInvitation(ctx, portfoliomgmtdb.AcceptInvitationParams{ID: record.ID, AcceptedAt: ptr(portfolios.Now())}); err != nil {
		return err
	}
	return httpx.OK(c, Membership{
		ID:          apijson.FormatUUID(membership.ID),
		PortfolioID: apijson.FormatUUID(record.PortfolioID),
		UserID:      apijson.FormatUUID(s.UserID),
		DisplayName: displayName(user.Username, user.Email),
		Email:       user.Email,
		Role:        portfolios.RoleEditor,
		Status:      statusActive,
		JoinedAt:    portfolios.FormatISODateTimePtr(membership.JoinedAt),
		CreatedAt:   formatOptional(membership.CreatedAt),
	})
}

// activateMembership reuses the (portfolio, user) row whatever its status,
// else inserts one, and makes it an active editor joined now.
func (h *handlers) activateMembership(ctx context.Context, q *portfoliomgmtdb.Queries, portfolioID, userID uuid.UUID) (portfoliomgmtdb.PortfolioMembership, error) {
	existing, err := q.MembershipForUser(ctx, portfoliomgmtdb.MembershipForUserParams{PortfolioID: portfolioID, UserID: userID})
	if err != nil {
		return portfoliomgmtdb.PortfolioMembership{}, err
	}
	now := portfolios.Now()
	if len(existing) > 0 {
		return q.ActivateMembership(ctx, portfoliomgmtdb.ActivateMembershipParams{
			ID: existing[0].ID, Role: portfolios.RoleEditor, Status: statusActive, JoinedAt: &now,
		})
	}
	return q.InsertMembership(ctx, portfoliomgmtdb.InsertMembershipParams{
		ID: uuid.New(), PortfolioID: portfolioID, UserID: userID,
		Role: portfolios.RoleEditor, Status: statusActive, JoinedAt: &now, CreatedAt: &now,
	})
}

// DELETE /v1/portfolios/:portfolioId/invitations/:invitationId
func (h *handlers) revokeInvitation(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{OwnerOnly: true})
	if err != nil {
		return err
	}
	invitationID, err := parameter(c, "invitationId")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	q := portfoliomgmtdb.New(h.db)
	found, err := q.InvitationByID(ctx, portfoliomgmtdb.InvitationByIDParams{ID: invitationID, PortfolioID: ac.Portfolio.ID})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return httpx.NotFound("Invitation not found.")
	}
	// Vapor flipped an accepted invitation to revoked and answered 204 while
	// the membership it created stayed active, so the owner believed access
	// was gone. Access is the membership's: point at removing the member.
	if found[0].Status == statusAccepted {
		return httpx.Conflict("This invitation was accepted; remove the member instead.")
	}
	if err := q.SetInvitationStatus(ctx, portfoliomgmtdb.SetInvitationStatusParams{ID: invitationID, Status: statusRevoked, UpdatedAt: ptr(portfolios.Now())}); err != nil {
		return err
	}
	if err := collapseJointOwnershipIfEmpty(ctx, q, ac.Portfolio.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// DELETE /v1/portfolios/:portfolioId/members/:membershipId
func (h *handlers) revokeMember(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{OwnerOnly: true})
	if err != nil {
		return err
	}
	membershipID, err := parameter(c, "membershipId")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	q := portfoliomgmtdb.New(h.db)
	found, err := q.ActiveMembershipByID(ctx, portfoliomgmtdb.ActiveMembershipByIDParams{ID: membershipID, PortfolioID: ac.Portfolio.ID})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return httpx.NotFound("Portfolio member not found.")
	}
	if err := q.SetMembershipStatus(ctx, portfoliomgmtdb.SetMembershipStatusParams{ID: membershipID, Status: statusRevoked, UpdatedAt: ptr(portfolios.Now())}); err != nil {
		return err
	}
	if err := collapseJointOwnershipIfEmpty(ctx, q, ac.Portfolio.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// POST /v1/portfolios/:portfolioId/leave
func (h *handlers) leave(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	if ac.Role != portfolios.RoleEditor {
		return httpx.BadRequest("A portfolio owner cannot leave their own portfolio.")
	}
	ctx := c.Request().Context()
	q := portfoliomgmtdb.New(h.db)
	found, err := q.ActiveMembershipForUser(ctx, portfoliomgmtdb.ActiveMembershipForUserParams{PortfolioID: ac.Portfolio.ID, UserID: s.UserID})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return httpx.NotFound("Portfolio membership not found.")
	}
	if err := q.SetMembershipStatus(ctx, portfoliomgmtdb.SetMembershipStatusParams{ID: found[0].ID, Status: statusLeft, UpdatedAt: ptr(portfolios.Now())}); err != nil {
		return err
	}
	if err := collapseJointOwnershipIfEmpty(ctx, q, ac.Portfolio.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// collapseJointOwnershipIfEmpty turns the portfolio back to individual when
// no active member and no pending invitation remain (and bumps updated_at
// even when it already was individual, as Fluent's save of an assignment).
func collapseJointOwnershipIfEmpty(ctx context.Context, q *portfoliomgmtdb.Queries, listID uuid.UUID) error {
	active, err := q.CountActiveMemberships(ctx, listID)
	if err != nil {
		return err
	}
	pending, err := q.CountPendingInvitations(ctx, listID)
	if err != nil {
		return err
	}
	if active != 0 || pending != 0 {
		return nil
	}
	return q.SetPortfolioListOwnership(ctx, portfoliomgmtdb.SetPortfolioListOwnershipParams{
		ID: listID, Ownership: portfolios.OwnershipIndividual, UpdatedAt: ptr(portfolios.Now()),
	})
}
