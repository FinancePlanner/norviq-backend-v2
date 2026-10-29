package achievements

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/apijson"

	"github.com/google/uuid"
)

// Ported from SocialFeedTests.swift and the route half of
// SocialAchievementPrivacyTests.swift. The friend graph is seeded with SQL:
// the social routes belong to another slice.

type feedResp struct {
	Items []struct {
		ID    string `json:"id"`
		Actor struct {
			ID                string  `json:"id"`
			Username          string  `json:"username"`
			FriendshipStatus  string  `json:"friendshipStatus"`
			MutualFriendCount *int    `json:"mutualFriendCount"`
			AvatarURL         *string `json:"avatarUrl"`
		} `json:"actor"`
		Achievement struct {
			Key, Tier, Title, IconKey string
		} `json:"achievement"`
		CongratsCount       int  `json:"congratsCount"`
		ViewerCongratulated bool `json:"viewerCongratulated"`
	} `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

func (f feedResp) ids() []string {
	out := make([]string, len(f.Items))
	for i, it := range f.Items {
		out[i] = it.ID
	}
	return out
}

// befriend stores the friendship both ways, started daysAgo.
func (e *env) befriend(a, b uuid.UUID, daysAgo int) {
	at := time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour)
	e.exec(`INSERT INTO social_friendships (id, user_id, friend_id, created_at) VALUES ($1, $2, $3, $5), ($4, $3, $2, $5)`,
		uuid.New(), a, b, uuid.New(), at)
}

// activity stores an unlock of key (its first tier) daysAgo plus its feed item.
func (e *env) activity(actor uuid.UUID, key string, daysAgo float64) string {
	at := time.Now().Add(-time.Duration(daysAgo * float64(24*time.Hour)))
	tier := ""
	if d := Lookup(key); d != nil {
		tier = d.Tiers[0].Tier
	}
	ua := e.insertUnlock(actor, key, tier, SourceLive, false, at)
	id := uuid.New()
	e.exec(`INSERT INTO social_activity (id, actor_id, kind, user_achievement_id, created_at) VALUES ($1, $2, 'achievement_unlocked', $3, $4)`, id, actor, ua, at)
	return apijson.FormatUUID(id)
}

func (e *env) settings(userID uuid.UUID, showAchievements, showReturn bool, suspended bool) {
	var susp *time.Time
	if suspended {
		now := time.Now()
		susp = &now
	}
	e.exec(`INSERT INTO social_settings (id, user_id, search_visibility, discoverable_by_contacts, discoverable_by_x, show_return_percent,
			show_streaks, show_xp, leaderboard_opt_in, created_at, suspended_at, show_achievements)
		VALUES ($1, $2, 'everyone', true, true, $3, true, true, true, now(), $4, $5)`, uuid.New(), userID, showReturn, susp, showAchievements)
}

func (e *env) block(blocker, blocked uuid.UUID) {
	e.exec(`INSERT INTO social_blocks (id, blocker_id, blocked_id, created_at) VALUES ($1, $2, $3, now())`, uuid.New(), blocker, blocked)
}

func (e *env) feed(tok, query string) feedResp {
	e.t.Helper()
	r := e.Do(http.MethodGet, "/v1/social/feed"+query, tok, nil)
	if r.Status != http.StatusOK {
		e.t.Fatalf("feed %d %s", r.Status, r.Body)
	}
	var f feedResp
	r.JSON(e.t, &f)
	return f
}

func TestFeedFlagsOff(t *testing.T) {
	for _, flags := range [][]string{
		{"SOCIAL_ACTIVITY_FEED_ENABLED", "0"},
		{"ACHIEVEMENTS_ENABLED", "0"},
		{"SOCIAL_ENABLED", "0"},
	} {
		e := newEnv(t, flags...)
		a, tok := e.user("sfd-off1")
		b, _ := e.user("sfd-off2")
		e.befriend(a, b, 30)
		for _, c := range []struct{ method, path string }{
			{http.MethodGet, "/v1/social/feed"},
			{http.MethodPost, "/v1/social/feed/" + uuid.NewString() + "/congrats"},
			{http.MethodGet, "/v1/social/users/" + b.String() + "/achievements"},
		} {
			if r := e.Do(c.method, c.path, tok, nil); r.Status != http.StatusNotFound {
				t.Fatalf("%v %s %s: %d", flags, c.method, c.path, r.Status)
			}
		}
	}
}

func TestFeedSuspendedCallerIs403(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sfd-susp")
	e.settings(v, true, false, true)
	r := e.Do(http.MethodGet, "/v1/social/feed", tok, nil)
	if r.Status != http.StatusForbidden || !strings.Contains(string(r.Body), "Your access to friends features has been suspended.") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestFeedNewestFirstAndPaging(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sfd-pg1")
	f, _ := e.user("sfd-pg2")
	e.befriend(v, f, 30)
	older := e.activity(f, "start.first_holding", 3)
	newer := e.activity(f, "start.first_budget", 2)
	p1 := e.feed(tok, "?limit=1")
	if len(p1.Items) != 1 || p1.Items[0].ID != newer || p1.Items[0].Actor.ID != apijson.FormatUUID(f) ||
		p1.Items[0].Achievement.Key != "start.first_budget" || p1.Items[0].CongratsCount != 0 || p1.Items[0].ViewerCongratulated {
		t.Fatalf("page1 %+v", p1)
	}
	if p1.Items[0].Actor.Username != "sfd_pg2" || p1.Items[0].Actor.FriendshipStatus != "friends" {
		t.Fatalf("actor %+v", p1.Items[0].Actor)
	}
	if p1.NextCursor == nil {
		t.Fatal("no cursor")
	}
	p2 := e.feed(tok, "?limit=1&cursor="+*p1.NextCursor)
	if len(p2.Items) != 1 || p2.Items[0].ID != older || p2.NextCursor != nil {
		t.Fatalf("page2 %+v", p2)
	}
	if r := e.Do(http.MethodGet, "/v1/social/feed?cursor=nope", tok, nil); r.Status != http.StatusBadRequest {
		t.Fatalf("bad cursor %d", r.Status)
	}
	if r := e.Do(http.MethodGet, "/v1/social/feed?limit=abc", tok, nil); r.Status != http.StatusBadRequest {
		t.Fatalf("bad limit %d", r.Status)
	}
	// Limits clamp to 1…50.
	if got := e.feed(tok, "?limit=0"); len(got.Items) != 1 {
		t.Fatalf("limit=0 -> %d", len(got.Items))
	}
}

func TestFeedTimeAndPeopleRules(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sfd-tr1")
	f, _ := e.user("sfd-tr2")
	stranger, _ := e.user("sfd-tr3")
	e.befriend(v, f, 200)
	e.activity(v, "start.first_holding", 1)
	e.activity(stranger, "start.first_holding", 1)
	e.activity(f, "start.first_budget", 100) // older than 90 days
	kept := e.activity(f, "start.first_goal", 10)
	kept2 := e.activity(f, "start.first_holding", 80)
	if got := e.feed(tok, "").ids(); strings.Join(got, ",") != kept+","+kept2 {
		t.Fatalf("got %v", got)
	}
}

func TestFeedBeforeFriendshipHidden(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sfd-bf1")
	f, _ := e.user("sfd-bf2")
	e.befriend(v, f, 30)
	e.activity(f, "start.first_holding", 40)
	after := e.activity(f, "start.first_budget", 5)
	if got := e.feed(tok, "").ids(); len(got) != 1 || got[0] != after {
		t.Fatalf("got %v", got)
	}
}

func TestFeedPrivacyRules(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sfd-pr1")
	off, _ := e.user("sfd-pr2")
	blockedByMe, _ := e.user("sfd-pr3")
	blocksMe, _ := e.user("sfd-pr4")
	suspended, _ := e.user("sfd-pr5")
	ok, _ := e.user("sfd-pr6")
	for _, o := range []uuid.UUID{off, blockedByMe, blocksMe, suspended, ok} {
		e.befriend(v, o, 30)
		e.activity(o, "start.first_holding", 1)
	}
	e.settings(off, false, false, false)
	e.settings(suspended, true, false, true)
	e.block(v, blockedByMe)
	e.block(blocksMe, v)
	got := e.feed(tok, "")
	if len(got.Items) != 1 || got.Items[0].Actor.ID != apijson.FormatUUID(ok) {
		t.Fatalf("got %+v", got)
	}
	// Mutual friend count: v and ok share no friends, so it is omitted.
	if got.Items[0].Actor.MutualFriendCount != nil {
		t.Fatal("mutual count")
	}
}

func TestFeedPerformanceNeedsReturnPercent(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sfd-perf1")
	f, _ := e.user("sfd-perf2")
	e.befriend(v, f, 30)
	plain := e.activity(f, "start.first_holding", 2)
	perf := e.activity(f, "perf.positive_year", 1)
	if got := e.feed(tok, "").ids(); len(got) != 1 || got[0] != plain {
		t.Fatalf("got %v", got)
	}
	e.settings(f, true, true, false)
	if got := e.feed(tok, "").ids(); strings.Join(got, ",") != perf+","+plain {
		t.Fatalf("got %v", got)
	}
}

func TestCongratsIdempotent(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sfd-cg1")
	f, _ := e.user("sfd-cg2")
	e.befriend(v, f, 30)
	id := e.activity(f, "start.first_holding", 1)
	for range 2 {
		if r := e.Do(http.MethodPost, "/v1/social/feed/"+id+"/congrats", tok, nil); r.Status != http.StatusNoContent {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM social_congrats WHERE activity_id = $1`, id); n != 1 {
		t.Fatalf("congrats rows %d", n)
	}
	first := e.feed(tok, "").Items[0]
	if first.CongratsCount != 1 || !first.ViewerCongratulated {
		t.Fatalf("%+v", first)
	}
}

func TestCongratsGuards(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sfd-gd1")
	f, _ := e.user("sfd-gd2")
	stranger, _ := e.user("sfd-gd3")
	e.befriend(v, f, 30)
	own := e.activity(v, "start.first_holding", 1)
	strangers := e.activity(stranger, "start.first_holding", 1)
	cases := []struct {
		path   string
		status int
		reason string
	}{
		{own, http.StatusBadRequest, "You cannot congratulate yourself"},
		{strangers, http.StatusNotFound, "Activity not found"},
		{uuid.NewString(), http.StatusNotFound, "Activity not found"},
		{"not-a-uuid", http.StatusBadRequest, "Invalid activityId"},
	}
	for _, c := range cases {
		r := e.Do(http.MethodPost, "/v1/social/feed/"+c.path+"/congrats", tok, nil)
		if r.Status != c.status || !strings.Contains(string(r.Body), c.reason) {
			t.Fatalf("%s: %d %s", c.path, r.Status, r.Body)
		}
	}
	if e.count(`SELECT COUNT(*) FROM social_congrats`) != 0 {
		t.Fatal("rows written")
	}
}

func TestCongratsPushThrottle(t *testing.T) {
	e := newEnv(t)
	actor, _ := e.user("sfd-ps1")
	a, tokA := e.user("sfd-ps2")
	b, tokB := e.user("sfd-ps3")
	e.befriend(actor, a, 30)
	e.befriend(actor, b, 30)
	e.addDevice(actor, "capable", capability)
	e.addDevice(actor, "old-app")
	id := e.activity(actor, "social.inviter", 1)
	post := func(tok string) {
		if r := e.Do(http.MethodPost, "/v1/social/feed/"+id+"/congrats", tok, nil); r.Status != http.StatusNoContent {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	post(tokA)
	sent := e.sender.all()
	if len(sent) != 1 || sent[0].n.Category != "achievement_congrats" || sent[0].n.Title != "🎉 Congrats" ||
		sent[0].n.Body != "sfd_ps2 cheered your Recruiter (Bronze)" || sent[0].n.ThreadID != "achievements" ||
		strings.Join(sent[0].tokens, ",") != "capable" {
		t.Fatalf("sent %+v", sent)
	}
	post(tokA) // duplicate: no push
	post(tokB) // another friend a moment later: throttled
	if len(e.sender.all()) != 1 {
		t.Fatal("throttle")
	}
	if n := e.count(`SELECT COUNT(*) FROM social_congrats WHERE activity_id = $1`, id); n != 2 {
		t.Fatalf("rows %d", n)
	}
}

func TestCongratsPushAfterWindow(t *testing.T) {
	e := newEnv(t)
	actor, _ := e.user("sfd-pw1")
	a, _ := e.user("sfd-pw2")
	b, tokB := e.user("sfd-pw3")
	e.befriend(actor, a, 30)
	e.befriend(actor, b, 30)
	e.addDevice(actor, "capable", capability)
	id := e.activity(actor, "start.first_holding", 1)
	e.exec(`INSERT INTO social_congrats (id, activity_id, from_user_id, created_at) VALUES ($1, $2, $3, now() - interval '11 minutes')`, uuid.New(), id, a)
	if r := e.Do(http.MethodPost, "/v1/social/feed/"+id+"/congrats", tokB, nil); r.Status != http.StatusNoContent {
		t.Fatal(r.Status)
	}
	if len(e.sender.all()) != 1 {
		t.Fatal("no push after window")
	}
}

// SocialAchievementPrivacyTests: GET /v1/social/users/:userId/achievements.

func TestFriendAchievementsRoute(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sap-v")
	owner, ownerTok := e.user("sap-owner")
	stranger, _ := e.user("sap-stranger")
	e.befriend(v, owner, 30)
	now := time.Now()
	e.insertUnlock(owner, "start.first_holding", "", SourceLive, true, now.Add(-2*time.Hour))
	e.insertUnlock(owner, "discipline.check_in", "silver", SourceLive, true, now.Add(-time.Hour))
	e.insertUnlock(owner, "legacy.first_purchase", "bronze", SourceLegacyBadge, true, now)
	e.insertUnlock(owner, "perf.comeback", "", SourceSweep, true, now.Add(-30*time.Minute))
	get := func(tok string, who uuid.UUID) (int, []UnlockDTO) {
		r := e.do(http.MethodGet, "/v1/social/users/"+who.String()+"/achievements", tok, "pt", nil)
		var out []UnlockDTO
		if r.Status == http.StatusOK {
			r.JSON(t, &out)
		}
		return r.Status, out
	}
	status, list := get(tok, owner)
	if status != http.StatusOK || len(list) != 2 || list[0].Key != "discipline.check_in" || list[0].XP != 50 ||
		list[1].Key != "start.first_holding" || list[1].Title != "Primeira posição" {
		t.Fatalf("%d %+v", status, list)
	}
	// The owner sees their own, performance included (legacy never).
	if _, own := get(ownerTok, owner); len(own) != 3 {
		t.Fatalf("own %+v", own)
	}
	if s, _ := get(tok, stranger); s != http.StatusNotFound {
		t.Fatalf("stranger %d", s)
	}
	// Return % shared: performance visible.
	e.settings(owner, true, true, false)
	if _, l := get(tok, owner); len(l) != 3 || l[0].Key != "perf.comeback" {
		t.Fatalf("with return %% %+v", l)
	}
	// A block set by the owner hides the list.
	e.block(owner, v)
	if s, _ := get(tok, owner); s != http.StatusNotFound {
		t.Fatalf("blocked %d", s)
	}
	if r := e.Do(http.MethodGet, "/v1/social/users/nope/achievements", tok, nil); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Invalid userId") {
		t.Fatalf("bad id %d %s", r.Status, r.Body)
	}
}

func TestFriendAchievementsSharingOff(t *testing.T) {
	e := newEnv(t)
	v, tok := e.user("sap2-v")
	owner, _ := e.user("sap2-owner")
	e.befriend(v, owner, 30)
	e.insertUnlock(owner, "start.first_holding", "", SourceLive, true, time.Now())
	e.settings(owner, false, false, false)
	if r := e.Do(http.MethodGet, "/v1/social/users/"+owner.String()+"/achievements", tok, nil); r.Status != http.StatusNotFound ||
		!strings.Contains(string(r.Body), "User not found") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestShowcaseSelection(t *testing.T) {
	now := time.Now()
	row := func(key, tier string, rank int64, age time.Duration) Unlock {
		u := Unlock{ID: uuid.New(), AchievementKey: key, Tier: tier, UnlockedAt: now.Add(-age)}
		if rank > 0 {
			u.ShowcaseRank = &rank
		}
		return u
	}
	// Newest first, as visibleRows returns them.
	rows := []Unlock{
		row("start.first_holding", "", 0, time.Minute),
		row("discipline.check_in", "bronze", 0, 2*time.Minute),
		row("craft.thesis", "gold", 0, 3*time.Minute),
		row("plan.scenario", "silver", 0, 4*time.Minute),
	}
	got := Showcase(rows, LangEN)
	keys := []string{}
	for _, g := range got {
		keys = append(keys, g.Key)
	}
	if strings.Join(keys, ",") != "craft.thesis,plan.scenario,discipline.check_in" {
		t.Fatalf("default %v", keys)
	}
	rows[0].ShowcaseRank = ptr64(2)
	rows[3].ShowcaseRank = ptr64(1)
	got = Showcase(rows, LangPT)
	if len(got) != 2 || got[0].Key != "plan.scenario" || got[1].Title != "Primeira posição" {
		t.Fatalf("pinned %+v", got)
	}
}

func ptr64(v int64) *int64 { return &v }
