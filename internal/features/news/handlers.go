package news

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/activity"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/badgesdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// swiftUUID is a Swift `UUID` request field.
type swiftUUID struct{ uuid.UUID }

// SwiftValidate implements swiftjson.Validator.
func (*swiftUUID) SwiftValidate(raw json.RawMessage, path []string) error {
	if swiftjson.IsNull(raw) {
		return swiftjson.ValueNotFound("String", path, "")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return swiftjson.TypeMismatch("String", path, raw)
	}
	if _, ok := parseSwiftUUID(s); !ok {
		return swiftjson.DataCorrupted(path, "Attempted to decode UUID from invalid UUID string.")
	}
	return nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (u *swiftUUID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	id, err := uuid.Parse(s)
	u.UUID = id
	return err
}

// pathUUID is requireUUIDParameter.
func pathUUID(c *echo.Context, name, reason string) (uuid.UUID, error) {
	raw := c.Param(name)
	if v, err := url.PathUnescape(raw); err == nil {
		raw = v
	}
	id, ok := parseSwiftUUID(raw)
	if !ok {
		return uuid.UUID{}, httpx.BadRequest(reason)
	}
	return id, nil
}

// clampedLimit is NewsController.clampedLimit.
func clampedLimit(raw *int, def, maxValue int) int {
	v := def
	if raw != nil {
		v = *raw
	}
	return max(1, min(v, maxValue))
}

// GET /v1/news?symbol=&limit=&cursor=
func (h *handlers) listNews(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	limit := clampedLimit(httpx.QueryOptionalInt(c, "limit"), 50, 200)
	cursor := parseNewsCursor(httpx.QueryString(c, "cursor"))
	items, next, err := h.svc.List(c.Request().Context(), s.UserID, httpx.QueryString(c, "symbol"), limit, cursor)
	if err != nil {
		return err
	}
	if next != nil {
		c.Response().Header().Add("X-Next-Cursor", *next)
	}
	return httpx.OK(c, items)
}

// GET /v1/news/feed?limit=
func (h *handlers) feedNews(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	limit := clampedLimit(httpx.QueryOptionalInt(c, "limit"), 50, 200)
	items, err := h.svc.Feed(c.Request().Context(), s.UserID, limit)
	if err != nil {
		return err
	}
	return httpx.OK(c, items)
}

// POST /v1/news
func (h *handlers) createNews(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p ItemRequest
	if err := swiftjson.DecodeRequest(c, &p); err != nil {
		return err
	}
	created, err := h.svc.Create(c.Request().Context(), s.UserID, p)
	if err != nil {
		return err
	}
	return httpx.Created(c, created)
}

// GET /v1/news/:newsId
func (h *handlers) getNews(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathUUID(c, "newsId", "Invalid news ID")
	if err != nil {
		return err
	}
	item, err := h.svc.Get(c.Request().Context(), id, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, item)
}

// PUT /v1/news/:newsId
func (h *handlers) updateNews(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathUUID(c, "newsId", "Invalid news ID")
	if err != nil {
		return err
	}
	var p ItemRequest
	if err := swiftjson.DecodeRequest(c, &p); err != nil {
		return err
	}
	item, err := h.svc.Update(c.Request().Context(), id, s.UserID, p)
	if err != nil {
		return err
	}
	return httpx.OK(c, item)
}

// DELETE /v1/news/:newsId
func (h *handlers) deleteNews(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathUUID(c, "newsId", "Invalid news ID")
	if err != nil {
		return err
	}
	if err := h.svc.Delete(c.Request().Context(), id, s.UserID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// POST /v1/news/sync
func (h *handlers) syncNews(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.Sync(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// viewRequest is NewsViewRequest.
type viewRequest struct {
	NewsID   *swiftUUID `json:"newsId"`
	Symbol   *string    `json:"symbol"`
	Headline string     `json:"headline"`
	URL      *string    `json:"url"`
}

var whitespaceRun = regexp.MustCompile(`\s+`)

// referenceKey is newsViewReferenceKey.
func referenceKey(p viewRequest, headline string, symbol *string) string {
	if p.NewsID != nil {
		return "news:" + strings.ToLower(p.NewsID.String())
	}
	if p.URL != nil {
		if u := strings.ToLower(strings.TrimSpace(*p.URL)); u != "" {
			return "url:" + u
		}
	}
	sym := "market"
	if symbol != nil {
		sym = *symbol
	}
	return "headline:" + sym + ":" + whitespaceRun.ReplaceAllString(strings.ToLower(headline), " ")
}

// POST /v1/news/view
func (h *handlers) recordNewsView(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p viewRequest
	if err := swiftjson.DecodeRequest(c, &p); err != nil {
		return err
	}
	headline := strings.TrimSpace(p.Headline)
	if headline == "" {
		return httpx.BadRequest("headline is required.")
	}
	var symbol *string
	if p.Symbol != nil {
		v := strings.ToUpper(strings.TrimSpace(*p.Symbol))
		symbol = &v
	}
	key := referenceKey(p, headline, symbol)
	subtitle := "Read market news"
	if symbol != nil {
		subtitle = "Read " + *symbol + " news"
	}
	ctx := c.Request().Context()
	if err := activity.NewRecorder(h.db).Record(ctx, activity.Record{
		UserID:       s.UserID,
		Type:         activity.TypeNewsViewed,
		Title:        headline,
		Subtitle:     subtitle,
		IsGrowth:     true,
		Symbol:       "newspaper.fill",
		ReferenceKey: &key,
	}); err != nil {
		return err
	}
	// Request.reconcileBadges: best effort, then queue an achievement check.
	svc := badges.Service{Q: badgesdb.New(h.db), Logger: h.logger.Warn}
	if _, err := svc.Evaluate(ctx, s.UserID); err != nil {
		h.logger.Warn("badge reconciliation failed", "user", s.UserID.String(), "error", err)
	}
	achievementhooks.Schedule(s.UserID)
	return httpx.NoContent(c)
}
