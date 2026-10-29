package banking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/bankingdb"

	"github.com/google/uuid"
)

// PlaidConfig is PlaidConfiguration.
type PlaidConfig struct {
	ClientID   string
	Secret     string
	BaseURL    string
	WebhookURL *string
}

// plaidConfigFromEnv is PlaidConfiguration.fromEnvironment().
func plaidConfigFromEnv(s Settings) *PlaidConfig {
	id, secret := trimmedNonEmpty(&s.PlaidClientID), trimmedNonEmpty(&s.PlaidSecret)
	if id == nil || secret == nil {
		return nil
	}
	env := "sandbox"
	if e := trimmedNonEmpty(&s.PlaidEnv); e != nil {
		env = strings.ToLower(*e)
	}
	host := "https://sandbox.plaid.com"
	switch env {
	case "production":
		host = "https://production.plaid.com"
	case "development":
		host = "https://development.plaid.com"
	}
	return &PlaidConfig{ClientID: *id, Secret: *secret, BaseURL: host, WebhookURL: trimmedNonEmpty(&s.PlaidWebhookURL)}
}

// Wire models. Vapor decoded every Plaid response with the app-wide
// JSONDecoder.backendAPI, which rewrites snake_case keys before the
// CodingKeys below are looked up (see plaidClient.post).

type plaidJWK struct {
	Alg string `json:"alg"`
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type plaidWebhookKeyResponse struct {
	Key plaidJWK `json:"key"`
}

type plaidLinkTokenResponse struct {
	LinkToken  string  `json:"link_token"`
	Expiration *string `json:"expiration"`
}

type plaidExchangeResponse struct {
	AccessToken string `json:"access_token"`
	ItemID      string `json:"item_id"`
}

type plaidBalances struct {
	Current         *float64 `json:"current"`
	IsoCurrencyCode *string  `json:"iso_currency_code"`
}

type plaidAccount struct {
	AccountID string         `json:"account_id"`
	Name      string         `json:"name"`
	Mask      *string        `json:"mask"`
	Type      *string        `json:"type"`
	Balances  *plaidBalances `json:"balances"`
}

type plaidAccountsResponse struct {
	Accounts []plaidAccount `json:"accounts"`
}

type plaidTransaction struct {
	TransactionID   string    `json:"transaction_id"`
	AccountID       string    `json:"account_id"`
	Amount          float64   `json:"amount"`
	IsoCurrencyCode *string   `json:"iso_currency_code"`
	Date            string    `json:"date"`
	Name            *string   `json:"name"`
	MerchantName    *string   `json:"merchant_name"`
	Pending         bool      `json:"pending"`
	Category        *[]string `json:"category"`
}

type plaidRemovedTransaction struct {
	TransactionID string `json:"transaction_id"`
}

type plaidSyncResponse struct {
	Added      []plaidTransaction        `json:"added"`
	Modified   []plaidTransaction        `json:"modified"`
	Removed    []plaidRemovedTransaction `json:"removed"`
	NextCursor string                    `json:"next_cursor"`
	HasMore    bool                      `json:"has_more"`
}

type plaidEmptyResponse struct{}

// plaidClient is PlaidClient: read-only (transactions + balances).
type plaidClient struct {
	config PlaidConfig
	b      *base
}

func (c *plaidClient) createLinkToken(ctx context.Context, userID uuid.UUID) (plaidLinkTokenResponse, error) {
	body := map[string]any{
		"user":          map[string]string{"client_user_id": apijson.FormatUUID(userID)},
		"client_name":   "Norviq",
		"products":      []string{"transactions"},
		"country_codes": []string{"US"},
		"language":      "en",
	}
	if c.config.WebhookURL != nil {
		body["webhook"] = *c.config.WebhookURL
	}
	var out plaidLinkTokenResponse
	err := c.post(ctx, "/link/token/create", body, &out)
	return out, err
}

func (c *plaidClient) exchangePublicToken(ctx context.Context, publicToken string) (plaidExchangeResponse, error) {
	var out plaidExchangeResponse
	err := c.post(ctx, "/item/public_token/exchange", map[string]any{"public_token": publicToken}, &out)
	return out, err
}

func (c *plaidClient) accounts(ctx context.Context, accessToken string) (plaidAccountsResponse, error) {
	var out plaidAccountsResponse
	err := c.post(ctx, "/accounts/get", map[string]any{"access_token": accessToken}, &out)
	return out, err
}

func (c *plaidClient) transactionsSync(ctx context.Context, accessToken string, cursor *string) (plaidSyncResponse, error) {
	body := map[string]any{"access_token": accessToken}
	if cursor != nil {
		body["cursor"] = *cursor
	}
	var out plaidSyncResponse
	err := c.post(ctx, "/transactions/sync", body, &out)
	return out, err
}

func (c *plaidClient) removeItem(ctx context.Context, accessToken string) error {
	var out plaidEmptyResponse
	return c.post(ctx, "/item/remove", map[string]any{"access_token": accessToken}, &out)
}

func (c *plaidClient) webhookVerificationKey(ctx context.Context, keyID string) (plaidJWK, error) {
	var out plaidWebhookKeyResponse
	err := c.post(ctx, "/webhook_verification_key/get", map[string]any{"key_id": keyID}, &out)
	return out.Key, err
}

// post sends body plus the credentials. A non-200 is a 502; the body is
// then decoded as Vapor's `response.content.decode` did, through
// JSONDecoder.backendAPI. That decoder rewrites snake_case keys before the
// explicit snake_case CodingKeys are looked up, so every Plaid response with
// a required snake_case field fails to decode (a 400 DecodingError) — a
// Vapor bug ported as is; swiftjson.Decode reproduces the rewrite.
func (c *plaidClient) post(ctx context.Context, path string, body map[string]any, out any) error {
	body["client_id"] = c.config.ClientID
	body["secret"] = c.config.Secret
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// Length only: Plaid error bodies can echo request fields.
		c.b.logger.Warn(fmt.Sprintf("Plaid %s failed status=%d body_bytes=%d", path, resp.StatusCode, len(data)))
		return httpx.Abort(http.StatusBadGateway, "Plaid request failed.")
	}
	return swiftjson.Decode(data, out)
}

// PlaidProvider is the Plaid driver.
type PlaidProvider struct {
	client *plaidClient
	vault  *crypto.Vault
	b      *base
}

// Kind implements Provider.
func (*PlaidProvider) Kind() Kind { return KindPlaid }

// CreateLinkSession implements Provider.
func (p *PlaidProvider) CreateLinkSession(ctx context.Context, _ Call, userID uuid.UUID) (LinkSessionResponse, error) {
	token, err := p.client.createLinkToken(ctx, userID)
	if err != nil {
		return LinkSessionResponse{}, err
	}
	var expiration *apijson.Time
	if token.Expiration != nil {
		if t, err := time.Parse(time.RFC3339, *token.Expiration); err == nil && !strings.Contains(*token.Expiration, ".") {
			expiration = ptr(apijson.NewTime(t))
		}
	}
	return LinkSessionResponse{Provider: KindPlaid, LinkToken: &token.LinkToken, Expiration: expiration}, nil
}

// Exchange implements Provider.
func (p *PlaidProvider) Exchange(ctx context.Context, _ Call, req ExchangeRequest, userID uuid.UUID) (*bankingdb.BankConnection, error) {
	exchanged, err := p.client.exchangePublicToken(ctx, req.PublicToken)
	if err != nil {
		return nil, err
	}
	encrypted, err := p.vault.Encrypt(exchanged.AccessToken, crypto.ContextBankPlaid)
	if err != nil {
		return nil, err
	}
	conn := &bankingdb.BankConnection{
		UserID: userID, Provider: string(KindPlaid), InstitutionID: req.InstitutionID,
		InstitutionName: req.InstitutionName, ProviderItemID: exchanged.ItemID,
		AccessTokenEnc: &encrypted, Status: statusActive,
	}
	if err := p.b.insertConnection(ctx, conn); err != nil {
		return nil, err
	}
	accounts, err := p.client.accounts(ctx, exchanged.AccessToken)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts.Accounts {
		if err := p.upsertAccount(ctx, a, conn.ID); err != nil {
			return nil, err
		}
	}
	return conn, nil
}

func (p *PlaidProvider) accountsByProviderID(ctx context.Context, connectionID uuid.UUID) (map[string]bankingdb.BankAccount, error) {
	rows, err := p.b.q.ListConnectionAccounts(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bankingdb.BankAccount, len(rows))
	for i := range rows {
		out[rows[i].ProviderAccountID] = rows[i]
	}
	return out, nil
}

// Sync implements Provider (/transactions/sync with the stored cursor).
func (p *PlaidProvider) Sync(ctx context.Context, _ Call, conn *bankingdb.BankConnection) (SyncResult, error) {
	if conn.AccessTokenEnc == nil {
		return SyncResult{}, errNotConfigured(KindPlaid)
	}
	accessToken, err := p.vault.Decrypt(*conn.AccessTokenEnc, crypto.ContextBankPlaid)
	if err != nil {
		return SyncResult{}, err
	}
	byID, err := p.accountsByProviderID(ctx, conn.ID)
	if err != nil {
		return SyncResult{}, err
	}
	var result SyncResult
	cursor := conn.SyncCursor
	for hasMore := true; hasMore; {
		page, err := p.client.transactionsSync(ctx, accessToken, cursor)
		if err != nil {
			return SyncResult{}, err
		}
		for _, tx := range append(page.Added, page.Modified...) {
			account, ok := byID[tx.AccountID]
			if !ok {
				// A new account appeared mid-sync: fetch and cache it. Swift
				// then skips this transaction either way (both branches
				// `continue`), so it is only picked up by a later change.
				refreshed, err := p.client.accounts(ctx, accessToken)
				if err != nil {
					return SyncResult{}, err
				}
				for _, a := range refreshed.Accounts {
					if err := p.upsertAccount(ctx, a, conn.ID); err != nil {
						return SyncResult{}, err
					}
				}
				if byID, err = p.accountsByProviderID(ctx, conn.ID); err != nil {
					return SyncResult{}, err
				}
				continue
			}
			added, err := p.upsertTransaction(ctx, tx, account, conn.UserID)
			if err != nil {
				return SyncResult{}, err
			}
			if added {
				result.Added++
			} else {
				result.Modified++
			}
		}
		for _, removed := range page.Removed {
			existing, err := p.b.q.GetUserTransactionByProviderTx(ctx, bankingdb.GetUserTransactionByProviderTxParams{
				ProviderTxID: removed.TransactionID, UserID: conn.UserID,
			})
			if isNoRows(err) {
				continue
			}
			if err != nil {
				return SyncResult{}, err
			}
			if existing.Status == txSuggested {
				if err := p.b.q.DeleteTransaction(ctx, existing.ID); err != nil {
					return SyncResult{}, err
				}
				result.Removed++
			}
		}
		cursor = &page.NextCursor
		hasMore = page.HasMore
	}
	now := p.b.now()
	ok := "ok"
	conn.SyncCursor, conn.LastSyncedAt, conn.LastSyncStatus, conn.LastSyncError, conn.Status, conn.UpdatedAt =
		cursor, &now, &ok, nil, statusActive, &now
	if err := p.b.q.UpdateConnectionPlaidSynced(ctx, bankingdb.UpdateConnectionPlaidSyncedParams{
		ID: conn.ID, SyncCursor: cursor, Now: now,
	}); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// Disconnect implements Provider (/item/remove).
func (p *PlaidProvider) Disconnect(ctx context.Context, _ Call, conn *bankingdb.BankConnection) error {
	if conn.AccessTokenEnc == nil {
		return nil
	}
	accessToken, err := p.vault.Decrypt(*conn.AccessTokenEnc, crypto.ContextBankPlaid)
	if err != nil {
		return err
	}
	return p.client.removeItem(ctx, accessToken)
}

// ListInstitutions implements Provider: Plaid selects inside its SDK.
func (*PlaidProvider) ListInstitutions(context.Context, Call, string) ([]InstitutionResponse, error) {
	return nil, errUnsupported()
}

// CreateHostedLink implements Provider.
func (*PlaidProvider) CreateHostedLink(context.Context, Call, uuid.UUID, string, string) (LinkSessionResponse, error) {
	return LinkSessionResponse{}, errUnsupported()
}

// CompleteHostedLink implements Provider.
func (*PlaidProvider) CompleteHostedLink(context.Context, Call, string) (*bankingdb.BankConnection, error) {
	return nil, errUnsupported()
}

func (p *PlaidProvider) upsertAccount(ctx context.Context, a plaidAccount, connectionID uuid.UUID) error {
	row, err := p.b.q.GetAccountByProviderID(ctx, bankingdb.GetAccountByProviderIDParams{
		ConnectionID: connectionID, ProviderAccountID: a.AccountID,
	})
	if err != nil && !isNoRows(err) {
		return err
	}
	if isNoRows(err) {
		row = bankingdb.BankAccount{ConnectionID: connectionID, ProviderAccountID: a.AccountID}
	}
	now := p.b.now()
	row.Name, row.Mask, row.Type = a.Name, a.Mask, a.Type
	row.Currency, row.Balance = nil, nil
	if a.Balances != nil {
		row.Currency, row.Balance = a.Balances.IsoCurrencyCode, a.Balances.Current
	}
	row.BalanceAsOf = &now
	return p.b.saveAccount(ctx, &row)
}

// upsertTransaction reports whether a row was inserted.
func (p *PlaidProvider) upsertTransaction(ctx context.Context, tx plaidTransaction, account bankingdb.BankAccount, userID uuid.UUID) (bool, error) {
	occurredOn, ok := parseDay(tx.Date)
	if !ok {
		occurredOn = p.b.now()
	}
	merchant := tx.MerchantName
	if merchant == nil {
		merchant = tx.Name
	}
	hash := plaidDedupeHash(account.ID, tx.Date, tx.Amount, deref(merchant))
	var category *string
	if tx.Category != nil {
		category = ptr(strings.Join(*tx.Category, " / "))
	}
	existing, err := p.b.q.GetAccountTransaction(ctx, bankingdb.GetAccountTransactionParams{
		AccountID: account.ID, ProviderTxID: tx.TransactionID,
	})
	if err == nil {
		// Never overwrite a row the user already acted on.
		if existing.Status == txSuggested {
			existing.Amount, existing.Currency, existing.OccurredOn = tx.Amount, tx.IsoCurrencyCode, occurredOn
			existing.Merchant, existing.DescriptionText, existing.Pending = merchant, tx.Name, tx.Pending
			existing.ProviderCategory, existing.DedupeHash = category, hash
			if err := p.b.updateTransaction(ctx, &existing); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if !isNoRows(err) {
		return false, err
	}
	row := bankingdb.BankTransaction{
		AccountID: account.ID, UserID: userID, ProviderTxID: tx.TransactionID, DedupeHash: hash,
		Amount: tx.Amount, Currency: tx.IsoCurrencyCode, OccurredOn: occurredOn, Merchant: merchant,
		DescriptionText: tx.Name, Pending: tx.Pending, Status: txSuggested, ProviderCategory: category,
	}
	return true, p.b.insertTransaction(ctx, &row)
}

// plaidDedupeHash is PlaidProvider.dedupeHash.
func plaidDedupeHash(accountID uuid.UUID, date string, amount float64, description string) string {
	return dedupeHash(apijson.FormatUUID(accountID), date, amount, description)
}
