package tax

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"time"

	"norviq-api/internal/apijson"
	dec "norviq-api/internal/platform/nsdecimal"

	"github.com/google/uuid"
)

// Vapor ran with the process time zone; production containers are UTC,
// so every Calendar computation here is UTC.

// isoDate is TaxService.isoDate: ISO8601DateFormatter with fractional
// seconds (milliseconds, rounded to nearest as Foundation does).
func isoDate(t time.Time) string {
	t = t.UTC()
	ms := math.Round(float64(t.Nanosecond()) / 1e6)
	base := t.Truncate(time.Second)
	if ms >= 1000 {
		base = base.Add(time.Second)
		ms = 0
	}
	return base.Format("2006-01-02T15:04:05") + "." + pad3(int(ms)) + "Z"
}

func pad3(n int) string {
	s := "00" + strconv.Itoa(n)
	return s[len(s)-3:]
}

// isoSeconds is a plain ISO8601DateFormatter(): whole seconds, rounded to
// the nearest second.
func isoSeconds(t time.Time) string {
	t = t.UTC()
	r := t.Round(time.Second)
	return r.Format("2006-01-02T15:04:05Z")
}

// parseTaxDate is TaxService.parseTaxDate: fractional ISO 8601 first, then
// plain.
func parseTaxDate(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func uuidString(id uuid.UUID) string { return apijson.FormatUUID(id) }

func uuidPtrString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := apijson.FormatUUID(*id)
	return &s
}

// parseUUID is Swift's UUID(uuidString:): case-insensitive, hyphenated only.
func parseUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, false
	}
	return id, true
}

func strPtr(s string) *string { return &s }

func intPtr(n int) *int { return &n }

func decPtr(d dec.Decimal) *dec.Decimal { return &d }

// fd converts a Double to a Decimal through its shortest round-trip text,
// so 0.28 is 0.28. Deliberate fix: Vapor used Decimal(Double), whose
// multiply-by-ten drift turned the PT 28% rate into 0.2800000000000000512
// and read a stored 88.55 back as 88.55000000000003.
func fd(v float64) dec.Decimal {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dec.FromFloat64(v)
	}
	if d, ok := dec.Parse(strconv.FormatFloat(v, 'g', -1, 64)); ok {
		return d
	}
	return dec.FromFloat64(v)
}

// di is Decimal(Int).
func di(v int) dec.Decimal { return dec.FromInt(int64(v)) }

// lit is Decimal(string:)!.
func lit(s string) dec.Decimal { return dec.MustParse(s) }

// dd is NSDecimalNumber(decimal:).doubleValue.
func dd(d dec.Decimal) float64 { return d.Float64() }

func dmax(a, b dec.Decimal) dec.Decimal { return dec.Max(a, b) }

func dmin(a, b dec.Decimal) dec.Decimal { return dec.Min(a, b) }

var zero = dec.Zero

// encodeSorted is TaxService.encode: JSONEncoder with .sortedKeys. The
// stored JSON is read back by Vapor too, so keys and number text follow
// Swift's.
func encodeSorted(v any) (string, error) {
	raw, err := apijson.Marshal(v)
	if err != nil {
		return "", err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var generic any
	if err := d.Decode(&generic); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	writeSorted(&buf, generic)
	return buf.String(), nil
}

func writeSorted(buf *bytes.Buffer, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(escapeSlashes(kb))
			buf.WriteByte(':')
			writeSorted(buf, x[k])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeSorted(buf, e)
		}
		buf.WriteByte(']')
	case json.Number:
		buf.WriteString(x.String())
	case string:
		b, _ := marshalNoHTML(x)
		buf.Write(escapeSlashes(b))
	default:
		b, _ := json.Marshal(x)
		buf.Write(b)
	}
}

func marshalNoHTML(s string) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// escapeSlashes: JSONEncoder escapes "/" unless .withoutEscapingSlashes.
func escapeSlashes(b []byte) []byte { return bytes.ReplaceAll(b, []byte("/"), []byte(`\/`)) }

// decodeStored is TaxService.decode (JSONDecoder of stored JSON).
func decodeStored(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// startOfYearUTC is Calendar(gregorian).date(from: year/1/1).
func startOfYearUTC(year int) time.Time { return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC) }

// daysBetween is Calendar.dateComponents([.day], from:to:).day.
func daysBetween(from, to time.Time) int {
	f := from.UTC()
	t := to.UTC()
	d := int(t.Sub(f).Hours() / 24)
	// Calendar counts whole days of wall-clock time; in UTC that is the
	// truncated duration.
	return d
}

func addDays(t time.Time, n int) time.Time { return t.AddDate(0, 0, n) }

// timeIntervalSince is Date.timeIntervalSince in seconds.
func timeIntervalSince(a, b time.Time) float64 { return a.Sub(b).Seconds() }

func itoa(n int) string { return strconv.Itoa(n) }
