// Package boards ports BoardsController, BoardsService, BoardNotifier and
// CommunityValidation (Community/*.swift): user-created topic boards under
// /v1/boards, /v1/board-posts, /v1/board-comments, /v1/board-reports and
// /v1/community, plus the Daily board's publisher and job.
package boards

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/platform/discord"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/server"
	"norviq-api/internal/store/boardsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts the Boards routes. Boards are not flag-gated in Vapor.
type Feature struct {
	// Push overrides the push sender (tests); nil uses push.From(d).
	Push Pusher
	// Discord overrides the report alert sender (tests); nil uses
	// discord.FromEnv.
	Discord discord.Sender
}

func (Feature) Name() string { return "boards" }

type handler struct {
	s       *Service
	n       *Notifier
	discord discord.Sender
	log     *slog.Logger
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s := &Service{DB: d.DB, Q: boardsdb.New(d.DB)}
	pusher := f.Push
	if pusher == nil {
		svc, err := push.From(d)
		if err != nil {
			return err
		}
		pusher = svc
	}
	sender := f.Discord
	if sender == nil {
		sender = discord.FromEnv(d.Logger)
	}
	h := &handler{s: s, n: &Notifier{S: s, Push: pusher, Logger: d.Logger}, discord: sender, log: d.Logger}

	access := community.New(d.DB, d.Admin.IsAdmin)
	base := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.FirstPartyOnly(), access.Middleware()}
	with := func(extra ...echo.MiddlewareFunc) []echo.MiddlewareFunc {
		return append(append([]echo.MiddlewareFunc{}, base...), extra...)
	}
	limit := func(n int, seconds int, prefix string) []echo.MiddlewareFunc {
		return with(d.Limiter.Limit(n, time.Duration(seconds)*time.Second, prefix))
	}
	v1 := r.V1

	v1.GET("/community/me", h.viewerStatus, with()...)
	v1.POST("/community/guidelines/accept", h.acceptGuidelines, with()...)
	v1.POST("/community/blocks", h.block, with()...)
	v1.DELETE("/community/blocks/:username", h.unblock, with()...)
	v1.GET("/community/notifications", h.notifications, with()...)
	v1.GET("/community/notifications/unread-count", h.unreadCount, with()...)
	v1.POST("/community/notifications/read", h.markRead, with()...)
	v1.GET("/community/notification-settings", h.notificationSettings, with()...)
	v1.PUT("/community/notification-settings", h.updateNotificationSettings, with()...)

	v1.GET("/boards", h.listBoards, with()...)
	// The per-day cap is enforced in the handler against the database; this
	// only stops a burst.
	v1.POST("/boards", h.createBoard, limit(10, 3600, "ratelimit:board-create")...)
	v1.GET("/boards/:slug", h.getBoard, with()...)
	v1.PATCH("/boards/:slug", h.updateBoard, with()...)
	v1.GET("/boards/:slug/posts", h.listPosts, with()...)
	v1.POST("/boards/:slug/posts", h.createPost, limit(10, 3600, "ratelimit:board-post")...)

	v1.GET("/board-posts/:postId", h.getPost, with()...)
	v1.DELETE("/board-posts/:postId", h.deletePost, with()...)
	v1.POST("/board-posts/:postId/vote", h.vote, limit(60, 60, "ratelimit:board-vote")...)
	v1.POST("/board-posts/:postId/comments", h.createComment, limit(30, 600, "ratelimit:board-comment")...)

	v1.DELETE("/board-comments/:commentId", h.deleteComment, with()...)
	v1.POST("/board-reports", h.report, limit(20, 3600, "ratelimit:board-report")...)
	return nil
}

func ctxOf(c *echo.Context) context.Context { return c.Request().Context() }

// param is `req.parameters.get(name) ?? ""`, percent-decoded as Vapor's
// router stored it.
func param(c *echo.Context, name string) string {
	raw := c.Param(name)
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}

// uuidParam is BoardsController.uuid.
func uuidParam(c *echo.Context, name string) (uuid.UUID, error) {
	id, ok := parseUUID(param(c, name))
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Invalid " + name)
	}
	return id, nil
}

// MARK: - Viewer

func sanctionDTO(s *community.Sanction, username *string) *UserSanction {
	kind := s.Kind
	if kind != community.SanctionMute && kind != community.SanctionBan {
		kind = community.SanctionMute
	}
	return &UserSanction{
		ID:        apijson.NewUUID(s.ID),
		Username:  username,
		Kind:      kind,
		Reason:    s.Reason,
		ExpiresAt: apijson.TimePtr(s.ExpiresAt),
		CreatedAt: apijson.NewTime(s.CreatedAt),
		RevokedAt: apijson.TimePtr(s.RevokedAt),
	}
}

func (h *handler) viewerStatus(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	var sanction *UserSanction
	if v.ActiveSanction != nil {
		sanction = sanctionDTO(v.ActiveSanction, v.Username)
	}
	// A social suspension has no sanction row; report it as the ban it acts
	// as, so clients need only one rule.
	if v.SocialSuspendedAt != nil && !v.IsAdmin && (sanction == nil || sanction.Kind != community.SanctionBan) {
		sanction = &UserSanction{
			ID:        apijson.NewUUID(v.UserID),
			Username:  v.Username,
			Kind:      community.SanctionBan,
			Reason:    "Suspended by a moderator.",
			CreatedAt: apijson.NewTime(*v.SocialSuspendedAt),
		}
	}
	return httpx.OK(c, CommunityViewerStatus{
		Username:           v.Username,
		IsAdmin:            v.IsAdmin,
		GuidelinesAccepted: v.GuidelinesAccepted,
		HasUsername:        v.Username != nil && *v.Username != "",
		ActiveSanction:     sanction,
	})
}

// acceptGuidelines is idempotent.
func (h *handler) acceptGuidelines(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if !v.GuidelinesAccepted {
		if err := h.s.Q.BoardGuidelinesAccept(ctxOf(c), boardsdb.BoardGuidelinesAcceptParams{
			ID: uuid.New(), UserID: v.UserID, AcceptedAt: h.s.now(),
		}); err != nil {
			return err
		}
	}
	return httpx.NoContent(c)
}

// block writes the same `social_blocks` row the Friends feature uses, so a
// block anywhere hides the person everywhere. Idempotent.
func (h *handler) block(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	body, err := decodeBody(c, []field{{name: "username", kind: fString}})
	if err != nil {
		return err
	}
	username := *body.str("username")
	// The system account is refused before the lookup, so "norviq" (or
	// "@norviq") gets the same 400 whether or not its row exists. Vapor
	// looked it up first and answered 404 when it did not.
	if users.IsSystemUsername(strings.Trim(trim(username), "@")) {
		return httpx.BadRequest("That account can't be blocked.")
	}
	ctx := ctxOf(c)
	target, err := h.s.userIDForUsername(ctx, username)
	if err != nil {
		return err
	}
	if target == v.UserID {
		return httpx.BadRequest("You can't block yourself.")
	}
	return h.s.inTx(ctx, func(q *boardsdb.Queries) error {
		exists, err := q.BoardBlockExists(ctx, boardsdb.BoardBlockExistsParams{BlockerID: v.UserID, BlockedID: target})
		if err != nil {
			return err
		}
		if !exists {
			if err := q.BoardInsertBlock(ctx, boardsdb.BoardInsertBlockParams{
				ID: uuid.New(), BlockerID: v.UserID, BlockedID: target, CreatedAt: h.s.now(),
			}); err != nil {
				return err
			}
		}
		if err := q.BoardUnfriend(ctx, boardsdb.BoardUnfriendParams{A: v.UserID, B: target}); err != nil {
			return err
		}
		return q.BoardDeleteFriendRequests(ctx, boardsdb.BoardDeleteFriendRequestsParams{A: v.UserID, B: target})
	}, func() error { return httpx.NoContent(c) })
}

func (h *handler) unblock(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	target, err := h.s.userIDForUsername(ctx, param(c, "username"))
	if err != nil {
		return err
	}
	if err := h.s.Q.BoardDeleteBlock(ctx, boardsdb.BoardDeleteBlockParams{BlockerID: v.UserID, BlockedID: target}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// MARK: - Notifications

func (h *handler) notifications(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	page, err := h.n.page(ctxOf(c), v.UserID, httpx.QueryString(c, "cursor"))
	if err != nil {
		return err
	}
	return httpx.OK(c, page)
}

func (h *handler) unreadCount(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	n, err := h.s.unreadCount(ctxOf(c), v.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, BoardUnreadCount{UnreadCount: n})
}

func (h *handler) markRead(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	body, err := decodeBody(c, []field{{name: "ids", kind: fUUIDs, optional: true}})
	if err != nil {
		return err
	}
	ids, present := body.ids("ids")
	if len(ids) > 200 {
		return httpx.BadRequest("Mark at most 200 notifications at once.")
	}
	if err := h.n.markRead(ctxOf(c), ids, !present, v.UserID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handler) notificationSettings(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	s, err := h.n.settings(ctxOf(c), v.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, s)
}

func (h *handler) updateNotificationSettings(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	body, err := decodeBody(c, []field{{name: "replyPush", kind: fBool}, {name: "upvotePush", kind: fBool}})
	if err != nil {
		return err
	}
	settings := BoardNotificationSettings{ReplyPush: body.boolean("replyPush"), UpvotePush: body.boolean("upvotePush")}
	ctx := ctxOf(c)
	rows, err := h.s.Q.BoardNotificationSettingsFor(ctx, v.UserID)
	if err != nil {
		return err
	}
	now := h.s.now()
	if len(rows) == 0 {
		err = h.s.Q.BoardNotificationSettingsInsert(ctx, boardsdb.BoardNotificationSettingsInsertParams{
			ID: uuid.New(), UserID: v.UserID, ReplyPush: settings.ReplyPush, UpvotePush: settings.UpvotePush, UpdatedAt: &now,
		})
	} else {
		err = h.s.Q.BoardNotificationSettingsUpdate(ctx, boardsdb.BoardNotificationSettingsUpdateParams{
			ID: rows[0].ID, ReplyPush: settings.ReplyPush, UpvotePush: settings.UpvotePush, UpdatedAt: &now,
		})
	}
	if err != nil {
		return err
	}
	return httpx.OK(c, settings)
}

// MARK: - Boards

// listBoards: the Daily board and its author are created by the first
// publish, so the board stays hidden until DAILY_BOARD_POST_ENABLED or the
// operator command writes a post.
func (h *handler) listBoards(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	resp, err := h.s.listBoards(ctxOf(c), httpx.QueryString(c, "cursor"), v.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handler) createBoard(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	body, err := decodeBody(c, []field{{name: "slug", kind: fString}, {name: "name", kind: fString}, {name: "description", kind: fString}})
	if err != nil {
		return err
	}
	slug, err := validateSlug(*body.str("slug"))
	if err != nil {
		return err
	}
	name, err := validateBoardName(*body.str("name"))
	if err != nil {
		return err
	}
	description, err := validateBoardDescription(*body.str("description"))
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	now := h.s.now()
	if !v.IsAdmin {
		creator := v.UserID
		recent, err := h.s.Q.BoardsCreatedSince(ctx, boardsdb.BoardsCreatedSinceParams{CreatorID: &creator, CreatedAt: now.Add(-24 * time.Hour)})
		if err != nil {
			return err
		}
		if recent >= maxBoardsPerDay {
			return httpx.CodedAbort(http.StatusTooManyRequests, "board_limit_reached",
				fmt.Sprintf("You can create up to %d boards a day.", maxBoardsPerDay))
		}
	}
	taken, err := h.s.Q.BoardBySlugWithDeleted(ctx, slug)
	if err != nil {
		return err
	}
	if len(taken) > 0 {
		return httpx.Conflict("That board address is taken.")
	}
	creator := v.UserID
	board := boardsdb.Board{
		ID: uuid.New(), Slug: slug, Name: name, Description: description, CreatorID: &creator,
		LastActivityAt: now, CreatedAt: h.s.now(),
	}
	if err := h.s.Q.BoardInsert(ctx, boardsdb.BoardInsertParams{
		ID: board.ID, Slug: slug, Name: name, Description: description, CreatorID: &creator,
		LastActivityAt: board.LastActivityAt, CreatedAt: board.CreatedAt,
	}); err != nil {
		return err
	}
	names := map[uuid.UUID]string{}
	if v.Username != nil {
		names[v.UserID] = *v.Username
	}
	return httpx.OK(c, boardDTO(&board, names))
}

func (h *handler) getBoard(c *echo.Context) error {
	board, err := h.s.boardBySlug(ctxOf(c), param(c, "slug"))
	if err != nil {
		return err
	}
	dtos, err := h.s.boardDTOs(ctxOf(c), []boardsdb.Board{*board})
	if err != nil {
		return err
	}
	return httpx.OK(c, dtos[0])
}

func (h *handler) updateBoard(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	ctx := ctxOf(c)
	board, err := h.s.boardBySlug(ctx, param(c, "slug"))
	if err != nil {
		return err
	}
	if (board.CreatorID == nil || *board.CreatorID != v.UserID) && !v.IsAdmin {
		return httpx.Forbidden("Only the board's creator can edit it.")
	}
	body, err := decodeBody(c, []field{{name: "description", kind: fString}})
	if err != nil {
		return err
	}
	description, err := validateBoardDescription(*body.str("description"))
	if err != nil {
		return err
	}
	if err := h.s.Q.BoardUpdateDescription(ctx, boardsdb.BoardUpdateDescriptionParams{ID: board.ID, Description: description}); err != nil {
		return err
	}
	board.Description = description
	dtos, err := h.s.boardDTOs(ctx, []boardsdb.Board{*board})
	if err != nil {
		return err
	}
	return httpx.OK(c, dtos[0])
}

// MARK: - Posts

func (h *handler) listPosts(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	board, err := h.s.boardBySlug(ctx, param(c, "slug"))
	if err != nil {
		return err
	}
	sort := sortNew
	if s := httpx.QueryString(c, "sort"); s != nil && (*s == sortTop || *s == sortActive || *s == sortNew) {
		sort = *s
	}
	var kind *string
	if k := httpx.QueryString(c, "kind"); k != nil && validKind(*k) {
		kind = k
	}
	var tag *string
	if t := httpx.QueryString(c, "tag"); t != nil {
		tags, err := validateTags([]string{*t})
		if err != nil {
			return err
		}
		if len(tags) > 0 {
			tag = &tags[0]
		}
	}
	page, err := h.s.listPosts(ctx, board, sort, kind, tag, httpx.QueryString(c, "cursor"), v.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, page)
}

func (h *handler) createPost(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	ctx := ctxOf(c)
	board, err := h.s.boardBySlug(ctx, param(c, "slug"))
	if err != nil {
		return err
	}
	body, err := decodeBody(c, []field{
		{name: "kind", kind: fEnum, enumName: "BoardPostKind", valid: validKind},
		{name: "title", kind: fString},
		{name: "url", kind: fString, optional: true},
		{name: "body", kind: fString, optional: true},
		{name: "tags", kind: fStrings},
	})
	if err != nil {
		return err
	}
	title, err := validateTitle(*body.str("title"))
	if err != nil {
		return err
	}
	tags, err := validateTags(body.strings("tags"))
	if err != nil {
		return err
	}
	text, err := validatePostBody(body.str("body"))
	if err != nil {
		return err
	}
	kind := *body.str("kind")
	var link, domain *string
	if kind == KindLink {
		u, d, err := validateLink(body.str("url"))
		if err != nil {
			return err
		}
		link, domain = &u, &d
	} else if raw := body.str("url"); raw != nil && trim(*raw) != "" {
		return httpx.BadRequest("Only link posts take a URL.")
	}

	now := h.s.now()
	post := boardsdb.BoardPost{
		ID: uuid.New(), BoardID: board.ID, AuthorID: v.UserID, Kind: kind, Title: title, Url: link, Domain: domain,
		Body: text, Tags: tags, ParticipantCount: 1, LastActivityAt: now, CreatedAt: h.s.now(),
	}
	if err := h.s.inTx(ctx, func(q *boardsdb.Queries) error {
		if err := insertPost(ctx, q, &post); err != nil {
			return err
		}
		return q.BoardBumpPost(ctx, boardsdb.BoardBumpPostParams{ID: board.ID, LastActivityAt: now})
	}, nil); err != nil {
		return err
	}
	achievementhooks.Schedule(v.UserID)
	return httpx.OK(c, summary(&post, board.Slug, v.Username, false, nil))
}

func insertPost(ctx context.Context, q *boardsdb.Queries, p *boardsdb.BoardPost) error {
	return q.BoardPostInsert(ctx, boardsdb.BoardPostInsertParams{
		ID: p.ID, BoardID: p.BoardID, AuthorID: p.AuthorID, Kind: p.Kind, Title: p.Title, Url: p.Url, Domain: p.Domain,
		Body: p.Body, Tags: p.Tags, LastActivityAt: p.LastActivityAt, CreatedAt: p.CreatedAt,
		AiGenerated: p.AiGenerated, AiTopic: p.AiTopic, TradingDate: p.TradingDate,
	})
}

func (h *handler) getPost(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "postId")
	if err != nil {
		return err
	}
	detail, err := h.s.detail(ctxOf(c), id, v.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, detail)
}

func (h *handler) deletePost(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "postId")
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	post, board, err := h.s.post(ctx, id)
	if err != nil {
		return err
	}
	if post.AuthorID != v.UserID && !v.IsAdmin {
		return httpx.Forbidden("You can only delete your own posts.")
	}
	now := h.s.now()
	return h.s.inTx(ctx, func(q *boardsdb.Queries) error {
		if err := q.BoardPostSoftDelete(ctx, boardsdb.BoardPostSoftDeleteParams{ID: post.ID, DeletedAt: &now}); err != nil {
			return err
		}
		return q.BoardDropPost(ctx, board.ID)
	}, func() error { return httpx.NoContent(c) })
}

// vote toggles the viewer's upvote. The vote row and the score move in one
// transaction so the score is always the row count.
func (h *handler) vote(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	id, err := uuidParam(c, "postId")
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	post, board, err := h.s.post(ctx, id)
	if err != nil {
		return err
	}
	// Same rule as comments: a post by someone blocked either way is not
	// there for you, so it cannot be voted on (Vapor let the vote through).
	blocked, err := h.s.blockedEitherWay(ctx, v.UserID, post.AuthorID)
	if err != nil {
		return err
	}
	if blocked {
		return httpx.NotFound("Post not found")
	}
	var result BoardVoteResponse
	if err := h.s.inTx(ctx, func(q *boardsdb.Queries) error {
		inserted, err := q.BoardVoteInsert(ctx, boardsdb.BoardVoteInsertParams{
			ID: uuid.New(), PostID: post.ID, UserID: v.UserID, CreatedAt: h.s.now(),
		})
		if err != nil {
			return err
		}
		voted := len(inserted) > 0
		delta := int64(1)
		if !voted {
			delta = -1
			if err := q.BoardVoteDelete(ctx, boardsdb.BoardVoteDeleteParams{PostID: post.ID, UserID: v.UserID}); err != nil {
				return err
			}
		}
		scores, err := q.BoardPostScoreMove(ctx, boardsdb.BoardPostScoreMoveParams{Delta: delta, ID: post.ID})
		if err != nil {
			return err
		}
		score := post.Score
		if len(scores) > 0 {
			score = scores[0]
		}
		result = BoardVoteResponse{Score: score, Voted: voted}
		return nil
	}, nil); err != nil {
		return err
	}
	if result.Voted {
		h.n.upvoted(ctx, post, board, v)
		// The score is the author's achievement, not the voter's.
		achievementhooks.Schedule(post.AuthorID)
	}
	return httpx.OK(c, result)
}

// MARK: - Comments

func (h *handler) createComment(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	id, err := uuidParam(c, "postId")
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	post, board, err := h.s.post(ctx, id)
	if err != nil {
		return err
	}
	body, err := decodeBody(c, []field{{name: "parentId", kind: fUUID, optional: true}, {name: "body", kind: fString}})
	if err != nil {
		return err
	}
	text, err := validateCommentBody(*body.str("body"))
	if err != nil {
		return err
	}
	blocked, err := h.s.blockedEitherWay(ctx, v.UserID, post.AuthorID)
	if err != nil {
		return err
	}
	if blocked {
		return httpx.NotFound("Post not found")
	}

	var depth int64
	var parent *boardsdb.BoardComment
	parentID := body.id("parentId")
	if parentID != nil {
		found, err := h.s.Q.BoardCommentByID(ctx, *parentID)
		if err != nil {
			return err
		}
		if len(found) == 0 || found[0].PostID != post.ID {
			return httpx.BadRequest("That reply target is gone.")
		}
		parent = &found[0]
		depth = parent.Depth + 1
		if depth > maxCommentDepth {
			return httpx.BadRequest("This thread is too deep to reply further.")
		}
	}

	now := h.s.now()
	comment := boardsdb.BoardComment{
		ID: uuid.New(), PostID: post.ID, ParentID: parentID, AuthorID: v.UserID, Body: text, Depth: depth, CreatedAt: h.s.now(),
	}
	if err := h.s.inTx(ctx, func(q *boardsdb.Queries) error {
		newParticipant := false
		if v.UserID != post.AuthorID {
			commented, err := q.BoardHasCommented(ctx, boardsdb.BoardHasCommentedParams{PostID: post.ID, AuthorID: v.UserID})
			if err != nil {
				return err
			}
			newParticipant = !commented
		}
		if err := q.BoardCommentInsert(ctx, boardsdb.BoardCommentInsertParams{
			ID: comment.ID, PostID: comment.PostID, ParentID: comment.ParentID, AuthorID: comment.AuthorID,
			Body: comment.Body, Depth: comment.Depth, CreatedAt: comment.CreatedAt,
		}); err != nil {
			return err
		}
		participants := int64(0)
		if newParticipant {
			participants = 1
		}
		if err := q.BoardPostCommentBump(ctx, boardsdb.BoardPostCommentBumpParams{Participants: participants, Now: now, ID: post.ID}); err != nil {
			return err
		}
		if err := q.BoardTouch(ctx, boardsdb.BoardTouchParams{ID: board.ID, LastActivityAt: now}); err != nil {
			return err
		}
		// Your own comment is not news to you.
		return q.BoardPostViewOwnComment(ctx, boardsdb.BoardPostViewOwnCommentParams{PostID: post.ID, UserID: v.UserID})
	}, nil); err != nil {
		return err
	}
	h.n.replied(ctx, &comment, parent, post, board, v)
	return httpx.OK(c, BoardComment{
		ID:             apijson.NewUUID(comment.ID),
		ParentID:       apijson.UUIDPtr(comment.ParentID),
		Depth:          comment.Depth,
		AuthorUsername: v.Username,
		Body:           comment.Body,
		CreatedAt:      apijson.NewTime(comment.CreatedAt),
	})
}

// deleteComment is a soft delete: the thread keeps the slot and shows it as
// deleted.
func (h *handler) deleteComment(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "commentId")
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	found, err := h.s.Q.BoardCommentByID(ctx, id)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return httpx.NotFound("Comment not found")
	}
	if found[0].AuthorID != v.UserID && !v.IsAdmin {
		return httpx.Forbidden("You can only delete your own comments.")
	}
	now := h.s.now()
	return h.s.inTx(ctx, func(q *boardsdb.Queries) error {
		deleted, err := q.BoardCommentSoftDelete(ctx, boardsdb.BoardCommentSoftDeleteParams{ID: id, DeletedAt: &now})
		if err != nil || len(deleted) == 0 {
			return err
		}
		// The post's count follows (Vapor never decremented it).
		return q.BoardPostCommentDrop(ctx, deleted[0])
	}, func() error { return httpx.NoContent(c) })
}

// MARK: - Reports

// Report target types (CommunityReportTarget).
const (
	reportTargetPost    = "board_post"
	reportTargetComment = "board_comment"
)

// report lands in the same `social_reports` queue as Friends reports, with
// a board target type, and pings Discord so a report is seen the same day.
func (h *handler) report(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	body, err := decodeBody(c, []field{
		{name: "postId", kind: fUUID, optional: true},
		{name: "commentId", kind: fUUID, optional: true},
		{name: "reason", kind: fEnum, enumName: "BoardReportReason", valid: validReportReason},
		{name: "note", kind: fString, optional: true},
	})
	if err != nil {
		return err
	}
	var note *string
	if raw := body.str("note"); raw != nil {
		t := trim(*raw)
		note = &t
	}
	if note != nil && charCount(*note) > maxReportNoteChars {
		return httpx.BadRequest("Keep the details under 1,000 characters.")
	}
	ctx := ctxOf(c)
	postID, commentID := body.id("postId"), body.id("commentId")
	var targetType, excerpt string
	var targetID uuid.UUID
	switch {
	case postID != nil && commentID == nil:
		posts, err := h.s.Q.BoardPostByID(ctx, *postID)
		if err != nil {
			return err
		}
		if len(posts) == 0 {
			return httpx.NotFound("Post not found")
		}
		targetType, targetID, excerpt = reportTargetPost, *postID, posts[0].Title
	case postID == nil && commentID != nil:
		comments, err := h.s.Q.BoardCommentByID(ctx, *commentID)
		if err != nil {
			return err
		}
		if len(comments) == 0 {
			return httpx.NotFound("Comment not found")
		}
		targetType, targetID, excerpt = reportTargetComment, *commentID, comments[0].Body
	default:
		return httpx.BadRequest("Report exactly one post or comment.")
	}
	if note != nil && *note == "" {
		note = nil
	}
	reason := *body.str("reason")
	if err := h.s.Q.BoardReportInsert(ctx, boardsdb.BoardReportInsertParams{
		ID: uuid.New(), ReporterID: v.UserID, TargetType: targetType, TargetID: apijson.FormatUUID(targetID),
		Reason: reason, Note: note, CreatedAt: h.s.now(),
	}); err != nil {
		return err
	}
	h.log.Info(fmt.Sprintf("boards.report filed target_type=%s reason=%s", targetType, reason))

	text := "🚩 Board report (" + reason + ") on " + targetType + ":\n```" + prefixChars(excerpt, 300) + "```"
	go func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := h.discord.Send(ctx, text); err != nil {
			h.log.Debug("Failed to send discord board report notification")
		}
	}(context.WithoutCancel(ctx))
	return c.NoContent(http.StatusAccepted)
}
