package achievements

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Vapor decoded the seen/showcase bodies with `req.content.decode` and
// answered Swift DecodingErrors as 400s in Foundation's wording; the feed's
// `limit` went through URLQueryDecoder. mirror of
// internal/features/billing/decode.go (JSON bodies only), plus the unkeyed
// container and UUID cases these bodies need, so statuses and reasons match.

const (
	dictType  = "Dictionary<String, Any>"
	arrayType = "Array<Any>"
)

// object is one JSON object at a coding path.
type object struct {
	path   string
	fields map[string]json.RawMessage
}

// readObject is ContentConfiguration's JSON decoder lookup plus the top-level
// keyed container. Form bodies are not supported (no client sends them).
func readObject(c *echo.Context) (object, error) {
	ct := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if ct == "" {
		return object{}, httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mt = strings.ToLower(ct)
	}
	switch mt {
	case "application/json", "application/vnd.api+json":
	case "text/plain":
		return object{}, httpx.BadRequest("Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries.")
	default:
		return object{}, httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed",
			"Support for reading media type '"+ct+"' has not been configured.")
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
	return asObject(root, "")
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func indexPath(path string, i int) string {
	return joinPath(path, "Index "+strconv.Itoa(i))
}

// jsonKind is how Foundation's decoder names the JSON it found.
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

// asObject is a keyed container at path.
func asObject(raw json.RawMessage, path string) (object, error) {
	switch kind := jsonKind(raw); kind {
	case "a dictionary":
	case "null":
		return object{}, httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type '%s') at path '%s'. Cannot get keyed decoding container -- found null value instead.", dictType, path))
	default:
		return object{}, typeMismatch(dictType, path, kind)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return object{}, corrupted(err)
	}
	return object{path: path, fields: fields}, nil
}

func (o object) missing(key string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, o.path, key, key))
}

// requiredArray is container.decode([T].self, forKey:): the elements, raw.
func (o object) requiredArray(key string) ([]json.RawMessage, error) {
	raw, ok := o.fields[key]
	if !ok {
		return nil, o.missing(key)
	}
	path := joinPath(o.path, key)
	switch kind := jsonKind(raw); kind {
	case "an array":
	case "null":
		return nil, httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type '%s') at path '%s'. Cannot get unkeyed decoding container -- found null value instead.", arrayType, path))
	default:
		return nil, typeMismatch(arrayType, path, kind)
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, corrupted(err)
	}
	return out, nil
}

// requiredString is container.decode(String.self, forKey:).
func (o object) requiredString(key string) (string, error) {
	raw, ok := o.fields[key]
	if !ok {
		return "", o.missing(key)
	}
	return stringAt(raw, joinPath(o.path, key))
}

// stringAt decodes a String value at path.
func stringAt(raw json.RawMessage, path string) (string, error) {
	switch kind := jsonKind(raw); kind {
	case "a string":
	case "null":
		return "", httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type 'String') at path '%s'. Cannot get value of type String -- found null value instead.", path))
	default:
		return "", typeMismatch("String", path, kind)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", corrupted(err)
	}
	return s, nil
}

// uuidAt is UUID(from:): a String, then UUID(uuidString:).
func uuidAt(raw json.RawMessage, path string) (uuid.UUID, error) {
	s, err := stringAt(raw, path)
	if err != nil {
		return uuid.UUID{}, err
	}
	id, ok := parseSwiftUUID(s)
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Data corrupted at path '" + path + "'. Attempted to decode UUID from invalid UUID string.")
	}
	return id, nil
}

// queryLimit is `req.query[Int.self, at: "limit"]` through URLQueryDecoder:
// the last `name=value` wins, a bare `name` (no `=`) is a flag and reads as
// absent, and a present but non-Int value (empty included) is a 400.
func queryLimit(c *echo.Context, name string) (*int, error) {
	var value *string
	for _, piece := range strings.Split(c.Request().URL.RawQuery, "&") {
		k, v, ok := strings.Cut(piece, "=")
		if !ok {
			continue
		}
		if key, err := url.QueryUnescape(k); err != nil || key != name {
			continue
		}
		unescaped, err := url.QueryUnescape(v)
		if err != nil {
			unescaped = v
		}
		value = &unescaped
	}
	if value == nil {
		return nil, nil
	}
	n, err := strconv.Atoi(*value)
	if err != nil {
		return nil, httpx.BadRequest(fmt.Sprintf("Value was not of type 'Int' at path '%s'. Data found at '%s' was not Int.", name, name))
	}
	return &n, nil
}
