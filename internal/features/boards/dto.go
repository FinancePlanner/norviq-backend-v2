package boards

import "norviq-api/internal/apijson"

// StockPlanShared Community DTOs (BoardsDTOs.swift, BoardNotificationsDTOs.swift).

// Post kinds (BoardPostKind).
const (
	KindLink = "link"
	KindText = "text"
	KindAsk  = "ask"
	KindShow = "show"
)

func validKind(k string) bool { return k == KindLink || k == KindText || k == KindAsk || k == KindShow }

// validAITopic is BoardAITopic(rawValue:): the shared enum knows only these,
// so every other DailyBoardTopic (mag7, etf, macro, ...) encodes no aiTopic.
func validAITopic(t string) bool { return t == "stock" || t == "index" || t == "news" }

// validReportReason is BoardReportReason(rawValue:).
func validReportReason(r string) bool {
	switch r {
	case "spam", "harassment", "hate", "scam", "impersonation", "inappropriate", "other":
		return true
	}
	return false
}

// BoardSummary is the shared DTO.
type BoardSummary struct {
	ID              apijson.UUID `json:"id"`
	Slug            string       `json:"slug"`
	Name            string       `json:"name"`
	Description     string       `json:"description"`
	CreatorUsername *string      `json:"creatorUsername,omitempty"`
	PostCount       int64        `json:"postCount"`
	CreatedAt       apijson.Time `json:"createdAt"`
	LastActivityAt  apijson.Time `json:"lastActivityAt"`
}

// BoardListResponse is the shared DTO.
type BoardListResponse struct {
	Items      []BoardSummary    `json:"items"`
	NextCursor *string           `json:"nextCursor,omitempty"`
	DailyPost  *BoardPostSummary `json:"dailyPost,omitempty"`
}

// BoardPostSummary is the shared DTO (its custom encode keeps tags,
// viewerHasVoted and aiGenerated always present).
type BoardPostSummary struct {
	ID               apijson.UUID `json:"id"`
	BoardSlug        string       `json:"boardSlug"`
	Kind             string       `json:"kind"`
	Title            string       `json:"title"`
	URL              *string      `json:"url,omitempty"`
	Domain           *string      `json:"domain,omitempty"`
	Tags             []string     `json:"tags"`
	AuthorUsername   *string      `json:"authorUsername,omitempty"`
	CreatedAt        apijson.Time `json:"createdAt"`
	Score            int64        `json:"score"`
	CommentCount     int64        `json:"commentCount"`
	ParticipantCount int64        `json:"participantCount"`
	ViewCount        int64        `json:"viewCount"`
	ViewerHasVoted   bool         `json:"viewerHasVoted"`
	NewCommentCount  *int64       `json:"newCommentCount,omitempty"`
	AIGenerated      bool         `json:"aiGenerated"`
	AITopic          *string      `json:"aiTopic,omitempty"`
}

// BoardPostPage is the shared DTO.
type BoardPostPage struct {
	Items      []BoardPostSummary `json:"items"`
	NextCursor *string            `json:"nextCursor,omitempty"`
}

// BoardComment is the shared DTO.
type BoardComment struct {
	ID             apijson.UUID  `json:"id"`
	ParentID       *apijson.UUID `json:"parentId,omitempty"`
	Depth          int64         `json:"depth"`
	AuthorUsername *string       `json:"authorUsername,omitempty"`
	Body           string        `json:"body"`
	CreatedAt      apijson.Time  `json:"createdAt"`
	IsDeleted      bool          `json:"isDeleted"`
}

// BoardPostDetail is the shared DTO.
type BoardPostDetail struct {
	Post     BoardPostSummary `json:"post"`
	Body     *string          `json:"body,omitempty"`
	Comments []BoardComment   `json:"comments"`
}

// BoardVoteResponse is the shared DTO.
type BoardVoteResponse struct {
	Score int64 `json:"score"`
	Voted bool  `json:"voted"`
}

// UserSanction is the shared DTO.
type UserSanction struct {
	ID        apijson.UUID  `json:"id"`
	Username  *string       `json:"username,omitempty"`
	Kind      string        `json:"kind"`
	Reason    string        `json:"reason"`
	ExpiresAt *apijson.Time `json:"expiresAt,omitempty"`
	CreatedAt apijson.Time  `json:"createdAt"`
	RevokedAt *apijson.Time `json:"revokedAt,omitempty"`
}

// CommunityViewerStatus is the shared DTO.
type CommunityViewerStatus struct {
	Username           *string       `json:"username,omitempty"`
	IsAdmin            bool          `json:"isAdmin"`
	GuidelinesAccepted bool          `json:"guidelinesAccepted"`
	HasUsername        bool          `json:"hasUsername"`
	ActiveSanction     *UserSanction `json:"activeSanction,omitempty"`
}

// BoardNotification is the shared DTO.
type BoardNotification struct {
	ID            apijson.UUID  `json:"id"`
	Kind          string        `json:"kind"`
	ActorUsername *string       `json:"actorUsername,omitempty"`
	PostID        apijson.UUID  `json:"postId"`
	BoardSlug     string        `json:"boardSlug"`
	PostTitle     string        `json:"postTitle"`
	CommentID     *apijson.UUID `json:"commentId,omitempty"`
	Excerpt       *string       `json:"excerpt,omitempty"`
	CreatedAt     apijson.Time  `json:"createdAt"`
	IsRead        bool          `json:"isRead"`
}

// BoardNotificationPage is the shared DTO.
type BoardNotificationPage struct {
	Items       []BoardNotification `json:"items"`
	NextCursor  *string             `json:"nextCursor,omitempty"`
	UnreadCount int64               `json:"unreadCount"`
}

// BoardUnreadCount is the shared DTO.
type BoardUnreadCount struct {
	UnreadCount int64 `json:"unreadCount"`
}

// BoardNotificationSettings is the shared DTO.
type BoardNotificationSettings struct {
	ReplyPush  bool `json:"replyPush"`
	UpvotePush bool `json:"upvotePush"`
}
