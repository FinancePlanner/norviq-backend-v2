package terminalpositions

import (
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/swiftjson"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// pathID is the controller's id(_:): `req.parameters.get("id", as:
// UUID.self)`, else 400 "invalid id".
func pathID(c *echo.Context) (uuid.UUID, error) {
	id, ok := parseSwiftUUID(c.Param("id"))
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("invalid id")
	}
	return id, nil
}

func (h *handlers) index(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rows, err := h.svc.List(ctx, s.UserID, httpx.QueryString(c, "ticker"))
	if err != nil {
		return err
	}
	out, err := h.svc.ListResponseFor(ctx, s.UserID, rows)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) summary(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Summary(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) create(c *echo.Context) error {
	var in CreateRequest
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	row, err := h.svc.Create(c.Request().Context(), s.UserID, in)
	if err != nil {
		return err
	}
	achievementhooks.Schedule(s.UserID)
	return httpx.Created(c, ToResponse(row))
}

func (h *handlers) update(c *echo.Context) error {
	var in UpdateRequest
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	row, err := h.svc.Update(c.Request().Context(), s.UserID, id, in)
	if err != nil {
		return err
	}
	return httpx.OK(c, ToResponse(row))
}

func (h *handlers) delete(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	if err := h.svc.Delete(c.Request().Context(), s.UserID, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) duplicate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	row, err := h.svc.Duplicate(c.Request().Context(), s.UserID, id)
	if err != nil {
		return err
	}
	return httpx.Created(c, ToResponse(row))
}

func (h *handlers) reorder(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var in OrderRequest
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	ctx := c.Request().Context()
	rows, err := h.svc.Reorder(ctx, s.UserID, in.IDs)
	if err != nil {
		return err
	}
	out, err := h.svc.ListResponseFor(ctx, s.UserID, rows)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// MARK: - TerminalPositionsAIController

// advisor is the AI controller's advisor(_:): Pro, then AI consent, then a
// configured client, all before the body is read.
func (h *handlers) advisor(c *echo.Context) (Advisor, auth.Session, error) {
	s, err := auth.Require(c)
	if err != nil {
		return Advisor{}, auth.Session{}, err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureTerminalPositionAI, s.UserID); err != nil {
		return Advisor{}, auth.Session{}, err
	}
	if err := h.ai.Consent.Require(c, s.UserID); err != nil {
		return Advisor{}, auth.Session{}, err
	}
	client, ok := AIClient(h.d)
	if !ok {
		return Advisor{}, auth.Session{}, errUnavailable()
	}
	return Advisor{Client: client, Logger: h.d.Logger}, *s, nil
}

func (h *handlers) shareFacts(c *echo.Context) error {
	adv, s, err := h.advisor(c)
	if err != nil {
		return err
	}
	var in ShareFactsRequest
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	ctx := c.Request().Context()
	currency, err := h.svc.Currency(ctx, s.UserID)
	if err != nil {
		return err
	}
	ticker, err := NormalisedTicker(in.Ticker)
	if err != nil {
		return err
	}
	out, err := adv.ShareFacts(ctx, ticker, currency)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) scenario(c *echo.Context) error {
	adv, s, err := h.advisor(c)
	if err != nil {
		return err
	}
	var in ScenarioSuggestionRequest
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	ctx := c.Request().Context()
	currency, err := h.svc.Currency(ctx, s.UserID)
	if err != nil {
		return err
	}
	ticker, err := NormalisedTicker(in.Ticker)
	if err != nil {
		return err
	}
	out, err := adv.Scenario(ctx, ticker, in.HorizonYears, currency)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
