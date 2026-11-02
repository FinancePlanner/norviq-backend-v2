// Package bankrevoke is the account-deletion half of Vapor's bank providers:
// BankConnectionRevoker and each provider's `disconnect(connection:)` (Plaid
// /item/remove, GoCardless requisition delete, Enable Banking session
// delete). The full providers belong to the banking port; this package
// carries only the revocation calls, configured from the same variables.
package bankrevoke

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"norviq-api/internal/platform/crypto"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Connection is the part of a bank_connections row a revocation needs.
type Connection struct {
	ID             uuid.UUID
	Provider       string
	ProviderItemID string
	AccessTokenEnc *string
}

// Provider disconnects one connection at its aggregator.
type Provider interface {
	Disconnect(ctx context.Context, c Connection) error
}

// Kinds are BankProviderKind's raw values.
const (
	KindPlaid         = "plaid"
	KindGoCardless    = "gocardless"
	KindEnableBanking = "enablebanking"
)

// ErrNotConfigured is BankProviderError.notConfigured.
type ErrNotConfigured struct{ Kind string }

func (e ErrNotConfigured) Error() string {
	return "Bank provider " + e.Kind + " is not configured."
}

// ErrTimedOut is BankConnectionRevoker.TimedOut.
var ErrTimedOut = errors.New("timed out")

// Revoker is BankConnectionRevoker over a BankProviderRegistry.
type Revoker struct {
	Providers map[string]Provider
	Logger    *slog.Logger
	// Timeout caps each call (default 10s), so a hung provider cannot hold
	// a deletion open.
	Timeout time.Duration
}

// Revoke is BankConnectionRevoker.revoke: best effort. A failure is
// logged, never returned: the account deletion must still go through.
func (r *Revoker) Revoke(ctx context.Context, c Connection) {
	err := r.revoke(ctx, c)
	if err != nil && r.Logger != nil {
		r.Logger.Warn(fmt.Sprintf("bank.revoke_failed provider=%s connection_id=%s error_type=%T",
			c.Provider, strings.ToUpper(c.ID.String()), err))
	}
}

func (r *Revoker) revoke(ctx context.Context, c Connection) error {
	kind := c.Provider
	switch kind {
	case KindPlaid, KindGoCardless, KindEnableBanking:
	default:
		// BankProviderKind(rawValue:) ?? .plaid
		kind = KindPlaid
	}
	p, ok := r.Providers[kind]
	if !ok {
		return ErrNotConfigured{Kind: kind}
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	// Whichever finishes first wins; the loser is canceled and left.
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Disconnect(cctx, c) }()
	select {
	case err := <-done:
		return err
	case <-cctx.Done():
		return ErrTimedOut
	}
}

// FromEnv registers each provider whose credentials are configured, as
// configureBankProviders did.
func FromEnv(vault *crypto.Vault, client *http.Client, logger *slog.Logger) *Revoker {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	get := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	providers := map[string]Provider{}
	if id, secret := get("PLAID_CLIENT_ID"), get("PLAID_SECRET"); id != "" && secret != "" {
		providers[KindPlaid] = Plaid{ClientID: id, Secret: secret, BaseURL: PlaidHost(get("PLAID_ENV")), Vault: vault, HTTP: client}
	}
	if id, key := get("GOCARDLESS_SECRET_ID"), get("GOCARDLESS_SECRET_KEY"); id != "" && key != "" {
		base := get("GOCARDLESS_API_BASE")
		if base == "" {
			base = "https://bankaccountdata.gocardless.com/api/v2"
		}
		providers[KindGoCardless] = GoCardless{SecretID: id, SecretKey: key, BaseURL: base, HTTP: client}
	}
	if app, pem := get("ENABLEBANKING_APP_ID"), get("ENABLEBANKING_PRIVATE_KEY_PEM"); app != "" && pem != "" {
		base := get("ENABLEBANKING_API_BASE")
		if base == "" {
			base = "https://api.enablebanking.com"
		}
		providers[KindEnableBanking] = EnableBanking{
			ApplicationID: app, PrivateKeyPEM: strings.ReplaceAll(pem, `\n`, "\n"), BaseURL: base, HTTP: client,
		}
	}
	return &Revoker{Providers: providers, Logger: logger}
}

// PlaidHost is PlaidConfiguration's PLAID_ENV mapping (default sandbox).
func PlaidHost(env string) string {
	switch strings.ToLower(env) {
	case "production":
		return "https://production.plaid.com"
	case "development":
		return "https://development.plaid.com"
	}
	return "https://sandbox.plaid.com"
}

// Plaid is PlaidProvider.disconnect: /item/remove with the decrypted access
// token; nothing to do without one.
type Plaid struct {
	ClientID, Secret, BaseURL string
	Vault                     *crypto.Vault
	HTTP                      *http.Client
}

// ErrPlaid is the 502 "Plaid request failed." Abort.
//
//lint:ignore ST1005 Vapor's reason is part of the wire-compatible error.
var ErrPlaid = errors.New("Plaid request failed.")

func (p Plaid) Disconnect(ctx context.Context, c Connection) error {
	if c.AccessTokenEnc == nil {
		return nil
	}
	if p.Vault == nil {
		return errors.New("token encryption unavailable")
	}
	token, err := p.Vault.Decrypt(*c.AccessTokenEnc, crypto.ContextBankPlaid)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"access_token": token, "client_id": p.ClientID, "secret": p.Secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/item/remove", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ErrPlaid
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj)
}

// GoCardless is GoCardlessProvider.disconnect: a fresh access token, then
// DELETE /requisitions/{id}/ (404 counts as done).
type GoCardless struct {
	SecretID, SecretKey, BaseURL string
	HTTP                         *http.Client
}

// ErrGoCardless is the 502 "GoCardless request failed." Abort.
//
//lint:ignore ST1005 Vapor's reason is part of the wire-compatible error.
var ErrGoCardless = errors.New("GoCardless request failed.")

func (g GoCardless) Disconnect(ctx context.Context, c Connection) error {
	body, _ := json.Marshal(map[string]string{"secret_id": g.SecretID, "secret_key": g.SecretKey})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.BaseURL+"/token/new/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrGoCardless
	}
	var tok struct {
		Access *string `json:"access"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return err
	}
	if tok.Access == nil {
		return errors.New("GoCardless token response has no access token")
	}
	del, err := http.NewRequestWithContext(ctx, http.MethodDelete, g.BaseURL+"/requisitions/"+c.ProviderItemID+"/", nil)
	if err != nil {
		return err
	}
	del.Header.Set("Authorization", "Bearer "+*tok.Access)
	dresp, err := g.HTTP.Do(del)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, dresp.Body)
	_ = dresp.Body.Close()
	if dresp.StatusCode == http.StatusNotFound {
		return nil
	}
	if dresp.StatusCode < 200 || dresp.StatusCode >= 300 {
		return ErrGoCardless
	}
	return nil
}

// EnableBanking is EnableBankingProvider.disconnect: DELETE /sessions/{id}
// with a short-lived RS256 JWT. 404 and the "session already gone" error
// codes count as done.
type EnableBanking struct {
	ApplicationID, PrivateKeyPEM, BaseURL string
	HTTP                                  *http.Client
	Now                                   func() time.Time
}

// APIError is EnableBankingAPIError.
type APIError struct {
	StatusCode int
	Code       *string
}

func (e APIError) Error() string {
	return fmt.Sprintf("Enable Banking request failed (%d).", e.StatusCode)
}

// reauthCodes is EnableBankingAPIError.reauthCodes.
var reauthCodes = map[string]bool{
	"EXPIRED_SESSION": true, "CLOSED_SESSION": true, "REVOKED_SESSION": true, "SESSION_DOES_NOT_EXIST": true,
}

// Token is EnableBankingClient.makeToken: kid is the application id,
// iss enablebanking.com, aud [api.enablebanking.com], valid an hour.
func (e EnableBanking) Token(now time.Time) (string, error) {
	key, err := parseRSAKey(e.PrivateKeyPEM)
	if err != nil {
		//lint:ignore ST1005 Vapor's reason is part of the wire-compatible error.
		return "", errors.New("Invalid Enable Banking private key format.")
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "enablebanking.com",
		"aud": []string{"api.enablebanking.com"},
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	})
	t.Header["kid"] = e.ApplicationID
	t.Header["typ"] = "JWT"
	return t.SignedString(key)
}

func parseRSAKey(pem string) (*rsa.PrivateKey, error) {
	return jwt.ParseRSAPrivateKeyFromPEM([]byte(pem))
}

func (e EnableBanking) Disconnect(ctx context.Context, c Connection) error {
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	token, err := e.Token(now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.BaseURL+"/sessions/"+c.ProviderItemID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var body struct {
		Error *string `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	apiErr := APIError{StatusCode: resp.StatusCode, Code: body.Error}
	if resp.StatusCode == http.StatusNotFound || (body.Error != nil && reauthCodes[*body.Error]) {
		return nil
	}
	return apiErr
}
