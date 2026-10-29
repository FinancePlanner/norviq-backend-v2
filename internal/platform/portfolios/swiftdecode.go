package portfolios

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// SwiftEnum is implemented by string types that stand for a Swift
// `enum: String, Codable`: decoding any other raw value is a DecodingError.
type SwiftEnum interface {
	SwiftEnumCases() (typeName string, cases []string)
}

// SwiftKeys picks the key rewrite the Swift decoder applied before matching
// CodingKeys.
type SwiftKeys int

const (
	// KeysBackendAPI is JSONDecoder.backendAPI (Vapor's request decoder):
	// apijson.NormalizeSnakeCaseKey for every key containing "_".
	KeysBackendAPI SwiftKeys = iota
	// KeysConvertFromSnakeCase is Foundation's .convertFromSnakeCase.
	KeysConvertFromSnakeCase
)

var (
	timeType = reflect.TypeOf(apijson.Time{})
	uuidType = reflect.TypeOf(apijson.UUID{})
	rawType  = reflect.TypeOf(json.RawMessage{})
)

// DecodeSwiftBody is `req.content.decode(T.self)` for a synthesized Swift
// Decodable: 415 without a Content-Type, then DecodeSwift.
//
// Go struct conventions for v: a pointer field is a Swift optional (absent or
// null decodes as nil); every other field is required, and a missing key or a
// null is a 400 with Vapor's DecodingError reason, checked in declaration
// order like the synthesized init(from:). Named string types implementing
// SwiftEnum reject unknown raw values; apijson.UUID is strict
// UUID(uuidString:); apijson.Time is SharedDateDecoder; any and
// json.RawMessage take any JSON value.
func DecodeSwiftBody(c *echo.Context, v any) error {
	if c.Request().Header.Get("Content-Type") == "" {
		return httpx.Abort(http.StatusUnsupportedMediaType, "Can't decode data without a content type")
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	return DecodeSwift(raw, v, KeysBackendAPI)
}

// DecodeSwift decodes raw into v as Swift's JSONDecoder would, returning
// Vapor's 400 DecodingError reasons (see DecodeSwiftBody).
func DecodeSwift(raw []byte, v any, keys SwiftKeys) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil || dec.More() {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("DecodeSwift: need a non-nil pointer, got %T", v)
	}
	clean, err := swiftWalk(tree, rv.Elem().Type(), nil, keys)
	if err != nil {
		return err
	}
	data, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// SwiftKeyNotFound is DecodingError.keyNotFound's Vapor reason.
func SwiftKeyNotFound(key string, path []string) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, strings.Join(path, "."), key, key))
}

func swiftValueNotFound(typeName string, path []string) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf(
		"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.",
		typeName, strings.Join(path, "."), typeName))
}

func swiftTypeMismatch(typeName string, path []string, found any) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf(
		"Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		typeName, strings.Join(path, "."), typeName, swiftKind(found)))
}

func swiftDataCorrupted(path []string, description string) *httpx.Error {
	return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. %s", strings.Join(path, "."), description))
}

// swiftKind is how Foundation's decoder named the JSON it found.
func swiftKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case []any:
		return "an array"
	case map[string]any:
		return "a dictionary"
	}
	return "unknown"
}

func swiftTypeName(t reflect.Type) string {
	switch {
	case t == timeType:
		return "Date"
	case t == uuidType:
		return "UUID"
	}
	if e, ok := reflect.Zero(t).Interface().(SwiftEnum); ok {
		name, _ := e.SwiftEnumCases()
		return name
	}
	switch t.Kind() {
	case reflect.String:
		return "String"
	case reflect.Bool:
		return "Bool"
	case reflect.Int, reflect.Int64:
		return "Int"
	case reflect.Uint64:
		return "UInt64"
	case reflect.Float64:
		return "Double"
	case reflect.Slice:
		return "Array<Any>"
	case reflect.Map, reflect.Struct:
		return "Dictionary<String, Any>"
	}
	return t.String()
}

func appendPath(path []string, seg string) []string {
	out := make([]string, len(path), len(path)+1)
	copy(out, path)
	return append(out, seg)
}

// swiftWalk validates one value against t and returns it with object keys
// rewritten to the Go struct's json names and integral numbers normalized.
func swiftWalk(v any, t reflect.Type, path []string, keys SwiftKeys) (any, error) {
	if t.Kind() == reflect.Pointer {
		if v == nil {
			return nil, nil
		}
		return swiftWalk(v, t.Elem(), path, keys)
	}
	if t == rawType || t.Kind() == reflect.Interface {
		return v, nil
	}
	if v == nil {
		return nil, swiftValueNotFound(swiftTypeName(t), path)
	}
	switch {
	case t == timeType:
		raw, _ := json.Marshal(v)
		if _, err := apijson.ParseTimeJSON(raw); err != nil {
			if _, isString := v.(string); !isString {
				if _, isNumber := v.(json.Number); !isNumber {
					return nil, swiftTypeMismatch("String", path, v)
				}
			}
			return nil, swiftDataCorrupted(path, "Date must be an ISO8601 string or seconds since Apple reference date")
		}
		return v, nil
	case t == uuidType:
		s, ok := v.(string)
		if !ok {
			return nil, swiftTypeMismatch("String", path, v)
		}
		if _, ok := ParseSwiftUUID(s); !ok {
			return nil, swiftDataCorrupted(path, "Attempted to decode UUID from invalid UUID string.")
		}
		return s, nil
	}
	switch t.Kind() {
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return nil, swiftTypeMismatch(swiftTypeNameRaw(t), path, v)
		}
		if e, ok := reflect.New(t).Elem().Interface().(SwiftEnum); ok {
			name, cases := e.SwiftEnumCases()
			valid := false
			for _, c := range cases {
				valid = valid || c == s
			}
			if !valid {
				return nil, swiftDataCorrupted(path, fmt.Sprintf("Cannot initialize %s from invalid String value %s", name, s))
			}
		}
		return s, nil
	case reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return nil, swiftTypeMismatch("Bool", path, v)
		}
		return b, nil
	case reflect.Float64:
		n, ok := v.(json.Number)
		if !ok {
			return nil, swiftTypeMismatch("Double", path, v)
		}
		f, err := strconv.ParseFloat(string(n), 64)
		if err != nil {
			return nil, swiftDataCorrupted(path, fmt.Sprintf("Parsed JSON number <%s> does not fit in Double.", n))
		}
		return f, nil
	case reflect.Int, reflect.Int64, reflect.Uint64:
		n, ok := v.(json.Number)
		if !ok {
			return nil, swiftTypeMismatch(swiftTypeName(t), path, v)
		}
		return swiftInteger(n, t, path)
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return nil, swiftTypeMismatch("Array<Any>", path, v)
		}
		out := make([]any, len(arr))
		for i, el := range arr {
			cv, err := swiftWalk(el, t.Elem(), appendPath(path, "Index "+strconv.Itoa(i)), keys)
			if err != nil {
				return nil, err
			}
			out[i] = cv
		}
		return out, nil
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, swiftTypeMismatch("Dictionary<String, Any>", path, v)
		}
		out := make(map[string]any, len(obj))
		for k, el := range obj {
			cv, err := swiftWalk(el, t.Elem(), appendPath(path, k), keys)
			if err != nil {
				return nil, err
			}
			out[k] = cv
		}
		return out, nil
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, swiftTypeMismatch("Dictionary<String, Any>", path, v)
		}
		byKey := make(map[string]any, len(obj))
		for k, el := range obj {
			byKey[swiftKey(k, keys)] = el
		}
		out := map[string]any{}
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
			el, present := byKey[name]
			optional := f.Type.Kind() == reflect.Pointer
			if !present {
				if optional {
					continue
				}
				return nil, SwiftKeyNotFound(name, path)
			}
			cv, err := swiftWalk(el, f.Type, appendPath(path, name), keys)
			if err != nil {
				return nil, err
			}
			if cv != nil {
				out[name] = cv
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("DecodeSwift: unsupported type %s", t)
}

func swiftTypeNameRaw(t reflect.Type) string {
	if _, ok := reflect.Zero(t).Interface().(SwiftEnum); ok {
		return "String"
	}
	return swiftTypeName(t)
}

// swiftInteger accepts what Foundation's decoder accepted for a fixed-width
// integer: an integer literal in range, or a number that converts exactly.
func swiftInteger(n json.Number, t reflect.Type, path []string) (any, error) {
	name := swiftTypeName(t)
	fail := func() (any, error) {
		return nil, swiftDataCorrupted(path, fmt.Sprintf("Parsed JSON number <%s> does not fit in %s.", n, name))
	}
	s := string(n)
	if t.Kind() == reflect.Uint64 {
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return json.Number(strconv.FormatUint(u, 10)), nil
		}
	} else if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return json.Number(strconv.FormatInt(i, 10)), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || math.IsInf(f, 0) {
		return fail()
	}
	if t.Kind() == reflect.Uint64 {
		if f < 0 || f >= 18446744073709551616.0 {
			return fail()
		}
		return json.Number(strconv.FormatUint(uint64(f), 10)), nil
	}
	if f < -9223372036854775808.0 || f >= 9223372036854775808.0 {
		return fail()
	}
	return json.Number(strconv.FormatInt(int64(f), 10)), nil
}

func swiftKey(k string, keys SwiftKeys) string {
	if !strings.Contains(k, "_") {
		return k
	}
	if keys == KeysBackendAPI {
		return apijson.NormalizeSnakeCaseKey(k)
	}
	return ConvertFromSnakeCase(k)
}

// ConvertFromSnakeCase is Foundation's JSONDecoder.KeyDecodingStrategy
// .convertFromSnakeCase: leading/trailing underscores kept, inner words
// capitalized (only the first letter; the rest lowercased as Foundation's
// `capitalized` did).
func ConvertFromSnakeCase(k string) string {
	start := 0
	for start < len(k) && k[start] == '_' {
		start++
	}
	end := len(k)
	for end > start && k[end-1] == '_' {
		end--
	}
	if start == end {
		return k
	}
	parts := strings.Split(k[start:end], "_")
	var b strings.Builder
	b.WriteString(k[:start])
	first := true
	for _, p := range parts {
		if p == "" {
			continue
		}
		if first {
			b.WriteString(p)
			first = false
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + strings.ToLower(p[1:]))
	}
	b.WriteString(k[end:])
	return b.String()
}

// ConvertToSnakeCase is Foundation's .convertToSnakeCase for ASCII
// camelCase keys: an underscore before each uppercase run's boundary, then
// lowercased ("targetWeight" -> "target_weight", "fxURL" -> "fx_url").
func ConvertToSnakeCase(k string) string {
	if k == "" {
		return k
	}
	var words []string
	wordStart := 0
	runes := []rune(k)
	isUpper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
	for i := 1; i < len(runes); i++ {
		if !isUpper(runes[i]) {
			continue
		}
		// Start of an uppercase run, or the last capital of a run that
		// precedes a lowercase letter ("URLValue" -> "url_value").
		if !isUpper(runes[i-1]) || (i+1 < len(runes) && !isUpper(runes[i+1])) {
			words = append(words, string(runes[wordStart:i]))
			wordStart = i
		}
	}
	words = append(words, string(runes[wordStart:]))
	return strings.ToLower(strings.Join(words, "_"))
}
