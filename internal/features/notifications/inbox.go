package notifications

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/store/notificationsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// inboxItem is NotificationInboxItem.
type inboxItem struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	DeepLink  *string           `json:"deepLink,omitempty"`
	Payload   map[string]string `json:"payload"`
	CreatedAt string            `json:"createdAt"`
	ReadAt    *string           `json:"readAt,omitempty"`
}

// inboxPage is NotificationInboxPage.
type inboxPage struct {
	Items       []inboxItem `json:"items"`
	UnreadCount int         `json:"unreadCount"`
	NextCursor  *string     `json:"nextCursor,omitempty"`
}

// itemFor is notificationResponse: nil (the row is skipped) when the row
// has no created_at or an unknown kind.
func itemFor(e *inbox.Event) (*inboxItem, error) {
	if e.CreatedAt == nil || !inbox.ValidKind(e.Kind) {
		return nil, nil
	}
	payload := map[string]string{}
	if len(e.Payload) > 0 {
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return nil, err
		}
	}
	item := &inboxItem{
		ID:        apijson.FormatUUID(e.ID),
		Kind:      e.Kind,
		Title:     e.Title,
		Body:      e.Body,
		DeepLink:  e.DeepLink,
		Payload:   payload,
		CreatedAt: apijson.FormatTime(*e.CreatedAt),
	}
	if e.ReadAt != nil {
		s := apijson.FormatTime(*e.ReadAt)
		item.ReadAt = &s
	}
	return item, nil
}

// GET /v1/notifications/inbox?unread=&kind=&limit=&cursor=
func (h *handlers) inbox(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	unreadOnly := false
	if v := httpx.QueryOptionalBool(c, "unread"); v != nil {
		unreadOnly = *v
	}
	limit := min(max(httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 50), 1), 100)
	params := notificationsdb.NotificationInboxPageParams{
		UserID:     s.UserID,
		UnreadOnly: unreadOnly,
		PageLimit:  int32(limit + 1), //nolint:gosec // limit is clamped to 1...100
	}
	if kind := httpx.QueryString(c, "kind"); kind != nil {
		if !inbox.ValidKind(*kind) {
			return httpx.BadRequest("Invalid notification kind.")
		}
		params.Kind = kind
	}
	if raw := httpx.QueryString(c, "cursor"); raw != nil {
		cur, ok := parseCursor(*raw)
		if !ok {
			return httpx.BadRequest("Invalid notification cursor.")
		}
		at := cur.bound()
		params.CursorAt = &at
		params.CursorID = cur.id
	}

	ctx := c.Request().Context()
	rows, err := h.q.NotificationInboxPage(ctx, params)
	if err != nil {
		return err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := inboxPage{Items: make([]inboxItem, 0, len(rows))}
	if hasMore {
		if last := rows[len(rows)-1]; last.CreatedAt != nil {
			next := encodeCursor(*last.CreatedAt, last.ID)
			page.NextCursor = &next
		}
	}
	for i := range rows {
		item, err := itemFor(&rows[i])
		if err != nil {
			return err
		}
		if item != nil {
			page.Items = append(page.Items, *item)
		}
	}
	unread, err := h.q.CountUnreadNotificationEvents(ctx, s.UserID)
	if err != nil {
		return err
	}
	page.UnreadCount = int(unread)
	return httpx.OK(c, page)
}

// PUT /v1/notifications/inbox/:eventID/read {"read": Bool}. The event is
// looked up before the body is read, so an unknown id is a 404 whatever the
// body.
func (h *handlers) updateReadState(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, ok := parseSwiftUUID(c.Param("eventID"))
	if !ok {
		return httpx.NotFound("")
	}
	ctx := c.Request().Context()
	event, err := h.q.NotificationEventForUser(ctx, notificationsdb.NotificationEventForUserParams{ID: id, UserID: s.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	var body struct {
		Read *bool `json:"read"`
	}
	if err := httpx.Decode(c, &body); err != nil {
		return err
	}
	if body.Read == nil {
		return httpx.BadRequest("No such key 'read' at path ''.")
	}
	if *body.Read {
		now := time.Now()
		event.ReadAt = &now
	} else {
		event.ReadAt = nil
	}
	if err := h.q.SetNotificationEventReadAt(ctx, notificationsdb.SetNotificationEventReadAtParams{ID: event.ID, ReadAt: event.ReadAt}); err != nil {
		return err
	}
	item, err := itemFor(&event)
	if err != nil {
		return err
	}
	if item == nil {
		return httpx.Abort(http.StatusInternalServerError, "")
	}
	return httpx.OK(c, item)
}

// POST /v1/notifications/inbox/read-all -> 204.
func (h *handlers) readAll(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := h.q.MarkAllNotificationEventsRead(c.Request().Context(), notificationsdb.MarkAllNotificationEventsReadParams{
		Now: &now, UserID: s.UserID,
	}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// parseSwiftUUID is UUID(uuidString:): only the 36-character hyphenated
// form, either case.
func parseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// Cursor arithmetic reproduces Foundation and PostgresNIO exactly, so
// cursors minted by Vapor and by v2 are interchangeable and select the same
// rows.
const (
	// pgEpochReference is 2000-01-01T00:00:00Z as seconds since the Apple
	// reference date (2001-01-01): PostgresNIO's _psqlDateStart.
	pgEpochReference = -31_622_400.0
	pgEpochUnixMicro = 946_684_800_000_000
	appleEpochUnix   = 978_307_200
)

// cursor is NotificationListCursor. ti is the Date as
// timeIntervalSinceReferenceDate; id is nil for a legacy timestamp cursor.
type cursor struct {
	ti float64
	id *uuid.UUID
}

// referenceInterval is how PostgresNIO decoded a timestamptz into a Date.
func referenceInterval(t time.Time) float64 {
	us := t.UnixMicro() - pgEpochUnixMicro
	return pgEpochReference + float64(us)/1_000_000
}

// bound is how PostgresNIO encoded the Date back into a query parameter:
// microseconds since 2000, truncated.
func (c cursor) bound() time.Time {
	us := int64((c.ti - pgEpochReference) * 1_000_000)
	return time.UnixMicro(us + pgEpochUnixMicro).UTC()
}

// encodeCursor is NotificationListCursor.encode: unpadded URL-safe base64
// of "v1|<bit pattern of the Date's Double>|<UUID>".
func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	bits := math.Float64bits(referenceInterval(createdAt))
	payload := "v1|" + strconv.FormatUint(bits, 10) + "|" + apijson.FormatUUID(id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// parseCursor is NotificationListCursor.parse: the opaque form, or a legacy
// ISO 8601 timestamp with fractional seconds.
func parseCursor(raw string) (cursor, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cursor{}, false
	}
	if c, ok := decodeOpaque(raw); ok {
		return c, true
	}
	if !strings.Contains(raw, ".") {
		return cursor{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return cursor{}, false
	}
	// ISO8601DateFormatter parses to millisecond precision.
	return cursor{ti: float64(t.UnixMilli())/1000 - appleEpochUnix}, true
}

func decodeOpaque(raw string) (cursor, bool) {
	b64 := strings.NewReplacer("-", "+", "_", "/").Replace(raw)
	if pad := len(b64) % 4; pad > 0 {
		b64 += strings.Repeat("=", 4-pad)
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return cursor{}, false
	}
	// Swift's split(separator:) drops empty pieces.
	var parts []string
	for _, p := range strings.Split(string(data), "|") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) != 3 || parts[0] != "v1" {
		return cursor{}, false
	}
	bits, err := strconv.ParseUint(strings.TrimPrefix(parts[1], "+"), 10, 64)
	if err != nil {
		return cursor{}, false
	}
	id, ok := parseSwiftUUID(parts[2])
	if !ok {
		return cursor{}, false
	}
	ti := math.Float64frombits(bits)
	if math.IsNaN(ti) || math.IsInf(ti, 0) {
		// Vapor trapped on a non-finite Date; reject it instead.
		return cursor{}, false
	}
	return cursor{ti: ti, id: &id}, true
}
