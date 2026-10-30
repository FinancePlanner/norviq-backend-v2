package crypto

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type cryptoApp struct {
	*testutil.App
	t *testing.T
}

func newCryptoApp(t *testing.T) *cryptoApp {
	t.Helper()
	fixed := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	f := Feature{
		Provider:     MockProvider{Now: func() time.Time { return fixed }},
		Markets:      makeService(&stubMarkets{coins: universe()}, nil, defaultRef()),
		Entitlements: func(d *server.Deps) *entitlements.Service { return entitlements.New(d.DB, entitlements.Config{}) },
	}
	return &cryptoApp{App: testutil.NewApp(t, []server.Feature{f}), t: t}
}

func (a *cryptoApp) exec(sql string, args ...any) {
	a.t.Helper()
	if _, err := a.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		a.t.Fatal(err)
	}
}

func (a *cryptoApp) freeUser() uuid.UUID {
	return a.CreateUser("crypto-" + uuid.NewString() + "@example.com")
}

func (a *cryptoApp) trialUser() uuid.UUID {
	id := a.freeUser()
	a.exec(`UPDATE users SET trial_started_at = now(), trial_days = 7, trial_tier = 'temporary' WHERE id = $1`, id)
	return id
}

func (a *cryptoApp) proUser() uuid.UUID {
	id := a.freeUser()
	a.exec(`INSERT INTO entitlements (id, user_id, level, subscription_id, created_at, updated_at) VALUES ($1, $2, 'pro', NULL, now(), now())`, uuid.New(), id)
	return id
}

// send posts a raw body with an explicit content type.
func (a *cryptoApp) send(method, path, token, contentType, body string) (int, string) {
	a.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, a.Server.URL+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.Server.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var readPaths = []string{
	"/v1/crypto/list",
	"/v1/crypto/quote/BTCUSD",
	"/v1/crypto/quote-short/BTCUSD",
	"/v1/crypto/batch-quotes",
	"/v1/crypto/history/light/BTCUSD",
	"/v1/crypto/news",
	"/v1/crypto/news/BTCUSD",
	"/v1/crypto/markets",
	"/v1/crypto/portfolio",
}

// BillingTests: every crypto endpoint is premium.
func TestFreeUserIsBlockedEverywhere(t *testing.T) {
	a := newCryptoApp(t)
	tok := a.Token(a.freeUser())
	assertUpgrade := func(status int, body, what string) {
		t.Helper()
		if status != http.StatusForbidden || !strings.Contains(body, "upgrade_required") ||
			!strings.Contains(body, `"feature":"crypto"`) || !strings.Contains(body, `"requiredPlan":"pro"`) {
			t.Errorf("%s: %d %s", what, status, body)
		}
	}
	for _, p := range readPaths {
		s, b := a.send(http.MethodGet, p, tok, "", "")
		assertUpgrade(s, b, p)
	}
	form := "application/x-www-form-urlencoded"
	s, b := a.send(http.MethodPost, "/v1/crypto/portfolio", tok, form, "symbol=BTC&name=Bitcoin&quantity=1&average_buy_price=1")
	assertUpgrade(s, b, "POST")
	id := uuid.NewString()
	s, b = a.send(http.MethodPut, "/v1/crypto/portfolio/"+id, tok, form, "symbol=BTC&name=Bitcoin&quantity=1&average_buy_price=1")
	assertUpgrade(s, b, "PUT")
	s, b = a.send(http.MethodDelete, "/v1/crypto/portfolio/"+id, tok, "", "")
	assertUpgrade(s, b, "DELETE")
}

func TestTrialAndProAreAllowed(t *testing.T) {
	a := newCryptoApp(t)
	for name, user := range map[string]uuid.UUID{"trial": a.trialUser(), "pro": a.proUser()} {
		tok := a.Token(user)
		for _, p := range readPaths {
			if s, b := a.send(http.MethodGet, p, tok, "", ""); s != http.StatusOK {
				t.Errorf("%s %s: %d %s", name, p, s, b)
			}
		}
	}
}

func TestUnauthenticated(t *testing.T) {
	a := newCryptoApp(t)
	if r := a.Do(http.MethodGet, "/v1/crypto/list", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatal(r.Status)
	}
}

func TestReadRouteErrors(t *testing.T) {
	a := newCryptoApp(t)
	tok := a.Token(a.proUser())
	cases := []struct {
		path   string
		status int
		reason string
	}{
		{"/v1/crypto/markets?timeframe=5y", 400, "timeframe must be one of: 1d, 1w, 1m, ytd, 1y, all."},
		{"/v1/crypto/history/weekly/BTCUSD", 400, "Invalid resolution."},
	}
	for _, c := range cases {
		s, b := a.send(http.MethodGet, c.path, tok, "", "")
		if s != c.status || !strings.Contains(b, c.reason) {
			t.Errorf("%s: %d %s", c.path, s, b)
		}
	}
	s, b := a.send(http.MethodGet, "/v1/crypto/markets?timeframe=ALL&limit=1", tok, "", "")
	if s != 200 || !strings.Contains(b, `"timeframe":"all"`) || strings.Count(b, `"id":"`) < 1 {
		t.Errorf("markets: %d %s", s, b)
	}
}

func TestPortfolioCRUD(t *testing.T) {
	a := newCryptoApp(t)
	tok := a.Token(a.proUser())
	form := "application/x-www-form-urlencoded"

	// Vapor bug ported: the JSON CodingKey "average_buy_price" can never match.
	s, b := a.send(http.MethodPost, "/v1/crypto/portfolio", tok, "application/json",
		`{"symbol":"btc","name":"Bitcoin","quantity":1,"averageBuyPrice":100,"average_buy_price":100}`)
	if s != 400 || !strings.Contains(b, "average_buy_price") {
		t.Fatalf("json post: %d %s", s, b)
	}

	s, b = a.send(http.MethodPost, "/v1/crypto/portfolio", tok, form, "symbol=+btc+&name=+Bitcoin+&quantity=1&average_buy_price=100")
	if s != 201 || !strings.Contains(b, `"symbol":"BTC"`) || !strings.Contains(b, `"name":"Bitcoin"`) {
		t.Fatalf("create: %d %s", s, b)
	}
	// Merge: weighted average, name taken untrimmed (Vapor quirk).
	s, b = a.send(http.MethodPost, "/v1/crypto/portfolio", tok, form, "symbol=BTC&name=+BTC+&quantity=3&average_buy_price=200")
	if s != 201 || !strings.Contains(b, `"quantity":4`) || !strings.Contains(b, `"averageBuyPrice":175`) || !strings.Contains(b, `"name":" BTC "`) {
		t.Fatalf("merge: %d %s", s, b)
	}
	var list []PortfolioItemResponse
	r := a.Do(http.MethodGet, "/v1/crypto/portfolio", tok, nil)
	r.JSON(t, &list)
	if len(list) != 1 {
		t.Fatalf("list: %s", r.Body)
	}
	id := list[0].ID

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range map[string]string{"symbol": "eth", "name": "Ether", "quantity": "2", "average_buy_price": "10"} {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	s, b = a.send(http.MethodPut, "/v1/crypto/portfolio/"+strings.ToLower(id), tok, mw.FormDataContentType(), buf.String())
	if s != 200 || !strings.Contains(b, `"symbol":"ETH"`) || !strings.Contains(b, `"quantity":2`) {
		t.Fatalf("update: %d %s", s, b)
	}

	s, b = a.send(http.MethodPost, "/v1/crypto/portfolio", tok, form, "symbol=BTC&name=B&quantity=x&average_buy_price=1")
	if s != 400 || !strings.Contains(b, "Value was not of type 'Double' at path 'quantity'") {
		t.Fatalf("bad number: %d %s", s, b)
	}
	s, b = a.send(http.MethodPost, "/v1/crypto/portfolio", tok, form, "symbol=BTC&quantity=1&average_buy_price=1")
	if s != 400 || !strings.Contains(b, "No value found (expected type 'String') at path 'name'") {
		t.Fatalf("missing: %d %s", s, b)
	}

	missing := uuid.NewString()
	if s, b = a.send(http.MethodPut, "/v1/crypto/portfolio/"+missing, tok, form, "symbol=A&name=A&quantity=1&average_buy_price=1"); s != 404 || !strings.Contains(b, "Crypto portfolio item not found.") {
		t.Fatalf("put 404: %d %s", s, b)
	}
	if s, b = a.send(http.MethodDelete, "/v1/crypto/portfolio/nope", tok, "", ""); s != 400 || !strings.Contains(b, "Invalid itemId.") {
		t.Fatalf("bad id: %d %s", s, b)
	}
	// Another user's item is invisible.
	other := a.Token(a.proUser())
	if s, _ = a.send(http.MethodDelete, "/v1/crypto/portfolio/"+id, other, "", ""); s != 404 {
		t.Fatalf("cross-user delete: %d", s)
	}
	if s, _ = a.send(http.MethodDelete, "/v1/crypto/portfolio/"+id, tok, "", ""); s != 204 {
		t.Fatalf("delete: %d", s)
	}
	if s, _ = a.send(http.MethodDelete, "/v1/crypto/portfolio/"+id, tok, "", ""); s != 404 {
		t.Fatalf("delete again: %d", s)
	}
}

func TestRefreshJobTick(t *testing.T) {
	a := newCryptoApp(t)
	p := &stubMarkets{coins: universe()}
	m := makeService(p, nil, defaultRef())
	RefreshJob{}.Tick(context.Background(), a.Deps, m, 0)
	if p.universeCalls() != 1 {
		t.Fatalf("calls = %d", p.universeCalls())
	}
}
