package boards

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/store/boardsdb"

	"github.com/google/uuid"
)

// BoardNotifier.swift: writes the Boards activity feed and sends the
// matching push. Best effort throughout: a failure here is logged and never
// fails the vote or comment that caused it.

// upvotePushWindow holds upvote pushes for the same post to one an hour; the
// feed still records every voter.
const upvotePushWindow = time.Hour

// Notification kinds (BoardNotificationKind).
const (
	notifyReply  = "reply"
	notifyUpvote = "upvote"
	notifyOther  = "other"
)

// Pusher sends one push to a user's active devices. push.Service satisfies
// it; tests record.
type Pusher interface {
	SendToUser(ctx context.Context, userID uuid.UUID, n push.Notification) (push.Summary, error)
}

// Notifier is BoardNotifier.
type Notifier struct {
	S      *Service
	Push   Pusher // nil: no pushes
	Logger *slog.Logger
}

var defaultSettings = BoardNotificationSettings{ReplyPush: true, UpvotePush: true}

func (n *Notifier) settings(ctx context.Context, userID uuid.UUID) (BoardNotificationSettings, error) {
	rows, err := n.S.Q.BoardNotificationSettingsFor(ctx, userID)
	if err != nil {
		return BoardNotificationSettings{}, err
	}
	if len(rows) == 0 {
		return defaultSettings, nil
	}
	return BoardNotificationSettings{ReplyPush: rows[0].ReplyPush, UpvotePush: rows[0].UpvotePush}, nil
}

func who(actor *community.Viewer) string {
	if actor.Username != nil {
		return "@" + *actor.Username
	}
	return "Someone"
}

// replied tells the parent comment's author, or the post's author for a
// top-level comment. Nobody is told about their own reply, or by someone
// either side has blocked.
func (n *Notifier) replied(ctx context.Context, comment *boardsdb.BoardComment, parent *boardsdb.BoardComment,
	post *boardsdb.BoardPost, board *boardsdb.Board, actor *community.Viewer,
) {
	recipient := post.AuthorID
	if parent != nil {
		recipient = parent.AuthorID
	}
	if recipient == actor.UserID {
		return
	}
	err := func() error {
		blocked, err := n.S.blockedEitherWay(ctx, recipient, actor.UserID)
		if err != nil || blocked {
			return err
		}
		actorID, commentID := actor.UserID, comment.ID
		if err := n.S.Q.BoardNotificationInsertReply(ctx, boardsdb.BoardNotificationInsertReplyParams{
			ID: uuid.New(), RecipientID: recipient, ActorID: &actorID, PostID: post.ID, CommentID: &commentID, CreatedAt: n.S.now(),
		}); err != nil {
			return err
		}
		settings, err := n.settings(ctx, recipient)
		if err != nil || !settings.ReplyPush {
			return err
		}
		title := who(actor) + " replied to you"
		if parent == nil {
			title = who(actor) + " commented on your post"
		}
		n.push(ctx, push.BoardReply, push.BoardEvent(push.BoardReply, title, prefixChars(comment.Body, 140), post.ID, board.Slug), recipient)
		return nil
	}()
	if err != nil {
		n.Logger.Warn(fmt.Sprintf("boards.notify reply failed error=%T", err))
	}
}

// upvoted records the first upvote from each voter; toggling off and on
// again does not notify twice.
func (n *Notifier) upvoted(ctx context.Context, post *boardsdb.BoardPost, board *boardsdb.Board, actor *community.Viewer) {
	if post.AuthorID == actor.UserID {
		return
	}
	err := func() error {
		blocked, err := n.S.blockedEitherWay(ctx, post.AuthorID, actor.UserID)
		if err != nil || blocked {
			return err
		}
		now := n.S.now()
		actorID := actor.UserID
		inserted, err := n.S.Q.BoardNotificationInsertUpvote(ctx, boardsdb.BoardNotificationInsertUpvoteParams{
			ID: uuid.New(), RecipientID: post.AuthorID, ActorID: &actorID, PostID: post.ID, CreatedAt: now,
		})
		if err != nil || len(inserted) == 0 {
			return err
		}
		settings, err := n.settings(ctx, post.AuthorID)
		if err != nil || !settings.UpvotePush {
			return err
		}
		recent, err := n.S.Q.BoardRecentUpvoteNotifications(ctx, boardsdb.BoardRecentUpvoteNotificationsParams{
			RecipientID: post.AuthorID, PostID: post.ID, CreatedAt: now.Add(-upvotePushWindow),
		})
		// The row just written is one of them.
		if err != nil || recent > 1 {
			return err
		}
		n.push(ctx, push.BoardUpvote, push.BoardEvent(push.BoardUpvote, who(actor)+" upvoted your post", prefixChars(post.Title, 140), post.ID, board.Slug), post.AuthorID)
		return nil
	}()
	if err != nil {
		n.Logger.Warn(fmt.Sprintf("boards.notify upvote failed error=%T", err))
	}
}

func (n *Notifier) push(ctx context.Context, kind string, msg push.Notification, recipient uuid.UUID) {
	if n.Push == nil {
		return
	}
	if _, err := n.Push.SendToUser(ctx, recipient, msg); err != nil {
		n.Logger.Warn(fmt.Sprintf("boards.push failed kind=%s error=%T", kind, err))
	}
}

// page is BoardNotifier.page: newest first. Notifications whose post is
// gone, or whose actor is blocked either way, are left out.
func (n *Notifier) page(ctx context.Context, viewer uuid.UUID, raw *string) (*BoardNotificationPage, error) {
	s := n.S
	hidden, err := s.hidden(ctx, s.Q, viewer)
	if err != nil {
		return nil, err
	}
	params := boardsdb.BoardNotificationsPageParams{Viewer: viewer, Hidden: keys(hidden), Lim: pageSize + 1}
	if c, ok := parseCursor(raw); ok {
		params.HasCursor, params.CursorAt, params.CursorID = true, c.bound(), c.id
	}
	rows, err := s.Q.BoardNotificationsPage(ctx, params)
	if err != nil {
		return nil, err
	}
	pageRows := rows[:min(len(rows), pageSize)]

	postIDs := make([]uuid.UUID, 0, len(pageRows))
	var commentIDs, actorIDs []uuid.UUID
	for i := range pageRows {
		postIDs = append(postIDs, pageRows[i].PostID)
		if pageRows[i].CommentID != nil {
			commentIDs = append(commentIDs, *pageRows[i].CommentID)
		}
		if pageRows[i].ActorID != nil {
			actorIDs = append(actorIDs, *pageRows[i].ActorID)
		}
	}
	posts, err := s.Q.BoardPostsByIDs(ctx, uniq(postIDs))
	if err != nil {
		return nil, err
	}
	postByID := map[uuid.UUID]boardsdb.BoardPost{}
	boardIDs := make([]uuid.UUID, 0, len(posts))
	for i := range posts {
		postByID[posts[i].ID] = posts[i]
		boardIDs = append(boardIDs, posts[i].BoardID)
	}
	boards, err := s.Q.BoardsByIDs(ctx, uniq(boardIDs))
	if err != nil {
		return nil, err
	}
	slugByID := map[uuid.UUID]string{}
	for _, b := range boards {
		slugByID[b.ID] = b.Slug
	}
	commentByID := map[uuid.UUID]boardsdb.BoardComment{}
	if len(commentIDs) > 0 {
		comments, err := s.Q.BoardCommentsByIDs(ctx, uniq(commentIDs))
		if err != nil {
			return nil, err
		}
		for _, c := range comments {
			commentByID[c.ID] = c
		}
	}
	names, err := s.usernames(ctx, actorIDs)
	if err != nil {
		return nil, err
	}

	items := []BoardNotification{}
	for i := range pageRows {
		row := &pageRows[i]
		// Deleted posts and boards drop out of the feed.
		post, ok := postByID[row.PostID]
		if !ok {
			continue
		}
		slug, ok := slugByID[post.BoardID]
		if !ok {
			continue
		}
		kind := row.Kind
		if kind != notifyReply && kind != notifyUpvote {
			kind = notifyOther
		}
		item := BoardNotification{
			ID:        apijson.NewUUID(row.ID),
			Kind:      kind,
			PostID:    apijson.NewUUID(row.PostID),
			BoardSlug: slug,
			PostTitle: post.Title,
			CreatedAt: apijson.NewTime(row.CreatedAt),
			IsRead:    row.ReadAt != nil,
		}
		if row.ActorID != nil {
			item.ActorUsername = nameOf(names, *row.ActorID)
		}
		if row.CommentID != nil {
			if c, ok := commentByID[*row.CommentID]; ok {
				item.CommentID = apijson.UUIDPtr(&c.ID)
				excerpt := prefixChars(c.Body, 140)
				item.Excerpt = &excerpt
			}
		}
		items = append(items, item)
	}
	out := &BoardNotificationPage{Items: items}
	if len(rows) > pageSize && len(pageRows) > 0 {
		last := pageRows[len(pageRows)-1]
		next := encodeCursor(last.CreatedAt, last.ID)
		out.NextCursor = &next
	}
	if out.UnreadCount, err = s.Q.BoardUnreadCount(ctx, boardsdb.BoardUnreadCountParams{Viewer: viewer, Hidden: keys(hidden)}); err != nil {
		return nil, err
	}
	return out, nil
}

// markRead is BoardNotifier.markRead: nil marks everything, an empty list
// nothing.
func (n *Notifier) markRead(ctx context.Context, ids []uuid.UUID, all bool, viewer uuid.UUID) error {
	now := n.S.now()
	if all {
		return n.S.Q.BoardMarkAllRead(ctx, boardsdb.BoardMarkAllReadParams{RecipientID: viewer, ReadAt: &now})
	}
	if len(ids) == 0 {
		return nil
	}
	return n.S.Q.BoardMarkRead(ctx, boardsdb.BoardMarkReadParams{Now: &now, Viewer: viewer, Ids: ids})
}
