package groups

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/swiftjson"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// The Groups request DTOs use synthesized Decodable, so a bad body is a 400
// whose reason is Foundation's DecodingError as Vapor words it. decodeBody
// walks the fields in declaration order and reproduces the common reasons,
// as boards/decode.go does for the Boards DTOs.

type fieldKind int

const (
	fString fieldKind = iota
	fBool
	fUUID
	fEnum // a String raw-value enum; enumName names it in the reason
	fUUIDs
	fInt
	fInt64
)

type field struct {
	name     string
	kind     fieldKind
	optional bool
	enumName string
	valid    func(string) bool
}

// decoded holds the present, non-null values by field name.
type decoded map[string]any

func (d decoded) str(k string) *string {
	if v, ok := d[k].(string); ok {
		return &v
	}
	return nil
}

func (d decoded) boolean(k string) bool { v, _ := d[k].(bool); return v }

func (d decoded) id(k string) uuid.UUID { v, _ := d[k].(uuid.UUID); return v }

func (d decoded) ids(k string) []uuid.UUID { v, _ := d[k].([]uuid.UUID); return v }

func (d decoded) integer(k string) *int {
	if v, ok := d[k].(int64); ok {
		n := int(v)
		return &n
	}
	return nil
}

func (d decoded) int64v(k string) int64 { v, _ := d[k].(int64); return v }

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
		var probe json.RawMessage
		return nil, swiftjson.Corrupted(json.Unmarshal(trimmed, &probe))
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
			if f.kind == fUUIDs {
				return nil, httpx.BadRequest(fmt.Sprintf(
					"No value found (expected type 'Array<Any>') at path '%s'. Cannot get unkeyed decoding container -- found null value instead.", f.name))
			}
			t := swiftType(f.kind)
			return nil, httpx.BadRequest(fmt.Sprintf(
				"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.", t, f.name, t))
		}
		switch f.kind {
		case fUUIDs:
			var items []json.RawMessage
			if json.Unmarshal(v, &items) != nil {
				return nil, mismatch("Array<Any>", f.name, v)
			}
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
				id, ok := parseUUID(s)
				if !ok {
					return nil, badUUID(path)
				}
				ids = append(ids, id)
			}
			out[f.name] = ids
		case fBool:
			var b bool
			if json.Unmarshal(v, &b) != nil {
				return nil, mismatch("Bool", f.name, v)
			}
			out[f.name] = b
		case fInt, fInt64:
			n, err := decodeInt(v, f)
			if err != nil {
				return nil, err
			}
			out[f.name] = n
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

// decodeInt is JSONDecoder's Int/Int64: a JSON number that fits exactly.
func decodeInt(v json.RawMessage, f field) (int64, error) {
	t := swiftType(f.kind)
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 || !(trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9')) {
		return 0, mismatch(t, f.name, v)
	}
	if n, err := strconv.ParseInt(string(trimmed), 10, 64); err == nil {
		if f.kind == fInt64 || (n >= math.MinInt && n <= math.MaxInt) {
			return n, nil
		}
	}
	if fl, err := strconv.ParseFloat(string(trimmed), 64); err == nil && fl == math.Trunc(fl) &&
		fl >= -9.223372036854775808e18 && fl < 9.223372036854775808e18 {
		return int64(fl), nil
	}
	// Darwin Foundation rejects the whole document (swiftjson.checkInteger).
	msg := fmt.Sprintf("Number %s is not representable in Swift.", string(trimmed))
	return 0, httpx.BadRequest(fmt.Sprintf(
		"Data corrupted at path ''. The given data was not valid JSON. Underlying error: Error Domain=NSCocoaErrorDomain Code=3840 %q UserInfo={NSDebugDescription=%s}.", msg, msg))
}

func swiftType(k fieldKind) string {
	switch k {
	case fBool:
		return "Bool"
	case fInt:
		return "Int"
	case fInt64:
		return "Int64"
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

func validVisibility(s string) bool { return s == VisibilityPrivate || s == VisibilityPublic }

func validReportReason(s string) bool {
	switch s {
	case "spam", "harassment", "hate", "scam", "impersonation", "inappropriate", "other":
		return true
	}
	return false
}
