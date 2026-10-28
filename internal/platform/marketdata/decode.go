package marketdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"norviq-api/internal/apijson"
)

// decodeStrict decodes a provider body the way Vapor's content decoder did:
// snake_case keys normalized to camelCase (JSONDecoder.backendAPI), and a
// missing or null value for a non-optional Swift property is a failure. A Go
// field is optional exactly when its json tag says omitempty.
func decodeStrict(data []byte, v any) error {
	if err := apijson.Unmarshal(data, v); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return err
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

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

func checkRequired(t reflect.Type, node any, path string) error {
	if reflect.PointerTo(t).Implements(unmarshalerType) || t.Implements(unmarshalerType) {
		return nil
	}
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
	case reflect.Map:
		obj, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		for k, item := range obj {
			if err := checkRequired(t.Elem(), item, path+"."+k); err != nil {
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
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			name, opts, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			value, present := obj[name]
			optional := strings.Contains(opts, "omitempty") || f.Type.Kind() == reflect.Pointer
			if !present || value == nil {
				if optional {
					continue
				}
				return fmt.Errorf("keyNotFound %s.%s", path, name)
			}
			if err := checkRequired(f.Type, value, path+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

// upstreamResponse is what a provider call came back with.
type upstreamResponse struct {
	status int
	header http.Header
	body   []byte
}

// bodyString is the trimmed body, as the Swift providers logged it.
func (r *upstreamResponse) bodyString() string { return strings.TrimSpace(string(r.body)) }

// doRequest sends req with a per-call timeout and reads the whole body.
func doRequest(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) (*upstreamResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return &upstreamResponse{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

var errEmptyBody = errors.New("empty body")

// decodeBody decodes a 200 body strictly; an empty body fails like Swift's.
func decodeBody(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return errEmptyBody
	}
	return decodeStrict(body, v)
}
