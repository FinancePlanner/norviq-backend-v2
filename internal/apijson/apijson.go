// Package apijson reproduces the Vapor API's JSON wire format byte for byte
// where clients can observe it.
//
// The Swift backend encoded with `JSONEncoder.backendAPI`: property names as
// declared (camelCase), dates as ISO 8601 at second precision in UTC, and
// UUIDs as `uuidString`, which is uppercase. It decoded with a custom key
// strategy that rewrote any key containing an underscore to camelCase (in
// keyed containers only, never dictionary keys), so clients may send either
// spelling. Both directions are kept here so handler
// code never has to think about them.
package apijson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
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
//
// The rewrite follows v's type, because Swift applied its key strategy only
// to keyed containers: the keys of a [String: T] dictionary (a Go map) reach
// the handler exactly as sent, while the keys of the structs inside it are
// rewritten. Types with their own UnmarshalJSON receive their bytes untouched;
// an interface target (any) gets every key rewritten.
func Unmarshal(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return io.EOF
	}
	if bytes.IndexByte(raw, '_') >= 0 {
		normalized, err := normalizeKeys(raw, reflect.TypeOf(v))
		if err != nil {
			return err
		}
		raw = normalized
	}
	return json.Unmarshal(raw, v)
}

// UnmarshalKeyed is Unmarshal with every object key rewritten, whatever v's
// type. It is for hand-written decoders that walk raw JSON as Swift keyed
// containers (e.g. into map[string]json.RawMessage) to reproduce Vapor's
// DecodingError texts.
func UnmarshalKeyed(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return io.EOF
	}
	if bytes.IndexByte(raw, '_') >= 0 {
		normalized, err := normalizeKeys(raw, nil)
		if err != nil {
			return err
		}
		raw = normalized
	}
	return json.Unmarshal(raw, v)
}

func normalizeKeys(raw []byte, t reflect.Type) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	return json.Marshal(rewrite(tree, t))
}

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// rewrite renames the snake_case keys of every JSON object that decodes into
// a struct. A nil t means "unknown type": rewrite everything, as Foundation
// did for values decoded through keyed containers.
func rewrite(node any, t reflect.Type) any {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != nil {
		if reflect.PointerTo(t).Implements(unmarshalerType) {
			return node
		}
		if t.Kind() == reflect.Interface {
			t = nil
		}
	}
	switch n := node.(type) {
	case map[string]any:
		if t != nil && t.Kind() == reflect.Map {
			for k, v := range n {
				n[k] = rewrite(v, t.Elem())
			}
			return n
		}
		var fields map[string]reflect.Type
		if t != nil && t.Kind() == reflect.Struct {
			fields = structFields(t)
		}
		out := make(map[string]any, len(n))
		for k, v := range n {
			key := k
			if strings.Contains(k, "_") {
				key = NormalizeSnakeCaseKey(k)
				// The rewrite spells an acronym segment as one capital
				// ("show_source_urls" -> "showSourceUrls"), so it can miss a
				// Swift property such as showSourceURLs. When the exact
				// name misses, take the one field that matches it
				// case-insensitively. Two such fields: pick none, so the
				// value cannot land on whichever is declared first.
				if fields != nil {
					name, m := resolveField(fields, key)
					if m == fieldAmbiguous {
						continue
					}
					key = name
				}
			}
			// A camelCase key wins over its snake_case twin, matching the
			// last-write order Foundation is not guaranteed to give either way.
			if _, exists := out[key]; exists && key != k {
				continue
			}
			var ft reflect.Type
			if t != nil {
				var ok bool
				if ft, ok = lookupField(fields, key); !ok {
					// Unknown keys are dropped by encoding/json anyway.
					out[key] = v
					continue
				}
			}
			out[key] = rewrite(v, ft)
		}
		return out
	case []any:
		var et reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			et = t.Elem()
		}
		for i := range n {
			n[i] = rewrite(n[i], et)
		}
		return n
	default:
		return node
	}
}

// lookupField matches a key to a field the way encoding/json does: exact
// name first, then case-insensitively. A key that matches two fields only
// case-insensitively has no type (it used to get either one at random).
func lookupField(fields map[string]reflect.Type, key string) (reflect.Type, bool) {
	name, m := resolveField(fields, key)
	if m == fieldMissing || m == fieldAmbiguous {
		return nil, false
	}
	return fields[name], true
}

type fieldMatch int

const (
	fieldMissing   fieldMatch = iota // no field has this name, in any case
	fieldExact                       // a field has exactly this name
	fieldFolded                      // exactly one field has this name in another case
	fieldAmbiguous                   // two or more fields have this name in another case
)

// resolveField finds the field name that key decodes into: key itself when
// a field has that exact name, else the single field whose name equals key
// case-insensitively. It returns key unchanged when there is no such field
// or more than one.
func resolveField[V any](fields map[string]V, key string) (string, fieldMatch) {
	if _, ok := fields[key]; ok {
		return key, fieldExact
	}
	found, n := "", 0
	for name := range fields {
		if strings.EqualFold(name, key) {
			found, n = name, n+1
		}
	}
	switch n {
	case 0:
		return key, fieldMissing
	case 1:
		return found, fieldFolded
	default:
		return key, fieldAmbiguous
	}
}

// LookupKey reads name from a keyed container that went through the
// snake_case rewrite (UnmarshalKeyed into map[string]json.RawMessage), for
// hand-written decoders that look Swift property names up one by one.
//
// An exact key always wins. Only when it is absent does the lookup fall
// back to the single key that equals name case-insensitively: the rewrite
// turns "show_source_urls" into "showSourceUrls" and "show_xp" into
// "showXp", which the Swift properties showSourceURLs and showXP never
// matched, so Vapor answered those snake_case bodies with a 400. Two keys
// that differ from name only by case are ambiguous and match neither.
//
// siblings are the other names the decoder reads from the same container.
// A name that equals one of them case-insensitively (a DTO with both
// redirectURI and redirectUri) is matched exactly only, so one key never
// feeds two properties. A decoder that cannot list its siblings must not
// read two names that differ only by case.
func LookupKey[V any](obj map[string]V, name string, siblings ...string) (V, bool) {
	var zero V
	if v, ok := obj[name]; ok {
		return v, true
	}
	for _, s := range siblings {
		if s != name && strings.EqualFold(s, name) {
			return zero, false
		}
	}
	key, m := resolveField(obj, name)
	if m != fieldFolded {
		return zero, false
	}
	return obj[key], true
}

var fieldCache sync.Map // reflect.Type -> map[string]reflect.Type

// structFields maps the JSON names of t's fields, embedded ones included, to
// their types.
func structFields(t reflect.Type) map[string]reflect.Type {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.(map[string]reflect.Type)
	}
	fields := map[string]reflect.Type{}
	collectFields(t, fields)
	fieldCache.Store(t, fields)
	return fields
}

func collectFields(t reflect.Type, fields map[string]reflect.Type) {
	// Fields declared directly on t shadow promoted ones, as in encoding/json.
	var embedded []reflect.Type
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				embedded = append(embedded, et)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
	for _, et := range embedded {
		promoted := map[string]reflect.Type{}
		collectFields(et, promoted)
		for name, ft := range promoted {
			if _, taken := fields[name]; !taken {
				fields[name] = ft
			}
		}
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
