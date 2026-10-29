package achievements

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// Reasons below were captured from the flagged Vapor (parity/achievements.jsonl).

func reasonOf(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return env.Reason
}

func TestBodyDecodingErrors(t *testing.T) {
	e := newEnv(t)
	_, tok := e.user("dec-body")
	const seen, showcase = "/v1/achievements/seen", "/v1/achievements/showcase"
	cases := []struct {
		method, path, body string
		status             int
		reason             string
	}{
		{http.MethodPost, seen, `{}`, 400, `No such key 'ids' at path ''. No value associated with key CodingKeys(stringValue: "ids", intValue: nil) ("ids").`},
		{http.MethodPost, seen, `{"ids":null}`, 400, `No value found (expected type 'Array<Any>') at path 'ids'. Cannot get unkeyed decoding container -- found null value instead.`},
		{http.MethodPost, seen, `{"ids":"nope"}`, 400, `Value was not of type 'Array<Any>' at path 'ids'. Expected to decode Array<Any> but found a string instead.`},
		{http.MethodPost, seen, `{"ids":{}}`, 400, `Value was not of type 'Array<Any>' at path 'ids'. Expected to decode Array<Any> but found a dictionary instead.`},
		{http.MethodPost, seen, `{"ids":[1]}`, 400, `Value was not of type 'String' at path 'ids.Index 0'. Expected to decode String but found number instead.`},
		{http.MethodPost, seen, `{"ids":[null]}`, 400, `No value found (expected type 'String') at path 'ids.Index 0'. Cannot get value of type String -- found null value instead.`},
		{http.MethodPost, seen, `{"ids":["5a000000-0000-4000-8000-00000000a101","x"]}`, 400, `Data corrupted at path 'ids.Index 1'. Attempted to decode UUID from invalid UUID string.`},
		{http.MethodPost, seen, `{"ids":["5a00000000004000800000000000a101"]}`, 400, `Data corrupted at path 'ids.Index 0'. Attempted to decode UUID from invalid UUID string.`},
		{http.MethodPost, seen, `{"ids":["5A000000-0000-4000-8000-00000000A101"]}`, 204, ``},
		{http.MethodPut, showcase, `{"items":[{"tier":""}]}`, 400, `No such key 'key' at path 'items.Index 0'. No value associated with key CodingKeys(stringValue: "key", intValue: nil) ("key").`},
		{http.MethodPut, showcase, `{"items":[{"key":"a","tier":""},{"key":"b"}]}`, 400, `No such key 'tier' at path 'items.Index 1'. No value associated with key CodingKeys(stringValue: "tier", intValue: nil) ("tier").`},
		{http.MethodPut, showcase, `{"items":[{"key":3,"tier":""}]}`, 400, `Value was not of type 'String' at path 'items.Index 0.key'. Expected to decode String but found number instead.`},
		{http.MethodPut, showcase, `{"items":[{"key":"a","tier":null}]}`, 400, `No value found (expected type 'String') at path 'items.Index 0.tier'. Cannot get value of type String -- found null value instead.`},
		{http.MethodPut, showcase, `{"items":[3]}`, 400, `Value was not of type 'Dictionary<String, Any>' at path 'items.Index 0'. Expected to decode Dictionary<String, Any> but found number instead.`},
		{http.MethodPut, showcase, `{"items":[null]}`, 400, `No value found (expected type 'Dictionary<String, Any>') at path 'items.Index 0'. Cannot get keyed decoding container -- found null value instead.`},
		{http.MethodPut, showcase, `[]`, 400, `Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found an array instead.`},
		{http.MethodPut, showcase, `null`, 400, `No value found (expected type 'Dictionary<String, Any>') at path ''. Cannot get keyed decoding container -- found null value instead.`},
		{http.MethodPut, showcase, `{"items":`, 400, ``},
	}
	for _, c := range cases {
		r := e.Do(c.method, c.path, tok, []byte(c.body))
		if r.Status != c.status {
			t.Fatalf("%s %s: %d %s", c.path, c.body, r.Status, r.Body)
		}
		if c.reason != "" {
			if got := reasonOf(t, r.Body); got != c.reason {
				t.Fatalf("%s %s:\n got %s\nwant %s", c.path, c.body, got, c.reason)
			}
		}
	}
}

func (e *env) raw(method, path, token, contentType string, body []byte) (int, string) {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.Server.URL+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.Server.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if len(data) == 0 {
		return resp.StatusCode, ""
	}
	return resp.StatusCode, reasonOf(e.t, data)
}

func TestBodyContentTypes(t *testing.T) {
	e := newEnv(t)
	_, tok := e.user("dec-ct")
	for _, c := range []struct {
		ct     string
		body   string
		status int
		reason string
	}{
		{"", `{"ids":[]}`, 415, "Can't decode data without a content type"},
		{"text/plain", `{"ids":[]}`, 400, "Value was not of type 'Dictionary<String, Decodable>' at path ''. Plaintext decoding does not support dictionaries."},
		{"application/xml", `{"ids":[]}`, 415, "Support for reading media type 'application/xml' has not been configured."},
		{"application/json", ``, 422, "Unprocessable Entity"},
	} {
		if status, reason := e.raw(http.MethodPost, "/v1/achievements/seen", tok, c.ct, []byte(c.body)); status != c.status || reason != c.reason {
			t.Fatalf("%q: %d %q", c.ct, status, reason)
		}
	}
	if status, _ := e.raw(http.MethodPost, "/v1/achievements/seen", tok, "application/json; charset=utf-8", []byte(`{"ids":[]}`)); status != 204 {
		t.Fatalf("json with charset: %d", status)
	}
}

func TestFeedLimitParsing(t *testing.T) {
	e := newEnv(t)
	_, tok := e.user("dec-limit")
	const bad = "Value was not of type 'Int' at path 'limit'. Data found at 'limit' was not Int."
	for q, want := range map[string]int{
		"limit=abc": 400, "limit=": 400, "limit=1.5": 400, "limit=+3": 400, "limit=1&limit=abc": 400,
		"limit": 200, "limit=-3": 200, "limit=abc&limit=2": 200, "limit=%2B3": 200, "": 200,
	} {
		r := e.Do(http.MethodGet, "/v1/social/feed?"+q, tok, nil)
		if r.Status != want {
			t.Fatalf("%q: %d %s", q, r.Status, r.Body)
		}
		if want == 400 && reasonOf(t, r.Body) != bad {
			t.Fatalf("%q: %s", q, r.Body)
		}
	}
}
