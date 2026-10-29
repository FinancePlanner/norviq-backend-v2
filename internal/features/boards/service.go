package boards

import (
	"context"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/boardsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BoardsService.swift.
const (
	pageSize           = 30
	maxCommentsPerPost = 500
	dailySlug          = "daily"
)

// Service holds the boards queries. Handlers, the notifier and the daily
// publisher share it.
type Service struct {
	DB  *pgxpool.Pool
	Q   *boardsdb.Queries
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func errNotFound(reason string) error { return httpx.NotFound(reason) }

// usernames is BoardsService.usernames: users without a username are absent.
func (s *Service) usernames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Q.BoardUsernames(ctx, uniq(ids))
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

func uniq(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// userIDForUsername is BoardsService.userId(forUsername:): whitespace and
// any leading or trailing "@" are trimmed.
func (s *Service) userIDForUsername(ctx context.Context, raw string) (uuid.UUID, error) {
	username := strings.Trim(trim(raw), "@")
	if username == "" {
		return uuid.UUID{}, errNotFound("User not found")
	}
	ids, err := s.Q.BoardUserIDByUsername(ctx, &username)
	if err != nil {
		return uuid.UUID{}, err
	}
	if len(ids) == 0 {
		return uuid.UUID{}, errNotFound("User not found")
	}
	return ids[0], nil
}

// hidden is SocialService.blockedEitherWay: people the viewer blocked, who
// blocked the viewer, and anyone a moderator suspended from social.
func (s *Service) hidden(ctx context.Context, q *boardsdb.Queries, viewer uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := q.BoardBlocksInvolving(ctx, viewer)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]bool{}
	for _, r := range rows {
		if r.BlockerID == viewer {
			out[r.BlockedID] = true
		} else {
			out[r.BlockerID] = true
		}
	}
	suspended, err := q.BoardSuspendedUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range suspended {
		out[id] = true
	}
	delete(out, viewer)
	return out, nil
}

// blockedEitherWay is SocialService.isBlockedEitherWay(a, b).
func (s *Service) blockedEitherWay(ctx context.Context, a, b uuid.UUID) (bool, error) {
	h, err := s.hidden(ctx, s.Q, a)
	if err != nil {
		return false, err
	}
	return h[b], nil
}

func keys(m map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// boardBySlug is BoardsService.board(slug:).
func (s *Service) boardBySlug(ctx context.Context, slug string) (*boardsdb.Board, error) {
	rows, err := s.Q.BoardBySlug(ctx, strings.ToLower(slug))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errNotFound("Board not found")
	}
	return &rows[0], nil
}

// post is BoardsService.post(id:): a post whose board is gone is gone too.
func (s *Service) post(ctx context.Context, id uuid.UUID) (*boardsdb.BoardPost, *boardsdb.Board, error) {
	posts, err := s.Q.BoardPostByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if len(posts) == 0 {
		return nil, nil, errNotFound("Post not found")
	}
	boards, err := s.Q.BoardByID(ctx, posts[0].BoardID)
	if err != nil {
		return nil, nil, err
	}
	if len(boards) == 0 {
		return nil, nil, errNotFound("Post not found")
	}
	return &posts[0], &boards[0], nil
}

func boardDTO(b *boardsdb.Board, names map[uuid.UUID]string) BoardSummary {
	out := BoardSummary{
		ID:             apijson.NewUUID(b.ID),
		Slug:           b.Slug,
		Name:           b.Name,
		Description:    b.Description,
		PostCount:      b.PostCount,
		CreatedAt:      apijson.NewTime(b.CreatedAt),
		LastActivityAt: apijson.NewTime(b.LastActivityAt),
	}
	if b.CreatorID != nil {
		if n, ok := names[*b.CreatorID]; ok {
			out.CreatorUsername = &n
		}
	}
	return out
}

func (s *Service) boardDTOs(ctx context.Context, boards []boardsdb.Board) ([]BoardSummary, error) {
	var creators []uuid.UUID
	for i := range boards {
		if boards[i].CreatorID != nil {
			creators = append(creators, *boards[i].CreatorID)
		}
	}
	names, err := s.usernames(ctx, creators)
	if err != nil {
		return nil, err
	}
	out := make([]BoardSummary, 0, len(boards))
	for i := range boards {
		out = append(out, boardDTO(&boards[i], names))
	}
	return out, nil
}

func summary(p *boardsdb.BoardPost, boardSlug string, author *string, voted bool, newComments *int64) BoardPostSummary {
	kind := p.Kind
	if !validKind(kind) {
		kind = KindText
	}
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	out := BoardPostSummary{
		ID:               apijson.NewUUID(p.ID),
		BoardSlug:        boardSlug,
		Kind:             kind,
		Title:            p.Title,
		URL:              p.Url,
		Domain:           p.Domain,
		Tags:             tags,
		AuthorUsername:   author,
		CreatedAt:        apijson.NewTime(p.CreatedAt),
		Score:            p.Score,
		CommentCount:     p.CommentCount,
		ParticipantCount: p.ParticipantCount,
		ViewCount:        p.ViewCount,
		ViewerHasVoted:   voted,
		NewCommentCount:  newComments,
		AIGenerated:      p.AiGenerated,
	}
	if p.AiTopic != nil && validAITopic(*p.AiTopic) {
		t := *p.AiTopic
		out.AITopic = &t
	}
	return out
}

func nameOf(names map[uuid.UUID]string, id uuid.UUID) *string {
	if n, ok := names[id]; ok {
		return &n
	}
	return nil
}

// summaries is BoardsService.summaries: in the order given, with the
// viewer's vote and "N new" state.
func (s *Service) summaries(ctx context.Context, posts []boardsdb.BoardPost, slugs map[uuid.UUID]string, viewer uuid.UUID) ([]BoardPostSummary, error) {
	out := []BoardPostSummary{}
	if len(posts) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(posts))
	authors := make([]uuid.UUID, len(posts))
	for i := range posts {
		ids[i] = posts[i].ID
		authors[i] = posts[i].AuthorID
	}
	votedIDs, err := s.Q.BoardVotedPostIDs(ctx, boardsdb.BoardVotedPostIDsParams{Viewer: viewer, Ids: ids})
	if err != nil {
		return nil, err
	}
	voted := map[uuid.UUID]bool{}
	for _, id := range votedIDs {
		voted[id] = true
	}
	views, err := s.Q.BoardPostViews(ctx, boardsdb.BoardPostViewsParams{Viewer: viewer, Ids: ids})
	if err != nil {
		return nil, err
	}
	lastSeen := map[uuid.UUID]int64{}
	for _, v := range views {
		if _, ok := lastSeen[v.PostID]; !ok {
			lastSeen[v.PostID] = v.LastSeenCommentCount
		}
	}
	names, err := s.usernames(ctx, authors)
	if err != nil {
		return nil, err
	}
	for i := range posts {
		p := &posts[i]
		var fresh *int64
		if seen, ok := lastSeen[p.ID]; ok {
			n := max(0, p.CommentCount-seen)
			fresh = &n
		}
		out = append(out, summary(p, slugs[p.BoardID], nameOf(names, p.AuthorID), voted[p.ID], fresh))
	}
	return out, nil
}

// listBoards is BoardsService.listBoards. raw is the cursor query value as
// given (nil when absent).
func (s *Service) listBoards(ctx context.Context, raw *string, viewer uuid.UUID) (*BoardListResponse, error) {
	params := boardsdb.BoardsListParams{Limit: pageSize + 1}
	if c, ok := parseCursor(raw); ok {
		params.HasCursor, params.CursorAt, params.CursorID = true, c.bound(), c.id
	}
	rows, err := s.Q.BoardsList(ctx, params)
	if err != nil {
		return nil, err
	}
	page := rows[:min(len(rows), pageSize)]
	resp := &BoardListResponse{}
	if len(rows) > pageSize {
		last := page[len(page)-1]
		next := encodeCursor(last.LastActivityAt, last.ID)
		resp.NextCursor = &next
	}
	if raw == nil {
		if resp.DailyPost, err = s.currentDailyPost(ctx, viewer); err != nil {
			return nil, err
		}
	}
	if resp.Items, err = s.boardDTOs(ctx, page); err != nil {
		return nil, err
	}
	return resp, nil
}

// currentDailyPost is the newest published daily post, with this viewer's
// vote state.
func (s *Service) currentDailyPost(ctx context.Context, viewer uuid.UUID) (*BoardPostSummary, error) {
	boards, err := s.Q.BoardBySlug(ctx, dailySlug)
	if err != nil || len(boards) == 0 {
		return nil, err
	}
	posts, err := s.Q.BoardLatestDailyPost(ctx, boards[0].ID)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	items, err := s.summaries(ctx, posts, map[uuid.UUID]string{boards[0].ID: boards[0].Slug}, viewer)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}

// Sorts (BoardPostSort).
const (
	sortNew    = "new"
	sortTop    = "top"
	sortActive = "active"
)

// listPosts is BoardsService.listPosts: `new` and `active` page by keyset on
// their timestamp; `top` ranks by HN-style gravity, which shifts with time,
// so it pages by offset.
func (s *Service) listPosts(ctx context.Context, board *boardsdb.Board, sort string, kind, tag, raw *string, viewer uuid.UUID) (*BoardPostPage, error) {
	hidden, err := s.hidden(ctx, s.Q, viewer)
	if err != nil {
		return nil, err
	}
	// The newest daily post is pinned on the unfiltered first page, so it is
	// left out of the list itself.
	var pinned *BoardPostSummary
	if board.Slug == dailySlug && kind == nil && tag == nil {
		if pinned, err = s.currentDailyPost(ctx, viewer); err != nil {
			return nil, err
		}
	}

	sql := "SELECT id FROM board_posts WHERE board_id = $1 AND deleted_at IS NULL"
	args := []any{board.ID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if pinned != nil {
		sql += " AND id <> " + arg(pinned.ID.Std())
	}
	if kind != nil {
		sql += " AND kind = " + arg(*kind)
	}
	if tag != nil {
		sql += " AND " + arg(*tag) + " = ANY(tags)"
	}
	if len(hidden) > 0 {
		sql += " AND author_id <> ALL(" + arg(keys(hidden)) + "::uuid[])"
	}
	offset := 0
	switch sort {
	case sortTop:
		if raw != nil {
			offset = topOffset(*raw)
		}
		sql += " ORDER BY (score + 1) / power(extract(epoch from (now() - created_at)) / 3600.0 + 2, 1.5) DESC, created_at DESC, id DESC"
		sql += " LIMIT " + strconv.Itoa(pageSize+1) + " OFFSET " + arg(offset)
	default:
		column := "created_at"
		if sort == sortActive {
			column = "last_activity_at"
		}
		if c, ok := parseCursor(raw); ok {
			sql += " AND (" + column + ", id) < (" + arg(c.bound()) + ", " + arg(c.id) + ")"
		}
		sql += " ORDER BY " + column + " DESC, id DESC LIMIT " + strconv.Itoa(pageSize+1)
	}
	rows, err := s.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	ordered, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	pageIDs := ordered[:min(len(ordered), pageSize)]
	found, err := s.Q.BoardPostsByIDs(ctx, pageIDs)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]boardsdb.BoardPost{}
	for i := range found {
		byID[found[i].ID] = found[i]
	}
	posts := make([]boardsdb.BoardPost, 0, len(pageIDs))
	for _, id := range pageIDs {
		if p, ok := byID[id]; ok {
			posts = append(posts, p)
		}
	}

	resp := &BoardPostPage{}
	if len(ordered) > pageSize && len(posts) > 0 {
		last := posts[len(posts)-1]
		var next string
		switch sort {
		case sortNew:
			next = encodeCursor(last.CreatedAt, last.ID)
		case sortActive:
			next = encodeCursor(last.LastActivityAt, last.ID)
		default:
			next = "top-" + strconv.Itoa(offset+pageSize)
		}
		resp.NextCursor = &next
	}
	items, err := s.summaries(ctx, posts, map[uuid.UUID]string{board.ID: board.Slug}, viewer)
	if err != nil {
		return nil, err
	}
	if raw == nil && pinned != nil {
		items = append([]BoardPostSummary{*pinned}, items...)
	}
	resp.Items = items
	return resp, nil
}

// topOffset is BoardsService.topOffset; anything else is offset 0.
func topOffset(cursor string) int {
	rest, ok := strings.CutPrefix(cursor, "top-")
	if !ok {
		return 0
	}
	v, err := strconv.Atoi(rest)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// detail is BoardsService.detail: records the view, then returns the post
// with every comment. Comments by people blocked either way, and deleted
// ones, keep their slot with no body so the thread still hangs together.
func (s *Service) detail(ctx context.Context, postID, viewer uuid.UUID) (*BoardPostDetail, error) {
	post, board, err := s.post(ctx, postID)
	if err != nil {
		return nil, err
	}
	hidden, err := s.hidden(ctx, s.Q, viewer)
	if err != nil {
		return nil, err
	}
	if hidden[post.AuthorID] {
		return nil, errNotFound("Post not found")
	}
	previous, err := s.recordView(ctx, post, viewer)
	if err != nil {
		return nil, err
	}
	fresh := post
	if again, err := s.Q.BoardPostByID(ctx, postID); err != nil {
		return nil, err
	} else if len(again) > 0 {
		fresh = &again[0]
	}
	comments, err := s.Q.BoardCommentsForPost(ctx, boardsdb.BoardCommentsForPostParams{PostID: postID, Limit: maxCommentsPerPost})
	if err != nil {
		return nil, err
	}
	authors := make([]uuid.UUID, 0, len(comments)+1)
	for i := range comments {
		authors = append(authors, comments[i].AuthorID)
	}
	names, err := s.usernames(ctx, append(authors, post.AuthorID))
	if err != nil {
		return nil, err
	}
	dtos := make([]BoardComment, 0, len(comments))
	for i := range comments {
		cm := &comments[i]
		gone := cm.DeletedAt != nil || hidden[cm.AuthorID]
		d := BoardComment{
			ID:        apijson.NewUUID(cm.ID),
			ParentID:  apijson.UUIDPtr(cm.ParentID),
			Depth:     cm.Depth,
			Body:      cm.Body,
			CreatedAt: apijson.NewTime(cm.CreatedAt),
			IsDeleted: gone,
		}
		if gone {
			d.Body = ""
		} else {
			d.AuthorUsername = nameOf(names, cm.AuthorID)
		}
		dtos = append(dtos, d)
	}
	voted, err := s.Q.BoardHasVoted(ctx, boardsdb.BoardHasVotedParams{PostID: postID, UserID: viewer})
	if err != nil {
		return nil, err
	}
	var newComments *int64
	if previous != nil {
		n := max(0, fresh.CommentCount-*previous)
		newComments = &n
	}
	return &BoardPostDetail{
		Post:     summary(fresh, board.Slug, nameOf(names, post.AuthorID), voted, newComments),
		Body:     fresh.Body,
		Comments: dtos,
	}, nil
}

// recordView returns the comment count the viewer saw last time, or nil on
// a first visit (which is also the only time the view count moves).
func (s *Service) recordView(ctx context.Context, post *boardsdb.BoardPost, viewer uuid.UUID) (*int64, error) {
	existing, err := s.Q.BoardPostView(ctx, boardsdb.BoardPostViewParams{PostID: post.ID, UserID: viewer})
	if err != nil {
		return nil, err
	}
	now := s.now()
	if len(existing) > 0 {
		previous := existing[0].LastSeenCommentCount
		if err := s.Q.BoardPostViewUpdate(ctx, boardsdb.BoardPostViewUpdateParams{
			ID: existing[0].ID, LastSeenCommentCount: post.CommentCount, LastSeenAt: now,
		}); err != nil {
			return nil, err
		}
		return &previous, nil
	}
	inserted, err := s.Q.BoardPostViewInsert(ctx, boardsdb.BoardPostViewInsertParams{
		ID: uuid.New(), PostID: post.ID, UserID: viewer, LastSeenCommentCount: post.CommentCount, LastSeenAt: now,
	})
	if err != nil {
		return nil, err
	}
	if len(inserted) > 0 {
		if err := s.Q.BoardPostViewCountBump(ctx, post.ID); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// inTx runs fn in one transaction, as `req.db.transaction`, then (when the
// commit succeeded) then.
func (s *Service) inTx(ctx context.Context, fn func(q *boardsdb.Queries) error, then func() error) error {
	if err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error { return fn(s.Q.WithTx(tx)) }); err != nil {
		return err
	}
	if then != nil {
		return then()
	}
	return nil
}
