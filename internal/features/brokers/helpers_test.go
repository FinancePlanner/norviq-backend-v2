package brokers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

var bg = context.Background()

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(bg, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(bg, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func float(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) float64 {
	t.Helper()
	var f float64
	if err := pool.QueryRow(bg, sql, args...).Scan(&f); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return f
}

// noSearch is a market search that finds nothing.
type noSearch struct{}

func (noSearch) Search(context.Context, string) ([]marketdata.SearchResultResponse, error) {
	return nil, nil
}

// stubExtractor answers by image bytes (extraction runs concurrently).
type stubExtractor struct {
	byBody map[string]Extraction
}

func (*stubExtractor) IsEnabled() bool { return true }

func (s *stubExtractor) Extract(_ context.Context, image []byte, _ string) (Extraction, error) {
	return s.byBody[string(image)], nil
}

type stubPosition struct {
	ticker     string
	quantity   float64
	avgCost    float64
	assetClass string
}

type stubTrade struct {
	id, symbol, typ  string
	quantity, price  float64
	daysAgo          int
	assetClass       string
	conid            string
	listingExchange  string
	settleDaysOffset int
}

// stubGateway answers like the IBKR Client Portal gateway; transactions only
// inside the requested from...to window.
type stubGateway struct {
	mu        sync.Mutex
	accountID string
	positions []stubPosition
	trades    []stubTrade
	ledger    string
	calls     []string
}

func dayAgo(n int) time.Time {
	d := time.Now().UTC().AddDate(0, 0, -n)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

func (g *stubGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case strings.HasSuffix(r.URL.Path, "/portfolio/accounts"):
		write([]map[string]any{{"accountId": g.accountID, "displayName": "Ledger IBKR"}})
	case strings.HasSuffix(r.URL.Path, "/positions/0"):
		out := []map[string]any{}
		for _, p := range g.positions {
			o := map[string]any{"ticker": p.ticker, "position": p.quantity, "avgCost": p.avgCost, "currency": "USD"}
			if p.assetClass != "" {
				o["assetClass"] = p.assetClass
			}
			out = append(out, o)
		}
		write(out)
	case strings.HasSuffix(r.URL.Path, "/pa/transactions"):
		from, _ := time.Parse("2006-01-02", r.URL.Query().Get("from"))
		to, _ := time.Parse("2006-01-02", r.URL.Query().Get("to"))
		to = to.Add(86399 * time.Second)
		out := []map[string]any{}
		for i := range g.trades {
			tr := &g.trades[i]
			d := dayAgo(tr.daysAgo)
			if d.Before(from) || d.After(to) {
				continue
			}
			o := map[string]any{
				"transactionID": tr.id, "symbol": tr.symbol, "type": tr.typ, "quantity": tr.quantity,
				"price": tr.price, "currency": "USD", "date": d.Format("2006-01-02"),
			}
			if tr.assetClass != "" {
				o["assetClass"] = tr.assetClass
			}
			if tr.conid != "" {
				o["conid"] = tr.conid
			}
			out = append(out, o)
		}
		write(out)
	case strings.Contains(r.URL.Path, "/iserver/contract/"):
		for i := range g.trades {
			tr := &g.trades[i]
			if tr.conid != "" && strings.Contains(r.URL.Path, "/"+tr.conid+"/") {
				write(map[string]any{"listingExchange": tr.listingExchange})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/ledger"):
		if g.ledger == "" {
			_, _ = w.Write([]byte("{}"))
		} else {
			_, _ = w.Write([]byte(g.ledger))
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{})
	}
}

// harness is a v2 app with the brokers feature wired to stubs.
type harness struct {
	app     *testutil.App
	gateway *stubGateway
	gwURL   string
	env     map[string]string
	extract *stubExtractor
	svc     *service
}

func newHarness(t *testing.T, env map[string]string) *harness {
	t.Helper()
	h := &harness{gateway: &stubGateway{accountID: "ULEDGER" + uuid.NewString()[:8]}, env: map[string]string{}, extract: &stubExtractor{}}
	gw := httptest.NewServer(h.gateway)
	t.Cleanup(gw.Close)
	h.gwURL = gw.URL + "/v1/api"
	h.env["IBKR_API_BASE_URL"] = h.gwURL
	for k, v := range env {
		h.env[k] = v
	}
	t.Setenv("BYPASS_BILLING", "true")
	f := h.feature()
	h.app = testutil.NewApp(t, []server.Feature{f})
	s, err := f.service(h.app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	h.svc = s
	return h
}

func (h *harness) feature() Feature {
	return Feature{
		Market:    noSearch{},
		Extractor: h.extract,
		Consent:   func(*echo.Context, uuid.UUID) error { return nil },
		Env:       func(k string) string { return h.env[k] },
		Sleep:     func(context.Context, time.Duration) error { return nil },
	}
}

func (h *harness) user(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	id := h.app.CreateUser("parity-brokers-test-" + uuid.NewString()[:8] + "@example.com")
	return id, h.app.Token(id)
}

func (h *harness) instrument(t *testing.T, symbol string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	mustExec(t, h.app.Deps.DB, `INSERT INTO instruments (id, conid, symbol, exchange, currency, created_at) VALUES ($1, $2, $3, 'TEST', 'USD', now())`,
		id, "test:"+symbol+":"+id.String()[:6], symbol)
	return id
}

func (h *harness) connection(t *testing.T, userID uuid.UUID) {
	t.Helper()
	mustExec(t, h.app.Deps.DB, `INSERT INTO broker_connections (id, user_id, provider, status, created_at, updated_at) VALUES ($1, $2, 'ibkr', 'connected', now(), now())`,
		uuid.New(), userID)
}
