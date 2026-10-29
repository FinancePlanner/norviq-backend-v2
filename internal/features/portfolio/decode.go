package portfolio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// decodeListName is `req.content.decode(PortfolioListRequest.self).name`
// with Vapor's statuses and Foundation's DecodingError wording (the same
// rules internal/features/billing/decode.go reproduces).
func decodeListName(c *echo.Context) (string, error) {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return "", httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mt = strings.ToLower(raw)
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	switch mt {
	case "application/json", "application/vnd.api+json":
		if fields, err = jsonObject(body); err != nil {
			return "", err
		}
	case "application/x-www-form-urlencoded":
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", httpx.BadRequest(err.Error())
		}
		fields = map[string]json.RawMessage{}
		for k, v := range values {
			if len(v) > 0 {
				b, _ := json.Marshal(v[len(v)-1])
				fields[k] = b
			}
		}
	case "text/plain":
		return "", httpx.BadRequest("Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries.")
	default:
		return "", httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed",
			"Support for reading media type '"+raw+"' has not been configured.")
	}
	return requiredString(fields, "name")
}

func jsonObject(body []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	var root json.RawMessage
	if err := apijson.UnmarshalKeyed(body, &root); err != nil {
		return nil, corrupted(err)
	}
	switch kind := jsonKind(root); kind {
	case "a dictionary":
	case "null":
		return nil, httpx.BadRequest("No value found (expected type 'Dictionary<String, Any>') at path ''. Cannot get keyed decoding container -- found null value instead.")
	default:
		return nil, typeMismatch("Dictionary<String, Any>", "", kind)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(root, &fields); err != nil {
		return nil, corrupted(err)
	}
	return fields, nil
}

func requiredString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", httpx.BadRequest(fmt.Sprintf(
			"No such key '%s' at path ''. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).", key, key, key))
	}
	switch kind := jsonKind(raw); kind {
	case "a string":
	case "null":
		return "", httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type 'String') at path '%s'. Cannot get value of type String -- found null value instead.", key))
	default:
		return "", typeMismatch("String", key, kind)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", corrupted(err)
	}
	return s, nil
}

func corrupted(err error) error {
	return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON. Underlying error: " + err.Error() + ".")
}

func typeMismatch(typ, path, found string) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.", typ, path, typ, found))
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
