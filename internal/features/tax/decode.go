package tax

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	dec "norviq-api/internal/platform/nsdecimal"

	"github.com/labstack/echo/v5"
)

// decodeBody is `req.content.decode(T.self)`: Vapor picks the decoder by
// Content-Type, a body-less request is a 422, and a Swift DecodingError is a
// 400 whose reason names the first failing key in declaration order. The
// value is then decoded type-directed (snake_case keys accepted).
func decodeBody(c *echo.Context, v any) error {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	if raw == "" {
		return httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed", "Can't decode data without a content type")
	}
	mt, _, perr := mime.ParseMediaType(raw)
	if perr != nil {
		mt = strings.ToLower(raw)
	}
	if mt != "application/json" && mt != "application/vnd.api+json" {
		return httpx.CodedAbort(http.StatusUnsupportedMediaType, "request_failed",
			"Support for reading media type '"+raw+"' has not been configured.")
	}
	var generic any
	if err := apijson.UnmarshalKeyed(body, &generic); err != nil {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON. Underlying error: " + err.Error() + ".")
	}
	if err := validate(reflect.TypeOf(v).Elem(), generic, nil, ""); err != nil {
		return err
	}
	if err := apijson.Unmarshal(body, v); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return nil
}

var decimalType = reflect.TypeOf(dec.Decimal{})

func joinCodingPath(path []string) string { return strings.Join(path, ".") }

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "a dictionary"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "bool"
	case float64, json.Number:
		return "number"
	}
	return "unknown"
}

func swiftTypeName(t reflect.Type) string {
	switch {
	case t == decimalType:
		return "Decimal"
	case t.Kind() == reflect.String:
		return "String"
	case t.Kind() == reflect.Int:
		return "Int"
	case t.Kind() == reflect.Bool:
		return "Bool"
	case t.Kind() == reflect.Slice:
		return "Array<Any>"
	}
	return "Dictionary<String, Any>"
}

func mismatch(t reflect.Type, path []string, v any) error {
	typ := swiftTypeName(t)
	found := kindOf(v)
	if n, ok := v.(float64); ok {
		found = "number " + strconv.FormatFloat(n, 'f', -1, 64)
	}
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		typ, joinCodingPath(path), typ, found))
}

// validate walks the decoded JSON the way Swift's synthesized Decodable
// would, reporting the first error with Foundation's wording.
func validate(t reflect.Type, v any, path []string, enum string) error {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == decimalType:
		switch v.(type) {
		case float64, json.Number:
			return nil
		}
		return mismatch(t, path, v)
	case t.Kind() == reflect.String:
		s, ok := v.(string)
		if !ok {
			return mismatch(t, path, v)
		}
		if enum != "" {
			set := enumSets[enum]
			if !oneOf(s, set.cases) {
				return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Cannot initialize %s from invalid String value %s.",
					joinCodingPath(path), set.name, s))
			}
		}
		return nil
	case t.Kind() == reflect.Int:
		n, ok := v.(float64)
		if !ok {
			return mismatch(t, path, v)
		}
		if n != float64(int64(n)) {
			return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Parsed JSON number <%s> does not fit in Int.",
				joinCodingPath(path), strconv.FormatFloat(n, 'f', -1, 64)))
		}
		return nil
	case t.Kind() == reflect.Bool:
		if _, ok := v.(bool); !ok {
			return mismatch(t, path, v)
		}
		return nil
	case t.Kind() == reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return mismatch(t, path, v)
		}
		for i, e := range arr {
			p := append(append([]string{}, path...), "Index "+strconv.Itoa(i))
			if e == nil && t.Elem().Kind() != reflect.Pointer {
				return valueNotFound(t.Elem(), p)
			}
			if err := validate(t.Elem(), e, p, ""); err != nil {
				return err
			}
		}
		return nil
	case t.Kind() == reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return mismatch(t, path, v)
		}
		for k, e := range m {
			if err := validate(t.Elem(), e, append(append([]string{}, path...), k), ""); err != nil {
				return err
			}
		}
		return nil
	case t.Kind() == reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return mismatch(t, path, v)
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			val, present := m[name]
			p := append(append([]string{}, path...), name)
			optional := f.Type.Kind() == reflect.Pointer
			if !present || val == nil {
				if optional {
					continue
				}
				if !present {
					return httpx.BadRequest(fmt.Sprintf(
						"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
						name, joinCodingPath(path), name, name))
				}
				return valueNotFound(f.Type, p)
			}
			if err := validate(f.Type, val, p, f.Tag.Get("enum")); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func valueNotFound(t reflect.Type, path []string) error {
	typ := swiftTypeName(t)
	return httpx.BadRequest(fmt.Sprintf("No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.",
		typ, joinCodingPath(path), typ))
}

// taxQuery is TaxController.TaxQuery decoded from the URL query.
type taxQuery struct {
	Jurisdiction *Jurisdiction
	TaxYear      *int
}

func lastQueryValue(c *echo.Context, name string) (string, bool) {
	vals, ok := c.Request().URL.Query()[name]
	if !ok || len(vals) == 0 {
		return "", false
	}
	return vals[len(vals)-1], true
}

// decodeTaxQuery is `req.query.decode(TaxQuery.self)`.
func decodeTaxQuery(c *echo.Context) (taxQuery, error) {
	var q taxQuery
	if raw, ok := lastQueryValue(c, "jurisdiction"); ok {
		j := Jurisdiction(raw)
		if !j.valid() {
			return q, httpx.BadRequest("Data corrupted at path 'jurisdiction'. Cannot initialize TaxJurisdiction from invalid String value " + raw)
		}
		q.Jurisdiction = &j
	}
	if raw, ok := lastQueryValue(c, "taxYear"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return q, httpx.BadRequest("Data corrupted at path 'taxYear'. Unable to convert data '" + raw + "' to Int.")
		}
		q.TaxYear = &n
	}
	return q, nil
}

// jurisdiction is `query.jurisdiction ?? .unitedStates`.
func (q taxQuery) jurisdiction() Jurisdiction {
	if q.Jurisdiction == nil {
		return US
	}
	return *q.Jurisdiction
}
