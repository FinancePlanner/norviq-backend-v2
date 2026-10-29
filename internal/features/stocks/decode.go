package stocks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// The request DTOs here are synthesized Swift Codable structs: a
// non-optional field is required, a wrong JSON type fails, and an enum raw
// value must be known. decodeBody validates the body against a field list
// the way Foundation's JSONDecoder walks it (declaration order, depth first)
// and returns Vapor's DecodingError reason for the first failure, then
// decodes the (valid) body into the Go struct.

type kind int

const (
	kindString kind = iota
	kindDouble
	kindEnum   // a String-backed enum: name is the Swift type, allowed its raw values
	kindObject // a nested struct: fields describe it
	kindArray  // an array of nested structs: fields describe the element
)

type field struct {
	name     string
	kind     kind
	optional bool
	typeName string   // Swift type for enums and nested structs
	allowed  []string // enum raw values
	fields   []field  // nested struct fields
}

func reqString(name string) field { return field{name: name, kind: kindString} }
func optString(name string) field { return field{name: name, kind: kindString, optional: true} }
func reqDouble(name string) field { return field{name: name, kind: kindDouble} }
func optDouble(name string) field { return field{name: name, kind: kindDouble, optional: true} }

// decodeBody is `req.content.decode(T.self)` for a JSON body.
func decodeBody(c *echo.Context, spec []field, target any) error {
	if c.Request().Header.Get("Content-Type") == "" {
		return httpx.Abort(415, "Can't decode data without a content type")
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	if err := validateBody(raw, spec); err != nil {
		return err
	}
	if err := apijson.Unmarshal(raw, target); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return nil
}

// validateBody checks raw against spec without the HTTP layer.
func validateBody(raw []byte, spec []field) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	return validateObject(trimmed, spec, nil, "Dictionary<String, Any>")
}

func pathString(path []string) string { return strings.Join(path, ".") }

func validateObject(raw []byte, spec []field, path []string, typeName string) error {
	var obj map[string]json.RawMessage
	if raw[0] != '{' {
		return httpx.BadRequest(fmt.Sprintf(
			"Value was not of type 'Dictionary<String, Any>' at path '%s'. Expected to decode Dictionary<String, Any> but found %s instead.",
			pathString(path), jsonKindDescription(raw)))
	}
	if err := apijson.UnmarshalKeyed(raw, &obj); err != nil {
		return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
	}
	_ = typeName
	for _, f := range spec {
		v, present := obj[f.name]
		v = bytes.TrimSpace(v)
		isNull := present && bytes.Equal(v, []byte("null"))
		fieldPath := append(append([]string{}, path...), f.name)
		if !present || isNull {
			if f.optional {
				continue
			}
			if !present {
				return httpx.BadRequest(fmt.Sprintf(
					"No such key '%s' at path '%s'. No value associated with key CodingKeys(stringValue: %q, intValue: nil) (%q).",
					f.name, pathString(path), f.name, f.name))
			}
			t := swiftTypeName(f)
			if f.kind == kindObject {
				return httpx.BadRequest(fmt.Sprintf(
					"No value found (expected type '%s') at path '%s'. Cannot get keyed decoding container -- found null value instead.",
					t, pathString(fieldPath)))
			}
			return httpx.BadRequest(fmt.Sprintf(
				"No value found (expected type '%s') at path '%s'. Cannot get value of type %s -- found null value instead.",
				t, pathString(fieldPath), t))
		}
		if err := validateValue(v, f, fieldPath); err != nil {
			return err
		}
	}
	return nil
}

func swiftTypeName(f field) string {
	switch f.kind {
	case kindString:
		return "String"
	case kindDouble:
		return "Double"
	case kindEnum:
		return "String"
	case kindObject:
		return "Dictionary<String, Any>"
	case kindArray:
		return "Array<Any>"
	}
	return "Any"
}

func validateValue(v []byte, f field, path []string) error {
	switch f.kind {
	case kindString, kindEnum:
		var s string
		if v[0] != '"' || json.Unmarshal(v, &s) != nil {
			return typeMismatch("String", v, path)
		}
		if f.kind == kindEnum {
			for _, a := range f.allowed {
				if s == a {
					return nil
				}
			}
			return httpx.BadRequest(fmt.Sprintf("Data corrupted at path '%s'. Cannot initialize %s from invalid String value %s.",
				pathString(path), f.typeName, s))
		}
	case kindDouble:
		var n float64
		if (v[0] != '-' && (v[0] < '0' || v[0] > '9')) || json.Unmarshal(v, &n) != nil {
			return typeMismatch("Double", v, path)
		}
	case kindObject:
		return validateObject(v, f.fields, path, f.typeName)
	case kindArray:
		if v[0] != '[' {
			return typeMismatch("Array<Any>", v, path)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(v, &items); err != nil {
			return typeMismatch("Array<Any>", v, path)
		}
		for i, item := range items {
			item = bytes.TrimSpace(item)
			itemPath := append(append([]string{}, path...), fmt.Sprintf("Index %d", i))
			if bytes.Equal(item, []byte("null")) {
				return httpx.BadRequest(fmt.Sprintf(
					"No value found (expected type 'Dictionary<String, Any>') at path '%s'. Cannot get keyed decoding container -- found null value instead.",
					pathString(itemPath)))
			}
			if err := validateObject(item, f.fields, itemPath, f.typeName); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeMismatch(t string, v []byte, path []string) error {
	return httpx.BadRequest(fmt.Sprintf("Value was not of type '%s' at path '%s'. Expected to decode %s but found %s instead.",
		t, pathString(path), t, jsonKindDescription(v)))
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

// ---- request specs -----------------------------------------------------------

var assetCategories = []string{"stock", "etf", "mutual_fund", "crypto", "cash", "bond", "real_estate", "commodity"}

var stockRequestSpec = []field{
	reqString("symbol"),
	reqDouble("shares"),
	reqDouble("buyPrice"),
	reqString("buyDate"),
	optString("notes"),
	{name: "category", kind: kindEnum, typeName: "AssetCategory", allowed: assetCategories},
	optString("portfolioListId"),
}

var bulkStockRequestSpec = []field{
	{name: "stocks", kind: kindArray, typeName: "StockRequest", fields: stockRequestSpec},
}

var sellStockRequestSpec = []field{
	reqDouble("sharesToSell"),
	reqDouble("sellPrice"),
	reqString("sellDate"),
}

var priceRangeSpec = []field{reqDouble("low"), reqDouble("high")}

var valuationRequestSpec = []field{
	reqString("symbol"),
	{name: "bearCase", kind: kindObject, typeName: "PriceRange", fields: priceRangeSpec},
	{name: "baseCase", kind: kindObject, typeName: "PriceRange", fields: priceRangeSpec},
	{name: "bullCase", kind: kindObject, typeName: "PriceRange", fields: priceRangeSpec},
	optString("rationale"),
	optString("targetDate"),
}

var createTransactionSpec = []field{
	reqString("symbol"),
	reqString("type"),
	reqDouble("quantity"),
	reqDouble("price"),
	optString("currency"),
	reqString("tradeDate"),
	optString("settleDate"),
	optDouble("fees"),
	optString("portfolioListId"),
}

var updateTransactionSpec = []field{
	optDouble("quantity"),
	optDouble("price"),
	optString("currency"),
	optString("tradeDate"),
	optString("settleDate"),
	optDouble("fees"),
}
