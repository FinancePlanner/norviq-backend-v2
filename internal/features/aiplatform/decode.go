package aiplatform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// fieldKind is the Swift type a body field decodes as.
type fieldKind string

const (
	kindString fieldKind = "String"
	kindBool   fieldKind = "Bool"
)

type fieldSpec struct {
	name     string
	kind     fieldKind
	required bool
}

// decodeObject reads a JSON object body the way a Swift Decodable with the
// given fields did, in declaration order, and returns Vapor's DecodingError
// reasons for the common failures (missing key, null, wrong type) so the 400
// text matches. Values are the decoded strings and bools; absent or null
// optionals are missing from the map.
func decodeObject(c *echo.Context, fields []fieldSpec) (map[string]any, error) {
	if c.Request().Header.Get("Content-Type") == "" {
		return nil, httpx.Abort(415, "Can't decode data without a content type")
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := apijson.Unmarshal(raw, &obj); err != nil {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && json.Valid(trimmed) {
			return nil, httpx.BadRequest(fmt.Sprintf(
				"Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found %s instead.",
				jsonKindDescription(trimmed)))
		}
		return nil, httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	if obj == nil {
		return nil, httpx.BadRequest("Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found null instead.")
	}
	out := map[string]any{}
	for _, f := range fields {
		v, present := obj[f.name]
		isNull := present && bytes.Equal(bytes.TrimSpace(v), []byte("null"))
		if !present || isNull {
			if !f.required {
				continue
			}
			if !present {
				return nil, httpx.BadRequest(fmt.Sprintf(
					"No such key '%s' at path ''. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
					f.name, f.name, f.name))
			}
			return nil, httpx.BadRequest(fmt.Sprintf(
				"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.",
				f.kind, f.name, f.kind))
		}
		switch f.kind {
		case kindString:
			var s string
			if json.Unmarshal(v, &s) != nil {
				return nil, typeMismatch(f, v)
			}
			out[f.name] = s
		case kindBool:
			var b bool
			if json.Unmarshal(v, &b) != nil {
				return nil, typeMismatch(f, v)
			}
			out[f.name] = b
		}
	}
	return out, nil
}

func typeMismatch(f fieldSpec, v json.RawMessage) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		f.kind, f.name, f.kind, jsonKindDescription(v)))
}

// jsonKindDescription is how Foundation's decoder named the JSON it found.
func jsonKindDescription(v []byte) string {
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

func optString(m map[string]any, k string) *string {
	if v, ok := m[k].(string); ok {
		return &v
	}
	return nil
}
