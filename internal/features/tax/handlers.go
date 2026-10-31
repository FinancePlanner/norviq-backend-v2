package tax

import (
	"errors"
	"strconv"
	"strings"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type handlers struct {
	svc     *service
	storage reportStorage
}

func (h *handlers) currentTaxYear() int { return h.svc.now().UTC().Year() }

func (h *handlers) requireTaxPro(c *echo.Context, userID uuid.UUID) error {
	return h.svc.ent.RequirePremium(c.Request().Context(), entitlements.FeatureTaxOptimization, userID)
}

// requireFilingPackAccess is Pro, or a purchased pack for exactly this tax
// year; the upgrade error is rethrown untouched.
func (h *handlers) requireFilingPackAccess(c *echo.Context, userID uuid.UUID, taxYear int) error {
	err := h.requireTaxPro(c, userID)
	var upgrade *entitlements.UpgradeRequiredError
	if !errors.As(err, &upgrade) {
		return err
	}
	has, herr := h.svc.ent.HasTaxPack(c.Request().Context(), userID, taxYear)
	if herr != nil {
		return herr
	}
	if !has {
		return err
	}
	return nil
}

// session is req.auth.require(SessionToken.self) plus the TaxQuery decode
// most handlers start with.
func (h *handlers) session(c *echo.Context) (uuid.UUID, taxQuery, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.UUID{}, taxQuery{}, err
	}
	q, err := decodeTaxQuery(c)
	return s.UserID, q, err
}

func (h *handlers) yearOr(q taxQuery) int {
	if q.TaxYear != nil {
		return *q.TaxYear
	}
	return h.currentTaxYear()
}

func (h *handlers) capabilities(c *echo.Context) error {
	_, q, err := h.session(c)
	if err != nil {
		return err
	}
	return httpx.OK(c, h.svc.capabilities(h.yearOr(q)))
}

func (h *handlers) lossCarryforwards(c *echo.Context) error {
	userID, q, err := h.session(c)
	if err != nil {
		return err
	}
	j, year := q.jurisdiction(), h.yearOr(q)
	ctx := c.Request().Context()
	var out LossCarryforwardLedger
	switch j {
	case DE:
		out, err = h.svc.germanyLossResponse(ctx, userID, year)
	case PT, ES, US:
		out, err = ptLedger{h.svc}.response(ctx, userID, j, year)
	default:
		out = LossCarryforwardLedger{
			GeneratedAt: isoSeconds(h.svc.now()), Jurisdiction: j, AsOfTaxYear: year,
			TotalAvailable: money(zero, defaultCurrency(j)), Balances: []LossCarryforwardBalance{},
		}
	}
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) getProfile(c *echo.Context) error {
	userID, q, err := h.session(c)
	if err != nil {
		return err
	}
	p, err := h.svc.profile(c.Request().Context(), userID, q.jurisdiction(), h.yearOr(q))
	if err != nil {
		return err
	}
	if p == nil {
		return httpx.NotFound("Tax profile not found.")
	}
	return httpx.OK(c, p)
}

func (h *handlers) getProfileContext(c *echo.Context) error {
	userID, q, err := h.session(c)
	if err != nil {
		return err
	}
	out, err := h.svc.profileContext(c.Request().Context(), userID, q.jurisdiction(), h.yearOr(q))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) saveProfile(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requireTaxPro(c, s.UserID); err != nil {
		return err
	}
	var req ProfileRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if req.TaxYear < 2020 || req.TaxYear > h.currentTaxYear()+1 {
		return httpx.Unprocessable("Tax year is outside the supported range.")
	}
	out, err := h.svc.saveProfile(c.Request().Context(), s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func pathUUID(c *echo.Context, name string) (uuid.UUID, bool) { return parseUUID(c.Param(name)) }

// saveMarketAdmission is Pro-gated like the other tax writes, and the gate
// runs before the attestation 422. (Vapor had no gate here: a free user could
// classify instruments, and got the 422 before any entitlement check.)
func (h *handlers) saveMarketAdmission(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	id, ok := pathUUID(c, "instrumentId")
	if !ok {
		return httpx.BadRequest("Invalid instrument id.")
	}
	var req MarketAdmissionRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if req.Status != "unknown" && strings.ToLower(c.Request().Header.Get("X-Tax-Evidence-Attested")) != "true" {
		return httpx.Unprocessable("Verify market admission against a broker statement or official listing before classification.")
	}
	out, err := h.svc.saveMarketAdmission(c.Request().Context(), userID, id, req.Status)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) saveFundClassification(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, ok := pathUUID(c, "instrumentId")
	if !ok {
		return httpx.BadRequest("Invalid instrument id.")
	}
	var req FundClassificationRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.saveFundClassification(c.Request().Context(), s.UserID, id, req.Classification)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) saveFundAnnualInput(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requireTaxPro(c, s.UserID); err != nil {
		return err
	}
	var req FundAnnualInputRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.saveFundAnnualInput(c.Request().Context(), s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// queryKeyMissing is the URL-query decoder's valueNotFound reason; it
// decodes a UUID through String.
func queryKeyMissing(key, swiftType string) error {
	return httpx.BadRequest("No value found (expected type '" + swiftType + "') at path '" + key + "'. No " + swiftType + " was found at '" + key + "'.")
}

// queryIntMismatch is the URL-query decoder's typeMismatch reason for Int.
func queryIntMismatch(key string) error {
	return httpx.BadRequest("Value was not of type 'Int' at path '" + key + "'. Data found at '" + key + "' was not Int.")
}

func queryUUID(c *echo.Context, key string) (uuid.UUID, error) {
	raw, ok := lastQueryValue(c, key)
	if !ok {
		return uuid.UUID{}, queryKeyMissing(key, "String")
	}
	id, ok := parseUUID(raw)
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Data corrupted at path '" + key + "'. Attempted to decode UUID from invalid UUID string.")
	}
	return id, nil
}

func (h *handlers) getFundAnnualInput(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	accountID, err := queryUUID(c, "accountId")
	if err != nil {
		return err
	}
	instrumentID, err := queryUUID(c, "instrumentId")
	if err != nil {
		return err
	}
	raw, ok := lastQueryValue(c, "calculationYear")
	if !ok {
		return queryKeyMissing("calculationYear", "Int")
	}
	year, err := strconv.Atoi(raw)
	if err != nil {
		return queryIntMismatch("calculationYear")
	}
	out, err := h.svc.fundAnnualInput(c.Request().Context(), s.UserID, accountID, instrumentID, year)
	if err != nil {
		return err
	}
	if out == nil {
		return httpx.NotFound("Annual fund input not found.")
	}
	return httpx.OK(c, out)
}

func (h *handlers) dashboard(c *echo.Context) error {
	userID, q, err := h.session(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	out, err := h.svc.dashboard(ctx, userID, q.jurisdiction(), h.yearOr(q))
	if err != nil {
		return err
	}
	pro, err := h.svc.ent.IsPro(ctx, userID)
	if err != nil {
		return err
	}
	if !pro {
		out.Opportunities = []OpportunityResponse{}
		out.Assumptions = append(append([]string{}, out.Assumptions...), "Upgrade to Pro to review individual opportunities and run scenarios.")
		empty := []LocationOpportunity{}
		out.LocationOpportunities = &empty
	}
	return httpx.OK(c, out)
}

// proThenQuery is the common head of the Pro-gated handlers.
func (h *handlers) proThenQuery(c *echo.Context) (uuid.UUID, taxQuery, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.UUID{}, taxQuery{}, err
	}
	if err := h.requireTaxPro(c, s.UserID); err != nil {
		return uuid.UUID{}, taxQuery{}, err
	}
	q, err := decodeTaxQuery(c)
	return s.UserID, q, err
}

func (h *handlers) pro(c *echo.Context) (uuid.UUID, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.UUID{}, err
	}
	return s.UserID, h.requireTaxPro(c, s.UserID)
}

func (h *handlers) createScenario(c *echo.Context) error {
	userID, q, err := h.proThenQuery(c)
	if err != nil {
		return err
	}
	var req ScenarioRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.createScenario(c.Request().Context(), userID, req, q.jurisdiction())
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) getScenario(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	notFound := httpx.NotFound("Tax scenario not found.")
	id, ok := pathUUID(c, "scenarioId")
	if !ok {
		return notFound
	}
	out, err := h.svc.scenario(c.Request().Context(), userID, id)
	if err != nil {
		return err
	}
	if out == nil {
		return notFound
	}
	return httpx.OK(c, out)
}

func (h *handlers) createActionPlan(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	var req ActionPlanRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.createActionPlan(c.Request().Context(), userID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) listActionPlans(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	out, err := h.svc.actionPlans(c.Request().Context(), userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) getActionPlan(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	notFound := httpx.NotFound("Tax action plan not found.")
	id, ok := pathUUID(c, "actionPlanId")
	if !ok {
		return notFound
	}
	out, err := h.svc.actionPlan(c.Request().Context(), userID, id)
	if err != nil {
		return err
	}
	if out == nil {
		return notFound
	}
	return httpx.OK(c, out)
}

func (h *handlers) transitionActionPlan(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	id, ok := pathUUID(c, "actionPlanId")
	if !ok {
		return httpx.BadRequest("Invalid tax action plan id.")
	}
	var req ActionPlanTransitionRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.transitionActionPlan(c.Request().Context(), userID, id, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) createLocationScenario(c *echo.Context) error {
	userID, q, err := h.proThenQuery(c)
	if err != nil {
		return err
	}
	var req LocationScenarioRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.createLocationScenario(c.Request().Context(), userID, req, q.jurisdiction())
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) createPlacementPlan(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	var req ActionPlanRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.createPlacementPlan(c.Request().Context(), userID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) opportunityID(c *echo.Context) (string, error) {
	id := c.Param("opportunityId")
	if id == "" {
		return "", httpx.BadRequest("Invalid tax opportunity id.")
	}
	return id, nil
}

func (h *handlers) dismissOpportunity(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	id, err := h.opportunityID(c)
	if err != nil {
		return err
	}
	q, err := decodeTaxQuery(c)
	if err != nil {
		return err
	}
	if err := h.svc.dismissOpportunity(c.Request().Context(), userID, id, q.jurisdiction(), h.yearOr(q)); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) restoreOpportunity(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	id, err := h.opportunityID(c)
	if err != nil {
		return err
	}
	q, err := decodeTaxQuery(c)
	if err != nil {
		return err
	}
	if err := h.svc.restoreOpportunity(c.Request().Context(), userID, id, h.yearOr(q)); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) getNotificationPreferences(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	out, err := h.svc.notificationPreferences(c.Request().Context(), userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) saveNotificationPreferences(c *echo.Context) error {
	userID, err := h.pro(c)
	if err != nil {
		return err
	}
	var req NotificationPreferences
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	out, err := h.svc.saveNotificationPreferences(c.Request().Context(), userID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
