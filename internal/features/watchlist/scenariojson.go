package watchlist

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"norviq-api/internal/httpx"
)

// scenarioJSON is Vapor's ScenarioJSON: a struct with one property,
// `values: [String: ScenarioJSONValue]`, so on the wire and in jsonb it is
// {"values": {...}}. ScenarioJSONValue numbers are Doubles.
type scenarioJSON struct {
	Values map[string]any
}

func emptyScenarioJSON() scenarioJSON { return scenarioJSON{Values: map[string]any{}} }

func (s *scenarioJSON) decodeSwift(raw json.RawMessage, path string) error {
	kind := jsonKind(raw)
	if kind == "null" {
		return httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type '%s') at path '%s'. Cannot get keyed decoding container -- found null value instead.", dictType, path))
	}
	if kind != "a dictionary" {
		return typeMismatch(dictType, path, kind)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	values, ok := top["values"]
	if !ok {
		return keyNotFound("values", path)
	}
	vpath := joinPath(path, "values")
	switch k := jsonKind(values); k {
	case "a dictionary":
	case "null":
		return httpx.BadRequest(fmt.Sprintf(
			"No value found (expected type '%s') at path '%s'. Cannot get keyed decoding container -- found null value instead.", dictType, vpath))
	default:
		return typeMismatch(dictType, vpath, k)
	}
	m, err := genericObject(values)
	if err != nil {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	s.Values = m
	return nil
}

// MarshalJSON writes {"values": {...}}.
func (s scenarioJSON) MarshalJSON() ([]byte, error) {
	v := s.Values
	if v == nil {
		v = map[string]any{}
	}
	return json.Marshal(map[string]any{"values": v})
}

func genericObject(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// errFluentScenarioJSON is the error Fluent raised loading a row whose jsonb
// column is not ScenarioJSON-shaped (a 500).
func errFluentScenarioJSON(column string) error {
	return errors.New("invalid field: '" + column + "', type: ScenarioJSON, error: DecodingError.keyNotFound: " +
		"Key 'values' not found in keyed decoding container. Debug description: No value associated with key " +
		`CodingKeys(stringValue: "values", intValue: nil) ("values").`)
}

// loadScenarioJSON is Fluent decoding a ScenarioJSON column.
func loadScenarioJSON(raw json.RawMessage, column string) (scenarioJSON, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return scenarioJSON{}, errFluentScenarioJSON(column)
	}
	values, ok := top["values"]
	if !ok || jsonKind(values) != "a dictionary" {
		return scenarioJSON{}, errFluentScenarioJSON(column)
	}
	m, err := genericObject(values)
	if err != nil {
		return scenarioJSON{}, errFluentScenarioJSON(column)
	}
	return scenarioJSON{Values: m}, nil
}
