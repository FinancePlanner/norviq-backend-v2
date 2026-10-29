package identity

import (
	"context"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/users"
)

// Ports AuthTests: registration, login, MFA, refresh and password reset.

func TestRegistration(t *testing.T) {
	h := newHarness(t)
	base := map[string]any{
		"username": "newuser", "email": "  New@Example.COM ", "password": goodPassword,
		"confirmPassword": goodPassword, "dateOfBirth": "1990-05-01T00:00:00Z",
	}
	with := func(k string, v any) body {
		m := body{}
		for kk, vv := range base {
			m[kk] = vv
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name   string
		body   body
		status int
		reason string
	}{
		{"confirm mismatch", with("confirmPassword", "Other1!pass"), 400, "Password and confirm password do not match"},
		{"bad username", with("username", "ab"), 400, "Username must be 4-30 characters (letters, numbers, underscore)"},
		{"reserved username", with("username", "norviq"), 400, "That username is reserved."},
		{"invalid email", with("email", "nope"), 400, "Invalid email"},
		{"short password", with("password", "Ab1!").confirm("Ab1!"), 400, "Password must be at least 8 characters long"},
		{"no upper", with("password", "abcdefg1!").confirm("abcdefg1!"), 400, "Password must contain at least one uppercase letter"},
		{"no lower", with("password", "ABCDEFG1!").confirm("ABCDEFG1!"), 400, "Password must contain at least one lowercase letter"},
		{"no digit", with("password", "Abcdefgh!").confirm("Abcdefgh!"), 400, "Password must contain at least one number"},
		{"no symbol", with("password", "Abcdefgh1").confirm("Abcdefgh1"), 400, "Password must contain at least one special character"},
		{"future dob", with("dateOfBirth", time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339)), 400, "Date of birth cannot be in the future"},
		{"missing username", with("username", nil), 400, `No such key 'username' at path ''. No value associated with key CodingKeys(stringValue: "username", intValue: nil) ("username").`},
		{"missing confirm", with("confirmPassword", nil), 400, "No such key 'confirmPassword' at path ''. Missing confirmPassword."},
		{"missing dob", with("dateOfBirth", nil), 400, "Value was not of type 'Date' at path 'date_of_birth'. Date must be an ISO8601 string or seconds since Apple reference date."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expect(t, h.Do("POST", "/v1/auth/register", "", c.body), c.status, c.reason)
		})
	}

	r := h.Do("POST", "/v1/auth/register", "", base)
	expect(t, r, 200, "")
	var a AuthResponse
	r.JSON(t, &a)
	if a.Email != "new@example.com" || a.Username != "newuser" || a.Token == "" || a.RefreshToken == "" ||
		a.ExpiresIn != 3600 || a.RefreshExpiresIn != 86400 || !a.DateOfBirth.Equal(time.Date(1990, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("auth response %+v", a)
	}
	if m := <-h.alert.got; m != "🎉 New user registered (password)" || strings.Contains(m, "example.com") {
		t.Fatalf("alert %q", m)
	}
	// The date of birth is stored encrypted, never in the plaintext column.
	var plain *time.Time
	var enc []byte
	var tier *string
	var days *int64
	if err := h.Deps.DB.QueryRow(context.Background(),
		`SELECT date_of_birth, date_of_birth_encrypted, trial_tier, trial_days FROM users WHERE email = 'new@example.com'`).
		Scan(&plain, &enc, &tier, &days); err != nil {
		t.Fatal(err)
	}
	if plain != nil || len(enc) == 0 || tier == nil || *tier != "temporary" || days == nil || *days != 7 {
		t.Fatalf("stored dob=%v enc=%d tier=%v days=%v", plain, len(enc), tier, days)
	}
	if u := h.userByEmail("new@example.com"); u.DateOfBirth == nil || u.PasswordSetAt == nil || u.EmailVerifiedAt != nil {
		t.Fatalf("user %+v", u)
	}
	expect(t, h.Do("POST", "/v1/auth/register", "", with("email", "other@example.com")), 409, "Username already registered")
	expect(t, h.Do("POST", "/v1/auth/register", "", with("username", "another")), 409, "Email already registered")
}

// body is a register payload; confirm sets confirmPassword too.
type body map[string]any

func (m body) confirm(v any) body { m["confirmPassword"] = v; return m }

func TestLoginAndLockout(t *testing.T) {
	h := newHarness(t)
	h.register("loginuser", "login@example.com")
	ok := h.Do("POST", "/v1/auth/login", "", map[string]any{"email": " LOGIN@example.com", "password": goodPassword})
	expect(t, ok, 200, "")
	var a AuthResponse
	ok.JSON(t, &a)
	if a.Username != "loginuser" {
		t.Fatalf("%+v", a)
	}
	expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "ghost@example.com", "password": goodPassword}), 401, "Invalid email or password")
	expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "bad", "password": goodPassword}), 400, "Invalid email")
	expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"password": goodPassword}), 400,
		`No such key 'email' at path ''. No value associated with key CodingKeys(stringValue: "email", intValue: nil) ("email").`)
	for range 5 {
		expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "login@example.com", "password": "Wr0ng!pass"}), 401, "Invalid email or password")
	}
	r := h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "login@example.com", "password": goodPassword})
	if r.Status != 403 || !strings.HasPrefix(reason(t, r), "Account is temporarily locked due to multiple failed login attempts. Please try again in ") {
		t.Fatalf("lockout %d %s", r.Status, r.Body)
	}
	// Lockout state goes through persistUser: PII is rewritten, never plaintext.
	var plain *time.Time
	if err := h.Deps.DB.QueryRow(context.Background(), `SELECT date_of_birth FROM users WHERE email='login@example.com'`).Scan(&plain); err != nil || plain != nil {
		t.Fatalf("plaintext dob after lockout: %v %v", plain, err)
	}
	// The system account can never sign in.
	expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": users.SystemEmail, "password": goodPassword}), 401, "Invalid email or password")
}

func TestLoginMFA(t *testing.T) {
	h := newHarness(t, withSettings(func(s *Settings) {
		s.MFA.Enabled = true
		s.MFA.AllowLegacyBypass = false
		s.MFA.BypassEmails = map[string]bool{"bypass@example.com": true}
	}))
	h.register("mfauser", "mfa@example.com")
	h.register("bypassuser", "bypass@example.com")
	creds := map[string]any{"email": "mfa@example.com", "password": goodPassword}
	capable := map[string]string{"X-StockPlan-Client-Capabilities": "foo, MFA-AUTH-V1"}

	expect(t, h.req("POST", "/v1/auth/login", "", creds, nil), 426, "Please update the app to continue signing in.")

	r := h.req("POST", "/v1/auth/login", "", creds, capable)
	expect(t, r, 200, "")
	var out AuthLoginOutcome
	r.JSON(t, &out)
	if out.Status != "mfaRequired" || out.MFA == nil || out.Auth != nil || out.MFA.MaskedDestination != "mf•••@e•••.com" ||
		out.MFA.ExpiresIn != 300 || out.MFA.ResendAvailableIn != 30 || out.MFA.Channel != "email" {
		t.Fatalf("outcome %s", r.Body)
	}
	msg := h.mail.last(t)
	code := strings.TrimPrefix(msg.Subject, "Your Norviq code is ")
	if len(code) != 6 || msg.Purpose != "mfa_login" || msg.To != "mfa@example.com" || !strings.Contains(msg.Body, "finish signing in: "+code) {
		t.Fatalf("mail %+v", msg)
	}
	id := out.MFA.ChallengeID.String()
	expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": "12"}), 400, "Invalid verification code format.")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": wrong}), 401, "Invalid verification code.")
	expect(t, h.Do("POST", "/v1/auth/mfa/resend", "", map[string]any{"challengeId": id}), 429, "Please wait before requesting another verification code.")
	r = h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": code})
	expect(t, r, 200, "")
	var a AuthResponse
	r.JSON(t, &a)
	if a.Email != "mfa@example.com" || a.Token == "" {
		t.Fatalf("%s", r.Body)
	}
	expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": code}), 401, "Verification challenge is no longer valid.")
	expect(t, h.Do("POST", "/v1/auth/mfa/resend", "", map[string]any{"challengeId": "6F9619FF-8B86-D011-B42D-00CF4FC964FF"}), 401, "Invalid or expired verification challenge.")

	// A bypass address authenticates directly, even for a capable client.
	r = h.req("POST", "/v1/auth/login", "", map[string]any{"email": "bypass@example.com", "password": goodPassword}, capable)
	r.JSON(t, &out)
	if out.Status != "authenticated" || out.Auth == nil {
		t.Fatalf("bypass %s", r.Body)
	}
}

func TestMFAResendAndAttemptCap(t *testing.T) {
	now := time.Now()
	clock := &now
	h := newHarness(t, withSettings(func(s *Settings) { s.MFA.Enabled = true }), func(f *Feature) {
		f.Now = func() time.Time { return *clock }
	})
	h.register("resender", "resend@example.com")
	capable := map[string]string{"X-StockPlan-Client-Capabilities": "mfa-auth-v1"}
	r := h.req("POST", "/v1/auth/login", "", map[string]any{"email": "resend@example.com", "password": goodPassword}, capable)
	var out AuthLoginOutcome
	r.JSON(t, &out)
	id := out.MFA.ChallengeID.String()
	for i := 1; i <= 3; i++ {
		*clock = clock.Add(31 * time.Second)
		r = h.Do("POST", "/v1/auth/mfa/resend", "", map[string]any{"challengeId": id})
		expect(t, r, 200, "")
	}
	*clock = clock.Add(31 * time.Second)
	expect(t, h.Do("POST", "/v1/auth/mfa/resend", "", map[string]any{"challengeId": id}), 429, "Maximum resend attempts reached.")
	for range 5 {
		h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": "999999"})
	}
	// The fifth failure consumes the challenge.
	expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": "999999"}), 401, "Verification challenge is no longer valid.")
	*clock = clock.Add(time.Hour)
	r = h.req("POST", "/v1/auth/login", "", map[string]any{"email": "resend@example.com", "password": goodPassword}, capable)
	r.JSON(t, &out)
	*clock = clock.Add(10 * time.Minute)
	expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": out.MFA.ChallengeID.String(), "code": "123456"}), 401, "Verification challenge expired.")
	// A non-capable client with legacy bypass gets a bare AuthResponse.
	r = h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "resend@example.com", "password": goodPassword})
	var a AuthResponse
	r.JSON(t, &a)
	if a.Token == "" {
		t.Fatalf("legacy %s", r.Body)
	}
}

func TestTestFlightBypassCode(t *testing.T) {
	h := newHarness(t, withSettings(func(s *Settings) { s.MFA.Enabled = true; s.TestFlightBypass = true }))
	h.register("tester", "test@stockplan.app")
	r := h.req("POST", "/v1/auth/login", "", map[string]any{"email": "test@stockplan.app", "password": goodPassword},
		map[string]string{"X-StockPlan-Client-Capabilities": "mfa-auth-v1"})
	var out AuthLoginOutcome
	r.JSON(t, &out)
	if h.mail.count() != 0 {
		t.Fatal("bypass address must not be mailed")
	}
	expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": out.MFA.ChallengeID.String(), "code": "123456"}), 200, "")
}

func TestMeAndRefresh(t *testing.T) {
	h := newHarness(t)
	a := h.register("meuser", "me@example.com")
	r := h.Do("GET", "/v1/auth/me", a.Token, nil)
	expect(t, r, 200, "")
	var me authUserResponse
	r.JSON(t, &me)
	if me.ID != a.UserID.String() || me.Username != "meuser" || me.Email != "me@example.com" {
		t.Fatalf("%s", r.Body)
	}
	expect(t, h.Do("GET", "/v1/auth/me", "", nil), 401, "")

	r = h.Do("POST", "/v1/auth/refresh", "", map[string]any{"refreshToken": a.RefreshToken})
	expect(t, r, 200, "")
	var b AuthResponse
	r.JSON(t, &b)
	if b.RefreshToken == a.RefreshToken {
		t.Fatal("refresh token not rotated")
	}
	expect(t, h.Do("POST", "/v1/auth/refresh", "", map[string]any{"refreshToken": a.RefreshToken}), 401, "Invalid refresh token")
	expect(t, h.Do("POST", "/v1/auth/refresh", "", map[string]any{"refreshToken": "nope"}), 401, "Invalid refresh token")

	// A user created without username or date of birth gets the fallbacks.
	id := h.CreateUser("bare@example.com")
	r = h.Do("GET", "/v1/auth/me", h.Token(id), nil)
	r.JSON(t, &me)
	if me.Username != "bare" || me.DateOfBirth.Month() != time.January || me.DateOfBirth.Year() != time.Now().UTC().Year()-20 {
		t.Fatalf("%s", r.Body)
	}
}

func TestPasswordReset(t *testing.T) {
	h := newHarness(t)
	a := h.register("resetuser", "reset@example.com")
	generic := `{"message":"If the account exists, a reset code has been sent."}`
	r := h.Do("POST", "/v1/auth/forgot-password", "", map[string]any{"email": "ghost@example.com"})
	if r.Status != 200 || string(r.Body) != generic {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = h.Do("POST", "/v1/auth/forgot-password", "", map[string]any{"email": "reset@example.com"})
	var fp forgotPasswordResponse
	r.JSON(t, &fp)
	if fp.ResetCode == nil || h.mail.last(t).Body != "Use this code to reset your password: "+*fp.ResetCode ||
		h.mail.last(t).Subject != "Your StockPlan reset code" {
		t.Fatalf("%s %+v", r.Body, h.mail.last(t))
	}
	expect(t, h.Do("POST", "/v1/auth/reset-password", "", map[string]any{"email": "ghost@example.com", "code": "123456", "newPassword": "N3w!Password"}), 401, "Invalid reset code")
	wrong := "000000"
	if *fp.ResetCode == wrong {
		wrong = "111111"
	}
	expect(t, h.Do("POST", "/v1/auth/reset-password", "", map[string]any{"email": "reset@example.com", "code": wrong, "newPassword": "N3w!Password"}), 401, "Invalid reset code")
	expect(t, h.Do("POST", "/v1/auth/reset-password", "", map[string]any{"email": "reset@example.com", "code": *fp.ResetCode, "newPassword": "N3w!Password"}), 204, "")

	// Every session ended, and the mailbox is now proven.
	expect(t, h.Do("GET", "/v1/auth/me", a.Token, nil), 401, "")
	expect(t, h.Do("POST", "/v1/auth/refresh", "", map[string]any{"refreshToken": a.RefreshToken}), 401, "Invalid refresh token")
	if u := h.userByEmail("reset@example.com"); u.EmailVerifiedAt == nil {
		t.Fatal("reset must verify the email")
	}
	expect(t, h.Do("POST", "/v1/auth/login", "", map[string]any{"email": "reset@example.com", "password": "N3w!Password"}), 200, "")

	// Three codes per five minutes; the fourth is silently skipped.
	for range 3 {
		h.Do("POST", "/v1/auth/resend-reset", "", map[string]any{"email": "reset@example.com"})
	}
	before := h.mail.count()
	r = h.Do("POST", "/v1/auth/resend-reset", "", map[string]any{"email": "reset@example.com"})
	if r.Status != 200 || string(r.Body) != generic || h.mail.count() != before {
		t.Fatalf("throttle %s", r.Body)
	}
}

func TestResetTokenLocksAfterRepeatedFailures(t *testing.T) {
	h := newHarness(t)
	h.register("lockreset", "lockreset@example.com")
	r := h.Do("POST", "/v1/auth/forgot-password", "", map[string]any{"email": "lockreset@example.com"})
	var fp forgotPasswordResponse
	r.JSON(t, &fp)
	wrong := "000000"
	if *fp.ResetCode == wrong {
		wrong = "111111"
	}
	for range 5 {
		h.Do("POST", "/v1/auth/reset-password", "", map[string]any{"email": "lockreset@example.com", "code": wrong, "newPassword": "N3w!Password"})
	}
	// The token is used up: even the right code fails now.
	expect(t, h.Do("POST", "/v1/auth/reset-password", "", map[string]any{"email": "lockreset@example.com", "code": *fp.ResetCode, "newPassword": "N3w!Password"}), 401, "Invalid reset code")
}

func TestCleanupDeletesDeadTokens(t *testing.T) {
	h := newHarness(t)
	a := h.register("cleanup", "cleanup@example.com")
	h.Do("POST", "/v1/auth/refresh", "", map[string]any{"refreshToken": a.RefreshToken}) // revokes the first
	h.Do("POST", "/v1/auth/forgot-password", "", map[string]any{"email": "cleanup@example.com"})
	resets, refresh := CleanupOnce(context.Background(), h.Deps, time.Now().Add(time.Hour))
	if resets != 1 || refresh != 1 {
		t.Fatalf("deleted resets=%d refresh=%d", resets, refresh)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		CleanupJob{Interval: time.Millisecond, InitialDelay: time.Millisecond}.Run(ctx, h.Deps)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if (CleanupJob{}).Name() != "auth-token-cleanup" {
		t.Fatal("job name")
	}
}

func TestDefaultTrialDaysClamp(t *testing.T) {
	for raw, want := range map[string]int{"": 7, "x": 7, "3": 7, "10": 10, "30": 14} {
		if got := (&Settings{DefaultTrialDaysRaw: raw}).defaultTrialDays(); got != want {
			t.Errorf("%q: %d, want %d", raw, got, want)
		}
	}
}

func TestMaskDestination(t *testing.T) {
	for in, want := range map[string]string{"ab@d.io": "ab•••@d•••.io", "x": "••••", "joão@exämple.pt": "jo•••@e•••.pt"} {
		if got := maskDestination(in, "email"); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
	if maskDestination("+351912345678", "sms") != "••••5678" || maskDestination("12", "sms") != "••••" {
		t.Error("sms mask")
	}
}
