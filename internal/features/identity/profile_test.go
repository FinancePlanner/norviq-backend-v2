package identity

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Ports UserProfileTests (deletion stays on Vapor) and OnboardingTests.

func TestProfileReadAndUpdate(t *testing.T) {
	h := newHarness(t)
	a := h.register("profiler", "profile@example.com")
	id := a.UserID.String()
	want := `{"userProfile":{"id":"` + id + `","email":"profile@example.com","username":"profiler"}}`
	for _, path := range []string{"/v1/users", "/v1/users/" + id, "/v1/users/" + strings.ToLower(id)} {
		if r := h.Do("GET", path, a.Token, nil); r.Status != 200 || string(r.Body) != want {
			t.Fatalf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	other := h.register("someone", "someone@example.com")
	expect(t, h.Do("GET", "/v1/users/"+other.UserID.String(), a.Token, nil), 403, "You are not allowed to access this user profile")
	expect(t, h.Do("GET", "/v1/users/not-a-uuid", a.Token, nil), 400, "Invalid user ID")

	update := map[string]any{"userProfile": map[string]any{
		"id": id, "email": "Profile@Example.com", "bio": "  Investor  ", "username": " NewName ",
		"avatarURL": "https://cdn.example/a.png", "bannerAvatarURL": "https://cdn.example/b.png",
	}}
	r := h.Do("PUT", "/v1/users", a.Token, update)
	if r.Status != 200 || string(r.Body) != `{"userProfile":{"id":"`+id+`","email":"profile@example.com","bio":"Investor","avatarURL":"https://cdn.example/a.png","bannerAvatarURL":"https://cdn.example/b.png","username":"newname"}}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// The bio is PII: encrypted at rest, plaintext column cleared.
	var plain *string
	var enc []byte
	_ = h.Deps.DB.QueryRow(context.Background(), `SELECT bio, bio_encrypted FROM users WHERE id=$1`, a.UserID.Std()).Scan(&plain, &enc)
	if plain != nil || len(enc) == 0 {
		t.Fatalf("bio stored as %v / %d bytes", plain, len(enc))
	}
	if r := h.Do("PUT", "/v1/users/"+id, a.Token, update); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	bad := func(mut func(map[string]any), status int, why string) {
		t.Helper()
		p := map[string]any{"id": id, "email": "profile@example.com"}
		mut(p)
		expect(t, h.Do("PUT", "/v1/users", a.Token, map[string]any{"userProfile": p}), status, why)
	}
	bad(func(p map[string]any) { p["id"] = strings.ToLower(id) }, 400, "Profile ID does not match authenticated user")
	bad(func(p map[string]any) { p["email"] = "moved@example.com" }, 400, "EMAIL_CHANGE_REQUIRES_REAUTH")
	bad(func(p map[string]any) { p["username"] = "someone" }, 409, "Username already registered")
	bad(func(p map[string]any) { p["username"] = "Norviq" }, 400, "That username is reserved.")
	bad(func(p map[string]any) { p["username"] = "a b" }, 400, "Username must be 4-30 characters (letters, numbers, underscore)")

	expect(t, h.Do("PATCH", "/v1/users/username", a.Token, map[string]any{"username": "someone"}), 409, "Username already registered")
	r = h.Do("PATCH", "/v1/users/username", a.Token, map[string]any{"username": "Renamed"})
	if !strings.Contains(string(r.Body), `"username":"renamed"`) {
		t.Fatalf("%s", r.Body)
	}
}

func TestEmailChangeNeedsReauth(t *testing.T) {
	h := newHarness(t)
	a := h.register("mover", "mover@example.com")
	second := h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "mover@example.com", "password": goodPassword})
	var other AuthResponse
	second.JSON(t, &other)
	h.register("taken", "taken@example.com")

	expect(t, h.Do("PATCH", "/v1/users/email", a.Token, map[string]any{"email": "MOVER@example.com"}), 400, "EMAIL_UNCHANGED")
	expect(t, h.Do("PATCH", "/v1/users/email", a.Token, map[string]any{"email": "new@example.com"}), 400, "CURRENT_PASSWORD_REQUIRED")
	expect(t, h.Do("PATCH", "/v1/users/email", a.Token, map[string]any{"email": "new@example.com", "currentPassword": "Wr0ng!pass"}), 400, "INVALID_CURRENT_PASSWORD")
	expect(t, h.Do("PATCH", "/v1/users/email", a.Token, map[string]any{"email": "taken@example.com", "currentPassword": goodPassword}), 409, "Email already registered")

	// Tokens minted in this same second must survive the spare-by-iat rule.
	time.Sleep(1100 * time.Millisecond)
	fresh := h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "mover@example.com", "password": goodPassword})
	var caller AuthResponse
	fresh.JSON(t, &caller)
	r := h.Do("PATCH", "/v1/users/email", caller.Token, map[string]any{"email": "New@Example.com", "currentPassword": goodPassword, "refreshToken": caller.RefreshToken})
	if r.Status != 200 || !strings.Contains(string(r.Body), `"email":"new@example.com"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if u := h.userByEmail("new@example.com"); u == nil || u.EmailVerifiedAt != nil {
		t.Fatal("new address must start unverified")
	}
	expect(t, h.Do("GET", "/v1/auth/me", caller.Token, nil), 200, "")
	expect(t, h.Do("POST", "/v1/auth/refresh", "", map[string]any{"refreshToken": caller.RefreshToken}), 200, "")
	expect(t, h.Do("GET", "/v1/auth/me", a.Token, nil), 401, "")
	expect(t, h.Do("POST", "/v1/auth/refresh", "", map[string]any{"refreshToken": other.RefreshToken}), 401, "Invalid refresh token")

	// OAuth-only accounts have no password to re-enter.
	oauthOnly := h.CreateUser("oauthonly@example.com")
	expect(t, h.Do("PATCH", "/v1/users/email", h.Token(oauthOnly), map[string]any{"email": "x@example.com"}), 409, "REAUTH_REQUIRED")
}

func TestPasswordChange(t *testing.T) {
	h := newHarness(t)
	a := h.register("changer", "changer@example.com")
	time.Sleep(1100 * time.Millisecond)
	fresh := h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "changer@example.com", "password": goodPassword})
	var caller AuthResponse
	fresh.JSON(t, &caller)
	expect(t, h.Do("PATCH", "/v1/users/password", caller.Token, map[string]any{"currentPassword": "Wr0ng!pass", "newPassword": "N3w!Password"}), 400, "INVALID_CURRENT_PASSWORD")
	// The new password must meet the password rules (Vapor took anything).
	expect(t, h.Do("PATCH", "/v1/users/password", caller.Token, map[string]any{"currentPassword": goodPassword, "newPassword": "short"}), 400, "Password must be at least 8 characters long")
	expect(t, h.Do("PATCH", "/v1/users/password", caller.Token, map[string]any{"currentPassword": goodPassword, "newPassword": "alllowercase1!"}), 400, "Password must contain at least one uppercase letter")
	expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "changer@example.com", "password": goodPassword}), 200, "")
	r := h.Do("PATCH", "/v1/users/password", caller.Token, map[string]any{"currentPassword": goodPassword, "newPassword": "N3w!Password", "refreshToken": caller.RefreshToken})
	if r.Status != 200 || string(r.Body) != `{"message":"Password updated successfully","success":true}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	expect(t, h.Do("GET", "/v1/auth/me", caller.Token, nil), 200, "")
	expect(t, h.Do("GET", "/v1/auth/me", a.Token, nil), 401, "")
	expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "changer@example.com", "password": "N3w!Password"}), 200, "")
}

func TestOnboarding(t *testing.T) {
	h := newHarness(t)
	a := h.register("onboard", "onboard@example.com")
	want := `{"addHoldingCompleted":false,"setBudgetCompleted":false,"setGoalCompleted":false}`
	if r := h.Do("GET", "/v1/onboarding", a.Token, nil); r.Status != 200 || string(r.Body) != want {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	expect(t, h.Do("GET", "/v1/onboarding", "", nil), 401, "")
	expect(t, h.Do("PATCH", "/v1/onboarding", a.Token, []byte(`[1]`)), 400, "Expected a JSON object")
	expect(t, h.Do("PATCH", "/v1/onboarding", a.Token, map[string]any{"firstGoalAt": "x", "addHoldingCompleted": true}), 400, "addHoldingCompleted is set by the server, not by clients")
	expect(t, h.Do("PATCH", "/v1/onboarding", a.Token, map[string]any{"funnelStep": "nowhere"}), 400, "Unknown funnel step nowhere")
	expect(t, h.Do("PATCH", "/v1/onboarding", a.Token, map[string]any{"funnel_completed": false}), 400, "funnelCompleted is one-way")
	r := h.Do("PATCH", "/v1/onboarding", a.Token, map[string]any{"funnel_step": "budget", "funnelCompleted": true, "guidedStartDismissed": true})
	var dto onboardingStateDTO
	r.JSON(t, &dto)
	if dto.FunnelStep == nil || *dto.FunnelStep != "budget" || dto.FunnelCompletedAt == nil || dto.GuidedStartDismissedAt == nil {
		t.Fatalf("%s", r.Body)
	}
	first := dto.FunnelCompletedAt.Time
	time.Sleep(1100 * time.Millisecond)
	dto = onboardingStateDTO{}
	h.Do("PATCH", "/v1/onboarding", a.Token, map[string]any{"funnelCompleted": true, "guidedStartDismissed": false}).JSON(t, &dto)
	if !dto.FunnelCompletedAt.Equal(first) || dto.GuidedStartDismissedAt != nil {
		t.Fatalf("completion must keep its first timestamp; dismissal is two-way: %+v", dto)
	}
	// Scoped tokens need settings:write to patch.
	pat := h.Do("POST", "/v1/tokens", a.Token, map[string]any{"name": "ro", "scopes": []string{"settings:read"}})
	var created patCreateResponse
	pat.JSON(t, &created)
	expect(t, h.Do("GET", "/v1/onboarding", created.Token, nil), 200, "")
	expect(t, h.Do("PATCH", "/v1/onboarding", created.Token, map[string]any{"funnelStep": "done"}), 403, "")
	expect(t, h.Do("GET", "/v1/users", created.Token, nil), 200, "")
	expect(t, h.Do("PUT", "/v1/users", created.Token, map[string]any{}), 401, "")
}
