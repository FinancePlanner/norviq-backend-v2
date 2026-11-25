package fxrates_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"norviq-api/internal/features/fxrates"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type ecbStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []string
	last map[string]string // quote -> raw query
}

func newECBStub(t *testing.T) *ecbStub {
	t.Helper()
	s := &ecbStub{last: map[string]string{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		s.mu.Lock()
		s.hits = append(s.hits, path)
		s.mu.Unlock()
		parts := strings.Split(path, "/")
		key := parts[len(parts)-1] // D.USD.EUR.SP00.A
		quote := strings.Split(key, ".")[1]
		s.mu.Lock()
		s.last[quote] = r.URL.RawQuery
		s.mu.Unlock()
		header := "OBS_STATUS,OBS_VALUE,KEY,TIME_PERIOD\n"
		switch quote {
		case "USD":
			_, _ = w.Write([]byte(header + "A,1.0512,EXR." + key + ",2026-11-24\nA,1.0480,EXR." + key + ",2026-11-21\n"))
		case "GBP":
			_, _ = w.Write([]byte(header + "A,0.8800,EXR." + key + ",2026-11-24\n"))
		case "DKK":
			_, _ = w.Write([]byte(header + "A,7.4600,EXR." + key + ",2026-11-24\n"))
		case "TWD":
			_, _ = w.Write([]byte(header))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *ecbStub) quotes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, h := range s.hits {
		p := strings.Split(h, "/")
		out = append(out, strings.Split(p[len(p)-1], ".")[1])
	}
	sort.Strings(out)
	return out
}

func seed(t *testing.T, app *testutil.App, quoteCurrencies ...string) {
	t.Helper()
	ctx := context.Background()
	uid := app.CreateUser("fx@example.com")
	if _, err := app.Deps.DB.Exec(ctx,
		`INSERT INTO portfolio_lists (id, user_id, name, base_currency, created_at) VALUES ($1,$2,'fx','USD', now())`,
		uuid.New(), uid); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	for i, c := range quoteCurrencies {
		if _, err := app.Deps.DB.Exec(ctx,
			`INSERT INTO quote_cache (id, provider, symbol, currency, price, as_of, created_at) VALUES ($1,'t',$2,$3,1,now(),now())`,
			uuid.New(), "SYM"+string(rune('A'+i)), c); err != nil {
			t.Fatalf("seed quote: %v", err)
		}
	}
}

func logs() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestRunECBTick(t *testing.T) {
	app := testutil.NewApp(t, nil)
	seed(t, app, "GBp", "DKK", "EUR", "GBP")
	stub := newECBStub(t)
	logger, buf := logs()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	n, err := fxrates.RunECBTick(context.Background(), app.Deps.DB, stub.srv.Client(), stub.srv.URL, now, logger)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 { // USD 2 + GBP 1 + DKK 1
		t.Fatalf("inserted %d", n)
	}
	if got := strings.Join(stub.quotes(), ","); got != "DKK,GBP,USD" {
		t.Fatalf("requested %s", got)
	}
	if q := stub.last["USD"]; !strings.Contains(q, "startPeriod=2026-11-15") || !strings.Contains(q, "endPeriod=2026-11-25") {
		t.Fatalf("window %q", q)
	}
	n, err = fxrates.RunECBTick(context.Background(), app.Deps.DB, stub.srv.Client(), stub.srv.URL, now, logger)
	if err != nil || n != 0 {
		t.Fatalf("second run %d %v", n, err)
	}
	if strings.Contains(buf.String(), "fetch failed") {
		t.Fatalf("unexpected failure log: %s", buf.String())
	}
}

func TestRunECBTickUnsupportedAndFailure(t *testing.T) {
	app := testutil.NewApp(t, nil)
	seed(t, app, "TWD", "XYZ", "DKK")
	stub := newECBStub(t)
	logger, buf := logs()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	// A transport that fails DKK with 503 and passes the rest through.
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "D.DKK.") {
			return &http.Response{StatusCode: 503, Body: http.NoBody, Header: http.Header{}}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	n, err := fxrates.RunECBTick(context.Background(), app.Deps.DB, client, stub.srv.URL, now, logger)
	if err != nil {
		t.Fatalf("tick must not fail: %v", err)
	}
	if n != 2 { // USD from the seeded list; TWD/XYZ/DKK store nothing
		t.Fatalf("inserted %d", n)
	}
	out := buf.String()
	for _, want := range []string{
		"level=INFO msg=\"fx_ecb_daily unsupported currency\" currency=TWD",
		"level=INFO msg=\"fx_ecb_daily unsupported currency\" currency=XYZ",
		"level=WARN msg=\"fx_ecb_daily fetch failed\" currency=DKK",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fetch failed\" currency=TWD") || strings.Contains(out, "fetch failed\" currency=XYZ") {
		t.Errorf("unsupported logged as failure:\n%s", out)
	}
}

func TestRunECBTickWarnsWhenKnownCurrencyStops(t *testing.T) {
	app := testutil.NewApp(t, nil)
	seed(t, app, "TWD")
	if _, err := app.Deps.DB.Exec(context.Background(),
		`INSERT INTO fx_daily_rates (id, date, base, quote, rate) VALUES ($1,'2026-11-20','EUR','TWD',35)`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	stub := newECBStub(t) // answers 200 with no rows for TWD
	logger, buf := logs()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if _, err := fxrates.RunECBTick(context.Background(), app.Deps.DB, stub.srv.Client(), stub.srv.URL, now, logger); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN msg=\"fx_ecb_daily no new fixings\" currency=TWD") {
		t.Errorf("missing warn in:\n%s", out)
	}
	if strings.Contains(out, "unsupported currency\" currency=TWD") {
		t.Errorf("known currency logged as unsupported:\n%s", out)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCurrenciesInUseIncludesStocks(t *testing.T) {
	app := testutil.NewApp(t, nil)
	seed(t, app)
	ctx := context.Background()
	uid := app.CreateUser("fx-stock@example.com")
	var listID uuid.UUID
	if err := app.Deps.DB.QueryRow(ctx, `SELECT id FROM portfolio_lists WHERE name = 'fx' LIMIT 1`).Scan(&listID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Deps.DB.Exec(ctx,
		`INSERT INTO stocks (id, user_id, portfolio_list_id, symbol, shares, buy_price, buy_date, currency, created_at)
		 VALUES ($1,$2,$3,'NESN',1,1,'2026-01-02','CHF',now())`, uuid.New(), uid, listID); err != nil {
		t.Fatal(err)
	}
	stub := newECBStub(t)
	logger, _ := logs()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if _, err := fxrates.RunECBTick(ctx, app.Deps.DB, stub.srv.Client(), stub.srv.URL, now, logger); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stub.quotes(), ","); !strings.Contains(got, "CHF") {
		t.Fatalf("CHF not requested: %s", got)
	}
}
