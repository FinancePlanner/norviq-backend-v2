package admin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/admin"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type env struct {
	app        *testutil.App
	admin      uuid.UUID
	adminToken string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	adminID := uuid.New()
	app := testutil.NewApp(t, []server.Feature{admin.Feature{}}, func(d *server.Deps) {
		d.Admin.IDs[adminID] = struct{}{}
	})
	exec(t, app, `INSERT INTO users (id, email, password_hash, created_at, is_verified, username) VALUES ($1, 'admin@example.com', 'x', now(), true, 'boss')`, adminID)
	return &env{app: app, admin: adminID, adminToken: app.Token(adminID)}
}

func exec(t *testing.T, app *testutil.App, sql string, args ...any) {
	t.Helper()
	if _, err := app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) user(t *testing.T, username string) uuid.UUID {
	t.Helper()
	id := e.app.CreateUser(username + "@example.com")
	exec(t, e.app, `UPDATE users SET username = $2 WHERE id = $1`, id, username)
	return id
}

func reason(t *testing.T, r *testutil.Response) string {
	t.Helper()
	var body struct {
		Reason string `json:"reason"`
	}
	r.JSON(t, &body)
	return body.Reason
}

func TestAdminGate(t *testing.T) {
	e := newEnv(t)
	member := e.user(t, "member")
	for _, path := range []string{"/v1/admin/community/sanctions", "/v1/admin/community/reports", "/v1/admin/social/reports"} {
		if r := e.app.Do(http.MethodGet, path, "", nil); r.Status != http.StatusUnauthorized {
			t.Fatalf("%s anonymous: %d", path, r.Status)
		}
		r := e.app.Do(http.MethodGet, path, e.app.Token(member), nil)
		if r.Status != http.StatusForbidden || reason(t, r) != "Admin access required." {
			t.Fatalf("%s member: %d %s", path, r.Status, r.Body)
		}
		if r := e.app.Do(http.MethodGet, path, e.adminToken, nil); r.Status != http.StatusOK {
			t.Fatalf("%s admin: %d %s", path, r.Status, r.Body)
		}
	}
}

func TestAdminFailsClosed(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{admin.Feature{}})
	id := app.CreateUser("someone@example.com")
	r := app.Do(http.MethodGet, "/v1/admin/community/sanctions", app.Token(id), nil)
	if r.Status != http.StatusForbidden || reason(t, r) != "Admin access is disabled (no admin user ids configured)." {
		t.Fatalf("got %d %s", r.Status, r.Body)
	}
}

func TestSanctions(t *testing.T) {
	e := newEnv(t)
	target := e.user(t, "troll")

	bad := []struct {
		body   map[string]any
		status int
		reason string
	}{
		{map[string]any{"username": "norviq", "kind": "mute", "reason": "x"}, 400, "That account can't be sanctioned."},
		{map[string]any{"username": "nobody", "kind": "mute", "reason": "x"}, 404, "User not found"},
		{map[string]any{"username": "boss", "kind": "ban", "reason": "x"}, 400, "Admins can't be muted or banned."},
		{map[string]any{"username": "troll", "kind": "ban", "reason": "   "}, 400, "Give a reason of up to 500 characters."},
		{map[string]any{"username": "troll", "kind": "ban", "reason": strings.Repeat("a", 501)}, 400, "Give a reason of up to 500 characters."},
		{map[string]any{"username": "troll", "kind": "ban", "reason": "x", "durationHours": 0}, 400, "Duration must be between 1 hour and a year, or empty for indefinite."},
		{map[string]any{"username": "troll", "kind": "ban", "reason": "x", "durationHours": 8761}, 400, "Duration must be between 1 hour and a year, or empty for indefinite."},
	}
	for _, b := range bad {
		r := e.app.Do(http.MethodPost, "/v1/admin/community/sanctions", e.adminToken, b.body)
		if r.Status != b.status || reason(t, r) != b.reason {
			t.Fatalf("%v: got %d %s", b.body, r.Status, r.Body)
		}
	}
	if r := e.app.Do(http.MethodPost, "/v1/admin/community/sanctions", e.adminToken, map[string]any{"username": "troll", "kind": "slap", "reason": "x"}); r.Status != 400 {
		t.Fatalf("bad kind: %d", r.Status)
	}

	r := e.app.Do(http.MethodPost, "/v1/admin/community/sanctions", e.adminToken, map[string]any{"username": " @troll ", "kind": "mute", "reason": " spam ", "durationHours": 24})
	if r.Status != http.StatusOK {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	var first struct {
		ID        string  `json:"id"`
		Username  string  `json:"username"`
		Kind      string  `json:"kind"`
		Reason    string  `json:"reason"`
		ExpiresAt *string `json:"expiresAt"`
		RevokedAt *string `json:"revokedAt"`
	}
	r.JSON(t, &first)
	if first.Username != "troll" || first.Kind != "mute" || first.Reason != "spam" || first.ExpiresAt == nil || first.RevokedAt != nil || first.ID != strings.ToUpper(first.ID) {
		t.Fatalf("create body: %s", r.Body)
	}
	// A second mute replaces the first.
	r = e.app.Do(http.MethodPost, "/v1/admin/community/sanctions", e.adminToken, map[string]any{"username": "troll", "kind": "mute", "reason": "again"})
	if r.Status != http.StatusOK {
		t.Fatalf("second: %d", r.Status)
	}
	var open int
	if err := e.app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM community_sanctions WHERE user_id = $1 AND revoked_at IS NULL`, target).Scan(&open); err != nil || open != 1 {
		t.Fatalf("open sanctions = %d, %v", open, err)
	}

	r = e.app.Do(http.MethodGet, "/v1/admin/community/sanctions", e.adminToken, nil)
	var list struct {
		Items []struct {
			ID        string  `json:"id"`
			Reason    string  `json:"reason"`
			RevokedAt *string `json:"revokedAt"`
		} `json:"items"`
	}
	r.JSON(t, &list)
	if len(list.Items) != 2 {
		t.Fatalf("list: %s", r.Body)
	}

	if r := e.app.Do(http.MethodDelete, "/v1/admin/community/sanctions/not-a-uuid", e.adminToken, nil); r.Status != 404 || reason(t, r) != "Sanction not found" {
		t.Fatalf("revoke bad id: %d %s", r.Status, r.Body)
	}
	if r := e.app.Do(http.MethodDelete, "/v1/admin/community/sanctions/"+first.ID, e.adminToken, nil); r.Status != http.StatusNoContent {
		t.Fatalf("revoke: %d", r.Status)
	}
}

func TestDeleteBoard(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	exec(t, e.app, `INSERT INTO boards (id, slug, name, description, creator_id, post_count, last_activity_at, created_at) VALUES ($1, 'daily', 'Daily', 'd', NULL, 0, $2, $2), ($3, 'stocks', 'Stocks', 's', NULL, 0, $2, $2)`, uuid.New(), now, uuid.New())

	if r := e.app.Do(http.MethodDelete, "/v1/admin/boards/daily", e.adminToken, nil); r.Status != 400 || reason(t, r) != "The Daily board stays up." {
		t.Fatalf("daily: %d %s", r.Status, r.Body)
	}
	if r := e.app.Do(http.MethodDelete, "/v1/admin/boards/STOCKS", e.adminToken, nil); r.Status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	if r := e.app.Do(http.MethodDelete, "/v1/admin/boards/stocks", e.adminToken, nil); r.Status != 404 || reason(t, r) != "Board not found" {
		t.Fatalf("again: %d %s", r.Status, r.Body)
	}
	var deleted bool
	_ = e.app.Deps.DB.QueryRow(context.Background(), `SELECT deleted_at IS NOT NULL FROM boards WHERE slug = 'stocks'`).Scan(&deleted)
	if !deleted {
		t.Fatal("board not soft-deleted")
	}
}

func TestCommunityReports(t *testing.T) {
	e := newEnv(t)
	author := e.user(t, "author")
	reporter := e.user(t, "reporter")
	now := time.Now()
	boardID, postID, commentID := uuid.New(), uuid.New(), uuid.New()
	groupID, channelID, messageID := uuid.New(), uuid.New(), uuid.New()
	exec(t, e.app, `INSERT INTO boards (id, slug, name, description, post_count, last_activity_at, created_at) VALUES ($1, 'b', 'B', 'b', 0, $2, $2)`, boardID, now)
	exec(t, e.app, `INSERT INTO board_posts (id, board_id, author_id, kind, title, tags, score, comment_count, participant_count, view_count, last_activity_at, created_at, deleted_at)
		VALUES ($1, $2, $3, 'text', 'A title', '{}', 0, 0, 0, 0, $4, $4, $4)`, postID, boardID, author, now)
	exec(t, e.app, `INSERT INTO board_comments (id, post_id, author_id, body, depth, created_at) VALUES ($1, $2, $3, 'a comment', 0, $4)`, commentID, postID, author, now)
	exec(t, e.app, `INSERT INTO groups (id, name, description, topic, visibility, owner_id, member_count, created_at) VALUES ($1, 'Grp', 'd', 'general', 'private', $2, 1, $3)`, groupID, author, now)
	exec(t, e.app, `INSERT INTO group_channels (id, group_id, name, kind, position, last_seq, created_at) VALUES ($1, $2, 'general', 'text', 0, 1, $3)`, channelID, groupID, now)
	exec(t, e.app, `INSERT INTO group_messages (id, channel_id, group_id, author_id, seq, client_message_id, body, created_at) VALUES ($1, $2, $3, $4, 1, 'c1', 'hello group', $5)`, messageID, channelID, groupID, author, now)
	insertReport := func(target, id, reason string, at time.Time) uuid.UUID {
		rid := uuid.New()
		exec(t, e.app, `INSERT INTO social_reports (id, reporter_id, target_type, target_id, reason, status, created_at) VALUES ($1, $2, $3, $4, $5, 'open', $6)`, rid, reporter, target, id, reason, at)
		return rid
	}
	postReport := insertReport("board_post", postID.String(), "spam", now.Add(-4*time.Minute))
	insertReport("board_comment", strings.ToUpper(commentID.String()), "weird", now.Add(-3*time.Minute))
	insertReport("group_message", messageID.String(), "hate", now.Add(-2*time.Minute))
	insertReport("board_post", uuid.NewString(), "scam", now.Add(-time.Minute))
	insertReport("user", author.String(), "spam", now)

	r := e.app.Do(http.MethodGet, "/v1/admin/community/reports", e.adminToken, nil)
	var list struct {
		Items []map[string]any `json:"items"`
	}
	r.JSON(t, &list)
	if len(list.Items) != 4 {
		t.Fatalf("items: %s", r.Body)
	}
	gone, msg, comment, post := list.Items[0], list.Items[1], list.Items[2], list.Items[3]
	if gone["excerpt"] != "(gone)" || gone["reason"] != "scam" || gone["postId"] != nil {
		t.Fatalf("gone: %v", gone)
	}
	if msg["excerpt"] != "hello group" || msg["groupName"] != "Grp" || msg["targetAuthorUsername"] != "author" || msg["groupMessageId"] != strings.ToUpper(messageID.String()) {
		t.Fatalf("message: %v", msg)
	}
	if comment["excerpt"] != "a comment" || comment["reason"] != "other" || comment["boardSlug"] != "b" || comment["commentId"] == nil {
		t.Fatalf("comment: %v", comment)
	}
	if post["excerpt"] != "A title" || post["reporterUsername"] != "reporter" || post["targetAuthorUsername"] != "author" {
		t.Fatalf("post: %v", post)
	}

	if r := e.app.Do(http.MethodPost, "/v1/admin/community/reports/"+postReport.String()+"/resolve", e.adminToken, nil); r.Status != http.StatusNoContent {
		t.Fatalf("resolve: %d %s", r.Status, r.Body)
	}
	var status, resolution string
	_ = e.app.Deps.DB.QueryRow(context.Background(), `SELECT status, resolution FROM social_reports WHERE id = $1`, postReport).Scan(&status, &resolution)
	if status != "resolved" || resolution != "dismiss" {
		t.Fatalf("resolved row: %s %s", status, resolution)
	}
	if r := e.app.Do(http.MethodPost, "/v1/admin/community/reports/"+uuid.NewString()+"/resolve", e.adminToken, nil); r.Status != 404 || reason(t, r) != "Report not found" {
		t.Fatalf("missing: %d %s", r.Status, r.Body)
	}
}

func TestSocialModeration(t *testing.T) {
	e := newEnv(t)
	bad := e.user(t, "bad")
	friend := e.user(t, "friend")
	reporter := e.user(t, "snitch")
	now := time.Now()
	exec(t, e.app, `INSERT INTO social_friendships (id, user_id, friend_id, created_at) VALUES ($1, $2, $3, $5), ($4, $3, $2, $5)`, uuid.New(), bad, friend, uuid.New(), now)
	exec(t, e.app, `INSERT INTO social_friend_requests (id, from_user_id, to_user_id, created_at) VALUES ($1, $2, $3, $4)`, uuid.New(), reporter, bad, now)
	userReport, msgReport := uuid.New(), uuid.New()
	exec(t, e.app, `INSERT INTO social_reports (id, reporter_id, target_type, target_id, reason, status, created_at) VALUES ($1, $2, 'user', $3, 'spam', 'open', $5), ($4, $2, 'message', 'm-1', 'hate', 'open', $6)`,
		userReport, reporter, bad.String(), msgReport, now.Add(-time.Hour), now)

	r := e.app.Do(http.MethodGet, "/v1/admin/social/reports", e.adminToken, nil)
	var list struct {
		Reports []map[string]any `json:"reports"`
	}
	r.JSON(t, &list)
	if len(list.Reports) != 2 || list.Reports[0]["targetUsername"] != "bad" || list.Reports[1]["targetUsername"] != nil {
		t.Fatalf("reports: %s", r.Body)
	}

	r = e.app.Do(http.MethodPost, "/v1/admin/social/reports/"+msgReport.String()+"/resolve", e.adminToken, map[string]any{"action": "suspend_user"})
	if r.Status != http.StatusUnprocessableEntity || reason(t, r) != "Only user reports can suspend a user." {
		t.Fatalf("suspend message: %d %s", r.Status, r.Body)
	}
	r = e.app.Do(http.MethodPost, "/v1/admin/social/reports/"+userReport.String()+"/resolve", e.adminToken, map[string]any{"action": "suspend_user", "note": "  done  "})
	if r.Status != http.StatusNoContent {
		t.Fatalf("resolve: %d %s", r.Status, r.Body)
	}
	var suspended bool
	var friendships, requests int
	ctx := context.Background()
	_ = e.app.Deps.DB.QueryRow(ctx, `SELECT suspended_at IS NOT NULL FROM social_settings WHERE user_id = $1`, bad).Scan(&suspended)
	_ = e.app.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM social_friendships WHERE user_id = $1 OR friend_id = $1`, bad).Scan(&friendships)
	_ = e.app.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM social_friend_requests WHERE to_user_id = $1`, bad).Scan(&requests)
	if !suspended || friendships != 0 || requests != 0 {
		t.Fatalf("suspended=%v friendships=%d requests=%d", suspended, friendships, requests)
	}
	var note string
	_ = e.app.Deps.DB.QueryRow(ctx, `SELECT resolution_note FROM social_reports WHERE id = $1`, userReport).Scan(&note)
	if note != "done" {
		t.Fatalf("note %q", note)
	}

	r = e.app.Do(http.MethodGet, "/v1/admin/social/reports", e.adminToken, nil)
	r.JSON(t, &list)
	if len(list.Reports) != 1 {
		t.Fatalf("open after resolve: %s", r.Body)
	}
	r = e.app.Do(http.MethodGet, "/v1/admin/social/reports?status=all", e.adminToken, nil)
	r.JSON(t, &list)
	if len(list.Reports) != 2 {
		t.Fatalf("all: %s", r.Body)
	}

	if r := e.app.Do(http.MethodDelete, "/v1/admin/social/users/"+bad.String()+"/suspend", e.adminToken, nil); r.Status != http.StatusNoContent {
		t.Fatalf("unsuspend: %d", r.Status)
	}
	_ = e.app.Deps.DB.QueryRow(ctx, `SELECT suspended_at IS NOT NULL FROM social_settings WHERE user_id = $1`, bad).Scan(&suspended)
	if suspended {
		t.Fatal("still suspended")
	}
	if r := e.app.Do(http.MethodPost, "/v1/admin/social/users/nope/suspend", e.adminToken, nil); r.Status != 400 || reason(t, r) != "Invalid userId" {
		t.Fatalf("bad id: %d %s", r.Status, r.Body)
	}
	if r := e.app.Do(http.MethodPost, "/v1/admin/social/users/"+uuid.NewString()+"/suspend", e.adminToken, nil); r.Status != 404 || reason(t, r) != "User not found" {
		t.Fatalf("missing user: %d %s", r.Status, r.Body)
	}
	system := e.app.CreateUser("daily-board@system.norviq.invalid")
	if r := e.app.Do(http.MethodPost, "/v1/admin/social/users/"+system.String()+"/suspend", e.adminToken, nil); r.Status != 400 || reason(t, r) != "That account can't be suspended." {
		t.Fatalf("system: %d %s", r.Status, r.Body)
	}
	// Unsuspending a user with no settings row creates one with defaults.
	if r := e.app.Do(http.MethodDelete, "/v1/admin/social/users/"+friend.String()+"/suspend", e.adminToken, nil); r.Status != http.StatusNoContent {
		t.Fatalf("unsuspend fresh: %d", r.Status)
	}
	var rows int
	_ = e.app.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM social_settings WHERE user_id = $1 AND suspended_at IS NULL`, friend).Scan(&rows)
	if rows != 1 {
		t.Fatalf("settings rows %d", rows)
	}
}
