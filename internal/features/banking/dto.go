package banking

import (
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/bankingdb"
)

// Kind is BankProviderKind.
type Kind string

// The aggregators, in BankProviderKind.allCases order.
const (
	KindPlaid         Kind = "plaid"
	KindGoCardless    Kind = "gocardless"
	KindEnableBanking Kind = "enablebanking"
)

// parseKind is BankProviderKind(rawValue:).
func parseKind(raw string) (Kind, bool) {
	switch Kind(raw) {
	case KindPlaid, KindGoCardless, KindEnableBanking:
		return Kind(raw), true
	}
	return "", false
}

// kindOrPlaid is `BankProviderKind(rawValue: x) ?? .plaid`.
func kindOrPlaid(raw string) Kind {
	if k, ok := parseKind(raw); ok {
		return k
	}
	return KindPlaid
}

// BankConnectionStatus and BankTransactionStatus raw values.
const (
	statusActive         = "active"
	statusReauthRequired = "reauth_required"
	statusDisconnected   = "disconnected"
	statusError          = "error"

	txSuggested = "suggested"
	txImported  = "imported"
	txDismissed = "dismissed"
)

// AccountResponse is BankAccountResponse.
type AccountResponse struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Mask     *string  `json:"mask,omitempty"`
	Currency *string  `json:"currency,omitempty"`
	Type     *string  `json:"type,omitempty"`
	Balance  *float64 `json:"balance,omitempty"`
}

// ConnectionResponse is BankConnectionResponse.
type ConnectionResponse struct {
	ID               string            `json:"id"`
	Provider         Kind              `json:"provider"`
	InstitutionName  *string           `json:"institutionName,omitempty"`
	Status           string            `json:"status"`
	ConsentExpiresAt *apijson.Time     `json:"consentExpiresAt,omitempty"`
	LastSyncedAt     *apijson.Time     `json:"lastSyncedAt,omitempty"`
	Accounts         []AccountResponse `json:"accounts"`
}

// TransactionResponse is BankTransactionResponse.
type TransactionResponse struct {
	ID                string  `json:"id"`
	AccountID         string  `json:"accountId"`
	Amount            float64 `json:"amount"`
	Currency          *string `json:"currency,omitempty"`
	Date              string  `json:"date"`
	Merchant          *string `json:"merchant,omitempty"`
	DescriptionText   *string `json:"descriptionText,omitempty"`
	Pending           bool    `json:"pending"`
	Status            string  `json:"status"`
	ProviderCategory  *string `json:"providerCategory,omitempty"`
	ExpenseID         *string `json:"expenseId,omitempty"`
	PossibleDuplicate bool    `json:"possibleDuplicate"`
}

// LinkSessionResponse is BankLinkSessionResponse.
type LinkSessionResponse struct {
	Provider   Kind          `json:"provider"`
	LinkToken  *string       `json:"linkToken,omitempty"`
	HostedURL  *string       `json:"hostedURL,omitempty"`
	Expiration *apijson.Time `json:"expiration,omitempty"`
}

// InstitutionResponse is BankInstitutionResponse.
type InstitutionResponse struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Bic     *string `json:"bic,omitempty"`
	LogoURL *string `json:"logoURL,omitempty"`
	Country *string `json:"country,omitempty"`
	Beta    *bool   `json:"beta,omitempty"`
}

// SyncResponse is BankSyncResponse.
type SyncResponse struct {
	Added    int `json:"added"`
	Modified int `json:"modified"`
	Removed  int `json:"removed"`
}

// ExchangeRequest is BankExchangeRequest.
type ExchangeRequest struct {
	PublicToken     string  `json:"publicToken"`
	InstitutionID   *string `json:"institutionId"`
	InstitutionName *string `json:"institutionName"`
}

// hostedLinkRequest is BankHostedLinkRequest.
type hostedLinkRequest struct {
	InstitutionID string `json:"institutionId"`
	RedirectURI   string `json:"redirectURI"`
}

// SyncResult is BankSyncResult.
type SyncResult struct{ Added, Modified, Removed int }

// dayLayout is the yyyy-MM-dd DateFormatter (Gregorian, POSIX, UTC).
const dayLayout = "2006-01-02"

// parseDay is that formatter's date(from:).
func parseDay(s string) (time.Time, bool) {
	// ICU reads one-digit months and days under a two-digit pattern.
	t, err := time.ParseInLocation("2006-1-2", s, time.UTC)
	return t, err == nil
}

// formatDay is that formatter's string(from:).
func formatDay(t time.Time) string { return t.UTC().Format(dayLayout) }

func accountResponse(a *bankingdb.BankAccount) AccountResponse {
	return AccountResponse{
		ID: apijson.FormatUUID(a.ID), Name: a.Name, Mask: a.Mask,
		Currency: a.Currency, Type: a.Type, Balance: a.Balance,
	}
}

// connectionResponse is BankConnectionResponse(from:accounts:).
func connectionResponse(c *bankingdb.BankConnection, accounts []bankingdb.BankAccount) ConnectionResponse {
	status := c.Status
	switch status {
	case statusActive, statusReauthRequired, statusDisconnected, statusError:
	default:
		status = statusActive
	}
	out := ConnectionResponse{
		ID: apijson.FormatUUID(c.ID), Provider: kindOrPlaid(c.Provider),
		InstitutionName: c.InstitutionName, Status: status,
		ConsentExpiresAt: apijson.TimePtr(c.ConsentExpiresAt), LastSyncedAt: apijson.TimePtr(c.LastSyncedAt),
		Accounts: make([]AccountResponse, 0, len(accounts)),
	}
	for i := range accounts {
		out.Accounts = append(out.Accounts, accountResponse(&accounts[i]))
	}
	return out
}

// transactionResponse is BankTransactionResponse(from:possibleDuplicate:).
func transactionResponse(t *bankingdb.BankTransaction, possibleDuplicate bool) TransactionResponse {
	status := t.Status
	switch status {
	case txSuggested, txImported, txDismissed:
	default:
		status = txSuggested
	}
	var expenseID *string
	if t.ExpenseID != nil {
		s := apijson.FormatUUID(*t.ExpenseID)
		expenseID = &s
	}
	return TransactionResponse{
		ID: apijson.FormatUUID(t.ID), AccountID: apijson.FormatUUID(t.AccountID),
		Amount: t.Amount, Currency: t.Currency, Date: formatDay(t.OccurredOn),
		Merchant: t.Merchant, DescriptionText: t.DescriptionText, Pending: t.Pending,
		Status: status, ProviderCategory: t.ProviderCategory, ExpenseID: expenseID,
		PossibleDuplicate: possibleDuplicate,
	}
}
