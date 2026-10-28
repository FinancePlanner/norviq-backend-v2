package identity

import (
	"encoding/json"
	"slices"
	"sort"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/onboarding"
	"norviq-api/internal/store/onboardingdb"
	"norviq-api/internal/store/usersdb"

	"github.com/labstack/echo/v5"
)

// --- AI consent ---------------------------------------------------------------

// aiConsentResponse always carries the key, null included.
type aiConsentResponse struct {
	ConsentedAt *string `json:"consentedAt"`
}

func consentBody(t *apijson.Time) aiConsentResponse {
	if t == nil {
		return aiConsentResponse{}
	}
	s := apijson.FormatTime(t.Time)
	return aiConsentResponse{ConsentedAt: &s}
}

func (s *service) aiConsentShow(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	u, err := s.requireUser(c.Request().Context(), sess.UserID, "User not found")
	if err != nil {
		return err
	}
	return httpx.OK(c, consentBody(apijson.TimePtr(u.AiDataConsentAt)))
}

// aiConsentGrant keeps the first timestamp: it records when the user first agreed.
func (s *service) aiConsentGrant(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, sess.UserID, "User not found")
	if err != nil {
		return err
	}
	if u.AiDataConsentAt == nil {
		now := s.now()
		if err := s.users.Q(nil).SetUserAIConsent(ctx, usersdb.SetUserAIConsentParams{ID: u.ID, AiDataConsentAt: &now, UpdatedAt: &now}); err != nil {
			return err
		}
		u.AiDataConsentAt = &now
	}
	return httpx.OK(c, consentBody(apijson.TimePtr(u.AiDataConsentAt)))
}

func (s *service) aiConsentWithdraw(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, sess.UserID, "User not found")
	if err != nil {
		return err
	}
	if u.AiDataConsentAt != nil {
		now := s.now()
		if err := s.users.Q(nil).SetUserAIConsent(ctx, usersdb.SetUserAIConsentParams{ID: u.ID, AiDataConsentAt: nil, UpdatedAt: &now}); err != nil {
			return err
		}
	}
	return httpx.OK(c, aiConsentResponse{})
}

// --- onboarding ---------------------------------------------------------------

var clientWritableOnboardingKeys = []string{
	"funnelStep", "funnel_step", "funnelCompleted", "funnel_completed",
	"guidedStartDismissed", "guided_start_dismissed",
}

var funnelSteps = []string{"questionnaire", "paywall", "welcome", "import", "budget", "done"}

// onboardingStateDTO is StockPlanShared.OnboardingStateDTO.
type onboardingStateDTO struct {
	FunnelStep             *string       `json:"funnelStep,omitempty"`
	FunnelCompletedAt      *apijson.Time `json:"funnelCompletedAt,omitempty"`
	AddHoldingCompleted    bool          `json:"addHoldingCompleted"`
	FirstHoldingAt         *apijson.Time `json:"firstHoldingAt,omitempty"`
	SetBudgetCompleted     bool          `json:"setBudgetCompleted"`
	FirstBudgetAt          *apijson.Time `json:"firstBudgetAt,omitempty"`
	SetGoalCompleted       bool          `json:"setGoalCompleted"`
	FirstGoalAt            *apijson.Time `json:"firstGoalAt,omitempty"`
	GuidedStartDismissedAt *apijson.Time `json:"guidedStartDismissedAt,omitempty"`
}

func onboardingDTO(r onboardingdb.OnboardingState) onboardingStateDTO {
	return onboardingStateDTO{
		FunnelStep: r.FunnelStep, FunnelCompletedAt: apijson.TimePtr(r.FunnelCompletedAt),
		AddHoldingCompleted: r.FirstHoldingAt != nil, FirstHoldingAt: apijson.TimePtr(r.FirstHoldingAt),
		SetBudgetCompleted: r.FirstBudgetAt != nil, FirstBudgetAt: apijson.TimePtr(r.FirstBudgetAt),
		SetGoalCompleted: r.FirstGoalAt != nil, FirstGoalAt: apijson.TimePtr(r.FirstGoalAt),
		GuidedStartDismissedAt: apijson.TimePtr(r.GuidedStartDismissedAt),
	}
}

func (s *service) onboardingGet(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	row, err := onboarding.FetchOrCreate(c.Request().Context(), s.d.DB, sess.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, onboardingDTO(row))
}

func (s *service) onboardingPatch(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	body, err := readBody(c)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return httpx.BadRequest("Expected a JSON object")
	}
	keys := make([]string, 0, len(object))
	for k := range object {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(clientWritableOnboardingKeys, k) {
			return httpx.BadRequest(k + " is set by the server, not by clients")
		}
	}
	var p struct {
		FunnelStep           *string `json:"funnelStep"`
		FunnelCompleted      *bool   `json:"funnelCompleted"`
		GuidedStartDismissed *bool   `json:"guidedStartDismissed"`
	}
	if err := apijson.Unmarshal(body, &p); err != nil {
		return httpx.BadRequest(err.Error())
	}
	if p.FunnelCompleted != nil && !*p.FunnelCompleted {
		return httpx.BadRequest("funnelCompleted is one-way")
	}
	if p.FunnelStep != nil && !slices.Contains(funnelSteps, *p.FunnelStep) {
		return httpx.BadRequest("Unknown funnel step " + *p.FunnelStep)
	}
	row, err := onboarding.Apply(c.Request().Context(), s.d.DB, sess.UserID, onboarding.Patch{
		FunnelStep: p.FunnelStep, FunnelCompleted: p.FunnelCompleted, GuidedStartDismissed: p.GuidedStartDismissed,
	}, s.now())
	if err != nil {
		return err
	}
	return httpx.OK(c, onboardingDTO(row))
}
