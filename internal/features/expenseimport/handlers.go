package expenseimport

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/multipartform"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type handlers struct {
	d   *server.Deps
	ai  *ai.Platform
	ent *entitlements.Service
	svc *Service
}

// sessionID is the controller's sessionID(_:).
func sessionID(c *echo.Context) (uuid.UUID, error) {
	raw := c.Param("sessionID")
	if len(raw) == 36 {
		if id, err := uuid.Parse(raw); err == nil {
			return id, nil
		}
	}
	return uuid.UUID{}, httpx.BadRequest("Invalid import id.")
}

func (h *handlers) analyze(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	// body: .collect(maxSize:) ran before the handler.
	body, err := multipartform.ReadBody(c.Request(), int64(DefaultLimits.MaxBytes))
	if err != nil {
		return err
	}
	// Gate before parsing so a free user never spends server time on a file
	// they can't import.
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureSpreadsheetImport, s.UserID); err != nil {
		return err
	}
	data, fileName, err := readUpload(c, body)
	if err != nil {
		return err
	}
	// The daily AI cap is charged only when a model will actually be called.
	if h.svc.Provider.IsEnabled() {
		ok, err := h.ai.Consent.IsAllowedRequest(c, s.UserID)
		if err != nil {
			return err
		}
		if ok {
			if err := h.ai.Cap.Enforce(ctx, s.UserID,
				"Spreadsheet import is unavailable right now. Try again shortly.",
				"You've reached today's AI limit. Try again tomorrow, or import a CSV.",
				ai.CapOptions{}); err != nil {
				return err
			}
		}
	}
	consent := func(context.Context, uuid.UUID) (bool, error) { return h.ai.Consent.IsAllowedRequest(c, s.UserID) }
	resp, err := h.svc.Analyze(ctx, data, fileName, s.UserID, consent)
	if err != nil {
		return err
	}
	h.d.Logger.Info(fmt.Sprintf("spreadsheet_import_analyze sheets=%d rows=%d ai=%t", len(resp.Sheets), resp.Preview.TotalRows, resp.AIAvailable))
	return httpx.JSON(c, http.StatusOK, resp)
}

func (h *handlers) fetch(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := sessionID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	session, err := h.svc.Store.Load(ctx, id, s.UserID)
	if err != nil {
		return err
	}
	resp, err := h.svc.Fetch(ctx, session, s.UserID)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, resp)
}

// loadForDecision is the shared head of preview and commit: premium
// re-checked on every step, then the session, then the body.
func (h *handlers) loadForDecision(c *echo.Context, userID uuid.UUID) (Session, DecisionRequest, error) {
	ctx := c.Request().Context()
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureSpreadsheetImport, userID); err != nil {
		return Session{}, DecisionRequest{}, err
	}
	id, err := sessionID(c)
	if err != nil {
		return Session{}, DecisionRequest{}, err
	}
	session, err := h.svc.Store.Load(ctx, id, userID)
	if err != nil {
		return Session{}, DecisionRequest{}, err
	}
	var decision DecisionRequest
	if err := swiftjson.DecodeRequest(c, &decision); err != nil {
		return Session{}, DecisionRequest{}, err
	}
	return session, decision, nil
}

func (h *handlers) preview(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	session, decision, err := h.loadForDecision(c, s.UserID)
	if err != nil {
		return err
	}
	resp, err := h.svc.PreviewDecision(c.Request().Context(), session, decision, s.UserID)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, resp)
}

func (h *handlers) commit(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	session, decision, err := h.loadForDecision(c, s.UserID)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	resp, err := h.svc.Commit(ctx, session, decision, s.UserID)
	if err != nil {
		return err
	}
	if err := h.ent.IncrementUsage(ctx, entitlements.FeatureSpreadsheetImport, s.UserID, 1); err != nil {
		return err
	}
	h.d.Logger.Info(fmt.Sprintf("spreadsheet_import_commit imported=%d skipped=%d failed=%d", resp.Imported, resp.Skipped, resp.Failed))
	return httpx.JSON(c, http.StatusOK, resp)
}

func (h *handlers) discard(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := sessionID(c)
	if err != nil {
		return err
	}
	if err := h.svc.Store.Discard(c.Request().Context(), id, s.UserID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// readUpload accepts multipart (a `file` or `spreadsheet` part) or a raw
// body named by X-File-Name. The filename is only ever used for display.
func readUpload(c *echo.Context, body []byte) ([]byte, string, error) {
	maxBytes := DefaultLimits.MaxBytes
	contentType := c.Request().Header.Get(echo.HeaderContentType)
	if _, isMultipart, _ := multipartform.Boundary(contentType); isMultipart {
		parts, err := multipartform.DecodeForm(contentType, body)
		if err != nil {
			return nil, "", err
		}
		file, err := multipartform.FileField(parts, "file")
		if err != nil {
			return nil, "", err
		}
		spreadsheet, err := multipartform.FileField(parts, "spreadsheet")
		if err != nil {
			return nil, "", err
		}
		if file == nil {
			file = spreadsheet
		}
		if file == nil {
			return nil, "", httpx.BadRequest("Missing file field in multipart body.")
		}
		if len(file.Body) > maxBytes {
			return nil, "", httpx.Abort(http.StatusRequestEntityTooLarge,
				fmt.Sprintf("Spreadsheet must be %d MB or smaller.", maxBytes/(1024*1024)))
		}
		return file.Body, sanitize(*file.Filename), nil
	}
	if len(body) == 0 {
		return nil, "", httpx.BadRequest("Missing spreadsheet body.")
	}
	name := "spreadsheet.xlsx"
	if v := c.Request().Header.Values("X-File-Name"); len(v) > 0 {
		name = v[0]
	}
	return body, sanitize(name), nil
}

// sanitize strips path separators and control characters from a
// client-supplied name, then trims; empty becomes "spreadsheet.xlsx".
func sanitize(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, raw)
	cleaned = strings.TrimFunc(cleaned, unicode.IsSpace)
	if cleaned == "" {
		return "spreadsheet.xlsx"
	}
	if r := []rune(cleaned); len(r) > 255 {
		cleaned = string(r[:255])
	}
	return cleaned
}
