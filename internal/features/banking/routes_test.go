package banking

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/bankingdb"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func (tb *testBank) addTx(t *testing.T, account *bankingdb.BankAccount, userID uuid.UUID, ref string, amount float64, day string, pending bool) uuid.UUID {
	t.Helper()
	d, _ := parseDay(day)
	tx := bankingdb.BankTransaction{
		AccountID: account.ID, UserID: userID, ProviderTxID: ref, DedupeHash: ref, Amount: amount,
		Currency: ptr("EUR"), OccurredOn: d, Merchant: ptr("Shop " + ref), Pending: pending, Status: txSuggested,
	}
	if err := tb.bank.base.insertTransaction(context.Background(), &tx); err != nil {
		t.Fatal(err)
	}
	return tx.ID
}

func TestListConnectionsAndTransactions(t *testing.T) {
	tb := newTestBank(t, nil)
	userID := tb.app.CreateUser("bank-list@example.com")
	other := tb.app.CreateUser("bank-list-other@example.com")
	conn, account := tb.makeConnection(t, userID, KindEnableBanking, "session-1", "uid-1")
	tb.makeConnection(t, other, KindEnableBanking, "session-2", "uid-2")
	older := tb.addTx(t, account, userID, "r-old", -4.5, "2026-10-01", false)
	newer := tb.addTx(t, account, userID, "r-new", -12, "2026-10-18", true)
	// A manual expense one day off with the same magnitude flags r-new.
	if _, err := tb.app.Deps.DB.Exec(context.Background(),
		`INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at) VALUES ($1, $2, 'Lunch', 12, 'fun', '2026-10-17', now())`,
		uuid.New(), userID); err != nil {
		t.Fatal(err)
	}
	token := tb.app.Token(userID)

	r := tb.app.Do(http.MethodGet, "/v1/banks/connections", token, nil)
	var conns []map[string]any
	r.JSON(t, &conns)
	if r.Status != http.StatusOK || len(conns) != 1 {
		t.Fatalf("connections %d %s", r.Status, r.Body)
	}
	c := conns[0]
	if c["id"] != strings.ToUpper(conn.ID.String()) || c["provider"] != "enablebanking" || c["status"] != "active" ||
		c["institutionName"] != "Banco Teste" || c["lastSyncedAt"] != nil {
		t.Fatalf("connection %v", c)
	}
	if exp, _ := c["consentExpiresAt"].(string); !strings.HasSuffix(exp, "Z") || strings.Contains(exp, ".") {
		t.Fatalf("consentExpiresAt %q", exp)
	}
	accounts := c["accounts"].([]any)
	if len(accounts) != 1 || accounts[0].(map[string]any)["name"] != "Conta" || accounts[0].(map[string]any)["currency"] != "EUR" {
		t.Fatalf("accounts %v", accounts)
	}

	r = tb.app.Do(http.MethodGet, "/v1/banks/transactions", token, nil)
	var txs []map[string]any
	r.JSON(t, &txs)
	if r.Status != http.StatusOK || len(txs) != 2 {
		t.Fatalf("transactions %d %s", r.Status, r.Body)
	}
	if txs[0]["id"] != strings.ToUpper(newer.String()) || txs[0]["date"] != "2026-10-18" || txs[0]["possibleDuplicate"] != true ||
		txs[0]["pending"] != true || txs[0]["accountId"] != strings.ToUpper(account.ID.String()) {
		t.Fatalf("newer %v", txs[0])
	}
	if txs[1]["id"] != strings.ToUpper(older.String()) || txs[1]["possibleDuplicate"] != false {
		t.Fatalf("older %v", txs[1])
	}
	if _, has := txs[1]["expenseId"]; has {
		t.Fatalf("nil expenseId encoded: %v", txs[1])
	}
	r = tb.app.Do(http.MethodGet, "/v1/banks/transactions?status=dismissed", token, nil)
	if r.Status != http.StatusOK || string(r.Body) != "[]" {
		t.Fatalf("dismissed %d %s", r.Status, r.Body)
	}
}

func TestImportAndDismissTransaction(t *testing.T) {
	tb := newTestBank(t, nil)
	userID := tb.app.CreateUser("bank-import@example.com")
	_, account := tb.makeConnection(t, userID, KindEnableBanking, "session-1", "uid-1")
	txID := tb.addTx(t, account, userID, "r-1", -23.5, "2026-10-18", false)
	other := tb.addTx(t, account, userID, "r-2", -3, "2026-10-18", false)
	token := tb.app.Token(userID)
	path := "/v1/banks/transactions/" + strings.ToUpper(txID.String())

	r := tb.app.Do(http.MethodPost, path+"/import", token, map[string]any{})
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "No such key 'pillar'") {
		t.Fatalf("missing pillar %d %s", r.Status, r.Body)
	}
	r = tb.app.Do(http.MethodPost, path+"/import", token, map[string]any{"pillar": "Fun", "title_override": "  Dinner  "})
	var expense map[string]any
	r.JSON(t, &expense)
	if r.Status != http.StatusOK || expense["title"] != "Dinner" || expense["amount"] != 23.5 || expense["pillar"] != "fun" || expense["occurredOn"] != "2026-10-18" {
		t.Fatalf("import %d %s", r.Status, r.Body)
	}
	var status string
	var expenseID *uuid.UUID
	_ = tb.app.Deps.DB.QueryRow(context.Background(), `SELECT status, expense_id FROM bank_transactions WHERE id = $1`, txID).Scan(&status, &expenseID)
	if status != txImported || expenseID == nil || strings.ToUpper(expenseID.String()) != expense["id"] {
		t.Fatalf("row %s %v", status, expenseID)
	}
	r = tb.app.Do(http.MethodPost, path+"/import", token, map[string]any{"pillar": "fun"})
	if r.Status != http.StatusConflict || !strings.Contains(string(r.Body), "Transaction is not awaiting review.") {
		t.Fatalf("reimport %d %s", r.Status, r.Body)
	}

	r = tb.app.Do(http.MethodPost, "/v1/banks/transactions/"+other.String()+"/dismiss", token, nil)
	if r.Status != http.StatusNoContent {
		t.Fatalf("dismiss %d %s", r.Status, r.Body)
	}
	r = tb.app.Do(http.MethodPost, "/v1/banks/transactions/not-a-uuid/dismiss", token, nil)
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Missing transaction id.") {
		t.Fatalf("bad id %d %s", r.Status, r.Body)
	}
	stranger := tb.app.Token(tb.app.CreateUser("bank-import-stranger@example.com"))
	r = tb.app.Do(http.MethodPost, path+"/dismiss", stranger, nil)
	if r.Status != http.StatusNotFound || !strings.Contains(string(r.Body), "Transaction not found.") {
		t.Fatalf("stranger %d %s", r.Status, r.Body)
	}
	if r := tb.app.Do(http.MethodGet, "/v1/banks/transactions", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous %d", r.Status)
	}
}

func TestConnectionSyncAndDisconnect(t *testing.T) {
	tb := newTestBank(t, nil)
	tb.stub.on("GET /accounts/uid-1/transactions", ok(ebPage(nil, ebTx{ref: "r-1", amount: "5.00", creditor: "Bolt"}.json())))
	tb.stub.on("DELETE /sessions/session-1", stubReply{http.StatusNotFound, `{"error":"SESSION_DOES_NOT_EXIST"}`})
	userID := tb.app.CreateUser("bank-sync@example.com")
	conn, _ := tb.makeConnection(t, userID, KindEnableBanking, "session-1", "uid-1")
	token := tb.app.Token(userID)
	path := "/v1/banks/connections/" + strings.ToUpper(conn.ID.String())

	r := tb.app.Do(http.MethodPost, path+"/sync", token, nil)
	if r.Status != http.StatusOK || string(r.Body) != `{"added":1,"modified":0,"removed":0}` {
		t.Fatalf("sync %d %s", r.Status, r.Body)
	}
	if got := tb.connection(t, conn.ID); deref(got.LastSyncStatus) != "ok" {
		t.Fatalf("last sync %+v", got)
	}
	r = tb.app.Do(http.MethodDelete, "/v1/banks/connections/"+uuid.NewString(), token, nil)
	if r.Status != http.StatusNotFound || !strings.Contains(string(r.Body), "Bank connection not found.") {
		t.Fatalf("missing %d %s", r.Status, r.Body)
	}
	r = tb.app.Do(http.MethodDelete, path, token, nil)
	if r.Status != http.StatusNoContent || len(tb.stub.requests("DELETE /sessions/session-1")) != 1 {
		t.Fatalf("disconnect %d %s", r.Status, r.Body)
	}
	if tb.count(t, "bank_connections") != 0 || tb.count(t, "bank_transactions") != 0 {
		t.Fatal("connection not deleted with its rows")
	}
}

func TestInstitutionsAndLinkSession(t *testing.T) {
	tb := newTestBank(t, nil)
	tb.stub.on("GET /aspsps", ok(`{"aspsps":[{"name":"Banco Teste","country":"PT","logo":"https://eb.example.test/logo.png","bic":"BTESPTPL","beta":true,"maximum_consent_validity":7776000}]}`))
	tb.stub.on("POST /auth", ok(`{"url":"https://tilisy.example/auth/abc","authorization_id":"auth-1"}`))
	token := tb.app.Token(tb.app.CreateUser("bank-link@example.com"))

	r := tb.app.Do(http.MethodGet, "/v1/banks/institutions?country=pt", token, nil)
	if want := `[{"id":"PT:Banco Teste","name":"Banco Teste","bic":"BTESPTPL","logoURL":"https://eb.example.test/logo.png","country":"PT","beta":true}]`; r.Status != http.StatusOK || string(r.Body) != want {
		t.Fatalf("institutions %d %s", r.Status, r.Body)
	}
	if q := tb.stub.requests("GET /aspsps")[0].URL.RawQuery; q != "country=PT&psu_type=personal&service=AIS" {
		t.Fatalf("query %q", q)
	}
	r = tb.app.Do(http.MethodGet, "/v1/banks/institutions?provider=bogus", token, nil)
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Unknown bank provider.") {
		t.Fatalf("bogus %d %s", r.Status, r.Body)
	}
	r = tb.app.Do(http.MethodGet, "/v1/banks/institutions?provider=GoCardless", token, nil)
	if r.Status != http.StatusServiceUnavailable || !strings.Contains(string(r.Body), "Bank provider gocardless is not configured.") {
		t.Fatalf("gocardless %d %s", r.Status, r.Body)
	}

	r = tb.app.Do(http.MethodPost, "/v1/banks/link-session", token, map[string]any{"institution_id": "PT:Banco Teste", "redirect_uri": "https://evil.example"})
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Bank redirect URI is not allowed.") {
		t.Fatalf("evil %d %s", r.Status, r.Body)
	}
	r = tb.app.Do(http.MethodPost, "/v1/banks/link-session", token, map[string]any{"institutionId": "PT:Banco Teste", "redirectURI": appRedirect})
	var session map[string]any
	r.JSON(t, &session)
	if r.Status != http.StatusOK || session["provider"] != "enablebanking" || session["hostedURL"] != "https://tilisy.example/auth/abc" || session["expiration"] == nil {
		t.Fatalf("hosted %d %s", r.Status, r.Body)
	}
	// No body: not hosted, so Plaid, which is not configured here.
	r = tb.app.Do(http.MethodPost, "/v1/banks/link-session", token, nil)
	if r.Status != http.StatusServiceUnavailable || !strings.Contains(string(r.Body), "Bank provider plaid is not configured.") {
		t.Fatalf("plaid %d %s", r.Status, r.Body)
	}
	r = tb.app.Do(http.MethodPost, "/v1/banks/connections", token, map[string]any{})
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "No such key 'publicToken'") {
		t.Fatalf("exchange %d %s", r.Status, r.Body)
	}
}

func TestBankSyncRequiresPremium(t *testing.T) {
	st := newStub(t)
	app := testutil.NewApp(t, []server.Feature{Feature{Settings: &Settings{}, HTTP: st.srv.Client(), Entitlements: &entitlements.Config{}}})
	token := app.Token(app.CreateUser("bank-free@example.com"))
	r := app.Do(http.MethodPost, "/v1/banks/link-session", token, nil)
	if r.Status != http.StatusForbidden {
		t.Fatalf("free user %d %s", r.Status, r.Body)
	}
	// Reads stay open.
	if r := app.Do(http.MethodGet, "/v1/banks/connections", token, nil); r.Status != http.StatusOK || string(r.Body) != "[]" {
		t.Fatalf("connections %d %s", r.Status, r.Body)
	}
}

func TestSyncJobFlagsExpiringConsent(t *testing.T) {
	tb := newTestBank(t, nil)
	tb.stub.on("GET /accounts/uid-2/transactions", stubReply{http.StatusBadGateway, `oops`})
	userID := tb.app.CreateUser("bank-job@example.com")
	expiring, _ := tb.makeConnection(t, userID, KindEnableBanking, "session-1", "uid-1")
	failing, _ := tb.makeConnection(t, userID, KindEnableBanking, "session-2", "uid-2")
	if _, err := tb.app.Deps.DB.Exec(context.Background(), `UPDATE bank_connections SET consent_expires_at = now() + interval '2 days' WHERE id = $1`, expiring.ID); err != nil {
		t.Fatal(err)
	}
	tb.bank.SyncOnce(context.Background())
	if got := tb.connection(t, expiring.ID); got.Status != statusReauthRequired || deref(got.LastSyncStatus) != "consent_expiring" {
		t.Fatalf("expiring %+v", got)
	}
	var lastErr *string
	got := tb.connection(t, failing.ID)
	_ = tb.app.Deps.DB.QueryRow(context.Background(), `SELECT last_sync_error FROM bank_connections WHERE id = $1`, failing.ID).Scan(&lastErr)
	if got.Status != statusActive || deref(got.LastSyncStatus) != "error" || deref(lastErr) != "Enable Banking request failed." {
		t.Fatalf("failing %+v %v", got, deref(lastErr))
	}
}

// MARK: - Plaid webhook

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func signPlaid(t *testing.T, key *ecdsa.PrivateKey, iat int64, body []byte) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": "kid-1", "typ": "JWT"})
	sum := sha256.Sum256(body)
	claims, _ := json.Marshal(map[string]any{"iat": iat, "request_body_sha256": hex.EncodeToString(sum[:])})
	input := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64(sig)
}

func TestPlaidWebhook(t *testing.T) {
	// Not configured: 503 before anything else.
	off := newTestBank(t, nil)
	if r := off.app.Do(http.MethodPost, "/webhooks/plaid", "", []byte(`{}`)); r.Status != http.StatusServiceUnavailable ||
		!strings.Contains(string(r.Body), "Plaid webhook verification is not configured.") {
		t.Fatalf("unconfigured %d %s", r.Status, r.Body)
	}

	tb := newTestBank(t, func(s *Settings) { s.PlaidClientID, s.PlaidSecret = "client", "secret" })
	plaid, _ := tb.bank.Registry.Provider(KindPlaid)
	plaid.(*PlaidProvider).client.config.BaseURL = tb.stub.srv.URL
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x, y := make([]byte, 32), make([]byte, 32)
	key.X.FillBytes(x)
	key.Y.FillBytes(y)
	tb.stub.on("POST /webhook_verification_key/get", ok(fmt.Sprintf(
		`{"key":{"alg":"ES256","kty":"EC","crv":"P-256","kid":"kid-1","x":%q,"y":%q,"use":"sig","created_at":1},"request_id":"r"}`, b64(x), b64(y))))
	userID := tb.app.CreateUser("plaid-hook@example.com")
	conn := &bankingdb.BankConnection{UserID: userID, Provider: "plaid", ProviderItemID: "item-1", Status: statusActive}
	if err := tb.bank.base.insertConnection(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	send := func(body []byte, token string) *testutil.Response {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, tb.app.Server.URL+"/webhooks/plaid", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Plaid-Verification", token)
		}
		resp, err := tb.app.Server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return &testutil.Response{Status: resp.StatusCode, Body: data}
	}
	body := []byte(`{"webhook_type":"ITEM","webhook_code":"ERROR","item_id":"item-1"}`)
	now := time.Now().Unix()

	if r := send(body, ""); r.Status != http.StatusUnauthorized || !strings.Contains(string(r.Body), "Missing Plaid-Verification header.") {
		t.Fatalf("missing %d %s", r.Status, r.Body)
	}
	if r := send(body, "a.b"); r.Status != http.StatusUnauthorized || !strings.Contains(string(r.Body), "Malformed Plaid verification token.") {
		t.Fatalf("malformed %d %s", r.Status, r.Body)
	}
	if r := send(body, signPlaid(t, key, now-3600, body)); r.Status != http.StatusUnauthorized || !strings.Contains(string(r.Body), "expired") {
		t.Fatalf("expired %d %s", r.Status, r.Body)
	}
	if r := send(body, signPlaid(t, key, now, []byte("other"))); r.Status != http.StatusUnauthorized || !strings.Contains(string(r.Body), "body hash mismatch") {
		t.Fatalf("hash %d %s", r.Status, r.Body)
	}
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if r := send(body, signPlaid(t, otherKey, now, body)); r.Status != http.StatusUnauthorized || !strings.Contains(string(r.Body), "Invalid Plaid webhook signature.") {
		t.Fatalf("signature %d %s", r.Status, r.Body)
	}
	if r := send(body, signPlaid(t, key, now, body)); r.Status != http.StatusOK || len(r.Body) != 0 {
		t.Fatalf("valid %d %s", r.Status, r.Body)
	}
	got := tb.connection(t, conn.ID)
	if got.Status != statusReauthRequired || deref(got.LastSyncStatus) != "reauth_required" {
		t.Fatalf("connection %+v", got)
	}
	// Keys are cached by kid.
	if n := len(tb.stub.requests("POST /webhook_verification_key/get")); n != 1 {
		t.Fatalf("key fetched %d times", n)
	}
	unknown := []byte(`{"webhook_type":"ITEM","webhook_code":"ERROR","item_id":"nope"}`)
	if r := send(unknown, signPlaid(t, key, now, unknown)); r.Status != http.StatusOK {
		t.Fatalf("unknown item %d %s", r.Status, r.Body)
	}
	bad := []byte(`{"webhook_type":"ITEM"}`)
	if r := send(bad, signPlaid(t, key, now, bad)); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Malformed Plaid webhook payload.") {
		t.Fatalf("malformed payload %d %s", r.Status, r.Body)
	}
}

func TestAppURL(t *testing.T) {
	for _, c := range []struct {
		base, status string
		msg          *string
		want         string
	}{
		{"norviqa://oauth/bank-callback", "connected", nil, "norviqa://oauth/bank-callback?bank=connected"},
		{"https://www.norviq.org/settings/banks?tab=1", "error", ptr("Bank link is invalid or expired."), "https://www.norviq.org/settings/banks?tab=1&bank=error&error=Bank%20link%20is%20invalid%20or%20expired."},
		{"https://x.example/a#frag", "error", ptr("a&b=c é"), "https://x.example/a?bank=error&error=a%26b%3Dc%20%C3%A9#frag"},
	} {
		if got := appURL(c.base, c.status, c.msg); got != c.want {
			t.Errorf("appURL(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}
