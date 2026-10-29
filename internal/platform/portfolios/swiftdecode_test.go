package portfolios_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"

	"github.com/labstack/echo/v5"
)

type color string

func (color) SwiftEnumCases() (string, []string) { return "Color", []string{"red", "blue"} }

type inner struct {
	Weight float64 `json:"weight"`
}

type decoded struct {
	Name    string            `json:"name"`
	Count   int               `json:"count"`
	Seed    uint64            `json:"seed"`
	Color   color             `json:"color"`
	On      bool              `json:"on"`
	ID      apijson.UUID      `json:"id"`
	When    apijson.Time      `json:"when"`
	Items   []inner           `json:"items"`
	Tags    map[string]string `json:"tags"`
	Note    *string           `json:"note,omitempty"`
	Extra   any               `json:"extra"`
	Raw     json.RawMessage   `json:"raw"`
	Skipped string            `json:"-"`
}

const validBody = `{"name":"n","count":2.0,"seed":18446744073709551615,"color":"red","on":true,
	"id":"7C9E6679-7425-40DE-944B-E07FC1F90AE7","when":"2026-01-02T03:04:05Z",
	"items":[{"weight":0.5}],"tags":{"a":"b"},"extra":[1,"x"],"raw":{"k":null}}`

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) {
		t.Fatalf("not an httpx.Error: %v", err)
	}
	return he.Reason
}

func TestDecodeSwiftAcceptsValidBody(t *testing.T) {
	var v decoded
	if err := portfolios.DecodeSwift([]byte(validBody), &v, portfolios.KeysBackendAPI); err != nil {
		t.Fatal(err)
	}
	if v.Name != "n" || v.Count != 2 || v.Seed != 18446744073709551615 || v.Color != "red" || !v.On ||
		len(v.Items) != 1 || v.Items[0].Weight != 0.5 || v.Tags["a"] != "b" || v.Note != nil || v.When.Unix() != 1767323045 {
		t.Fatalf("decoded %+v", v)
	}
	if string(v.Raw) != `{"k":null}` {
		t.Fatalf("raw %s", v.Raw)
	}
}

func TestDecodeSwiftReasons(t *testing.T) {
	cases := []struct {
		name, patch, want string
	}{
		{"missing first key", `{}`, `No such key 'name' at path ''. No value associated with key CodingKeys(stringValue: "name", intValue: nil) ("name").`},
		{"null required", `{"name":null}`, "No value found (expected type 'String') at path 'name'. Cannot get value of type String -- found null value instead."},
		{"type mismatch", `{"name":1}`, "Value was not of type 'String' at path 'name'. Expected to decode String but found number instead."},
		{"fraction int", `{"name":"n","count":1.5}`, "Data corrupted at path 'count'. Parsed JSON number <1.5> does not fit in Int."},
		{"string int", `{"name":"n","count":"1"}`, "Expected to decode Int but found a string instead."},
		{"negative uint", `{"name":"n","count":1,"seed":-1}`, "Parsed JSON number <-1> does not fit in UInt64."},
		{"bad enum", `{"name":"n","count":1,"seed":1,"color":"green"}`, "Data corrupted at path 'color'. Cannot initialize Color from invalid String value green."},
		{"enum type", `{"name":"n","count":1,"seed":1,"color":true}`, "Expected to decode String but found bool instead."},
		{"bool type", `{"name":"n","count":1,"seed":1,"color":"red","on":"yes"}`, "Expected to decode Bool but found a string instead."},
		{"bad uuid", `{"name":"n","count":1,"seed":1,"color":"red","on":true,"id":"nope"}`, "Attempted to decode UUID from invalid UUID string."},
		{"uuid type", `{"name":"n","count":1,"seed":1,"color":"red","on":true,"id":[]}`, "Expected to decode String but found an array instead."},
		{"bad date", `{"name":"n","count":1,"seed":1,"color":"red","on":true,"id":"7C9E6679-7425-40DE-944B-E07FC1F90AE7","when":"soon"}`, "Data corrupted at path 'when'. Date must be an ISO8601 string or seconds since Apple reference date."},
		{"date type", `{"name":"n","count":1,"seed":1,"color":"red","on":true,"id":"7C9E6679-7425-40DE-944B-E07FC1F90AE7","when":{}}`, "Expected to decode String but found a dictionary instead."},
		{"nested", `{"name":"n","count":1,"seed":1,"color":"red","on":true,"id":"7C9E6679-7425-40DE-944B-E07FC1F90AE7","when":0,"items":[{}]}`, "No such key 'weight' at path 'items.Index 0'."},
		{"array type", `{"name":"n","count":1,"seed":1,"color":"red","on":true,"id":"7C9E6679-7425-40DE-944B-E07FC1F90AE7","when":0,"items":{}}`, "Expected to decode Array<Any> but found a dictionary instead."},
		{"map value", `{"name":"n","count":1,"seed":1,"color":"red","on":true,"id":"7C9E6679-7425-40DE-944B-E07FC1F90AE7","when":0,"items":[],"tags":{"a":1}}`, "at path 'tags.a'."},
		{"map type", `{"name":"n","count":1,"seed":1,"color":"red","on":true,"id":"7C9E6679-7425-40DE-944B-E07FC1F90AE7","when":0,"items":[],"tags":[]}`, "Expected to decode Dictionary<String, Any> but found an array instead."},
		{"invalid json", `{"name":`, "Data corrupted at path ''. The given data was not valid JSON."},
		{"trailing json", `{} {}`, "The given data was not valid JSON."},
		{"top-level type", `"x"`, "Expected to decode Dictionary<String, Any> but found a string instead."},
	}
	for _, tc := range cases {
		var v decoded
		err := portfolios.DecodeSwift([]byte(tc.patch), &v, portfolios.KeysBackendAPI)
		if err == nil {
			t.Fatalf("%s: no error", tc.name)
		}
		if got := reasonOf(t, err); !strings.Contains(got, tc.want) {
			t.Fatalf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
	if err := portfolios.DecodeSwift([]byte(`{}`), decoded{}, portfolios.KeysBackendAPI); err == nil {
		t.Fatal("non-pointer accepted")
	}
	var bad struct {
		C complex128 `json:"c"`
	}
	if err := portfolios.DecodeSwift([]byte(`{"c":1}`), &bad, portfolios.KeysBackendAPI); err == nil {
		t.Fatal("unsupported type accepted")
	}
}

func TestDecodeSwiftKeyStrategies(t *testing.T) {
	var v struct {
		TargetWeight float64 `json:"targetWeight"`
	}
	if err := portfolios.DecodeSwift([]byte(`{"target_weight":0.25}`), &v, portfolios.KeysConvertFromSnakeCase); err != nil || v.TargetWeight != 0.25 {
		t.Fatalf("convertFromSnakeCase: %v %v", v, err)
	}
	if err := portfolios.DecodeSwift([]byte(`{"target_weight":0.5}`), &v, portfolios.KeysBackendAPI); err != nil || v.TargetWeight != 0.5 {
		t.Fatalf("backendAPI: %v %v", v, err)
	}
	for in, want := range map[string]string{
		"target_weight": "targetWeight", "_private_key_": "_privateKey_", "a__b": "aB", "___": "___",
		"plain": "plain", "URL_value": "URLValue", "fx_RATE": "fxRate",
	} {
		if got := portfolios.ConvertFromSnakeCase(in); got != want {
			t.Fatalf("ConvertFromSnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"targetWeight": "target_weight", "fxURL": "fx_url", "URLValue": "url_value", "": "", "id": "id",
	} {
		if got := portfolios.ConvertToSnakeCase(in); got != want {
			t.Fatalf("ConvertToSnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeSwiftBodyRequiresContentType(t *testing.T) {
	e := echo.New()
	var v decoded
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(validBody))
	err := portfolios.DecodeSwiftBody(e.NewContext(req, httptest.NewRecorder()), &v)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusUnsupportedMediaType || he.Reason != "Can't decode data without a content type" {
		t.Fatalf("no content type: %v", err)
	}
	req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(validBody))
	req.Header.Set("Content-Type", "application/json")
	if err := portfolios.DecodeSwiftBody(e.NewContext(req, httptest.NewRecorder()), &v); err != nil {
		t.Fatal(err)
	}
}
