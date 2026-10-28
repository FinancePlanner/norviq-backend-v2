package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/middleware"
	"norviq-api/internal/server"
	"norviq-api/internal/store"

	"github.com/google/uuid"
)

// JWTSecret is the secret test apps sign sessions with.
const JWTSecret = "test-secret"

// App is a running v2 server over a fresh database, with no legacy upstream:
// an unported route answers 404, so a test can never pass by accident
// against Vapor.
type App struct {
	T      testing.TB
	Deps   *server.Deps
	Server *httptest.Server
}

// NewApp starts the given features. mutate may adjust Deps (config, Redis)
// before routes are mounted.
func NewApp(t testing.TB, features []server.Feature, mutate ...func(*server.Deps)) *App {
	t.Helper()
	pool := DB(t)
	cfg := config.Base{Environment: config.Testing, JWTSecret: JWTSecret}
	q := store.New(pool)
	d := &server.Deps{
		Config:  cfg,
		Logger:  slog.New(slog.DiscardHandler),
		DB:      pool,
		Queries: q,
		Auth:    &auth.Authenticator{Signer: auth.NewSigner(JWTSecret), Store: auth.PGStore{Q: q}},
		Admin: auth.Admin{IDs: map[uuid.UUID]struct{}{}, UserExists: func(ctx context.Context, id uuid.UUID) (bool, error) {
			return q.UserExists(ctx, id)
		}},
		Health: server.NewHealth(),
	}
	d.Limiter = &middleware.Limiter{ClientIP: middleware.NewClientIP(cfg.TrustedProxyCIDRs, ""), Logger: d.Logger}
	for _, m := range mutate {
		m(d)
	}
	srv, err := server.New(d, features)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return &App{T: t, Deps: d, Server: ts}
}

// CreateUser inserts a minimal verified user and returns its id.
func (a *App) CreateUser(email string) uuid.UUID {
	a.T.Helper()
	id := uuid.New()
	_, err := a.Deps.DB.Exec(context.Background(),
		`INSERT INTO users (id, email, password_hash, created_at, is_verified) VALUES ($1, $2, $3, now(), true)`,
		id, email, "$2b$12$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvali")
	if err != nil {
		a.T.Fatalf("create user: %v", err)
	}
	return id
}

// Token signs a SessionToken for userID.
func (a *App) Token(userID uuid.UUID) string {
	now := time.Now()
	tok, err := auth.NewSigner(JWTSecret).Mint(userID, now.Add(time.Hour), now)
	if err != nil {
		a.T.Fatalf("mint: %v", err)
	}
	return tok
}

// Response is a completed request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body into v, failing the test on error.
func (r *Response) JSON(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

// Do sends a request. body may be nil, a []byte, or any value to encode as
// JSON. token may be empty.
func (a *App) Do(method, path, token string, body any) *Response {
	a.T.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			a.T.Fatalf("encode body: %v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.Server.URL+path, rd)
	if err != nil {
		a.T.Fatalf("request: %v", err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.Server.Client().Do(req)
	if err != nil {
		a.T.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		a.T.Fatalf("read: %v", err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}
