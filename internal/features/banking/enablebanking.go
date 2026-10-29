package banking

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/bankingdb"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// EnableBankingConfig is EnableBankingConfiguration.
type EnableBankingConfig struct {
	// ApplicationID is the Control Panel application id, sent as the JWT kid.
	ApplicationID string
	// PrivateKeyPEM is the RSA key whose certificate the application holds.
	PrivateKeyPEM string
	BaseURL       string
	// RedirectURL overrides the callback URL derived from the request host.
	RedirectURL *string
}

func enableBankingConfigFromEnv(s Settings) *EnableBankingConfig {
	id, key := trimmedNonEmpty(&s.EnableBankingAppID), trimmedNonEmpty(&s.EnableBankingPrivateKeyPEM)
	if id == nil || key == nil {
		return nil
	}
	base := "https://api.enablebanking.com"
	if b := trimmedNonEmpty(&s.EnableBankingAPIBase); b != nil {
		base = *b
	}
	return &EnableBankingConfig{
		ApplicationID: *id, PrivateKeyPEM: strings.ReplaceAll(*key, `\n`, "\n"),
		BaseURL: base, RedirectURL: trimmedNonEmpty(&s.EnableBankingRedirectURL),
	}
}

// Wire models. Enable Banking responses are decoded with a plain
// JSONDecoder (snake_case keys matched as written).

type ebASPSP struct {
	Name                   string  `json:"name"`
	Country                string  `json:"country"`
	Logo                   *string `json:"logo"`
	Bic                    *string `json:"bic"`
	Beta                   *bool   `json:"beta"`
	MaximumConsentValidity *int    `json:"maximum_consent_validity"`
}

type ebASPSPsResponse struct {
	ASPSPs []ebASPSP `json:"aspsps"`
}

type ebAuthResponse struct {
	URL             string  `json:"url"`
	AuthorizationID *string `json:"authorization_id"`
}

type ebSessionAccount struct {
	UID                string  `json:"uid"`
	IdentificationHash *string `json:"identification_hash"`
	Name               *string `json:"name"`
	Product            *string `json:"product"`
	Currency           *string `json:"currency"`
	CashAccountType    *string `json:"cash_account_type"`
	AccountID          *struct {
		Iban *string `json:"iban"`
	} `json:"account_id"`
}

type ebSessionResponse struct {
	SessionID string             `json:"session_id"`
	Accounts  []ebSessionAccount `json:"accounts"`
	ASPSP     *struct {
		Name    string `json:"name"`
		Country string `json:"country"`
	} `json:"aspsp"`
	Access *struct {
		ValidUntil *string `json:"valid_until"`
	} `json:"access"`
}

type ebParty struct {
	Name *string `json:"name"`
}

type ebTransaction struct {
	EntryReference    *string `json:"entry_reference"`
	TransactionAmount struct {
		Amount   string  `json:"amount"`
		Currency *string `json:"currency"`
	} `json:"transaction_amount"`
	CreditDebitIndicator  string    `json:"credit_debit_indicator"`
	Status                string    `json:"status"`
	BookingDate           *string   `json:"booking_date"`
	ValueDate             *string   `json:"value_date"`
	TransactionDate       *string   `json:"transaction_date"`
	Creditor              *ebParty  `json:"creditor"`
	Debtor                *ebParty  `json:"debtor"`
	RemittanceInformation *[]string `json:"remittance_information"`
	MerchantCategoryCode  *string   `json:"merchant_category_code"`
}

type ebTransactionsResponse struct {
	Transactions    []ebTransaction `json:"transactions"`
	ContinuationKey *string         `json:"continuation_key"`
}

// EnableBankingAPIError is a non-2xx answer carrying Enable Banking's
// machine-readable `error` code. It answers 502.
type EnableBankingAPIError struct {
	StatusCode int
	Code       *string
}

func (e *EnableBankingAPIError) Error() string { return "Enable Banking request failed." }

// Unwrap renders it as the AbortError it was: 502 with a fixed reason.
func (e *EnableBankingAPIError) Unwrap() error {
	return httpx.Abort(http.StatusBadGateway, "Enable Banking request failed.")
}

// RequiresReauth reports that the bank consent is gone.
func (e *EnableBankingAPIError) RequiresReauth() bool {
	if e.Code == nil {
		return false
	}
	switch *e.Code {
	case "EXPIRED_SESSION", "CLOSED_SESSION", "REVOKED_SESSION", "SESSION_DOES_NOT_EXIST":
		return true
	}
	return false
}

// enableBankingClient is EnableBankingClient: account information only.
type enableBankingClient struct {
	config EnableBankingConfig
	b      *base
}

func (c *enableBankingClient) aspsps(ctx context.Context, country string) ([]ebASPSP, error) {
	var out ebASPSPsResponse
	path := "/aspsps?country=" + strings.ToUpper(country) + "&psu_type=personal&service=AIS"
	if err := c.send(ctx, http.MethodGet, path, "/aspsps", nil, &out); err != nil {
		return nil, err
	}
	return out.ASPSPs, nil
}

func (c *enableBankingClient) startAuthorization(ctx context.Context, aspspName, country string, validUntil time.Time, state, redirectURL string) (ebAuthResponse, error) {
	body := map[string]any{
		"access":       map[string]string{"valid_until": ebTimestamp(validUntil)},
		"aspsp":        map[string]string{"name": aspspName, "country": strings.ToUpper(country)},
		"state":        state,
		"redirect_url": redirectURL,
		"psu_type":     "personal",
	}
	var out ebAuthResponse
	err := c.send(ctx, http.MethodPost, "/auth", "/auth", body, &out)
	return out, err
}

func (c *enableBankingClient) createSession(ctx context.Context, code string) (ebSessionResponse, error) {
	var out ebSessionResponse
	err := c.send(ctx, http.MethodPost, "/sessions", "/sessions", map[string]string{"code": code}, &out)
	return out, err
}

func (c *enableBankingClient) deleteSession(ctx context.Context, id string) error {
	err := c.send(ctx, http.MethodDelete, "/sessions/"+id, "/sessions/{id}", nil, nil)
	var apiErr *EnableBankingAPIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || apiErr.RequiresReauth()) {
		// Already closed or expired is the outcome we wanted.
		return nil
	}
	return err
}

// transactions fetches every page: only a null (or empty) continuation key
// ends the loop, since an empty page may still carry one.
func (c *enableBankingClient) transactions(ctx context.Context, accountUID string, dateFrom *string) ([]ebTransaction, error) {
	all := []ebTransaction{}
	var continuation *string
	for {
		q := []string{}
		if dateFrom != nil {
			q = append(q, "date_from="+queryEscape(*dateFrom))
		} else {
			// No history yet: ask for as much as the bank will give.
			q = append(q, "strategy=longest")
		}
		if continuation != nil {
			q = append(q, "continuation_key="+queryEscape(*continuation))
		}
		path := "/accounts/" + accountUID + "/transactions?" + strings.Join(q, "&")
		var page ebTransactionsResponse
		if err := c.send(ctx, http.MethodGet, path, "/accounts/{uid}/transactions", nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Transactions...)
		continuation = nil
		if page.ContinuationKey != nil && *page.ContinuationKey != "" {
			continuation = page.ContinuationKey
		}
		if continuation == nil {
			return all, nil
		}
	}
}

// send signs and sends one request. out == nil skips decoding.
func (c *enableBankingClient) send(ctx context.Context, method, path, logPath string, body, out any) error {
	token, err := c.makeToken(c.b.now())
	if err != nil {
		return err
	}
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
	req.Header.Set("Authorization", "Bearer "+token)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var eb struct {
			Error *string `json:"error"`
		}
		var code *string
		if len(data) > 0 && swiftjson.DecodeExternal(data, &eb) == nil {
			code = eb.Error
		}
		// Code and length only: error bodies can echo account details.
		c.b.logger.Warn(fmt.Sprintf("EnableBanking %s failed status=%d error=%s body_bytes=%d",
			logPath, resp.StatusCode, deref(orDash(code)), len(data)))
		return &EnableBankingAPIError{StatusCode: resp.StatusCode, Code: code}
	}
	if out == nil {
		return nil
	}
	if len(data) == 0 {
		return httpx.Abort(http.StatusBadGateway, "Enable Banking returned an empty body.")
	}
	return swiftjson.DecodeExternal(data, out)
}

func orDash(s *string) *string {
	if s == nil {
		return ptr("-")
	}
	return s
}

// makeToken signs the short-lived RS256 JWT every call carries.
func (c *enableBankingClient) makeToken(now time.Time) (string, error) {
	key, err := parseRSAPrivateKey(c.config.PrivateKeyPEM)
	if err != nil {
		return "", httpx.Abort(http.StatusInternalServerError, "Invalid Enable Banking private key format.")
	}
	claims := jwt.MapClaims{
		"iss": "enablebanking.com",
		"aud": "api.enablebanking.com",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = c.config.ApplicationID
	t.Header["typ"] = "JWT"
	return t.SignedString(key)
}

// parseRSAPrivateKey reads a PKCS#1 or PKCS#8 PEM, as Insecure.RSA.PrivateKey(pem:).
func parseRSAPrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return rk, nil
}

// ebTimestamp is RFC 3339 with an explicit offset (ISO8601DateFormatter
// .withInternetDateTime, which prints UTC as "Z").
func ebTimestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z07:00") }

// parseEBTimestamp is EnableBankingProvider.parseTimestamp.
func parseEBTimestamp(raw string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	return t, err == nil
}

// queryEscape percent-encodes a URLQueryItem value as URLComponents does.
func queryEscape(s string) string { return percentEncode(s) }

// EnableBankingProvider is the Enable Banking driver (EU, hosted redirect).
type EnableBankingProvider struct {
	client *enableBankingClient
	b      *base
}

const (
	ebDailyAccountCallBudget = 4
	ebMaxConsentValidity     = 180 * 86400 * time.Second
	ebSyncOverlap            = 3 * 86400 * time.Second
)

// Kind implements Provider.
func (*EnableBankingProvider) Kind() Kind { return KindEnableBanking }

// CreateLinkSession implements Provider (hosted links only).
func (*EnableBankingProvider) CreateLinkSession(context.Context, Call, uuid.UUID) (LinkSessionResponse, error) {
	return LinkSessionResponse{}, errUnsupported()
}

// Exchange implements Provider (hosted links only).
func (*EnableBankingProvider) Exchange(context.Context, Call, ExchangeRequest, uuid.UUID) (*bankingdb.BankConnection, error) {
	return nil, errUnsupported()
}

// ebInstitutionID is "<COUNTRY>:<name>": Enable Banking names a bank by
// (name, country).
func ebInstitutionID(country, name string) string { return strings.ToUpper(country) + ":" + name }

func parseEBInstitutionID(id string) (country, name string, ok bool) {
	country, name, found := strings.Cut(id, ":")
	// Swift split(separator:maxSplits:) drops an empty leading part.
	if !found || len([]rune(country)) != 2 || name == "" {
		return "", "", false
	}
	return strings.ToUpper(country), name, true
}

// ListInstitutions implements Provider.
func (p *EnableBankingProvider) ListInstitutions(ctx context.Context, _ Call, country string) ([]InstitutionResponse, error) {
	list, err := p.client.aspsps(ctx, country)
	if err != nil {
		return nil, err
	}
	out := make([]InstitutionResponse, 0, len(list))
	for _, a := range list {
		out = append(out, InstitutionResponse{
			ID: ebInstitutionID(a.Country, a.Name), Name: a.Name, Bic: a.Bic, LogoURL: a.Logo,
			Country: ptr(a.Country), Beta: a.Beta,
		})
	}
	return out, nil
}

// CreateHostedLink implements Provider: /auth, capped at the bank's
// maximum consent validity, and a pending flow keyed by state.
func (p *EnableBankingProvider) CreateHostedLink(ctx context.Context, call Call, userID uuid.UUID, institutionID, redirectURI string) (LinkSessionResponse, error) {
	country, name, ok := parseEBInstitutionID(institutionID)
	if !ok {
		return LinkSessionResponse{}, httpx.BadRequest("Unknown bank.")
	}
	list, err := p.client.aspsps(ctx, country)
	if err != nil {
		return LinkSessionResponse{}, err
	}
	var aspsp *ebASPSP
	for i := range list {
		if list[i].Name == name {
			aspsp = &list[i]
			break
		}
	}
	if aspsp == nil {
		return LinkSessionResponse{}, httpx.BadRequest("Unknown bank.")
	}
	now := p.b.now()
	limit := ebMaxConsentValidity
	if aspsp.MaximumConsentValidity != nil {
		limit = min(time.Duration(*aspsp.MaximumConsentValidity)*time.Second, ebMaxConsentValidity)
	}
	state := randomReference(32)
	redirect := ""
	if p.client.config.RedirectURL != nil {
		redirect = *p.client.config.RedirectURL
	} else if redirect, err = callbackURL(call, "/v1/banks/enablebanking/callback"); err != nil {
		return LinkSessionResponse{}, err
	}
	auth, err := p.client.startAuthorization(ctx, aspsp.Name, aspsp.Country, now.Add(limit), state, redirect)
	if err != nil {
		return LinkSessionResponse{}, err
	}
	// requisition_id holds the authorization id until the callback swaps in
	// the session id.
	requisitionID := state
	if auth.AuthorizationID != nil {
		requisitionID = *auth.AuthorizationID
	}
	expiresAt := now.Add(1800 * time.Second)
	if err := p.b.q.InsertLinkFlow(ctx, bankingdb.InsertLinkFlowParams{
		ID: uuid.New(), UserID: userID, Provider: string(KindEnableBanking), Reference: state,
		RequisitionID: requisitionID, InstitutionID: &institutionID, AppRedirectUri: redirectURI,
		ExpiresAt: expiresAt, Now: now,
	}); err != nil {
		return LinkSessionResponse{}, err
	}
	return LinkSessionResponse{Provider: KindEnableBanking, HostedURL: &auth.URL, Expiration: ptr(apijson.NewTime(expiresAt))}, nil
}

func (p *EnableBankingProvider) connectionByItem(ctx context.Context, itemID string) (*bankingdb.BankConnection, error) {
	c, err := p.b.q.GetConnectionByItem(ctx, bankingdb.GetConnectionByItemParams{Provider: string(KindEnableBanking), ProviderItemID: itemID})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CompleteHostedLink implements Provider: the bank redirect carries the
// one-time code, exchanged for a session.
func (p *EnableBankingProvider) CompleteHostedLink(ctx context.Context, call Call, reference string) (*bankingdb.BankConnection, error) {
	now := p.b.now()
	flow, err := p.b.q.GetLinkFlow(ctx, bankingdb.GetLinkFlowParams{Reference: reference, Provider: string(KindEnableBanking)})
	if err != nil && !isNoRows(err) {
		return nil, err
	}
	if isNoRows(err) || !flow.ExpiresAt.After(now) {
		return nil, errInvalidLink()
	}
	// The code is single-use: a replayed callback only finds what the first
	// one created.
	if flow.UsedAt != nil {
		existing, err := p.connectionByItem(ctx, flow.RequisitionID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
		return nil, errInvalidLink()
	}
	code := ""
	if call.Code != nil {
		code = strings.TrimFunc(*call.Code, unicode.IsSpace)
	}
	if code == "" {
		return nil, httpx.BadRequest("Missing authorization code.")
	}
	session, err := p.client.createSession(ctx, code)
	if err != nil {
		return nil, err
	}
	if len(session.Accounts) == 0 {
		return nil, httpx.BadRequest("No accounts were shared for this bank.")
	}
	consentExpiresAt := now.Add(90 * 86400 * time.Second)
	if session.Access != nil && session.Access.ValidUntil != nil {
		if t, ok := parseEBTimestamp(*session.Access.ValidUntil); ok {
			consentExpiresAt = t
		}
	}

	conn, err := p.reconsentTarget(ctx, flow.UserID, session.Accounts)
	if err != nil {
		return nil, err
	}
	if conn != nil {
		// Re-consent for accounts we already hold: keep the connection and
		// its history, pointed at the new session.
		conn.ProviderItemID, conn.Status, conn.ConsentExpiresAt = session.SessionID, statusActive, &consentExpiresAt
		conn.LastSyncStatus, conn.LastSyncError, conn.UpdatedAt = nil, nil, &now
		if err := p.b.q.UpdateConnectionReconsent(ctx, bankingdb.UpdateConnectionReconsentParams{
			ID: conn.ID, ProviderItemID: session.SessionID, ConsentExpiresAt: &consentExpiresAt, Now: p.b.now(),
		}); err != nil {
			return nil, err
		}
	} else {
		institutionName := (*string)(nil)
		if session.ASPSP != nil {
			institutionName = &session.ASPSP.Name
		} else if flow.InstitutionID != nil {
			if _, name, ok := parseEBInstitutionID(*flow.InstitutionID); ok {
				institutionName = &name
			}
		}
		conn = &bankingdb.BankConnection{
			UserID: flow.UserID, Provider: string(KindEnableBanking), InstitutionID: flow.InstitutionID,
			InstitutionName: institutionName, ProviderItemID: session.SessionID, Status: statusActive,
			ConsentExpiresAt: &consentExpiresAt,
		}
		if err := p.b.insertConnection(ctx, conn); err != nil {
			return nil, err
		}
	}
	if err := p.upsertAccounts(ctx, session.Accounts, conn.ID); err != nil {
		return nil, err
	}
	if err := p.b.q.UpdateLinkFlowSession(ctx, bankingdb.UpdateLinkFlowSessionParams{
		ID: flow.ID, RequisitionID: session.SessionID, UsedAt: now,
	}); err != nil {
		return nil, err
	}
	// Many banks give full history only right after consent; backfill now.
	_, _ = p.Sync(ctx, call, conn)
	return conn, nil
}

// reconsentTarget is a connection of this user already holding one of the
// session's accounts (matched by identification hash).
func (p *EnableBankingProvider) reconsentTarget(ctx context.Context, userID uuid.UUID, accounts []ebSessionAccount) (*bankingdb.BankConnection, error) {
	hashes := []string{}
	for _, a := range accounts {
		if a.IdentificationHash != nil {
			hashes = append(hashes, *a.IdentificationHash)
		}
	}
	if len(hashes) == 0 {
		return nil, nil
	}
	conns, err := p.b.q.ListUserProviderConnections(ctx, bankingdb.ListUserProviderConnectionsParams{UserID: userID, Provider: string(KindEnableBanking)})
	if err != nil || len(conns) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, len(conns))
	for i := range conns {
		ids[i] = conns[i].ID
	}
	match, err := p.b.q.FindAccountByHashes(ctx, bankingdb.FindAccountByHashesParams{ConnectionIds: ids, Hashes: hashes})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for i := range conns {
		if conns[i].ID == match.ConnectionID {
			return &conns[i], nil
		}
	}
	return nil, nil
}

func (p *EnableBankingProvider) upsertAccounts(ctx context.Context, accounts []ebSessionAccount, connectionID uuid.UUID) error {
	existing, err := p.b.q.ListConnectionAccounts(ctx, connectionID)
	if err != nil {
		return err
	}
	for _, remote := range accounts {
		var account *bankingdb.BankAccount
		for i := range existing {
			local := &existing[i]
			if remote.IdentificationHash != nil && local.IdentificationHash != nil && *local.IdentificationHash == *remote.IdentificationHash {
				account = local
				break
			}
			if local.ProviderAccountID == remote.UID {
				account = local
				break
			}
		}
		if account == nil {
			account = &bankingdb.BankAccount{ConnectionID: connectionID, Name: "Account"}
		}
		// The uid is session-scoped, so always take the new one.
		account.ProviderAccountID = remote.UID
		if remote.IdentificationHash != nil {
			account.IdentificationHash = remote.IdentificationHash
		}
		if n := trimmedNonEmpty(remote.Name); n != nil {
			account.Name = *n
		} else if n := trimmedNonEmpty(remote.Product); n != nil {
			account.Name = *n
		}
		if remote.AccountID != nil && remote.AccountID.Iban != nil {
			r := []rune(*remote.AccountID.Iban)
			account.Mask = ptr(string(r[max(0, len(r)-4):]))
		}
		if remote.Currency != nil {
			account.Currency = remote.Currency
		}
		if remote.CashAccountType != nil {
			account.Type = remote.CashAccountType
		}
		if err := p.b.saveAccount(ctx, account); err != nil {
			return err
		}
	}
	return nil
}

// Sync implements Provider.
func (p *EnableBankingProvider) Sync(ctx context.Context, _ Call, conn *bankingdb.BankConnection) (SyncResult, error) {
	if conn.ConsentExpiresAt != nil && !conn.ConsentExpiresAt.After(p.b.now()) {
		if err := p.b.markReauth(ctx, conn, "consent_expired"); err != nil {
			return SyncResult{}, err
		}
		return SyncResult{}, httpx.Forbidden("Bank consent expired. Reconnect to keep syncing.")
	}
	var dateFrom *time.Time
	if conn.LastSyncedAt != nil {
		dateFrom = ptr(conn.LastSyncedAt.Add(-ebSyncOverlap))
	}
	accounts, err := p.b.q.ListConnectionAccounts(ctx, conn.ID)
	if err != nil {
		return SyncResult{}, err
	}
	var result SyncResult
	for i := range accounts {
		account := &accounts[i]
		allowed, err := p.b.consumeDailyBudget(ctx, "enablebanking:budget:"+account.ProviderAccountID, ebDailyAccountCallBudget)
		if err != nil {
			return SyncResult{}, err
		}
		if !allowed {
			p.b.logger.Info("enablebanking daily budget exhausted account=" + apijson.FormatUUID(account.ID))
			continue
		}
		var from *string
		if dateFrom != nil {
			from = ptr(formatDay(*dateFrom))
		}
		txs, err := p.client.transactions(ctx, account.ProviderAccountID, from)
		if err != nil {
			var apiErr *EnableBankingAPIError
			if errors.As(err, &apiErr) && apiErr.RequiresReauth() {
				if err := p.b.markReauth(ctx, conn, "consent_revoked"); err != nil {
					return SyncResult{}, err
				}
				return SyncResult{}, httpx.Forbidden("Bank consent expired. Reconnect to keep syncing.")
			}
			return SyncResult{}, err
		}
		var since *time.Time
		if dateFrom != nil {
			d := dateFrom.UTC()
			since = ptr(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC))
		}
		removed, err := p.b.q.DeletePendingSuggestions(ctx, bankingdb.DeletePendingSuggestionsParams{AccountID: account.ID, Since: since})
		if err != nil {
			return SyncResult{}, err
		}
		result.Removed += int(removed)
		for _, tx := range txs {
			if tx.Status != "BOOK" && tx.Status != "PDNG" {
				continue
			}
			added, err := p.upsert(ctx, tx, account, conn.UserID)
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
	if err := p.b.markSynced(ctx, conn); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// Disconnect implements Provider (closes the session).
func (p *EnableBankingProvider) Disconnect(ctx context.Context, _ Call, conn *bankingdb.BankConnection) error {
	return p.client.deleteSession(ctx, conn.ProviderItemID)
}

// upsert inserts or refreshes one transaction, keyed on entry_reference
// (else a content hash), and reports whether a row was added.
func (p *EnableBankingProvider) upsert(ctx context.Context, tx ebTransaction, account *bankingdb.BankAccount, userID uuid.UUID) (bool, error) {
	magnitude := math.Abs(swiftDouble(tx.TransactionAmount.Amount))
	isDebit := strings.EqualFold(tx.CreditDebitIndicator, "DBIT")
	// Money out is negative, as for GoCardless.
	amount := magnitude
	if isDebit {
		amount = -magnitude
	}
	pending := tx.Status == "PDNG"
	dateString := ""
	switch {
	case tx.BookingDate != nil:
		dateString = *tx.BookingDate
	case tx.TransactionDate != nil:
		dateString = *tx.TransactionDate
	case tx.ValueDate != nil:
		dateString = *tx.ValueDate
	}
	occurredOn, ok := parseDay(dateString)
	if !ok {
		occurredOn = p.b.now()
	}
	var description *string
	if tx.RemittanceInformation != nil {
		parts := []string{}
		for _, s := range *tx.RemittanceInformation {
			if t := strings.TrimFunc(s, isSwiftWhitespace); t != "" {
				parts = append(parts, t)
			}
		}
		description = ptr(strings.Join(parts, " "))
	}
	var counterparty *string
	if isDebit {
		if tx.Creditor != nil {
			counterparty = tx.Creditor.Name
		}
	} else if tx.Debtor != nil {
		counterparty = tx.Debtor.Name
	}
	merchant := trimmedNonEmpty(counterparty)
	if merchant == nil {
		merchant = trimmedNonEmpty(description)
	}
	accountKey := apijson.FormatUUID(account.ID)
	if account.IdentificationHash != nil {
		accountKey = *account.IdentificationHash
	}
	hash := dedupeHash(accountKey, dateString, amount, deref(merchant))
	providerTxID := hash
	if ref := trimmedNonEmpty(tx.EntryReference); ref != nil {
		providerTxID = *ref
	}
	descriptionText := trimmedNonEmpty(description)

	existing, err := p.b.q.GetAccountTransaction(ctx, bankingdb.GetAccountTransactionParams{AccountID: account.ID, ProviderTxID: providerTxID})
	if err == nil {
		if existing.Status == txSuggested {
			existing.Amount, existing.Currency, existing.OccurredOn = amount, tx.TransactionAmount.Currency, occurredOn
			existing.Merchant, existing.DescriptionText, existing.Pending = merchant, descriptionText, pending
			existing.DedupeHash, existing.ProviderCategory = hash, tx.MerchantCategoryCode
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
		AccountID: account.ID, UserID: userID, ProviderTxID: providerTxID, DedupeHash: hash, Amount: amount,
		Currency: tx.TransactionAmount.Currency, OccurredOn: occurredOn, Merchant: merchant,
		DescriptionText: descriptionText, Pending: pending, Status: txSuggested, ProviderCategory: tx.MerchantCategoryCode,
	}
	return true, p.b.insertTransaction(ctx, &row)
}
