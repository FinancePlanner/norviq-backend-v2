package digest

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

	"github.com/labstack/echo/v5"
)

// preferencesUpdate is DigestPreferencesUpdate: every field optional, a PUT
// changes only what it names.
type preferencesUpdate struct {
	EmailEnabled           *bool
	PushEnabled            *bool
	TelegramEnabled        *bool
	SentimentAlertsEnabled *bool
	Weekday                *int
	LocalHour              *int
	Timezone               *string
	Country                *string
}

// decodePreferencesUpdate is req.content.decode(DigestPreferencesUpdate.self)
// with Foundation's DecodingError reasons, field by field in declaration
// order (the same walker as groups/decode.go, cut down to its field types).
func decodePreferencesUpdate(c *echo.Context) (preferencesUpdate, error) {
	var u preferencesUpdate
	if c.Request().Header.Get("Content-Type") == "" {
		return u, httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return u, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return u, httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	var obj map[string]json.RawMessage
	if err := apijson.UnmarshalKeyed(trimmed, &obj); err != nil || obj == nil {
		if json.Valid(trimmed) {
			return u, httpx.BadRequest(fmt.Sprintf(
				"Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found %s instead.",
				jsonKind(trimmed)))
		}
		return u, httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	present := func(name string) (json.RawMessage, bool) {
		v, ok := obj[name]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, false
		}
		return v, true
	}
	bools := []struct {
		name string
		dst  **bool
	}{
		{"emailEnabled", &u.EmailEnabled},
		{"pushEnabled", &u.PushEnabled},
		{"telegramEnabled", &u.TelegramEnabled},
		{"sentimentAlertsEnabled", &u.SentimentAlertsEnabled},
	}
	for _, f := range bools {
		if v, ok := present(f.name); ok {
			var b bool
			if json.Unmarshal(v, &b) != nil {
				return u, mismatch("Bool", f.name, v)
			}
			*f.dst = &b
		}
	}
	ints := []struct {
		name string
		dst  **int
	}{{"weekday", &u.Weekday}, {"localHour", &u.LocalHour}}
	for _, f := range ints {
		if v, ok := present(f.name); ok {
			n, err := decodeInt(v, f.name)
			if err != nil {
				return u, err
			}
			*f.dst = &n
		}
	}
	strs := []struct {
		name string
		dst  **string
	}{{"timezone", &u.Timezone}, {"country", &u.Country}}
	for _, f := range strs {
		if v, ok := present(f.name); ok {
			var s string
			if json.Unmarshal(v, &s) != nil {
				return u, mismatch("String", f.name, v)
			}
			*f.dst = &s
		}
	}
	return u, nil
}

// decodeInt is JSONDecoder's Int: a JSON number that fits exactly.
func decodeInt(v json.RawMessage, path string) (int, error) {
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 || !(trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9')) {
		return 0, mismatch("Int", path, v)
	}
	if n, err := strconv.ParseInt(string(trimmed), 10, 64); err == nil {
		return int(n), nil
	}
	if fl, err := strconv.ParseFloat(string(trimmed), 64); err == nil && fl == math.Trunc(fl) &&
		fl >= -9.223372036854775808e18 && fl < 9.223372036854775808e18 {
		return int(fl), nil
	}
	return 0, httpx.BadRequest(fmt.Sprintf(
		"Data corrupted at path '%s'. Parsed JSON number <%s> does not fit in Int.", path, string(trimmed)))
}

func mismatch(t, path string, v json.RawMessage) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		t, path, t, jsonKind(v)))
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
