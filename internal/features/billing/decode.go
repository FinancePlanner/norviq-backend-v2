package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Vapor's `req.content.decode` picked a decoder by Content-Type and turned
// Swift DecodingErrors into 400s with Foundation's wording. The billing
// bodies are tiny, so they are read field by field here to keep both the
// statuses and the reasons (iOS and RevenueCat never see the difference,
// parity does).

type bodyKind int

const (
	bodyJSON bodyKind = iota
	bodyForm
)

// contentKind is ContentConfiguration's decoder lookup.
func contentKind(c *echo.Context) (bodyKind, error) {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return 0, httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mt = strings.ToLower(raw)
	}
	switch mt {
	case "application/json", "application/vnd.api+json":
		return bodyJSON, nil
	case "application/x-www-form-urlencoded":
		return bodyForm, nil
	case "text/plain":
		return 0, httpx.BadRequest("Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries.")
	}
	return 0, httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed",
		"Support for reading media type '"+raw+"' has not been configured.")
}

// object is one JSON object being decoded with Swift's rules: exact
// (case-sensitive) keys, after the backendAPI snake_case rewrite.
type object struct {
	path   string
	fields map[string]json.RawMessage
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
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

const dictType = "Dictionary<String, Any>"

// asObject reads raw as a keyed container at path.
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

// parseObject decodes a request body (snake_case keys rewritten to
// camelCase, as JSONDecoder.backendAPI did) into its top-level object.
func parseObject(body []byte) (object, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return object{}, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	var root json.RawMessage
	if err := apijson.UnmarshalKeyed(body, &root); err != nil {
		return object{}, corrupted(err)
	}
	return asObject(root, "")
}

func corrupted(err error) error {
	return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON. Underlying error: " + err.Error() + ".")
}

func typeMismatch(typ, path, found string) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.", typ, path, typ, found))
}

func (o object) missing(key string) error {
	return httpx.BadRequest(fmt.Sprintf(
		"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
		key, o.path, key, key))
}

// requiredString is container.decode(String.self, forKey:).
func (o object) requiredString(key string) (string, error) {
	raw, ok := o.fields[key]
	if !ok {
		return "", o.missing(key)
	}
	if jsonKind(raw) == "null" {
		return "", httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type 'String') at path '%s'. Cannot get value of type String -- found null value instead.", joinPath(o.path, key)))
	}
	return o.stringValue(key, raw)
}

func (o object) stringValue(key string, raw json.RawMessage) (string, error) {
	if kind := jsonKind(raw); kind != "a string" {
		return "", typeMismatch("String", joinPath(o.path, key), kind)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", corrupted(err)
	}
	return s, nil
}

// optionalString is decodeIfPresent(String.self, forKey:).
func (o object) optionalString(key string) (*string, error) {
	raw, ok := o.fields[key]
	if !ok || jsonKind(raw) == "null" {
		return nil, nil
	}
	s, err := o.stringValue(key, raw)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// optionalInt64 is decodeIfPresent(Int64.self, forKey:).
func (o object) optionalInt64(key string) (*int64, error) {
	raw, ok := o.fields[key]
	if !ok || jsonKind(raw) == "null" {
		return nil, nil
	}
	path := joinPath(o.path, key)
	if kind := jsonKind(raw); kind != "number" {
		return nil, typeMismatch("Int64", path, kind)
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, corrupted(err)
	}
	v, err := n.Int64()
	if err != nil {
		// Foundation accepts 12.0 but not 12.5 or out-of-range values, and
		// reports them as a whole-document corruption (Darwin wording).
		f, ferr := n.Float64()
		if ferr != nil || f != float64(int64(f)) {
			msg := fmt.Sprintf("Number %s is not representable in Swift.", n.String())
			return nil, httpx.BadRequest(fmt.Sprintf(
				"Data corrupted at path ''. The given data was not valid JSON. Underlying error: Error Domain=NSCocoaErrorDomain Code=3840 %q UserInfo={NSDebugDescription=%s}.", msg, msg))
		}
		v = int64(f)
	}
	return &v, nil
}

// formObject reads an application/x-www-form-urlencoded body as a flat
// object of strings (URLEncodedFormDecoder).
func formObject(body []byte) (object, error) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return object{}, httpx.BadRequest(err.Error())
	}
	fields := map[string]json.RawMessage{}
	for k, v := range values {
		if len(v) == 0 {
			continue
		}
		b, _ := json.Marshal(v[len(v)-1])
		fields[k] = b
	}
	return object{fields: fields}, nil
}

func ignoreNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}
