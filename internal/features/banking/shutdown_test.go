package banking

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/store/bankingdb"
)

// cancelingProvider cancels the tick from inside its first Sync and counts
// calls, so the loop's own ctx check is what stops the next connection.
type cancelingProvider struct {
	Provider
	cancel context.CancelFunc
	mu     sync.Mutex
	calls  int
}

func (p *cancelingProvider) Kind() Kind { return KindEnableBanking }

func (p *cancelingProvider) Sync(context.Context, Call, *bankingdb.BankConnection) (SyncResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.cancel()
	return SyncResult{}, nil
}

// Shutdown ends a sync tick between connections, never inside a provider call.
func TestSyncOnceStopsBetweenConnectionsOnCancel(t *testing.T) {
	tb := newTestBank(t, nil)
	userID := tb.app.CreateUser("bank-cancel@example.com")
	tb.makeConnection(t, userID, KindEnableBanking, "session-1", "uid-1")
	tb.makeConnection(t, userID, KindEnableBanking, "session-2", "uid-2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &cancelingProvider{cancel: cancel}
	tb.bank.Registry = NewRegistry(p)

	tb.bank.SyncOnce(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 1 {
		t.Fatalf("provider synced %d connections after cancel, want 1", p.calls)
	}
}

// A failed Plaid sync is logged with the connection and still answers 200: the
// 6-hour poller is the backstop, and there is no error taxonomy for a retry.
func TestPlaidWebhookLogsSyncFailureAndAnswers200(t *testing.T) {
	tb := newTestBank(t, func(s *Settings) { s.PlaidClientID, s.PlaidSecret = "client", "secret" })
	logs := &syncBuffer{}
	tb.bank.base.logger = slog.New(slog.NewJSONHandler(logs, nil))
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
	// /transactions/sync is not stubbed, so the provider's Sync fails.
	userID := tb.app.CreateUser("plaid-log@example.com")
	conn := &bankingdb.BankConnection{UserID: userID, Provider: "plaid", ProviderItemID: "item-1", Status: statusActive}
	if err := tb.bank.base.insertConnection(context.Background(), conn); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"webhook_type":"TRANSACTIONS","webhook_code":"SYNC_UPDATES_AVAILABLE","item_id":"item-1"}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, tb.app.Server.URL+"/webhooks/plaid", bytes.NewReader(body))
	req.Header.Set("Plaid-Verification", signPlaid(t, key, time.Now().Unix(), body))
	resp, err := tb.app.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status %d, want 200", resp.StatusCode)
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"plaid.webhook.sync_failed"`) ||
		!strings.Contains(strings.ToLower(out), strings.ToLower(conn.ID.String())) ||
		!strings.Contains(out, `"error":`) {
		t.Fatalf("no sync failure logged: %s", out)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
