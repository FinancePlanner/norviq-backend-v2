package brokers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/multipartform"
	"norviq-api/internal/platform/onboarding"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// handlers is BrokerController (plus AuthController.brokerIBKRCallback).
type handlers struct {
	s       *service
	badges  *badges.Service
	consent func(c *echo.Context, userID uuid.UUID) error
}

// screenshotMaxBody is the screenshot route's 26 MB collect cap.
const screenshotMaxBody = 26 * 1024 * 1024

const (
	csvMaxBytes             = 5 * 1024 * 1024
	maxScreenshotImageBytes = 8 * 1024 * 1024
	maxScreenshotRows       = 300
)

// reconcileBadges is Request.reconcileBadges.
func (h *handlers) reconcileBadges(ctx context.Context, userID uuid.UUID) {
	if _, err := h.badges.Evaluate(ctx, userID); err != nil {
		h.s.logger.Warn("badge reconciliation failed", "user", userID.String(), "error", err)
	}
	achievementhooks.Schedule(userID)
}

func (h *handlers) listBrokers(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.s.list(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) getBroker(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.s.get(c.Request().Context(), c.Param("provider"), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// listHoldings is the first page of the user's holdings in every list.
func (h *handlers) listHoldings(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := h.s.q().HoldingsPage(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	out := make([]HoldingResponse, len(rows))
	for i := range rows {
		r := rows[i]
		out[i] = HoldingResponse{Symbol: r.Symbol, Quantity: r.Shares, Currency: "USD"}
	}
	return httpx.OK(c, out)
}

func (h *handlers) syncIBKR(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.s.entitlements.RequirePremium(ctx, entitlements.FeatureBrokerSync, s.UserID); err != nil {
		return err
	}
	resp, err := h.s.syncIBKR(ctx, s.UserID)
	if err != nil {
		return toHTTP(err)
	}
	h.reconcileBadges(ctx, s.UserID)
	return httpx.OK(c, resp)
}

func (h *handlers) syncStatus(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	conn, err := h.s.find(c.Request().Context(), h.s.db, "ibkr", s.UserID)
	if err != nil {
		return err
	}
	if conn == nil {
		return httpx.NotFound("IBKR connection not found")
	}
	resp := SyncStatusResponse{Status: conn.Status, IsStale: true, StatusDetail: conn.StatusDetail}
	if conn.LastSyncedAt != nil {
		resp.LastSyncedAt = apijson.TimePtr(conn.LastSyncedAt)
		resp.IsStale = h.s.now().Sub(*conn.LastSyncedAt) > 24*time.Hour
	}
	return httpx.OK(c, resp)
}

func (h *handlers) startConnect(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body connectStartRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	resp, err := h.s.startIBKRConnect(c.Request().Context(), c.Request(), body.RedirectURI, body.PortfolioListID, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) connectCredentials(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body connectCredentialsRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	resp, err := h.s.connectIBKRCredentials(c.Request().Context(), body.Token, body.QueryID, body.PortfolioListID, s.UserID)
	if err != nil {
		return err
	}
	achievementhooks.Schedule(s.UserID)
	return httpx.OK(c, resp)
}

func (h *handlers) disconnect(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	resp, err := h.s.disconnectIBKR(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// callback is AuthController.brokerIBKRCallback.
func (h *handlers) callback(c *echo.Context) error {
	var flowID *uuid.UUID
	if raw := httpx.QueryString(c, "flowId"); raw != nil {
		id, ok := portfolios.ParseSwiftUUID(*raw)
		if !ok {
			return httpx.BadRequest("Invalid broker flow id.")
		}
		flowID = &id
	}
	var code, providerError *string
	if v := httpx.QueryString(c, "code"); v != nil {
		code = trimmedNonEmpty(*v)
	}
	if v := httpx.QueryString(c, "error"); v != nil {
		providerError = trimmedNonEmpty(*v)
	}
	if flowID == nil && code == nil && providerError == nil {
		return httpx.BadRequest("Invalid broker flow id.")
	}
	state := httpx.QueryString(c, "state")
	if state == nil || portfolios.TrimSwift(*state) == "" {
		return httpx.BadRequest("Missing broker flow state.")
	}
	location, err := h.s.handleIBKRCallback(c.Request().Context(), c.Request(), flowID, code, providerError, *state,
		func(userID uuid.UUID) { achievementhooks.Schedule(userID) })
	if err != nil {
		return err
	}
	c.Response().Header().Set("Location", location)
	return c.NoContent(http.StatusSeeOther)
}

// MARK: - CSV import

// requireProvider is the multipart field, else ?provider=, else ?broker=,
// else X-Broker-Provider, normalized.
func requireProvider(c *echo.Context, multipartValue *string) (string, error) {
	raw := multipartValue
	if raw == nil {
		raw = httpx.QueryString(c, "provider")
	}
	if raw == nil {
		raw = httpx.QueryString(c, "broker")
	}
	if raw == nil {
		if v := c.Request().Header.Values("X-Broker-Provider"); len(v) > 0 {
			raw = &v[0]
		}
	}
	if raw == nil {
		return "", httpx.BadRequest("Missing broker provider. Use ?provider=... or multipart field provider.")
	}
	return NormalizeProvider(*raw)
}

// readCsvUpload reads a multipart "file" (or "csv") part, else the raw body.
func readCsvUpload(c *echo.Context) (provider, csv string, err error) {
	contentType := c.Request().Header.Get(echo.HeaderContentType)
	if _, isMultipart, _ := multipartform.Boundary(contentType); isMultipart {
		body, err := multipartform.ReadBody(c.Request(), 1<<40)
		if err != nil {
			return "", "", err
		}
		parts, err := multipartform.DecodeForm(contentType, body)
		if err != nil {
			return "", "", err
		}
		var field *string
		if p := multipartform.Named(parts, "provider"); p != nil {
			v := strings.ToValidUTF8(string(p.Body), "�")
			field = &v
		}
		file, err := multipartform.FileField(parts, "file")
		if err != nil {
			return "", "", err
		}
		alt, err := multipartform.FileField(parts, "csv")
		if err != nil {
			return "", "", err
		}
		if provider, err = requireProvider(c, field); err != nil {
			return "", "", err
		}
		if file == nil {
			file = alt
		}
		if file == nil {
			return "", "", httpx.BadRequest("Missing file field in multipart body.")
		}
		if len(file.Body) > csvMaxBytes {
			return "", "", httpx.Abort(http.StatusRequestEntityTooLarge, "CSV file must be 5 MB or smaller.")
		}
		return provider, strings.ToValidUTF8(string(file.Body), "�"), nil
	}
	if provider, err = requireProvider(c, nil); err != nil {
		return "", "", err
	}
	body, err := multipartform.ReadBody(c.Request(), 1<<40)
	if err != nil {
		return "", "", err
	}
	if len(body) == 0 {
		return "", "", httpx.BadRequest("Missing CSV body.")
	}
	return provider, strings.ToValidUTF8(string(body), "�"), nil
}

func (h *handlers) csvPreview(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	provider, csv, err := readCsvUpload(c)
	if err != nil {
		return err
	}
	resp, err := h.s.csvPreviewText(c.Request().Context(), csv, provider, httpx.QueryString(c, "portfolioListId"), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (h *handlers) csvCommit(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	provider, csv, err := readCsvUpload(c)
	if err != nil {
		return err
	}
	confirm := false
	if v := httpx.QueryOptionalBool(c, "confirmMergeExisting"); v != nil {
		confirm = *v
	}
	ctx := c.Request().Context()
	resp, wrote, err := h.s.csvCommitText(ctx, csv, provider, httpx.QueryString(c, "portfolioListId"), s.UserID, confirm)
	if err != nil {
		return err
	}
	h.afterCommit(ctx, s.UserID, wrote)
	return httpx.OK(c, resp)
}

// afterCommit reconciles badges and, when a stock row was written (imported
// rows are always category stock), latches the holding onboarding step.
func (h *handlers) afterCommit(ctx context.Context, userID uuid.UUID, wrote bool) {
	h.reconcileBadges(ctx, userID)
	if wrote {
		onboarding.StampBestEffort(ctx, h.s.db, h.s.logger, onboarding.LatchHolding, userID)
	}
}

// MARK: - Screenshot import

const screenshotMultipartRequired = "Upload screenshots as multipart/form-data."

func (h *handlers) screenshotPreview(c *echo.Context) error {
	body, err := multipartform.ReadBody(c.Request(), screenshotMaxBody)
	if err != nil {
		return err
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.s.entitlements.RequirePremium(ctx, entitlements.FeatureScreenshotImport, s.UserID); err != nil {
		return err
	}
	provider, images, err := readScreenshotUpload(c, body)
	if err != nil {
		return err
	}
	resp, err := h.s.screenshotPreview(ctx, images, provider, httpx.QueryString(c, "portfolioListId"), s.UserID,
		func() error { return h.consent(c, s.UserID) })
	if err != nil {
		return err
	}
	h.s.logger.Info("portfolio_screenshot_preview images=" + itoa(resp.ImageCount) + " kind=" + resp.Kind +
		" rows=" + itoa(len(resp.Items)) + " errors=" + itoa(len(resp.Errors)))
	return httpx.OK(c, resp)
}

func readScreenshotUpload(c *echo.Context, body []byte) (string, []screenshotImage, error) {
	boundary, isMultipart, hasBoundary := multipartform.Boundary(c.Request().Header.Get(echo.HeaderContentType))
	if !isMultipart {
		return "", nil, httpx.Abort(http.StatusUnsupportedMediaType, screenshotMultipartRequired)
	}
	if !hasBoundary {
		return "", nil, httpx.BadRequest(screenshotMultipartRequired)
	}
	if len(body) == 0 {
		return "", nil, httpx.BadRequest("Missing image field in multipart body.")
	}
	parts, err := multipartform.Parse(body, boundary)
	switch {
	case errors.Is(err, multipartform.ErrEmptyBoundary):
		return "", nil, httpx.BadRequest("Upload the form as multipart/form-data.")
	case err != nil:
		return "", nil, httpx.BadRequest("Could not read the uploaded form.")
	}
	files := multipartform.ImageFiles(parts)
	if len(files) == 0 {
		return "", nil, httpx.BadRequest("Missing image field in multipart body.")
	}
	if len(files) > maxScreenshotImages {
		return "", nil, httpx.BadRequest("Upload at most " + itoa(maxScreenshotImages) + " screenshots at a time.")
	}
	for _, f := range files {
		if len(f.Body) > maxScreenshotImageBytes {
			return "", nil, httpx.Abort(http.StatusRequestEntityTooLarge, "Each screenshot must be 8 MB or smaller.")
		}
	}
	// fields: the first non-file part under each canonical name.
	var field *string
	for _, p := range parts {
		name := multipartform.CanonicalFieldName(p.Name)
		if multipartform.ImageFieldNames[name] && p.Filename != nil {
			continue
		}
		if name == "provider" {
			v := strings.ToValidUTF8(string(p.Body), "�")
			field = &v
			break
		}
	}
	provider, err := requireProvider(c, field)
	if err != nil {
		return "", nil, err
	}
	images := make([]screenshotImage, 0, len(files))
	for _, f := range files {
		if !strings.HasPrefix(strings.ToLower(f.ContentType), "image/") && f.ContentType != "application/octet-stream" {
			return "", nil, httpx.BadRequest("Screenshots must be images.")
		}
		images = append(images, screenshotImage{data: f.Body, contentType: f.ContentType})
	}
	return provider, images, nil
}

func (h *handlers) screenshotCommit(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.s.entitlements.RequirePremium(ctx, entitlements.FeatureScreenshotImport, s.UserID); err != nil {
		return err
	}
	var body screenshotCommitRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	provider, err := NormalizeProvider(body.Provider)
	if err != nil {
		return err
	}
	if len(body.Items) == 0 {
		return httpx.BadRequest("No rows to import.")
	}
	if len(body.Items) > maxScreenshotRows {
		return httpx.BadRequest("Too many rows in one import (max " + itoa(maxScreenshotRows) + ").")
	}
	items := make([]PreviewItem, len(body.Items))
	for i, it := range body.Items {
		items[i] = PreviewItem{
			Line: i, Symbol: it.Symbol, Shares: it.Shares, BuyPrice: it.BuyPrice,
			BuyDate: it.BuyDate, Notes: it.Notes, Confidence: it.Confidence,
		}
	}
	listID := body.PortfolioListID
	if listID == nil {
		listID = httpx.QueryString(c, "portfolioListId")
	}
	confirm := body.ConfirmMergeExisting != nil && *body.ConfirmMergeExisting
	resp, wrote, err := h.s.commit(ctx, items, nil, provider, listID, s.UserID, confirm)
	if err != nil {
		return err
	}
	h.afterCommit(ctx, s.UserID, wrote)
	h.s.logger.Info("portfolio_screenshot_commit inserted=" + itoa(len(resp.Inserted)) +
		" updated=" + itoa(len(resp.Updated)) + " errors=" + itoa(len(resp.Errors)))
	return httpx.OK(c, resp)
}

// platformConsent is AIConsent.require through the shared AI platform.
func platformConsent(p *ai.Platform) func(c *echo.Context, userID uuid.UUID) error {
	return func(c *echo.Context, userID uuid.UUID) error { return p.Consent.Require(c, userID) }
}
