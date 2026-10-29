package portfoliomgmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// Vapor's `req.content.decode` picked a decoder by Content-Type and turned
// Swift DecodingErrors into 400s with Foundation's wording. The bodies here
// are flat StockPlanShared DTOs with synthesized Codable, so they are read
// field by field, in declaration order, to keep statuses and reasons.

// object is the top-level JSON object of a request body, keys already
// rewritten from snake_case as JSONDecoder.backendAPI did.
type object struct {
	fields map[string]json.RawMessage
}

// readObject is req.content.decode's prologue: 415 without a content type
// (checked before the body), 422 for an empty body, then the JSON document.
func readObject(c *echo.Context) (object, error) {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return object{}, httpx.Abort(http.StatusUnsupportedMediaType, "Can't decode data without a content type")
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mt = strings.ToLower(raw)
	}
	switch mt {
	case "application/json", "application/vnd.api+json":
	case "text/plain":
		return object{}, httpx.BadRequest("Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries.")
	default:
		// URL-encoded form bodies were decodable in Vapor too; no client of
		// these routes sends one (see the port's report).
		return object{}, httpx.Abort(http.StatusUnsupportedMediaType,
			"Support for reading media type '"+raw+"' has not been configured.")
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return object{}, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return object{}, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	var root json.RawMessage
	if err := apijson.UnmarshalKeyed(body, &root); err != nil {
		return object{}, corrupted(err)
	}
	switch kind := jsonKind(root); kind {
	case "a dictionary":
	case "null":
		return object{}, httpx.BadRequest("No value found (expected type 'Dictionary<String, Any>') at path ''. Cannot get keyed decoding container -- found null value instead.")
	default:
		return object{}, typeMismatch("Dictionary<String, Any>", "", kind)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(root, &fields); err != nil {
		return object{}, corrupted(err)
	}
	return object{fields: fields}, nil
}

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

func corrupted(err error) error {
	return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON. Underlying error: " + err.Error() + ".")
}

func typeMismatch(typ, path, found string) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.", typ, path, typ, found))
}

func missing(key string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path ''. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, key, key))
}

func nullValue(typ, key string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.", typ, key, typ))
}

// required returns the raw value of a non-optional key.
func (o object) required(key, typ string) (json.RawMessage, error) {
	raw, ok := o.fields[key]
	if !ok {
		return nil, missing(key)
	}
	if jsonKind(raw) == "null" {
		return nil, nullValue(typ, key)
	}
	return raw, nil
}

// optional returns the raw value of an optional key, nil when absent or null.
func (o object) optional(key string) json.RawMessage {
	raw, ok := o.fields[key]
	if !ok || jsonKind(raw) == "null" {
		return nil
	}
	return raw
}

func stringValue(key string, raw json.RawMessage) (string, error) {
	if kind := jsonKind(raw); kind != "a string" {
		return "", typeMismatch("String", key, kind)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", corrupted(err)
	}
	return s, nil
}

// String is container.decode(String.self, forKey:).
func (o object) String(key string) (string, error) {
	raw, err := o.required(key, "String")
	if err != nil {
		return "", err
	}
	return stringValue(key, raw)
}

// OptionalString is decodeIfPresent(String.self, forKey:).
func (o object) OptionalString(key string) (*string, error) {
	raw := o.optional(key)
	if raw == nil {
		return nil, nil
	}
	s, err := stringValue(key, raw)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Bool is container.decode(Bool.self, forKey:).
func (o object) Bool(key string) (bool, error) {
	raw, err := o.required(key, "Bool")
	if err != nil {
		return false, err
	}
	if kind := jsonKind(raw); kind != "bool" {
		return false, typeMismatch("Bool", key, kind)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, corrupted(err)
	}
	return b, nil
}

// Double is container.decode(Double.self, forKey:).
func (o object) Double(key string) (float64, error) {
	raw, err := o.required(key, "Double")
	if err != nil {
		return 0, err
	}
	if kind := jsonKind(raw); kind != "number" {
		return 0, typeMismatch("Double", key, kind)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, corrupted(err)
	}
	return f, nil
}

// enumValue is a String-backed RawRepresentable's init(from:).
func enumValue(key, typ string, allowed []string, raw json.RawMessage) (string, error) {
	s, err := stringValue(key, raw)
	if err != nil {
		return "", err
	}
	if !slices.Contains(allowed, s) {
		return "", httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Cannot initialize %s from invalid String value %s.", key, typ, s))
	}
	return s, nil
}

// Enum is container.decode(E.self, forKey:) for a String enum.
func (o object) Enum(key, typ string, allowed []string) (string, error) {
	raw, err := o.required(key, "String")
	if err != nil {
		return "", err
	}
	return enumValue(key, typ, allowed, raw)
}

// OptionalEnum is decodeIfPresent(E.self, forKey:) for a String enum.
func (o object) OptionalEnum(key, typ string, allowed []string) (*string, error) {
	raw := o.optional(key)
	if raw == nil {
		return nil, nil
	}
	s, err := enumValue(key, typ, allowed, raw)
	if err != nil {
		return nil, err
	}
	return &s, nil
}
