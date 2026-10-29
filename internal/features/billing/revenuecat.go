package billing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
)

// revenueCat is the REST client BillingController used through req.client.
//
// Vapor decoded these responses with the global JSONDecoder.backendAPI,
// whose key strategy rewrites every snake_case key to camelCase before the
// CodingKeys lookup. The structs below map snake_case names, so every
// snake_case field decodes as nil in Vapor (dictionary keys such as
// "pro_access" are left alone). v2 reproduces that by decoding with
// apijson.Unmarshal, which applies the same strategy. This is a Vapor bug, ported on
// purpose and listed in the port report.
type revenueCat struct {
	base   string
	client *http.Client
}

// v2Subscription is BillingController.RevenueCatV2Subscription.
type v2Subscription struct {
	ID                  *string `json:"id"`
	Store               *string `json:"store"`
	Status              *string `json:"status"`
	GivesAccess         *bool   `json:"gives_access"`
	ManagementURL       *string `json:"management_url"`
	CurrentPeriodEndsAt *int64  `json:"current_period_ends_at"`
	EndsAt              *int64  `json:"ends_at"`
}

type v2SubscriptionList struct {
	Items *[]v2Subscription `json:"items"`
}

type authenticatedManagementURL struct {
	ManagementURL *string `json:"management_url"`
}

// rcEntitlement is BillingController.RevenueCatEntitlement.
type rcEntitlement struct {
	ExpiresDate       *string `json:"expires_date"`
	PurchaseDate      *string `json:"purchase_date"`
	ProductIdentifier *string `json:"product_identifier"`
}

// rcSubscription is BillingController.RevenueCatSubscription.
type rcSubscription struct {
	ExpiresDate             *string `json:"expires_date"`
	PurchaseDate            *string `json:"purchase_date"`
	OriginalPurchaseDate    *string `json:"original_purchase_date"`
	PeriodType              *string `json:"period_type"`
	Store                   *string `json:"store"`
	UnsubscribeDetectedAt   *string `json:"unsubscribe_detected_at"`
	BillingIssuesDetectedAt *string `json:"billing_issues_detected_at"`
	GracePeriodExpiresDate  *string `json:"grace_period_expires_date"`
}

// subscriber is BillingController.RevenueCatSubscriber.
type subscriber struct {
	Entitlements  map[string]rcEntitlement  `json:"entitlements"`
	Subscriptions map[string]rcSubscription `json:"subscriptions"`
}

type subscriberResponse struct {
	Subscriber *struct {
		Entitlements  *map[string]rcEntitlement  `json:"entitlements"`
		Subscriptions *map[string]rcSubscription `json:"subscriptions"`
	} `json:"subscriber"`
}

// pathSegment percent-encodes like CharacterSet.urlPathAllowed minus "/".
func pathSegment(s string) string {
	const allowed = "!$&'()*+,-.:;=@_~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.IndexByte(allowed, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (rc *revenueCat) get(ctx context.Context, url, apiKey string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := rc.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// subscriptions is fetchRevenueCatSubscriptions.
func (rc *revenueCat) subscriptions(ctx context.Context, projectID, userID, apiKey string) ([]v2Subscription, error) {
	url := fmt.Sprintf("%s/v2/projects/%s/customers/%s/subscriptions?limit=20", rc.base, pathSegment(projectID), pathSegment(userID))
	status, body, err := rc.get(ctx, url, apiKey)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("RevenueCat subscription lookup failed with status %d.", status))
	}
	invalid := httpx.Abort(http.StatusBadGateway, "RevenueCat subscription list response was invalid.")
	var list v2SubscriptionList
	if err := apijson.Unmarshal(body, &list); err != nil || list.Items == nil {
		return nil, invalid
	}
	for _, s := range *list.Items {
		if s.ID == nil { // `id` is a non-optional String in Swift
			return nil, invalid
		}
	}
	return *list.Items, nil
}

// authenticatedManagementURL is fetchAuthenticatedManagementURL: nil on any
// failure. With Vapor's key rewrite the required management_url never
// decodes, so this is always nil after the request is made.
func (rc *revenueCat) authenticatedManagementURL(ctx context.Context, projectID, subscriptionID, apiKey string) (*string, error) {
	url := fmt.Sprintf("%s/v2/projects/%s/subscriptions/%s/authenticated_management_url", rc.base, pathSegment(projectID), pathSegment(subscriptionID))
	status, body, err := rc.get(ctx, url, apiKey)
	if err != nil {
		return nil, err
	}
	return managementURLFrom(status, body), nil
}

func managementURLFrom(status int, body []byte) *string {
	if status != http.StatusOK {
		return nil
	}
	var out authenticatedManagementURL
	if apijson.Unmarshal(body, &out) != nil {
		return nil
	}
	return out.ManagementURL
}

// subscriber is fetchRevenueCatSubscriber.
func (rc *revenueCat) subscriber(ctx context.Context, userID, apiKey string) (*subscriber, error) {
	url := fmt.Sprintf("%s/v1/subscribers/%s", rc.base, pathSegment(userID))
	status, body, err := rc.get(ctx, url, apiKey)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, httpx.Abort(http.StatusBadGateway, fmt.Sprintf("RevenueCat subscriber lookup failed with status %d.", status))
	}
	var resp subscriberResponse
	if err := apijson.Unmarshal(body, &resp); err != nil || resp.Subscriber == nil ||
		resp.Subscriber.Entitlements == nil || resp.Subscriber.Subscriptions == nil {
		return nil, httpx.Abort(http.StatusBadGateway, "RevenueCat subscriber response was invalid.")
	}
	return &subscriber{Entitlements: *resp.Subscriber.Entitlements, Subscriptions: *resp.Subscriber.Subscriptions}, nil
}

// managementScore is managementSubscriptionScore.
func managementScore(s v2Subscription) int {
	if s.GivesAccess != nil && *s.GivesAccess {
		return 4
	}
	if s.Status == nil {
		return 0
	}
	switch strings.ToLower(*s.Status) {
	case "active", "trialing", "grace_period":
		return 3
	case entitlements.StatusCancelled:
		return 2
	case "billing_issue":
		return 1
	}
	return 0
}

func periodEnd(s v2Subscription) int64 {
	switch {
	case s.CurrentPeriodEndsAt != nil:
		return *s.CurrentPeriodEndsAt
	case s.EndsAt != nil:
		return *s.EndsAt
	}
	return 0
}

// preferredManagementSubscription is the first of the list sorted by
// managementSubscriptionSort (score, then latest period end), keeping list
// order among ties.
func preferredManagementSubscription(subs []v2Subscription) *v2Subscription {
	var best *v2Subscription
	for i := range subs {
		s := &subs[i]
		if best == nil {
			best = s
			continue
		}
		bs, ss := managementScore(*best), managementScore(*s)
		if ss > bs || (ss == bs && periodEnd(*s) > periodEnd(*best)) {
			best = s
		}
	}
	return best
}

// isWebBillingStore is isRevenueCatWebBillingStore.
func isWebBillingStore(store *string) bool {
	if store == nil {
		return false
	}
	switch strings.ToLower(*store) {
	case "rc_billing", "revenuecat", "web_billing", "revenuecat_web_billing":
		return true
	}
	return false
}
