package brokers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSODRequestURLAndErrorCodes(t *testing.T) {
	c := &sodClient{config: sodConfigFromEnv(func(string) string { return "" })}
	raw, err := c.requestURL(" tok ", "qid", "20240511")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if u.Host != "ndcdyn.interactivebrokers.com" || u.Path != "/Reporting/IBRITService" ||
		q.Get("t") != "tok" || q.Get("q") != "qid" || q.Get("rd") != "20240511" || q.Get("s") != "norviq -ws" {
		t.Fatalf("url = %s", raw)
	}
	if !strings.Contains(raw, "s=norviq%20-ws") {
		t.Fatalf("service code not %%20-encoded: %s", raw)
	}
	if _, err := c.requestURL("t", "q", "2024-05-11"); err == nil || err.(*sodError).reason() != "IBKR statement date format is invalid." {
		t.Fatalf("bad date: %v", err)
	}
	if _, err := c.requestURL("", "q", "20240511"); err == nil || err.(*sodError).httpError().Status != http.StatusBadRequest {
		t.Fatalf("missing creds: %v", err)
	}

	for body, want := range map[string]int{
		"1052": sodInvalidTokenOrQuery, "Error 1019 Statement generation in progress": sodGenerationInProgress,
		"1010": sodNoStatement, "1018": sodRateLimited,
	} {
		if got, ok := parseSODErrorCode(body); !ok || got != want {
			t.Errorf("%q -> %d %v", body, got, ok)
		}
	}
	if _, ok := parseSODErrorCode(strings.Repeat("DATA,AAPL,10,1052\n", 40)); ok {
		t.Error("large CSV false positive")
	}
	if _, err := decodeSODPayload([]byte("PK\x03\x04"), "x"); err == nil || !strings.Contains(err.Error(), "ZIP archive") {
		t.Fatalf("zip: %v", err)
	}
}

func TestSODReportDateSkipsWeekends(t *testing.T) {
	monday := time.Date(2024, 5, 13, 0, 30, 0, 0, newYork)
	if got := sodReportDate(monday); got != "20240510" {
		t.Fatalf("got %s", got)
	}
	// 02:00 UTC Tuesday is still Monday evening in New York.
	if got := sodReportDate(time.Date(2024, 5, 14, 2, 0, 0, 0, time.UTC)); got != "20240510" {
		t.Fatalf("got %s", got)
	}
}

func TestSODFetchRetriesGenerationInProgress(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte("1019"))
			return
		}
		_, _ = w.Write([]byte("Open Positions,Header,Symbol\nOpen Positions,Data,AAPL\n"))
	}))
	defer srv.Close()
	clock := time.Unix(0, 0)
	c := &sodClient{
		config: sodConfig{baseURL: srv.URL, serviceCode: "x"}, http: srv.Client(),
		now:   func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) error { clock = clock.Add(d); return nil },
		log:   func(string) {},
	}
	files, err := c.fetch(bg, "t", "q", "20240510")
	if err != nil || len(files) != 1 || !strings.Contains(files[0].csvText, "AAPL") || calls != 2 {
		t.Fatalf("files=%v err=%v calls=%d", files, err, calls)
	}
}

func TestHTParsers(t *testing.T) {
	sections, err := parseHTCSV(readFixture(t, "flex-style-sample.csv"))
	if err != nil || len(sections) != 2 || sections[0].name != "Open Positions" || len(sections[0].rows) != 2 ||
		sections[0].rows[0]["symbol"] != "AAPL" || sections[1].name != "Trades" || len(sections[1].rows) != 2 {
		t.Fatalf("flex = %+v %v", sections, err)
	}
	sections, err = parseHTCSV(readFixture(t, "ht-discriminator-sample.csv"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sections {
		if s.name == "Open Positions" && len(s.rows) == 2 && s.rows[0]["symbol"] == "ASTS" {
			found = true
		}
	}
	if !found {
		t.Fatalf("discriminated = %+v", sections)
	}
	st, err := parseSODStatement([]sodFile{{name: "f.csv", csvText: readFixture(t, "flex-style-sample.csv")}})
	if err != nil || st.positions != 2 || st.activities != 2 {
		t.Fatalf("statement = %+v %v", st, err)
	}
	if _, err := parseHTCSV("\n \n"); err == nil {
		t.Fatal("empty accepted")
	}
}

func TestConnectModeAndScopeGuard(t *testing.T) {
	cases := []struct {
		raw      string
		hasOAuth bool
		want     string
	}{
		{"", true, connectOAuth2},
		{"", false, connectGateway},
		{" AUTO ", true, connectOAuth2},
		{"client-portal", true, connectGateway},
		{"web_api", false, connectOAuth2},
	}
	for _, c := range cases {
		if got, err := connectModeFromEnv(c.raw, c.hasOAuth); err != nil || got != c.want {
			t.Errorf("%q/%v -> %q %v", c.raw, c.hasOAuth, got, err)
		}
	}
	if _, err := connectModeFromEnv("socket", false); err == nil {
		t.Error("unknown mode accepted")
	}
	for _, scope := range []string{"read,trading", "portfolio+orders", "account write"} {
		s := scope
		if assertReadOnlyScope(&s) == nil {
			t.Errorf("%q allowed", scope)
		}
	}
	ok := "read portfolio,accounts"
	if err := assertReadOnlyScope(&ok); err != nil {
		t.Errorf("read-only scope rejected: %v", err)
	}
	env := map[string]string{"IBKR_OAUTH_CLIENT_ID": "c", "IBKR_OAUTH_AUTHORIZATION_URL": "https://a", "IBKR_OAUTH_TOKEN_URL": "https://t", "IBKR_OAUTH_API_BASE_URL": "https://b", "IBKR_OAUTH_SCOPE": "transfer"}
	if _, err := oauthConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Error("write scope passed config")
	}
	delete(env, "IBKR_OAUTH_TOKEN_URL")
	if cfg, err := oauthConfigFromEnv(func(k string) string { return env[k] }); cfg != nil || err != nil {
		t.Error("partial config enabled OAuth")
	}
}

// Vapor rewrote access_token to accessToken before looking it up, so no
// token response ever decoded. v2 reads the snake_case keys as sent.
func TestTokenResponseDecodesSnakeCase(t *testing.T) {
	tok, ok := decodeTokenResponse("application/json", []byte(`{"access_token":"a","refresh_token":"r","expires_in":60}`))
	if !ok || tok.accessToken != "a" || tok.refreshToken == nil || *tok.refreshToken != "r" || tok.expiresIn == nil || *tok.expiresIn != 60 {
		t.Fatalf("tok=%+v ok=%v", tok, ok)
	}
	for _, body := range []string{`{"accessToken":"a"}`, `{"access_token":1}`, `{"access_token":"a","expires_in":"x"}`} {
		if _, ok := decodeTokenResponse("application/json", []byte(body)); ok {
			t.Errorf("%s decoded", body)
		}
	}
	if _, ok := decodeTokenResponse("text/plain", []byte(`{"access_token":"a"}`)); ok {
		t.Error("non-JSON content type decoded")
	}
}

func TestClientAssertionIsRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	kid := "k1"
	o := &oauthClient{config: oauthConfig{clientID: "cid", tokenURL: "https://t/token", privateKeyPEM: &pemText, keyID: &kid}, now: time.Now, logger: slog.New(slog.DiscardHandler)}
	signed, err := o.clientAssertion()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Parse(signed, func(*jwt.Token) (any, error) { return &key.PublicKey, nil }, jwt.WithValidMethods([]string{"RS256"}), jwt.WithAudience("https://t/token"))
	if err != nil || tok.Header["kid"] != "k1" || tok.Header["typ"] != "JWT" {
		t.Fatalf("assertion: %v %v", err, tok)
	}
	claims := tok.Claims.(jwt.MapClaims)
	if claims["iss"] != "cid" || claims["sub"] != "cid" {
		t.Fatalf("claims = %v", claims)
	}
	bad := "not a key"
	o.config.privateKeyPEM = &bad
	if _, err := o.clientAssertion(); err == nil || asHTTP(err).Reason != "Invalid IBKR OAuth private key format." {
		t.Fatalf("bad key: %v", err)
	}
	o.config.privateKeyPEM = nil
	if _, err := o.clientAssertion(); err == nil || asHTTP(err).Status != http.StatusServiceUnavailable {
		t.Fatalf("no key: %v", err)
	}
}

func TestScreenshotMapping(t *testing.T) {
	var e extractedScreenshot
	if !e.decode([]byte(`{"kind":"HOLDINGS","rows":[{"symbol":" aapl ","shares":3,"buyPrice":190,"buyDate":"2024-01-01","confidence":0.9},{"symbol":"  "}]}`)) {
		t.Fatal("decode failed")
	}
	x := e.toExtraction()
	if x.Kind != kindHoldings || len(x.Rows) != 1 || x.Rows[0].Symbol != "AAPL" || x.Rows[0].BuyPrice != nil || x.Rows[0].BuyDate != nil {
		t.Fatalf("holdings kept a price: %+v", x)
	}
	e = extractedScreenshot{}
	e.decode([]byte(`{"kind":"trades","rows":[{"symbol":"msft","buyPrice":10}]}`))
	if x := e.toExtraction(); x.Rows[0].BuyPrice == nil || *x.Rows[0].BuyPrice != 10 {
		t.Fatalf("trade lost its price: %+v", x)
	}
	e = extractedScreenshot{}
	e.decode([]byte(`{"kind":"chart","rows":[]}`))
	if x := e.toExtraction(); x.Rejection == nil || !strings.HasPrefix(*x.Rejection, "This doesn't look like") {
		t.Fatalf("unknown = %+v", x)
	}
	if (&extractedScreenshot{}).decode([]byte(`{"kind":"trades","rows":[{"shares":"3"}]}`)) {
		t.Fatal("string shares decoded")
	}
}

func TestQueryEncodingAndRedirects(t *testing.T) {
	got := encodeQueryItems([][2]string{{"error", "a b&c=d/e?f+g"}})
	if got != "error=a%20b%26c%3Dd/e?f+g" {
		t.Fatalf("got %s", got)
	}
	if got := brokerAppRedirectURL("norviq://brokers?x=1", "error", "Bad thing"); got != "norviq://brokers?x=1&broker=ibkr&status=error&error=Bad%20thing" {
		t.Fatalf("redirect = %s", got)
	}
	if got := formEscape("a+b=c&d e"); got != "a%2Bb%3Dc%26d%20e" {
		t.Fatalf("form = %s", got)
	}
	if s := randomURLSafeString(32); len(s) != 32 || strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~") != "" {
		t.Fatalf("state = %q", s)
	}
}

func TestOpeningLotMath(t *testing.T) {
	if q := requiredOpeningQuantity(10, 15, 6); q != 1 {
		t.Fatalf("required = %v", q)
	}
	if q := requiredOpeningQuantity(9, 15, 6); q != 0 {
		t.Fatalf("required = %v", q)
	}
	if _, ok := remainingOpeningQuantity(2, 3); ok {
		t.Fatal("over-consumed accepted")
	}
	if r, ok := remainingOpeningQuantity(5, 2); !ok || r != 3 {
		t.Fatalf("remaining = %v", r)
	}
	if next := nextDailyRun(time.Date(2026, 1, 2, 7, 0, 0, 0, time.UTC)); !next.Equal(time.Date(2026, 1, 3, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("next = %v", next)
	}
	if next := nextDailyRun(time.Date(2026, 1, 2, 5, 0, 0, 0, time.UTC)); !next.Equal(time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("next = %v", next)
	}
}
