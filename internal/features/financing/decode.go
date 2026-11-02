package financing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"

	"github.com/labstack/echo/v5"
)

// Vapor's `req.content.decode(T.self)` picked a decoder by Content-Type:
// none was a 415, an empty JSON body a 422, and a DecodingError (missing
// key, null, wrong type, bad enum value) a 400. The 400 reasons here are not
// Foundation's text; parity compares them with -ignore reason.

type mediaKind int

const (
	mediaJSON mediaKind = iota
	mediaForm
	mediaMultipart
)

func mediaType(c *echo.Context) (mediaKind, map[string]string, error) {
	raw := strings.TrimSpace(c.Request().Header.Get(echo.HeaderContentType))
	if raw == "" {
		return 0, nil, httpx.Abort(http.StatusUnsupportedMediaType, "Can't decode data without a content type")
	}
	mt, params, err := mime.ParseMediaType(raw)
	if err != nil {
		mt = strings.ToLower(raw)
	}
	switch mt {
	case "application/json", "application/vnd.api+json":
		return mediaJSON, params, nil
	case "application/x-www-form-urlencoded":
		return mediaForm, params, nil
	case "multipart/form-data":
		return mediaMultipart, params, nil
	case "text/plain":
		return 0, nil, httpx.BadRequest("Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries.")
	}
	return 0, nil, httpx.Abort(http.StatusUnsupportedMediaType, "Support for reading media type '"+raw+"' has not been configured.")
}

// validator is the enum check Swift's RawRepresentable decode did.
type validator interface{ validate() error }

// decodeBody is `req.content.decode(T.self)` for a JSON (or flat
// urlencoded) body.
func decodeBody(c *echo.Context, v validator) error {
	kind, params, err := mediaType(c)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return httpx.Abort(http.StatusUnprocessableEntity, "")
	}
	switch kind {
	case mediaForm:
		values, perr := url.ParseQuery(string(body))
		if perr != nil {
			return httpx.BadRequest(perr.Error())
		}
		if err := decodeForm(values, v, formMissing); err != nil {
			return httpx.BadRequest(err.Error())
		}
	case mediaMultipart:
		if params["boundary"] == "" {
			return httpx.Abort(http.StatusUnsupportedMediaType, "Unsupported Media Type")
		}
		if err := decodeForm(multipartValues(body, params["boundary"]), v, multipartMissing); err != nil {
			return httpx.BadRequest(err.Error())
		}
	default:
		if err := decodeStrict(body, v); err != nil {
			return httpx.BadRequest(err.Error())
		}
	}
	if err := v.validate(); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return nil
}

// decodeStrict decodes like Swift's JSONDecoder: snake_case keys normalized,
// and a missing or null value for a non-optional property is an error. A Go
// field is optional exactly when it is a pointer.
func decodeStrict(data []byte, v any) error {
	if err := apijson.Unmarshal(data, v); err != nil {
		return err
	}
	var tree any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return err
	}
	if tree == nil {
		//lint:ignore ST1005 Foundation's wording is part of the wire-compatible error.
		return errors.New("Cannot get keyed decoding container -- found null value instead.")
	}
	return checkRequired(reflect.TypeOf(v).Elem(), normalizeTree(tree), "")
}

func normalizeTree(node any) any {
	switch n := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, v := range n {
			key := k
			if strings.Contains(k, "_") {
				key = apijson.NormalizeSnakeCaseKey(k)
			}
			if _, exists := out[key]; exists && key != k {
				continue
			}
			out[key] = normalizeTree(v)
		}
		return out
	case []any:
		for i := range n {
			n[i] = normalizeTree(n[i])
		}
		return n
	}
	return node
}

func checkRequired(t reflect.Type, node any, path string) error {
	switch t.Kind() {
	case reflect.Pointer:
		if node == nil {
			return nil
		}
		return checkRequired(t.Elem(), node, path)
	case reflect.Slice:
		items, ok := node.([]any)
		if !ok {
			return nil
		}
		for i, item := range items {
			if err := checkRequired(t.Elem(), item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		obj, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			value, present := obj[name]
			if !present || value == nil {
				if f.Type.Kind() == reflect.Pointer {
					continue
				}
				//lint:ignore ST1005 Foundation's wording is part of the wire-compatible error.
				return fmt.Errorf("No value associated with key '%s' at path '%s'.", name, path)
			}
			sub := name
			if path != "" {
				sub = path + "." + name
			}
			if err := checkRequired(f.Type, value, sub); err != nil {
				return err
			}
		}
	}
	return nil
}

// formMissing is URLEncodedFormDecoder's error for an absent key.
func formMissing(name, typ string) error {
	//lint:ignore ST1005 Vapor's wording is part of the wire-compatible error.
	return fmt.Errorf("No value found (expected type '%s') at path '%s'. No %s was found at '%s'.", typ, name, typ, name)
}

// multipartMissing is FormDataDecoder's error for an absent key.
func multipartMissing(name, _ string) error {
	//lint:ignore ST1005 Vapor's wording is part of the wire-compatible error.
	return fmt.Errorf("No such key '%s' at path ''. No value associated with key %q.", name, name)
}

// multipartValues reads the text parts of a multipart body (the last part
// with a name wins); an unparseable body yields what was read so far.
func multipartValues(body []byte, boundary string) url.Values {
	values := url.Values{}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		p, err := r.NextRawPart()
		if err != nil {
			return values
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return values
		}
		if name := p.FormName(); name != "" {
			values[name] = []string{string(data)}
		}
	}
}

// swiftTypeName names a Go field kind as the Swift decoder did.
func swiftTypeName(k reflect.Kind) string {
	switch k {
	case reflect.Float64:
		return "Double"
	case reflect.Int:
		return "Int"
	}
	return "String"
}

// decodeForm is URLEncodedFormDecoder (and FormDataDecoder) for the flat
// request types: every value arrives as a string, keys exactly as sent, and
// is converted to the field's type.
func decodeForm(values url.Values, v any, missing func(name, typ string) error) error {
	rv := reflect.ValueOf(v).Elem()
	rt := rv.Type()
	for i := range rt.NumField() {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		raw, present := formValue(values, name)
		ft := f.Type
		optional := ft.Kind() == reflect.Pointer
		if optional {
			ft = ft.Elem()
		}
		if !present {
			if optional {
				continue
			}
			return missing(name, swiftTypeName(ft.Kind()))
		}
		target := reflect.New(ft).Elem()
		switch ft.Kind() {
		case reflect.String:
			target.SetString(raw)
		case reflect.Float64:
			n, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				//lint:ignore ST1005 Foundation's wording is part of the wire-compatible error.
				return fmt.Errorf("Could not convert value at '%s' to Double.", name)
			}
			target.SetFloat(n)
		case reflect.Int:
			n, err := strconv.Atoi(raw)
			if err != nil {
				//lint:ignore ST1005 Foundation's wording is part of the wire-compatible error.
				return fmt.Errorf("Could not convert value at '%s' to Int.", name)
			}
			target.SetInt(int64(n))
		default:
			//lint:ignore ST1005 Foundation's wording is part of the wire-compatible error.
			return fmt.Errorf("Value at '%s' cannot be decoded from a form.", name)
		}
		if optional {
			p := reflect.New(ft)
			p.Elem().Set(target)
			rv.Field(i).Set(p)
		} else {
			rv.Field(i).Set(target)
		}
	}
	return nil
}

func formValue(values url.Values, name string) (string, bool) {
	if vs, ok := values[name]; ok && len(vs) > 0 {
		return vs[len(vs)-1], true
	}
	return "", false
}
