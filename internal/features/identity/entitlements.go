package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/store/identitydb"
	"norviq-api/internal/store/usersdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Entitlements is the one billing question identity asks: which plan level
// a user is on. The billing port owns the real resolver; identity carries
// dbEntitlements, a faithful copy of DefaultEntitlementResolver, until the
// billing package exposes one this interface can wrap.
type Entitlements interface {
	Level(ctx context.Context, userID uuid.UUID) (string, error)
}

// isPro is EntitlementSnapshot.isPro.
func isPro(level string) bool {
	return level == "pro" || strings.HasPrefix(level, "pro_") ||
		level == "premium" || strings.HasPrefix(level, "premium_") || level == "temporary"
}

// UpgradeRequiredError is BillingUpgradeRequiredError for a premium-only
// feature. Vapor's BillingErrorMiddleware renders it outside the usual
// error envelope, so it writes its own body.
type UpgradeRequiredError struct {
	Feature string
	Plan    string
}

func (e *UpgradeRequiredError) Error() string {
	return fmt.Sprintf("Upgrade required. feature=%s plan=%s required=pro", e.Feature, e.Plan)
}

type upgradeRequiredBody struct {
	Success      bool   `json:"success"`
	Code         string `json:"code"`
	Error        string `json:"error"`
	Feature      string `json:"feature"`
	Plan         string `json:"plan"`
	RequiredPlan string `json:"requiredPlan"`
}

// writeUpgradeRequired renders err when it is an UpgradeRequiredError and
// returns it untouched otherwise.
func writeUpgradeRequired(c *echo.Context, err error) error {
	var up *UpgradeRequiredError
	if !errors.As(err, &up) {
		return err
	}
	return httpx.JSON(c, http.StatusForbidden, upgradeRequiredBody{
		Success: false, Code: "upgrade_required", Error: up.Error(),
		Feature: up.Feature, Plan: up.Plan, RequiredPlan: "pro",
	})
}

const featureMCPAccess = "mcp_access"

// requirePremium is UsageCounterService.requirePremium.
func (s *service) requirePremium(ctx context.Context, feature string, userID uuid.UUID) error {
	level, err := s.ent.Level(ctx, userID)
	if err != nil {
		return err
	}
	if !isPro(level) {
		return &UpgradeRequiredError{Feature: feature, Plan: level}
	}
	return nil
}

// dbEntitlements ports DefaultEntitlementResolver.
type dbEntitlements struct {
	db            usersdb.DBTX
	bypass        bool
	premiumEmails map[string]bool
	now           func() time.Time
}

func (e dbEntitlements) Level(ctx context.Context, userID uuid.UUID) (string, error) {
	if e.bypass {
		return "pro", nil
	}
	user, err := usersdb.New(e.db).UserByID(ctx, userID)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if found && len(e.premiumEmails) > 0 && user.EmailVerifiedAt != nil && e.premiumEmails[strings.ToLower(user.Email)] {
		return "pro", nil
	}
	level, err := identitydb.New(e.db).EntitlementLevelByUser(ctx, userID)
	hasEntitlement := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if found && user.TrialTier != nil && e.hasActiveTrial(user) && (!hasEntitlement || level == "free") {
		return *user.TrialTier, nil
	}
	if !hasEntitlement {
		return "free", nil
	}
	return level, nil
}

func (e dbEntitlements) hasActiveTrial(u usersdb.User) bool {
	if u.TrialStartedAt == nil || u.TrialDays == nil || *u.TrialDays <= 0 {
		return false
	}
	return e.now().Before(u.TrialStartedAt.Add(time.Duration(*u.TrialDays) * 24 * time.Hour))
}
