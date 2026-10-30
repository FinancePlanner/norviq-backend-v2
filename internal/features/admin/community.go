package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/boards"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/store/admindb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// maxSanctionHours is CommunityAdminController.maxSanctionHours.
const maxSanctionHours = 24 * 365

// dailyBoardSlug is DailyBoardIdentity.slug.
const dailyBoardSlug = "daily"

// CommunityReportTarget values (BoardsController.swift).
const (
	targetPost         = "board_post"
	targetComment      = "board_comment"
	targetGroupMessage = "group_message"
)

// sanctionKind is CommunitySanctionKind.
type sanctionKind string

func (sanctionKind) SwiftEnum() (string, []string, bool) {
	return "CommunitySanctionKind", []string{"mute", "ban"}, false
}

// createSanctionRequest is CreateSanctionRequest.
type createSanctionRequest struct {
	Username      string       `json:"username"`
	Kind          sanctionKind `json:"kind"`
	Reason        string       `json:"reason"`
	DurationHours *int         `json:"durationHours"`
}

// userSanctionList is UserSanctionListResponse.
type userSanctionList struct {
	Items []boards.UserSanction `json:"items"`
}

// boardReportItem is BoardReportItem (StockPlanShared 5.26.0).
type boardReportItem struct {
	ID                   apijson.UUID  `json:"id"`
	ReporterUsername     *string       `json:"reporterUsername,omitempty"`
	PostID               *apijson.UUID `json:"postId,omitempty"`
	CommentID            *apijson.UUID `json:"commentId,omitempty"`
	BoardSlug            *string       `json:"boardSlug,omitempty"`
	Excerpt              string        `json:"excerpt"`
	TargetAuthorUsername *string       `json:"targetAuthorUsername,omitempty"`
	Reason               string        `json:"reason"`
	Note                 *string       `json:"note,omitempty"`
	CreatedAt            apijson.Time  `json:"createdAt"`
	GroupID              *apijson.UUID `json:"groupId,omitempty"`
	GroupMessageID       *apijson.UUID `json:"groupMessageId,omitempty"`
	GroupName            *string       `json:"groupName,omitempty"`
}

type boardReportList struct {
	Items []boardReportItem `json:"items"`
}

// boardReportReason is `BoardReportReason(rawValue:) ?? .other`.
func boardReportReason(raw string) string {
	switch raw {
	case "spam", "harassment", "hate", "scam", "impersonation", "inappropriate", "other":
		return raw
	}
	return "other"
}

// sanctionKindOf is `CommunitySanctionKind(rawValue:) ?? .mute`.
func sanctionKindOf(raw string) string {
	if raw == "ban" {
		return "ban"
	}
	return "mute"
}

// sanctionDTO is CommunityAdminController.dto(_:username:).
func sanctionDTO(s admindb.CommunitySanction, username *string) boards.UserSanction {
	return boards.UserSanction{
		ID:        apijson.NewUUID(s.ID),
		Username:  username,
		Kind:      sanctionKindOf(s.Kind),
		Reason:    s.Reason,
		ExpiresAt: apijson.TimePtr(s.ExpiresAt),
		CreatedAt: apijson.NewTime(s.CreatedAt),
		RevokedAt: apijson.TimePtr(s.RevokedAt),
	}
}

// usernames is BoardsService.usernames(for:): users without a username are
// left out.
func (h *handler) usernames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	seen := map[uuid.UUID]bool{}
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	rows, err := h.q.UsernamesForUsers(ctx, unique)
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

func nameOf(names map[uuid.UUID]string, id uuid.UUID) *string {
	if n, ok := names[id]; ok {
		return &n
	}
	return nil
}

// DELETE /v1/admin/boards/:slug — soft delete: the board and its posts
// disappear, the slug stays taken.
func (h *handler) deleteBoard(c *echo.Context) error {
	ctx := c.Request().Context()
	rows, err := h.q.LiveBoardBySlug(ctx, strings.ToLower(c.Param("slug")))
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("Board not found")
	}
	board := rows[0]
	if board.Slug == dailyBoardSlug {
		return httpx.BadRequest("The Daily board stays up.")
	}
	now := h.now()
	if err := h.q.SoftDeleteBoard(ctx, admindb.SoftDeleteBoardParams{DeletedAt: &now, ID: board.ID}); err != nil {
		return err
	}
	h.d.Logger.Info("boards.admin deleted board slug=" + board.Slug)
	return httpx.NoContent(c)
}

// GET /v1/admin/community/sanctions
func (h *handler) listSanctions(c *echo.Context) error {
	ctx := c.Request().Context()
	rows, err := h.q.ListSanctions(ctx)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.UserID
	}
	names, err := h.usernames(ctx, ids)
	if err != nil {
		return err
	}
	items := make([]boards.UserSanction, len(rows))
	for i, r := range rows {
		items[i] = sanctionDTO(r, nameOf(names, r.UserID))
	}
	return httpx.OK(c, userSanctionList{Items: items})
}

// POST /v1/admin/community/sanctions — a new sanction of the same kind
// replaces the one in force.
func (h *handler) createSanction(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body createSanctionRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	if users.IsSystemUsername(body.Username) {
		return httpx.BadRequest("That account can't be sanctioned.")
	}
	ctx := c.Request().Context()
	// BoardsService.userId(forUsername:)
	username := strings.Trim(strings.TrimSpace(body.Username), "@")
	if username == "" {
		return httpx.NotFound("User not found")
	}
	ids, err := h.q.UserByUsername(ctx, &username)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return httpx.NotFound("User not found")
	}
	target := ids[0]
	targetRows, err := h.q.UserForModeration(ctx, target)
	if err != nil {
		return err
	}
	if len(targetRows) == 0 || h.d.Admin.IsAdmin(target) {
		return httpx.BadRequest("Admins can't be muted or banned.")
	}
	targetUser := targetRows[0]
	reason := strings.TrimSpace(body.Reason)
	if reason == "" || boards.CharCount(reason) > 500 {
		return httpx.BadRequest("Give a reason of up to 500 characters.")
	}
	if body.DurationHours != nil && (*body.DurationHours < 1 || *body.DurationHours > maxSanctionHours) {
		return httpx.BadRequest("Duration must be between 1 hour and a year, or empty for indefinite.")
	}

	now := h.now()
	var expiresAt *time.Time
	if body.DurationHours != nil {
		t := now.Add(time.Duration(*body.DurationHours) * time.Hour)
		expiresAt = &t
	}
	adminID := s.UserID
	sanction := admindb.CommunitySanction{
		ID: uuid.New(), UserID: target, Kind: string(body.Kind), Reason: reason,
		ExpiresAt: expiresAt, CreatedBy: &adminID, CreatedAt: h.now(),
	}
	err = h.inTx(ctx, func(q *admindb.Queries) error {
		if err := q.RevokeOpenSanctions(ctx, admindb.RevokeOpenSanctionsParams{RevokedAt: &now, UserID: target, Kind: sanction.Kind}); err != nil {
			return err
		}
		return q.InsertSanction(ctx, admindb.InsertSanctionParams{
			ID: sanction.ID, UserID: sanction.UserID, Kind: sanction.Kind, Reason: sanction.Reason,
			ExpiresAt: sanction.ExpiresAt, CreatedBy: sanction.CreatedBy, CreatedAt: sanction.CreatedAt,
		})
	})
	if err != nil {
		return err
	}
	hours := "indefinite"
	if body.DurationHours != nil {
		hours = strconv.Itoa(*body.DurationHours)
	}
	h.d.Logger.Info("boards.admin sanction kind=" + sanction.Kind + " hours=" + hours)
	return httpx.OK(c, sanctionDTO(sanction, targetUser.Username))
}

// DELETE /v1/admin/community/sanctions/:sanctionId
func (h *handler) revokeSanction(c *echo.Context) error {
	ctx := c.Request().Context()
	id, ok := portfolios.ParseSwiftUUID(c.Param("sanctionId"))
	if !ok {
		return httpx.NotFound("Sanction not found")
	}
	rows, err := h.q.SanctionByID(ctx, id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("Sanction not found")
	}
	if rows[0].RevokedAt == nil {
		now := h.now()
		if err := h.q.SetSanctionRevoked(ctx, admindb.SetSanctionRevokedParams{RevokedAt: &now, ID: id}); err != nil {
			return err
		}
	}
	return httpx.NoContent(c)
}

// GET /v1/admin/community/reports — open board and group-message reports,
// newest first. Reported content may already be deleted; the queue still
// shows it.
func (h *handler) listReports(c *echo.Context) error {
	ctx := c.Request().Context()
	reports, err := h.q.OpenCommunityReports(ctx, []string{targetPost, targetComment, targetGroupMessage})
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(reports))
	for i := range reports {
		r := &reports[i]
		if id, ok := portfolios.ParseSwiftUUID(r.TargetID); ok {
			ids = append(ids, id)
		}
	}
	comments, err := h.q.CommentsByIDsWithDeleted(ctx, ids)
	if err != nil {
		return err
	}
	postIDs := append([]uuid.UUID{}, ids...)
	for _, cm := range comments {
		postIDs = append(postIDs, cm.PostID)
	}
	posts, err := h.q.PostsByIDsWithDeleted(ctx, postIDs)
	if err != nil {
		return err
	}
	boardIDs := make([]uuid.UUID, len(posts))
	for i, p := range posts {
		boardIDs[i] = p.BoardID
	}
	boardRows, err := h.q.BoardsByIDsWithDeleted(ctx, boardIDs)
	if err != nil {
		return err
	}
	messages, err := h.q.GroupMessagesByIDs(ctx, ids)
	if err != nil {
		return err
	}
	groupIDs := make([]uuid.UUID, len(messages))
	for i, m := range messages {
		groupIDs[i] = m.GroupID
	}
	groups, err := h.q.GroupsByIDsWithDeleted(ctx, groupIDs)
	if err != nil {
		return err
	}

	messageByID := map[uuid.UUID]admindb.GroupMessagesByIDsRow{}
	for _, m := range messages {
		if _, dup := messageByID[m.ID]; !dup {
			messageByID[m.ID] = m
		}
	}
	groupNameByID := map[uuid.UUID]string{}
	for _, g := range groups {
		if _, dup := groupNameByID[g.ID]; !dup {
			groupNameByID[g.ID] = g.Name
		}
	}
	commentByID := map[uuid.UUID]admindb.CommentsByIDsWithDeletedRow{}
	for _, cm := range comments {
		if _, dup := commentByID[cm.ID]; !dup {
			commentByID[cm.ID] = cm
		}
	}
	postByID := map[uuid.UUID]admindb.PostsByIDsWithDeletedRow{}
	for _, p := range posts {
		if _, dup := postByID[p.ID]; !dup {
			postByID[p.ID] = p
		}
	}
	slugByID := map[uuid.UUID]string{}
	for _, b := range boardRows {
		if _, dup := slugByID[b.ID]; !dup {
			slugByID[b.ID] = b.Slug
		}
	}

	nameIDs := make([]uuid.UUID, 0, len(reports)+len(posts)+len(comments)+len(messages))
	for i := range reports {
		r := &reports[i]
		nameIDs = append(nameIDs, r.ReporterID)
	}
	for _, p := range posts {
		nameIDs = append(nameIDs, p.AuthorID)
	}
	for _, cm := range comments {
		nameIDs = append(nameIDs, cm.AuthorID)
	}
	for _, m := range messages {
		nameIDs = append(nameIDs, m.AuthorID)
	}
	names, err := h.usernames(ctx, nameIDs)
	if err != nil {
		return err
	}

	items := make([]boardReportItem, 0, len(reports))
	for i := range reports {
		r := &reports[i]
		targetID, hasTarget := portfolios.ParseSwiftUUID(r.TargetID)
		item := boardReportItem{
			ID:               apijson.NewUUID(r.ID),
			ReporterUsername: nameOf(names, r.ReporterID),
			Reason:           boardReportReason(r.Reason),
			Note:             r.Note,
			CreatedAt:        apijson.NewTime(r.CreatedAt),
		}
		if r.TargetType == targetGroupMessage {
			excerpt := "(gone)"
			if m, ok := messageByID[targetID]; hasTarget && ok {
				excerpt = m.Body
				item.TargetAuthorUsername = nameOf(names, m.AuthorID)
				gid, mid := apijson.NewUUID(m.GroupID), apijson.NewUUID(m.ID)
				item.GroupID, item.GroupMessageID = &gid, &mid
				if n, ok := groupNameByID[m.GroupID]; ok {
					item.GroupName = &n
				}
			}
			item.Excerpt = boards.PrefixChars(excerpt, 280)
			items = append(items, item)
			continue
		}
		var comment *admindb.CommentsByIDsWithDeletedRow
		var post *admindb.PostsByIDsWithDeletedRow
		if r.TargetType == targetComment {
			if cm, ok := commentByID[targetID]; hasTarget && ok {
				comment = &cm
				if p, ok := postByID[cm.PostID]; ok {
					post = &p
				}
			}
		} else if p, ok := postByID[targetID]; hasTarget && ok {
			post = &p
		}
		excerpt := "(gone)"
		var authorID *uuid.UUID
		switch {
		case comment != nil:
			excerpt = comment.Body
			authorID = &comment.AuthorID
		case post != nil:
			excerpt = post.Title
		}
		if post != nil {
			pid := apijson.NewUUID(post.ID)
			item.PostID = &pid
			if s, ok := slugByID[post.BoardID]; ok {
				item.BoardSlug = &s
			}
			if authorID == nil {
				authorID = &post.AuthorID
			}
		}
		if comment != nil {
			cid := apijson.NewUUID(comment.ID)
			item.CommentID = &cid
		}
		if authorID != nil {
			item.TargetAuthorUsername = nameOf(names, *authorID)
		}
		item.Excerpt = boards.PrefixChars(excerpt, 280)
		items = append(items, item)
	}
	return httpx.OK(c, boardReportList{Items: items})
}

// POST /v1/admin/community/reports/:reportId/resolve — closes the report
// with the same fields the social moderation queue writes.
func (h *handler) resolveReport(c *echo.Context) error {
	ctx := c.Request().Context()
	id, ok := portfolios.ParseSwiftUUID(c.Param("reportId"))
	if !ok {
		return httpx.NotFound("Report not found")
	}
	rows, err := h.q.ReportByID(ctx, id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("Report not found")
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	now := h.now()
	resolver := s.UserID
	if err := h.q.DismissCommunityReport(ctx, admindb.DismissCommunityReportParams{ResolvedBy: &resolver, ResolvedAt: &now, ID: id}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
