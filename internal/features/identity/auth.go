package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/store/identitydb"
	"norviq-api/internal/store/usersdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

const mfaCapabilityToken = "mfa-auth-v1"

// AuthResponse is StockPlanShared.AuthResponse.
type AuthResponse struct {
	Token            string       `json:"token"`
	UserID           apijson.UUID `json:"userId"`
	ExpiresIn        int          `json:"expiresIn"`
	RefreshToken     string       `json:"refreshToken"`
	RefreshExpiresIn int          `json:"refreshExpiresIn"`
	Username         string       `json:"username"`
	Email            string       `json:"email"`
	DateOfBirth      apijson.Time `json:"dateOfBirth"`
}

// AuthLoginOutcome is what MFA-capable clients receive from login/exchange.
type AuthLoginOutcome struct {
	Status string                    `json:"status"` // authenticated | mfaRequired
	Auth   *AuthResponse             `json:"auth,omitempty"`
	MFA    *AuthMFAChallengeResponse `json:"mfa,omitempty"`
}

type authUserResponse struct {
	ID          string       `json:"id"`
	Username    string       `json:"username"`
	Email       string       `json:"email"`
	DateOfBirth apijson.Time `json:"dateOfBirth"`
}

type forgotPasswordResponse struct {
	Message   string  `json:"message"`
	ResetCode *string `json:"resetCode,omitempty"`
}

const resetSentMessage = "If the account exists, a reset code has been sent."

// keyNotFound reproduces the reason Vapor gives for a missing required key.
func keyNotFound(key string) *httpx.Error {
	return keyNotFoundDesc(key, fmt.Sprintf(`No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q)`, key, key))
}

// keyNotFoundDesc is keyNotFound for DTOs that throw their own description.
func keyNotFoundDesc(key, description string) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf("No such key '%s' at path ''. %s.", key, description))
}

// badDate is SharedDateDecoder's failure for a missing or unparseable date.
func badDate(key string) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type 'Date' at path '%s'. Date must be an ISO8601 string or seconds since Apple reference date.", key))
}

// requireStrings returns the first missing key, in declaration order.
func requireStrings(fields ...any) *httpx.Error {
	for i := 0; i+1 < len(fields); i += 2 {
		if p, ok := fields[i+1].(*string); ok && p == nil {
			return keyNotFound(fields[i].(string))
		}
	}
	return nil
}

// --- register ---------------------------------------------------------------

type registerRequest struct {
	Username        *string         `json:"username"`
	Password        *string         `json:"password"`
	ConfirmPassword *string         `json:"confirmPassword"`
	Email           *string         `json:"email"`
	DateOfBirth     json.RawMessage `json:"dateOfBirth"`
	TrialDays       *int            `json:"trialDays"`
	CouponCode      *string         `json:"couponCode"`
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{4,30}$`)

func (s *service) register(c *echo.Context) error {
	var p registerRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if err := requireStrings("username", p.Username, "password", p.Password); err != nil {
		return err
	}
	if p.ConfirmPassword == nil {
		return keyNotFoundDesc("confirmPassword", "Missing confirmPassword")
	}
	if err := requireStrings("email", p.Email); err != nil {
		return err
	}
	if len(p.DateOfBirth) == 0 {
		return badDate("date_of_birth")
	}
	dob, err := apijson.ParseTimeJSON(p.DateOfBirth)
	if err != nil {
		return badDate("date_of_birth")
	}
	ctx := c.Request().Context()
	username := strings.ToLower(strings.TrimSpace(*p.Username))
	email := users.NormalizeEmail(*p.Email)
	if *p.Password != *p.ConfirmPassword {
		return httpx.BadRequest("Password and confirm password do not match")
	}
	if err := validateUsername(username); err != nil {
		return err
	}
	if users.IsSystemUsername(username) {
		return httpx.BadRequest("That username is reserved.")
	}
	if err := validateEmail(email); err != nil {
		return err
	}
	if err := validatePassword(*p.Password); err != nil {
		return err
	}
	now := s.now()
	if dob.After(now) {
		return httpx.BadRequest("Date of birth cannot be in the future")
	}
	if u, err := s.users.ByUsername(ctx, nil, username); err != nil || u != nil {
		return orConflict(err, "Username already registered")
	}
	if u, err := s.users.ByEmail(ctx, nil, email); err != nil || u != nil {
		return orConflict(err, "Email already registered")
	}
	hash, err := users.HashPassword(*p.Password)
	if err != nil {
		return err
	}
	u, err := s.users.Create(ctx, nil, users.NewUser{
		Username: username, Email: email, PasswordHash: hash, PasswordSetAt: &now, DateOfBirth: dob,
	}, now)
	if err != nil {
		s.log.Error("auth.register create_user_failed", "error", err)
		return err
	}
	if err := s.initializeDefaultTrial(ctx, u); err != nil {
		return err
	}
	s.alert.Notify("🎉 New user registered (password)")
	resp, err := s.makeAuthResponse(ctx, u)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func orConflict(err error, reason string) error {
	if err != nil {
		return err
	}
	return httpx.Conflict(reason)
}

// initializeDefaultTrial is TrialService.initializeTrial with tier "temporary".
func (s *service) initializeDefaultTrial(ctx context.Context, u *users.User) error {
	if u.HadTrial {
		return nil
	}
	now := s.now()
	days := int64(s.cfg.defaultTrialDays())
	tier := "temporary"
	if err := s.users.Q(nil).SetUserTrial(ctx, usersdb.SetUserTrialParams{
		ID: u.ID, TrialStartedAt: &now, TrialDays: &days, TrialTier: &tier, UpdatedAt: &now,
	}); err != nil {
		return err
	}
	u.TrialStartedAt, u.TrialDays, u.TrialTier = &now, &days, &tier
	return nil
}

func validateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return httpx.BadRequest("Username must be 4-30 characters (letters, numbers, underscore)")
	}
	return nil
}

func validateEmail(email string) error {
	if email == "" || !strings.Contains(email, "@") {
		return httpx.BadRequest("Invalid email")
	}
	return nil
}

func validatePassword(password string) error {
	if utf8.RuneCountInString(password) < 8 {
		return httpx.BadRequest("Password must be at least 8 characters long")
	}
	has := func(f func(rune) bool) bool { return strings.IndexFunc(password, f) >= 0 }
	switch {
	case !has(func(r rune) bool { return unicode.IsUpper(r) || unicode.IsTitle(r) }):
		return httpx.BadRequest("Password must contain at least one uppercase letter")
	case !has(unicode.IsLower):
		return httpx.BadRequest("Password must contain at least one lowercase letter")
	case !has(func(r rune) bool { return unicode.Is(unicode.Nd, r) }):
		return httpx.BadRequest("Password must contain at least one number")
	case !has(func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }):
		return httpx.BadRequest("Password must contain at least one special character")
	}
	return nil
}

// --- login ------------------------------------------------------------------

type loginRequest struct {
	Email    *string `json:"email"`
	Password *string `json:"password"`
}

func clientHas(c *echo.Context, token string) bool {
	for _, v := range strings.Split(c.Request().Header.Get("X-StockPlan-Client-Capabilities"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), token) {
			return true
		}
	}
	return false
}

// requireMFA decides whether this client must pass MFA (AuthController.requireMFA).
func (s *service) requireMFA(c *echo.Context) (bool, error) {
	if !s.cfg.MFA.Enabled {
		return false, nil
	}
	if clientHas(c, mfaCapabilityToken) {
		return true, nil
	}
	if !s.cfg.MFA.AllowLegacyBypass {
		return false, httpx.Abort(http.StatusUpgradeRequired, "Please update the app to continue signing in.")
	}
	return false, nil
}

// writeOutcome sends the outcome to MFA-capable clients and the bare
// AuthResponse to the rest.
func writeOutcome(c *echo.Context, o AuthLoginOutcome) error {
	if clientHas(c, mfaCapabilityToken) {
		return httpx.OK(c, o)
	}
	if o.Auth == nil {
		return httpx.BadRequest("MFA is required for this client.")
	}
	return httpx.OK(c, o.Auth)
}

func (s *service) loginHandler(c *echo.Context) error {
	var p loginRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if err := requireStrings("email", p.Email, "password", p.Password); err != nil {
		return err
	}
	needMFA, err := s.requireMFA(c)
	if err != nil {
		return err
	}
	o, err := s.login(c.Request().Context(), *p.Email, *p.Password, needMFA)
	if err != nil {
		return err
	}
	return writeOutcome(c, o)
}

const invalidCredentials = "Invalid email or password"

func (s *service) login(ctx context.Context, rawEmail, password string, needMFA bool) (AuthLoginOutcome, error) {
	email := users.NormalizeEmail(rawEmail)
	if err := validateEmail(email); err != nil {
		return AuthLoginOutcome{}, err
	}
	if err := validatePassword(password); err != nil {
		return AuthLoginOutcome{}, err
	}
	u, err := s.users.ByEmail(ctx, nil, email)
	if err != nil {
		return AuthLoginOutcome{}, err
	}
	if u == nil {
		_, _ = users.HashPassword(password) // same cost whether or not the account exists
		return AuthLoginOutcome{}, unauthorized(invalidCredentials)
	}
	if users.IsSystemEmail(u.Email) {
		return AuthLoginOutcome{}, unauthorized(invalidCredentials)
	}
	now := s.now()
	if u.LockoutUntil != nil && u.LockoutUntil.After(now) {
		minutes := int(u.LockoutUntil.Sub(now).Seconds() / 60)
		msg := "Account is temporarily locked due to multiple failed login attempts. Please try again shortly."
		if minutes > 0 {
			msg = fmt.Sprintf("Account is temporarily locked due to multiple failed login attempts. Please try again in %d minute(s).", minutes)
		}
		return AuthLoginOutcome{}, httpx.Forbidden(msg)
	}
	ok, err := users.VerifyPassword(password, u.PasswordHash)
	if err != nil {
		return AuthLoginOutcome{}, err
	}
	if !ok {
		u.FailedLoginAttempts++
		if u.FailedLoginAttempts >= 5 {
			until := now.Add(15 * time.Minute)
			u.LockoutUntil = &until
			s.log.Warn("auth.login account_locked", "user_id", apijson.FormatUUID(u.ID), "until", until)
		}
		if err := s.persistLoginState(ctx, u); err != nil {
			return AuthLoginOutcome{}, err
		}
		return AuthLoginOutcome{}, unauthorized(invalidCredentials)
	}
	if u.FailedLoginAttempts > 0 || u.LockoutUntil != nil {
		u.FailedLoginAttempts, u.LockoutUntil = 0, nil
		if err := s.persistLoginState(ctx, u); err != nil {
			return AuthLoginOutcome{}, err
		}
	}
	// A concurrent reset may have replaced the hash while bcrypt ran.
	current, err := s.users.ByID(ctx, nil, u.ID)
	if err != nil {
		return AuthLoginOutcome{}, err
	}
	if current == nil || current.PasswordHash != u.PasswordHash {
		return AuthLoginOutcome{}, unauthorized(invalidCredentials)
	}
	if needMFA && !s.cfg.MFA.BypassEmails[users.NormalizeEmail(u.Email)] {
		ch, err := s.issueMFAChallenge(ctx, u, "login")
		if err != nil {
			return AuthLoginOutcome{}, err
		}
		s.log.Info("auth.mfa challenge_issued", "purpose", "login", "user_id", apijson.FormatUUID(u.ID))
		return AuthLoginOutcome{Status: "mfaRequired", MFA: ch}, nil
	}
	resp, err := s.makeAuthResponse(ctx, u)
	if err != nil {
		return AuthLoginOutcome{}, err
	}
	return AuthLoginOutcome{Status: "authenticated", Auth: resp}, nil
}

// persistLoginState is persistUser after a lockout counter change.
func (s *service) persistLoginState(ctx context.Context, u *users.User) error {
	return pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		now := s.now()
		if err := s.users.Q(tx).SetUserLoginState(ctx, usersdb.SetUserLoginStateParams{
			ID: u.ID, FailedLoginAttempts: u.FailedLoginAttempts, LockoutUntil: u.LockoutUntil, UpdatedAt: &now,
		}); err != nil {
			return err
		}
		return s.users.RewritePII(ctx, tx, u, now)
	})
}

// --- me ---------------------------------------------------------------------

func (s *service) me(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	u, err := s.requireUser(c.Request().Context(), sess.UserID, "User not found")
	if err != nil {
		return err
	}
	return httpx.OK(c, authUserResponse{
		ID: apijson.FormatUUID(u.ID), Username: responseUsername(u), Email: u.Email,
		DateOfBirth: apijson.NewTime(s.responseDateOfBirth(u)),
	})
}

func responseUsername(u *users.User) string {
	if u.Username != nil {
		if t := strings.TrimSpace(*u.Username); t != "" {
			return t
		}
	}
	if prefix, _, _ := strings.Cut(u.Email, "@"); prefix != "" {
		return prefix
	}
	return u.Email
}

func (s *service) responseDateOfBirth(u *users.User) time.Time {
	if u.DateOfBirth != nil {
		return *u.DateOfBirth
	}
	return s.defaultDateOfBirth()
}

// defaultDateOfBirth is January 1st, twenty years ago (UTC, as in the cluster).
func (s *service) defaultDateOfBirth() time.Time {
	return time.Date(s.now().UTC().Year()-20, 1, 1, 0, 0, 0, 0, time.UTC)
}

// --- tokens -----------------------------------------------------------------

// randomURLSafe is base64url without padding over n random bytes.
func randomURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// randomURLSafeString is AuthService.randomURLSafeString: at least 32 random
// bytes, cut to length.
func randomURLSafeString(length int) string {
	raw := randomURLSafe(max(length, 32))
	if len(raw) >= length {
		return raw[:length]
	}
	return raw + strings.ReplaceAll(strings.ToUpper(uuid.NewString()), "-", "")
}

func (s *service) makeAuthResponse(ctx context.Context, u *users.User) (*AuthResponse, error) {
	now := s.now()
	token, err := s.signer.Mint(u.ID, now.Add(time.Duration(s.cfg.JWTExpiresIn)*time.Second), now)
	if err != nil {
		return nil, err
	}
	refresh := randomURLSafe(32)
	if err := identitydb.New(s.d.DB).InsertRefreshToken(ctx, identitydb.InsertRefreshTokenParams{
		ID: uuid.New(), UserID: u.ID, TokenHash: users.HashToken(refresh),
		ExpiresAt: now.Add(time.Duration(s.cfg.RefreshExpiresIn) * time.Second), CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	return &AuthResponse{
		Token: token, UserID: apijson.NewUUID(u.ID), ExpiresIn: s.cfg.JWTExpiresIn,
		RefreshToken: refresh, RefreshExpiresIn: s.cfg.RefreshExpiresIn,
		Username: responseUsername(u), Email: u.Email, DateOfBirth: apijson.NewTime(s.responseDateOfBirth(u)),
	}, nil
}

type refreshRequest struct {
	RefreshToken *string `json:"refreshToken"`
}

func (s *service) refresh(c *echo.Context) error {
	var p refreshRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if err := requireStrings("refreshToken", p.RefreshToken); err != nil {
		return err
	}
	ctx := c.Request().Context()
	now := s.now()
	q := identitydb.New(s.d.DB)
	stored, err := q.ValidRefreshToken(ctx, identitydb.ValidRefreshTokenParams{TokenHash: users.HashToken(*p.RefreshToken), ExpiresAt: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return unauthorized("Invalid refresh token")
	}
	if err != nil {
		return err
	}
	u, err := s.requireUser(ctx, stored.UserID, "User not found")
	if err != nil {
		return err
	}
	if u.SessionsValidAfter != nil && stored.CreatedAt.Before(*u.SessionsValidAfter) {
		return unauthorized("Invalid refresh token")
	}
	if err := q.RevokeRefreshToken(ctx, identitydb.RevokeRefreshTokenParams{ID: stored.ID, RevokedAt: &now}); err != nil {
		return err
	}
	resp, err := s.makeAuthResponse(ctx, u)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// --- password reset ---------------------------------------------------------

type emailRequest struct {
	Email *string `json:"email"`
}

func (s *service) forgotPassword(c *echo.Context) error { return s.issueResetCode(c) }

func (s *service) resendReset(c *echo.Context) error { return s.issueResetCode(c) }

func (s *service) issueResetCode(c *echo.Context) error {
	var p emailRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if err := requireStrings("email", p.Email); err != nil {
		return err
	}
	ctx := c.Request().Context()
	email := users.NormalizeEmail(*p.Email)
	if err := validateEmail(email); err != nil {
		return err
	}
	generic := forgotPasswordResponse{Message: resetSentMessage}
	u, err := s.users.ByEmail(ctx, nil, email)
	if err != nil {
		return err
	}
	if u == nil || users.IsSystemEmail(u.Email) {
		return httpx.OK(c, generic)
	}
	now := s.now()
	q := identitydb.New(s.d.DB)
	recent, err := q.CountRecentPasswordResetTokens(ctx, identitydb.CountRecentPasswordResetTokensParams{
		UserID: u.ID, CreatedAt: now.Add(-5 * time.Minute),
	})
	if err != nil {
		return err
	}
	if recent >= 3 {
		s.log.Warn("auth.password_reset throttled", "user_id", apijson.FormatUUID(u.ID))
		return httpx.OK(c, generic)
	}
	code := sixDigitCode()
	if err := q.InsertPasswordResetToken(ctx, identitydb.InsertPasswordResetTokenParams{
		ID: uuid.New(), UserID: u.ID, CodeHash: users.HashToken(code), ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := s.mail.Send(ctx, mailer.Message{
		To: email, Subject: "Your StockPlan reset code", Body: "Use this code to reset your password: " + code,
	}); err != nil {
		return err
	}
	if s.cfg.ResetReturnCode {
		generic.ResetCode = &code
	}
	return httpx.OK(c, generic)
}

// maxResetAttempts is how many times one reset code may be checked in total.
const maxResetAttempts = 5

var errResetCodeSpent = errors.New("reset code already used")

type resetPasswordRequest struct {
	Email       *string `json:"email"`
	Code        *string `json:"code"`
	NewPassword *string `json:"newPassword"`
}

func (s *service) resetPassword(c *echo.Context) error {
	var p resetPasswordRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if err := requireStrings("email", p.Email, "code", p.Code, "newPassword", p.NewPassword); err != nil {
		return err
	}
	ctx := c.Request().Context()
	email := users.NormalizeEmail(*p.Email)
	if err := validateEmail(email); err != nil {
		return err
	}
	if err := validatePassword(*p.NewPassword); err != nil {
		return err
	}
	const invalid = "Invalid reset code"
	u, err := s.users.ByEmail(ctx, nil, email)
	if err != nil {
		return err
	}
	if u == nil {
		return unauthorized(invalid)
	}
	now := s.now()
	q := identitydb.New(s.d.DB)
	tok, err := q.LatestActivePasswordResetToken(ctx, identitydb.LatestActivePasswordResetTokenParams{UserID: u.ID, ExpiresAt: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return unauthorized(invalid)
	}
	if err != nil {
		return err
	}
	if tok.LockedUntil != nil && tok.LockedUntil.After(now) {
		return httpx.Abort(http.StatusTooManyRequests, "Too many reset attempts. Please request a new code later.")
	}
	// Claim the attempt before comparing, so concurrent guesses cannot share
	// one read of the counter and exceed the cap.
	attempts, err := q.ClaimPasswordResetAttempt(ctx, identitydb.ClaimPasswordResetAttemptParams{ID: tok.ID, MaxAttempts: maxResetAttempts})
	if errors.Is(err, pgx.ErrNoRows) {
		return unauthorized(invalid)
	}
	if err != nil {
		return err
	}
	if tok.CodeHash != users.HashToken(*p.Code) {
		if attempts >= maxResetAttempts {
			locked := now.Add(15 * time.Minute)
			if err := q.LockPasswordResetToken(ctx, identitydb.LockPasswordResetTokenParams{
				ID: tok.ID, UsedAt: &now, LockedUntil: &locked,
			}); err != nil {
				return err
			}
		}
		return unauthorized(invalid)
	}
	hash, err := users.HashPassword(*p.NewPassword)
	if err != nil {
		return err
	}
	verified := u.EmailVerifiedAt
	if verified == nil {
		verified = &now // the code arrived by email: the mailbox is proven
	}
	err = pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		if err := s.users.Q(tx).SetUserPassword(ctx, usersdb.SetUserPasswordParams{
			ID: u.ID, PasswordHash: hash, PasswordSetAt: &now, EmailVerifiedAt: verified, UpdatedAt: &now,
		}); err != nil {
			return err
		}
		if err := s.users.RewritePII(ctx, tx, u, now); err != nil {
			return err
		}
		n, err := identitydb.New(tx).ConsumePasswordResetToken(ctx, identitydb.ConsumePasswordResetTokenParams{ID: tok.ID, UsedAt: &now})
		if err != nil {
			return err
		}
		if n == 0 {
			return errResetCodeSpent // a concurrent request used or locked it
		}
		return users.RevokeAllTokens(ctx, tx, u.ID, nil, now)
	})
	if errors.Is(err, errResetCodeSpent) {
		return unauthorized(invalid)
	}
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func sixDigitCode() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) % 1_000_000
	return fmt.Sprintf("%06d", n)
}
