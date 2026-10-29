package identity

import (
	"context"

	"norviq-api/internal/platform/entitlements"

	"github.com/google/uuid"
)

// Premium is the one billing question identity asks (PATs, OAuth consent,
// introspection's `entitled`). *entitlements.Service satisfies it; a refusal
// is an *entitlements.UpgradeRequiredError, which the error handler renders
// as BillingErrorMiddleware's 403 body.
type Premium interface {
	RequirePremium(ctx context.Context, feature entitlements.Feature, userID uuid.UUID) error
}

func (s *service) requirePremium(ctx context.Context, userID uuid.UUID) error {
	return s.ent.RequirePremium(ctx, entitlements.FeatureMCPAccess, userID)
}
