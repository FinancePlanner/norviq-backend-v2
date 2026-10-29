package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"norviq-api/internal/platform/portfolios"

	"github.com/google/uuid"
)

// actionArgs is ActionArguments: loosely typed tool arguments as a model
// produced them, coerced per accessor. A body that is not a JSON object
// reads as empty.
type actionArgs map[string]any

func parseArgs(raw string) actionArgs {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return actionArgs{}
	}
	return obj
}

// string is a non-empty String value.
func (a actionArgs) string(key string) (string, bool) {
	s, ok := a[key].(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func (a actionArgs) stringPtr(key string) *string {
	if s, ok := a.string(key); ok {
		return &s
	}
	return nil
}

// double accepts a JSON number or a numeric string.
func (a actionArgs) double(key string) (float64, bool) {
	switch v := a[key].(type) {
	case json.Number:
		f, err := strconv.ParseFloat(v.String(), 64)
		return f, err == nil
	case float64:
		return v, true
	case string:
		return swiftDouble(v)
	}
	return 0, false
}

func (a actionArgs) doublePtr(key string) *float64 {
	if f, ok := a.double(key); ok {
		return &f
	}
	return nil
}

// int accepts an integral JSON number, a fractional one (truncated), or an
// integer string.
func (a actionArgs) int(key string) (int, bool) {
	switch v := a[key].(type) {
	case json.Number:
		if n, err := strconv.Atoi(v.String()); err == nil {
			return n, true
		}
		f, err := strconv.ParseFloat(v.String(), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return int(f), true
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	return 0, false
}

// bool accepts a JSON bool, or a string equal to "true".
func (a actionArgs) bool(key string) (bool, bool) {
	switch v := a[key].(type) {
	case bool:
		return v, true
	case string:
		return v == "true", true
	}
	return false, false
}

func (a actionArgs) uuid(key string) (uuid.UUID, bool) {
	s, ok := a.string(key)
	if !ok {
		return uuid.Nil, false
	}
	return portfolios.ParseSwiftUUID(s)
}

// swiftDouble is Double(String): no surrounding whitespace, no hex floats
// with underscores; "inf"/"nan" spellings are accepted as Swift did.
func swiftDouble(s string) (float64, bool) {
	if s == "" || s[0] == ' ' || s[len(s)-1] == ' ' {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// strippingConfirm is AIAssistantTurnService.strippingConfirm: the arguments
// without a model-invented "confirm", re-encoded only when one was removed.
func strippingConfirm(raw string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
		return raw
	}
	if _, ok := obj["confirm"]; !ok {
		return raw
	}
	delete(obj, "confirm")
	out, err := marshalCompact(obj)
	if err != nil {
		return raw
	}
	return string(out)
}

// isErrorPayload: a JSON object with an "error" key.
func isErrorPayload(raw string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
		return false
	}
	_, ok := obj["error"]
	return ok
}

// marshalCompact encodes without HTML escaping or a trailing newline.
func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}
