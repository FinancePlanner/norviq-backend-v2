// Package apijson reproduces the Vapor API's JSON wire format byte for byte
// where clients can observe it.
//
// The Swift backend encoded with `JSONEncoder.backendAPI`: property names as
// declared (camelCase), dates as ISO 8601 at second precision in UTC, and
// UUIDs as `uuidString`, which is uppercase. It decoded with a custom key
// strategy that rewrote any key containing an underscore to camelCase, so
// clients may send either spelling. Both directions are kept here so handler
// code never has to think about them.
package apijson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// appleReferenceEpoch is 2001-01-01T00:00:00Z, the zero of Foundation's
// `timeIntervalSinceReferenceDate`. Swift clients that fall back to numeric
// dates send seconds since this instant, not since 1970.
var appleReferenceEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// Time is a timestamp with the Swift encoder's wire format:
// "2006-01-02T15:04:05Z", UTC, no fractional seconds.
type Time struct{ time.Time }

// NewTime wraps t.
func NewTime(t time.Time) Time { return Time{t} }

// TimePtr wraps t, keeping nil as nil (an absent optional in Swift).
func TimePtr(t *time.Time) *Time {
	if t == nil {
		return nil
	}
	return &Time{*t}
}

const wireLayout = "2006-01-02T15:04:05Z"

// FormatTime renders t the way the Swift encoder did.
func FormatTime(t time.Time) string { return t.UTC().Format(wireLayout) }

func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(`"` + FormatTime(t.Time) + `"`), nil
}

func (t *Time) UnmarshalJSON(data []byte) error {
	parsed, err := ParseTimeJSON(data)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

// ParseTimeJSON accepts every date spelling `SharedDateDecoder` accepted:
// ISO 8601 with or without fractional seconds, a bare yyyy-MM-dd (UTC
// midnight), or seconds since the Apple reference date as a number or a
// numeric string.
func ParseTimeJSON(data []byte) (time.Time, error) {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return time.Time{}, err
		}
		return ParseTimeString(s)
	}
	seconds, err := strconv.ParseFloat(string(data), 64)
	if err != nil {
		return time.Time{}, errors.New("Date must be an ISO8601 string or seconds since Apple reference date")
	}
	return fromReferenceSeconds(seconds), nil
}

// ParseTimeString parses one date string as SharedDateDecoder did.
func ParseTimeString(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
		return t, nil
	}
	if seconds, err := strconv.ParseFloat(s, 64); err == nil {
		return fromReferenceSeconds(seconds), nil
	}
	return time.Time{}, errors.New("Date must be an ISO8601 string or seconds since Apple reference date")
}

func fromReferenceSeconds(seconds float64) time.Time {
	whole := int64(seconds)
	frac := seconds - float64(whole)
	return appleReferenceEpoch.Add(time.Duration(whole)*time.Second + time.Duration(frac*float64(time.Second)))
}

// UUID is a uuid.UUID that encodes uppercase, as Swift's `uuidString` does,
// and decodes either case.
type UUID uuid.UUID

// NewUUID wraps u.
func NewUUID(u uuid.UUID) UUID { return UUID(u) }

// UUIDPtr wraps u, keeping nil as nil.
func UUIDPtr(u *uuid.UUID) *UUID {
	if u == nil {
		return nil
	}
	v := UUID(*u)
	return &v
}

// FormatUUID renders u as Swift did.
func FormatUUID(u uuid.UUID) string { return strings.ToUpper(u.String()) }

func (u UUID) String() string { return FormatUUID(uuid.UUID(u)) }

// Std returns the underlying uuid.UUID.
func (u UUID) Std() uuid.UUID { return uuid.UUID(u) }

func (u UUID) MarshalJSON() ([]byte, error) {
	return []byte(`"` + u.String() + `"`), nil
}

func (u *UUID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := uuid.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid UUID %q", s)
	}
	*u = UUID(parsed)
	return nil
}

// Marshal encodes v without HTML escaping. Foundation also wrote "/" as "\/";
// that is the same JSON value, so parity is checked on parsed documents.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	// Encoder.Encode appends a newline; JSONEncoder does not.
	return bytes.TrimSuffix(out, []byte{'\n'}), nil
}

// Decode reads one JSON value from r into v after normalizing snake_case
// object keys to the camelCase spelling the Swift decoder used.
func Decode(r io.Reader, v any) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return Unmarshal(raw, v)
}

// Unmarshal is Decode for a byte slice.
func Unmarshal(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return io.EOF
	}
	if bytes.IndexByte(raw, '_') >= 0 {
		normalized, err := normalizeKeys(raw)
		if err != nil {
			return err
		}
		raw = normalized
	}
	return json.Unmarshal(raw, v)
}

func normalizeKeys(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	return json.Marshal(rewrite(tree))
}

func rewrite(node any) any {
	switch n := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, v := range n {
			key := k
			if strings.Contains(k, "_") {
				key = NormalizeSnakeCaseKey(k)
			}
			// A camelCase key wins over its snake_case twin, matching the
			// last-write order Foundation is not guaranteed to give either way.
			if _, exists := out[key]; exists && key != k {
				continue
			}
			out[key] = rewrite(v)
		}
		return out
	case []any:
		for i := range n {
			n[i] = rewrite(n[i])
		}
		return n
	default:
		return node
	}
}

// NormalizeSnakeCaseKey ports `JSONDecoder.backendAPI`'s key rewrite:
// lowercase every segment, then capitalize all but the first, spelling the
// segments "url", "uri" and "id" as "URL", "URI" and "Id".
func NormalizeSnakeCaseKey(key string) string {
	var parts []string
	for _, p := range strings.Split(key, "_") {
		if p != "" {
			parts = append(parts, strings.ToLower(p))
		}
	}
	if len(parts) == 0 {
		return key
	}
	var b strings.Builder
	b.WriteString(parts[0])
	for _, seg := range parts[1:] {
		switch seg {
		case "url":
			b.WriteString("URL")
		case "uri":
			b.WriteString("URI")
		case "id":
			b.WriteString("Id")
		default:
			b.WriteString(strings.ToUpper(seg[:1]) + seg[1:])
		}
	}
	return b.String()
}
