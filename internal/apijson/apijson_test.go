package apijson

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeSnakeCaseKey(t *testing.T) {
	cases := map[string]string{
		"user_id":         "userId",
		"avatar_url":      "avatarURL",
		"redirect_uri":    "redirectURI",
		"FIRST_name":      "firstName",
		"already":         "already",
		"a__b":            "aB",
		"portfolio_id_x":  "portfolioIdX",
		"_leading":        "leading",
		"share_url_token": "shareURLToken",
	}
	for in, want := range cases {
		if got := NormalizeSnakeCaseKey(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestUnmarshalAcceptsBothKeySpellings(t *testing.T) {
	type body struct {
		UserID    string `json:"userId"`
		AvatarURL string `json:"avatarURL"`
		Nested    struct {
			IsPublic bool `json:"isPublic"`
		} `json:"nestedThing"`
	}
	for _, raw := range []string{
		`{"user_id":"a","avatar_url":"b","nested_thing":{"is_public":true}}`,
		`{"userId":"a","avatarURL":"b","nestedThing":{"isPublic":true}}`,
	} {
		var b body
		if err := Unmarshal([]byte(raw), &b); err != nil {
			t.Fatal(err)
		}
		if b.UserID != "a" || b.AvatarURL != "b" || !b.Nested.IsPublic {
			t.Errorf("%s decoded to %+v", raw, b)
		}
	}
}

func TestTimeWireFormat(t *testing.T) {
	ts := time.Date(2026, 10, 28, 12, 30, 45, 987654321, time.FixedZone("x", 3600))
	got, err := Marshal(struct {
		At Time `json:"at"`
	}{NewTime(ts)})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"at":"2026-10-28T11:30:45Z"}` {
		t.Errorf("got %s", got)
	}
}

func TestParseTimeVariants(t *testing.T) {
	want := time.Date(2026, 10, 28, 11, 30, 45, 0, time.UTC)
	for _, raw := range []string{
		`"2026-10-28T11:30:45Z"`,
		`"2026-10-28T12:30:45+01:00"`,
		`"2026-10-28T11:30:45.000Z"`,
		// Seconds since 2001-01-01.
		`814879845`,
		`"814879845"`,
	} {
		got, err := ParseTimeJSON([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if !got.Equal(want) {
			t.Errorf("%s: got %v", raw, got)
		}
	}
	day, err := ParseTimeJSON([]byte(`"2026-10-28"`))
	if err != nil || !day.Equal(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date-only: %v %v", day, err)
	}
}

func TestUUIDUppercase(t *testing.T) {
	id := uuid.MustParse("4b06f5a8-1234-4abc-9def-0123456789ab")
	got, _ := Marshal(NewUUID(id))
	if string(got) != `"4B06F5A8-1234-4ABC-9DEF-0123456789AB"` {
		t.Errorf("got %s", got)
	}
	var back UUID
	if err := Unmarshal([]byte(`"4b06f5a8-1234-4abc-9def-0123456789ab"`), &back); err != nil || back.Std() != id {
		t.Errorf("roundtrip: %v %v", back, err)
	}
}

func TestMarshalDoesNotEscapeHTML(t *testing.T) {
	got, _ := Marshal(map[string]string{"q": "a<b&c"})
	if string(got) != `{"q":"a<b&c"}` {
		t.Errorf("got %s", got)
	}
}

func TestUnmarshalKeepsDictionaryKeys(t *testing.T) {
	type item struct {
		GivesAccess bool `json:"givesAccess"`
	}
	type base struct {
		CreatedBy string `json:"createdBy"`
	}
	type body struct {
		base
		DisplayName string            `json:"displayName"`
		Labels      map[string]string `json:"labels"`
		Items       map[string]*item  `json:"items"`
		List        []item            `json:"list"`
		Loose       any               `json:"loose"`
	}
	var got body
	err := Unmarshal([]byte(`{
		"display_name": "n",
		"created_by": "me",
		"labels": {"pro_monthly": "a"},
		"items": {"pro_annual": {"gives_access": true}},
		"list": [{"gives_access": true}],
		"loose": {"some_key": 1}
	}`), &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "n" || got.CreatedBy != "me" {
		t.Errorf("struct keys not rewritten: %+v", got)
	}
	if got.Labels["pro_monthly"] != "a" {
		t.Errorf("map key rewritten: %v", got.Labels)
	}
	if it := got.Items["pro_annual"]; it == nil || !it.GivesAccess {
		t.Errorf("map value: %v", got.Items)
	}
	if len(got.List) != 1 || !got.List[0].GivesAccess {
		t.Errorf("slice elem: %v", got.List)
	}
	if m, _ := got.Loose.(map[string]any); m["someKey"] == nil {
		t.Errorf("interface target not rewritten: %v", got.Loose)
	}
}

func TestUnmarshalKeyedRewritesRawContainers(t *testing.T) {
	var obj map[string]json.RawMessage
	if err := UnmarshalKeyed([]byte(`{"app_user_id": {"inner_key": 1}}`), &obj); err != nil {
		t.Fatal(err)
	}
	if string(obj["appUserId"]) != `{"innerKey":1}` {
		t.Errorf("got %v", obj)
	}
}

// show_source_urls rewrites to showSourceUrls; the Swift property is
// showSourceURLs. The fallback maps a rewritten key that misses exactly to
// the one field that matches it case-insensitively.
func TestUnmarshalAcronymFieldsFromSnakeCase(t *testing.T) {
	type settings struct {
		ShowSourceURLs bool `json:"showSourceURLs"`
		ShowXP         bool `json:"showXP"`
		Nested         *struct {
			FeedIDs []string `json:"feedIDs"`
			Extra   struct {
				InnerKey int `json:"innerKey"`
			} `json:"extra"`
		} `json:"nestedURLs"`
	}
	var s settings
	raw := `{"show_source_urls":true,"show_xp":true,"nested_urls":{"feed_ids":["a"],"extra":{"inner_key":7}}}`
	if err := Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if !s.ShowSourceURLs || !s.ShowXP || s.Nested == nil || len(s.Nested.FeedIDs) != 1 || s.Nested.Extra.InnerKey != 7 {
		t.Fatalf("decoded to %+v (nested %+v)", s, s.Nested)
	}
	// The rewritten key now carries the field's exact name.
	out, err := normalizeKeys([]byte(raw), reflect.TypeFor[settings]())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"nestedURLs":{"extra":{"innerKey":7},"feedIDs":["a"]},"showSourceURLs":true,"showXP":true}` {
		t.Fatalf("normalized %s", out)
	}
}

// The camelCase spelling still wins over its snake_case twin when the twin
// reaches the field only through the fallback (before, the twin sorted
// after it and overwrote it).
func TestUnmarshalExactCamelBeatsFoldedTwin(t *testing.T) {
	var s struct {
		ShowSourceURLs bool `json:"showSourceURLs"`
	}
	if err := Unmarshal([]byte(`{"show_source_urls":false,"showSourceURLs":true}`), &s); err != nil {
		t.Fatal(err)
	}
	if !s.ShowSourceURLs {
		t.Fatal("snake_case twin overwrote the camelCase key")
	}
}

// An exact field name always wins over a case-insensitive one, and a
// rewritten key that matches two fields only by case decodes into neither.
func TestUnmarshalFallbackExactWinsAndAmbiguityPicksNone(t *testing.T) {
	var exact struct {
		Folded string `json:"fooID"`
		Exact  string `json:"fooId"`
	}
	if err := Unmarshal([]byte(`{"foo_id":"x"}`), &exact); err != nil {
		t.Fatal(err)
	}
	if exact.Exact != "x" || exact.Folded != "" {
		t.Fatalf("exact match lost: %+v", exact)
	}
	var amb struct {
		First  string `json:"barURLs"`
		Second string `json:"barUrLs"`
		Other  string `json:"other"`
	}
	if err := Unmarshal([]byte(`{"bar_urls":"x","other_":"y"}`), &amb); err != nil {
		t.Fatal(err)
	}
	if amb.First != "" || amb.Second != "" || amb.Other != "y" {
		t.Fatalf("ambiguous key landed on a field: %+v", amb)
	}
	// The type used to rewrite nested keys is deterministic too: no type.
	fields := structFields(reflect.TypeOf(amb))
	for range 50 {
		if _, ok := lookupField(fields, "barurls"); ok {
			t.Fatal("ambiguous key resolved to a field")
		}
	}
}

// Keys the client spelled in camelCase, and every map key, are untouched
// by the fallback.
func TestUnmarshalFallbackLeavesCamelAndMapKeys(t *testing.T) {
	type body struct {
		Tags map[string]bool `json:"tagIDs"`
	}
	out, err := normalizeKeys([]byte(`{"tag_ids":{"show_xp":true,"showxp":true},"shOwXp":1}`), reflect.TypeFor[body]())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"shOwXp":1,"tagIDs":{"show_xp":true,"showxp":true}}` {
		t.Fatalf("normalized %s", out)
	}
}

func TestLookupKey(t *testing.T) {
	var obj map[string]json.RawMessage
	if err := UnmarshalKeyed([]byte(`{"show_source_urls":true,"show_xp":false,"plain":1}`), &obj); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"showSourceURLs": "true", "showXP": "false", "plain": "1"} {
		if v, ok := LookupKey(obj, name); !ok || string(v) != want {
			t.Errorf("%s: %q %v", name, v, ok)
		}
	}
	if _, ok := LookupKey(obj, "missing"); ok {
		t.Error("missing key found")
	}
	// Exact wins over a key that only folds to the name.
	both := map[string]int{"showXP": 1, "showXp": 2}
	if v, _ := LookupKey(both, "showXP"); v != 1 {
		t.Errorf("exact lost: %d", v)
	}
	if v, _ := LookupKey(both, "showXp"); v != 2 {
		t.Errorf("exact lost: %d", v)
	}
	// Two keys that only fold to the name: neither.
	if v, ok := LookupKey(map[string]int{"showXp": 1, "SHOWXP": 2}, "showXP"); ok {
		t.Errorf("ambiguous key resolved to %d", v)
	}
	if _, ok := LookupKey(map[string]int(nil), "x"); ok {
		t.Error("nil map")
	}
}

// A decoder that reads two names differing only by case (redirectURI and
// redirectUri) gets exact matches only, so one key never feeds both.
func TestLookupKeySiblingsDifferingByCaseStayExact(t *testing.T) {
	names := []string{"redirectURI", "redirectUri", "other"}
	obj := map[string]int{"redirectUri": 1, "OTHER": 2}
	if _, ok := LookupKey(obj, "redirectURI", names...); ok {
		t.Error("redirectURI fell back to redirectUri")
	}
	if v, ok := LookupKey(obj, "redirectUri", names...); !ok || v != 1 {
		t.Errorf("redirectUri: %d %v", v, ok)
	}
	// Names without a case twin still fall back.
	if v, ok := LookupKey(obj, "other", names...); !ok || v != 2 {
		t.Errorf("other: %d %v", v, ok)
	}
	// Without the sibling list the fallback would take it.
	if v, ok := LookupKey(obj, "redirectURI"); !ok || v != 1 {
		t.Errorf("no siblings: %d %v", v, ok)
	}
}
