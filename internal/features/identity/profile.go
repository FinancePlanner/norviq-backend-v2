package identity

import (
	"context"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/store/usersdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// userProfile is StockPlanShared.UserProfile.
type userProfile struct {
	ID              *string `json:"id"`
	Email           *string `json:"email"`
	Bio             *string `json:"bio,omitempty"`
	AvatarURL       *string `json:"avatarURL,omitempty"`
	BannerAvatarURL *string `json:"bannerAvatarURL,omitempty"`
	Username        *string `json:"username,omitempty"`
}

type profileEnvelope struct {
	UserProfile *userProfile `json:"userProfile"`
}

func normalizeOptional(p *string) *string { return trimmedNonEmpty(p) }

func makeProfile(u *users.User) profileEnvelope {
	id, email := apijson.FormatUUID(u.ID), u.Email
	return profileEnvelope{UserProfile: &userProfile{
		ID: &id, Email: &email, Bio: normalizeOptional(u.Bio),
		AvatarURL: u.AvatarUrl, BannerAvatarURL: u.BannerAvatarUrl, Username: normalizeOptional(u.Username),
	}}
}

// validateOptionalUsername is UserProfileService.validateUsername.
func validateOptionalUsername(username *string) error {
	if username == nil {
		return nil
	}
	if err := validateUsername(*username); err != nil {
		return err
	}
	if users.IsSystemUsername(*username) {
		return httpx.BadRequest("That username is reserved.")
	}
	return nil
}

func (s *service) ensureUsernameFree(ctx context.Context, username *string, self uuid.UUID) error {
	if username == nil {
		return nil
	}
	existing, err := s.users.ByUsername(ctx, nil, *username)
	if err != nil {
		return err
	}
	if existing != nil && existing.ID != self {
		return httpx.Conflict("Username already registered")
	}
	return nil
}

// saveProfile is repo.save after a profile edit: the plain columns plus the
// re-encrypted PII, in one transaction.
func (s *service) saveProfile(ctx context.Context, u *users.User) error {
	return pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		now := s.now()
		if err := s.users.Q(tx).SetUserProfile(ctx, usersdb.SetUserProfileParams{
			ID: u.ID, Username: u.Username, AvatarUrl: u.AvatarUrl, BannerAvatarUrl: u.BannerAvatarUrl, UpdatedAt: &now,
		}); err != nil {
			return err
		}
		return s.users.RewritePII(ctx, tx, u, now)
	})
}

func lowerPtr(p *string) *string {
	if p == nil {
		return nil
	}
	l := strings.ToLower(*p)
	return &l
}

func (s *service) getProfile(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	return s.writeProfile(c, sess.UserID)
}

func (s *service) writeProfile(c *echo.Context, id uuid.UUID) error {
	u, err := s.requireUser(c.Request().Context(), id, "User not found")
	if err != nil {
		return err
	}
	return httpx.OK(c, makeProfile(u))
}

func (s *service) updateProfile(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	return s.update(c, sess.UserID)
}

func (s *service) update(c *echo.Context, userID uuid.UUID) error {
	var p profileEnvelope
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if p.UserProfile == nil {
		return keyNotFound("userProfile")
	}
	if p.UserProfile.ID == nil {
		return keyNotFound("id")
	}
	if p.UserProfile.Email == nil {
		return keyNotFound("email")
	}
	if *p.UserProfile.ID != apijson.FormatUUID(userID) {
		return httpx.BadRequest("Profile ID does not match authenticated user")
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, userID, "User not found")
	if err != nil {
		return err
	}
	email := users.NormalizeEmail(*p.UserProfile.Email)
	username := lowerPtr(normalizeOptional(p.UserProfile.Username))
	if err := validateEmail(email); err != nil {
		return err
	}
	if err := validateOptionalUsername(username); err != nil {
		return err
	}
	if email != users.NormalizeEmail(u.Email) {
		return httpx.BadRequest("EMAIL_CHANGE_REQUIRES_REAUTH")
	}
	if err := s.ensureUsernameFree(ctx, username, u.ID); err != nil {
		return err
	}
	u.Username = username
	u.Bio = normalizeOptional(p.UserProfile.Bio)
	u.AvatarUrl = p.UserProfile.AvatarURL
	u.BannerAvatarUrl = p.UserProfile.BannerAvatarURL
	if err := s.saveProfile(ctx, u); err != nil {
		return err
	}
	return httpx.OK(c, makeProfile(u))
}

func (s *service) updateUsername(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p struct {
		Username *string `json:"username"`
	}
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if p.Username == nil {
		return keyNotFound("username")
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, sess.UserID, "User not found")
	if err != nil {
		return err
	}
	username := lowerPtr(normalizeOptional(p.Username))
	if err := validateOptionalUsername(username); err != nil {
		return err
	}
	if err := s.ensureUsernameFree(ctx, username, u.ID); err != nil {
		return err
	}
	u.Username = username
	if err := s.saveProfile(ctx, u); err != nil {
		return err
	}
	return httpx.OK(c, makeProfile(u))
}

// sparedSession is UserProfileService.callerSession.
func sparedSession(sess *auth.Session, refreshToken *string) *users.SparedSession {
	sp := &users.SparedSession{AccessTokenIssuedAt: sess.IssuedAt}
	if refreshToken != nil {
		h := users.HashToken(*refreshToken)
		sp.RefreshTokenHash = &h
	}
	return sp
}

func (s *service) updateEmail(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p struct {
		Email           *string `json:"email"`
		CurrentPassword *string `json:"currentPassword"`
		RefreshToken    *string `json:"refreshToken"`
	}
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if p.Email == nil {
		return keyNotFound("email")
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, sess.UserID, "User not found")
	if err != nil {
		return err
	}
	email := users.NormalizeEmail(*p.Email)
	if err := validateEmail(email); err != nil {
		return err
	}
	if email == users.NormalizeEmail(u.Email) {
		return httpx.BadRequest("EMAIL_UNCHANGED")
	}
	current := ""
	if p.CurrentPassword != nil {
		current = *p.CurrentPassword
	}
	matches := false
	if current != "" {
		if matches, err = users.VerifyPassword(current, u.PasswordHash); err != nil {
			return err
		}
	}
	if !matches {
		switch {
		case u.PasswordSetAt == nil:
			return httpx.Conflict("REAUTH_REQUIRED")
		case current == "":
			return httpx.BadRequest("CURRENT_PASSWORD_REQUIRED")
		default:
			return httpx.BadRequest("INVALID_CURRENT_PASSWORD")
		}
	}
	existing, err := s.users.ByEmail(ctx, nil, email)
	if err != nil {
		return err
	}
	if existing != nil && existing.ID != u.ID {
		return httpx.Conflict("Email already registered")
	}
	spared := sparedSession(sess, p.RefreshToken)
	err = pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		now := s.now()
		if err := s.users.Q(tx).SetUserEmail(ctx, usersdb.SetUserEmailParams{ID: u.ID, Email: email, EmailVerifiedAt: nil, UpdatedAt: &now}); err != nil {
			return err
		}
		if err := s.users.RewritePII(ctx, tx, u, now); err != nil {
			return err
		}
		return users.RevokeAllTokens(ctx, tx, u.ID, spared, now)
	})
	if err != nil {
		return err
	}
	u.Email, u.EmailVerifiedAt = email, nil
	return httpx.OK(c, makeProfile(u))
}

func (s *service) updatePassword(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p struct {
		CurrentPassword *string `json:"currentPassword"`
		NewPassword     *string `json:"newPassword"`
		RefreshToken    *string `json:"refreshToken"`
	}
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if err := requireStrings("currentPassword", p.CurrentPassword, "newPassword", p.NewPassword); err != nil {
		return err
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, sess.UserID, "User not found")
	if err != nil {
		return err
	}
	ok, err := users.VerifyPassword(*p.CurrentPassword, u.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.BadRequest("INVALID_CURRENT_PASSWORD")
	}
	// The same rules as registration, set-password and reset. Vapor skipped
	// them here, so a signed-in user could change to any password at all.
	if err := validatePassword(*p.NewPassword); err != nil {
		return err
	}
	hash, err := users.HashPassword(*p.NewPassword)
	if err != nil {
		return err
	}
	spared := sparedSession(sess, p.RefreshToken)
	err = pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		now := s.now()
		if err := s.users.Q(tx).SetUserPassword(ctx, usersdb.SetUserPasswordParams{
			ID: u.ID, PasswordHash: hash, PasswordSetAt: &now, EmailVerifiedAt: u.EmailVerifiedAt, UpdatedAt: &now,
		}); err != nil {
			return err
		}
		if err := s.users.RewritePII(ctx, tx, u, now); err != nil {
			return err
		}
		return users.RevokeAllTokens(ctx, tx, u.ID, spared, now)
	})
	if err != nil {
		return err
	}
	return httpx.OK(c, map[string]any{"success": true, "message": "Password updated successfully"})
}

func (s *service) authorizedUserID(c *echo.Context) (uuid.UUID, error) {
	sess, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || len(c.Param("id")) != 36 {
		return uuid.Nil, httpx.BadRequest("Invalid user ID")
	}
	if id != sess.UserID {
		return uuid.Nil, httpx.Forbidden("You are not allowed to access this user profile")
	}
	return id, nil
}

func (s *service) getProfileByID(c *echo.Context) error {
	id, err := s.authorizedUserID(c)
	if err != nil {
		return err
	}
	return s.writeProfile(c, id)
}

func (s *service) updateProfileByID(c *echo.Context) error {
	id, err := s.authorizedUserID(c)
	if err != nil {
		return err
	}
	return s.update(c, id)
}
