package banking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/bankingdb"

	"github.com/google/uuid"
)

// GoCardlessConfig is GoCardlessConfiguration.
type GoCardlessConfig struct {
	SecretID  string
	SecretKey string
	BaseURL   string
}

func goCardlessConfigFromEnv(s Settings) *GoCardlessConfig {
	id, key := trimmedNonEmpty(&s.GoCardlessSecretID), trimmedNonEmpty(&s.GoCardlessSecretKey)
	if id == nil || key == nil {
		return nil
	}
	base := "https://bankaccountdata.gocardless.com/api/v2"
	if b := trimmedNonEmpty(&s.GoCardlessAPIBase); b != nil {
		base = *b
	}
	return &GoCardlessConfig{SecretID: *id, SecretKey: *key, BaseURL: base}
}

// Wire models (camelCase on the wire, so backendAPI decodes them).

type gcTokenResponse struct {
	Access string `json:"access"`
}

type gcInstitution struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Bic       *string   `json:"bic"`
	Logo      *string   `json:"logo"`
	Countries *[]string `json:"countries"`
}

type gcAgreementResponse struct {
	ID string `json:"id"`
}

type gcRequisitionResponse struct {
	ID       string    `json:"id"`
	Link     *string   `json:"link"`
	Status   *string   `json:"status"`
	Accounts *[]string `json:"accounts"`
}

type gcAccountDetails struct {
	Account struct {
		Name      *string `json:"name"`
		OwnerName *string `json:"ownerName"`
		Currency  *string `json:"currency"`
		Iban      *string `json:"iban"`
		Product   *string `json:"product"`
	} `json:"account"`
}

type gcTransactionAmount struct {
	Amount   string  `json:"amount"`
	Currency *string `json:"currency"`
}

type gcTransaction struct {
	TransactionID                     *string             `json:"transactionId"`
	InternalTransactionID             *string             `json:"internalTransactionId"`
	BookingDate                       *string             `json:"bookingDate"`
	ValueDate                         *string             `json:"valueDate"`
	TransactionAmount                 gcTransactionAmount `json:"transactionAmount"`
	RemittanceInformationUnstructured *string             `json:"remittanceInformationUnstructured"`
	CreditorName                      *string             `json:"creditorName"`
	DebtorName                        *string             `json:"debtorName"`
}

type gcTransactionsResponse struct {
	Transactions struct {
		Booked  *[]gcTransaction `json:"booked"`
		Pending *[]gcTransaction `json:"pending"`
	} `json:"transactions"`
}

// goCardlessClient is GoCardlessClient (Bank Account Data, AIS only).
type goCardlessClient struct {
	config GoCardlessConfig
	b      *base
}

func (c *goCardlessClient) newAccessToken(ctx context.Context) (string, error) {
	var out gcTokenResponse
	body := map[string]string{"secret_id": c.config.SecretID, "secret_key": c.config.SecretKey}
	if err := c.do(ctx, http.MethodPost, "/token/new/", "/token/new/", "", body, &out); err != nil {
		return "", err
	}
	return out.Access, nil
}

func (c *goCardlessClient) institutions(ctx context.Context, country, token string) ([]gcInstitution, error) {
	var out []gcInstitution
	err := c.do(ctx, http.MethodGet, "/institutions/?country="+strings.ToUpper(country), "/institutions/", token, nil, &out)
	return out, err
}

func (c *goCardlessClient) createAgreement(ctx context.Context, institutionID, token string) (string, error) {
	body := map[string]any{
		"institution_id":        institutionID,
		"max_historical_days":   90,
		"access_valid_for_days": 90,
		"access_scope":          []string{"balances", "details", "transactions"},
	}
	var out gcAgreementResponse
	if err := c.do(ctx, http.MethodPost, "/agreements/enduser/", "/agreements/enduser/", token, body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *goCardlessClient) createRequisition(ctx context.Context, institutionID, redirect, agreementID, reference, token string) (gcRequisitionResponse, error) {
	body := map[string]string{
		"institution_id": institutionID, "redirect": redirect, "agreement": agreementID, "reference": reference,
	}
	var out gcRequisitionResponse
	err := c.do(ctx, http.MethodPost, "/requisitions/", "/requisitions/", token, body, &out)
	return out, err
}

func (c *goCardlessClient) requisition(ctx context.Context, id, token string) (gcRequisitionResponse, error) {
	var out gcRequisitionResponse
	err := c.do(ctx, http.MethodGet, "/requisitions/"+id+"/", "/requisitions/{id}/", token, nil, &out)
	return out, err
}

func (c *goCardlessClient) deleteRequisition(ctx context.Context, id, token string) error {
	// Already gone (404) is the outcome we wanted.
	return c.do(ctx, http.MethodDelete, "/requisitions/"+id+"/", "/requisitions/{id}/", token, nil, nil)
}

func (c *goCardlessClient) accountDetails(ctx context.Context, accountID, token string) (gcAccountDetails, error) {
	var out gcAccountDetails
	err := c.do(ctx, http.MethodGet, "/accounts/"+accountID+"/details/", "/accounts/{id}/details/", token, nil, &out)
	return out, err
}

func (c *goCardlessClient) transactions(ctx context.Context, accountID, token string) (gcTransactionsResponse, error) {
	var out gcTransactionsResponse
	err := c.do(ctx, http.MethodGet, "/accounts/"+accountID+"/transactions/", "/accounts/{id}/transactions/", token, nil, &out)
	return out, err
}

// do sends one request. A non-2xx is a 502 (except a DELETE's 404, which
// out == nil marks); a 2xx body is decoded through backendAPI.
func (c *goCardlessClient) do(ctx context.Context, method, path, logPath, token string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.config.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if out == nil && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Length only: GoCardless error bodies can echo account details.
		c.b.logger.Warn(fmt.Sprintf("GoCardless %s failed status=%d body_bytes=%d", logPath, resp.StatusCode, len(data)))
		return httpx.Abort(http.StatusBadGateway, "GoCardless request failed.")
	}
	if out == nil {
		return nil
	}
	return swiftjson.Decode(data, out)
}

// GoCardlessProvider is the GoCardless Bank Account Data driver (EU).
type GoCardlessProvider struct {
	client *goCardlessClient
	b      *base
}

// gcDailyAccountCallBudget is the PSD2 cap on pulls per account per day.
const gcDailyAccountCallBudget = 4

// Kind implements Provider.
func (*GoCardlessProvider) Kind() Kind { return KindGoCardless }

// CreateLinkSession implements Provider (hosted links only).
func (*GoCardlessProvider) CreateLinkSession(context.Context, Call, uuid.UUID) (LinkSessionResponse, error) {
	return LinkSessionResponse{}, errUnsupported()
}

// Exchange implements Provider (hosted links only).
func (*GoCardlessProvider) Exchange(context.Context, Call, ExchangeRequest, uuid.UUID) (*bankingdb.BankConnection, error) {
	return nil, errUnsupported()
}

// ListInstitutions implements Provider.
func (p *GoCardlessProvider) ListInstitutions(ctx context.Context, _ Call, country string) ([]InstitutionResponse, error) {
	token, err := p.client.newAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	list, err := p.client.institutions(ctx, country, token)
	if err != nil {
		return nil, err
	}
	out := make([]InstitutionResponse, 0, len(list))
	upper := strings.ToUpper(country)
	for _, i := range list {
		out = append(out, InstitutionResponse{ID: i.ID, Name: i.Name, Bic: i.Bic, LogoURL: i.Logo, Country: &upper})
	}
	return out, nil
}

// CreateHostedLink implements Provider: agreement, requisition, flow.
func (p *GoCardlessProvider) CreateHostedLink(ctx context.Context, call Call, userID uuid.UUID, institutionID, redirectURI string) (LinkSessionResponse, error) {
	token, err := p.client.newAccessToken(ctx)
	if err != nil {
		return LinkSessionResponse{}, err
	}
	agreementID, err := p.client.createAgreement(ctx, institutionID, token)
	if err != nil {
		return LinkSessionResponse{}, err
	}
	reference := randomReference(24)
	callback, err := callbackURL(call, "/v1/banks/gocardless/callback")
	if err != nil {
		return LinkSessionResponse{}, err
	}
	requisition, err := p.client.createRequisition(ctx, institutionID, callback, agreementID, reference, token)
	if err != nil {
		return LinkSessionResponse{}, err
	}
	now := p.b.now()
	expiresAt := now.Add(1800 * time.Second)
	if err := p.b.q.InsertLinkFlow(ctx, bankingdb.InsertLinkFlowParams{
		ID: uuid.New(), UserID: userID, Provider: string(KindGoCardless), Reference: reference,
		RequisitionID: requisition.ID, InstitutionID: &institutionID, AppRedirectUri: redirectURI,
		ExpiresAt: expiresAt, Now: now,
	}); err != nil {
		return LinkSessionResponse{}, err
	}
	return LinkSessionResponse{Provider: KindGoCardless, HostedURL: requisition.Link, Expiration: ptr(apijson.NewTime(expiresAt))}, nil
}

func errInvalidLink() error { return httpx.BadRequest("Bank link is invalid or expired.") }

func (p *GoCardlessProvider) existingConnection(ctx context.Context, flow bankingdb.BankLinkFlow) (*bankingdb.BankConnection, error) {
	c, err := p.b.q.GetConnectionByItem(ctx, bankingdb.GetConnectionByItemParams{
		Provider: string(KindGoCardless), ProviderItemID: flow.RequisitionID,
	})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CompleteHostedLink implements Provider.
func (p *GoCardlessProvider) CompleteHostedLink(ctx context.Context, call Call, reference string) (*bankingdb.BankConnection, error) {
	now := p.b.now()
	flow, err := p.b.q.GetLinkFlow(ctx, bankingdb.GetLinkFlowParams{Reference: reference, Provider: string(KindGoCardless)})
	if err != nil && !isNoRows(err) {
		return nil, err
	}
	if isNoRows(err) || !flow.ExpiresAt.After(now) {
		return nil, errInvalidLink()
	}
	if flow.UsedAt != nil {
		existing, err := p.existingConnection(ctx, flow)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}
	token, err := p.client.newAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	requisition, err := p.client.requisition(ctx, flow.RequisitionID, token)
	if err != nil {
		return nil, err
	}
	accountIDs := deref(requisition.Accounts)
	if len(accountIDs) == 0 {
		return nil, httpx.BadRequest("No accounts were shared for this bank.")
	}
	existing, err := p.existingConnection(ctx, flow)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := p.ensureAccounts(ctx, accountIDs, existing.ID, token); err != nil {
			return nil, err
		}
		if err := p.markUsed(ctx, &flow, now); err != nil {
			return nil, err
		}
		_, _ = p.Sync(ctx, call, existing)
		return existing, nil
	}
	if flow.UsedAt != nil {
		return nil, errInvalidLink()
	}
	conn := &bankingdb.BankConnection{
		UserID: flow.UserID, Provider: string(KindGoCardless), InstitutionID: flow.InstitutionID,
		InstitutionName: flow.InstitutionID, ProviderItemID: flow.RequisitionID, Status: statusActive,
		ConsentExpiresAt: ptr(now.Add(90 * 86400 * time.Second)),
	}
	if err := p.b.insertConnection(ctx, conn); err != nil {
		return nil, err
	}
	if err := p.ensureAccounts(ctx, accountIDs, conn.ID, token); err != nil {
		return nil, err
	}
	if err := p.markUsed(ctx, &flow, now); err != nil {
		return nil, err
	}
	_, _ = p.Sync(ctx, call, conn)
	return conn, nil
}

func (p *GoCardlessProvider) markUsed(ctx context.Context, flow *bankingdb.BankLinkFlow, at time.Time) error {
	if flow.UsedAt != nil {
		return nil
	}
	flow.UsedAt = &at
	return p.b.q.MarkLinkFlowUsed(ctx, bankingdb.MarkLinkFlowUsedParams{ID: flow.ID, UsedAt: at})
}

func (p *GoCardlessProvider) ensureAccounts(ctx context.Context, accountIDs []string, connectionID uuid.UUID, token string) error {
	existing, err := p.b.q.ListConnectionAccounts(ctx, connectionID)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for i := range existing {
		have[existing[i].ProviderAccountID] = true
	}
	for _, id := range accountIDs {
		if have[id] {
			continue
		}
		name := "Account"
		var currency *string
		// Details are best effort (`try?`): a failure names it "Account".
		if details, err := p.client.accountDetails(ctx, id, token); err == nil {
			switch {
			case details.Account.Name != nil:
				name = *details.Account.Name
			case details.Account.OwnerName != nil:
				name = *details.Account.OwnerName
			}
			currency = details.Account.Currency
		}
		account := bankingdb.BankAccount{ConnectionID: connectionID, ProviderAccountID: id, Name: name, Currency: currency}
		if err := p.b.insertAccount(ctx, &account); err != nil {
			return err
		}
	}
	return nil
}

// Sync implements Provider: polls each account within the daily budget.
func (p *GoCardlessProvider) Sync(ctx context.Context, _ Call, conn *bankingdb.BankConnection) (SyncResult, error) {
	if conn.ConsentExpiresAt != nil && !conn.ConsentExpiresAt.After(p.b.now()) {
		if err := p.b.markReauth(ctx, conn, "consent_expired"); err != nil {
			return SyncResult{}, err
		}
		return SyncResult{}, httpx.Forbidden("Bank consent expired. Reconnect to keep syncing.")
	}
	token, err := p.client.newAccessToken(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	accounts, err := p.b.q.ListConnectionAccounts(ctx, conn.ID)
	if err != nil {
		return SyncResult{}, err
	}
	var result SyncResult
	for i := range accounts {
		account := &accounts[i]
		allowed, err := p.b.consumeDailyBudget(ctx, "gocardless:budget:"+account.ProviderAccountID, gcDailyAccountCallBudget)
		if err != nil {
			return SyncResult{}, err
		}
		if !allowed {
			p.b.logger.Info("gocardless daily budget exhausted account=" + account.ProviderAccountID)
			continue
		}
		payload, err := p.client.transactions(ctx, account.ProviderAccountID, token)
		if err != nil {
			return SyncResult{}, err
		}
		for _, group := range []struct {
			txs     []gcTransaction
			pending bool
		}{{deref(payload.Transactions.Booked), false}, {deref(payload.Transactions.Pending), true}} {
			for _, tx := range group.txs {
				added, err := p.upsert(ctx, tx, group.pending, account, conn.UserID)
				if err != nil {
					return SyncResult{}, err
				}
				if added {
					result.Added++
				} else {
					result.Modified++
				}
			}
		}
	}
	if err := p.b.markSynced(ctx, conn); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// Disconnect implements Provider (deletes the requisition).
func (p *GoCardlessProvider) Disconnect(ctx context.Context, _ Call, conn *bankingdb.BankConnection) error {
	token, err := p.client.newAccessToken(ctx)
	if err != nil {
		return err
	}
	return p.client.deleteRequisition(ctx, conn.ProviderItemID, token)
}

// swiftDouble is Double(String): nil (0 here) on anything unparsable.
func swiftDouble(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// upsert reports whether a row was inserted.
func (p *GoCardlessProvider) upsert(ctx context.Context, tx gcTransaction, pending bool, account *bankingdb.BankAccount, userID uuid.UUID) (bool, error) {
	amount := swiftDouble(tx.TransactionAmount.Amount)
	dateString := ""
	switch {
	case tx.BookingDate != nil:
		dateString = *tx.BookingDate
	case tx.ValueDate != nil:
		dateString = *tx.ValueDate
	}
	occurredOn, ok := parseDay(dateString)
	if !ok {
		occurredOn = p.b.now()
	}
	merchant := tx.CreditorName
	if merchant == nil {
		merchant = tx.DebtorName
	}
	if merchant == nil {
		merchant = tx.RemittanceInformationUnstructured
	}
	accountKey := apijson.FormatUUID(account.ID)
	hash := dedupeHash(accountKey, dateString, amount, deref(merchant))
	// GoCardless ids are unstable at some banks; fall back to the hash.
	providerTxID := hash
	switch {
	case tx.TransactionID != nil:
		providerTxID = *tx.TransactionID
	case tx.InternalTransactionID != nil:
		providerTxID = *tx.InternalTransactionID
	}
	update := func(existing bankingdb.BankTransaction) error {
		existing.ProviderTxID, existing.Amount, existing.Currency = providerTxID, amount, tx.TransactionAmount.Currency
		existing.OccurredOn, existing.Merchant, existing.DescriptionText = occurredOn, merchant, tx.RemittanceInformationUnstructured
		existing.Pending, existing.DedupeHash = pending, hash
		return p.b.updateTransaction(ctx, &existing)
	}

	existing, err := p.b.q.GetAccountTransaction(ctx, bankingdb.GetAccountTransactionParams{AccountID: account.ID, ProviderTxID: providerTxID})
	if err == nil {
		if existing.Status == txSuggested {
			return false, update(existing)
		}
		return false, nil
	}
	if !isNoRows(err) {
		return false, err
	}
	match, err := p.pendingMatchForBooked(ctx, tx, account.ID, hash, pending)
	if err != nil {
		return false, err
	}
	if match != nil {
		if match.Status == txSuggested {
			return false, update(*match)
		}
		return false, nil
	}
	row := bankingdb.BankTransaction{
		AccountID: account.ID, UserID: userID, ProviderTxID: providerTxID, DedupeHash: hash, Amount: amount,
		Currency: tx.TransactionAmount.Currency, OccurredOn: occurredOn, Merchant: merchant,
		DescriptionText: tx.RemittanceInformationUnstructured, Pending: pending, Status: txSuggested,
	}
	return true, p.b.insertTransaction(ctx, &row)
}

// pendingMatchForBooked finds the pending row a booked transaction replaces:
// by internal id, else by a unique dedupe-hash match.
func (p *GoCardlessProvider) pendingMatchForBooked(ctx context.Context, tx gcTransaction, accountID uuid.UUID, hash string, pending bool) (*bankingdb.BankTransaction, error) {
	if pending {
		return nil, nil
	}
	if tx.InternalTransactionID != nil && *tx.InternalTransactionID != "" {
		existing, err := p.b.q.GetPendingAccountTransaction(ctx, bankingdb.GetPendingAccountTransactionParams{
			AccountID: accountID, ProviderTxID: *tx.InternalTransactionID,
		})
		if err == nil {
			return &existing, nil
		}
		if !isNoRows(err) {
			return nil, err
		}
	}
	matches, err := p.b.q.ListPendingByHash(ctx, bankingdb.ListPendingByHashParams{AccountID: accountID, DedupeHash: hash})
	if err != nil {
		return nil, err
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	return nil, nil
}
