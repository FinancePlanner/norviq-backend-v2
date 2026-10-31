// Package notifications ports PushNotificationsController (APNS device
// registry, earnings reminder preferences) and the notification inbox routes
// of WealthAutomationController (/v1/notifications/inbox…).
package notifications

import (
	"context"

	"norviq-api/internal/auth"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/notificationsdb"

	"github.com/google/uuid"
)

// PremiumGate is the slice of billing's UsageCounterService this feature
// needs: `requirePremium(.targetAlerts, userId:)`. It returns the billing
// upgrade error (Vapor's billingUpgradeError) for a free user.
type PremiumGate interface {
	RequirePremium(ctx context.Context, feature string, userID uuid.UUID) error
}

// BillingFeatureTargetAlerts is BillingFeature.targetAlerts.
const BillingFeatureTargetAlerts = "target_alerts"

// Feature mounts /v1/notifications/*.
type Feature struct {
	// Premium builds the billing gate. The earnings preference routes need
	// it, so they are not registered (404) while it is nil.
	Premium func(d *server.Deps) PremiumGate
}

func (Feature) Name() string { return "notifications" }

type handlers struct {
	d       *server.Deps
	push    *push.Service
	q       notificationsdb.Querier
	premium PremiumGate
}

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	svc, err := push.From(d)
	if err != nil {
		return err
	}
	d.Health.Add("apns", svc.HealthCheck)

	h := &handlers{d: d, push: svc, q: notificationsdb.New(d.DB)}
	read := []echoMW{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeNotificationsRead)}
	write := []echoMW{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeNotificationsWrite)}

	// PushNotificationsController.
	r.V1.PUT("/notifications/apns/device", h.registerDevice, write...)
	r.V1.POST("/notifications/apns/device/deactivate", h.deactivateDevice, write...)
	if f.Premium != nil {
		h.premium = f.Premium(d)
		r.V1.GET("/notifications/earnings/preferences", h.getEarningsPreferences, read...)
		r.V1.PUT("/notifications/earnings/preferences", h.updateEarningsPreferences, write...)
	}

	// WealthAutomationController's inbox.
	r.V1.GET("/notifications/inbox", h.inbox, read...)
	r.V1.POST("/notifications/inbox/read-all", h.readAll, write...)
	r.V1.PUT("/notifications/inbox/:eventID/read", h.updateReadState, write...)
	return nil
}
