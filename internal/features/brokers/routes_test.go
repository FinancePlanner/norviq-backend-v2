package brokers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"testing"

	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type apiError struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func expectError(t *testing.T, r *testutil.Response, status int, reason string) {
	t.Helper()
	var e apiError
	if r.Status != status || json.Unmarshal(r.Body, &e) != nil || e.Reason != reason {
		t.Fatalf("got %d %s, want %d %q", r.Status, r.Body, status, reason)
	}
}

// raw sends body with an explicit content type and optional headers.
func (h *harness) raw(t *testing.T, method, path, token, contentType string, body []byte, headers map[string]string) *testutil.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(bg, method, h.app.Server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return &testutil.Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

type formPart struct {
	name, filename, contentType string
	body                        []byte
}

func multipartBody(t *testing.T, parts ...formPart) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		hdr := textproto.MIMEHeader{}
		disp := `form-data; name="` + p.name + `"`
		if p.filename != "" {
			disp += `; filename="` + p.filename + `"`
		}
		hdr.Set("Content-Disposition", disp)
		if p.contentType != "" {
			hdr.Set("Content-Type", p.contentType)
		}
		pw, err := w.CreatePart(hdr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = pw.Write(p.body)
	}
	_ = w.Close()
	return w.FormDataContentType(), buf.Bytes()
}

func TestListGetAndProviderValidation(t *testing.T) {
	h := newHarness(t, nil)
	_, tok := h.user(t)
	r := h.app.Do(http.MethodGet, "/v1/brokers", tok, nil)
	if r.Status != http.StatusOK || strings.TrimSpace(string(r.Body)) != "[]" {
		t.Fatalf("list = %d %s", r.Status, r.Body)
	}
	expectError(t, h.app.Do(http.MethodGet, "/v1/brokers/IBKR", tok, nil), http.StatusNotFound, "Broker not found.")
	// GET /v1/brokers/ibkr never reaches :provider in Vapor (the GET trie's
	// "ibkr" node shadows it): route-not-found, even unsigned.
	expectError(t, h.app.Do(http.MethodGet, "/v1/brokers/ibkr", tok, nil), http.StatusNotFound, "Not Found")
	expectError(t, h.app.Do(http.MethodGet, "/v1/brokers/ibkr", "", nil), http.StatusNotFound, "Not Found")
	expectError(t, h.app.Do(http.MethodGet, "/v1/brokers/import", tok, nil), http.StatusNotFound, "Broker not found.")
	expectError(t, h.app.Do(http.MethodGet, "/v1/brokers/Pilot", tok, nil), http.StatusBadRequest, `"pilot" is reserved.`)
	expectError(t, h.app.Do(http.MethodGet, "/v1/brokers/%20-%20", tok, nil), http.StatusBadRequest, "Invalid broker provider.")
	expectError(t, h.app.Do(http.MethodGet, "/v1/brokers/ibkr/sync/status", tok, nil), http.StatusNotFound, "IBKR connection not found")
	if r := h.app.Do(http.MethodGet, "/v1/brokers", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", r.Status)
	}
}

func TestNormalizeProvider(t *testing.T) {
	for in, want := range map[string]string{"Interactive Brokers": "interactive-brokers", "a__b": "a__b", "x!!y": "x-y", "--ok--": "ok"} {
		if got, err := NormalizeProvider(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v", in, got, err)
		}
	}
	if _, err := NormalizeProvider(strings.Repeat("a", 65)); err == nil {
		t.Error("65 characters accepted")
	}
}

func TestConnectCredentialsAndModeConflict(t *testing.T) {
	h := newHarness(t, map[string]string{"OAUTH_ALLOWED_REDIRECT_URIS": "norviq://brokers"})
	userID, tok := h.user(t)
	expectError(t, h.app.Do(http.MethodPost, "/v1/brokers/ibkr/connect/credentials", tok, map[string]string{"token": " ", "queryId": "q"}),
		http.StatusBadRequest, "IBKR Web Service token is required.")
	expectError(t, h.app.Do(http.MethodPost, "/v1/brokers/ibkr/connect/credentials", tok, map[string]string{"token": "t", "queryId": ""}),
		http.StatusBadRequest, "IBKR Web Service query ID is required.")

	r := h.app.Do(http.MethodPost, "/v1/brokers/ibkr/connect/credentials", tok, map[string]string{"token": " tok ", "queryId": "qid"})
	var conn ConnectionResponse
	r.JSON(t, &conn)
	if r.Status != http.StatusOK || conn.Status != "connected" || conn.DisplayName == nil || *conn.DisplayName != "IBKR Web Service" || conn.PortfolioListID == nil {
		t.Fatalf("credentials = %d %s", r.Status, r.Body)
	}
	var access, external string
	if err := h.app.Deps.DB.QueryRow(bg, `SELECT access_token, external_id FROM broker_connections WHERE user_id = $1`, userID).Scan(&access, &external); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(access, "nvq-enc:") || external != sodWebServiceMarker {
		t.Fatalf("stored access=%q external=%q", access, external)
	}

	// A gateway callback now conflicts with the Web Service connection.
	start := h.startConnect(t, tok)
	r = h.raw(t, http.MethodGet, start, "", "", nil, nil)
	loc := r.Header.Get("Location")
	if r.Status != http.StatusSeeOther || !strings.HasPrefix(loc, "norviq://brokers?broker=ibkr&status=error&error=Disconnect%20the%20existing%20IBKR%20Web%20Service%20connection") {
		t.Fatalf("conflict redirect = %d %q", r.Status, loc)
	}
}

// startConnect runs connect/start and returns the callback path to visit.
func (h *harness) startConnect(t *testing.T, tok string) string {
	t.Helper()
	r := h.app.Do(http.MethodPost, "/v1/brokers/ibkr/connect/start", tok, map[string]string{"redirectURI": " norviq://brokers "})
	var start ConnectStartResponse
	r.JSON(t, &start)
	if r.Status != http.StatusOK || start.ExpiresIn != 600 {
		t.Fatalf("start = %d %s", r.Status, r.Body)
	}
	u, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.RequestURI()
}

func TestStartConnectValidation(t *testing.T) {
	h := newHarness(t, nil)
	_, tok := h.user(t)
	path := "/v1/brokers/ibkr/connect/start"
	expectError(t, h.app.Do(http.MethodPost, path, tok, map[string]string{"redirectURI": "not a url"}), http.StatusBadRequest, "Invalid broker redirect URI.")
	expectError(t, h.app.Do(http.MethodPost, path, tok, map[string]string{"redirectURI": "norviq://x"}), http.StatusServiceUnavailable, "OAuth redirect allowlist is not configured.")
	h.env["OAUTH_ALLOWED_REDIRECT_URIS"] = "norviq://brokers, https://norviq.org/brokers"
	expectError(t, h.app.Do(http.MethodPost, path, tok, map[string]string{"redirectURI": "norviq://x"}), http.StatusBadRequest, "Broker redirect URI is not allowed.")

	r := h.raw(t, http.MethodPost, path, tok, "application/json", []byte(`{"redirectURI":"https://norviq.org/brokers"}`), map[string]string{"X-Forwarded-Proto": "http"})
	var start ConnectStartResponse
	r.JSON(t, &start)
	u, _ := url.Parse(start.AuthorizationURL)
	if u.Scheme != "http" || u.Path != "/v1/auth/brokers/ibkr/callback" || u.Query().Get("flowId") != start.FlowID || len(u.Query().Get("state")) != 32 {
		t.Fatalf("authorizationURL = %q", start.AuthorizationURL)
	}
	if start.FlowID != strings.ToUpper(start.FlowID) {
		t.Fatalf("flowId not uppercase: %s", start.FlowID)
	}
}

func TestGatewayCallbackConnectsAndSyncs(t *testing.T) {
	h := newHarness(t, map[string]string{"OAUTH_ALLOWED_REDIRECT_URIS": "norviq://brokers"})
	userID, tok := h.user(t)
	symbol := strings.ToUpper("CBK" + uuid.NewString()[:4])
	h.instrument(t, symbol)
	h.gateway.positions = []stubPosition{{ticker: symbol, quantity: 4, avgCost: 25}}

	callback := h.startConnect(t, tok)
	r := h.raw(t, http.MethodGet, callback, "", "", nil, nil)
	if r.Status != http.StatusSeeOther || r.Header.Get("Location") != "norviq://brokers?broker=ibkr&status=success" {
		t.Fatalf("callback = %d %q %s", r.Status, r.Header.Get("Location"), r.Body)
	}
	r = h.app.Do(http.MethodGet, "/v1/brokers/ibkr", tok, nil)
	var conn ConnectionResponse
	r.JSON(t, &conn)
	if conn.Status != "connected" || conn.LastSyncedAt == nil || conn.DisplayName == nil || *conn.DisplayName != "Ledger IBKR" {
		t.Fatalf("connection = %s", r.Body)
	}
	// The flow is single use.
	expectError(t, h.raw(t, http.MethodGet, callback, "", "", nil, nil), http.StatusUnauthorized, "Broker connect flow is invalid or expired.")

	r = h.app.Do(http.MethodGet, "/v1/brokers/holdings", tok, nil)
	var holdings []HoldingResponse
	r.JSON(t, &holdings)
	if len(holdings) != 1 || holdings[0].Symbol != symbol || holdings[0].Quantity != 4 || holdings[0].Currency != "USD" {
		t.Fatalf("holdings = %s", r.Body)
	}
	r = h.app.Do(http.MethodGet, "/v1/brokers/ibkr/sync/status", tok, nil)
	var status SyncStatusResponse
	r.JSON(t, &status)
	if status.Status != "connected" || status.IsStale {
		t.Fatalf("status = %s", r.Body)
	}

	r = h.app.Do(http.MethodPost, "/v1/brokers/ibkr/sync", tok, nil)
	var sync SyncResponse
	r.JSON(t, &sync)
	if r.Status != http.StatusOK || sync.Updated != 1 || sync.RunID != conn.ID {
		t.Fatalf("sync = %d %s", r.Status, r.Body)
	}

	r = h.app.Do(http.MethodDelete, "/v1/brokers/ibkr/connection", tok, nil)
	r.JSON(t, &conn)
	if conn.Status != "disconnected" || conn.StatusDetail != nil {
		t.Fatalf("disconnect = %s", r.Body)
	}
	if n := count(t, h.app.Deps.DB, `SELECT count(*) FROM stocks WHERE user_id = $1`, userID); n != 0 {
		t.Fatalf("ibkr stocks kept: %d", n)
	}
}

func TestCallbackParameterValidation(t *testing.T) {
	h := newHarness(t, nil)
	cb := "/v1/auth/brokers/ibkr/callback"
	expectError(t, h.raw(t, http.MethodGet, cb, "", "", nil, nil), http.StatusBadRequest, "Invalid broker flow id.")
	expectError(t, h.raw(t, http.MethodGet, cb+"?flowId=nope&state=x", "", "", nil, nil), http.StatusBadRequest, "Invalid broker flow id.")
	expectError(t, h.raw(t, http.MethodGet, cb+"?code=%20&state=x", "", "", nil, nil), http.StatusBadRequest, "Invalid broker flow id.")
	expectError(t, h.raw(t, http.MethodGet, cb+"?code=c&state=%20", "", "", nil, nil), http.StatusBadRequest, "Missing broker flow state.")
	expectError(t, h.raw(t, http.MethodGet, cb+"?code=c&state=nothing", "", "", nil, nil), http.StatusUnauthorized, "Broker connect flow is invalid or expired.")
}

// throwawayKeyPEM is a fresh RSA key for one test, never a real credential.
func throwawayKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})), "\n", `\n`)
}

func TestOAuthCallbackRejectsEveryTokenResponse(t *testing.T) {
	var form url.Values
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer token.Close()
	h := newHarness(t, map[string]string{
		"OAUTH_ALLOWED_REDIRECT_URIS":  "norviq://brokers",
		"IBKR_OAUTH_CLIENT_ID":         "client-1",
		"IBKR_OAUTH_AUTHORIZATION_URL": "https://ibkr.test/authorize?old=1",
		"IBKR_OAUTH_TOKEN_URL":         token.URL + "/token",
		"IBKR_OAUTH_API_BASE_URL":      "http://unused.test",
		"IBKR_OAUTH_SCOPE":             "read portfolio",
		"IBKR_OAUTH_KEY_ID":            "kid-1",
		"IBKR_OAUTH_PRIVATE_KEY_PEM":   throwawayKeyPEM(t),
	})
	userID, tok := h.user(t)
	r := h.app.Do(http.MethodPost, "/v1/brokers/ibkr/connect/start", tok, map[string]string{"redirectURI": "norviq://brokers"})
	var start ConnectStartResponse
	r.JSON(t, &start)
	u, _ := url.Parse(start.AuthorizationURL)
	q := u.Query()
	if u.Host != "ibkr.test" || q.Get("old") != "" || q.Get("client_id") != "client-1" || q.Get("response_type") != "code" ||
		q.Get("scope") != "read portfolio" || !strings.Contains(u.RawQuery, "scope=read%20portfolio") {
		t.Fatalf("authorizationURL = %q", start.AuthorizationURL)
	}

	cb := "/v1/auth/brokers/ibkr/callback?code=abc&state=" + url.QueryEscape(q.Get("state"))
	r = h.raw(t, http.MethodGet, cb, "", "", nil, nil)
	want := "norviq://brokers?broker=ibkr&status=error&error=IBKR%20token%20response%20format%20was%20not%20recognized."
	if r.Status != http.StatusSeeOther || r.Header.Get("Location") != want {
		t.Fatalf("callback = %d %q", r.Status, r.Header.Get("Location"))
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "abc" || form.Get("client_assertion") == "" {
		t.Fatalf("token request = %v", form)
	}
	var status, detail string
	if err := h.app.Deps.DB.QueryRow(bg, `SELECT status, status_detail FROM broker_connections WHERE user_id = $1`, userID).Scan(&status, &detail); err != nil {
		t.Fatal(err)
	}
	if status != "error" || detail != "IBKR token response format was not recognized." {
		t.Fatalf("connection = %s %q", status, detail)
	}
}

func TestSODSync(t *testing.T) {
	var reply string
	var gotQuery url.Values
	sod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(reply))
	}))
	defer sod.Close()
	h := newHarness(t, map[string]string{"IBKR_SOD_BASE_URL": sod.URL + "/Reporting/IBRITService"})
	userID, tok := h.user(t)
	h.app.Do(http.MethodPost, "/v1/brokers/ibkr/connect/credentials", tok, map[string]string{"token": "tok", "queryId": "qid"})

	reply = readFixture(t, "flex-style-sample.csv")
	r := h.app.Do(http.MethodPost, "/v1/brokers/ibkr/sync", tok, nil)
	var sync SyncResponse
	r.JSON(t, &sync)
	if r.Status != http.StatusOK || sync.Status != "completed" || gotQuery.Get("t") != "tok" || gotQuery.Get("s") != "norviq -ws" {
		t.Fatalf("sync = %d %s (query %v)", r.Status, r.Body, gotQuery)
	}
	rd := gotQuery.Get("rd")
	var detail string
	_ = h.app.Deps.DB.QueryRow(bg, `SELECT status_detail FROM broker_connections WHERE user_id = $1`, userID).Scan(&detail)
	want := "SOD " + rd + ": fetched OK — statement-" + rd + ".csv. 2 provisional positions, 2 activities (ingest disabled until fixtures validated)."
	if detail != want {
		t.Fatalf("detail = %q, want %q", detail, want)
	}

	reply = "1052"
	expectError(t, h.app.Do(http.MethodPost, "/v1/brokers/ibkr/sync", tok, nil), http.StatusUnauthorized, "IBKR token or query ID is invalid.")
	var status string
	_ = h.app.Deps.DB.QueryRow(bg, `SELECT status, status_detail FROM broker_connections WHERE user_id = $1`, userID).Scan(&status, &detail)
	if status != "error" || detail != "IBKR token or query ID is invalid." {
		t.Fatalf("after error: %s %q", status, detail)
	}
}

func TestCSVPreviewAndCommit(t *testing.T) {
	h := newHarness(t, nil)
	userID, tok := h.user(t)
	csv := []byte("Ticker;Qty;Average Cost;Purchase Date;Notes\nzzqa;10;1,000.5;01/02/2024;first\nZZQA;5;2;2024-03-04;\n;1;1;;\nzzqb;-1;1;;\nzzqc;1;1;not a date;\n")
	expectError(t, h.raw(t, http.MethodPost, "/v1/brokers/import/csv", tok, "text/csv", csv, nil), http.StatusBadRequest,
		"Missing broker provider. Use ?provider=... or multipart field provider.")
	r := h.raw(t, http.MethodPost, "/v1/brokers/import/csv?broker=My%20Broker", tok, "text/csv", csv, nil)
	var preview PreviewResponse
	r.JSON(t, &preview)
	if r.Status != http.StatusOK || preview.Provider != "my-broker" || len(preview.Items) != 4 || len(preview.Errors) != 3 {
		t.Fatalf("preview = %d %s", r.Status, r.Body)
	}
	if preview.Errors[0].Message != "Missing symbol." || preview.Errors[1].Message != "Invalid shares (quantity)." ||
		preview.Errors[2].Message != "Invalid buyDate. Expected YYYY-MM-DD." {
		t.Fatalf("errors = %+v", preview.Errors)
	}
	if *preview.Items[0].BuyPrice != 1000.5 || *preview.Items[0].BuyDate != "01/02/2024" || preview.Items[0].ExistingPositionKind != kindNone {
		t.Fatalf("item = %+v", preview.Items[0])
	}

	// Multipart: the provider field wins over the query.
	ct, body := multipartBody(t, formPart{name: "provider", body: []byte("Fidelity")},
		formPart{name: "file", filename: "h.csv", contentType: "text/csv", body: csv})
	r = h.raw(t, http.MethodPost, "/v1/brokers/import/csv/commit?provider=other", tok, ct, body, nil)
	var commit CommitResponse
	r.JSON(t, &commit)
	if r.Status != http.StatusOK || commit.Provider != "fidelity" || len(commit.Inserted) != 1 || commit.ImportedLotsCount != 2 {
		t.Fatalf("commit = %d %s", r.Status, r.Body)
	}
	got := commit.Inserted[0]
	if got.Symbol != "ZZQA" || got.Shares != 15 || got.BuyDate != "2024-01-02" || got.Notes == nil || *got.Notes != "first" {
		t.Fatalf("stock = %+v", got)
	}
	if !near(got.BuyPrice, (10*1000.5+5*2)/15) {
		t.Fatalf("average cost = %v", got.BuyPrice)
	}

	// Re-importing replaces the import's own row.
	r = h.raw(t, http.MethodPost, "/v1/brokers/import/csv/commit", tok, ct, body, nil)
	r.JSON(t, &commit)
	if len(commit.Updated) != 1 || len(commit.ReplacedSymbols) != 1 || commit.ReplacedSymbols[0] != "ZZQA" {
		t.Fatalf("recommit = %s", r.Body)
	}
	// Another broker's import of the same symbol collides until confirmed.
	r = h.raw(t, http.MethodPost, "/v1/brokers/import/csv/commit?provider=schwab", tok, "text/csv", csv, nil)
	expectError(t, r, http.StatusConflict, "These holdings already exist and were not created by this import: ZZQA. Confirm the import to replace them.")
	r = h.raw(t, http.MethodPost, "/v1/brokers/import/csv/commit?provider=schwab&confirmMergeExisting=true", tok, "text/csv", csv, nil)
	r.JSON(t, &commit)
	if r.Status != http.StatusOK || len(commit.Inserted) != 1 {
		t.Fatalf("confirmed = %d %s", r.Status, r.Body)
	}
	if n := count(t, h.app.Deps.DB, `SELECT count(*) FROM stocks WHERE user_id = $1 AND symbol = 'ZZQA'`, userID); n != 1 {
		t.Fatalf("stocks after absorb = %d", n)
	}
	r = h.raw(t, http.MethodPost, "/v1/brokers/import/csv?provider=fidelity", tok, "text/csv", csv, nil)
	r.JSON(t, &preview)
	if preview.Items[0].ExistingPositionKind != kindImported {
		t.Fatalf("kind = %s", preview.Items[0].ExistingPositionKind)
	}
	var csvConnections int
	_ = h.app.Deps.DB.QueryRow(bg, `SELECT count(*) FROM broker_connections WHERE user_id = $1 AND status = 'csv'`, userID).Scan(&csvConnections)
	if csvConnections != 2 {
		t.Fatalf("csv connections = %d", csvConnections)
	}
}

func TestCSVParserEdges(t *testing.T) {
	if _, _, err := parseCSVPreview("  \n "); err == nil || reasonOf(asHTTP(err)) != "CSV body is empty." {
		t.Fatalf("empty: %v", err)
	}
	if _, _, err := parseCSVPreview("sep=,"); err == nil || reasonOf(asHTTP(err)) != "CSV must include a header row." {
		t.Fatalf("sep only: %v", err)
	}
	if _, _, err := parseCSVPreview("name,qty\nA,1"); err == nil || reasonOf(asHTTP(err)) != "CSV header must include a symbol column (symbol/ticker)." {
		t.Fatalf("no symbol: %v", err)
	}
	items, _, err := parseCSVPreview("\uFEFFSymbol\tShares\n\"AA\"\"PL\"\t\"1,5\"\n")
	if err != nil || len(items) != 1 || items[0].Symbol != `AA"PL` || *items[0].Shares != 15 || items[0].Line != 2 {
		t.Fatalf("items = %+v %v", items, err)
	}
	for in, want := range map[string]string{
		"2024-1-5": "2024-01-05", "2024/02/03": "2024-02-03", "3/4/2024": "2024-03-04",
		"20240506": "2024-05-06", "2024-05-06T10:00:00Z": "2024-05-06", "12-31-2023": "2023-12-31",
	} {
		if got, ok := normalizeDateOnlyString(in); !ok || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := normalizeDateOnlyString("2024-02-30"); ok {
		t.Error("2024-02-30 accepted")
	}
}

func TestScreenshotImport(t *testing.T) {
	h := newHarness(t, nil)
	_, tok := h.user(t)
	reason := "No positions could be read from this image."
	h.extract.byBody = map[string]Extraction{
		"png": {Kind: kindTrades, Rows: []ExtractedRow{{Symbol: "ZZSA", Shares: ptr(2.0), BuyPrice: ptr(10.0), BuyDate: str("2024-01-02")}}},
		"jpg": {Kind: kindHoldings, Rejection: &reason},
	}
	path := "/v1/brokers/import/screenshot"
	expectError(t, h.raw(t, http.MethodPost, path, tok, "application/json", []byte("{}"), nil), http.StatusUnsupportedMediaType, "Upload screenshots as multipart/form-data.")
	ct, body := multipartBody(t, formPart{name: "provider", body: []byte("robinhood")})
	expectError(t, h.raw(t, http.MethodPost, path, tok, ct, body, nil), http.StatusBadRequest, "Missing image field in multipart body.")
	ct, body = multipartBody(t, formPart{name: "file", filename: "a.txt", contentType: "text/plain", body: []byte("x")})
	expectError(t, h.raw(t, http.MethodPost, path+"?provider=x", tok, ct, body, nil), http.StatusBadRequest, "Screenshots must be images.")
	img := formPart{name: "file", filename: "a.png", contentType: "image/png", body: []byte("png")}
	ct, body = multipartBody(t, img, img, img, img)
	expectError(t, h.raw(t, http.MethodPost, path+"?provider=x", tok, ct, body, nil), http.StatusBadRequest, "Upload at most 3 screenshots at a time.")

	ct, body = multipartBody(t, formPart{name: "provider", body: []byte("Robinhood")}, img, formPart{name: "files[]", filename: "b.jpg", body: []byte("jpg")})
	r := h.raw(t, http.MethodPost, path, tok, ct, body, nil)
	var preview ScreenshotPreviewResponse
	r.JSON(t, &preview)
	if r.Status != http.StatusOK || preview.Provider != "robinhood" || preview.ImageCount != 2 || preview.Kind != kindTrades ||
		len(preview.Items) != 1 || len(preview.Errors) != 1 || preview.Errors[0].Message != "Image 2: "+reason || preview.Errors[0].Line != 1 {
		t.Fatalf("preview = %d %s", r.Status, r.Body)
	}

	commitPath := path + "/commit"
	expectError(t, h.app.Do(http.MethodPost, commitPath, tok, map[string]any{"provider": "robinhood", "items": []any{}}), http.StatusBadRequest, "No rows to import.")
	many := make([]map[string]any, 301)
	for i := range many {
		many[i] = map[string]any{"line": 0, "symbol": "ZZSA"}
	}
	expectError(t, h.app.Do(http.MethodPost, commitPath, tok, map[string]any{"provider": "robinhood", "items": many}), http.StatusBadRequest, "Too many rows in one import (max 300.")
	r = h.app.Do(http.MethodPost, commitPath, tok, map[string]any{"provider": "Robinhood", "items": []map[string]any{
		{"line": 7, "symbol": "ZZSA", "shares": 2, "buyPrice": 10, "buyDate": "2024-01-02"},
		{"line": 7, "symbol": "ZZSB", "shares": -1},
	}})
	var commit CommitResponse
	r.JSON(t, &commit)
	if r.Status != http.StatusOK || commit.Provider != "robinhood" || len(commit.Inserted) != 1 || len(commit.Errors) != 1 || commit.Errors[0].Line != 1 {
		t.Fatalf("commit = %d %s", r.Status, r.Body)
	}
}

func TestScreenshotDisabled(t *testing.T) {
	h := newHarness(t, nil)
	f := h.feature()
	f.Extractor = DisabledExtractor{}
	app := testutil.NewApp(t, []server.Feature{f})
	id := app.CreateUser("parity-brokers-disabled-" + uuid.NewString()[:8] + "@example.com")
	ct, body := multipartBody(t, formPart{name: "file", filename: "a.png", contentType: "image/png", body: []byte("png")})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, app.Server.URL+"/v1/brokers/import/screenshot?provider=x", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+app.Token(id))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	expectError(t, &testutil.Response{Status: resp.StatusCode, Body: data}, http.StatusServiceUnavailable,
		"Screenshot import is not available. Import a CSV or add positions manually.")
}

func ptr(f float64) *float64 { return &f }
func str(s string) *string   { return &s }

func asHTTP(err error) *httpx.Error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he
	}
	return httpx.Abort(http.StatusInternalServerError, err.Error())
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
