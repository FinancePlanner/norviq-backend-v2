package social_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/features/social"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/users"

	"github.com/google/uuid"
)

// Ported from SocialContactHashTests: the iOS app hashes on device with the
// same vector.
func TestContactHashVector(t *testing.T) {
	h, ok := social.ContactHash("  Ana@Example.com ", "test-pepper")
	if !ok || h != "daee873047f0f98be24af583ad20cf23023f1772bfa853aed2bb53967227737f" {
		t.Fatalf("hash = %q", h)
	}
	if _, ok := social.ContactHash("not-an-email", "p"); ok {
		t.Fatal("non-email hashed")
	}
}

func TestDisabledConfigAnd404(t *testing.T) {
	e := setup(t, social.Feature{}, "SOCIAL_ENABLED", "0")
	a := e.user("soc_off1")
	var cfg social.ConfigDTO
	e.get("/v1/social/config", a, &cfg)
	if cfg.Enabled || cfg.ContactPepper != nil || cfg.Leaderboards {
		t.Fatalf("config = %+v", cfg)
	}
	if s := e.status(http.MethodGet, "/v1/social/friends", a, nil); s != http.StatusNotFound {
		t.Fatalf("friends = %d", s)
	}
	if s := e.status(http.MethodGet, "/v1/gamification/xp", a, nil); s != http.StatusNotFound {
		t.Fatalf("xp = %d", s)
	}
	if s := e.status(http.MethodGet, "/v1/social/config", uuid.Nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("anonymous config = %d", s)
	}
}

func TestConfigFlags(t *testing.T) {
	e := setup(t, social.Feature{}, "GROUPS_ENABLED", "1", "ACHIEVEMENTS_ENABLED", "1", "SOCIAL_ACTIVITY_FEED_ENABLED", "yes")
	a := e.user("soc_cfg1")
	var cfg social.ConfigDTO
	e.get("/v1/social/config", a, &cfg)
	if !cfg.Enabled || !cfg.ContactsDiscovery || cfg.ContactHashVersion == nil || *cfg.ContactPepper != "test-pepper" ||
		!cfg.Groups || !cfg.GroupsPublic || !cfg.Achievements || !cfg.ActivityFeed || !cfg.Leaderboards || cfg.XImport || cfg.FacebookImport {
		t.Fatalf("config = %+v", cfg)
	}
	// /config writes the contact hash.
	var hash *string
	e.scalar(`SELECT email_hash FROM social_settings WHERE user_id = $1`, &hash, a)
	want, _ := social.ContactHash("soc_cfg1@example.com", "test-pepper")
	if hash == nil || *hash != want {
		t.Fatalf("hash = %v", hash)
	}
}

func TestRequestAndAccept(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_req1"), e.user("soc_req2")
	e.exec(`INSERT INTO push_devices (id, user_id, device_token, platform, apns_environment, authorization_status, is_active, last_seen_at, created_at, updated_at, capabilities)
		VALUES ($1, $2, 'tok-b', 'ios', 'sandbox', 'authorized', true, now(), now(), now(), '')`, uuid.New(), b)

	for range 2 { // sending again is a no-op, not a second request
		if r := e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{"userId": up(b)}); r.Status != http.StatusOK {
			t.Fatalf("send: %d %s", r.Status, r.Body)
		}
	}
	if len(e.push.sent) != 1 || e.push.sent[0].Title != "New friend request" || e.push.sent[0].Body != "@soc_req1 wants to be friends on Norviq." {
		t.Fatalf("pushes = %+v", e.push.sent)
	}
	var reqs social.FriendRequests
	e.get("/v1/social/friend-requests", b, &reqs)
	if len(reqs.Incoming) != 1 || reqs.Incoming[0].From.FriendshipStatus != social.StatusIncomingPending || len(reqs.Outgoing) != 0 {
		t.Fatalf("requests = %+v", reqs)
	}
	id := reqs.Incoming[0].ID.String()
	if s := e.status(http.MethodPost, "/v1/social/friend-requests/"+id+"/accept", a, nil); s != http.StatusNotFound {
		t.Fatalf("sender accept = %d", s)
	}
	if s := e.status(http.MethodPost, "/v1/social/friend-requests/"+id+"/accept", b, nil); s != http.StatusNoContent {
		t.Fatalf("accept = %d", s)
	}
	if got := e.friendIDs(a); len(got) != 1 || got[0] != up(b) {
		t.Fatalf("a friends = %v", got)
	}
	if got := e.friendIDs(b); len(got) != 1 || got[0] != up(a) {
		t.Fatalf("b friends = %v", got)
	}
	e.get("/v1/social/friend-requests", b, &reqs)
	if len(reqs.Incoming) != 0 {
		t.Fatalf("still pending: %+v", reqs)
	}
	if s := e.status(http.MethodPost, "/v1/social/friend-requests/nope/accept", b, nil); s != http.StatusBadRequest {
		t.Fatalf("bad id = %d", s)
	}
	if s := e.status(http.MethodDelete, "/v1/social/friends/"+up(b), a, nil); s != http.StatusNoContent || len(e.friendIDs(b)) != 0 {
		t.Fatalf("unfriend = %d", s)
	}
}

func TestDeclineAndCancel(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_dec1"), e.user("soc_dec2")
	var fr social.FriendRequest
	r := e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{"user_id": up(b)})
	r.decode(t, &fr)
	if fr.From.ID.Std() != a || fr.To.FriendshipStatus != social.StatusOutgoingPending {
		t.Fatalf("dto = %+v", fr)
	}
	if s := e.status(http.MethodDelete, "/v1/social/friend-requests/"+fr.ID.String(), b, nil); s != http.StatusNotFound {
		t.Fatalf("recipient cancel = %d", s)
	}
	if s := e.status(http.MethodPost, "/v1/social/friend-requests/"+fr.ID.String()+"/decline", b, nil); s != http.StatusNoContent {
		t.Fatalf("decline = %d", s)
	}
	e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{"userId": up(b)}).decode(t, &fr)
	if s := e.status(http.MethodDelete, "/v1/social/friend-requests/"+fr.ID.String(), a, nil); s != http.StatusNoContent {
		t.Fatalf("cancel = %d", s)
	}
	if r := e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{"userId": up(a)}); r.Status != 400 || r.reason(t) != "You can't add yourself." {
		t.Fatalf("self = %d %s", r.Status, r.Body)
	}
	if r := e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{"userId": up(uuid.New())}); r.Status != 404 || r.reason(t) != "User not found" {
		t.Fatalf("missing = %d %s", r.Status, r.Body)
	}
	if r := e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{}); r.Status != 400 ||
		!strings.HasPrefix(r.reason(t), "No such key 'userId'") {
		t.Fatalf("no body key = %d %s", r.Status, r.Body)
	}
}

func TestReverseRequestAccepts(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_rev1"), e.user("soc_rev2")
	e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{"userId": up(b)})
	if s := e.status(http.MethodPost, "/v1/social/friend-requests", b, map[string]any{"userId": up(a)}); s != http.StatusOK {
		t.Fatalf("reverse = %d", s)
	}
	if got := e.friendIDs(b); len(got) != 1 || got[0] != up(a) {
		t.Fatalf("friends = %v", got)
	}
	if r := e.call(http.MethodPost, "/v1/social/friend-requests", b, map[string]any{"userId": up(a)}); r.Status != http.StatusConflict ||
		r.reason(t) != "You're already friends." {
		t.Fatalf("again = %d %s", r.Status, r.Body)
	}
}

func TestBlockHidesBothWays(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_blk1"), e.user("soc_blk2")
	var link social.InviteLink
	e.call(http.MethodPost, "/v1/social/invites", a, nil).decode(t, &link)
	if link.URL != "https://norviq.test/i/"+link.Code || len(link.Code) != 10 {
		t.Fatalf("link = %+v", link)
	}
	var again social.InviteLink
	e.call(http.MethodPost, "/v1/social/invites", a, nil).decode(t, &again)
	if again.Code != link.Code {
		t.Fatal("invite code changed")
	}
	var preview social.UserSummary
	e.get("/v1/social/invites/"+link.Code, b, &preview)
	if preview.ID.Std() != a {
		t.Fatalf("preview = %+v", preview)
	}
	if s := e.status(http.MethodPost, "/v1/social/invites/"+link.Code+"/redeem", b, nil); s != http.StatusOK {
		t.Fatalf("redeem = %d", s)
	}
	if got := e.friendIDs(a); len(got) != 1 || got[0] != up(b) {
		t.Fatalf("friends = %v", got)
	}
	if s := e.status(http.MethodPost, "/v1/social/blocks/"+up(b), a, nil); s != http.StatusNoContent {
		t.Fatalf("block = %d", s)
	}
	if len(e.friendIDs(a)) != 0 || len(e.friendIDs(b)) != 0 {
		t.Fatal("friendship survived the block")
	}
	for _, p := range []string{"/v1/social/users/" + up(a), "/v1/social/invites/" + link.Code} {
		if s := e.status(http.MethodGet, p, b, nil); s != http.StatusNotFound {
			t.Fatalf("GET %s = %d", p, s)
		}
	}
	var search struct {
		Users []social.UserSummary `json:"users"`
	}
	e.get("/v1/social/users/search?q=soc_blk", b, &search)
	if len(search.Users) != 0 {
		t.Fatalf("search = %+v", search.Users)
	}
	var blocked struct {
		Users []social.UserSummary `json:"users"`
	}
	e.get("/v1/social/blocks", a, &blocked)
	if len(blocked.Users) != 1 || blocked.Users[0].ID.Std() != b || blocked.Users[0].FriendshipStatus != social.StatusBlocked {
		t.Fatalf("blocks = %+v", blocked)
	}
	if s := e.status(http.MethodDelete, "/v1/social/blocks/"+up(b), a, nil); s != http.StatusNoContent {
		t.Fatalf("unblock = %d", s)
	}
	if s := e.status(http.MethodGet, "/v1/social/users/"+up(a), b, nil); s != http.StatusOK {
		t.Fatalf("profile after unblock = %d", s)
	}
	if r := e.call(http.MethodPost, "/v1/social/blocks/"+up(a), a, nil); r.Status != 400 || r.reason(t) != "You can't block yourself." {
		t.Fatalf("self block = %d", r.Status)
	}
	if s := e.status(http.MethodGet, "/v1/social/invites/x!", b, nil); s != http.StatusNotFound {
		t.Fatalf("bad code = %d", s)
	}
}

func TestOwnInviteAndRedemptionCredit(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_own1"), e.user("soc_own2")
	var link social.InviteLink
	e.call(http.MethodPost, "/v1/social/invites", a, nil).decode(t, &link)
	if r := e.call(http.MethodPost, "/v1/social/invites/"+link.Code+"/redeem", a, nil); r.Status != 400 || r.reason(t) != "That's your own invite link." {
		t.Fatalf("own = %d %s", r.Status, r.Body)
	}
	var res struct {
		Inviter social.UserSummary `json:"inviter"`
	}
	e.call(http.MethodPost, "/v1/social/invites/"+link.Code+"/redeem", b, nil).decode(t, &res)
	if res.Inviter.ID.Std() != a || res.Inviter.FriendshipStatus != social.StatusFriends {
		t.Fatalf("redeem = %+v", res)
	}
	var n int
	e.scalar(`SELECT count(*) FROM social_invite_redemptions WHERE inviter_id = $1 AND invitee_id = $2`, &n, a, b)
	if n != 1 {
		t.Fatalf("redemptions = %d", n)
	}
	// Already friends: no second redemption (and no error). An old account
	// earns the inviter nothing.
	c := e.user("soc_own3")
	e.exec(`UPDATE users SET created_at = now() - interval '30 days' WHERE id = $1`, c)
	e.call(http.MethodPost, "/v1/social/invites/"+link.Code+"/redeem", b, nil)
	e.call(http.MethodPost, "/v1/social/invites/"+link.Code+"/redeem", c, nil)
	e.scalar(`SELECT count(*) FROM social_invite_redemptions`, &n)
	if n != 1 {
		t.Fatalf("redemptions = %d", n)
	}
	if r := e.call(http.MethodGet, "/v1/social/invites/abcdef", b, nil); r.Status != 404 || r.reason(t) != "Invite not found" {
		t.Fatalf("unknown = %d %s", r.Status, r.Body)
	}
}

func TestSearchVisibility(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_vis1"), e.user("soc_vis2")
	var found struct {
		Users []social.UserSummary `json:"users"`
	}
	e.get("/v1/social/users/search?q=SOC_VIS", a, &found)
	if len(found.Users) != 1 || found.Users[0].ID.Std() != b {
		t.Fatalf("found = %+v", found)
	}
	e.setPrivacy(b, func(p map[string]any) { p["searchVisibility"] = "nobody" })
	e.get("/v1/social/users/search?q=soc_vis", a, &found)
	if len(found.Users) != 0 {
		t.Fatalf("nobody still found: %+v", found)
	}
	// friends_of_friends: visible once a shares a friend with b.
	e.setPrivacy(b, func(p map[string]any) { p["searchVisibility"] = "friends_of_friends" })
	e.get("/v1/social/users/search?q=soc_vis", a, &found)
	if len(found.Users) != 0 {
		t.Fatal("friends_of_friends visible without a mutual friend")
	}
	m := e.user("soc_mut")
	e.befriend(a, m)
	e.befriend(b, m)
	e.get("/v1/social/users/search?q=soc_vis", a, &found)
	if len(found.Users) != 1 || found.Users[0].MutualFriendCount == nil || *found.Users[0].MutualFriendCount != 1 {
		t.Fatalf("fof = %+v", found)
	}
	e.get("/v1/social/users/search?q=s", a, &found)
	if len(found.Users) != 0 {
		t.Fatal("one-letter query searched")
	}
}

func TestPrivacyDefaultsAndPartialPut(t *testing.T) {
	e := setup(t, social.Feature{})
	a := e.user("soc_prv1")
	var p social.PrivacySettings
	e.get("/v1/social/privacy", a, &p)
	if p != social.DefaultPrivacy {
		t.Fatalf("defaults = %+v", p)
	}
	e.setPrivacy(a, func(m map[string]any) { m["discoverableByFacebook"] = false; m["showAchievements"] = false })
	// An old client's PUT without the two late fields keeps them.
	legacy := defaultPrivacy()
	delete(legacy, "discoverableByFacebook")
	delete(legacy, "showAchievements")
	legacy["showReturnPercent"] = true
	e.call(http.MethodPut, "/v1/social/privacy", a, legacy).decode(t, &p)
	if !p.ShowReturnPercent || p.DiscoverableByFacebook || p.ShowAchievements {
		t.Fatalf("partial = %+v", p)
	}
	bad := defaultPrivacy()
	bad["searchVisibility"] = "aliens"
	if r := e.call(http.MethodPut, "/v1/social/privacy", a, bad); r.Status != 400 ||
		r.reason(t) != "Data corrupted at path 'searchVisibility'. Cannot initialize SocialSearchVisibility from invalid String value aliens" {
		t.Fatalf("bad enum = %d %s", r.Status, r.Body)
	}
	missing := defaultPrivacy()
	delete(missing, "showXP")
	if r := e.call(http.MethodPut, "/v1/social/privacy", a, missing); r.Status != 400 {
		t.Fatalf("missing = %d", r.Status)
	}
}

func TestContactMatching(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_con1"), e.user("soc_con2")
	var cfg social.ConfigDTO
	e.get("/v1/social/config", b, &cfg)
	hash, _ := social.ContactHash("SOC_con2@example.com", *cfg.ContactPepper)
	body := map[string]any{"hashVersion": 1, "items": []map[string]any{
		{"hash": hash, "kind": "email"}, {"hash": strings.Repeat("a", 64), "kind": "email"}, {"hash": hash, "kind": "phone"},
	}}
	var res struct {
		Matches []struct {
			Hash string             `json:"hash"`
			User social.UserSummary `json:"user"`
		} `json:"matches"`
	}
	e.call(http.MethodPost, "/v1/social/discovery/contacts/match", a, body).decode(t, &res)
	if len(res.Matches) != 1 || res.Matches[0].User.ID.Std() != b || res.Matches[0].Hash != hash {
		t.Fatalf("matches = %+v", res)
	}
	e.setPrivacy(b, func(p map[string]any) { p["discoverableByContacts"] = false })
	e.call(http.MethodPost, "/v1/social/discovery/contacts/match", a, body).decode(t, &res)
	if len(res.Matches) != 0 {
		t.Fatalf("opted out still matched: %+v", res)
	}
	if r := e.call(http.MethodPost, "/v1/social/discovery/contacts/match", a, map[string]any{"hashVersion": 2, "items": []any{}}); r.Status != 422 {
		t.Fatalf("version = %d", r.Status)
	}
	items := make([]map[string]any, 1001)
	for i := range items {
		items[i] = map[string]any{"hash": "x", "kind": "email"}
	}
	if r := e.call(http.MethodPost, "/v1/social/discovery/contacts/match", a, map[string]any{"hashVersion": 1, "items": items}); r.Status != 413 {
		t.Fatalf("too many = %d", r.Status)
	}
	if r := e.call(http.MethodPost, "/v1/social/discovery/contacts/match", a, map[string]any{"hashVersion": 1, "items": []map[string]any{{"hash": "x", "kind": "fax"}}}); r.Status != 400 ||
		r.reason(t) != "Data corrupted at path 'items.Index 0.kind'. Cannot initialize SocialContactKind from invalid String value fax" {
		t.Fatalf("bad kind = %d %s", r.Status, r.Body)
	}
	t.Setenv("SOCIAL_CONTACT_PEPPER", "")
	if r := e.call(http.MethodPost, "/v1/social/discovery/contacts/match", a, body); r.Status != 404 || r.reason(t) != "Contact matching is not available." {
		t.Fatalf("no pepper = %d", r.Status)
	}
}

func TestSuspendedUserIsHiddenAndLockedOut(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_mod2"), e.user("soc_mod3")
	e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{"userId": up(b)})
	e.call(http.MethodGet, "/v1/social/privacy", b, nil)
	e.exec(`UPDATE social_settings SET suspended_at = now() WHERE user_id = $1`, b)
	if r := e.call(http.MethodGet, "/v1/social/friends", b, nil); r.Status != 403 || r.reason(t) != "Your access to friends features has been suspended." {
		t.Fatalf("suspended = %d %s", r.Status, r.Body)
	}
	if s := e.status(http.MethodGet, "/v1/social/users/"+up(b), a, nil); s != 404 {
		t.Fatalf("profile = %d", s)
	}
	var reqs social.FriendRequests
	e.get("/v1/social/friend-requests", a, &reqs)
	if len(reqs.Outgoing) != 0 {
		t.Fatalf("outgoing = %+v", reqs)
	}
	// /config is outside the middleware.
	if s := e.status(http.MethodGet, "/v1/social/config", b, nil); s != 200 {
		t.Fatalf("config = %d", s)
	}
}

func TestSystemAccountCannotBeBlocked(t *testing.T) {
	e := setup(t, social.Feature{})
	a := e.user("soc_mod5")
	sys := e.app.CreateUser(users.SystemEmail)
	if r := e.call(http.MethodPost, "/v1/social/blocks/"+up(sys), a, nil); r.Status != 400 || r.reason(t) != "That account can't be blocked." {
		t.Fatalf("block system = %d %s", r.Status, r.Body)
	}
	if r := e.call(http.MethodPost, "/v1/social/blocks/"+up(uuid.New()), a, nil); r.Status != 404 {
		t.Fatalf("missing = %d", r.Status)
	}
}

func TestReports(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_rep1"), e.user("soc_rep2")
	r := e.call(http.MethodPost, "/v1/social/reports", a, map[string]any{"targetType": "user", "targetId": up(b), "reason": "spam", "note": "  "})
	if r.Status != http.StatusAccepted || len(r.Body) != 0 {
		t.Fatalf("report = %d %q", r.Status, r.Body)
	}
	var note *string
	var status string
	e.scalar(`SELECT note FROM social_reports`, &note)
	e.scalar(`SELECT status FROM social_reports`, &status)
	if note != nil || status != "open" {
		t.Fatalf("stored note=%v status=%s", note, status)
	}
	want := "🚩 New social report: spam on user @soc_rep2 (" + up(b) + "). 1 open. Review within 24 hours: GET /v1/admin/social/reports"
	if len(e.discord.msgs) != 1 || e.discord.msgs[0] != want {
		t.Fatalf("discord = %q", e.discord.msgs)
	}
	for body, reason := range map[string]string{
		`{"targetType":"user","targetId":"  ","reason":"spam"}`:                                           "A report needs a target.",
		`{"targetType":"user","targetId":"x","reason":"spam","note":"` + strings.Repeat("n", 1001) + `"}`: "Keep the details under 1,000 characters.",
		`{"targetType":"group","targetId":"x","reason":"spam"}`:                                          "Data corrupted at path 'targetType'. Cannot initialize SocialReportTargetType from invalid String value group",
	} {
		if r := e.call(http.MethodPost, "/v1/social/reports", a, body); r.Status != 400 || r.reason(t) != reason {
			t.Fatalf("%s: %d %s", body, r.Status, r.Body)
		}
	}
}

func TestProfileStatsAndPrivacy(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_pro1"), e.user("soc_pro2")
	e.call(http.MethodPost, "/v1/gamification/check-in", b, nil)
	var p struct {
		User       social.UserSummary `json:"user"`
		StreakDays *int               `json:"streakDays"`
		XPLevel    *int               `json:"xpLevel"`
		BadgeCount *int               `json:"badgeCount"`
		JoinedAt   *string            `json:"joinedAt"`
		Showcase   *[]any             `json:"showcase"`
	}
	e.get("/v1/social/users/"+up(b), a, &p)
	if p.StreakDays == nil || *p.StreakDays != 1 || p.XPLevel == nil || *p.XPLevel != 1 || p.JoinedAt == nil || p.BadgeCount != nil || p.Showcase != nil {
		t.Fatalf("profile = %+v", p)
	}
	e.setPrivacy(b, func(m map[string]any) { m["showStreaks"] = false; m["showXP"] = false })
	p.StreakDays, p.XPLevel = nil, nil
	e.get("/v1/social/users/"+up(b), a, &p)
	if p.StreakDays != nil || p.XPLevel != nil {
		t.Fatalf("hidden stats shown: %+v", p)
	}
	// People always see their own.
	e.get("/v1/social/users/"+up(b), b, &p)
	if p.StreakDays == nil || p.XPLevel == nil || p.User.MutualFriendCount != nil {
		t.Fatalf("self = %+v", p)
	}
	if s := e.status(http.MethodGet, "/v1/social/users/not-a-uuid", a, nil); s != 400 {
		t.Fatalf("bad id = %d", s)
	}
}

func TestProfileShowcase(t *testing.T) {
	e := setup(t, social.Feature{}, "ACHIEVEMENTS_ENABLED", "1")
	a, b, c := e.user("soc_sc1"), e.user("soc_sc2"), e.user("soc_sc3")
	e.befriend(a, b)
	ins := func(key, tier string, ago int, rank *int) {
		e.exec(`INSERT INTO user_achievements (id, user_id, achievement_key, tier, unlocked_at, source, showcase_rank, created_at)
			VALUES ($1, $2, $3, $4, now() - make_interval(hours => $5), 'live', $6, now())`, uuid.New(), b, key, tier, ago, rank)
	}
	ins("craft.thesis", "bronze", 1, nil)
	ins("craft.target_hit", "gold", 5, nil)
	ins("social.friends", "silver", 2, nil)
	ins("start.first_goal", "", 0, nil)
	ins("perf.comeback", "", 3, nil)          // performance: hidden without return %
	ins("legacy.investor", "gold", 4, nil)    // pre-engine badge: never shown
	ins("unknown.key", "gold", 4, nil)        // not in the catalogue
	type entry struct{ Key, Tier, Title, IconKey string }
	var p struct {
		BadgeCount *int     `json:"badgeCount"`
		Showcase   *[]entry `json:"showcase"`
	}
	e.get("/v1/social/users/"+up(b), a, &p)
	if p.BadgeCount == nil || *p.BadgeCount != 4 || p.Showcase == nil {
		t.Fatalf("profile = %+v", p)
	}
	keys := []string{}
	for _, s := range *p.Showcase {
		keys = append(keys, s.Key)
	}
	if strings.Join(keys, ",") != "craft.target_hit,social.friends,craft.thesis" || (*p.Showcase)[0].Title != "On Target" {
		t.Fatalf("showcase = %+v", *p.Showcase)
	}
	two, one := 2, 1
	e.exec(`UPDATE user_achievements SET showcase_rank = $1 WHERE achievement_key = 'start.first_goal'`, one)
	e.exec(`UPDATE user_achievements SET showcase_rank = $1 WHERE achievement_key = 'craft.thesis'`, two)
	e.get("/v1/social/users/"+up(b), a, &p, "Accept-Language", "pt-PT,pt;q=0.9")
	if len(*p.Showcase) != 2 || (*p.Showcase)[0].Key != "start.first_goal" || (*p.Showcase)[1].Title != "Autor de teses" {
		t.Fatalf("pinned = %+v", *p.Showcase)
	}
	// Performance shows once the owner shares return %.
	e.setPrivacy(b, func(m map[string]any) { m["showReturnPercent"] = true })
	e.get("/v1/social/users/"+up(b), a, &p)
	if *p.BadgeCount != 5 {
		t.Fatalf("with perf = %d", *p.BadgeCount)
	}
	// Non-friends and sharing off see nothing.
	p.BadgeCount, p.Showcase = nil, nil
	e.get("/v1/social/users/"+up(b), c, &p)
	if p.BadgeCount != nil || p.Showcase != nil {
		t.Fatalf("stranger = %+v", p)
	}
	e.setPrivacy(b, func(m map[string]any) { m["showAchievements"] = false })
	e.get("/v1/social/users/"+up(b), a, &p)
	if p.BadgeCount != nil {
		t.Fatalf("sharing off = %+v", p)
	}
	// The owner sees their own (an empty showcase is still []).
	d := e.user("soc_sc4")
	r := e.call(http.MethodGet, "/v1/social/users/"+up(d), d, nil)
	if !strings.Contains(string(r.Body), `"badgeCount":0`) || !strings.Contains(string(r.Body), `"showcase":[]`) {
		t.Fatalf("own empty = %s", r.Body)
	}
	t.Setenv("ACHIEVEMENTS_ENABLED", "0")
	r = e.call(http.MethodGet, "/v1/social/users/"+up(d), d, nil)
	if strings.Contains(string(r.Body), "badgeCount") || strings.Contains(string(r.Body), "showcase") {
		t.Fatalf("flag off = %s", r.Body)
	}
}

func TestPushNotificationKinds(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b := e.user("soc_psh1"), e.user("soc_psh2")
	e.exec(`INSERT INTO push_devices (id, user_id, device_token, platform, apns_environment, authorization_status, is_active, last_seen_at, created_at, updated_at, capabilities)
		VALUES ($1, $2, 'tok-a', 'ios', 'sandbox', 'authorized', true, now(), now(), now(), '')`, uuid.New(), a)
	e.call(http.MethodPost, "/v1/social/friend-requests", a, map[string]any{"userId": up(b)})
	e.call(http.MethodPost, "/v1/social/friend-requests", b, map[string]any{"userId": up(a)})
	if len(e.push.sent) != 1 {
		t.Fatalf("pushes = %+v", e.push.sent)
	}
	n := e.push.sent[0]
	if n.Category != push.SocialFriendAccepted || n.Title != "You're now friends" || n.Body != "@soc_psh2 is now your friend on Norviq." {
		t.Fatalf("push = %+v", n)
	}
	_ = context.Background
}
