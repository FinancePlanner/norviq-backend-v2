package boards_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"norviq-api/internal/features/boards"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/boardsdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ports BoardsTests.swift and BoardNotificationsTests.swift. Admin routes
// belong to another slice, so sanctions, board deletion and the report queue
// are set up and read with SQL.

type recordedPush struct {
	to  uuid.UUID
	msg push.Notification
}

type fakePusher struct {
	mu   sync.Mutex
	sent []recordedPush
}

func (f *fakePusher) SendToUser(_ context.Context, id uuid.UUID, n push.Notification) (push.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, recordedPush{id, n})
	return push.Summary{}, nil
}

func (f *fakePusher) titles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, p := range f.sent {
		out = append(out, p.msg.Title)
	}
	return out
}

type fakeDiscord struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeDiscord) Send(_ context.Context, m string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
	return nil
}

type env struct {
	t      *testing.T
	app    *testutil.App
	pushes *fakePusher
	alerts *fakeDiscord
}

type user struct {
	id    uuid.UUID
	token string
	name  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	p, dc := &fakePusher{}, &fakeDiscord{}
	app := testutil.NewApp(t, []server.Feature{boards.Feature{Push: p, Discord: dc}})
	return &env{t: t, app: app, pushes: p, alerts: dc}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) register(name string) user {
	e.t.Helper()
	id := e.app.CreateUser("brd+" + name + "@example.com")
	username := "brd_" + name
	e.exec(`UPDATE users SET username = $2 WHERE id = $1`, id, username)
	return user{id: id, token: e.app.Token(id), name: username}
}

// member has accepted the guidelines and can post.
func (e *env) member(name string) user {
	e.t.Helper()
	u := e.register(name)
	if r := e.do(http.MethodPost, "/v1/community/guidelines/accept", u, nil); r.Status != http.StatusNoContent {
		e.t.Fatalf("accept: %d", r.Status)
	}
	return u
}

func (e *env) admin() user {
	u := e.register("admin")
	e.app.Deps.Admin.IDs[u.id] = struct{}{}
	return u
}

func (e *env) do(method, path string, as user, body any) *testutil.Response {
	e.t.Helper()
	return e.app.Do(method, path, as.token, body)
}

func code(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var env struct {
		Code string `json:"code"`
	}
	r.JSON(t, &env)
	return env.Code
}

func (e *env) createBoard(slug string, as user) *testutil.Response {
	return e.do(http.MethodPost, "/v1/boards", as, map[string]any{"slug": slug, "name": "Board " + slug, "description": "About " + slug})
}

func (e *env) createPost(slug string, as user, kind, title string, url *string, tags []string) boards.BoardPostSummary {
	e.t.Helper()
	if tags == nil {
		tags = []string{}
	}
	body := map[string]any{"kind": kind, "title": title, "tags": tags}
	if url != nil {
		body["url"] = *url
	}
	if kind != boards.KindLink {
		body["body"] = "Body"
	}
	r := e.do(http.MethodPost, "/v1/boards/"+slug+"/posts", as, body)
	if r.Status != http.StatusOK {
		e.t.Fatalf("create post: %d %s", r.Status, r.Body)
	}
	var out boards.BoardPostSummary
	r.JSON(e.t, &out)
	return out
}

func (e *env) comment(postID uuid.UUID, as user, parent *uuid.UUID, text string) *testutil.Response {
	body := map[string]any{"body": text}
	if parent != nil {
		body["parentId"] = parent.String()
	}
	return e.do(http.MethodPost, "/v1/board-posts/"+postID.String()+"/comments", as, body)
}

func (e *env) list(query string, as user) boards.BoardPostPage {
	e.t.Helper()
	r := e.do(http.MethodGet, "/v1/boards/"+query, as, nil)
	if r.Status != http.StatusOK {
		e.t.Fatalf("list %s: %d %s", query, r.Status, r.Body)
	}
	var out boards.BoardPostPage
	r.JSON(e.t, &out)
	return out
}

func ids(items []boards.BoardPostSummary) []uuid.UUID {
	out := []uuid.UUID{}
	for i := range items {
		out = append(out, items[i].ID.Std())
	}
	return out
}

func eqIDs(t *testing.T, got []uuid.UUID, want ...uuid.UUID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestBoardCreationGatesAndDailyCap(t *testing.T) {
	e := newEnv(t)
	fresh := e.register("fresh")
	r := e.createBoard("early", fresh)
	if r.Status != http.StatusForbidden || code(t, r) != "guidelines_required" {
		t.Fatalf("want guidelines_required, got %d %s", r.Status, r.Body)
	}
	alice := e.member("alice")
	for _, s := range []string{"one", "two", "three"} {
		if r := e.createBoard("b-"+s, alice); r.Status != http.StatusOK {
			t.Fatalf("create: %d %s", r.Status, r.Body)
		}
	}
	r = e.createBoard("b-four", alice)
	if r.Status != http.StatusTooManyRequests || code(t, r) != "board_limit_reached" {
		t.Fatalf("want board_limit_reached, got %d %s", r.Status, r.Body)
	}
	bob := e.member("bob")
	if r := e.createBoard("b-one", bob); r.Status != http.StatusConflict {
		t.Fatalf("taken slug: %d", r.Status)
	}
	if r := e.createBoard("admin", bob); r.Status != http.StatusBadRequest {
		t.Fatalf("reserved slug: %d", r.Status)
	}
	var list boards.BoardListResponse
	e.do(http.MethodGet, "/v1/boards", bob, nil).JSON(t, &list)
	if len(list.Items) != 3 || list.DailyPost != nil {
		t.Fatalf("list: %+v", list)
	}
	for _, b := range list.Items {
		if b.CreatorUsername == nil || *b.CreatorUsername != "brd_alice" {
			t.Fatalf("creator: %+v", b)
		}
	}
	// Admins skip the daily cap.
	admin := e.admin()
	e.exec(`INSERT INTO community_guidelines_acceptances (id, user_id, accepted_at) VALUES ($1, $2, now())`, uuid.New(), admin.id)
	for _, s := range []string{"a1", "a2", "a3", "a4"} {
		if r := e.createBoard("adm-"+s, admin); r.Status != http.StatusOK {
			t.Fatalf("admin create: %d", r.Status)
		}
	}
}

func TestPostsAndVotes(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.member("alice"), e.member("bob")
	e.createBoard("dca", alice)
	link := e.createPost("dca", alice, boards.KindLink, "DCA guide", ptr("https://www.example.com/dca"), []string{"#DCA"})
	if *link.Domain != "example.com" || len(link.Tags) != 1 || link.Tags[0] != "dca" || link.ParticipantCount != 1 {
		t.Fatalf("link: %+v", link)
	}
	r := e.do(http.MethodPost, "/v1/boards/dca/posts", alice, map[string]any{"kind": "text", "title": "Text post", "url": "https://example.com", "tags": []string{}})
	if r.Status != http.StatusBadRequest {
		t.Fatalf("text with url: %d", r.Status)
	}
	var vote boards.BoardVoteResponse
	e.do(http.MethodPost, "/v1/board-posts/"+link.ID.String()+"/vote", bob, nil).JSON(t, &vote)
	if vote != (boards.BoardVoteResponse{Score: 1, Voted: true}) {
		t.Fatalf("vote: %+v", vote)
	}
	if page := e.list("dca/posts", bob); !page.Items[0].ViewerHasVoted {
		t.Fatal("viewerHasVoted")
	}
	e.do(http.MethodPost, "/v1/board-posts/"+link.ID.String()+"/vote", bob, nil).JSON(t, &vote)
	if vote != (boards.BoardVoteResponse{Score: 0, Voted: false}) {
		t.Fatalf("unvote: %+v", vote)
	}
	var b boards.BoardSummary
	e.do(http.MethodGet, "/v1/boards/DCA", bob, nil).JSON(t, &b)
	if b.PostCount != 1 {
		t.Fatalf("post count: %+v", b)
	}
	// Creator-only edit.
	if r := e.do(http.MethodPatch, "/v1/boards/dca", bob, map[string]any{"description": "x"}); r.Status != http.StatusForbidden {
		t.Fatalf("edit by other: %d", r.Status)
	}
	e.do(http.MethodPatch, "/v1/boards/dca", alice, map[string]any{"description": "  New words "}).JSON(t, &b)
	if b.Description != "New words" {
		t.Fatalf("edit: %+v", b)
	}
}

func TestSortingAndFilters(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.member("alice"), e.member("bob")
	e.createBoard("etfs", alice)
	older := e.createPost("etfs", alice, boards.KindText, "Older post", nil, []string{"vanguard"})
	ask := e.createPost("etfs", alice, boards.KindAsk, "Ask: which ETF?", nil, nil)
	newest := e.createPost("etfs", alice, boards.KindShow, "Show: my allocation", nil, nil)

	eqIDs(t, ids(e.list("etfs/posts?sort=new", bob).Items), newest.ID.Std(), ask.ID.Std(), older.ID.Std())
	e.do(http.MethodPost, "/v1/board-posts/"+older.ID.String()+"/vote", bob, nil)
	if got := e.list("etfs/posts?sort=top", bob).Items[0].ID; got != older.ID {
		t.Fatalf("top: %v", got)
	}
	if r := e.comment(ask.ID.Std(), bob, nil, "A reply"); r.Status != http.StatusOK {
		t.Fatalf("comment: %d %s", r.Status, r.Body)
	}
	if got := e.list("etfs/posts?sort=active", bob).Items[0].ID; got != ask.ID {
		t.Fatalf("active: %v", got)
	}
	eqIDs(t, ids(e.list("etfs/posts?kind=ask", bob).Items), ask.ID.Std())
	eqIDs(t, ids(e.list("etfs/posts?kind=show", bob).Items), newest.ID.Std())
	eqIDs(t, ids(e.list("etfs/posts?tag=Vanguard", bob).Items), older.ID.Std())
	if r := e.do(http.MethodGet, "/v1/boards/etfs/posts?tag=bad%20tag", bob, nil); r.Status != http.StatusBadRequest {
		t.Fatalf("bad tag: %d", r.Status)
	}
}

func TestPagination(t *testing.T) {
	e := newEnv(t)
	alice := e.member("alice")
	e.createBoard("many", alice)
	boardID := uuid.Nil
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT id FROM boards WHERE slug='many'`).Scan(&boardID); err != nil {
		t.Fatal(err)
	}
	for i := range 35 {
		e.exec(`INSERT INTO board_posts (id, board_id, author_id, kind, title, tags, score, comment_count, participant_count, view_count, last_activity_at, created_at)
			VALUES ($1, $2, $3, 'text', 'p', '{}', 0, 0, 1, 0, now() - make_interval(mins => $4), now() - make_interval(mins => $4))`, uuid.New(), boardID, alice.id, i)
	}
	for _, sort := range []string{"new", "active", "top"} {
		first := e.list("many/posts?sort="+sort, alice)
		if len(first.Items) != 30 || first.NextCursor == nil {
			t.Fatalf("%s page 1: %d %v", sort, len(first.Items), first.NextCursor)
		}
		second := e.list("many/posts?sort="+sort+"&cursor="+*first.NextCursor, alice)
		if len(second.Items) != 5 || second.NextCursor != nil {
			t.Fatalf("%s page 2: %d", sort, len(second.Items))
		}
		seen := map[uuid.UUID]bool{}
		for _, id := range append(ids(first.Items), ids(second.Items)...) {
			if seen[id] {
				t.Fatalf("%s: duplicate %v", sort, id)
			}
			seen[id] = true
		}
	}
	if c := e.list("many/posts?sort=top", alice).NextCursor; *c != "top-30" {
		t.Fatalf("top cursor: %s", *c)
	}
}

func TestCommentsThreadsAndNewCounts(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.member("alice"), e.member("bob")
	e.createBoard("threads", alice)
	post := e.createPost("threads", alice, boards.KindText, "A post title", nil, nil)
	pid := post.ID.Std()
	if r := e.do(http.MethodGet, "/v1/board-posts/"+post.ID.String(), alice, nil); r.Status != http.StatusOK {
		t.Fatalf("open: %d", r.Status)
	}
	var parent *uuid.UUID
	for depth := 0; depth <= 8; depth++ {
		as := alice
		if depth%2 == 0 {
			as = bob
		}
		r := e.comment(pid, as, parent, "A reply")
		if r.Status != http.StatusOK {
			t.Fatalf("depth %d: %d %s", depth, r.Status, r.Body)
		}
		var c boards.BoardComment
		r.JSON(t, &c)
		if c.Depth != int64(depth) {
			t.Fatalf("depth %d != %d", c.Depth, depth)
		}
		id := c.ID.Std()
		parent = &id
	}
	if r := e.comment(pid, bob, parent, "A reply"); r.Status != http.StatusBadRequest {
		t.Fatalf("too deep: %d", r.Status)
	}
	if r := e.comment(pid, bob, ptr(uuid.New()), "A reply"); r.Status != http.StatusBadRequest {
		t.Fatalf("missing parent: %d", r.Status)
	}
	var d boards.BoardPostDetail
	e.do(http.MethodGet, "/v1/board-posts/"+post.ID.String(), alice, nil).JSON(t, &d)
	if len(d.Comments) != 9 || d.Post.CommentCount != 9 || d.Post.ParticipantCount != 2 || *d.Post.NewCommentCount != 5 || d.Post.ViewCount != 1 {
		t.Fatalf("detail: %+v", d.Post)
	}
	e.do(http.MethodGet, "/v1/board-posts/"+post.ID.String(), alice, nil).JSON(t, &d)
	if *d.Post.NewCommentCount != 0 {
		t.Fatalf("again: %d", *d.Post.NewCommentCount)
	}
	first := d.Comments[0].ID.String()
	if r := e.do(http.MethodDelete, "/v1/board-comments/"+first, alice, nil); r.Status != http.StatusForbidden {
		t.Fatalf("delete other's: %d", r.Status)
	}
	if r := e.do(http.MethodDelete, "/v1/board-comments/"+first, bob, nil); r.Status != http.StatusNoContent {
		t.Fatalf("delete own: %d", r.Status)
	}
	if r := e.do(http.MethodDelete, "/v1/board-comments/"+first, bob, nil); r.Status != http.StatusNotFound {
		t.Fatalf("delete again: %d", r.Status)
	}
	var after boards.BoardPostDetail
	e.do(http.MethodGet, "/v1/board-posts/"+post.ID.String(), alice, nil).JSON(t, &after)
	gone := after.Comments[0]
	if !gone.IsDeleted || gone.Body != "" || gone.AuthorUsername != nil {
		t.Fatalf("deleted slot: %+v", gone)
	}
	if r := e.do(http.MethodGet, "/v1/board-posts/nope", alice, nil); r.Status != http.StatusBadRequest {
		t.Fatalf("bad id: %d", r.Status)
	}
}

func (e *env) sanction(u user, kind string, expired bool) {
	exp := "now() + interval '24 hours'"
	if expired {
		exp = "now() - interval '1 minute'"
	}
	if kind == "ban" && !expired {
		exp = "NULL"
	}
	e.exec(`INSERT INTO community_sanctions (id, user_id, kind, reason, expires_at, created_at) VALUES ($1, $2, $3, 'Testing', `+exp+`, now())`,
		uuid.New(), u.id, kind)
}

func TestMuteBlocksWritingNotReading(t *testing.T) {
	e := newEnv(t)
	alice, troll := e.member("alice"), e.member("troll")
	e.createBoard("general", alice)
	post := e.createPost("general", alice, boards.KindText, "A post title", nil, nil)
	e.sanction(troll, "mute", false)

	r := e.do(http.MethodPost, "/v1/boards/general/posts", troll, map[string]any{"kind": "text", "title": "Spam spam", "tags": []string{}})
	if r.Status != http.StatusForbidden || code(t, r) != "community_muted" {
		t.Fatalf("post: %d %s", r.Status, r.Body)
	}
	if code(t, e.comment(post.ID.Std(), troll, nil, "x")) != "community_muted" {
		t.Fatal("comment not muted")
	}
	if code(t, e.do(http.MethodPost, "/v1/board-posts/"+post.ID.String()+"/vote", troll, nil)) != "community_muted" {
		t.Fatal("vote not muted")
	}
	e.list("general/posts", troll)
	if r := e.do(http.MethodPost, "/v1/board-reports", troll, map[string]any{"postId": post.ID.String(), "reason": "spam"}); r.Status != http.StatusAccepted {
		t.Fatalf("report: %d %s", r.Status, r.Body)
	}
	var st boards.CommunityViewerStatus
	e.do(http.MethodGet, "/v1/community/me", troll, nil).JSON(t, &st)
	if st.ActiveSanction == nil || st.ActiveSanction.Kind != "mute" || st.ActiveSanction.ExpiresAt == nil {
		t.Fatalf("status: %+v", st)
	}
	e.exec(`UPDATE community_sanctions SET expires_at = now() - interval '1 minute' WHERE user_id = $1`, troll.id)
	if r := e.comment(post.ID.Std(), troll, nil, "x"); r.Status != http.StatusOK {
		t.Fatalf("after expiry: %d", r.Status)
	}
}

func TestBanAndSuspension(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.member("alice"), e.member("bob")
	e.sanction(alice, "ban", false)
	r := e.do(http.MethodGet, "/v1/boards", alice, nil)
	if r.Status != http.StatusForbidden || code(t, r) != "community_banned" {
		t.Fatalf("banned: %d %s", r.Status, r.Body)
	}
	var st boards.CommunityViewerStatus
	e.do(http.MethodGet, "/v1/community/me", alice, nil).JSON(t, &st)
	if st.ActiveSanction == nil || st.ActiveSanction.Kind != "ban" || st.ActiveSanction.ExpiresAt != nil {
		t.Fatalf("status: %+v", st)
	}
	e.exec(`UPDATE community_sanctions SET revoked_at = now() WHERE user_id = $1`, alice.id)
	if r := e.do(http.MethodGet, "/v1/boards", alice, nil); r.Status != http.StatusOK {
		t.Fatalf("lifted: %d", r.Status)
	}

	e.exec(`INSERT INTO social_settings (id, user_id, search_visibility, discoverable_by_contacts, discoverable_by_x, show_return_percent,
		show_streaks, show_xp, leaderboard_opt_in, created_at, suspended_at) VALUES ($1, $2, 'everyone', true, true, true, true, true, true, now(), now())`,
		uuid.New(), bob.id)
	if r := e.do(http.MethodGet, "/v1/boards", bob, nil); code(t, r) != "community_banned" {
		t.Fatalf("suspended: %s", r.Body)
	}
	e.do(http.MethodGet, "/v1/community/me", bob, nil).JSON(t, &st)
	if st.ActiveSanction == nil || st.ActiveSanction.Kind != "ban" || st.ActiveSanction.Reason != "Suspended by a moderator." ||
		st.ActiveSanction.ID.Std() != bob.id || st.ActiveSanction.ExpiresAt != nil || !st.HasUsername {
		t.Fatalf("suspension status: %+v", st)
	}
	e.exec(`UPDATE social_settings SET suspended_at = NULL WHERE user_id = $1`, bob.id)
	if r := e.do(http.MethodGet, "/v1/boards", bob, nil); r.Status != http.StatusOK {
		t.Fatalf("unsuspended: %d", r.Status)
	}
}

func TestAdminDeletesAndDeletedBoards(t *testing.T) {
	e := newEnv(t)
	admin, alice := e.admin(), e.member("alice")
	e.createBoard("doomed", alice)
	e.createBoard("kept", alice)
	doomed := e.createPost("doomed", alice, boards.KindText, "A post title", nil, nil)
	kept := e.createPost("kept", alice, boards.KindText, "A post title", nil, nil)
	bob := e.member("bob")
	if r := e.do(http.MethodDelete, "/v1/board-posts/"+kept.ID.String(), bob, nil); r.Status != http.StatusForbidden {
		t.Fatalf("delete other's: %d", r.Status)
	}
	if r := e.do(http.MethodDelete, "/v1/board-posts/"+kept.ID.String(), admin, nil); r.Status != http.StatusNoContent {
		t.Fatalf("admin delete: %d", r.Status)
	}
	if len(e.list("kept/posts", alice).Items) != 0 {
		t.Fatal("deleted post listed")
	}
	var b boards.BoardSummary
	e.do(http.MethodGet, "/v1/boards/kept", alice, nil).JSON(t, &b)
	if b.PostCount != 0 {
		t.Fatalf("count: %d", b.PostCount)
	}
	// CommunityAdminController's soft delete.
	e.exec(`UPDATE boards SET deleted_at = now() WHERE slug = 'doomed'`)
	if r := e.do(http.MethodGet, "/v1/boards/doomed", alice, nil); r.Status != http.StatusNotFound {
		t.Fatalf("deleted board: %d", r.Status)
	}
	if r := e.do(http.MethodGet, "/v1/board-posts/"+doomed.ID.String(), alice, nil); r.Status != http.StatusNotFound {
		t.Fatalf("post on deleted board: %d", r.Status)
	}
	if r := e.createBoard("doomed", alice); r.Status != http.StatusConflict {
		t.Fatalf("slug stays taken: %d", r.Status)
	}
	var list boards.BoardListResponse
	e.do(http.MethodGet, "/v1/boards", alice, nil).JSON(t, &list)
	if len(list.Items) != 1 || list.Items[0].Slug != "kept" {
		t.Fatalf("list: %+v", list.Items)
	}
}

func TestBlocksAndReports(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.member("alice"), e.member("bob")
	e.createBoard("general", alice)
	bobPost := e.createPost("general", bob, boards.KindText, "Bob's take", nil, nil)
	e.createPost("general", alice, boards.KindText, "Alice's take", nil, nil)
	// Friends and requests between them go away.
	e.exec(`INSERT INTO social_friendships (id, user_id, friend_id, created_at) VALUES ($1, $2, $3, now()), ($4, $3, $2, now())`, uuid.New(), alice.id, bob.id, uuid.New())

	if r := e.do(http.MethodPost, "/v1/community/blocks", alice, map[string]any{"username": "@brd_bob"}); r.Status != http.StatusNoContent {
		t.Fatalf("block: %d %s", r.Status, r.Body)
	}
	if r := e.do(http.MethodPost, "/v1/community/blocks", alice, map[string]any{"username": "brd_bob"}); r.Status != http.StatusNoContent {
		t.Fatalf("block again: %d", r.Status)
	}
	var n int
	_ = e.app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM social_friendships`).Scan(&n)
	if n != 0 {
		t.Fatalf("friendships left: %d", n)
	}
	titles := func(p boards.BoardPostPage) string {
		var out []string
		for i := range p.Items {
			out = append(out, p.Items[i].Title)
		}
		return strings.Join(out, "|")
	}
	if got := titles(e.list("general/posts", alice)); got != "Alice's take" {
		t.Fatalf("alice sees %q", got)
	}
	if got := titles(e.list("general/posts", bob)); got != "Bob's take" {
		t.Fatalf("bob sees %q", got)
	}
	if r := e.do(http.MethodGet, "/v1/board-posts/"+bobPost.ID.String(), alice, nil); r.Status != http.StatusNotFound {
		t.Fatalf("blocked detail: %d", r.Status)
	}
	if r := e.do(http.MethodPost, "/v1/community/blocks", alice, map[string]any{"username": "brd_alice"}); r.Status != http.StatusBadRequest {
		t.Fatalf("self block: %d", r.Status)
	}
	if r := e.do(http.MethodPost, "/v1/community/blocks", alice, map[string]any{"username": "nobody"}); r.Status != http.StatusNotFound {
		t.Fatalf("unknown: %d", r.Status)
	}
	if r := e.do(http.MethodDelete, "/v1/community/blocks/brd_bob", alice, nil); r.Status != http.StatusNoContent {
		t.Fatalf("unblock: %d", r.Status)
	}
	if len(e.list("general/posts", alice).Items) != 2 {
		t.Fatal("unblock did not restore")
	}

	if r := e.do(http.MethodPost, "/v1/board-reports", alice, map[string]any{"postId": bobPost.ID.String(), "reason": "harassment", "note": " Not nice "}); r.Status != http.StatusAccepted || len(r.Body) != 0 {
		t.Fatalf("report: %d %q", r.Status, r.Body)
	}
	if r := e.do(http.MethodPost, "/v1/board-reports", alice, map[string]any{"postId": bobPost.ID.String(), "commentId": bobPost.ID.String(), "reason": "spam"}); r.Status != http.StatusBadRequest {
		t.Fatalf("both: %d", r.Status)
	}
	if r := e.do(http.MethodPost, "/v1/board-reports", alice, map[string]any{"commentId": uuid.NewString(), "reason": "spam"}); r.Status != http.StatusNotFound {
		t.Fatalf("missing comment: %d", r.Status)
	}
	var target, note, reason string
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT target_type, target_id, note, reason FROM social_reports`).Scan(&target, new(string), &note, &reason); err != nil {
		t.Fatal(err)
	}
	if target != "board_post" || note != "Not nice" || reason != "harassment" {
		t.Fatalf("report row: %s %s %s", target, note, reason)
	}
}

func TestDecodeErrorsMatchVapor(t *testing.T) {
	e := newEnv(t)
	alice := e.member("alice")
	e.createBoard("dec", alice)
	cases := []struct {
		path, body, want string
	}{
		{"/v1/boards", `{"slug":"x"}`, `No such key 'name' at path ''. No value associated with key CodingKeys(stringValue: \"name\", intValue: nil) (\"name\").`},
		{"/v1/boards", `{"slug":null,"name":"x","description":"y"}`, `No value found (expected type 'String') at path 'slug'. Cannot get value of type String -- found null value instead.`},
		{"/v1/boards", `{"slug":[1],"name":"x","description":"y"}`, `Value was not of type 'String' at path 'slug'. Expected to decode String but found an array instead.`},
		{"/v1/boards", `[]`, `Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found an array instead.`},
		{"/v1/boards/dec/posts", `{"kind":"bogus","title":"abc","tags":[]}`, `Data corrupted at path 'kind'. Cannot initialize BoardPostKind from invalid String value bogus.`},
		{"/v1/boards/dec/posts", `{"kind":"text","title":"abc","tags":[1]}`, `Value was not of type 'String' at path 'tags.Index 0'. Expected to decode String but found number instead.`},
		{"/v1/boards/dec/posts", `{"kind":"text","title":"abc","tags":null}`, `No value found (expected type 'Array<Any>') at path 'tags'. Cannot get unkeyed decoding container -- found null value instead.`},
		{"/v1/board-reports", `{"post_id":"zz","reason":"spam"}`, `Data corrupted at path 'postId'. Attempted to decode UUID from invalid UUID string.`},
		{"/v1/community/notifications/read", `{"ids":["zz"]}`, `Data corrupted at path 'ids.Index 0'. Attempted to decode UUID from invalid UUID string.`},
	}
	for _, tc := range cases {
		r := e.do(http.MethodPost, tc.path, alice, []byte(tc.body))
		var env struct {
			Reason string `json:"reason"`
		}
		r.JSON(t, &env)
		want := strings.ReplaceAll(tc.want, `\"`, `"`)
		if r.Status != http.StatusBadRequest || env.Reason != want {
			t.Errorf("%s %s: %d %q", tc.path, tc.body, r.Status, env.Reason)
		}
	}
	if r := e.do(http.MethodPost, "/v1/community/notifications/read", alice, []byte("")); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("empty body: %d", r.Status)
	}
}

func TestNotificationsRepliesAndPushes(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("ana"), e.member("bo")
	e.createBoard("notify", ana)
	post := e.createPost("notify", ana, boards.KindText, "Ana's post", nil, nil)
	var first boards.BoardComment
	e.comment(post.ID.Std(), bo, nil, "Great point").JSON(t, &first)
	fid := first.ID.Std()
	e.comment(post.ID.Std(), ana, &fid, "Thanks!")
	e.comment(post.ID.Std(), ana, nil, "Talking to myself")

	var feed boards.BoardNotificationPage
	e.do(http.MethodGet, "/v1/community/notifications", ana, nil).JSON(t, &feed)
	if len(feed.Items) != 1 || feed.Items[0].Kind != "reply" || *feed.Items[0].ActorUsername != "brd_bo" ||
		*feed.Items[0].Excerpt != "Great point" || feed.Items[0].BoardSlug != "notify" || feed.UnreadCount != 1 {
		t.Fatalf("ana feed: %+v", feed)
	}
	e.do(http.MethodGet, "/v1/community/notifications", bo, nil).JSON(t, &feed)
	if len(feed.Items) != 1 || *feed.Items[0].Excerpt != "Thanks!" {
		t.Fatalf("bo feed: %+v", feed)
	}
	if got := strings.Join(e.pushes.titles(), "|"); got != "@brd_bo commented on your post|@brd_ana replied to you" {
		t.Fatalf("pushes: %q", got)
	}
}

func TestUpvoteNotificationsAndSettings(t *testing.T) {
	e := newEnv(t)
	ana, bo, cy := e.member("ana"), e.member("bo"), e.member("cy")
	e.createBoard("notify", ana)
	post := e.createPost("notify", ana, boards.KindText, "Ana's post", nil, nil)
	vote := "/v1/board-posts/" + post.ID.String() + "/vote"
	for range 3 {
		e.do(http.MethodPost, vote, bo, nil)
	}
	e.do(http.MethodPost, vote, cy, nil)
	e.do(http.MethodPost, vote, ana, nil)
	var feed boards.BoardNotificationPage
	e.do(http.MethodGet, "/v1/community/notifications", ana, nil).JSON(t, &feed)
	if len(feed.Items) != 2 {
		t.Fatalf("upvotes: %+v", feed.Items)
	}
	if got := e.pushes.titles(); len(got) != 1 || got[0] != "@brd_bo upvoted your post" {
		t.Fatalf("pushes: %v", got)
	}

	var s boards.BoardNotificationSettings
	e.do(http.MethodGet, "/v1/community/notification-settings", bo, nil).JSON(t, &s)
	if s != (boards.BoardNotificationSettings{ReplyPush: true, UpvotePush: true}) {
		t.Fatalf("default: %+v", s)
	}
	e.do(http.MethodPut, "/v1/community/notification-settings", bo, map[string]any{"replyPush": false, "upvotePush": false}).JSON(t, &s)
	if s != (boards.BoardNotificationSettings{}) {
		t.Fatalf("updated: %+v", s)
	}
	e.do(http.MethodPut, "/v1/community/notification-settings", bo, map[string]any{"replyPush": false, "upvotePush": false})
	bp := e.createPost("notify", bo, boards.KindText, "Bo's post", nil, nil)
	e.comment(bp.ID.Std(), ana, nil, "hi")
	e.do(http.MethodPost, "/v1/board-posts/"+bp.ID.String()+"/vote", ana, nil)
	if len(e.pushes.titles()) != 1 {
		t.Fatalf("silenced pushes sent: %v", e.pushes.titles())
	}
	e.do(http.MethodGet, "/v1/community/notifications", bo, nil).JSON(t, &feed)
	if len(feed.Items) != 2 {
		t.Fatalf("feed still records: %d", len(feed.Items))
	}
}

func TestMarkReadAndHiding(t *testing.T) {
	e := newEnv(t)
	ana, bo, cy := e.member("ana"), e.member("bo"), e.member("cy")
	e.createBoard("notify", ana)
	post := e.createPost("notify", ana, boards.KindText, "Ana's post", nil, nil)
	e.comment(post.ID.Std(), bo, nil, "Nice post")
	e.comment(post.ID.Std(), cy, nil, "Nice post")
	var page boards.BoardNotificationPage
	e.do(http.MethodGet, "/v1/community/notifications", ana, nil).JSON(t, &page)
	if page.UnreadCount != 2 {
		t.Fatalf("unread: %d", page.UnreadCount)
	}
	if r := e.do(http.MethodPost, "/v1/community/notifications/read", ana, map[string]any{"ids": []string{page.Items[0].ID.String()}}); r.Status != http.StatusNoContent {
		t.Fatalf("mark: %d", r.Status)
	}
	var unread boards.BoardUnreadCount
	e.do(http.MethodGet, "/v1/community/notifications/unread-count", ana, nil).JSON(t, &unread)
	if unread.UnreadCount != 1 {
		t.Fatalf("unread after one: %d", unread.UnreadCount)
	}
	e.do(http.MethodPost, "/v1/community/notifications/read", ana, map[string]any{"ids": []string{}})
	e.do(http.MethodGet, "/v1/community/notifications/unread-count", ana, nil).JSON(t, &unread)
	if unread.UnreadCount != 1 {
		t.Fatalf("empty list marks nothing: %d", unread.UnreadCount)
	}
	e.do(http.MethodPost, "/v1/community/notifications/read", ana, map[string]any{})
	e.do(http.MethodGet, "/v1/community/notifications", ana, nil).JSON(t, &page)
	if page.UnreadCount != 0 || !page.Items[0].IsRead || !page.Items[1].IsRead {
		t.Fatalf("all read: %+v", page)
	}
	many := make([]string, 201)
	for i := range many {
		many[i] = uuid.NewString()
	}
	if r := e.do(http.MethodPost, "/v1/community/notifications/read", ana, map[string]any{"ids": many}); r.Status != http.StatusBadRequest {
		t.Fatalf("201 ids: %d", r.Status)
	}
	e.do(http.MethodPost, "/v1/community/blocks", ana, map[string]any{"username": "brd_cy"})
	e.do(http.MethodGet, "/v1/community/notifications", ana, nil).JSON(t, &page)
	if len(page.Items) != 1 || *page.Items[0].ActorUsername != "brd_bo" {
		t.Fatalf("blocked hidden: %+v", page.Items)
	}
	e.do(http.MethodDelete, "/v1/board-posts/"+post.ID.String(), ana, nil)
	e.do(http.MethodGet, "/v1/community/notifications", ana, nil).JSON(t, &page)
	if len(page.Items) != 0 {
		t.Fatalf("deleted post still in feed: %+v", page.Items)
	}
}

func TestBlockedCannotNotify(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("ana"), e.member("bo")
	e.createBoard("notify", ana)
	post := e.createPost("notify", ana, boards.KindText, "Ana's post", nil, nil)
	e.do(http.MethodPost, "/v1/community/blocks", ana, map[string]any{"username": "brd_bo"})
	if r := e.comment(post.ID.Std(), bo, nil, "hi"); r.Status != http.StatusNotFound {
		t.Fatalf("comment: %d", r.Status)
	}
	// The vote itself goes through (Vapor checks no block there); only the
	// notification is suppressed.
	if r := e.do(http.MethodPost, "/v1/board-posts/"+post.ID.String()+"/vote", bo, nil); r.Status != http.StatusOK {
		t.Fatalf("vote: %d", r.Status)
	}
	var page boards.BoardNotificationPage
	e.do(http.MethodGet, "/v1/community/notifications", ana, nil).JSON(t, &page)
	if len(page.Items) != 0 || len(e.pushes.titles()) != 0 {
		t.Fatalf("notified: %+v %v", page.Items, e.pushes.titles())
	}
}

func TestDailyPostPinnedAndFlagged(t *testing.T) {
	e := newEnv(t)
	alice := e.member("alice")
	s := &boards.Service{DB: e.app.Deps.DB, Q: boardsdb.New(e.app.Deps.DB)}
	ctx := context.Background()
	body := "NVDA closed at 131.2, up 4.2%.\n\nWritten by Norviq AI from the latest market data. Not financial advice."
	nvda := boards.DailyCandidate{Topic: boards.TopicStock, Symbol: "NVDA"}
	if _, err := s.InsertDaily(ctx, "2026-10-15", boards.DailyCandidate{Topic: boards.TopicETF, Symbol: "SPY"}, "SPY is up 9.1% this year", body); err != nil {
		t.Fatal(err)
	}
	if o, err := s.InsertDaily(ctx, "2026-10-16", nvda, "NVDA rose 4.2%", body); err != nil || o.String() != "published stock" {
		t.Fatalf("%v %v", o, err)
	}
	if o, _ := s.InsertDaily(ctx, "2026-10-16", nvda, "Second", body); o.String() != "already published" {
		t.Fatalf("second: %v", o)
	}
	e.exec(`UPDATE boards SET description = 'old words' WHERE slug = 'daily'`)
	if _, _, err := s.EnsureDaily(ctx); err != nil {
		t.Fatal(err)
	}
	var desc string
	_ = e.app.Deps.DB.QueryRow(ctx, `SELECT description FROM boards WHERE slug='daily'`).Scan(&desc)
	if desc != boards.DailyDescription {
		t.Fatalf("description: %q", desc)
	}

	var listed boards.BoardListResponse
	e.do(http.MethodGet, "/v1/boards", alice, nil).JSON(t, &listed)
	daily := listed.DailyPost
	if daily == nil || !daily.AIGenerated || daily.AITopic == nil || *daily.AITopic != "stock" || *daily.AuthorUsername != "norviq" || daily.Title != "NVDA rose 4.2%" {
		t.Fatalf("dailyPost: %+v", daily)
	}
	if strings.Join(daily.Tags, ",") != "stock,nvda,ai" {
		t.Fatalf("tags: %v", daily.Tags)
	}
	// A cursor (even an unusable one) drops the pin.
	listed = boards.BoardListResponse{}
	e.do(http.MethodGet, "/v1/boards?cursor=x", alice, nil).JSON(t, &listed)
	if listed.DailyPost != nil {
		t.Fatal("pinned with cursor")
	}
	if top := e.list("daily/posts?sort=top", alice); top.Items[0].ID != daily.ID {
		t.Fatal("top not pinned")
	}
	if mine := e.createPost("daily", alice, boards.KindText, "My own take", nil, nil); mine.AIGenerated {
		t.Fatal("member post flagged")
	}
	mixed := e.list("daily/posts", alice)
	if mixed.Items[0].ID != daily.ID || len(mixed.Items) != 3 {
		t.Fatalf("mixed: %v", ids(mixed.Items))
	}
	var spy *boards.BoardPostSummary
	for i := range mixed.Items {
		if mixed.Items[i].Title == "SPY is up 9.1% this year" {
			spy = &mixed.Items[i]
		}
	}
	// The ETF topic is not a BoardAITopic case, so it encodes no aiTopic.
	if spy == nil || !spy.AIGenerated || spy.AITopic != nil {
		t.Fatalf("older daily post: %+v", spy)
	}
	if asks := e.list("daily/posts?kind=ask", alice); len(asks.Items) != 0 {
		t.Fatalf("asks: %v", ids(asks.Items))
	}
	if r := e.do(http.MethodPost, "/v1/community/blocks", alice, map[string]any{"username": "norviq"}); r.Status != http.StatusBadRequest {
		t.Fatalf("block norviq: %d", r.Status)
	}
	var d boards.BoardPostDetail
	e.do(http.MethodGet, "/v1/board-posts/"+daily.ID.String(), alice, nil).JSON(t, &d)
	if d.Body == nil || !strings.Contains(*d.Body, "Not financial advice.") || !d.Post.AIGenerated {
		t.Fatalf("detail: %+v", d)
	}
	if r := e.comment(daily.ID.Std(), alice, nil, "A reply"); r.Status != http.StatusOK {
		t.Fatalf("comment: %d", r.Status)
	}
	if err := s.ResetDaily(ctx, "2026-10-16"); err != nil {
		t.Fatal(err)
	}
	listed = boards.BoardListResponse{}
	e.do(http.MethodGet, "/v1/boards", alice, nil).JSON(t, &listed)
	if listed.DailyPost == nil || listed.DailyPost.Title != "SPY is up 9.1% this year" {
		t.Fatalf("after reset: %+v", listed.DailyPost)
	}
	if o, _ := s.InsertDaily(ctx, "2026-10-16", nvda, "Rewritten", body); o.String() != "published stock" {
		t.Fatalf("rewrite: %v", o)
	}
	if o, _ := s.InsertDaily(ctx, "2026-10-17", boards.DailyCandidate{Topic: boards.TopicNews, URL: ptr("javascript:x")}, "News", body); o.String() != "skipped (headline link rejected)" {
		t.Fatalf("news: %v", o)
	}
	hist, err := s.DailyHistory(ctx, "2026-10-18")
	if err != nil || len(hist) != 2 || hist[0].Day != "2026-10-16" || *hist[1].Topic != "etf" {
		t.Fatalf("history: %+v %v", hist, err)
	}
}
