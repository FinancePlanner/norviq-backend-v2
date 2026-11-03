package banking

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"norviq-api/internal/httpx"
	"norviq-api/internal/store/bankingdb"

	"github.com/labstack/echo/v5"
)

// Plaid webhook verification failures (PlaidWebhookVerificationError).
var (
	errPlaidNotConfigured = httpx.Abort(http.StatusServiceUnavailable, "Plaid webhook verification is not configured.")
	errPlaidMissingHeader = httpx.Abort(http.StatusUnauthorized, "Missing Plaid-Verification header.")
	errPlaidMalformed     = httpx.Abort(http.StatusUnauthorized, "Malformed Plaid verification token.")
	errPlaidAlgorithm     = httpx.Abort(http.StatusUnauthorized, "Unsupported Plaid verification algorithm.")
	errPlaidSignature     = httpx.Abort(http.StatusUnauthorized, "Invalid Plaid webhook signature.")
	errPlaidBodyHash      = httpx.Abort(http.StatusUnauthorized, "Plaid webhook body hash mismatch.")
	errPlaidExpired       = httpx.Abort(http.StatusUnauthorized, "Plaid webhook verification token expired.")
)

// plaidKeyCache is PlaidWebhookKeyCache: keys by kid, never invalidated.
type plaidKeyCache struct {
	mu   sync.Mutex
	keys map[string]plaidJWK
}

func (c *plaidKeyCache) key(kid string, fetch func() (plaidJWK, error)) (plaidJWK, error) {
	c.mu.Lock()
	k, ok := c.keys[kid]
	c.mu.Unlock()
	if ok {
		return k, nil
	}
	k, err := fetch()
	if err != nil {
		return plaidJWK{}, err
	}
	c.mu.Lock()
	if c.keys == nil {
		c.keys = map[string]plaidJWK{}
	}
	c.keys[kid] = k
	c.mu.Unlock()
	return k, nil
}

// plaidMaxAge is the replay window (Plaid recommends 5 minutes).
const plaidMaxAge = 5 * 60

// base64URLDecode is PlaidWebhookVerifier.base64URLDecode.
func base64URLDecode(v string) ([]byte, bool) {
	s := strings.NewReplacer("-", "+", "_", "/").Replace(v)
	s += strings.Repeat("=", (4-len(s)%4)%4)
	if strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	out, err := base64.StdEncoding.DecodeString(s)
	return out, err == nil
}

// verifyPlaid is PlaidWebhookVerifier.verify: an ES256 JWS whose key is
// fetched by kid, whose iat is recent and whose request_body_sha256 claim
// matches the raw body.
func verifyPlaid(ctx context.Context, token string, rawBody []byte, client *plaidClient, cache *plaidKeyCache, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errPlaidMalformed
	}
	var header struct {
		Alg *string `json:"alg"`
		Kid *string `json:"kid"`
	}
	headerData, ok := base64URLDecode(parts[0])
	if !ok || json.Unmarshal(headerData, &header) != nil || header.Alg == nil || header.Kid == nil {
		return errPlaidMalformed
	}
	if *header.Alg != "ES256" {
		return errPlaidAlgorithm
	}
	jwk, err := cache.key(*header.Kid, func() (plaidJWK, error) { return client.webhookVerificationKey(ctx, *header.Kid) })
	if err != nil {
		return err
	}
	pub, err := plaidPublicKey(jwk)
	if err != nil {
		return err
	}
	sig, ok := base64URLDecode(parts[2])
	if !ok || len(sig) != 64 {
		return errPlaidMalformed
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return errPlaidSignature
	}
	var claims struct {
		Iat               json.RawMessage `json:"iat"`
		RequestBodySHA256 *string         `json:"request_body_sha256"`
	}
	payload, ok := base64URLDecode(parts[1])
	if !ok || json.Unmarshal(payload, &claims) != nil || claims.RequestBodySHA256 == nil {
		return errPlaidMalformed
	}
	iat, ok := swiftInt(claims.Iat)
	if !ok {
		return errPlaidMalformed
	}
	age := float64(now.UnixNano())/1e9 - float64(iat)
	if age > plaidMaxAge || age < -plaidMaxAge {
		return errPlaidExpired
	}
	sum := sha256.Sum256(rawBody)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(*claims.RequestBodySHA256))) != 1 {
		return errPlaidBodyHash
	}
	return nil
}

// errPlaidKey is the CryptoKit error P256.Signing.PublicKey(rawRepresentation:)
// threw for a point off the curve: not an AbortError, so a 500.
var errPlaidKey = errors.New("CryptoKitError.incorrectParameterSize")

func plaidPublicKey(jwk plaidJWK) (*ecdsa.PublicKey, error) {
	if jwk.Kty != "EC" || jwk.Crv != "P-256" {
		return nil, errPlaidMalformed
	}
	x, okX := base64URLDecode(jwk.X)
	y, okY := base64URLDecode(jwk.Y)
	if !okX || !okY || len(x) != 32 || len(y) != 32 {
		return nil, errPlaidMalformed
	}
	raw := append([]byte{4}, append(x, y...)...)
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, errPlaidKey
	}
	return key, nil
}

// plaidWebhook is PlaidWebhookController.receive (POST /webhooks/plaid).
func (h *handlers) plaidWebhook(c *echo.Context) error {
	rawBody, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	if h.plaid == nil {
		return errPlaidNotConfigured
	}
	token := strings.TrimFunc(c.Request().Header.Get("Plaid-Verification"), unicode.IsSpace)
	if token == "" {
		return errPlaidMissingHeader
	}
	ctx := c.Request().Context()
	if err := verifyPlaid(ctx, token, rawBody, h.plaid, h.keyCache, h.b.now()); err != nil {
		return err
	}
	var payload struct {
		WebhookType *string `json:"webhook_type"`
		WebhookCode *string `json:"webhook_code"`
		ItemID      *string `json:"item_id"`
	}
	if json.Unmarshal(rawBody, &payload) != nil || payload.WebhookType == nil || payload.WebhookCode == nil || payload.ItemID == nil {
		return httpx.BadRequest("Malformed Plaid webhook payload.")
	}
	conn, err := h.b.q.GetConnectionByItem(ctx, bankingdb.GetConnectionByItemParams{
		Provider: string(KindPlaid), ProviderItemID: *payload.ItemID,
	})
	if isNoRows(err) {
		// Unknown item: acknowledge so Plaid stops retrying.
		return c.NoContent(http.StatusOK)
	}
	if err != nil {
		return err
	}
	switch *payload.WebhookCode {
	case "SYNC_UPDATES_AVAILABLE", "INITIAL_UPDATE", "HISTORICAL_UPDATE", "DEFAULT_UPDATE":
		p, err := h.registry.Provider(KindPlaid)
		if err != nil {
			return err
		}
		if _, err := p.Sync(ctx, Call{}, &conn); err != nil {
			h.b.logger.Error("plaid.webhook.sync_failed",
				"connection_id", conn.ID.String(),
				"webhook_code", *payload.WebhookCode,
				"item_id", *payload.ItemID,
				"error", err.Error(),
			)
		}
	case "PENDING_EXPIRATION", "ERROR", "USER_PERMISSION_REVOKED":
		reauth := "reauth_required"
		if err := h.b.q.UpdateConnectionStatusError(ctx, bankingdb.UpdateConnectionStatusErrorParams{
			ID: conn.ID, Status: statusReauthRequired, LastSyncStatus: &reauth,
			LastSyncError: payload.WebhookCode, Now: h.b.now(),
		}); err != nil {
			return err
		}
	}
	return c.NoContent(http.StatusOK)
}

// swiftInt decodes a JSON number as Swift's Int: a whole number, never a
// string.
func swiftInt(raw json.RawMessage) (int64, bool) {
	t := strings.TrimSpace(string(raw))
	if t == "" || (t[0] != '-' && (t[0] < '0' || t[0] > '9')) {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	if i, err := n.Int64(); err == nil {
		return i, true
	}
	f, err := n.Float64()
	if err != nil || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}
