package social

import (
	"encoding/json"
	"strings"
	"testing"

	"norviq-api/internal/apijson"
)

// Two fields that differ only by case (OAuthStartRequest's redirectURI and
// redirectUri) are matched exactly: the case-insensitive fallback must not
// let one key feed both, or move an error to the other name.
func TestDecodeFieldsCaseTwinsStayExact(t *testing.T) {
	twins := []field{{name: "redirectURI", kind: kString}, {name: "redirectUri", kind: kString}}
	decode := func(body string) (map[string]any, error) {
		var obj map[string]json.RawMessage
		if err := apijson.UnmarshalKeyed([]byte(body), &obj); err != nil {
			t.Fatal(err)
		}
		return decodeFields(obj, twins, nil)
	}
	m, err := decode(`{"redirectUri":"https://a.test"}`)
	if err != nil || optStr(m, "redirectURI") != nil || optStr(m, "redirectUri") == nil {
		t.Fatalf("redirectUri: %v %v", m, err)
	}
	m, err = decode(`{"redirect_uri":"https://a.test"}`)
	if err != nil || optStr(m, "redirectURI") == nil || optStr(m, "redirectUri") != nil {
		t.Fatalf("redirect_uri: %v %v", m, err)
	}
	if _, err = decode(`{"redirectUri":5}`); err == nil || !strings.Contains(err.Error(), "'redirectUri'") || strings.Contains(err.Error(), "redirectURI") {
		t.Fatalf("mismatch moved to the twin: %v", err)
	}
}
