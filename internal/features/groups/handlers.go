package groups

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/store/groupsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// ---- Groups ------------------------------------------------------------------

func (h *handler) myGroups(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	q := h.s.q()
	memberships, err := q.MembersOfUser(ctx, v.UserID)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(memberships))
	for i, m := range memberships {
		ids[i] = m.GroupID
	}
	gs, err := q.GroupsByIDs(ctx, ids)
	if err != nil {
		return err
	}
	sortGroupsByActivity(gs)
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	items, err := h.s.summaries(ctx, gs, v.UserID, hidden)
	if err != nil {
		return err
	}
	pending, err := h.s.pendingInvitations(ctx, v.UserID, hidden)
	if err != nil {
		return err
	}
	return httpx.OK(c, GroupListResponse{Items: items, PendingInvitationCount: len(pending)})
}

var createGroupFields = []field{
	{name: "name", kind: fString},
	{name: "description", kind: fString, optional: true},
	{name: "iconEmoji", kind: fString, optional: true},
	{name: "topic", kind: fString},
	{name: "ticker", kind: fString, optional: true},
	{name: "visibility", kind: fEnum, enumName: "GroupVisibility", valid: validVisibility},
}

func (h *handler) createGroup(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	cfg := configFromEnv()
	body, err := decodeBody(c, createGroupFields)
	if err != nil {
		return err
	}
	topic := topicOf(*body.str("topic"))
	if topic == TopicOther {
		return httpx.BadRequest("Pick a topic for the group.")
	}
	name, err := validateGroupName(*body.str("name"))
	if err != nil {
		return err
	}
	description, err := validateGroupDescription(body.str("description"))
	if err != nil {
		return err
	}
	icon, err := validateIconEmoji(body.str("iconEmoji"))
	if err != nil {
		return err
	}
	ticker, err := validateTicker(body.str("ticker"), topic)
	if err != nil {
		return err
	}
	visibility := *body.str("visibility")
	if visibility == VisibilityPublic && !cfg.PublicDirectory {
		return httpx.BadRequest("Public groups aren't available right now.")
	}
	ctx := ctxOf(c)
	q := h.s.q()
	owner := v.UserID
	if !v.IsAdmin {
		// Deleted groups still count, or deleting would refill the quota.
		recent, err := q.GroupCountCreatedSince(ctx, groupsdb.GroupCountCreatedSinceParams{
			OwnerID: &owner, CreatedAt: h.s.now().Add(-86400e9),
		})
		if err != nil {
			return err
		}
		if recent >= maxGroupsCreatedPerDay {
			return httpx.CodedAbort(http.StatusTooManyRequests, "group_create_limit", "You can create up to 3 groups a day.")
		}
		owned, err := q.GroupCountOwned(ctx, &owner)
		if err != nil {
			return err
		}
		if owned >= maxGroupsOwned {
			return httpx.CodedAbort(http.StatusConflict, "group_owned_limit", "You can own up to 10 groups.")
		}
	}
	groupID := uuid.New()
	now := h.s.now()
	err = h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		if err := tq.GroupInsert(ctx, groupsdb.GroupInsertParams{
			ID: groupID, Name: name, Description: description, IconEmoji: icon, Topic: topic,
			Ticker: ticker, Visibility: visibility, OwnerID: &owner, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tq.ChannelInsert(ctx, groupsdb.ChannelInsertParams{
			ID: uuid.New(), GroupID: groupID, Name: defaultChannelName, Position: 0, CreatedAt: now,
		}); err != nil {
			return err
		}
		return h.s.addMember(ctx, tq, groupID, v.UserID, RoleOwner, true)
	})
	if err != nil {
		return err
	}
	achievementhooks.Schedule(v.UserID)
	h.log.Info("groups.created", "visibility", visibility, "topic", topic)
	d, err := h.s.detail(ctx, groupID, v)
	if err != nil {
		return err
	}
	return httpx.OK(c, d)
}

// discover is public groups, most members first. `ticker` matches exactly;
// `tickers` (comma-separated, up to 10) backs "suggested for you"; `q`
// searches names.
func (h *handler) discover(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if !configFromEnv().PublicDirectory {
		return httpx.NotFound("")
	}
	const pageSize = 25
	offset := 0
	if raw := httpx.QueryString(c, "cursor"); raw != nil {
		if n, err := strconv.Atoi(*raw); err == nil && n > 0 {
			offset = n
		}
	}
	tickers := []string{}
	t, err := validateTicker(httpx.QueryString(c, "ticker"), TopicStock)
	if err != nil {
		return err
	}
	if t != nil {
		tickers = append(tickers, *t)
	}
	if list := httpx.QueryString(c, "tickers"); list != nil {
		n := 0
		for _, raw := range strings.Split(*list, ",") {
			if raw == "" { // split(separator:) drops empty pieces
				continue
			}
			if n == 10 {
				break
			}
			n++
			s := raw
			if t, err := validateTicker(&s, TopicStock); err == nil && t != nil {
				tickers = append(tickers, *t)
			}
		}
	}
	pattern := ""
	if raw := httpx.QueryString(c, "q"); raw != nil {
		if q := trim(*raw); q != "" {
			escaped := prefixChars(q, 50)
			escaped = strings.ReplaceAll(escaped, `\`, `\\`)
			escaped = strings.ReplaceAll(escaped, "%", `\%`)
			escaped = strings.ReplaceAll(escaped, "_", `\_`)
			pattern = "%" + escaped + "%"
		}
	}
	ctx := ctxOf(c)
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	rows, err := h.s.q().GroupDiscover(ctx, groupsdb.GroupDiscoverParams{
		Tickers: tickers, NamePattern: pattern, Off: int64(offset), Lim: pageSize + 1,
	})
	if err != nil {
		return err
	}
	// Groups owned by someone hidden from the viewer drop out after paging.
	page := []groupsdb.Group{}
	for i := range rows {
		if i >= pageSize {
			break
		}
		if g := &rows[i]; g.OwnerID != nil {
			if _, h := hidden[*g.OwnerID]; h {
				continue
			}
		}
		page = append(page, rows[i])
	}
	items, err := h.s.summaries(ctx, page, v.UserID, hidden)
	if err != nil {
		return err
	}
	out := GroupDirectoryPage{Items: items}
	if len(rows) > pageSize {
		next := strconv.Itoa(offset + pageSize)
		out.NextCursor = &next
	}
	return httpx.OK(c, out)
}

func (h *handler) unreadCount(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	total, err := h.s.totalUnread(ctx, v.UserID, hidden)
	if err != nil {
		return err
	}
	pending, err := h.s.pendingInvitations(ctx, v.UserID, hidden)
	if err != nil {
		return err
	}
	return httpx.OK(c, GroupUnreadCount{Total: total, PendingInvitationCount: len(pending)})
}

// viewerAndGroup is the common prologue: the community viewer and the live
// group named by :groupId.
func (h *handler) viewerAndGroup(c *echo.Context) (*community.Viewer, *groupsdb.Group, error) {
	v, err := community.FromContext(c)
	if err != nil {
		return nil, nil, err
	}
	id, err := uuidParam(c, "groupId")
	if err != nil {
		return nil, nil, err
	}
	g, err := h.s.group(ctxOf(c), h.s.q(), id)
	if err != nil {
		return nil, nil, err
	}
	return v, g, nil
}

func (h *handler) getGroup(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	d, err := h.s.detail(ctxOf(c), g.ID, v)
	if err != nil {
		return err
	}
	return httpx.OK(c, d)
}

var updateGroupFields = []field{
	{name: "name", kind: fString, optional: true},
	{name: "description", kind: fString, optional: true},
	{name: "iconEmoji", kind: fString, optional: true},
	{name: "topic", kind: fString, optional: true},
	{name: "ticker", kind: fString, optional: true},
	{name: "visibility", kind: fEnum, optional: true, enumName: "GroupVisibility", valid: validVisibility},
}

func (h *handler) updateGroup(c *echo.Context) error {
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
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	if err := requireManager(m); err != nil {
		return err
	}
	body, err := decodeBody(c, updateGroupFields)
	if err != nil {
		return err
	}
	if raw := body.str("name"); raw != nil {
		if g.Name, err = validateGroupName(*raw); err != nil {
			return err
		}
	}
	if raw := body.str("description"); raw != nil {
		if g.Description, err = validateGroupDescription(raw); err != nil {
			return err
		}
	}
	if raw := body.str("iconEmoji"); raw != nil {
		if g.IconEmoji, err = validateIconEmoji(raw); err != nil {
			return err
		}
	}
	topic := g.Topic
	if raw := body.str("topic"); raw != nil {
		topic = topicOf(*raw)
	} else if t := topicOf(g.Topic); t == TopicOther {
		topic = TopicCustom // GroupTopic(rawValue:) ?? .custom
	}
	if topicOf(topic) == TopicOther {
		return httpx.BadRequest("Pick a topic for the group.")
	}
	g.Topic = topic
	if raw := body.str("ticker"); raw != nil {
		if g.Ticker, err = validateTicker(raw, topic); err != nil {
			return err
		}
	} else if !topicAllowsTicker(topic) {
		g.Ticker = nil
	}
	if vis := body.str("visibility"); vis != nil {
		if *vis == VisibilityPublic && !configFromEnv().PublicDirectory {
			return httpx.BadRequest("Public groups aren't available right now.")
		}
		g.Visibility = *vis
	}
	now := h.s.now()
	if err := h.s.q().GroupUpdate(ctx, groupsdb.GroupUpdateParams{
		ID: g.ID, Name: g.Name, Description: g.Description, IconEmoji: g.IconEmoji, Topic: g.Topic,
		Ticker: g.Ticker, Visibility: g.Visibility, UpdatedAt: &now,
	}); err != nil {
		return err
	}
	h.announce(ctx, EventGroupUpdated, g.ID, nil)
	d, err := h.s.detail(ctx, g.ID, v)
	if err != nil {
		return err
	}
	return httpx.OK(c, d)
}

// deleteGroup is a soft delete by the owner or a global admin.
func (h *handler) deleteGroup(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	if !v.IsAdmin {
		m, err := h.s.requireMember(ctx, g, v)
		if err != nil {
			return err
		}
		if err := requireOwner(m); err != nil {
			return err
		}
	}
	q := h.s.q()
	members, err := q.MembersOfGroup(ctx, g.ID)
	if err != nil {
		return err
	}
	now := h.s.now()
	if err := q.GroupSoftDelete(ctx, groupsdb.GroupSoftDeleteParams{ID: g.ID, DeletedAt: &now}); err != nil {
		return err
	}
	ids := map[uuid.UUID]struct{}{}
	for _, m := range members {
		ids[m.UserID] = struct{}{}
	}
	reason := RemovedDeleted
	h.pub.to(GroupRealtimeEvent{Type: EventMembershipRemoved, GroupID: uuidPtr(g.ID), Reason: &reason}, ids, nil)
	return noContent(c)
}

// joinGroup is public groups only. Push starts off.
func (h *handler) joinGroup(c *echo.Context) error {
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
	if g.Visibility != VisibilityPublic || !configFromEnv().PublicDirectory {
		return errGroupNotFound()
	}
	ctx := ctxOf(c)
	if g.OwnerID != nil {
		blocked, err := h.s.Social.IsBlockedEitherWay(ctx, v.UserID, *g.OwnerID)
		if err != nil {
			return err
		}
		if blocked {
			return errGroupNotFound()
		}
	}
	m, err := h.s.membership(ctx, h.s.q(), g.ID, v.UserID)
	if err != nil {
		return err
	}
	if m == nil {
		if err := h.s.tx(ctx, func(tq *groupsdb.Queries) error {
			return h.s.addMember(ctx, tq, g.ID, v.UserID, RoleMember, false)
		}); err != nil {
			return err
		}
		achievementhooks.Schedule(v.UserID)
	}
	h.announce(ctx, EventGroupUpdated, g.ID, nil)
	d, err := h.s.detail(ctx, g.ID, v)
	if err != nil {
		return err
	}
	return httpx.OK(c, d)
}

// leaveGroup: the owner must hand the group over first, unless they're the
// last one in it, in which case leaving deletes it.
func (h *handler) leaveGroup(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	q := h.s.q()
	m, err := h.s.membership(ctx, q, g.ID, v.UserID)
	if err != nil {
		return err
	}
	if m == nil {
		return noContent(c)
	}
	if roleOf(m) == RoleOwner {
		others, err := q.MemberCountOthers(ctx, groupsdb.MemberCountOthersParams{GroupID: g.ID, UserID: v.UserID})
		if err != nil {
			return err
		}
		if others != 0 {
			return httpx.CodedAbort(http.StatusConflict, "owner_must_transfer", "Make someone else the owner before you leave.")
		}
		now := h.s.now()
		if err := q.GroupSoftDelete(ctx, groupsdb.GroupSoftDeleteParams{ID: g.ID, DeletedAt: &now}); err != nil {
			return err
		}
		h.removed(v.UserID, g.ID, RemovedDeleted)
		return noContent(c)
	}
	if err := h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		return h.s.removeMember(ctx, tq, g.ID, v.UserID)
	}); err != nil {
		return err
	}
	h.removed(v.UserID, g.ID, RemovedLeft)
	h.announce(ctx, EventGroupUpdated, g.ID, nil)
	return noContent(c)
}

var notificationFields = []field{{name: "pushEnabled", kind: fBool}}

func (h *handler) updateNotifications(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	body, err := decodeBody(c, notificationFields)
	if err != nil {
		return err
	}
	m.PushEnabled = body.boolean("pushEnabled")
	if err := h.s.q().MemberSetPush(ctx, groupsdb.MemberSetPushParams{ID: m.ID, PushEnabled: m.PushEnabled}); err != nil {
		return err
	}
	return httpx.OK(c, membershipDTO(m))
}

// ---- Members -----------------------------------------------------------------

func cursorOffset(c *echo.Context) int {
	if raw := httpx.QueryString(c, "cursor"); raw != nil {
		if n, err := strconv.Atoi(*raw); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func (h *handler) members(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	if !v.IsAdmin {
		if _, err := h.s.requireMember(ctx, g, v); err != nil {
			return err
		}
	}
	const pageSize = 100
	offset := cursorOffset(c)
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	all, err := h.s.q().MembersOfGroup(ctx, g.ID)
	if err != nil {
		return err
	}
	rows := make([]groupsdb.GroupMember, 0, len(all))
	for _, m := range all {
		if _, h := hidden[m.UserID]; !h {
			rows = append(rows, m)
		}
	}
	sortMembers(rows)
	page := []groupsdb.GroupMember{}
	if offset < len(rows) {
		page = rows[offset:min(len(rows), offset+pageSize)]
	}
	ids := make([]uuid.UUID, len(page))
	for i, m := range page {
		ids[i] = m.UserID
	}
	names, err := h.s.usernames(ctx, ids)
	if err != nil {
		return err
	}
	out := GroupMemberPage{Items: make([]GroupMember, 0, len(page))}
	for i := range page {
		m := &page[i]
		out.Items = append(out.Items, GroupMember{
			UserID: apijson.NewUUID(m.UserID), Username: nameOf(names, m.UserID), Role: roleOf(m),
			JoinedAt: apijson.NewTime(m.JoinedAt),
		})
	}
	if len(rows) > offset+pageSize {
		next := strconv.Itoa(offset + pageSize)
		out.NextCursor = &next
	}
	return httpx.OK(c, out)
}

// sortMembers: owner first, then admins, then members by join date.
func sortMembers(rows []groupsdb.GroupMember) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && memberLess(&rows[j], &rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func memberLess(a, b *groupsdb.GroupMember) bool {
	ra, rb := rank(roleOf(a)), rank(roleOf(b))
	if ra != rb {
		return ra > rb
	}
	return a.JoinedAt.Before(b.JoinedAt)
}

var updateMemberFields = []field{{name: "role", kind: fString}}

// updateMember is owner only: make someone an admin or a member again.
func (h *handler) updateMember(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	if err := requireOwner(m); err != nil {
		return err
	}
	body, err := decodeBody(c, updateMemberFields)
	if err != nil {
		return err
	}
	role := *body.str("role")
	if role != RoleAdmin && role != RoleMember {
		return httpx.BadRequest("A member can be made an admin or a member.")
	}
	target, err := h.requireTarget(c, g)
	if err != nil {
		return err
	}
	if roleOf(target) == RoleOwner {
		return httpx.BadRequest("Transfer ownership instead.")
	}
	if err := h.s.q().MemberSetRole(ctx, groupsdb.MemberSetRoleParams{ID: target.ID, Role: role}); err != nil {
		return err
	}
	h.announce(ctx, EventGroupUpdated, g.ID, nil)
	return noContent(c)
}

func (h *handler) kickMember(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	target, err := h.requireTarget(c, g)
	if err != nil {
		return err
	}
	if err := h.requireOutranks(ctx, g, target, v); err != nil {
		return err
	}
	if err := h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		return h.s.removeMember(ctx, tq, target.GroupID, target.UserID)
	}); err != nil {
		return err
	}
	h.removed(target.UserID, target.GroupID, RemovedKicked)
	h.announce(ctx, EventGroupUpdated, target.GroupID, nil)
	return noContent(c)
}

var transferFields = []field{{name: "userId", kind: fUUID}}

func (h *handler) transferOwnership(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	owner, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	if err := requireOwner(owner); err != nil {
		return err
	}
	body, err := decodeBody(c, transferFields)
	if err != nil {
		return err
	}
	userID := body.id("userId")
	var heir *groupsdb.GroupMember
	if userID != v.UserID {
		if heir, err = h.s.membership(ctx, h.s.q(), g.ID, userID); err != nil {
			return err
		}
	}
	if heir == nil {
		return httpx.BadRequest("Pick another member of the group.")
	}
	now := h.s.now()
	if err := h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		if err := tq.MemberSetRole(ctx, groupsdb.MemberSetRoleParams{ID: heir.ID, Role: RoleOwner}); err != nil {
			return err
		}
		if err := tq.MemberSetRole(ctx, groupsdb.MemberSetRoleParams{ID: owner.ID, Role: RoleAdmin}); err != nil {
			return err
		}
		return tq.GroupSetOwner(ctx, groupsdb.GroupSetOwnerParams{ID: g.ID, OwnerID: &userID, UpdatedAt: &now})
	}); err != nil {
		return err
	}
	h.announce(ctx, EventGroupUpdated, g.ID, nil)
	return noContent(c)
}

func (h *handler) bans(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	if !v.IsAdmin {
		m, err := h.s.requireMember(ctx, g, v)
		if err != nil {
			return err
		}
		if err := requireManager(m); err != nil {
			return err
		}
	}
	rows, err := h.s.q().BansOfGroup(ctx, g.ID)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, b := range rows {
		ids[i] = b.UserID
	}
	names, err := h.s.usernames(ctx, ids)
	if err != nil {
		return err
	}
	out := GroupMemberPage{Items: make([]GroupMember, 0, len(rows))}
	for _, b := range rows {
		out.Items = append(out.Items, GroupMember{
			UserID: apijson.NewUUID(b.UserID), Username: nameOf(names, b.UserID), Role: RoleMember,
			JoinedAt: apijson.NewTime(b.CreatedAt),
		})
	}
	return httpx.OK(c, out)
}

var banFields = []field{{name: "userId", kind: fUUID}, {name: "reason", kind: fString, optional: true}}

// banMember removes the user and stops them rejoining. Banning someone who
// already left is allowed.
func (h *handler) banMember(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	body, err := decodeBody(c, banFields)
	if err != nil {
		return err
	}
	target := body.id("userId")
	if target == v.UserID {
		return httpx.BadRequest("You can't ban yourself.")
	}
	m, err := h.s.membership(ctx, h.s.q(), g.ID, target)
	if err != nil {
		return err
	}
	if m != nil {
		if err := h.requireOutranks(ctx, g, m, v); err != nil {
			return err
		}
	} else if !v.IsAdmin {
		actor, err := h.s.requireMember(ctx, g, v)
		if err != nil {
			return err
		}
		if err := requireManager(actor); err != nil {
			return err
		}
	}
	var reason *string
	if r := body.str("reason"); r != nil {
		if t := trim(*r); t != "" {
			t = prefixChars(t, 300)
			reason = &t
		}
	}
	now := h.s.now()
	if err := h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		if err := h.s.removeMember(ctx, tq, g.ID, target); err != nil {
			return err
		}
		exists, err := tq.BanExists(ctx, groupsdb.BanExistsParams{GroupID: g.ID, UserID: target})
		if err != nil {
			return err
		}
		if !exists {
			by := v.UserID
			if err := tq.BanInsert(ctx, groupsdb.BanInsertParams{
				ID: uuid.New(), GroupID: g.ID, UserID: target, BannedBy: &by, Reason: reason, CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		// Pending invitations to the banned user die with the ban.
		return tq.InvitationsRevokeForInvitee(ctx, groupsdb.InvitationsRevokeForInviteeParams{
			GroupID: g.ID, InviteeID: &target, RevokedAt: &now,
		})
	}); err != nil {
		return err
	}
	h.removed(target, g.ID, RemovedBanned)
	h.announce(ctx, EventGroupUpdated, g.ID, nil)
	return noContent(c)
}

func (h *handler) unbanMember(c *echo.Context) error {
	v, g, err := h.viewerAndGroup(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	if !v.IsAdmin {
		m, err := h.s.requireMember(ctx, g, v)
		if err != nil {
			return err
		}
		if err := requireManager(m); err != nil {
			return err
		}
	}
	userID, err := uuidParam(c, "userId")
	if err != nil {
		return err
	}
	if err := h.s.q().BanDelete(ctx, groupsdb.BanDeleteParams{GroupID: g.ID, UserID: userID}); err != nil {
		return err
	}
	return noContent(c)
}

// ---- Channels ----------------------------------------------------------------

var createChannelFields = []field{{name: "name", kind: fString}, {name: "topic", kind: fString, optional: true}}

func (h *handler) createChannel(c *echo.Context) error {
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
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	if err := requireManager(m); err != nil {
		return err
	}
	body, err := decodeBody(c, createChannelFields)
	if err != nil {
		return err
	}
	name, err := validateChannelName(*body.str("name"))
	if err != nil {
		return err
	}
	topic, err := validateChannelTopic(body.str("topic"))
	if err != nil {
		return err
	}
	q := h.s.q()
	existing, err := h.s.channels(ctx, q, []uuid.UUID{g.ID})
	if err != nil {
		return err
	}
	if len(existing) >= maxChannels {
		return httpx.CodedAbort(http.StatusConflict, "channel_limit", "A group can have up to 20 channels.")
	}
	position := int64(-1)
	for i := range existing {
		e := &existing[i]
		if e.Name == name {
			return httpx.CodedAbort(http.StatusConflict, "channel_name_taken", "There's already a #"+name+".")
		}
		position = max(position, e.Position)
	}
	ch := groupsdb.GroupChannel{ID: uuid.New(), GroupID: g.ID, Name: name, Kind: KindText, Topic: topic, Position: position + 1, CreatedAt: h.s.now()}
	if err := q.ChannelInsert(ctx, groupsdb.ChannelInsertParams{
		ID: ch.ID, GroupID: ch.GroupID, Name: ch.Name, Topic: ch.Topic, Position: ch.Position, CreatedAt: ch.CreatedAt,
	}); err != nil {
		return err
	}
	h.announce(ctx, EventChannelCreated, g.ID, &ch.ID)
	return httpx.OK(c, channelDTO(&ch, nil))
}

var updateChannelFields = []field{
	{name: "name", kind: fString, optional: true},
	{name: "topic", kind: fString, optional: true},
	{name: "position", kind: fInt, optional: true},
}

func (h *handler) updateChannel(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	id, err := uuidParam(c, "channelId")
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	ch, g, err := h.s.channel(ctx, id)
	if err != nil {
		return err
	}
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	if err := requireManager(m); err != nil {
		return err
	}
	body, err := decodeBody(c, updateChannelFields)
	if err != nil {
		return err
	}
	q := h.s.q()
	if raw := body.str("name"); raw != nil {
		name, err := validateChannelName(*raw)
		if err != nil {
			return err
		}
		if name != ch.Name {
			taken, err := q.ChannelNameTaken(ctx, groupsdb.ChannelNameTakenParams{GroupID: ch.GroupID, Name: name})
			if err != nil {
				return err
			}
			if taken {
				return httpx.CodedAbort(http.StatusConflict, "channel_name_taken", "There's already a #"+name+".")
			}
			ch.Name = name
		}
	}
	if raw := body.str("topic"); raw != nil {
		if ch.Topic, err = validateChannelTopic(raw); err != nil {
			return err
		}
	}
	if p := body.integer("position"); p != nil {
		ch.Position = int64(max(0, min(*p, maxChannels)))
	}
	if err := q.ChannelUpdate(ctx, groupsdb.ChannelUpdateParams{ID: ch.ID, Name: ch.Name, Topic: ch.Topic, Position: ch.Position}); err != nil {
		return err
	}
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	reads, err := h.s.readStates(ctx, []uuid.UUID{ch.ID}, v.UserID, hidden)
	if err != nil {
		return err
	}
	h.announce(ctx, EventChannelUpdated, ch.GroupID, &ch.ID)
	var rs *readState
	if r, ok := reads[ch.ID]; ok {
		rs = &r
	}
	return httpx.OK(c, channelDTO(ch, rs))
}

func (h *handler) deleteChannel(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "channelId")
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	ch, g, err := h.s.channel(ctx, id)
	if err != nil {
		return err
	}
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	if err := requireManager(m); err != nil {
		return err
	}
	q := h.s.q()
	count, err := q.ChannelCount(ctx, ch.GroupID)
	if err != nil {
		return err
	}
	if count <= 1 {
		return httpx.CodedAbort(http.StatusConflict, "last_channel", "A group needs at least one channel.")
	}
	now := h.s.now()
	if err := q.ChannelSoftDelete(ctx, groupsdb.ChannelSoftDeleteParams{ID: ch.ID, DeletedAt: &now}); err != nil {
		return err
	}
	h.announce(ctx, EventChannelDeleted, ch.GroupID, &ch.ID)
	return noContent(c)
}

// ---- Helpers -----------------------------------------------------------------

func (h *handler) requireTarget(c *echo.Context, g *groupsdb.Group) (*groupsdb.GroupMember, error) {
	userID, err := uuidParam(c, "userId")
	if err != nil {
		return nil, err
	}
	m, err := h.s.membership(ctxOf(c), h.s.q(), g.ID, userID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, httpx.NotFound("Member not found")
	}
	return m, nil
}

// requireOutranks: moderating someone needs a strictly higher role; a global
// admin can moderate anyone but the owner.
func (h *handler) requireOutranks(ctx context.Context, g *groupsdb.Group, target *groupsdb.GroupMember, v *community.Viewer) error {
	if target.UserID == v.UserID {
		return httpx.BadRequest("Leave the group instead.")
	}
	if v.IsAdmin && roleOf(target) != RoleOwner {
		return nil
	}
	actor, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	if !roleCanManage(roleOf(actor)) || rank(roleOf(actor)) <= rank(roleOf(target)) {
		return httpx.CodedAbort(http.StatusForbidden, "group_forbidden", "You can't remove that member.")
	}
	return nil
}
