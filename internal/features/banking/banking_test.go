package banking

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/bankingdb"
	"norviq-api/internal/testutil"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// stub is an aggregator stand-in: replies are queued per "METHOD /path";
// the last one repeats. Every request is recorded.
type stub struct {
	mu      sync.Mutex
	replies map[string][]stubReply
	reqs    map[string][]stubRequest
	srv     *httptest.Server
}

type stubReply struct {
	status int
	body   string
}

type stubRequest struct {
	URL    *url.URL
	Header http.Header
	Body   []byte
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{replies: map[string][]stubReply{}, reqs: map[string][]stubRequest{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		s.reqs[key] = append(s.reqs[key], stubRequest{URL: r.URL, Header: r.Header.Clone(), Body: body})
		queue := s.replies[key]
		reply := stubReply{status: http.StatusNotFound, body: `{"error":"NOT_STUBBED"}`}
		if len(queue) > 0 {
			reply = queue[0]
			if len(queue) > 1 {
				s.replies[key] = queue[1:]
			}
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) on(key string, replies ...stubReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[key] = replies
}

func (s *stub) requests(key string) []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[key]
}

func ok(body string) stubReply { return stubReply{status: http.StatusOK, body: body} }

const appRedirect = "norviqa://oauth/bank-callback"

func testPEM(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})), key
}

type testBank struct {
	app  *testutil.App
	bank *Bank
	stub *stub
}

func newTestBank(t *testing.T, mutate func(*Settings)) *testBank {
	t.Helper()
	st := newStub(t)
	pemText, _ := testPEM(t)
	s := Settings{
		EnableBankingAppID: "app-123", EnableBankingPrivateKeyPEM: pemText, EnableBankingAPIBase: st.srv.URL,
		EnableBankingRedirectURL: "https://api.example.test/v1/banks/enablebanking/callback",
		AllowedRedirectURIs:      appRedirect + ", https://www.norviq.org/settings/banks",
	}
	if mutate != nil {
		mutate(&s)
	}
	app := testutil.NewApp(t, []server.Feature{Feature{
		Settings: &s, HTTP: st.srv.Client(), Entitlements: &entitlements.Config{BypassBilling: true},
	}})
	bank, err := Shared(app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	return &testBank{app: app, bank: bank, stub: st}
}

func (tb *testBank) eb(t *testing.T) *EnableBankingProvider {
	t.Helper()
	p, err := tb.bank.Registry.Provider(KindEnableBanking)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*EnableBankingProvider)
}

func (tb *testBank) makeFlow(t *testing.T, userID uuid.UUID, provider Kind, state string) {
	t.Helper()
	now := time.Now()
	if err := tb.bank.base.q.InsertLinkFlow(context.Background(), bankingdb.InsertLinkFlowParams{
		ID: uuid.New(), UserID: userID, Provider: string(provider), Reference: state,
		RequisitionID: "auth-" + state, InstitutionID: ptr("PT:Banco Teste"), AppRedirectUri: appRedirect,
		ExpiresAt: now.Add(10 * time.Minute), Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func (tb *testBank) makeConnection(t *testing.T, userID uuid.UUID, provider Kind, itemID, accountUID string) (*bankingdb.BankConnection, *bankingdb.BankAccount) {
	t.Helper()
	ctx := context.Background()
	conn := &bankingdb.BankConnection{
		UserID: userID, Provider: string(provider), InstitutionID: ptr("PT:Banco Teste"),
		InstitutionName: ptr("Banco Teste"), ProviderItemID: itemID, Status: statusActive,
		ConsentExpiresAt: ptr(time.Now().Add(30 * 24 * time.Hour)),
	}
	if err := tb.bank.base.insertConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	account := &bankingdb.BankAccount{
		ConnectionID: conn.ID, ProviderAccountID: accountUID, Name: "Conta",
		IdentificationHash: ptr("hash-1"), Currency: ptr("EUR"),
	}
	if err := tb.bank.base.insertAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	return conn, account
}

func (tb *testBank) transactions(t *testing.T) []bankingdb.BankTransaction {
	t.Helper()
	rows, err := tb.app.Deps.DB.Query(context.Background(), `SELECT id, provider_tx_id, amount, merchant, pending, provider_category, status FROM bank_transactions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []bankingdb.BankTransaction
	for rows.Next() {
		var r bankingdb.BankTransaction
		if err := rows.Scan(&r.ID, &r.ProviderTxID, &r.Amount, &r.Merchant, &r.Pending, &r.ProviderCategory, &r.Status); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (tb *testBank) connection(t *testing.T, id uuid.UUID) bankingdb.BankConnection {
	t.Helper()
	var c bankingdb.BankConnection
	err := tb.app.Deps.DB.QueryRow(context.Background(),
		`SELECT id, provider_item_id, status, last_sync_status, institution_name FROM bank_connections WHERE id = $1`, id).
		Scan(&c.ID, &c.ProviderItemID, &c.Status, &c.LastSyncStatus, &c.InstitutionName)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (tb *testBank) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := tb.app.Deps.DB.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type redirect struct {
	StatusCode int
	Header     http.Header
}

// getNoFollow issues a GET without following redirects.
func (tb *testBank) getNoFollow(t *testing.T, path string) redirect {
	t.Helper()
	client := *tb.app.Server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, tb.app.Server.URL+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return redirect{StatusCode: resp.StatusCode, Header: resp.Header}
}

func ebPage(key *string, txs ...string) string {
	k := "null"
	if key != nil {
		k = `"` + *key + `"`
	}
	return `{"transactions":[` + strings.Join(txs, ",") + `],"continuation_key":` + k + `}`
}

type ebTx struct {
	ref, amount, indicator, status, date, creditor, debtor, mcc string
}

func (x ebTx) json() string {
	if x.amount == "" {
		x.amount = "1.00"
	}
	if x.indicator == "" {
		x.indicator = "DBIT"
	}
	if x.status == "" {
		x.status = "BOOK"
	}
	if x.date == "" {
		x.date = "2026-10-18"
	}
	party := x.creditor
	if party == "" {
		party = x.debtor
	}
	fields := []string{
		fmt.Sprintf(`"transaction_amount":{"amount":%q,"currency":"EUR"}`, x.amount),
		fmt.Sprintf(`"credit_debit_indicator":%q`, x.indicator),
		fmt.Sprintf(`"status":%q`, x.status),
		fmt.Sprintf(`"remittance_information":["Compra %s"]`, party),
	}
	if x.status == "PDNG" {
		fields = append(fields, fmt.Sprintf(`"transaction_date":%q`, x.date))
	} else {
		fields = append(fields, fmt.Sprintf(`"booking_date":%q`, x.date))
	}
	if x.ref != "" {
		fields = append(fields, fmt.Sprintf(`"entry_reference":%q`, x.ref))
	}
	if x.creditor != "" {
		fields = append(fields, fmt.Sprintf(`"creditor":{"name":%q}`, x.creditor))
	}
	if x.debtor != "" {
		fields = append(fields, fmt.Sprintf(`"debtor":{"name":%q}`, x.debtor))
	}
	if x.mcc != "" {
		fields = append(fields, fmt.Sprintf(`"merchant_category_code":%q`, x.mcc))
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func ebSession(id, uid string) string {
	return fmt.Sprintf(`{"session_id":%q,"accounts":[{"uid":%q,"identification_hash":"hash-1","name":"Conta Ordem","currency":"EUR","cash_account_type":"CACC","account_id":{"iban":"PT50000000000000000001234"}}],"aspsp":{"name":"Banco Teste","country":"PT"},"psu_type":"personal","access":{"valid_until":"2027-04-18T10:00:00.000000+00:00"}}`, id, uid)
}

func decodeSegment(t *testing.T, seg string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// MARK: - Enable Banking

func TestEnableBankingTokenShape(t *testing.T) {
	pemText, key := testPEM(t)
	c := &enableBankingClient{config: EnableBankingConfig{ApplicationID: "app-123", PrivateKeyPEM: pemText}}
	now := time.Now()
	token, err := c.makeToken(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("parts = %d", len(parts))
	}
	header, claims := decodeSegment(t, parts[0]), decodeSegment(t, parts[1])
	if header["alg"] != "RS256" || header["typ"] != "JWT" || header["kid"] != "app-123" {
		t.Fatalf("header %v", header)
	}
	if claims["iss"] != "enablebanking.com" || claims["aud"] != "api.enablebanking.com" {
		t.Fatalf("claims %v", claims)
	}
	iat, exp := claims["iat"].(float64), claims["exp"].(float64)
	if exp <= iat || exp-iat > 86400 {
		t.Fatalf("iat %v exp %v", iat, exp)
	}
	if _, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithAudience("api.enablebanking.com")); err != nil {
		t.Fatalf("verify: %v", err)
	}

	bad := &enableBankingClient{config: EnableBankingConfig{PrivateKeyPEM: "unused"}}
	if _, err := bad.makeToken(now); err == nil || !strings.Contains(err.Error(), "Invalid Enable Banking private key format.") {
		t.Fatalf("bad key err = %v", err)
	}
}

func TestEnableBankingPaginationFollowsContinuationKey(t *testing.T) {
	tb := newTestBank(t, nil)
	k1, k2 := "k1", "k2"
	tb.stub.on("GET /accounts/uid-1/transactions",
		ok(ebPage(&k1, ebTx{ref: "r1"}.json())), ok(ebPage(&k2)), ok(ebPage(nil, ebTx{ref: "r2"}.json())))
	txs, err := tb.eb(t).client.transactions(context.Background(), "uid-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 || *txs[0].EntryReference != "r1" || *txs[1].EntryReference != "r2" {
		t.Fatalf("txs %+v", txs)
	}
	calls := tb.stub.requests("GET /accounts/uid-1/transactions")
	if len(calls) != 3 || !strings.Contains(calls[0].URL.RawQuery, "strategy=longest") ||
		!strings.Contains(calls[1].URL.RawQuery, "continuation_key=k1") || !strings.Contains(calls[2].URL.RawQuery, "continuation_key=k2") {
		t.Fatalf("calls %+v", calls)
	}
	if got := calls[0].Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ey") {
		t.Fatalf("auth header %q", got)
	}
}

func TestEnableBankingHostedLinkCapsConsent(t *testing.T) {
	tb := newTestBank(t, nil)
	tb.stub.on("GET /aspsps", ok(`{"aspsps":[{"name":"Banco Teste","country":"PT","logo":"https://eb.example.test/logo.png","bic":"BTESPTPL","beta":true,"maximum_consent_validity":7776000}]}`))
	tb.stub.on("POST /auth", ok(`{"url":"https://tilisy.example/auth/abc","authorization_id":"auth-1"}`))
	userID := tb.app.CreateUser("eb-link@example.com")
	session, err := tb.eb(t).CreateHostedLink(context.Background(), Call{}, userID, "PT:Banco Teste", appRedirect)
	if err != nil {
		t.Fatal(err)
	}
	if session.Provider != KindEnableBanking || *session.HostedURL != "https://tilisy.example/auth/abc" {
		t.Fatalf("session %+v", session)
	}
	auths := tb.stub.requests("POST /auth")
	if len(auths) != 1 {
		t.Fatalf("auth calls %d", len(auths))
	}
	var body map[string]any
	_ = json.Unmarshal(auths[0].Body, &body)
	if body["redirect_url"] != "https://api.example.test/v1/banks/enablebanking/callback" || body["psu_type"] != "personal" {
		t.Fatalf("body %v", body)
	}
	if body["aspsp"].(map[string]any)["country"] != "PT" {
		t.Fatalf("aspsp %v", body["aspsp"])
	}
	validUntil, okT := parseEBTimestamp(body["access"].(map[string]any)["valid_until"].(string))
	if !okT || math.Abs(time.Until(validUntil).Seconds()-90*86400) > 120 {
		t.Fatalf("valid_until %v", body["access"])
	}
	flow, err := tb.bank.base.q.GetLinkFlow(context.Background(), bankingdb.GetLinkFlowParams{Reference: body["state"].(string), Provider: "enablebanking"})
	if err != nil || flow.RequisitionID != "auth-1" {
		t.Fatalf("flow %+v %v", flow, err)
	}
	if !strings.HasPrefix(flow.Reference, "nvq-") || len(flow.Reference) != 36 {
		t.Fatalf("reference %q", flow.Reference)
	}
}

func TestEnableBankingCallbackCreatesConnectionAndIsReplaySafe(t *testing.T) {
	tb := newTestBank(t, nil)
	tb.stub.on("POST /sessions", ok(ebSession("session-1", "uid-1")))
	tb.stub.on("GET /accounts/uid-1/transactions", ok(ebPage(nil,
		ebTx{ref: "r-coffee", amount: "4.50", creditor: "Cafe Lisboa", mcc: "5814"}.json(),
		ebTx{ref: "r-salary", amount: "2000.00", indicator: "CRDT", debtor: "ACME Lda"}.json(),
		ebTx{amount: "12.00", status: "PDNG", creditor: "Uber"}.json(),
		ebTx{ref: "r-canceled", amount: "9.99", status: "CNCL", creditor: "Shop"}.json(),
	)))
	userID := tb.app.CreateUser("eb-callback@example.com")
	tb.makeFlow(t, userID, KindEnableBanking, "state-1")
	for range 2 {
		resp := tb.getNoFollow(t, "/v1/banks/enablebanking/callback?state=state-1&code=code-1")
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != appRedirect+"?bank=connected" {
			t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if n := len(tb.stub.requests("POST /sessions")); n != 1 {
		t.Fatalf("sessions calls %d", n)
	}
	if tb.count(t, "bank_connections") != 1 {
		t.Fatal("want one connection")
	}
	var account bankingdb.BankAccount
	_ = tb.app.Deps.DB.QueryRow(context.Background(), `SELECT provider_account_id, identification_hash, mask FROM bank_accounts`).
		Scan(&account.ProviderAccountID, &account.IdentificationHash, &account.Mask)
	if account.ProviderAccountID != "uid-1" || *account.IdentificationHash != "hash-1" || *account.Mask != "1234" {
		t.Fatalf("account %+v", account)
	}
	rows := tb.transactions(t)
	if len(rows) != 3 {
		t.Fatalf("rows %d", len(rows))
	}
	byRef := map[string]bankingdb.BankTransaction{}
	pendingUber := false
	for i := range rows {
		byRef[rows[i].ProviderTxID] = rows[i]
		if rows[i].Pending && deref(rows[i].Merchant) == "Uber" {
			pendingUber = true
		}
	}
	if c := byRef["r-coffee"]; c.Amount != -4.5 || *c.Merchant != "Cafe Lisboa" || *c.ProviderCategory != "5814" {
		t.Fatalf("coffee %+v", c)
	}
	if s := byRef["r-salary"]; s.Amount != 2000 || *s.Merchant != "ACME Lda" {
		t.Fatalf("salary %+v", s)
	}
	if !pendingUber {
		t.Fatal("pending Uber row missing")
	}
}

func TestEnableBankingCallbackForwardsBankError(t *testing.T) {
	tb := newTestBank(t, nil)
	userID := tb.app.CreateUser("eb-cancel@example.com")
	tb.makeFlow(t, userID, KindEnableBanking, "state-x")
	resp := tb.getNoFollow(t, "/v1/banks/enablebanking/callback?state=state-x&error=access_denied")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != appRedirect+"?bank=error&error=access_denied" {
		t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if len(tb.stub.requests("POST /sessions")) != 0 {
		t.Fatal("session created")
	}
	// A provider failure is a redirect too, carrying the reason.
	resp = tb.getNoFollow(t, "/v1/banks/enablebanking/callback?state=state-x")
	if want := appRedirect + "?bank=error&error=Missing%20authorization%20code."; resp.Header.Get("Location") != want {
		t.Fatalf("location %q want %q", resp.Header.Get("Location"), want)
	}
	// Unknown references are a plain 400.
	r := tb.app.Do(http.MethodGet, "/v1/banks/enablebanking/callback?state=nope", "", nil)
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "Unknown bank link reference.") {
		t.Fatalf("unknown: %d %s", r.Status, r.Body)
	}
}

func TestEnableBankingReconsentReusesConnection(t *testing.T) {
	tb := newTestBank(t, nil)
	tb.stub.on("POST /sessions", ok(ebSession("session-1", "uid-1")), ok(ebSession("session-2", "uid-2")))
	booked := ebPage(nil, ebTx{ref: "r-1", amount: "10.00", creditor: "Pingo Doce"}.json())
	tb.stub.on("GET /accounts/uid-1/transactions", ok(booked))
	tb.stub.on("GET /accounts/uid-2/transactions", ok(booked))
	userID := tb.app.CreateUser("eb-reconsent@example.com")
	tb.makeFlow(t, userID, KindEnableBanking, "state-1")
	tb.getNoFollow(t, "/v1/banks/enablebanking/callback?state=state-1&code=c1")
	if _, err := tb.app.Deps.DB.Exec(context.Background(), `UPDATE bank_connections SET status = 'reauth_required'`); err != nil {
		t.Fatal(err)
	}
	tb.makeFlow(t, userID, KindEnableBanking, "state-2")
	resp := tb.getNoFollow(t, "/v1/banks/enablebanking/callback?state=state-2&code=c2")
	if resp.Header.Get("Location") != appRedirect+"?bank=connected" {
		t.Fatalf("location %q", resp.Header.Get("Location"))
	}
	var item, status string
	_ = tb.app.Deps.DB.QueryRow(context.Background(), `SELECT provider_item_id, status FROM bank_connections`).Scan(&item, &status)
	if tb.count(t, "bank_connections") != 1 || item != "session-2" || status != statusActive {
		t.Fatalf("connection %s %s", item, status)
	}
	var uid string
	_ = tb.app.Deps.DB.QueryRow(context.Background(), `SELECT provider_account_id FROM bank_accounts`).Scan(&uid)
	if tb.count(t, "bank_accounts") != 1 || uid != "uid-2" || tb.count(t, "bank_transactions") != 1 {
		t.Fatalf("accounts/transactions uid=%s", uid)
	}
}

func TestEnableBankingExpiredSessionRequiresReauth(t *testing.T) {
	tb := newTestBank(t, nil)
	tb.stub.on("GET /accounts/uid-1/transactions", stubReply{http.StatusUnauthorized, `{"code":401,"error":"EXPIRED_SESSION","message":"Session expired"}`})
	userID := tb.app.CreateUser("eb-expired@example.com")
	conn, _ := tb.makeConnection(t, userID, KindEnableBanking, "session-1", "uid-1")
	if _, err := tb.eb(t).Sync(context.Background(), Call{}, conn); err == nil {
		t.Fatal("want error")
	}
	got := tb.connection(t, conn.ID)
	if got.Status != statusReauthRequired || deref(got.LastSyncStatus) != "consent_revoked" {
		t.Fatalf("connection %+v", got)
	}
}

func TestEnableBankingPendingSuggestionsAreReplaced(t *testing.T) {
	tb := newTestBank(t, nil)
	tb.stub.on("GET /accounts/uid-1/transactions",
		ok(ebPage(nil, ebTx{amount: "7.00", status: "PDNG", date: "2026-10-18", creditor: "Bolt"}.json())),
		ok(ebPage(nil, ebTx{ref: "r-bolt", amount: "7.00", date: "2026-10-19", creditor: "Bolt"}.json())))
	userID := tb.app.CreateUser("eb-pending@example.com")
	conn, _ := tb.makeConnection(t, userID, KindEnableBanking, "session-1", "uid-1")
	p := tb.eb(t)
	if _, err := p.Sync(context.Background(), Call{}, conn); err != nil {
		t.Fatal(err)
	}
	if rows := tb.transactions(t); len(rows) != 1 || !rows[0].Pending {
		t.Fatalf("rows %+v", rows)
	}
	conn.LastSyncedAt = nil
	second, err := p.Sync(context.Background(), Call{}, conn)
	if err != nil {
		t.Fatal(err)
	}
	rows := tb.transactions(t)
	if second.Removed != 1 || len(rows) != 1 || rows[0].ProviderTxID != "r-bolt" || rows[0].Pending {
		t.Fatalf("second %+v rows %+v", second, rows)
	}
}

func TestRedirectAllowlist(t *testing.T) {
	list := "norviqa://oauth/bank-callback, https://www.norviq.org/settings/banks"
	for _, uri := range []string{"norviqa://oauth/bank-callback", "https://www.norviq.org/settings/banks"} {
		if err := validateRedirectURI(uri, list); err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
	}
	if err := validateRedirectURI("https://evil.example/settings/banks", list); err == nil {
		t.Fatal("evil allowed")
	}
	if err := validateRedirectURI("norviqa://oauth/bank-callback", ""); err == nil || !strings.Contains(err.Error(), "allowlist is not configured") {
		t.Fatalf("empty list: %v", err)
	}
}

// MARK: - GoCardless

func gcProvider(tb *testBank) *GoCardlessProvider {
	return &GoCardlessProvider{client: &goCardlessClient{config: GoCardlessConfig{BaseURL: "https://example.invalid"}, b: tb.bank.base}, b: tb.bank.base}
}

func gcTx(id, internal *string, booking, value string) gcTransaction {
	tx := gcTransaction{
		TransactionID: id, InternalTransactionID: internal,
		RemittanceInformationUnstructured: ptr("Cafe Lisboa"), CreditorName: ptr("Cafe Lisboa"),
	}
	if booking != "" {
		tx.BookingDate = &booking
	}
	if value != "" {
		tx.ValueDate = &value
	}
	tx.TransactionAmount.Amount, tx.TransactionAmount.Currency = "-12.34", ptr("EUR")
	return tx
}

func TestGoCardlessBookedUpdatesPendingWhenIDsChange(t *testing.T) {
	tb := newTestBank(t, nil)
	userID := tb.app.CreateUser("gocardless-dedupe@example.com")
	_, account := tb.makeConnection(t, userID, KindGoCardless, "requisition-1", "account-1")
	p, ctx := gcProvider(tb), context.Background()
	if added, err := p.upsert(ctx, gcTx(nil, ptr("internal-pending-1"), "", "2026-07-18"), true, account, userID); err != nil || !added {
		t.Fatalf("pending %v %v", added, err)
	}
	if added, err := p.upsert(ctx, gcTx(ptr("booked-transaction-1"), ptr("internal-pending-1"), "2026-07-18", ""), false, account, userID); err != nil || added {
		t.Fatalf("booked %v %v", added, err)
	}
	rows := tb.transactions(t)
	if len(rows) != 1 || rows[0].ProviderTxID != "booked-transaction-1" || rows[0].Pending || rows[0].Status != txSuggested {
		t.Fatalf("rows %+v", rows)
	}
}

func TestGoCardlessDistinctPendingWithSameHashArePreserved(t *testing.T) {
	tb := newTestBank(t, nil)
	userID := tb.app.CreateUser("gocardless-pending@example.com")
	_, account := tb.makeConnection(t, userID, KindGoCardless, "requisition-1", "account-1")
	p, ctx := gcProvider(tb), context.Background()
	for _, id := range []string{"internal-pending-1", "internal-pending-2"} {
		if added, err := p.upsert(ctx, gcTx(nil, ptr(id), "", "2026-07-18"), true, account, userID); err != nil || !added {
			t.Fatalf("%s %v %v", id, added, err)
		}
	}
	rows := tb.transactions(t)
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestGoCardlessBookedUpdatesMatchingPendingWhenHashesCollide(t *testing.T) {
	tb := newTestBank(t, nil)
	userID := tb.app.CreateUser("gocardless-collision@example.com")
	_, account := tb.makeConnection(t, userID, KindGoCardless, "requisition-1", "account-1")
	p, ctx := gcProvider(tb), context.Background()
	for _, id := range []string{"internal-pending-1", "internal-pending-2"} {
		if _, err := p.upsert(ctx, gcTx(nil, ptr(id), "", "2026-07-18"), true, account, userID); err != nil {
			t.Fatal(err)
		}
	}
	if added, err := p.upsert(ctx, gcTx(ptr("booked-transaction-2"), ptr("internal-pending-2"), "2026-07-18", ""), false, account, userID); err != nil || added {
		t.Fatalf("booked %v %v", added, err)
	}
	rows := tb.transactions(t)
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	var booked, pending int
	for i := range rows {
		switch {
		case rows[i].ProviderTxID == "booked-transaction-2" && !rows[i].Pending:
			booked++
		case rows[i].ProviderTxID == "internal-pending-1" && rows[i].Pending:
			pending++
		}
	}
	if booked != 1 || pending != 1 {
		t.Fatalf("rows %+v", rows)
	}
}

// MARK: - Plaid

func TestPlaidDedupeHash(t *testing.T) {
	account := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	a := plaidDedupeHash(account, "2026-07-12", 12.34, "Cafe Lisboa")
	if a != plaidDedupeHash(account, "2026-07-12", 12.34, "  cafe lisboa ") {
		t.Fatal("hash not stable")
	}
	if a == plaidDedupeHash(account, "2026-07-12", 12.35, "Cafe Lisboa") || a == plaidDedupeHash(account, "2026-07-13", 12.34, "Cafe Lisboa") {
		t.Fatal("hash not sensitive")
	}
}

func TestBase64URLDecode(t *testing.T) {
	got, ok := base64URLDecode("eyJhbGciOiJFUzI1NiJ9")
	if !ok || string(got) != `{"alg":"ES256"}` {
		t.Fatalf("got %q", got)
	}
	raw := []byte{0xFB, 0xEF, 0xBE, 0xFF}
	enc := strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(base64.StdEncoding.EncodeToString(raw)), "=")
	if got, ok := base64URLDecode(enc); !ok || !bytes.Equal(got, raw) {
		t.Fatalf("substitution %v", got)
	}
	if _, ok := base64URLDecode("!!!not base64!!!"); ok {
		t.Fatal("malformed accepted")
	}
}

func plaidTestBank(t *testing.T) *testBank {
	t.Helper()
	tb := newTestBank(t, func(s *Settings) {
		s.PlaidClientID, s.PlaidSecret = "client", "secret"
	})
	plaid, _ := tb.bank.Registry.Provider(KindPlaid)
	plaid.(*PlaidProvider).client.config.BaseURL = tb.stub.srv.URL
	return tb
}

// Vapor decoded Plaid responses through JSONDecoder.backendAPI, which
// rewrote link_token to linkToken before the snake_case CodingKey was looked
// up, so the link token never decoded (400 "No such key 'link_token'"). v2
// decodes the keys as Plaid spells them.
func TestPlaidLinkSessionDecodesSnakeCase(t *testing.T) {
	tb := plaidTestBank(t)
	tb.stub.on("POST /link/token/create", ok(`{"link_token":"link-sandbox-1","expiration":"2026-10-29T12:00:00Z","request_id":"r"}`))
	userID := tb.app.CreateUser("plaid-link@example.com")
	r := tb.app.Do(http.MethodPost, "/v1/banks/link-session", tb.app.Token(userID), nil)
	var got map[string]any
	r.JSON(t, &got)
	if r.Status != http.StatusOK || got["provider"] != "plaid" || got["linkToken"] != "link-sandbox-1" || got["expiration"] != "2026-10-29T12:00:00Z" {
		t.Fatalf("got %d %s", r.Status, r.Body)
	}
	reqs := tb.stub.requests("POST /link/token/create")
	if len(reqs) != 1 {
		t.Fatal("plaid not called")
	}
	var body map[string]any
	_ = json.Unmarshal(reqs[0].Body, &body)
	if body["client_id"] != "client" || body["secret"] != "secret" || fmt.Sprint(body["products"]) != "[transactions]" {
		t.Fatalf("body %v", body)
	}

	// A body Plaid sends without a required key is Plaid's fault: 502.
	tb.stub.on("POST /link/token/create", ok(`{"request_id":"r"}`))
	if r := tb.app.Do(http.MethodPost, "/v1/banks/link-session", tb.app.Token(userID), nil); r.Status != http.StatusBadGateway {
		t.Fatalf("undecodable %d %s", r.Status, r.Body)
	}
}

// Exchange and sync work end to end. The initial sync also returns a
// transaction on an account Plaid only lists on the second /accounts/get:
// Vapor cached the account and skipped the transaction; v2 imports it.
func TestPlaidExchangeAndSyncImportsNewAccountTransaction(t *testing.T) {
	tb := plaidTestBank(t)
	tb.stub.on("POST /item/public_token/exchange", ok(`{"access_token":"access-sandbox-1","item_id":"item-1","request_id":"r"}`))
	tb.stub.on("POST /accounts/get",
		ok(`{"accounts":[{"account_id":"acc-1","name":"Checking","mask":"0000","type":"depository","balances":{"current":100.5,"iso_currency_code":"USD"}}]}`),
		ok(`{"accounts":[{"account_id":"acc-1","name":"Checking","mask":"0000","type":"depository","balances":{"current":100.5,"iso_currency_code":"USD"}},{"account_id":"acc-2","name":"Savings","balances":{"current":5,"iso_currency_code":"USD"}}]}`))
	tb.stub.on("POST /transactions/sync", ok(`{"added":[`+
		`{"transaction_id":"tx-1","account_id":"acc-1","amount":12.5,"iso_currency_code":"USD","date":"2026-10-20","name":"COFFEE","merchant_name":"Cafe","pending":false,"category":["Food","Coffee"]},`+
		`{"transaction_id":"tx-2","account_id":"acc-2","amount":40,"iso_currency_code":"USD","date":"2026-10-21","name":"Groceries","pending":false}`+
		`],"modified":[],"removed":[],"next_cursor":"cur-1","has_more":false}`))
	userID := tb.app.CreateUser("plaid-exchange@example.com")
	r := tb.app.Do(http.MethodPost, "/v1/banks/connections", tb.app.Token(userID), map[string]any{"publicToken": "public-sandbox-1"})
	if r.Status != http.StatusOK {
		t.Fatalf("exchange %d %s", r.Status, r.Body)
	}
	if n := tb.count(t, "bank_accounts"); n != 2 {
		t.Fatalf("accounts %d", n)
	}
	rows := tb.transactions(t)
	got := map[string]bool{}
	for _, row := range rows {
		got[row.ProviderTxID] = true
	}
	if len(rows) != 2 || !got["tx-1"] || !got["tx-2"] {
		t.Fatalf("rows %+v", rows)
	}
	var cursor *string
	_ = tb.app.Deps.DB.QueryRow(context.Background(), `SELECT sync_cursor FROM bank_connections WHERE provider_item_id = 'item-1'`).Scan(&cursor)
	if deref(cursor) != "cur-1" {
		t.Fatalf("cursor %v", cursor)
	}
}

func TestGoCardlessCallbackCreatesConnection(t *testing.T) {
	var tb *testBank
	tb = newTestBank(t, func(s *Settings) {
		s.GoCardlessSecretID, s.GoCardlessSecretKey = "id", "key"
	})
	gc, _ := tb.bank.Registry.Provider(KindGoCardless)
	gc.(*GoCardlessProvider).client.config.BaseURL = tb.stub.srv.URL
	tb.stub.on("POST /token/new/", ok(`{"access":"tok","access_expires":86400}`))
	tb.stub.on("GET /requisitions/auth-ref-1/", ok(`{"id":"auth-ref-1","status":"LN","accounts":["acc-1"]}`))
	tb.stub.on("GET /accounts/acc-1/details/", ok(`{"account":{"name":"Checking","currency":"EUR"}}`))
	tb.stub.on("GET /accounts/acc-1/transactions/", ok(`{"transactions":{"booked":[{"transactionId":"t-1","bookingDate":"2026-07-18","transactionAmount":{"amount":"-12.34","currency":"EUR"},"creditorName":"Cafe"}],"pending":[]}}`))
	userID := tb.app.CreateUser("gc-callback@example.com")
	tb.makeFlow(t, userID, KindGoCardless, "ref-1")
	for range 2 {
		resp := tb.getNoFollow(t, "/v1/banks/gocardless/callback?ref=ref-1")
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != appRedirect+"?bank=connected" {
			t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if len(tb.stub.requests("GET /requisitions/auth-ref-1/")) != 1 {
		t.Fatal("replay re-fetched the requisition")
	}
	var name, institution string
	_ = tb.app.Deps.DB.QueryRow(context.Background(),
		`SELECT a.name, c.institution_name FROM bank_accounts a JOIN bank_connections c ON c.id = a.connection_id`).Scan(&name, &institution)
	if tb.count(t, "bank_connections") != 1 || name != "Checking" || institution != "PT:Banco Teste" {
		t.Fatalf("account %q institution %q", name, institution)
	}
	rows := tb.transactions(t)
	if len(rows) != 1 || rows[0].Amount != -12.34 || deref(rows[0].Merchant) != "Cafe" {
		t.Fatalf("rows %+v", rows)
	}
	if got := tb.stub.requests("GET /accounts/acc-1/transactions/")[0].Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("auth %q", got)
	}
}
