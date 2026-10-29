package groups

import (
	"context"
	"net/http"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/store/groupsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

var inviteFields = []field{
	{name: "expiresInHours", kind: fInt, optional: true},
	{name: "maxUses", kind: fInt, optional: true},
}

// createInviteLink: any member can make a link.
func (h *handler) createInviteLink(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	_, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	if _, err := h.s.requireMember(ctx, g, v); err != nil {
		return err
	}
	// `(try? req.content.decode(...)) ?? .default`: any decode failure
	// falls back to seven days, unlimited.
	opts := defaultInvite()
	if body, err := decodeBody(c, inviteFields); err == nil {
		opts = inviteOptions{ExpiresInHours: body.integer("expiresInHours"), MaxUses: body.integer("maxUses")}
	}
	now := h.s.now()
	expiresAt, maxUses, err := validateInvite(opts, now)
	if err != nil {
		return err
	}
	active, err := h.activeLinks(ctx, g.ID)
	if err != nil {
		return err
	}
	if len(active) >= maxActiveInviteLinks {
		return httpx.CodedAbort(http.StatusConflict, "invite_limit",
			"This group has 25 active invite links. Revoke one first.")
	}
	code := generateInviteCode()
	by := v.UserID
	inv := groupsdb.GroupInvite{ID: uuid.New(), GroupID: g.ID, CreatedBy: &by, Code: &code, ExpiresAt: expiresAt, CreatedAt: now}
	if maxUses != nil {
		n := int64(*maxUses)
		inv.MaxUses = &n
	}
	if err := h.s.q().InviteInsert(ctx, groupsdb.InviteInsertParams{
		ID: inv.ID, GroupID: inv.GroupID, CreatedBy: inv.CreatedBy, Code: inv.Code, ExpiresAt: inv.ExpiresAt,
		MaxUses: inv.MaxUses, CreatedAt: inv.CreatedAt,
	}); err != nil {
		return err
	}
	names := map[uuid.UUID]string{}
	if v.Username != nil {
		names[v.UserID] = *v.Username
	}
	return httpx.OK(c, inviteLinkDTO(&inv, names))
}

// inviteLinks: owner and admins see every active link; members their own.
func (h *handler) inviteLinks(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	links, err := h.activeLinks(ctx, g.ID)
	if err != nil {
		return err
	}
	if !roleCanManage(roleOf(m)) {
		mine := links[:0]
		for _, l := range links {
			if l.CreatedBy != nil && *l.CreatedBy == v.UserID {
				mine = append(mine, l)
			}
		}
		links = mine
	}
	var ids []uuid.UUID
	for _, l := range links {
		if l.CreatedBy != nil {
			ids = append(ids, *l.CreatedBy)
		}
	}
	names, err := h.s.usernames(ctx, ids)
	if err != nil {
		return err
	}
	out := GroupInviteLinkList{Items: make([]GroupInviteLink, 0, len(links))}
	for i := range links {
		out.Items = append(out.Items, inviteLinkDTO(&links[i], names))
	}
	return httpx.OK(c, out)
}

func (h *handler) revokeInvite(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "inviteId")
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	q := h.s.q()
	rows, err := q.InviteByID(ctx, id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("Invite not found")
	}
	inv := rows[0]
	g, err := h.s.group(ctx, q, inv.GroupID)
	if err != nil {
		return err
	}
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	if (inv.CreatedBy == nil || *inv.CreatedBy != v.UserID) && !roleCanManage(roleOf(m)) {
		return httpx.CodedAbort(http.StatusForbidden, "group_forbidden", "Only the invite's creator or an admin can revoke it.")
	}
	if inv.RevokedAt == nil {
		now := h.s.now()
		if err := q.InviteRevoke(ctx, groupsdb.InviteRevokeParams{ID: inv.ID, RevokedAt: &now}); err != nil {
			return err
		}
	}
	return noContent(c)
}

func (h *handler) previewInvite(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	inv, g, err := h.usableLink(ctx, param(c, "code"), v)
	if err != nil {
		return err
	}
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	sums, err := h.s.summaries(ctx, []groupsdb.Group{*g}, v.UserID, hidden)
	if err != nil {
		return err
	}
	out := GroupInvitePreview{Group: sums[0], AlreadyMember: sums[0].Membership != nil, ExpiresAt: apijson.TimePtr(inv.ExpiresAt)}
	if inv.Code != nil {
		out.Code = *inv.Code
	}
	if inv.CreatedBy != nil {
		names, err := h.s.usernames(ctx, []uuid.UUID{*inv.CreatedBy})
		if err != nil {
			return err
		}
		out.InviterUsername = nameOf(names, *inv.CreatedBy)
	}
	return httpx.OK(c, out)
}

// redeemInvite joins through a link. Redeeming a link you've already used
// is a no-op that returns the group.
func (h *handler) redeemInvite(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	ctx := ctxOf(c)
	q := h.s.q()
	code := param(c, "code")
	if inviteCodeWellFormed(code) {
		rows, err := q.InviteByCode(ctx, &code)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			gs, err := q.GroupByID(ctx, rows[0].GroupID)
			if err != nil {
				return err
			}
			if len(gs) > 0 {
				m, err := h.s.membership(ctx, q, rows[0].GroupID, v.UserID)
				if err != nil {
					return err
				}
				if m != nil {
					return h.respondDetail(c, gs[0].ID, v)
				}
			}
		}
	}
	inv, g, err := h.usableLink(ctx, code, v)
	if err != nil {
		return err
	}
	if err := h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		// Claims a use atomically, so two redeems racing for the last use
		// can't both get in.
		now := h.s.now()
		claimed, err := tq.InviteClaimUse(ctx, groupsdb.InviteClaimUseParams{ID: inv.ID, ExpiresAt: &now})
		if err != nil {
			return err
		}
		if len(claimed) == 0 {
			return httpx.CodedAbort(http.StatusGone, "invite_exhausted", "This invite has been used up.")
		}
		return h.s.addMember(ctx, tq, g.ID, v.UserID, RoleMember, true)
	}); err != nil {
		return err
	}
	achievementhooks.Schedule(v.UserID)
	h.announce(ctx, EventGroupUpdated, g.ID, nil)
	return h.respondDetail(c, g.ID, v)
}

func (h *handler) respondDetail(c *echo.Context, groupID uuid.UUID, v *community.Viewer) error {
	d, err := h.s.detail(ctxOf(c), groupID, v)
	if err != nil {
		return err
	}
	return httpx.OK(c, d)
}

var directInvitationFields = []field{{name: "userIds", kind: fUUIDs}}

// inviteFriends invites friends by id. Non-friends, blocked users, members,
// banned users and pending invitees are skipped quietly.
func (h *handler) inviteFriends(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	_, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	if _, err := h.s.requireMember(ctx, g, v); err != nil {
		return err
	}
	body, err := decodeBody(c, directInvitationFields)
	if err != nil {
		return err
	}
	// Array(Set(...)): Vapor's order was the Set's, i.e. arbitrary; v2 keeps
	// the request order.
	seen := map[uuid.UUID]bool{}
	userIDs := []uuid.UUID{}
	for _, id := range body.ids("userIds") {
		if !seen[id] {
			seen[id] = true
			userIDs = append(userIDs, id)
		}
	}
	if len(userIDs) > maxDirectInvitations {
		return httpx.BadRequest("Invite up to 25 people at a time.")
	}
	friends, err := h.s.Social.FriendIDs(ctx, v.UserID)
	if err != nil {
		return err
	}
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	q := h.s.q()
	skip := map[uuid.UUID]bool{}
	for id := range hidden {
		skip[id] = true
	}
	members, err := q.MembersOfGroupAmong(ctx, groupsdb.MembersOfGroupAmongParams{GroupID: g.ID, UserIds: userIDs})
	if err != nil {
		return err
	}
	banned, err := q.BannedAmong(ctx, groupsdb.BannedAmongParams{GroupID: g.ID, UserIds: userIDs})
	if err != nil {
		return err
	}
	pending, err := q.InviteesPendingAmong(ctx, groupsdb.InviteesPendingAmongParams{GroupID: g.ID, UserIds: userIDs})
	if err != nil {
		return err
	}
	for _, id := range append(members, banned...) {
		skip[id] = true
	}
	for _, id := range pending {
		if id != nil {
			skip[*id] = true
		}
	}
	targets := []uuid.UUID{}
	for _, id := range userIDs {
		if _, friend := friends[id]; friend && !skip[id] && id != v.UserID {
			targets = append(targets, id)
		}
	}
	now := h.s.now()
	if err := h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		for _, t := range targets {
			by, invitee := v.UserID, t
			if err := tq.InviteInsert(ctx, groupsdb.InviteInsertParams{
				ID: uuid.New(), GroupID: g.ID, CreatedBy: &by, InviteeID: &invitee, CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	set := map[uuid.UUID]struct{}{}
	out := GroupDirectInvitationResponse{InvitedUserIDs: make([]apijson.UUID, 0, len(targets))}
	for _, t := range targets {
		set[t] = struct{}{}
		out.InvitedUserIDs = append(out.InvitedUserIDs, apijson.NewUUID(t))
	}
	h.pub.to(GroupRealtimeEvent{Type: EventInvitationReceived, GroupID: uuidPtr(g.ID)}, set, nil)
	h.n.invited(targets, g.ID, g.Name, v.Username)
	return httpx.OK(c, out)
}

func (h *handler) invitations(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	pending, err := h.s.pendingInvitations(ctx, v.UserID, hidden)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(pending))
	var inviters []uuid.UUID
	for i, p := range pending {
		ids[i] = p.GroupID
		if p.CreatedBy != nil {
			inviters = append(inviters, *p.CreatedBy)
		}
	}
	gs, err := h.s.q().GroupsByIDs(ctx, ids)
	if err != nil {
		return err
	}
	sums, err := h.s.summaries(ctx, gs, v.UserID, hidden)
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]GroupSummary{}
	for i := range sums {
		if _, ok := byID[sums[i].ID.Std()]; !ok {
			byID[sums[i].ID.Std()] = sums[i]
		}
	}
	names, err := h.s.usernames(ctx, inviters)
	if err != nil {
		return err
	}
	out := GroupInvitationList{Items: []GroupInvitation{}}
	for _, p := range pending {
		sum, ok := byID[p.GroupID]
		if !ok {
			continue
		}
		item := GroupInvitation{ID: apijson.NewUUID(p.ID), Group: sum, CreatedAt: apijson.NewTime(p.CreatedAt)}
		if p.CreatedBy != nil {
			item.InviterUsername = nameOf(names, *p.CreatedBy)
		}
		out.Items = append(out.Items, item)
	}
	return httpx.OK(c, out)
}

func (h *handler) acceptInvitation(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	ctx := ctxOf(c)
	inv, err := h.pendingInvitation(c, v)
	if err != nil {
		return err
	}
	g, err := h.s.group(ctx, h.s.q(), inv.GroupID)
	if err != nil {
		return err
	}
	if err := h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		now := h.s.now()
		if err := tq.InviteAccept(ctx, groupsdb.InviteAcceptParams{ID: inv.ID, AcceptedAt: &now}); err != nil {
			return err
		}
		m, err := h.s.membership(ctx, tq, inv.GroupID, v.UserID)
		if err != nil {
			return err
		}
		if m == nil {
			return h.s.addMember(ctx, tq, g.ID, v.UserID, RoleMember, true)
		}
		return nil
	}); err != nil {
		return err
	}
	achievementhooks.Schedule(v.UserID)
	h.announce(ctx, EventGroupUpdated, g.ID, nil)
	return h.respondDetail(c, g.ID, v)
}

func (h *handler) declineInvitation(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	inv, err := h.pendingInvitation(c, v)
	if err != nil {
		return err
	}
	now := h.s.now()
	if err := h.s.q().InviteRevoke(ctxOf(c), groupsdb.InviteRevokeParams{ID: inv.ID, RevokedAt: &now}); err != nil {
		return err
	}
	return noContent(c)
}

// activeLinks: open links not yet expired or used up, newest first.
func (h *handler) activeLinks(ctx context.Context, groupID uuid.UUID) ([]groupsdb.GroupInvite, error) {
	rows, err := h.s.q().InviteLinksOpen(ctx, groupID)
	if err != nil {
		return nil, err
	}
	now := h.s.now()
	out := make([]groupsdb.GroupInvite, 0, len(rows))
	for _, r := range rows {
		if (r.ExpiresAt == nil || r.ExpiresAt.After(now)) && (r.MaxUses == nil || r.Uses < *r.MaxUses) {
			out = append(out, r)
		}
	}
	return out, nil
}

// usableLink is a link that can still be used. A blocked inviter, a deleted
// group or a malformed code all read as "not found"; spent links say why.
func (h *handler) usableLink(ctx context.Context, code string, v *community.Viewer) (*groupsdb.GroupInvite, *groupsdb.Group, error) {
	notFound := httpx.NotFound("Invite not found")
	if !inviteCodeWellFormed(code) {
		return nil, nil, notFound
	}
	q := h.s.q()
	rows, err := q.InviteByCode(ctx, &code)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 || rows[0].RevokedAt != nil {
		return nil, nil, notFound
	}
	inv := rows[0]
	gs, err := q.GroupByID(ctx, inv.GroupID)
	if err != nil {
		return nil, nil, err
	}
	if len(gs) == 0 {
		return nil, nil, notFound
	}
	if inv.CreatedBy != nil {
		blocked, err := h.s.Social.IsBlockedEitherWay(ctx, v.UserID, *inv.CreatedBy)
		if err != nil {
			return nil, nil, err
		}
		if blocked {
			return nil, nil, notFound
		}
	}
	if inv.ExpiresAt != nil && !inv.ExpiresAt.After(time.Now()) {
		return nil, nil, httpx.CodedAbort(http.StatusGone, "invite_expired", "This invite has expired.")
	}
	if inv.MaxUses != nil && inv.Uses >= *inv.MaxUses {
		return nil, nil, httpx.CodedAbort(http.StatusGone, "invite_exhausted", "This invite has been used up.")
	}
	return &inv, &gs[0], nil
}

func (h *handler) pendingInvitation(c *echo.Context, v *community.Viewer) (*groupsdb.GroupInvite, error) {
	id, err := uuidParam(c, "inviteId")
	if err != nil {
		return nil, err
	}
	ctx := ctxOf(c)
	q := h.s.q()
	notFound := httpx.NotFound("Invitation not found")
	rows, err := q.InviteByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, notFound
	}
	inv := rows[0]
	if inv.InviteeID == nil || *inv.InviteeID != v.UserID || inv.AcceptedAt != nil || inv.RevokedAt != nil {
		return nil, notFound
	}
	gs, err := q.GroupByID(ctx, inv.GroupID)
	if err != nil {
		return nil, err
	}
	if len(gs) == 0 {
		return nil, notFound
	}
	return &inv, nil
}
