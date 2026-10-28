package aiplatform

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/netguard"
	"norviq-api/internal/store/aiplatformdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type echoMiddleware = echo.MiddlewareFunc

type handlers struct{ p *ai.Platform }

// MARK: - AI consent

// consentResponse is AIConsentResponse: `{"consentedAt": "<ISO8601>" | null}`,
// the key always present.
type consentResponse struct {
	ConsentedAt *string `json:"consentedAt"`
}

func consentBody(at *time.Time) consentResponse {
	if at == nil {
		return consentResponse{}
	}
	s := apijson.FormatTime(*at)
	return consentResponse{ConsentedAt: &s}
}

// consentUser loads the user's consent, 401 "User not found" without a row.
func (h *handlers) consentUser(c *echo.Context) (uuid.UUID, *time.Time, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, nil, err
	}
	at, err := h.p.Queries.GetUserAIConsent(c.Request().Context(), s.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, httpx.Abort(http.StatusUnauthorized, "User not found")
	}
	return s.UserID, at, err
}

func (h *handlers) showConsent(c *echo.Context) error {
	_, at, err := h.consentUser(c)
	if err != nil {
		return err
	}
	return httpx.OK(c, consentBody(at))
}

// grantConsent keeps the original timestamp when granted again: it records
// when the user first agreed.
func (h *handlers) grantConsent(c *echo.Context) error {
	userID, at, err := h.consentUser(c)
	if err != nil {
		return err
	}
	if at == nil {
		now := time.Now()
		if err := h.p.Queries.SetUserAIConsent(c.Request().Context(), aiplatformdb.SetUserAIConsentParams{
			ID: userID, AiDataConsentAt: &now, UpdatedAt: &now,
		}); err != nil {
			return err
		}
		at = &now
	}
	return httpx.OK(c, consentBody(at))
}

func (h *handlers) withdrawConsent(c *echo.Context) error {
	userID, at, err := h.consentUser(c)
	if err != nil {
		return err
	}
	if at != nil {
		now := time.Now()
		if err := h.p.Queries.SetUserAIConsent(c.Request().Context(), aiplatformdb.SetUserAIConsentParams{
			ID: userID, AiDataConsentAt: nil, UpdatedAt: &now,
		}); err != nil {
			return err
		}
	}
	return httpx.OK(c, consentBody(nil))
}

// MARK: - Provider credentials

const (
	maxLabelLength = 100
	maxKeyLength   = 512
)

// credentialSummary is UserAIProviderCredentialSummary. There is no apiKey
// field: the key never comes back out, not even at creation.
type credentialSummary struct {
	ID            apijson.UUID  `json:"id"`
	Provider      string        `json:"provider"`
	Label         string        `json:"label"`
	KeyHint       string        `json:"keyHint"`
	BaseURL       *string       `json:"baseURL,omitempty"`
	DefaultModel  *string       `json:"defaultModel,omitempty"`
	Status        string        `json:"status"`
	LastVerified  *apijson.Time `json:"lastVerifiedAt,omitempty"`
	LastUsedAt    *apijson.Time `json:"lastUsedAt,omitempty"`
	LastErrorCode *string       `json:"lastErrorCode,omitempty"`
	CreatedAt     apijson.Time  `json:"createdAt"`
	UpdatedAt     apijson.Time  `json:"updatedAt"`
}

type credentialList struct {
	Items []credentialSummary `json:"items"`
}

// verifyResponse is UserAIProviderCredentialVerifyResponse.
type verifyResponse struct {
	Status     string        `json:"status"`
	VerifiedAt *apijson.Time `json:"verifiedAt,omitempty"`
	ErrorCode  *string       `json:"errorCode,omitempty"`
	Detail     string        `json:"detail"`
	LatencyMs  int           `json:"latencyMs"`
}

func summary(row *ai.Credential) credentialSummary {
	now := time.Now()
	created, updated := now, now
	if row.CreatedAt != nil {
		created = *row.CreatedAt
	}
	if row.UpdatedAt != nil {
		updated = *row.UpdatedAt
	}
	return credentialSummary{
		ID:            apijson.NewUUID(row.ID),
		Provider:      row.Provider,
		Label:         row.Label,
		KeyHint:       row.KeyHint,
		BaseURL:       row.BaseUrl,
		DefaultModel:  row.DefaultModel,
		Status:        row.Status,
		LastVerified:  apijson.TimePtr(row.LastVerifiedAt),
		LastUsedAt:    apijson.TimePtr(row.LastUsedAt),
		LastErrorCode: row.LastErrorCode,
		CreatedAt:     apijson.NewTime(created),
		UpdatedAt:     apijson.NewTime(updated),
	}
}

// requireFirstParty is requireFirstPartySession followed by requireEnabled.
// Credential management must never be reachable by an MCP token.
func (h *handlers) requireFirstParty(c *echo.Context) (*auth.Session, error) {
	if s := auth.FromContext(c); s != nil && s.IsThirdParty() {
		return nil, httpx.Forbidden("Credential management requires a first-party session.")
	}
	s, err := auth.Require(c)
	if err != nil {
		return nil, err
	}
	if !h.p.Settings.CredentialsEnabled {
		return nil, httpx.Abort(http.StatusServiceUnavailable, "Bring-your-own-key is currently disabled.")
	}
	return s, nil
}

func (h *handlers) listCredentials(c *echo.Context) error {
	s, err := h.requireFirstParty(c)
	if err != nil {
		return err
	}
	rows, err := h.p.Queries.ListAICredentials(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	items := make([]credentialSummary, 0, len(rows))
	for i := range rows {
		items = append(items, summary(&rows[i]))
	}
	return httpx.OK(c, credentialList{Items: items})
}

var credentialFields = []fieldSpec{
	{name: "provider", kind: kindString, required: true},
	{name: "label", kind: kindString, required: true},
	{name: "apiKey", kind: kindString, required: true},
	{name: "baseURL", kind: kindString},
	{name: "defaultModel", kind: kindString},
	{name: "verify", kind: kindBool},
}

func (h *handlers) upsertCredential(c *echo.Context) error {
	s, err := h.requireFirstParty(c)
	if err != nil {
		return err
	}
	body, err := decodeObject(c, credentialFields)
	if err != nil {
		return err
	}
	rawProvider := body["provider"].(string)
	provider := strings.ToLower(rawProvider)
	if !ai.IsUserProvider(provider) {
		return httpx.BadRequest(fmt.Sprintf("Unknown provider '%s'.", rawProvider))
	}
	label := strings.TrimSpace(body["label"].(string))
	if label == "" || utf8.RuneCountInString(label) > maxLabelLength {
		return httpx.BadRequest(fmt.Sprintf("Label must be 1-%d characters.", maxLabelLength))
	}
	apiKey := strings.TrimSpace(body["apiKey"].(string))
	if apiKey == "" || utf8.RuneCountInString(apiKey) > maxKeyLength {
		return httpx.BadRequest(fmt.Sprintf("API key must be 1-%d characters.", maxKeyLength))
	}
	// Embedded whitespace is a mangled paste, the commonest support ticket.
	if strings.IndexFunc(apiKey, unicode.IsSpace) >= 0 {
		return httpx.BadRequest("API key contains whitespace — check for a broken copy/paste.")
	}
	defaultModel := optString(body, "defaultModel")
	var baseURL *string
	rawBase := ""
	if b := optString(body, "baseURL"); b != nil {
		rawBase = strings.TrimSpace(*b)
	}
	if provider == ai.UserProviderCompatible {
		if rawBase == "" {
			return httpx.BadRequest("A base URL is required for an OpenAI-compatible endpoint.")
		}
		// SSRF guard: the server is about to send a bearer token here.
		if _, err := netguard.ValidateForRequest(rawBase); err != nil {
			return err
		}
		baseURL = &rawBase
		if defaultModel == nil || strings.TrimSpace(*defaultModel) == "" {
			return httpx.BadRequest("A default model is required for an OpenAI-compatible endpoint.")
		}
	} else if rawBase != "" {
		if _, err := netguard.ValidateForRequest(rawBase); err != nil {
			return err
		}
		baseURL = &rawBase
	}

	encrypted, err := h.p.Cipher.Encrypt(apiKey, ai.CredentialContextAIProvider)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	existing, err := h.p.Queries.GetAICredentialByProvider(ctx, aiplatformdb.GetAICredentialByProviderParams{UserID: s.UserID, Provider: provider})
	isNew := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !isNew {
		return err
	}
	now := time.Now()
	row := existing
	if isNew {
		row = ai.Credential{
			ID: uuid.New(), UserID: s.UserID, Provider: provider, Label: label,
			ApiKeyEncrypted: encrypted, KeyHint: keyHint(apiKey), KeyFingerprint: auth.SHA256Hex(apiKey),
			BaseUrl: baseURL, DefaultModel: defaultModel, Status: ai.CredentialUnverified,
			CreatedAt: &now, UpdatedAt: &now,
		}
		if err := h.p.Queries.InsertAICredential(ctx, aiplatformdb.InsertAICredentialParams{
			ID: row.ID, UserID: row.UserID, Provider: row.Provider, Label: row.Label,
			ApiKeyEncrypted: row.ApiKeyEncrypted, KeyHint: row.KeyHint, KeyFingerprint: row.KeyFingerprint,
			BaseUrl: row.BaseUrl, DefaultModel: row.DefaultModel, Status: row.Status,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}); err != nil {
			return err
		}
	} else {
		row.Label = label
		row.ApiKeyEncrypted = encrypted
		row.KeyHint = keyHint(apiKey)
		row.KeyFingerprint = auth.SHA256Hex(apiKey)
		row.BaseUrl = baseURL
		row.DefaultModel = defaultModel
		row.Status = ai.CredentialUnverified
		row.LastErrorCode, row.LastErrorAt, row.LastVerifiedAt = nil, nil, nil
		row.UpdatedAt = &now
		if err := h.save(c, &row); err != nil {
			return err
		}
	}

	if v, ok := body["verify"].(bool); !ok || v {
		result, err := h.p.Verifier.Verify(ctx, row.Provider, apiKey, row.BaseUrl, row.DefaultModel)
		if err != nil {
			return err
		}
		applyVerify(result, &row)
		if err := h.save(c, &row); err != nil {
			return err
		}
	}

	status := http.StatusOK
	if isNew {
		status = http.StatusCreated
	}
	return httpx.JSON(c, status, summary(&row))
}

func (h *handlers) verifyCredential(c *echo.Context) error {
	s, err := h.requireFirstParty(c)
	if err != nil {
		return err
	}
	row, err := h.ownedCredential(c, s.UserID)
	if err != nil {
		return err
	}
	if !ai.IsUserProvider(row.Provider) {
		return httpx.BadRequest("Stored credential has an unrecognised provider.")
	}
	// For this table plaintext can only mean corruption or a bad write path.
	if !h.p.Cipher.IsEncrypted(row.ApiKeyEncrypted) {
		return httpx.Abort(http.StatusInternalServerError, "Stored credential is not encrypted.")
	}
	apiKey, err := h.p.Cipher.Decrypt(row.ApiKeyEncrypted, ai.CredentialContextAIProvider)
	if err != nil {
		return err
	}
	result, err := h.p.Verifier.Verify(c.Request().Context(), row.Provider, apiKey, row.BaseUrl, row.DefaultModel)
	if err != nil {
		return err
	}
	applyVerify(result, row)
	if err := h.save(c, row); err != nil {
		return err
	}
	// 200 even on a bad key: the diagnostic itself succeeded.
	return httpx.OK(c, verifyResponse{
		Status:     row.Status,
		VerifiedAt: apijson.TimePtr(row.LastVerifiedAt),
		ErrorCode:  row.LastErrorCode,
		Detail:     result.Detail,
		LatencyMs:  result.LatencyMs,
	})
}

func (h *handlers) deleteCredential(c *echo.Context) error {
	s, err := h.requireFirstParty(c)
	if err != nil {
		return err
	}
	row, err := h.ownedCredential(c, s.UserID)
	if err != nil {
		return err
	}
	// Hard delete: a tombstone would block re-adding the provider forever.
	if err := h.p.Queries.DeleteAICredential(c.Request().Context(), row.ID); err != nil {
		return err
	}
	h.p.Logger.Info(fmt.Sprintf("ai_credential.deleted user=%s provider=%s", apijson.FormatUUID(s.UserID), row.Provider))
	return httpx.NoContent(c)
}

func (h *handlers) ownedCredential(c *echo.Context, userID uuid.UUID) (*ai.Credential, error) {
	id, ok := parseSwiftUUID(c.Param("credentialId"))
	if !ok {
		return nil, httpx.BadRequest("Invalid credential id.")
	}
	row, err := h.p.Queries.GetAICredentialForUser(c.Request().Context(), aiplatformdb.GetAICredentialForUserParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("Credential not found.")
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (h *handlers) save(c *echo.Context, row *ai.Credential) error {
	return h.p.Queries.UpdateAICredential(c.Request().Context(), aiplatformdb.UpdateAICredentialParams{
		ID: row.ID, Label: row.Label, ApiKeyEncrypted: row.ApiKeyEncrypted, KeyHint: row.KeyHint,
		KeyFingerprint: row.KeyFingerprint, BaseUrl: row.BaseUrl, DefaultModel: row.DefaultModel,
		Status: row.Status, LastVerifiedAt: row.LastVerifiedAt, LastUsedAt: row.LastUsedAt,
		LastErrorCode: row.LastErrorCode, LastErrorAt: row.LastErrorAt, UpdatedAt: row.UpdatedAt,
	})
}

// applyVerify records a verification on the row. Rate limiting and
// unreachability prove nothing about the key and leave status alone.
func applyVerify(r ai.VerifyResult, row *ai.Credential) {
	now := time.Now()
	row.UpdatedAt = &now
	if r.OK {
		row.Status = ai.CredentialActive
		row.LastVerifiedAt = &now
		row.LastErrorCode, row.LastErrorAt = nil, nil
		return
	}
	if r.ErrorCode != "" {
		row.LastErrorCode = ai.Str(r.ErrorCode)
	} else {
		row.LastErrorCode = nil
	}
	row.LastErrorAt = &now
	if !r.IsInconclusive() {
		row.Status = ai.CredentialInvalid
	}
}

// keyHint is the last four characters of the plaintext.
func keyHint(key string) string {
	r := []rune(key)
	if len(r) <= 4 {
		return key
	}
	return string(r[len(r)-4:])
}

// parseSwiftUUID accepts only what UUID(uuidString:) did: the 36-character
// hyphenated form, either case.
func parseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}
