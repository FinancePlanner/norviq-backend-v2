package groups_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/boards"
	"norviq-api/internal/features/groups"
	"norviq-api/internal/features/social"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/realtime"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

// Ports GroupsRouteTests (GroupsTests.swift). The admin report queue and
// sanctions routes belong to another slice, so those are set up and read
// with SQL.

type fakePusher struct {
	mu   sync.Mutex
	sent []push.Notification
	to   []uuid.UUID
}

func (f *fakePusher) SendToUser(_ context.Context, id uuid.UUID, n push.Notification) (push.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	f.to = append(f.to, id)
	return push.Summary{}, nil
}

func (f *fakePusher) all() ([]uuid.UUID, []push.Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.to...), append([]push.Notification(nil), f.sent...)
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
	bus    *realtime.Bus
}

type user struct {
	id    uuid.UUID
	token string
	name  string
}

type option func(*testing.T)

func groupsOff(t *testing.T)    { t.Setenv("GROUPS_ENABLED", "0") }
func directoryOff(t *testing.T) { t.Setenv("GROUPS_PUBLIC_ENABLED", "0") }

func newEnv(t *testing.T, opts ...option) *env {
	t.Helper()
	t.Setenv("SOCIAL_ENABLED", "1")
	t.Setenv("GROUPS_ENABLED", "1")
	t.Setenv("GROUPS_PUBLIC_ENABLED", "1")
	t.Setenv("GROUPS_BLOCKED_TERMS", "")
	for _, o := range opts {
		o(t)
	}
	p, dc, bus := &fakePusher{}, &fakeDiscord{}, realtime.NewBus()
	wait := func(done <-chan struct{}) { <-done }
	app := testutil.NewApp(t, []server.Feature{
		boards.Feature{Push: p, Discord: dc},
		social.Feature{},
		groups.Feature{Push: p, Discord: dc, Bus: bus, WaitPush: wait},
	})
	return &env{t: t, app: app, pushes: p, alerts: dc, bus: bus}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) register(name string, guidelines bool) user {
	e.t.Helper()
	id := e.app.CreateUser("grp+" + name + "@example.com")
	username := "grp_" + name
	e.exec(`UPDATE users SET username = $2 WHERE id = $1`, id, username)
	u := user{id: id, token: e.app.Token(id), name: username}
	if guidelines {
		if r := e.do(http.MethodPost, "/v1/community/guidelines/accept", u, nil); r.Status != http.StatusNoContent {
			e.t.Fatalf("accept: %d %s", r.Status, r.Body)
		}
	}
	return u
}

func (e *env) member(name string) user { return e.register(name, true) }

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

func expect(t *testing.T, r *testutil.Response, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}

func expectCode(t *testing.T, r *testutil.Response, want string) {
	t.Helper()
	if got := code(t, r); got != want {
		t.Fatalf("code %q, want %q (%d %s)", got, want, r.Status, r.Body)
	}
}

func (e *env) create(as user, name, topic string, ticker *string, visibility string) groups.GroupDetail {
	e.t.Helper()
	body := map[string]any{"name": name, "iconEmoji": "🍎", "topic": topic, "visibility": visibility}
	if ticker != nil {
		body["ticker"] = *ticker
	}
	r := e.do(http.MethodPost, "/v1/groups", as, body)
	expect(e.t, r, http.StatusOK)
	var d groups.GroupDetail
	r.JSON(e.t, &d)
	return d
}

func (e *env) createDefault(as user) groups.GroupDetail {
	return e.create(as, "$AAPL holders", groups.TopicStock, ptr("AAPL"), groups.VisibilityPrivate)
}

func (e *env) post(channel apijson.UUID, as user, body string, clientID ...string) *testutil.Response {
	id := uuid.NewString()
	if len(clientID) > 0 {
		id = clientID[0]
	}
	return e.do(http.MethodPost, "/v1/group-channels/"+channel.String()+"/messages", as, map[string]any{"clientMessageId": id, "body": body})
}

func (e *env) inviteLink(groupID apijson.UUID, as user, maxUses *int) groups.GroupInviteLink {
	e.t.Helper()
	body := map[string]any{"expiresInHours": 24}
	if maxUses != nil {
		body["maxUses"] = *maxUses
	}
	r := e.do(http.MethodPost, "/v1/groups/"+groupID.String()+"/invites", as, body)
	expect(e.t, r, http.StatusOK)
	var l groups.GroupInviteLink
	r.JSON(e.t, &l)
	return l
}

func (e *env) join(groupID apijson.UUID, as, creator user) {
	e.t.Helper()
	l := e.inviteLink(groupID, creator, nil)
	expect(e.t, e.do(http.MethodPost, "/v1/group-invites/"+l.Code+"/redeem", as, nil), http.StatusOK)
}

func (e *env) page(channel apijson.UUID, as user, query string) groups.GroupMessagePage {
	e.t.Helper()
	r := e.do(http.MethodGet, "/v1/group-channels/"+channel.String()+"/messages"+query, as, nil)
	expect(e.t, r, http.StatusOK)
	var p groups.GroupMessagePage
	r.JSON(e.t, &p)
	return p
}

func (e *env) unread(as user) groups.GroupUnreadCount {
	e.t.Helper()
	r := e.do(http.MethodGet, "/v1/groups/unread-count", as, nil)
	expect(e.t, r, http.StatusOK)
	var u groups.GroupUnreadCount
	r.JSON(e.t, &u)
	return u
}

func seqs(p groups.GroupMessagePage) []int64 {
	out := []int64{}
	for _, m := range p.Items {
		out = append(out, m.Seq)
	}
	return out
}

func eqSeqs(t *testing.T, got []int64, want ...int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("seqs %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("seqs %v, want %v", got, want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// ---- Flag and gate ----------------------------------------------------------

func TestFlagOff(t *testing.T) {
	e := newEnv(t, groupsOff)
	ana := e.member("off")
	expect(t, e.do(http.MethodGet, "/v1/groups", ana, nil), http.StatusNotFound)
	if r := e.do(http.MethodGet, "/v1/social/config", ana, nil); !strings.Contains(string(r.Body), `"groups":false`) {
		t.Fatalf("config: %s", r.Body)
	}
}

func TestContributionGate(t *testing.T) {
	e := newEnv(t)
	ana := e.register("gate", false)
	r := e.do(http.MethodPost, "/v1/groups", ana, map[string]any{"name": "Club", "topic": "community", "visibility": "private"})
	expect(t, r, http.StatusForbidden)
	expectCode(t, r, "guidelines_required")
	if r := e.do(http.MethodGet, "/v1/social/config", ana, nil); !strings.Contains(string(r.Body), `"groups":true`) {
		t.Fatalf("config: %s", r.Body)
	}
}

// ---- Groups -----------------------------------------------------------------

func TestCreateAndPrivacy(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("ana"), e.member("bo")
	d := e.createDefault(ana)
	if len(d.Channels) != 1 || d.Channels[0].Name != "general" {
		t.Fatalf("channels: %+v", d.Channels)
	}
	if d.Group.Membership == nil || d.Group.Membership.Role != groups.RoleOwner {
		t.Fatalf("membership: %+v", d.Group.Membership)
	}
	if d.Group.Ticker == nil || *d.Group.Ticker != "AAPL" || !d.Permissions.CanManageChannels {
		t.Fatalf("detail: %+v", d)
	}
	if d.Group.DefaultChannelID == nil || *d.Group.DefaultChannelID != d.Channels[0].ID {
		t.Fatal("default channel")
	}
	r := e.do(http.MethodGet, "/v1/groups/"+d.Group.ID.String(), bo, nil)
	expect(t, r, http.StatusNotFound)
	expectCode(t, r, "group_not_found")
	expect(t, e.do(http.MethodGet, "/v1/group-channels/"+d.Channels[0].ID.String()+"/messages", bo, nil), http.StatusNotFound)

	var mine groups.GroupListResponse
	e.do(http.MethodGet, "/v1/groups", ana, nil).JSON(t, &mine)
	if len(mine.Items) != 1 || mine.Items[0].ID != d.Group.ID {
		t.Fatalf("mine: %+v", mine)
	}
}

func TestDailyCap(t *testing.T) {
	e := newEnv(t)
	ana := e.member("cap")
	for i := range 3 {
		e.create(ana, "Group "+string(rune('A'+i)), groups.TopicStock, ptr("AAPL"), groups.VisibilityPrivate)
	}
	r := e.do(http.MethodPost, "/v1/groups", ana, map[string]any{"name": "One more", "topic": "custom", "visibility": "private"})
	expect(t, r, http.StatusTooManyRequests)
	expectCode(t, r, "group_create_limit")
}

func TestPublicGroups(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("pub"), e.member("pubbo")
	g := e.create(ana, "Nvidia bulls", groups.TopicStock, ptr("$nvda"), groups.VisibilityPublic)
	e.create(ana, "Apple", groups.TopicStock, ptr("AAPL"), groups.VisibilityPublic)

	discover := func(q string) groups.GroupDirectoryPage {
		t.Helper()
		r := e.do(http.MethodGet, "/v1/groups/discover"+q, bo, nil)
		expect(t, r, http.StatusOK)
		var p groups.GroupDirectoryPage
		r.JSON(t, &p)
		return p
	}
	for _, q := range []string{"?ticker=nvda", "?q=bull"} {
		if p := discover(q); len(p.Items) != 1 || p.Items[0].ID != g.Group.ID {
			t.Fatalf("discover %s: %+v", q, p.Items)
		}
	}
	if p := discover("?tickers=MSFT,NVDA"); len(p.Items) != 1 {
		t.Fatalf("suggested: %+v", p.Items)
	}

	var preview groups.GroupDetail
	e.do(http.MethodGet, "/v1/groups/"+g.Group.ID.String(), bo, nil).JSON(t, &preview)
	if len(preview.Channels) != 0 || preview.Permissions != (groups.GroupPermissions{}) || preview.Group.Membership != nil {
		t.Fatalf("preview: %+v", preview)
	}
	expectCode(t, e.do(http.MethodGet, "/v1/group-channels/"+g.Channels[0].ID.String()+"/messages", bo, nil), "group_membership_required")

	var joined groups.GroupDetail
	r := e.do(http.MethodPost, "/v1/groups/"+g.Group.ID.String()+"/join", bo, nil)
	expect(t, r, http.StatusOK)
	r.JSON(t, &joined)
	if m := joined.Group.Membership; m == nil || m.Role != groups.RoleMember || m.PushEnabled || joined.Group.MemberCount != 2 {
		t.Fatalf("joined: %+v", joined.Group)
	}
}

func TestPublicDirectoryOff(t *testing.T) {
	e := newEnv(t, directoryOff)
	ana := e.member("nopub")
	expect(t, e.do(http.MethodGet, "/v1/groups/discover", ana, nil), http.StatusNotFound)
	r := e.do(http.MethodPost, "/v1/groups", ana, map[string]any{"name": "Open", "topic": "community", "visibility": "public"})
	expect(t, r, http.StatusBadRequest)
}

// ---- Messages ---------------------------------------------------------------

func TestMessages(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("msg"), e.member("msgbo")
	g := e.createDefault(ana)
	ch := g.Channels[0].ID
	e.join(g.Group.ID, bo, ana)

	first := e.post(ch, ana, "hello", "c-1")
	expect(t, first, http.StatusCreated)
	again := e.post(ch, ana, "hello", "c-1")
	expect(t, again, http.StatusOK)
	var m1, m2 groups.GroupMessage
	first.JSON(t, &m1)
	again.JSON(t, &m2)
	if m1.ID != m2.ID {
		t.Fatal("retry posted twice")
	}
	for i := 2; i <= 5; i++ {
		expect(t, e.post(ch, bo, "m"), http.StatusCreated)
	}

	latest := e.page(ch, ana, "?limit=2")
	eqSeqs(t, seqs(latest), 4, 5)
	if !latest.HasMoreBefore || latest.HasMoreAfter {
		t.Fatalf("latest flags: %+v", latest)
	}
	older := e.page(ch, ana, "?before=4&limit=2")
	eqSeqs(t, seqs(older), 2, 3)
	if !older.HasMoreBefore || !older.HasMoreAfter {
		t.Fatalf("older flags: %+v", older)
	}
	catchUp := e.page(ch, ana, "?after=3")
	eqSeqs(t, seqs(catchUp), 4, 5)
	if catchUp.HasMoreAfter {
		t.Fatal("catch-up hasMoreAfter")
	}
	expect(t, e.do(http.MethodGet, "/v1/group-channels/"+ch.String()+"/messages?after=1&before=3", ana, nil), http.StatusBadRequest)

	// Ana's own message never counts; Bo's four do.
	if u := e.unread(ana); u.Total != 4 {
		t.Fatalf("unread %d", u.Total)
	}
	expect(t, e.do(http.MethodPost, "/v1/group-channels/"+ch.String()+"/read", ana, map[string]any{"seq": 5}), http.StatusNoContent)
	expect(t, e.do(http.MethodPost, "/v1/group-channels/"+ch.String()+"/read", ana, map[string]any{"seq": 2}), http.StatusNoContent)
	if u := e.unread(ana); u.Total != 0 {
		t.Fatalf("unread after read %d", u.Total)
	}
	if p := e.page(ch, ana, ""); p.LastReadSeq != 5 {
		t.Fatalf("lastReadSeq %d", p.LastReadSeq)
	}

	filtered := e.post(ch, bo, "you faggot")
	expect(t, filtered, http.StatusUnprocessableEntity)
	expectCode(t, filtered, "content_rejected")
	expect(t, e.post(ch, bo, strings.Repeat("x", 2001)), http.StatusBadRequest)

	// Invite joiners start with push on. Without Redis there is no
	// throttle: Bo got Ana's "hello" (the retry sent nothing), Ana got each
	// of Bo's four; nobody is pushed their own message.
	to, sent := e.pushes.all()
	toAna, toBo := 0, 0
	for i := range to {
		if sent[i].Category != groups.PushMessage {
			t.Fatalf("push: %+v", sent[i])
		}
		switch to[i] {
		case ana.id:
			toAna++
		case bo.id:
			toBo++
		}
	}
	if toAna != 4 || toBo != 1 || len(to) != 5 {
		t.Fatalf("pushes: ana %d bo %d total %d", toAna, toBo, len(to))
	}
}

func TestJoinMarksRead(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("hist"), e.member("histbo")
	g := e.createDefault(ana)
	for range 3 {
		e.post(g.Channels[0].ID, ana, "old")
	}
	e.join(g.Group.ID, bo, ana)
	if u := e.unread(bo); u.Total != 0 {
		t.Fatalf("unread %d", u.Total)
	}
}

func TestDeletePermissions(t *testing.T) {
	e := newEnv(t)
	ana, bo, cy := e.member("del"), e.member("delbo"), e.member("delcy")
	g := e.createDefault(ana)
	ch := g.Channels[0].ID
	e.join(g.Group.ID, bo, ana)
	e.join(g.Group.ID, cy, ana)

	var m groups.GroupMessage
	e.post(ch, bo, "mine").JSON(t, &m)
	expectCode(t, e.do(http.MethodDelete, "/v1/group-messages/"+m.ID.String(), cy, nil), "group_forbidden")
	expect(t, e.do(http.MethodDelete, "/v1/group-messages/"+m.ID.String(), ana, nil), http.StatusNoContent)
	p := e.page(ch, cy, "")
	if len(p.Items) != 1 || !p.Items[0].IsDeleted || p.Items[0].Body != nil {
		t.Fatalf("tombstone: %+v", p.Items)
	}
	expect(t, e.do(http.MethodDelete, "/v1/group-messages/"+uuid.NewString(), ana, nil), http.StatusNotFound)
}

func TestBlocking(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("blk"), e.member("blkbo")
	g := e.createDefault(ana)
	e.join(g.Group.ID, bo, ana)
	e.post(g.Channels[0].ID, bo, "hi ana")
	expect(t, e.do(http.MethodPost, "/v1/community/blocks", ana, map[string]any{"username": "grp_blkbo"}), http.StatusNoContent)
	if p := e.page(g.Channels[0].ID, ana, ""); len(p.Items) != 0 {
		t.Fatalf("blocked author visible: %+v", p.Items)
	}
	if u := e.unread(ana); u.Total != 0 {
		t.Fatalf("unread %d", u.Total)
	}
}

// ---- Invites ----------------------------------------------------------------

func TestInviteLinks(t *testing.T) {
	e := newEnv(t)
	ana, bo, cy := e.member("inv"), e.member("invbo"), e.member("invcy")
	g := e.createDefault(ana)
	link := e.inviteLink(g.Group.ID, ana, ptr(1))
	if !strings.HasSuffix(link.URL, "/g/"+link.Code) {
		t.Fatalf("url %s", link.URL)
	}
	var preview groups.GroupInvitePreview
	e.do(http.MethodGet, "/v1/group-invites/"+link.Code, bo, nil).JSON(t, &preview)
	if preview.Group.Name != "$AAPL holders" || preview.InviterUsername == nil || *preview.InviterUsername != "grp_inv" || preview.AlreadyMember {
		t.Fatalf("preview: %+v", preview)
	}
	expect(t, e.do(http.MethodPost, "/v1/group-invites/"+link.Code+"/redeem", bo, nil), http.StatusOK)
	expect(t, e.do(http.MethodPost, "/v1/group-invites/"+link.Code+"/redeem", bo, nil), http.StatusOK)
	spent := e.do(http.MethodPost, "/v1/group-invites/"+link.Code+"/redeem", cy, nil)
	expect(t, spent, http.StatusGone)
	expectCode(t, spent, "invite_exhausted")

	revoked := e.inviteLink(g.Group.ID, ana, nil)
	expect(t, e.do(http.MethodDelete, "/v1/group-invites/"+revoked.ID.String(), ana, nil), http.StatusNoContent)
	expect(t, e.do(http.MethodGet, "/v1/group-invites/"+revoked.Code, cy, nil), http.StatusNotFound)

	expired := e.inviteLink(g.Group.ID, ana, nil)
	e.exec(`UPDATE group_invites SET expires_at = now() - interval '60 seconds' WHERE id = $1`, expired.ID.Std())
	expectCode(t, e.do(http.MethodGet, "/v1/group-invites/"+expired.Code, cy, nil), "invite_expired")
}

func TestConcurrentRedeem(t *testing.T) {
	e := newEnv(t)
	ana, bo, cy := e.member("race"), e.member("racebo"), e.member("racecy")
	g := e.createDefault(ana)
	link := e.inviteLink(g.Group.ID, ana, ptr(1))
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i, u := range []user{bo, cy} {
		wg.Go(func() {
			statuses[i] = e.app.Do(http.MethodPost, "/v1/group-invites/"+link.Code+"/redeem", u.token, nil).Status
		})
	}
	wg.Wait()
	ok, gone := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusGone:
			gone++
		}
	}
	if ok != 1 || gone != 1 {
		t.Fatalf("statuses %v", statuses)
	}
	var d groups.GroupDetail
	e.do(http.MethodGet, "/v1/groups/"+g.Group.ID.String(), ana, nil).JSON(t, &d)
	if d.Group.MemberCount != 2 {
		t.Fatalf("memberCount %d", d.Group.MemberCount)
	}
}

func TestDirectInvitations(t *testing.T) {
	e := newEnv(t)
	ana, bo, cy := e.member("dir"), e.member("dirbo"), e.member("dircy")
	e.exec(`INSERT INTO social_friendships (id, user_id, friend_id, created_at) VALUES ($1, $2, $3, now()), ($4, $3, $2, now())`,
		uuid.New(), ana.id, bo.id, uuid.New())
	g := e.createDefault(ana)

	var resp groups.GroupDirectInvitationResponse
	r := e.do(http.MethodPost, "/v1/groups/"+g.Group.ID.String()+"/invitations", ana, map[string]any{"userIds": []string{bo.id.String(), cy.id.String()}})
	expect(t, r, http.StatusOK)
	r.JSON(t, &resp)
	if len(resp.InvitedUserIDs) != 1 || resp.InvitedUserIDs[0].Std() != bo.id {
		t.Fatalf("invited: %+v", resp)
	}
	if to, sent := e.pushes.all(); len(to) != 1 || to[0] != bo.id || sent[0].Category != groups.PushInvite {
		t.Fatalf("invite push: %v %+v", to, sent)
	}

	var pending groups.GroupInvitationList
	e.do(http.MethodGet, "/v1/groups/invitations", bo, nil).JSON(t, &pending)
	if len(pending.Items) != 1 || pending.Items[0].Group.ID != g.Group.ID {
		t.Fatalf("pending: %+v", pending)
	}
	if u := e.unread(bo); u.PendingInvitationCount != 1 {
		t.Fatalf("pendingInvitationCount %d", u.PendingInvitationCount)
	}
	joined := e.do(http.MethodPost, "/v1/groups/invitations/"+pending.Items[0].ID.String()+"/accept", bo, nil)
	expect(t, joined, http.StatusOK)
	var d groups.GroupDetail
	joined.JSON(t, &d)
	if d.Group.Membership == nil || d.Group.Membership.Role != groups.RoleMember {
		t.Fatalf("joined: %+v", d.Group)
	}
	var none groups.GroupInvitationList
	e.do(http.MethodGet, "/v1/groups/invitations", cy, nil).JSON(t, &none)
	if len(none.Items) != 0 {
		t.Fatalf("cy invited: %+v", none)
	}
}

// ---- Moderation -------------------------------------------------------------

func TestModeration(t *testing.T) {
	e := newEnv(t)
	ana, bo, cy := e.member("mod"), e.member("modbo"), e.member("modcy")
	g := e.createDefault(ana)
	id := g.Group.ID.String()
	e.join(g.Group.ID, bo, ana)
	e.join(g.Group.ID, cy, ana)

	expectCode(t, e.do(http.MethodDelete, "/v1/groups/"+id+"/members/"+cy.id.String(), bo, nil), "group_forbidden")
	expect(t, e.do(http.MethodPatch, "/v1/groups/"+id+"/members/"+bo.id.String(), ana, map[string]any{"role": "admin"}), http.StatusNoContent)
	expectCode(t, e.do(http.MethodDelete, "/v1/groups/"+id+"/members/"+ana.id.String(), bo, nil), "group_forbidden")
	expect(t, e.do(http.MethodPost, "/v1/groups/"+id+"/bans", bo, map[string]any{"userId": cy.id.String(), "reason": "spam"}), http.StatusNoContent)

	rejoin := e.inviteLink(g.Group.ID, ana, nil)
	expectCode(t, e.do(http.MethodPost, "/v1/group-invites/"+rejoin.Code+"/redeem", cy, nil), "group_banned")

	expectCode(t, e.do(http.MethodPost, "/v1/groups/"+id+"/leave", ana, nil), "owner_must_transfer")
	expect(t, e.do(http.MethodPost, "/v1/groups/"+id+"/transfer", ana, map[string]any{"userId": bo.id.String()}), http.StatusNoContent)
	var d groups.GroupDetail
	e.do(http.MethodGet, "/v1/groups/"+id, ana, nil).JSON(t, &d)
	if d.OwnerUsername == nil || *d.OwnerUsername != "grp_modbo" || d.Group.Membership == nil || d.Group.Membership.Role != groups.RoleAdmin {
		t.Fatalf("after transfer: %+v", d)
	}
	expect(t, e.do(http.MethodPost, "/v1/groups/"+id+"/leave", ana, nil), http.StatusNoContent)
}

func TestChannels(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("chn"), e.member("chnbo")
	g := e.createDefault(ana)
	id := g.Group.ID.String()
	e.join(g.Group.ID, bo, ana)

	r := e.do(http.MethodPost, "/v1/groups/"+id+"/channels", ana, map[string]any{"name": "Earnings Talk", "topic": "Q3"})
	expect(t, r, http.StatusOK)
	var created groups.GroupChannel
	r.JSON(t, &created)
	if created.Name != "earnings-talk" {
		t.Fatalf("name %q", created.Name)
	}
	expectCode(t, e.do(http.MethodPost, "/v1/groups/"+id+"/channels", ana, map[string]any{"name": "earnings-talk"}), "channel_name_taken")
	expectCode(t, e.do(http.MethodPost, "/v1/groups/"+id+"/channels", bo, map[string]any{"name": "mine"}), "group_forbidden")
	expect(t, e.do(http.MethodDelete, "/v1/group-channels/"+g.Channels[0].ID.String(), ana, nil), http.StatusNoContent)
	expectCode(t, e.do(http.MethodDelete, "/v1/group-channels/"+created.ID.String(), ana, nil), "last_channel")
}

func TestReports(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("rep"), e.member("repbo")
	g := e.createDefault(ana)
	e.join(g.Group.ID, bo, ana)
	var m groups.GroupMessage
	e.post(g.Channels[0].ID, bo, "buy my course").JSON(t, &m)

	expect(t, e.do(http.MethodPost, "/v1/group-messages/"+m.ID.String()+"/report", ana, map[string]any{"reason": "spam"}), http.StatusAccepted)
	expect(t, e.do(http.MethodDelete, "/v1/group-messages/"+m.ID.String(), bo, nil), http.StatusNoContent)
	if n := e.count(`SELECT count(*) FROM social_reports WHERE target_type = 'group_message' AND target_id = $1 AND reason = 'spam' AND reporter_id = $2`,
		m.ID.String(), ana.id); n != 1 {
		t.Fatalf("reports %d", n)
	}
	r := e.do(http.MethodPost, "/v1/group-messages/"+m.ID.String()+"/report", ana, map[string]any{"reason": "nope"})
	expect(t, r, http.StatusBadRequest)
	if !strings.Contains(string(r.Body), "Cannot initialize BoardReportReason from invalid String value nope") {
		t.Fatalf("reason: %s", r.Body)
	}
	r = e.do(http.MethodPost, "/v1/group-messages/"+m.ID.String()+"/report", ana, map[string]any{"reason": "spam", "note": strings.Repeat("n", 1001)})
	expect(t, r, http.StatusBadRequest)
}

func TestGlobalBan(t *testing.T) {
	e := newEnv(t)
	ana := e.member("ban")
	e.exec(`INSERT INTO community_sanctions (id, user_id, kind, reason, created_at) VALUES ($1, $2, 'ban', 'Testing', now())`, uuid.New(), ana.id)
	r := e.do(http.MethodGet, "/v1/groups", ana, nil)
	expect(t, r, http.StatusForbidden)
	expectCode(t, r, "community_banned")
}

// Groups owned by someone hidden from the viewer are filtered before paging,
// so the first page holds the visible group and no cursor (Vapor filtered
// after paging: an empty page that still had a nextCursor).
func TestDiscoverHidesBlockedOwnersBeforePaging(t *testing.T) {
	e := newEnv(t)
	ana, hid, bo := e.member("dsc"), e.member("dschid"), e.member("dscbo")
	visible := e.create(ana, "Visible", groups.TopicStock, ptr("AAPL"), groups.VisibilityPublic)
	seed := e.create(hid, "Hidden", groups.TopicStock, ptr("MSFT"), groups.VisibilityPublic)
	e.exec(`INSERT INTO groups
		SELECT (jsonb_populate_record(NULL::groups, to_jsonb(g) || jsonb_build_object('id', gen_random_uuid(), 'name', 'Hidden ' || n))).*
		FROM groups g, generate_series(1, 25) n WHERE g.id = $1`, seed.Group.ID.Std())
	e.exec(`UPDATE groups SET member_count = 50 WHERE owner_id = $1`, hid.id)
	expect(t, e.do(http.MethodPost, "/v1/community/blocks", bo, map[string]any{"username": "grp_dschid"}), http.StatusNoContent)

	r := e.do(http.MethodGet, "/v1/groups/discover", bo, nil)
	expect(t, r, http.StatusOK)
	var p groups.GroupDirectoryPage
	r.JSON(t, &p)
	if len(p.Items) != 1 || p.Items[0].ID != visible.Group.ID || p.NextCursor != nil {
		t.Fatalf("page: %d items, cursor %v", len(p.Items), p.NextCursor)
	}
	// Someone who blocked nobody still pages through all 27.
	r = e.do(http.MethodGet, "/v1/groups/discover", ana, nil)
	r.JSON(t, &p)
	if len(p.Items) != 25 || p.NextCursor == nil {
		t.Fatalf("unblocked page: %d items, cursor %v", len(p.Items), p.NextCursor)
	}
}

// A ban revokes the user's pending invitations; one revoked earlier keeps
// its revoked_at (Vapor overwrote it with the ban time).
func TestBanKeepsEarlierRevocation(t *testing.T) {
	e := newEnv(t)
	ana, cy := e.member("bsw"), e.member("bswcy")
	g := e.createDefault(ana)
	earlier := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	old, pending := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{old, pending} {
		e.exec(`INSERT INTO group_invites (id, group_id, created_by, invitee_id, expires_at, uses, created_at)
			VALUES ($1, $2, $3, $4, now() + interval '7 days', 0, now())`, id, g.Group.ID.Std(), ana.id, cy.id)
	}
	e.exec(`UPDATE group_invites SET revoked_at = $2 WHERE id = $1`, old, earlier)
	expect(t, e.do(http.MethodPost, "/v1/groups/"+g.Group.ID.String()+"/bans", ana, map[string]any{"userId": cy.id.String()}), http.StatusNoContent)
	var kept time.Time
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT revoked_at FROM group_invites WHERE id = $1`, old).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if !kept.Equal(earlier) {
		t.Fatalf("earlier revocation overwritten: %v", kept)
	}
	if n := e.count(`SELECT count(*) FROM group_invites WHERE id = $1 AND revoked_at IS NOT NULL`, pending); n != 1 {
		t.Fatal("pending invitation not revoked")
	}
}

func TestReaderMembershipsCarryNamesAndRolesOnly(t *testing.T) {
	e := newEnv(t)
	owner := e.member("reader_owner")
	stranger := e.member("reader_stranger")
	e.create(owner, "$AAPL club", groups.TopicStock, ptr("AAPL"), groups.VisibilityPrivate)

	reader := groups.NewReader(e.app.Deps)
	mine, err := reader.Memberships(context.Background(), owner.id)
	if err != nil || len(mine) != 1 || mine[0].Name != "$AAPL club" || mine[0].Role != "owner" {
		t.Fatalf("mine = %+v, %v", mine, err)
	}
	theirs, err := reader.Memberships(context.Background(), stranger.id)
	if err != nil || len(theirs) != 0 {
		t.Fatalf("theirs = %+v, %v", theirs, err)
	}
}
