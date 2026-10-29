package boards

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"

	"github.com/google/uuid"
)

// NotificationListCursor, as internal/features/notifications ports it (the
// helpers there are unexported; this is the same arithmetic). Boards only
// honor the opaque form with an id: a legacy timestamp cursor parses but is
// ignored, as Swift's `let id = cursor.id` guard did.
const (
	// pgEpochReference is 2000-01-01T00:00:00Z as seconds since the Apple
	// reference date (2001-01-01): PostgresNIO's _psqlDateStart.
	pgEpochReference = -31_622_400.0
	pgEpochUnixMicro = 946_684_800_000_000
)

type cursor struct {
	ti float64
	id uuid.UUID
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

// encodeCursor is NotificationListCursor.encode.
func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	bits := math.Float64bits(referenceInterval(createdAt))
	payload := "v1|" + strconv.FormatUint(bits, 10) + "|" + apijson.FormatUUID(id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// parseCursor is NotificationListCursor.parse followed by `let id =
// cursor.id`: only the opaque form yields a usable cursor.
func parseCursor(raw *string) (cursor, bool) {
	if raw == nil {
		return cursor{}, false
	}
	s := strings.TrimSpace(*raw)
	if s == "" {
		return cursor{}, false
	}
	b64 := strings.NewReplacer("-", "+", "_", "/").Replace(s)
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
	if len(parts[2]) != 36 {
		return cursor{}, false
	}
	id, err := uuid.Parse(parts[2])
	if err != nil {
		return cursor{}, false
	}
	ti := math.Float64frombits(bits)
	if math.IsNaN(ti) || math.IsInf(ti, 0) {
		// Vapor trapped on a non-finite Date; reject it instead.
		return cursor{}, false
	}
	return cursor{ti: ti, id: id}, true
}
