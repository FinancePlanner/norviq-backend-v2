// Package swiftjson decodes request bodies the way Vapor's
// `req.content.decode(T.self)` decoded a synthesized Swift `Decodable` with
// `JSONDecoder.backendAPI`: content-type lookup first (415), an empty body is
// a 422, then fields in declaration order, every non-optional field required,
// enum raw values checked, and the first failure reported as a 400 with
// Vapor's DecodingError wording (`No such key 'x' at path 'a.Index 0'. …`).
//
// Go struct conventions (declaration order = the Swift property order):
//   - `json:"name"` is the Swift property name (snake_case keys in the body
//     are rewritten first, as backendAPI's key strategy did);
//   - a pointer field is an Optional (`decodeIfPresent`), anything else is
//     required;
//   - `enum:"TypeName:a|b|c"` on a string field is a String raw-value enum.
package swiftjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// DecodeRequest is `req.content.decode(T.self)` for a JSON body.
func DecodeRequest(c *echo.Context, v any) error {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return httpx.Abort(http.StatusUnsupportedMediaType, "Can't decode data without a content type")
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
		return httpx.Abort(http.StatusUnsupportedMediaType, "Support for reading media type '"+raw+"' has not been configured.")
	}
	body, err := readAll(c)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	return Decode(body, v)
}

func readAll(c *echo.Context) ([]byte, error) {
	if c.Request().Body == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(c.Request().Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode decodes body into v (a pointer to a struct) with Swift's rules.
func Decode(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	if dec.More() {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("swiftjson: Decode needs a non-nil pointer, got %T", v)
	}
	return decodeInto(root, rv.Elem(), nil, "")
}

type path []string

func (p path) dot() string { return strings.Join(p, ".") }

func (p path) with(k string) path {
	out := make(path, len(p), len(p)+1)
	copy(out, p)
	return append(out, k)
}

const (
	dictType  = "Dictionary<String, Any>"
	arrayType = "Array<Any>"
)

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case map[string]any:
		return "a dictionary"
	case []any:
		return "an array"
	}
	return "unknown"
}

func typeMismatch(typ string, p path, v any) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		typ, p.dot(), typ, kindOf(v)))
}

func valueNotFound(typ string, p path, what string) error {
	return httpx.BadRequest(fmt.Sprintf("No value found (expected type '%s') at path '%s'. %s -- found null value instead.",
		typ, p.dot(), what))
}

func keyNotFound(key string, p path) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, p.dot(), key, key))
}

func dataCorrupted(p path, description string) error {
	return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. %s.", p.dot(), description))
}

func swiftTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "String"
	case reflect.Bool:
		return "Bool"
	case reflect.Int, reflect.Int64:
		return "Int"
	case reflect.Float64:
		return "Double"
	case reflect.Slice:
		return arrayType
	case reflect.Struct:
		return dictType
	}
	return t.String()
}

// decodeInto decodes a present, possibly null value at p into dst.
func decodeInto(v any, dst reflect.Value, p path, enumTag string) error {
	t := dst.Type()
	if v == nil {
		switch t.Kind() {
		case reflect.Struct:
			return valueNotFound(dictType, p, "Cannot get keyed decoding container")
		case reflect.Slice:
			return valueNotFound(arrayType, p, "Cannot get unkeyed decoding container")
		}
		name := swiftTypeName(t)
		if enumTag != "" {
			name = "String"
		}
		return valueNotFound(name, p, "Cannot get value of type "+name)
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return typeMismatch(dictType, p, v)
		}
		return decodeStruct(normalizeKeys(obj), dst, p)
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return typeMismatch(arrayType, p, v)
		}
		out := reflect.MakeSlice(t, len(arr), len(arr))
		for i, item := range arr {
			if err := decodeInto(item, out.Index(i), p.with("Index "+strconv.Itoa(i)), ""); err != nil {
				return err
			}
		}
		dst.Set(out)
		return nil
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return typeMismatch("String", p, v)
		}
		if enumTag != "" {
			name, values, _ := strings.Cut(enumTag, ":")
			valid := false
			for _, allowed := range strings.Split(values, "|") {
				if allowed == s {
					valid = true
					break
				}
			}
			if !valid {
				return dataCorrupted(p, "Cannot initialize "+name+" from invalid String value "+s)
			}
		}
		dst.SetString(s)
		return nil
	case reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return typeMismatch("Bool", p, v)
		}
		dst.SetBool(b)
		return nil
	case reflect.Int, reflect.Int64:
		n, ok := v.(json.Number)
		if !ok {
			return typeMismatch("Int", p, v)
		}
		if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
			dst.SetInt(i)
			return nil
		}
		f, err := strconv.ParseFloat(n.String(), 64)
		if err != nil || f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
			return dataCorrupted(p, "Parsed JSON number <"+n.String()+"> does not fit in Int")
		}
		dst.SetInt(int64(f))
		return nil
	case reflect.Float64:
		n, ok := v.(json.Number)
		if !ok {
			return typeMismatch("Double", p, v)
		}
		f, err := strconv.ParseFloat(n.String(), 64)
		if err != nil {
			return dataCorrupted(p, "Parsed JSON number <"+n.String()+"> does not fit in Double")
		}
		dst.SetFloat(f)
		return nil
	case reflect.Pointer:
		elem := reflect.New(t.Elem())
		if err := decodeInto(v, elem.Elem(), p, enumTag); err != nil {
			return err
		}
		dst.Set(elem)
		return nil
	}
	return fmt.Errorf("swiftjson: unsupported type %s", t)
}

func decodeStruct(obj map[string]any, dst reflect.Value, p path) error {
	t := dst.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		enumTag := f.Tag.Get("enum")
		v, present := obj[key]
		fv := dst.Field(i)
		if f.Type.Kind() == reflect.Pointer {
			if !present || v == nil {
				continue
			}
			if err := decodeInto(v, fv, p.with(key), enumTag); err != nil {
				return err
			}
			continue
		}
		if !present {
			return keyNotFound(key, p)
		}
		if err := decodeInto(v, fv, p.with(key), enumTag); err != nil {
			return err
		}
	}
	return nil
}

// normalizeKeys is backendAPI's key strategy: any key with an underscore
// becomes camelCase.
func normalizeKeys(obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		if strings.Contains(k, "_") {
			k = apijson.NormalizeSnakeCaseKey(k)
		}
		out[k] = v
	}
	return out
}
