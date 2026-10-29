package stocks

import (
	"encoding/base64"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/portfolios"

	"github.com/google/uuid"
)

// Swift's Date is a Double of seconds since 2001-01-01; PostgresNIO decodes a
// timestamptz as `Date(timeInterval: micros/1e6, since: 2000-01-01)` and
// binds one back as round((date - 2000-01-01) * 1e6) microseconds. The
// cursor carries that Double's bit pattern, so v2 reproduces the same float
// arithmetic to hand out byte-identical cursors.

var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

const pgEpochSinceReference = -31_622_400.0 // 2000-01-01 relative to 2001-01-01

// referenceSeconds is the Swift Date PostgresNIO builds from a stored time.
func referenceSeconds(t time.Time) float64 {
	micros := t.Sub(pgEpoch).Microseconds()
	return pgEpochSinceReference + float64(micros)/1e6
}

// timeFromReference is the timestamptz PostgresNIO binds for a Swift Date.
func timeFromReference(seconds float64) time.Time {
	micros := math.RoundToEven((seconds - pgEpochSinceReference) * 1e6)
	return pgEpoch.Add(time.Duration(int64(micros)) * time.Microsecond)
}

// StockListCursor is ListSupport.swift's StockListCursor.
type StockListCursor struct {
	CreatedAt time.Time
	ID        *uuid.UUID
}

// ParseStockListCursor is StockListCursor.parse: the opaque v1 cursor, the
// legacy "<iso>|<uuid>" one, or a bare fractional ISO8601 timestamp.
func ParseStockListCursor(raw *string) *StockListCursor {
	if raw == nil {
		return nil
	}
	s := trim(*raw)
	if s == "" {
		return nil
	}
	if c := decodeOpaqueCursor(s); c != nil {
		return c
	}
	t, ok := parseISOFractional(s)
	if !ok {
		return nil
	}
	return &StockListCursor{CreatedAt: t}
}

// EncodeStockListCursor is StockListCursor.encode.
func EncodeStockListCursor(createdAt time.Time, id uuid.UUID) string {
	bits := math.Float64bits(referenceSeconds(createdAt))
	payload := "v1|" + strconv.FormatUint(bits, 10) + "|" + apijson.FormatUUID(id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeOpaqueCursor(raw string) *StockListCursor {
	b64 := strings.NewReplacer("-", "+", "_", "/").Replace(raw)
	if pad := len(b64) % 4; pad > 0 {
		b64 += strings.Repeat("=", 4-pad)
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || !utf8.Valid(data) {
		return nil
	}
	// split(separator:) drops empty pieces.
	var parts []string
	for _, p := range strings.Split(string(data), "|") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 3 && parts[0] == "v1" {
		if bits, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
			if id, ok := portfolios.ParseSwiftUUID(parts[2]); ok {
				return &StockListCursor{CreatedAt: timeFromReference(math.Float64frombits(bits)), ID: &id}
			}
		}
	}
	if len(parts) == 2 {
		t, ok := parseISOFractional(parts[0])
		if !ok {
			return nil
		}
		id, ok := portfolios.ParseSwiftUUID(parts[1])
		if !ok {
			return nil
		}
		return &StockListCursor{CreatedAt: t, ID: &id}
	}
	return nil
}

var isoFractionalRE = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})\.([0-9]+)(Z|[+-][0-9]{2}:?[0-9]{2})$`)

// parseISOFractional is ISO8601DateFormatter [.withInternetDateTime,
// .withFractionalSeconds]: the fraction is required and kept to milliseconds.
func parseISOFractional(s string) (time.Time, bool) {
	m := isoFractionalRE.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	zone := m[3]
	if zone != "Z" && !strings.Contains(zone, ":") {
		zone = zone[:3] + ":" + zone[3:]
	}
	frac := (m[2] + "000")[:3]
	t, err := time.Parse("2006-01-02T15:04:05.000Z07:00", m[1]+"."+frac+zone)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
