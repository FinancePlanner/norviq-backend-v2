package apijson

import (
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
