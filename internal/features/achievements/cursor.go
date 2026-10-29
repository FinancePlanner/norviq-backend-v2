package achievements

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"

	"github.com/google/uuid"
)

// NotificationListCursor, as the feed uses it. mirror of the notifications
// feature's unexported cursor (internal/features/notifications/inbox.go):
// the arithmetic reproduces Foundation and PostgresNIO exactly, so cursors
// minted by Vapor and by v2 are interchangeable.
const (
	pgEpochReference = -31_622_400.0
	pgEpochUnixMicro = 946_684_800_000_000
	appleEpochUnix   = 978_307_200
)

type cursor struct {
	ti float64
	id *uuid.UUID
}

func referenceInterval(t time.Time) float64 {
	us := t.UnixMicro() - pgEpochUnixMicro
	return pgEpochReference + float64(us)/1_000_000
}

func (c cursor) bound() time.Time {
	us := int64((c.ti - pgEpochReference) * 1_000_000)
	return time.UnixMicro(us + pgEpochUnixMicro).UTC()
}

func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	bits := math.Float64bits(referenceInterval(createdAt))
	payload := "v1|" + strconv.FormatUint(bits, 10) + "|" + apijson.FormatUUID(id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// parseCursor is NotificationListCursor.parse; ok is false for nil.
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
		return cursor{}, false
	}
	return cursor{ti: ti, id: &id}, true
}

// parseSwiftUUID is UUID(uuidString:): the 36-character hyphenated form,
// either case.
func parseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}
