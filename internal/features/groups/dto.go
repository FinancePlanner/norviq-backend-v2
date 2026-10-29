package groups

import "norviq-api/internal/apijson"

// StockPlanShared/Community/GroupsDTOs.swift.

// GroupTopic, GroupVisibility, GroupRole, GroupChannelKind and
// GroupMembershipRemovalReason raw values.
const (
	TopicStock     = "stock"
	TopicETF       = "etf"
	TopicCrypto    = "crypto"
	TopicCommunity = "community"
	TopicCustom    = "custom"
	TopicOther     = "other"

	VisibilityPrivate = "private"
	VisibilityPublic  = "public"

	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleOther  = "other"

	KindText  = "text"
	KindOther = "other"

	RemovedLeft    = "left"
	RemovedKicked  = "kicked"
	RemovedBanned  = "banned"
	RemovedDeleted = "deleted"
)

// topicOf is GroupTopic(rawValue:) ?? .other (the decoder's fallback too).
func topicOf(raw string) string {
	switch raw {
	case TopicStock, TopicETF, TopicCrypto, TopicCommunity, TopicCustom:
		return raw
	}
	return TopicOther
}

func topicAllowsTicker(t string) bool { return t == TopicStock || t == TopicETF || t == TopicCrypto }

func visibilityOf(raw string) string {
	if raw == VisibilityPublic {
		return VisibilityPublic
	}
	return VisibilityPrivate
}

func roleCanManage(r string) bool { return r == RoleOwner || r == RoleAdmin }

// GroupMembership is the viewer's own membership.
type GroupMembership struct {
	Role        string       `json:"role"`
	PushEnabled bool         `json:"pushEnabled"`
	JoinedAt    apijson.Time `json:"joinedAt"`
}

// GroupMessagePreview is the list row's last message.
type GroupMessagePreview struct {
	AuthorUsername *string      `json:"authorUsername,omitempty"`
	Excerpt        *string      `json:"excerpt,omitempty"`
	CreatedAt      apijson.Time `json:"createdAt"`
}

// GroupSummary is one group as the viewer sees it.
type GroupSummary struct {
	ID               apijson.UUID         `json:"id"`
	Name             string               `json:"name"`
	Description      string               `json:"description"`
	IconEmoji        *string              `json:"iconEmoji,omitempty"`
	Topic            string               `json:"topic"`
	Ticker           *string              `json:"ticker,omitempty"`
	Visibility       string               `json:"visibility"`
	MemberCount      int                  `json:"memberCount"`
	DefaultChannelID *apijson.UUID        `json:"defaultChannelId,omitempty"`
	LastMessageAt    *apijson.Time        `json:"lastMessageAt,omitempty"`
	LastMessage      *GroupMessagePreview `json:"lastMessage,omitempty"`
	UnreadCount      int                  `json:"unreadCount"`
	Membership       *GroupMembership     `json:"membership,omitempty"`
	CreatedAt        apijson.Time         `json:"createdAt"`
}

// GroupPermissions is what the viewer may do in a group.
type GroupPermissions struct {
	CanPost           bool `json:"canPost"`
	CanInvite         bool `json:"canInvite"`
	CanEdit           bool `json:"canEdit"`
	CanManageChannels bool `json:"canManageChannels"`
	CanModerate       bool `json:"canModerate"`
	CanDelete         bool `json:"canDelete"`
}

// GroupChannel is one channel with the viewer's read state.
type GroupChannel struct {
	ID            apijson.UUID  `json:"id"`
	GroupID       apijson.UUID  `json:"groupId"`
	Name          string        `json:"name"`
	Kind          string        `json:"kind"`
	Topic         *string       `json:"topic,omitempty"`
	Position      int           `json:"position"`
	LastSeq       int64         `json:"lastSeq"`
	LastMessageAt *apijson.Time `json:"lastMessageAt,omitempty"`
	LastReadSeq   int64         `json:"lastReadSeq"`
	UnreadCount   int           `json:"unreadCount"`
}

// GroupDetail is GET /v1/groups/:id.
type GroupDetail struct {
	Group         GroupSummary     `json:"group"`
	Channels      []GroupChannel   `json:"channels"`
	Permissions   GroupPermissions `json:"permissions"`
	OwnerUsername *string          `json:"ownerUsername,omitempty"`
}

// GroupListResponse is GET /v1/groups.
type GroupListResponse struct {
	Items                  []GroupSummary `json:"items"`
	PendingInvitationCount int            `json:"pendingInvitationCount"`
}

// GroupDirectoryPage is GET /v1/groups/discover.
type GroupDirectoryPage struct {
	Items      []GroupSummary `json:"items"`
	NextCursor *string        `json:"nextCursor,omitempty"`
}

// GroupUnreadCount is GET /v1/groups/unread-count.
type GroupUnreadCount struct {
	Total                  int `json:"total"`
	PendingInvitationCount int `json:"pendingInvitationCount"`
}

// GroupMember is one member row (or a ban, shaped as a member).
type GroupMember struct {
	UserID      apijson.UUID `json:"userId"`
	Username    *string      `json:"username,omitempty"`
	DisplayName *string      `json:"displayName,omitempty"`
	Role        string       `json:"role"`
	JoinedAt    apijson.Time `json:"joinedAt"`
}

// GroupMemberPage is GET /v1/groups/:id/members and /bans.
type GroupMemberPage struct {
	Items      []GroupMember `json:"items"`
	NextCursor *string       `json:"nextCursor,omitempty"`
}

// GroupMessage is one chat message.
type GroupMessage struct {
	ID              apijson.UUID  `json:"id"`
	GroupID         apijson.UUID  `json:"groupId"`
	ChannelID       apijson.UUID  `json:"channelId"`
	Seq             int64         `json:"seq"`
	ClientMessageID *string       `json:"clientMessageId,omitempty"`
	AuthorID        *apijson.UUID `json:"authorId,omitempty"`
	AuthorUsername  *string       `json:"authorUsername,omitempty"`
	Body            *string       `json:"body,omitempty"`
	IsDeleted       bool          `json:"isDeleted"`
	CreatedAt       apijson.Time  `json:"createdAt"`
}

// GroupMessagePage is GET /v1/group-channels/:id/messages.
type GroupMessagePage struct {
	Items         []GroupMessage `json:"items"`
	HasMoreBefore bool           `json:"hasMoreBefore"`
	HasMoreAfter  bool           `json:"hasMoreAfter"`
	LastReadSeq   int64          `json:"lastReadSeq"`
}

// GroupInviteLink is a shareable link.
type GroupInviteLink struct {
	ID                apijson.UUID  `json:"id"`
	GroupID           apijson.UUID  `json:"groupId"`
	Code              string        `json:"code"`
	URL               string        `json:"url"`
	ExpiresAt         *apijson.Time `json:"expiresAt,omitempty"`
	MaxUses           *int          `json:"maxUses,omitempty"`
	Uses              int           `json:"uses"`
	CreatedByUsername *string       `json:"createdByUsername,omitempty"`
	CreatedAt         apijson.Time  `json:"createdAt"`
}

// GroupInviteLinkList is GET /v1/groups/:id/invites.
type GroupInviteLinkList struct {
	Items []GroupInviteLink `json:"items"`
}

// GroupInvitePreview is GET /v1/group-invites/:code.
type GroupInvitePreview struct {
	Code            string        `json:"code"`
	Group           GroupSummary  `json:"group"`
	InviterUsername *string       `json:"inviterUsername,omitempty"`
	AlreadyMember   bool          `json:"alreadyMember"`
	ExpiresAt       *apijson.Time `json:"expiresAt,omitempty"`
}

// GroupDirectInvitationResponse is POST /v1/groups/:id/invitations.
type GroupDirectInvitationResponse struct {
	InvitedUserIDs []apijson.UUID `json:"invitedUserIds"`
}

// GroupInvitation is a pending direct invitation to the viewer.
type GroupInvitation struct {
	ID              apijson.UUID `json:"id"`
	Group           GroupSummary `json:"group"`
	InviterUsername *string      `json:"inviterUsername,omitempty"`
	CreatedAt       apijson.Time `json:"createdAt"`
}

// GroupInvitationList is GET /v1/groups/invitations.
type GroupInvitationList struct {
	Items []GroupInvitation `json:"items"`
}

// Realtime event names (GroupRealtimeEventType).
const (
	EventReady              = "ready"
	EventMessageCreated     = "message.created"
	EventMessageDeleted     = "message.deleted"
	EventChannelRead        = "channel.read"
	EventChannelCreated     = "channel.created"
	EventChannelUpdated     = "channel.updated"
	EventChannelDeleted     = "channel.deleted"
	EventGroupUpdated       = "group.updated"
	EventMembershipRemoved  = "membership.removed"
	EventInvitationReceived = "invitation.received"
	EventReconnect          = "reconnect"
)

// GroupRealtimeEvent is the `data:` payload of one stream event.
type GroupRealtimeEvent struct {
	Type       string           `json:"type"`
	GroupID    *apijson.UUID    `json:"groupId,omitempty"`
	ChannelID  *apijson.UUID    `json:"channelId,omitempty"`
	Message    *GroupMessage    `json:"message,omitempty"`
	MessageID  *apijson.UUID    `json:"messageId,omitempty"`
	Seq        *int64           `json:"seq,omitempty"`
	Channel    *GroupChannel    `json:"channel,omitempty"`
	Group      *GroupSummary    `json:"group,omitempty"`
	Reason     *string          `json:"reason,omitempty"`
	Invitation *GroupInvitation `json:"invitation,omitempty"`
	ServerTime *apijson.Time    `json:"serverTime,omitempty"`
}
