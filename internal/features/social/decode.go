package social

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// kind is the Swift type a body field decodes as.
type kind int

const (
	kString kind = iota
	kBool
	kInt
	kUUID
	kEnum  // a String-backed enum; field.enum names it, field.values lists the cases
	kArray // [Object]; field.items describes each element
)

type field struct {
	name     string
	kind     kind
	required bool
	enum     string
	values   []string
	items    []field
}

func (f field) swiftType() string {
	switch f.kind {
	case kString, kEnum:
		return "String"
	case kBool:
		return "Bool"
	case kInt:
		return "Int"
	case kUUID:
		return "String"
	}
	return "Array<Any>"
}

// decodeBody reads a JSON object the way a Swift Decodable with these
// fields did (in declaration order) and returns Vapor's DecodingError
// reasons for the common failures, so 400s read the same. Absent or null
// optionals are missing from the result.
func decodeBody(c *echo.Context, fields []field) (map[string]any, error) {
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, httpx.BadRequest("Request body is empty.")
	}
	var obj map[string]json.RawMessage
	if err := apijson.UnmarshalKeyed(trimmed, &obj); err != nil {
		if json.Valid(trimmed) {
			return nil, httpx.BadRequest(fmt.Sprintf(
				"Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found %s instead.",
				jsonKind(trimmed)))
		}
		return nil, httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	if obj == nil {
		return nil, httpx.BadRequest("Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found null instead.")
	}
	return decodeFields(obj, fields, nil)
}

func joinPath(path []string, name string) string {
	return strings.Join(append(slices.Clone(path), name), ".")
}

func decodeFields(obj map[string]json.RawMessage, fields []field, path []string) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range fields {
		v, present := obj[f.name]
		isNull := present && bytes.Equal(bytes.TrimSpace(v), []byte("null"))
		if !present || isNull {
			if !f.required {
				continue
			}
			if !present {
				return nil, httpx.BadRequest(fmt.Sprintf(
					"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
					f.name, strings.Join(path, "."), f.name, f.name))
			}
			return nil, httpx.BadRequest(fmt.Sprintf(
				"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.",
				f.swiftType(), joinPath(path, f.name), f.swiftType()))
		}
		p := joinPath(path, f.name)
		switch f.kind {
		case kString, kEnum, kUUID:
			var s string
			if json.Unmarshal(v, &s) != nil {
				return nil, mismatch(f.swiftType(), p, v)
			}
			switch f.kind {
			case kEnum:
				if !slices.Contains(f.values, s) {
					return nil, httpx.BadRequest(fmt.Sprintf(
						"Data corrupted at path '%s'. Cannot initialize %s from invalid String value %s", p, f.enum, s))
				}
				out[f.name] = s
			case kUUID:
				id, err := uuid.Parse(s)
				if err != nil || len(s) != 36 {
					return nil, httpx.BadRequest(fmt.Sprintf(
						"Data corrupted at path '%s'. Attempted to decode UUID from invalid UUID string.", p))
				}
				out[f.name] = id
			default:
				out[f.name] = s
			}
		case kBool:
			var b bool
			if json.Unmarshal(v, &b) != nil {
				return nil, mismatch("Bool", p, v)
			}
			out[f.name] = b
		case kInt:
			t := bytes.TrimSpace(v)
			if len(t) == 0 || (t[0] != '-' && (t[0] < '0' || t[0] > '9')) {
				return nil, mismatch("Int", p, v)
			}
			n, err := strconv.ParseInt(string(t), 10, 64)
			if err != nil {
				return nil, httpx.BadRequest(fmt.Sprintf(
					"Data corrupted at path '%s'. Parsed JSON number <%s> does not fit in Int.", p, string(t)))
			}
			out[f.name] = int(n)
		case kArray:
			var elems []json.RawMessage
			if json.Unmarshal(v, &elems) != nil {
				return nil, mismatch("Array<Any>", p, v)
			}
			list := make([]map[string]any, 0, len(elems))
			for i, e := range elems {
				elemPath := append(slices.Clone(path), f.name, fmt.Sprintf("Index %d", i))
				var eo map[string]json.RawMessage
				if json.Unmarshal(e, &eo) != nil || eo == nil {
					return nil, mismatch("Dictionary<String, Any>", strings.Join(elemPath, "."), e)
				}
				m, err := decodeFields(eo, f.items, elemPath)
				if err != nil {
					return nil, err
				}
				list = append(list, m)
			}
			out[f.name] = list
		}
	}
	return out, nil
}

func mismatch(typ, path string, v json.RawMessage) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		typ, path, typ, jsonKind(v)))
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

func str(m map[string]any, k string) string   { s, _ := m[k].(string); return s }
func boolean(m map[string]any, k string) bool { b, _ := m[k].(bool); return b }

func optBool(m map[string]any, k string) *bool {
	if b, ok := m[k].(bool); ok {
		return &b
	}
	return nil
}

func optStr(m map[string]any, k string) *string {
	if s, ok := m[k].(string); ok {
		return &s
	}
	return nil
}
