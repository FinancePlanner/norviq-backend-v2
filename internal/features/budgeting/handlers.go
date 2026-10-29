package budgeting

import (
	"regexp"
	"strconv"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type handlers struct {
	engine  *Engine
	premium PremiumGate
}

// pathID is the controller's id(_:_:): a malformed UUID is a bare 400.
func pathID(c *echo.Context, name string) (uuid.UUID, error) {
	id, ok := parseUUID(c.Param(name))
	if !ok {
		return uuid.UUID{}, badRequest()
	}
	return id, nil
}

// requirePro gates every route but drift, dca-capacity, discipline and the
// alert policy behind Pro (BillingFeature.smartSuggestions).
func (h *handlers) requirePro(c *echo.Context, userID uuid.UUID) error {
	return h.premium.RequirePremium(c.Request().Context(), entitlements.FeatureSmartSuggestions, userID)
}

func (h *handlers) drift(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathID(c, "snapshotId")
	if err != nil {
		return err
	}
	out, err := h.engine.Dashboard(c.Request().Context(), nil, s.UserID, id)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) dcaCapacity(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.engine.spendToUnits(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) updateDcaCapacity(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var w dcaSymbolWire
	if err := httpx.Decode(c, &w); err != nil {
		return err
	}
	if w.Symbol == nil {
		return keyNotFound("symbol", "")
	}
	out, err := h.engine.updateDcaSymbol(c.Request().Context(), s.UserID, *w.Symbol)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

var throughPattern = regexp.MustCompile(`^(\d{1,4})-(\d{1,2})-(\d{1,2})$`)

// parseThrough is the controller's yyyy-MM-dd DateFormatter; anything it
// cannot read falls back to now.
func parseThrough(raw string, now time.Time) time.Time {
	m := throughPattern.FindStringSubmatch(raw)
	if m == nil {
		return now
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d {
		return now
	}
	return t
}

func (h *handlers) discipline(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	now := h.engine.now()
	through := now
	if raw, ok := c.QueryParams()["through"]; ok && len(raw) > 0 {
		through = parseThrough(raw[0], now)
	}
	// req.query[Int.self, at:] is optional: a malformed value is the default.
	months := 6
	if n, err := strconv.Atoi(c.QueryParam("months")); err == nil {
		months = n
	}
	out, err := h.engine.discipline(c.Request().Context(), s.UserID, through, months)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) updatePolicy(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathID(c, "snapshotId")
	if err != nil {
		return err
	}
	var w alertPolicyWire
	if err := httpx.Decode(c, &w); err != nil {
		return err
	}
	if err := w.validate(); err != nil {
		return err
	}
	out, err := h.engine.updatePolicy(c.Request().Context(), s.UserID, id, w)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) bulkUpdate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	id, err := pathID(c, "snapshotId")
	if err != nil {
		return err
	}
	var w bulkUpdateWire
	if err := httpx.Decode(c, &w); err != nil {
		return err
	}
	if err := w.validate(); err != nil {
		return err
	}
	out, err := h.engine.bulkUpdate(c.Request().Context(), s.UserID, id, w)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) preview(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	var w previewWire
	if err := httpx.Decode(c, &w); err != nil {
		return err
	}
	req, err := w.toRequest("")
	if err != nil {
		return err
	}
	out, err := h.engine.preview(c.Request().Context(), s.UserID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) commit(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	var w commitWire
	if err := httpx.Decode(c, &w); err != nil {
		return err
	}
	if w.RequestID == nil {
		return keyNotFound("requestId", "")
	}
	if w.Preview == nil {
		return keyNotFound("preview", "")
	}
	req, err := w.Preview.toRequest("preview")
	if err != nil {
		return err
	}
	out, err := h.engine.commit(c.Request().Context(), s.UserID, *w.RequestID, req)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) history(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	out, err := h.engine.history(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) templates(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	out, err := h.engine.listTemplates(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) createTemplate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	var w templateWire
	if err := httpx.Decode(c, &w); err != nil {
		return err
	}
	name, items, err := w.toRequest()
	if err != nil {
		return err
	}
	out, err := h.engine.createTemplate(c.Request().Context(), s.UserID, name, items)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) updateTemplate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	id, err := pathID(c, "templateId")
	if err != nil {
		return err
	}
	var w templateWire
	if err := httpx.Decode(c, &w); err != nil {
		return err
	}
	name, items, err := w.toRequest()
	if err != nil {
		return err
	}
	out, err := h.engine.updateTemplate(c.Request().Context(), s.UserID, id, name, items)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) deleteTemplate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	id, err := pathID(c, "templateId")
	if err != nil {
		return err
	}
	if err := h.engine.deleteTemplate(c.Request().Context(), s.UserID, id); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) applyTemplate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	if err := h.requirePro(c, s.UserID); err != nil {
		return err
	}
	id, err := pathID(c, "templateId")
	if err != nil {
		return err
	}
	var w applyWire
	if err := httpx.Decode(c, &w); err != nil {
		return err
	}
	if err := w.validate(); err != nil {
		return err
	}
	out, err := h.engine.applyTemplate(c.Request().Context(), s.UserID, id, w)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
