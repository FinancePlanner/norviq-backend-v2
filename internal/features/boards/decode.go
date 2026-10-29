package boards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// The shared request DTOs use synthesized Decodable, so a bad body is a 400
// whose reason is Foundation's DecodingError as Vapor words it. decodeBody
// walks the fields in declaration order and reproduces the common reasons
// (missing key, null, wrong JSON type, bad UUID, unknown enum value).

type fieldKind int

const (
	fString fieldKind = iota
	fBool
	fUUID
	fEnum // a String raw-value enum; enumName names it in the reason
	fStrings
	fUUIDs
)

type field struct {
	name     string
	kind     fieldKind
	optional bool
	enumName string
	valid    func(string) bool
}

// decoded holds the present, non-null values by field name: string, bool,
// uuid.UUID, []string or []uuid.UUID.
type decoded map[string]any

func (d decoded) str(k string) *string {
	if v, ok := d[k].(string); ok {
		return &v
	}
	return nil
}

func (d decoded) boolean(k string) bool { v, _ := d[k].(bool); return v }

func (d decoded) id(k string) *uuid.UUID {
	if v, ok := d[k].(uuid.UUID); ok {
		return &v
	}
	return nil
}

func (d decoded) strings(k string) []string { v, _ := d[k].([]string); return v }

func (d decoded) ids(k string) ([]uuid.UUID, bool) {
	v, ok := d[k].([]uuid.UUID)
	return v, ok
}

func decodeBody(c *echo.Context, fields []field) (decoded, error) {
	if c.Request().Header.Get("Content-Type") == "" {
		return nil, httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	var obj map[string]json.RawMessage
	if err := apijson.UnmarshalKeyed(trimmed, &obj); err != nil || obj == nil {
		if json.Valid(trimmed) {
			return nil, httpx.BadRequest(fmt.Sprintf(
				"Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found %s instead.",
				jsonKind(trimmed)))
		}
		return nil, httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	out := decoded{}
	for _, f := range fields {
		v, present := obj[f.name]
		if !present || isNull(v) {
			if f.optional {
				continue
			}
			if !present {
				return nil, httpx.BadRequest(fmt.Sprintf(
					"No such key '%s' at path ''. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
					f.name, f.name, f.name))
			}
			if f.kind == fStrings || f.kind == fUUIDs {
				return nil, httpx.BadRequest(fmt.Sprintf(
					"No value found (expected type 'Array<Any>') at path '%s'. Cannot get unkeyed decoding container -- found null value instead.", f.name))
			}
			t := swiftType(f.kind)
			return nil, httpx.BadRequest(fmt.Sprintf(
				"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.", t, f.name, t))
		}
		switch f.kind {
		case fStrings, fUUIDs:
			var items []json.RawMessage
			if json.Unmarshal(v, &items) != nil {
				return nil, mismatch("Array<Any>", f.name, v)
			}
			strs := make([]string, 0, len(items))
			ids := make([]uuid.UUID, 0, len(items))
			for i, item := range items {
				path := f.name + ".Index " + strconv.Itoa(i)
				if isNull(item) {
					return nil, httpx.BadRequest(fmt.Sprintf(
						"No value found (expected type 'String') at path '%s'. Cannot get value of type String -- found null value instead.", path))
				}
				var s string
				if json.Unmarshal(item, &s) != nil {
					return nil, mismatch("String", path, item)
				}
				if f.kind == fStrings {
					strs = append(strs, s)
					continue
				}
				id, ok := parseUUID(s)
				if !ok {
					return nil, badUUID(path)
				}
				ids = append(ids, id)
			}
			if f.kind == fStrings {
				out[f.name] = strs
			} else {
				out[f.name] = ids
			}
		case fBool:
			var b bool
			if json.Unmarshal(v, &b) != nil {
				return nil, mismatch("Bool", f.name, v)
			}
			out[f.name] = b
		default:
			var s string
			if json.Unmarshal(v, &s) != nil {
				return nil, mismatch("String", f.name, v)
			}
			switch f.kind {
			case fUUID:
				id, ok := parseUUID(s)
				if !ok {
					return nil, badUUID(f.name)
				}
				out[f.name] = id
			case fEnum:
				if !f.valid(s) {
					return nil, httpx.BadRequest(fmt.Sprintf(
						"Data corrupted at path '%s'. Cannot initialize %s from invalid String value %s.", f.name, f.enumName, s))
				}
				out[f.name] = s
			default:
				out[f.name] = s
			}
		}
	}
	return out, nil
}

func swiftType(k fieldKind) string {
	if k == fBool {
		return "Bool"
	}
	return "String"
}

func isNull(v json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(v), []byte("null")) }

func mismatch(t, path string, v json.RawMessage) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		t, path, t, jsonKind(v)))
}

func badUUID(path string) error {
	return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Attempted to decode UUID from invalid UUID string.", path))
}

// jsonKind is how Foundation's decoder named the JSON it found.
func jsonKind(v []byte) string {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return "nothing"
	}
	switch v[0] {
	case '"':
		return "a string"
	case '{':
		return "a dictionary"
	case '[':
		return "an array"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	}
	return "number"
}

// parseUUID is UUID(uuidString:): 36 characters, either case.
func parseUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}
