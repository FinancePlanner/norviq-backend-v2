package watchlist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Vapor's `req.content.decode(T.self)` ran Swift's synthesized Decodable
// inits and answered a DecodingError as a 400 with Foundation's wording. The
// reasons reach clients (and parity), so request bodies are decoded here by
// walking the target type field by field in declaration order, with Swift's
// rules: struct keys arrive snake_case or camelCase (JSONDecoder.backendAPI),
// dictionary keys untouched, nil optionals for missing or null keys.

// swiftValue is implemented by types with a hand-written Swift decoder.
type swiftValue interface {
	decodeSwift(raw json.RawMessage, path string) error
}

var swiftValueType = reflect.TypeFor[swiftValue]()

// decodeBody is req.content.decode for a JSON body.
func decodeBody(c *echo.Context, v any) error {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mt = strings.ToLower(raw)
	}
	switch mt {
	case "application/json", "application/vnd.api+json":
	case "text/plain":
		return httpx.BadRequest("Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries.")
	default:
		return httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed",
			"Support for reading media type '"+raw+"' has not been configured.")
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	return decodeJSON(body, v)
}

// decodeJSON decodes body into v (a pointer to a struct).
func decodeJSON(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	var root json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON. Underlying error: " + err.Error() + ".")
	}
	return decodeValue(root, "", reflect.ValueOf(v).Elem())
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// jsonKind is how Foundation names the JSON it found.
func jsonKind(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "null"
	}
	switch raw[0] {
	case '{':
		return "a dictionary"
	case '[':
		return "an array"
	case '"':
		return "a string"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	}
	return "number"
}

func typeMismatch(typ, path, found string) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.", typ, path, typ, found))
}

func keyNotFound(key, path string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, path, key, key))
}

func dataCorrupted(path, description string) error {
	return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. %s", path, description))
}

const dictType = "Dictionary<String, Any>"

// swiftTypeName is the type Foundation names in a mismatch.
func swiftTypeName(t reflect.Type) string {
	switch t {
	case reflect.TypeFor[uuid.UUID]():
		return "String"
	}
	switch t.Kind() {
	case reflect.String:
		return "String"
	case reflect.Float64, reflect.Float32:
		return "Double"
	case reflect.Int, reflect.Int64:
		return "Int"
	case reflect.Bool:
		return "Bool"
	case reflect.Slice:
		return "Array<Any>"
	default:
		return dictType
	}
}

func valueNotFound(t reflect.Type, path string) error {
	name := swiftTypeName(t)
	switch t.Kind() {
	case reflect.Struct:
		if t != reflect.TypeFor[uuid.UUID]() {
			return httpx.BadRequest(fmt.Sprintf(
				"No value found (expected type '%s') at path '%s'. Cannot get keyed decoding container -- found null value instead.", name, path))
		}
	case reflect.Slice:
		return httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type '%s') at path '%s'. Cannot get unkeyed decoding container -- found null value instead.", name, path))
	}
	return httpx.BadRequest(fmt.Sprintf(
		"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.", name, path, name))
}

// decodeValue decodes raw (never absent; possibly null) into rv.
func decodeValue(raw json.RawMessage, path string, rv reflect.Value) error {
	t := rv.Type()
	if reflect.PointerTo(t).Implements(swiftValueType) {
		return rv.Addr().Interface().(swiftValue).decodeSwift(raw, path)
	}
	if t.Kind() == reflect.Pointer {
		if jsonKind(raw) == "null" {
			rv.SetZero()
			return nil
		}
		elem := reflect.New(t.Elem())
		if err := decodeValue(raw, path, elem.Elem()); err != nil {
			return err
		}
		rv.Set(elem)
		return nil
	}
	kind := jsonKind(raw)
	if kind == "null" {
		return valueNotFound(t, path)
	}
	switch {
	case t == reflect.TypeFor[json.RawMessage]():
		rv.SetBytes(append([]byte(nil), raw...))
		return nil
	case t == reflect.TypeFor[uuid.UUID]():
		if kind != "a string" {
			return typeMismatch("String", path, kind)
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		id, ok := parseSwiftUUID(s)
		if !ok {
			return dataCorrupted(path, "Attempted to decode UUID from invalid UUID string.")
		}
		rv.Set(reflect.ValueOf(id))
		return nil
	}
	switch t.Kind() {
	case reflect.String:
		if kind != "a string" {
			return typeMismatch("String", path, kind)
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		rv.SetString(s)
	case reflect.Float64:
		if kind != "number" {
			return typeMismatch("Double", path, kind)
		}
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return dataCorrupted(path, "Parsed JSON number <"+string(raw)+"> does not fit in Double.")
		}
		rv.SetFloat(f)
	case reflect.Bool:
		if kind != "bool" {
			return typeMismatch("Bool", path, kind)
		}
		rv.SetBool(bytes.HasPrefix(bytes.TrimSpace(raw), []byte("t")))
	case reflect.Slice:
		if kind != "an array" {
			return typeMismatch("Array<Any>", path, kind)
		}
		var items []json.RawMessage
		_ = json.Unmarshal(raw, &items)
		out := reflect.MakeSlice(t, len(items), len(items))
		for i, item := range items {
			if err := decodeValue(item, joinPath(path, fmt.Sprintf("Index %d", i)), out.Index(i)); err != nil {
				return err
			}
		}
		rv.Set(out)
	case reflect.Struct:
		if kind != "a dictionary" {
			return typeMismatch(dictType, path, kind)
		}
		fields, err := keyedFields(raw)
		if err != nil {
			return err
		}
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			value, ok := fields[name]
			if !ok {
				if f.Type.Kind() == reflect.Pointer {
					continue
				}
				return keyNotFound(name, path)
			}
			if err := decodeValue(value, joinPath(path, name), rv.Field(i)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("watchlist: decode: unsupported type %s", t)
	}
	return nil
}

// keyedFields reads a JSON object, rewriting snake_case keys the way
// JSONDecoder.backendAPI did. A camelCase key wins over its snake twin.
func keyedFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		if strings.Contains(k, "_") {
			continue
		}
		out[k] = v
	}
	for k, v := range in {
		if !strings.Contains(k, "_") {
			continue
		}
		key := apijson.NormalizeSnakeCaseKey(k)
		if _, taken := out[key]; !taken {
			out[key] = v
		}
	}
	return out, nil
}

// parseSwiftUUID is `UUID(uuidString:)`: only the 36-character hyphenated
// form, any case.
func parseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// enumString decodes a String-backed Swift enum (strict: an unknown raw
// value is a dataCorrupted error).
func enumString(raw json.RawMessage, path, swiftName string, valid []string) (string, error) {
	kind := jsonKind(raw)
	if kind == "null" {
		return "", valueNotFound(reflect.TypeFor[string](), path)
	}
	if kind != "a string" {
		return "", typeMismatch("String", path, kind)
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	for _, v := range valid {
		if v == s {
			return s, nil
		}
	}
	return "", dataCorrupted(path, fmt.Sprintf("Cannot initialize %s from invalid String value %s.", swiftName, s))
}
