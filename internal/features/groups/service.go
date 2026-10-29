package groups

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/config"
	"norviq-api/internal/features/social"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/store/groupsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is GroupsConfiguration. Vapor read it from the environment on every
// request, so configFromEnv does too.
type Config struct {
	Enabled         bool
	PublicDirectory bool
}

func envBool(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	return config.Bool(raw, def)
}

// configFromEnv is GroupsConfiguration.fromEnvironment(): off unless both
// SOCIAL_ENABLED and GROUPS_ENABLED are set.
func configFromEnv() Config {
	enabled := social.ConfigFromEnv().Enabled && envBool("GROUPS_ENABLED", false)
	return Config{Enabled: enabled, PublicDirectory: enabled && envBool("GROUPS_PUBLIC_ENABLED", true)}
}

// Service is GroupsService: membership rules, read state and DTO mapping. A
// private group answers 404 to anyone outside it; a public one shows
// non-members a preview.
type Service struct {
	DB     *pgxpool.Pool
	Social *social.Service
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) q() *groupsdb.Queries { return groupsdb.New(s.DB) }

// tx runs fn in a transaction, as Fluent's db.transaction did.
func (s *Service) tx(ctx context.Context, fn func(q *groupsdb.Queries) error) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error { return fn(groupsdb.New(tx)) })
}

func errGroupNotFound() error {
	return httpx.CodedAbort(http.StatusNotFound, "group_not_found", "Group not found")
}

func errChannelNotFound() error {
	return httpx.CodedAbort(http.StatusNotFound, "channel_not_found", "Channel not found")
}

// hidden is SocialService.blockedEitherWay.
func (s *Service) hidden(ctx context.Context, viewer uuid.UUID) (map[uuid.UUID]struct{}, error) {
	return s.Social.BlockedEitherWay(ctx, viewer)
}

func keys(m map[uuid.UUID]struct{}) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---- Lookup and access -------------------------------------------------------

// group is GroupsService.group: a live group or 404 group_not_found.
func (s *Service) group(ctx context.Context, q *groupsdb.Queries, id uuid.UUID) (*groupsdb.Group, error) {
	rows, err := q.GroupByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errGroupNotFound()
	}
	return &rows[0], nil
}

func (s *Service) membership(ctx context.Context, q *groupsdb.Queries, groupID, userID uuid.UUID) (*groupsdb.GroupMember, error) {
	rows, err := q.MemberFor(ctx, groupsdb.MemberForParams{GroupID: groupID, UserID: userID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// access is GroupsService.access: the viewer's membership, or nil on a
// public group they can preview. 404 for a private group they're not in;
// global admins see every group.
func (s *Service) access(ctx context.Context, g *groupsdb.Group, v *community.Viewer) (*groupsdb.GroupMember, error) {
	m, err := s.membership(ctx, s.q(), g.ID, v.UserID)
	if err != nil {
		return nil, err
	}
	if m == nil && g.Visibility != VisibilityPublic && !v.IsAdmin {
		return nil, errGroupNotFound()
	}
	return m, nil
}

func (s *Service) requireMember(ctx context.Context, g *groupsdb.Group, v *community.Viewer) (*groupsdb.GroupMember, error) {
	m, err := s.access(ctx, g, v)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, httpx.CodedAbort(http.StatusForbidden, "group_membership_required", "Join the group first.")
	}
	return m, nil
}

func requireManager(m *groupsdb.GroupMember) error {
	if !roleCanManage(roleOf(m)) {
		return httpx.CodedAbort(http.StatusForbidden, "group_forbidden", "Only the group's owner and admins can do that.")
	}
	return nil
}

func requireOwner(m *groupsdb.GroupMember) error {
	if roleOf(m) != RoleOwner {
		return httpx.CodedAbort(http.StatusForbidden, "group_forbidden", "Only the group's owner can do that.")
	}
	return nil
}

// roleOf is GroupsService.role: GroupRole(rawValue:) ?? .member.
func roleOf(m *groupsdb.GroupMember) string {
	switch m.Role {
	case RoleOwner, RoleAdmin, RoleMember, RoleOther:
		return m.Role
	}
	return RoleMember
}

// rank: owner 3, admin 2, member 1.
func rank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	}
	return 1
}

// channel is GroupsService.channel: a live channel in a live group.
func (s *Service) channel(ctx context.Context, id uuid.UUID) (*groupsdb.GroupChannel, *groupsdb.Group, error) {
	q := s.q()
	rows, err := q.ChannelByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, errChannelNotFound()
	}
	gs, err := q.GroupByID(ctx, rows[0].GroupID)
	if err != nil {
		return nil, nil, err
	}
	if len(gs) == 0 {
		return nil, nil, errChannelNotFound()
	}
	return &rows[0], &gs[0], nil
}

func (s *Service) channels(ctx context.Context, q *groupsdb.Queries, groupIDs []uuid.UUID) ([]groupsdb.GroupChannel, error) {
	if len(groupIDs) == 0 {
		return []groupsdb.GroupChannel{}, nil
	}
	return q.ChannelsOfGroups(ctx, groupIDs)
}

// ---- Membership changes ------------------------------------------------------

// addMember is GroupsService.addMember: run inside a transaction. Every
// channel is marked read up to now.
func (s *Service) addMember(ctx context.Context, q *groupsdb.Queries, groupID, userID uuid.UUID, role string, pushEnabled bool) error {
	banned, err := q.BanExists(ctx, groupsdb.BanExistsParams{GroupID: groupID, UserID: userID})
	if err != nil {
		return err
	}
	if banned {
		return httpx.CodedAbort(http.StatusForbidden, "group_banned", "You can't join this group.")
	}
	members, err := q.MemberCountOfGroup(ctx, groupID)
	if err != nil {
		return err
	}
	if members >= maxMembers {
		return httpx.CodedAbort(http.StatusConflict, "group_full", "This group is full.")
	}
	joined, err := q.MemberCountOfUser(ctx, userID)
	if err != nil {
		return err
	}
	if joined >= maxGroupsJoined {
		return httpx.CodedAbort(http.StatusConflict, "group_join_limit",
			"You can be in up to 100 groups. Leave one to join another.")
	}
	now := s.now()
	if err := q.MemberInsert(ctx, groupsdb.MemberInsertParams{
		ID: uuid.New(), GroupID: groupID, UserID: userID, Role: role, PushEnabled: pushEnabled, JoinedAt: now,
	}); err != nil {
		return err
	}
	if err := q.ReadsMarkGroupRead(ctx, groupsdb.ReadsMarkGroupReadParams{UserID: userID, Now: now, GroupID: groupID}); err != nil {
		return err
	}
	return q.GroupRefreshMemberCount(ctx, groupID)
}

func (s *Service) removeMember(ctx context.Context, q *groupsdb.Queries, groupID, userID uuid.UUID) error {
	if err := q.MemberDelete(ctx, groupsdb.MemberDeleteParams{GroupID: groupID, UserID: userID}); err != nil {
		return err
	}
	if err := q.ReadsDeleteForGroup(ctx, groupsdb.ReadsDeleteForGroupParams{UserID: userID, GroupID: groupID}); err != nil {
		return err
	}
	return q.GroupRefreshMemberCount(ctx, groupID)
}

// ensureOwner is GroupsService.ensureOwner: when the owner's account is gone
// the oldest admin takes over, else the oldest member; an empty group is
// deleted.
func (s *Service) ensureOwner(ctx context.Context, g *groupsdb.Group) error {
	if g.OwnerID != nil {
		return nil
	}
	q := s.q()
	members, err := q.MembersOfGroupByJoined(ctx, g.ID)
	if err != nil {
		return err
	}
	var heir *groupsdb.GroupMember
	for i := range members {
		if members[i].Role == RoleAdmin {
			heir = &members[i]
			break
		}
	}
	if heir == nil && len(members) > 0 {
		heir = &members[0]
	}
	now := s.now()
	if heir == nil {
		if err := q.GroupSoftDelete(ctx, groupsdb.GroupSoftDeleteParams{ID: g.ID, DeletedAt: &now}); err != nil {
			return err
		}
		return errGroupNotFound()
	}
	if err := q.MemberSetRole(ctx, groupsdb.MemberSetRoleParams{ID: heir.ID, Role: RoleOwner}); err != nil {
		return err
	}
	owner := heir.UserID
	if err := q.GroupSetOwner(ctx, groupsdb.GroupSetOwnerParams{ID: g.ID, OwnerID: &owner, UpdatedAt: &now}); err != nil {
		return err
	}
	g.OwnerID = &owner
	return nil
}

// ---- Read state --------------------------------------------------------------

type readState struct {
	lastReadSeq int64
	unread      int
}

// readStates is GroupsService.readState: your own, deleted and hidden
// authors' messages never count.
func (s *Service) readStates(ctx context.Context, channelIDs []uuid.UUID, userID uuid.UUID, hidden map[uuid.UUID]struct{}) (map[uuid.UUID]readState, error) {
	out := map[uuid.UUID]readState{}
	if len(channelIDs) == 0 {
		return out, nil
	}
	rows, err := s.q().ReadStates(ctx, groupsdb.ReadStatesParams{UserID: userID, Hidden: keys(hidden), ChannelIds: channelIDs})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ChannelID] = readState{lastReadSeq: r.LastRead, unread: int(r.Unread)}
	}
	return out, nil
}

func (s *Service) totalUnread(ctx context.Context, userID uuid.UUID, hidden map[uuid.UUID]struct{}) (int, error) {
	n, err := s.q().TotalUnread(ctx, groupsdb.TotalUnreadParams{UserID: userID, Hidden: keys(hidden)})
	return int(n), err
}

// pendingInvitations is GroupsService.pendingInvitations: invites to live
// groups from people not hidden from the viewer, newest first.
func (s *Service) pendingInvitations(ctx context.Context, userID uuid.UUID, hidden map[uuid.UUID]struct{}) ([]groupsdb.GroupInvite, error) {
	q := s.q()
	invitee := userID
	rows, err := q.InvitationsPendingFor(ctx, &invitee)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.GroupID)
	}
	live := map[uuid.UUID]bool{}
	gs, err := q.GroupsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range gs {
		live[gs[i].ID] = true
	}
	out := make([]groupsdb.GroupInvite, 0, len(rows))
	for _, r := range rows {
		if !live[r.GroupID] {
			continue
		}
		if r.CreatedBy != nil {
			if _, h := hidden[*r.CreatedBy]; h {
				continue
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// ---- Mapping -----------------------------------------------------------------

// permissions is GroupsService.permissions.
func permissions(m *groupsdb.GroupMember, v *community.Viewer) GroupPermissions {
	if m == nil {
		if v.IsAdmin {
			return GroupPermissions{CanModerate: true, CanDelete: true}
		}
		return GroupPermissions{}
	}
	role := roleOf(m)
	return GroupPermissions{
		CanPost:           !v.IsMuted(),
		CanInvite:         true,
		CanEdit:           roleCanManage(role),
		CanManageChannels: roleCanManage(role),
		CanModerate:       roleCanManage(role) || v.IsAdmin,
		CanDelete:         role == RoleOwner || v.IsAdmin,
	}
}

func membershipDTO(m *groupsdb.GroupMember) GroupMembership {
	return GroupMembership{Role: roleOf(m), PushEnabled: m.PushEnabled, JoinedAt: apijson.NewTime(m.JoinedAt)}
}

func channelDTO(c *groupsdb.GroupChannel, read *readState) GroupChannel {
	kind := KindOther
	if c.Kind == KindText {
		kind = KindText
	}
	dto := GroupChannel{
		ID:            apijson.NewUUID(c.ID),
		GroupID:       apijson.NewUUID(c.GroupID),
		Name:          c.Name,
		Kind:          kind,
		Topic:         c.Topic,
		Position:      int(c.Position),
		LastSeq:       c.LastSeq,
		LastMessageAt: apijson.TimePtr(c.LastMessageAt),
	}
	if read != nil {
		dto.LastReadSeq = read.lastReadSeq
		dto.UnreadCount = read.unread
	}
	return dto
}

func messageDTO(m *groupsdb.GroupMessage, names map[uuid.UUID]string) GroupMessage {
	deleted := m.DeletedAt != nil
	cid := m.ClientMessageID
	author := apijson.NewUUID(m.AuthorID)
	dto := GroupMessage{
		ID:              apijson.NewUUID(m.ID),
		GroupID:         apijson.NewUUID(m.GroupID),
		ChannelID:       apijson.NewUUID(m.ChannelID),
		Seq:             m.Seq,
		ClientMessageID: &cid,
		AuthorID:        &author,
		AuthorUsername:  nameOf(names, m.AuthorID),
		IsDeleted:       deleted,
		CreatedAt:       apijson.NewTime(m.CreatedAt),
	}
	if !deleted {
		body := m.Body
		dto.Body = &body
	}
	return dto
}

func nameOf(names map[uuid.UUID]string, id uuid.UUID) *string {
	if n, ok := names[id]; ok {
		return &n
	}
	return nil
}

// usernames is BoardsService.usernames: users without a username are absent.
func (s *Service) usernames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q().GroupUsernames(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Username != nil {
			out[r.ID] = *r.Username
		}
	}
	return out, nil
}

type preview struct {
	author  uuid.UUID
	body    string
	deleted bool
	at      time.Time
}

// summaries is GroupsService.summaries: groups as the viewer sees them, in
// the order given.
func (s *Service) summaries(ctx context.Context, gs []groupsdb.Group, viewerID uuid.UUID, hidden map[uuid.UUID]struct{}) ([]GroupSummary, error) {
	out := make([]GroupSummary, 0, len(gs))
	if len(gs) == 0 {
		return out, nil
	}
	q := s.q()
	groupIDs := make([]uuid.UUID, len(gs))
	for i := range gs {
		groupIDs[i] = gs[i].ID
	}
	memberships, err := q.MembershipsOfUserIn(ctx, groupsdb.MembershipsOfUserInParams{GroupIds: groupIDs, UserID: viewerID})
	if err != nil {
		return nil, err
	}
	byGroup := map[uuid.UUID]*groupsdb.GroupMember{}
	for i := range memberships {
		if _, ok := byGroup[memberships[i].GroupID]; !ok {
			byGroup[memberships[i].GroupID] = &memberships[i]
		}
	}
	chans, err := s.channels(ctx, q, groupIDs)
	if err != nil {
		return nil, err
	}
	defaultChannel := map[uuid.UUID]uuid.UUID{}
	var memberChannelIDs []uuid.UUID
	for i := range chans {
		c := &chans[i]
		if _, ok := defaultChannel[c.GroupID]; !ok {
			defaultChannel[c.GroupID] = c.ID
		}
		if byGroup[c.GroupID] != nil {
			memberChannelIDs = append(memberChannelIDs, c.ID)
		}
	}
	reads, err := s.readStates(ctx, memberChannelIDs, viewerID, hidden)
	if err != nil {
		return nil, err
	}
	unreadByGroup := map[uuid.UUID]int{}
	for i := range chans {
		if r, ok := reads[chans[i].ID]; ok {
			unreadByGroup[chans[i].GroupID] += r.unread
		}
	}
	latest, err := q.LatestMessagePreviews(ctx, groupsdb.LatestMessagePreviewsParams{GroupIds: groupIDs, Hidden: keys(hidden)})
	if err != nil {
		return nil, err
	}
	previews := map[uuid.UUID]preview{}
	authors := make([]uuid.UUID, 0, len(latest))
	for _, r := range latest {
		previews[r.GroupID] = preview{author: r.AuthorID, body: r.Body, deleted: r.DeletedAt != nil, at: r.CreatedAt}
		authors = append(authors, r.AuthorID)
	}
	names, err := s.usernames(ctx, authors)
	if err != nil {
		return nil, err
	}
	for i := range gs {
		g := &gs[i]
		m := byGroup[g.ID]
		sum := GroupSummary{
			ID:            apijson.NewUUID(g.ID),
			Name:          g.Name,
			Description:   g.Description,
			IconEmoji:     g.IconEmoji,
			Topic:         topicOf(g.Topic),
			Ticker:        g.Ticker,
			Visibility:    visibilityOf(g.Visibility),
			MemberCount:   int(g.MemberCount),
			LastMessageAt: apijson.TimePtr(g.LastMessageAt),
			CreatedAt:     apijson.NewTime(g.CreatedAt),
		}
		if d, ok := defaultChannel[g.ID]; ok {
			id := apijson.NewUUID(d)
			sum.DefaultChannelID = &id
		}
		if m != nil {
			if p, ok := previews[g.ID]; ok {
				pv := &GroupMessagePreview{AuthorUsername: nameOf(names, p.author), CreatedAt: apijson.NewTime(p.at)}
				if !p.deleted {
					ex := prefixChars(p.body, 140)
					pv.Excerpt = &ex
				}
				sum.LastMessage = pv
			}
			sum.UnreadCount = unreadByGroup[g.ID]
			ms := membershipDTO(m)
			sum.Membership = &ms
		}
		out = append(out, sum)
	}
	return out, nil
}

// detail is GroupsService.detail. The group is reloaded so counts reflect a
// join or leave made in the same request.
func (s *Service) detail(ctx context.Context, groupID uuid.UUID, v *community.Viewer) (*GroupDetail, error) {
	q := s.q()
	g, err := s.group(ctx, q, groupID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOwner(ctx, g); err != nil {
		return nil, err
	}
	m, err := s.access(ctx, g, v)
	if err != nil {
		return nil, err
	}
	hidden, err := s.hidden(ctx, v.UserID)
	if err != nil {
		return nil, err
	}
	sums, err := s.summaries(ctx, []groupsdb.Group{*g}, v.UserID, hidden)
	if err != nil {
		return nil, err
	}
	chans := []GroupChannel{}
	if m != nil || v.IsAdmin {
		records, err := s.channels(ctx, q, []uuid.UUID{g.ID})
		if err != nil {
			return nil, err
		}
		reads := map[uuid.UUID]readState{}
		if m != nil {
			ids := make([]uuid.UUID, len(records))
			for i := range records {
				ids[i] = records[i].ID
			}
			if reads, err = s.readStates(ctx, ids, v.UserID, hidden); err != nil {
				return nil, err
			}
		}
		for i := range records {
			var rs *readState
			if r, ok := reads[records[i].ID]; ok {
				rs = &r
			}
			chans = append(chans, channelDTO(&records[i], rs))
		}
	}
	var owner *string
	if g.OwnerID != nil {
		names, err := s.usernames(ctx, []uuid.UUID{*g.OwnerID})
		if err != nil {
			return nil, err
		}
		owner = nameOf(names, *g.OwnerID)
	}
	return &GroupDetail{Group: sums[0], Channels: chans, Permissions: permissions(m, v), OwnerUsername: owner}, nil
}

// inviteURL is GroupsService.inviteURL: SOCIAL_INVITE_BASE_URL + "/g/<code>".
func inviteURL(code string) string {
	base := social.ConfigFromEnv().InviteBaseURL
	return strings.TrimSuffix(base, "/") + "/g/" + code
}

func inviteLinkDTO(inv *groupsdb.GroupInvite, names map[uuid.UUID]string) GroupInviteLink {
	code := ""
	if inv.Code != nil {
		code = *inv.Code
	}
	dto := GroupInviteLink{
		ID:        apijson.NewUUID(inv.ID),
		GroupID:   apijson.NewUUID(inv.GroupID),
		Code:      code,
		URL:       inviteURL(code),
		ExpiresAt: apijson.TimePtr(inv.ExpiresAt),
		Uses:      int(inv.Uses),
		CreatedAt: apijson.NewTime(inv.CreatedAt),
	}
	if inv.MaxUses != nil {
		n := int(*inv.MaxUses)
		dto.MaxUses = &n
	}
	if inv.CreatedBy != nil {
		dto.CreatedByUsername = nameOf(names, *inv.CreatedBy)
	}
	return dto
}

// sortGroupsByActivity is myGroups' sort: lastMessageAt ?? createdAt,
// newest first.
func sortGroupsByActivity(gs []groupsdb.Group) {
	at := func(g groupsdb.Group) time.Time {
		if g.LastMessageAt != nil {
			return *g.LastMessageAt
		}
		return g.CreatedAt
	}
	sort.SliceStable(gs, func(i, j int) bool { return at(gs[i]).After(at(gs[j])) })
}
