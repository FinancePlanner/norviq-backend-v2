// Package community is CommunityAccess.swift: the community viewer (who is
// asking, with their mute, ban, guidelines acceptance and social suspension)
// and CommunityAccessMiddleware. Boards, Articles and Groups all mount it.
package community

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/communityaccessdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Sanction kinds (CommunitySanctionKind raw values).
const (
	SanctionMute = "mute"
	SanctionBan  = "ban"
)

// Sanction is a CommunitySanction row.
type Sanction = communityaccessdb.CommunitySanction

// IsActive is CommunitySanction.isActive(at:).
func IsActive(s *Sanction, now time.Time) bool {
	return s.RevokedAt == nil && (s.ExpiresAt == nil || s.ExpiresAt.After(now))
}

// Viewer is CommunityViewer.
type Viewer struct {
	UserID             uuid.UUID
	Username           *string
	IsAdmin            bool
	ActiveSanction     *Sanction
	GuidelinesAccepted bool
	// SocialSuspendedAt is when a moderator suspended the user from social; a
	// suspension removes Boards too, exactly like a ban.
	SocialSuspendedAt *time.Time
}

// IsBanned is CommunityViewer.isBanned.
func (v *Viewer) IsBanned() bool {
	return !v.IsAdmin && ((v.ActiveSanction != nil && v.ActiveSanction.Kind == SanctionBan) || v.SocialSuspendedAt != nil)
}

// IsMuted is CommunityViewer.isMuted.
func (v *Viewer) IsMuted() bool {
	return !v.IsAdmin && v.ActiveSanction != nil && v.ActiveSanction.Kind == SanctionMute
}

// RequireCanContribute is CommunityViewer.requireCanContribute: the gate for
// anything that adds content or a vote. Reporting and blocking skip it.
func (v *Viewer) RequireCanContribute() error {
	if v.IsMuted() {
		err := httpx.CodedAbort(http.StatusForbidden, "community_muted",
			"You're muted in the Norviq community and can't post, comment, vote or chat right now.")
		if v.ActiveSanction.ExpiresAt != nil {
			err = err.WithDetails(map[string]string{"expiresAt": apijson.FormatTime(*v.ActiveSanction.ExpiresAt)})
		}
		return err
	}
	if v.Username == nil || *v.Username == "" {
		return httpx.CodedAbort(http.StatusForbidden, "username_required", "Pick a username before posting.")
	}
	if !v.GuidelinesAccepted && !v.IsAdmin {
		return httpx.CodedAbort(http.StatusForbidden, "guidelines_required",
			"Accept the community guidelines before posting.")
	}
	return nil
}

// Access resolves viewers. IsAdmin is the NORVIQ_ADMIN_USER_IDS allowlist
// (server.Deps.Admin.IsAdmin).
type Access struct {
	Q       communityaccessdb.Querier
	IsAdmin func(uuid.UUID) bool
	Now     func() time.Time
}

// New builds an Access over db.
func New(db communityaccessdb.DBTX, isAdmin func(uuid.UUID) bool) *Access {
	return &Access{Q: communityaccessdb.New(db), IsAdmin: isAdmin}
}

func (a *Access) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// ActiveSanction is CommunityAccess.activeSanction: the sanction in force,
// if any. A ban outranks a mute.
func (a *Access) ActiveSanction(ctx context.Context, userID uuid.UUID) (*Sanction, error) {
	rows, err := a.Q.AccessUnrevokedSanctions(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := a.now()
	var first *Sanction
	for i := range rows {
		if !IsActive(&rows[i], now) {
			continue
		}
		if rows[i].Kind == SanctionBan {
			return &rows[i], nil
		}
		if first == nil {
			first = &rows[i]
		}
	}
	return first, nil
}

// Viewer is CommunityAccess.viewer. A missing user is a 401.
func (a *Access) Viewer(ctx context.Context, userID uuid.UUID) (*Viewer, error) {
	username, err := a.Q.AccessUsername(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Abort(http.StatusUnauthorized, "")
	}
	if err != nil {
		return nil, err
	}
	accepted, err := a.Q.AccessGuidelinesAccepted(ctx, userID)
	if err != nil {
		return nil, err
	}
	suspended, err := a.Q.AccessSocialSuspendedAt(ctx, userID)
	if err != nil {
		return nil, err
	}
	sanction, err := a.ActiveSanction(ctx, userID)
	if err != nil {
		return nil, err
	}
	v := &Viewer{
		UserID:             userID,
		Username:           username,
		IsAdmin:            a.IsAdmin != nil && a.IsAdmin(userID),
		ActiveSanction:     sanction,
		GuidelinesAccepted: accepted,
	}
	if len(suspended) > 0 {
		v.SocialSuspendedAt = suspended[0]
	}
	return v, nil
}

const viewerKey = "community.viewer"

// Middleware is CommunityAccessMiddleware: resolves the viewer and turns
// banned users away from every route except `/community/me`, which has to
// answer so the app can explain why.
func (a *Access) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			s, err := auth.Require(c)
			if err != nil {
				return err
			}
			v, err := a.Viewer(c.Request().Context(), s.UserID)
			if err != nil {
				return err
			}
			c.Set(viewerKey, v)
			if v.IsBanned() && !strings.HasSuffix(c.Request().URL.Path, "/community/me") {
				return httpx.CodedAbort(http.StatusForbidden, "community_banned",
					"Your access to the Norviq community has been removed.")
			}
			return next(c)
		}
	}
}

// FromContext is req.communityViewer: a 500 if the middleware did not run.
func FromContext(c *echo.Context) (*Viewer, error) {
	if v, ok := c.Get(viewerKey).(*Viewer); ok {
		return v, nil
	}
	return nil, httpx.Abort(http.StatusInternalServerError, "Community viewer not resolved")
}
