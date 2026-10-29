// Package swiftjson decodes request bodies the way Vapor's
// `req.content.decode(T.self)` did for synthesized Swift Codable types, so a
// malformed body fails with the same status and the same Foundation
// DecodingError wording.
//
// The Go type describes the Swift one:
//
//   - a non-pointer field is `decode` (required): an absent key is
//     keyNotFound, null is valueNotFound;
//   - a pointer field is `decodeIfPresent`: absent and null are both nil;
//   - `swift:"default"` on a non-pointer field is `decodeIfPresent ?? default`
//     (the Go zero value, or whatever the caller pre-filled);
//   - a string-kinded type implementing Enum is a String-backed enum;
//   - a type implementing Validator checks its own raw JSON (custom
//     `init(from:)`).
//
// Keys are matched case-sensitively after the backendAPI snake_case rewrite
// (struct keys only, never dictionary keys). Fields are checked in
// declaration order, which is the order a synthesized decoder reads them, so
// the first error is the one Swift threw. Once the body validates it is
// decoded with apijson.Unmarshal.
package swiftjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// Enum is a String-backed Swift enum. Lenient enums (a custom init that
// maps unknown strings to `.unknown`) accept any string.
type Enum interface {
	SwiftEnum() (typeName string, allowed []string, lenient bool)
}

// Validator is a type with a custom Swift `init(from:)`: it checks its own
// raw value. path is the coding path of the value.
type Validator interface {
	SwiftValidate(raw json.RawMessage, path []string) error
}

// Path renders a coding path as Vapor's DecodingError reasons did
// (`codingPath.dotPath`; array elements are "Index N").
func Path(path []string) string { return strings.Join(path, ".") }

// Append returns path + key without aliasing path.
func Append(path []string, key string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = key
	return out
}

// Kind is Foundation's description of a JSON value in typeMismatch errors.
func Kind(raw json.RawMessage) string {
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

// IsNull reports a JSON null.
func IsNull(raw json.RawMessage) bool { return Kind(raw) == "null" }

// TypeMismatch is DecodingError.typeMismatch.
func TypeMismatch(typeName string, path []string, raw json.RawMessage) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		typeName, Path(path), typeName, Kind(raw)))
}

// KeyNotFound is DecodingError.keyNotFound for key in the container at path.
func KeyNotFound(key string, path []string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, Path(path), key, key))
}

// ValueNotFound is DecodingError.valueNotFound for a null where typeName was
// required.
func ValueNotFound(typeName string, path []string, container string) error {
	what := "value of type " + typeName
	switch container {
	case "keyed":
		what = "keyed decoding container"
	case "unkeyed":
		what = "unkeyed decoding container"
	}
	return httpx.BadRequest(fmt.Sprintf("No value found (expected type '%s') at path '%s'. Cannot get %s -- found null value instead.",
		typeName, Path(path), what))
}

// DataCorrupted is DecodingError.dataCorrupted at path. Vapor ends the
// description with exactly one period whether or not it had one.
func DataCorrupted(path []string, description string) error {
	return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. %s.", Path(path), strings.TrimSuffix(description, ".")))
}

// Corrupted is the whole-document "not valid JSON" error.
func Corrupted(err error) error {
	return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON. Underlying error: " + err.Error() + ".")
}

const dictType = "Dictionary<String, Any>"

// DecodeRequest is `req.content.decode(T.self)` for a JSON body.
func DecodeRequest(c *echo.Context, v any) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	mt, _, perr := mime.ParseMediaType(raw)
	if perr != nil {
		mt = strings.ToLower(raw)
	}
	switch mt {
	case "application/json", "application/vnd.api+json":
		return Decode(body, v)
	case "application/x-www-form-urlencoded":
		return decodeForm(body, v)
	case "text/plain":
		return httpx.BadRequest("Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries.")
	}
	return httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed",
		"Support for reading media type '"+raw+"' has not been configured.")
}

// Decode validates body against v's type with Swift's rules, then decodes it.
func Decode(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	var root json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return Corrupted(err)
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if err := validate(root, t, nil, true); err != nil {
		return err
	}
	if err := apijson.Unmarshal(body, v); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return nil
}

func decodeForm(body []byte, v any) error {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	fields := map[string]any{}
	for k, vs := range values {
		if len(vs) > 0 {
			fields[k] = vs[len(vs)-1]
		}
	}
	if err := formMissingKey(fields, reflect.TypeOf(v)); err != nil {
		return err
	}
	b, _ := json.Marshal(fields)
	return Decode(b, v)
}

// formMissingKey is URLEncodedFormDecoder's error for an absent required
// top-level key: valueNotFound, not keyNotFound as JSONDecoder says.
func formMissingKey(fields map[string]any, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" || f.Type.Kind() == reflect.Pointer || strings.Contains(f.Tag.Get("swift"), "default") {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if _, ok := fields[name]; !ok {
			typeName := SwiftTypeName(f.Type)
			return httpx.BadRequest(fmt.Sprintf("No value found (expected type '%s') at path '%s'. No %s was found at '%s'.", typeName, name, typeName, name))
		}
	}
	return nil
}

var (
	enumType      = reflect.TypeFor[Enum]()
	validatorType = reflect.TypeFor[Validator]()
	rawType       = reflect.TypeFor[json.RawMessage]()
)

// SwiftTypeName is the Swift spelling of t in error messages.
func SwiftTypeName(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(enumType) {
		name, _, _ := reflect.Zero(t).Interface().(Enum).SwiftEnum()
		return name
	}
	switch t.Kind() {
	case reflect.String:
		return "String"
	case reflect.Float64, reflect.Float32:
		return "Double"
	case reflect.Int, reflect.Int64, reflect.Int32:
		return "Int"
	case reflect.Bool:
		return "Bool"
	case reflect.Slice:
		return "Array<Any>"
	}
	return dictType
}

// validate checks raw against t. The last argument marks the root value.
func validate(raw json.RawMessage, t reflect.Type, path []string, _ bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawType {
		return nil
	}
	if reflect.PointerTo(t).Implements(validatorType) {
		return reflect.New(t).Interface().(Validator).SwiftValidate(raw, path)
	}
	if t.Implements(enumType) {
		name, allowed, lenient := reflect.Zero(t).Interface().(Enum).SwiftEnum()
		if IsNull(raw) {
			return ValueNotFound("String", path, "")
		}
		if Kind(raw) != "a string" {
			return TypeMismatch("String", path, raw)
		}
		if lenient {
			return nil
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		for _, a := range allowed {
			if a == s {
				return nil
			}
		}
		return DataCorrupted(path, fmt.Sprintf("Cannot initialize %s from invalid String value %s", name, s))
	}
	switch t.Kind() {
	case reflect.String:
		return expectKind(raw, "a string", "String", path)
	case reflect.Bool:
		return expectKind(raw, "bool", "Bool", path)
	case reflect.Float64, reflect.Float32:
		return expectKind(raw, "number", "Double", path)
	case reflect.Int, reflect.Int64, reflect.Int32:
		if err := expectKind(raw, "number", "Int", path); err != nil {
			return err
		}
		return checkInteger(raw)
	case reflect.Slice:
		if IsNull(raw) {
			return ValueNotFound(SwiftTypeName(t), path, "unkeyed")
		}
		if Kind(raw) != "an array" {
			return TypeMismatch("Array<Any>", path, raw)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return Corrupted(err)
		}
		for i, item := range items {
			if err := validate(item, t.Elem(), Append(path, fmt.Sprintf("Index %d", i)), false); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if IsNull(raw) {
			return ValueNotFound(dictType, path, "keyed")
		}
		if Kind(raw) != "a dictionary" {
			return TypeMismatch(dictType, path, raw)
		}
		var items map[string]json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return Corrupted(err)
		}
		for k, item := range items {
			if err := validate(item, t.Elem(), Append(path, k), false); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		return validateStruct(raw, t, path)
	}
	return nil
}

func expectKind(raw json.RawMessage, kind, typeName string, path []string) error {
	if IsNull(raw) {
		return ValueNotFound(typeName, path, "")
	}
	if Kind(raw) != kind {
		return TypeMismatch(typeName, path, raw)
	}
	return nil
}

// checkInteger rejects 1.5 for an Int as Foundation did (a whole-document
// corruption, Darwin wording).
func checkInteger(raw json.RawMessage) error {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return Corrupted(err)
	}
	if _, err := n.Int64(); err == nil {
		return nil
	}
	f, err := n.Float64()
	if err == nil && f == float64(int64(f)) {
		return nil
	}
	msg := fmt.Sprintf("Number %s is not representable in Swift.", n.String())
	return httpx.BadRequest(fmt.Sprintf(
		"Data corrupted at path ''. The given data was not valid JSON. Underlying error: Error Domain=NSCocoaErrorDomain Code=3840 %q UserInfo={NSDebugDescription=%s}.", msg, msg))
}

// Fields reads raw as a keyed container at path, with the snake_case
// rewrite applied to its keys (a camelCase key wins over its twin).
func Fields(raw json.RawMessage, path []string) (map[string]json.RawMessage, error) {
	if IsNull(raw) {
		return nil, ValueNotFound(dictType, path, "keyed")
	}
	if Kind(raw) != "a dictionary" {
		return nil, TypeMismatch(dictType, path, raw)
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, Corrupted(err)
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		key := k
		if strings.Contains(k, "_") {
			key = apijson.NormalizeSnakeCaseKey(k)
		}
		if _, exists := out[key]; exists && key != k {
			continue
		}
		out[key] = v
	}
	return out, nil
}

func validateStruct(raw json.RawMessage, t reflect.Type, path []string) error {
	fields, err := Fields(raw, path)
	if err != nil {
		return err
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		val, present := fields[name]
		optional := f.Type.Kind() == reflect.Pointer || strings.Contains(f.Tag.Get("swift"), "default")
		if optional {
			if !present || IsNull(val) {
				continue
			}
		} else if !present {
			return KeyNotFound(name, path)
		}
		if err := validate(val, f.Type, Append(path, name), false); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks raw against v's type (for custom validators composing
// nested values).
func Validate(raw json.RawMessage, v any, path []string) error {
	t := reflect.TypeOf(v)
	return validate(raw, t, path, false)
}
