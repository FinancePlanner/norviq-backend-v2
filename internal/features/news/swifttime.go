package news

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"

	"github.com/google/uuid"
)

// Foundation keeps a Date as a Double of seconds since 2001-01-01;
// PostgresNIO reads a timestamptz as -31622400 + micros/1e6 (seconds since
// the reference date) and binds a Date as Int64((date - 2000-01-01) * 1e6),
// truncating. The helpers below repeat that arithmetic so cursors and stored
// rows match Vapor's bit for bit.

const (
	pgEpochReference = -31622400.0       // 2000-01-01 as seconds since 2001-01-01
	pgEpochUnixMicro = 946684800_000_000 // 2000-01-01 in Unix microseconds
	appleEpochUnix   = 978307200         // 2001-01-01 in Unix seconds
)

// referenceInterval is the timeIntervalSinceReferenceDate of a Date that
// PostgresNIO decoded from t.
func referenceInterval(t time.Time) float64 {
	us := t.UnixMicro() - pgEpochUnixMicro
	return pgEpochReference + float64(us)/1_000_000
}

// bindReference is the instant Postgres stores for a Date whose
// timeIntervalSinceReferenceDate is ti.
func bindReference(ti float64) time.Time {
	us := int64((ti - pgEpochReference) * 1_000_000)
	return time.UnixMicro(us + pgEpochUnixMicro).UTC()
}

// bindDate is what Postgres stores for a Date parsed to t (millisecond or
// coarser precision): the round trip through a Double can land one
// microsecond early, exactly as in Vapor.
func bindDate(t time.Time) time.Time {
	ti := float64(t.Unix()-appleEpochUnix) + float64(t.Nanosecond())/1e9
	return bindReference(ti)
}

// formatISOMillis is ISO8601DateFormatter with .withInternetDateTime and
// .withFractionalSeconds: UTC, three fractional digits, rounded.
func formatISOMillis(t time.Time) string {
	return t.UTC().Round(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

// formatISOSeconds is ISO8601DateFormatter with .withInternetDateTime only.
func formatISOSeconds(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// parseISO8601 is the service's parseISO8601: fractional seconds first,
// then whole seconds. Foundation keeps milliseconds.
func parseISO8601(raw string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t.Truncate(time.Millisecond), true
}

// parseISOFractional is the fractional-seconds formatter alone (it rejects
// a value without fractional seconds).
func parseISOFractional(raw string) (time.Time, bool) {
	t, ok := parseISO8601(raw)
	if !ok {
		return time.Time{}, false
	}
	if !strings.Contains(raw[strings.IndexByte(raw, 'T')+1:], ".") {
		return time.Time{}, false
	}
	return t, true
}

// newsCursor is NewsListCursor.
type newsCursor struct {
	publishedAt time.Time // the instant to bind
	id          *uuid.UUID
}

// encodeNewsCursor is NewsListCursor.encode.
func encodeNewsCursor(publishedAt time.Time, id uuid.UUID) string {
	bits := math.Float64bits(referenceInterval(publishedAt))
	payload := "v1|" + strconv.FormatUint(bits, 10) + "|" + apijson.FormatUUID(id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// parseNewsCursor is NewsListCursor.parse.
func parseNewsCursor(raw *string) *newsCursor {
	if raw == nil {
		return nil
	}
	s := strings.TrimSpace(*raw)
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
	return &newsCursor{publishedAt: bindDate(t)}
}

func decodeOpaqueCursor(raw string) *newsCursor {
	b64 := strings.NewReplacer("-", "+", "_", "/").Replace(raw)
	if pad := len(b64) % 4; pad > 0 {
		b64 += strings.Repeat("=", 4-pad)
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	// String.split(separator:) drops empty pieces.
	var parts []string
	for _, p := range strings.Split(string(data), "|") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 3 && parts[0] == "v1" {
		bits, err := strconv.ParseUint(strings.TrimPrefix(parts[1], "+"), 10, 64)
		if err != nil {
			return nil
		}
		id, ok := parseSwiftUUID(parts[2])
		if !ok {
			return nil
		}
		ti := math.Float64frombits(bits)
		if math.IsNaN(ti) || math.IsInf(ti, 0) {
			return nil
		}
		return &newsCursor{publishedAt: bindReference(ti), id: &id}
	}
	if len(parts) == 2 {
		t, ok := parseISOFractional(parts[0])
		if !ok {
			return nil
		}
		id, ok := parseSwiftUUID(parts[1])
		if !ok {
			return nil
		}
		return &newsCursor{publishedAt: bindDate(t), id: &id}
	}
	return nil
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
