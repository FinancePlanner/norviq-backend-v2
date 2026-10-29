package crypto

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
)

// CredentialContext separates the domains a credential may be decrypted in;
// it becomes the AES-GCM additional authenticated data.
type CredentialContext string

const (
	ContextBroker         CredentialContext = "broker"
	ContextBankPlaid      CredentialContext = "bank_plaid"
	ContextBankGoCardless CredentialContext = "bank_gocardless"
	// ContextAIProvider holds user-supplied LLM API keys (bring your own key).
	ContextAIProvider CredentialContext = "ai_provider"
)

// AuthenticatedData is the AAD string Vapor binds for the context.
func (c CredentialContext) AuthenticatedData() string { return "norviq.credential." + string(c) }

// StoragePrefix marks an encrypted credential column value.
const StoragePrefix = "nvq-enc:"

// ErrMalformedStoredValue is TokenEncryptionError.malformedStoredValue.
var ErrMalformedStoredValue = errors.New("malformed stored credential")

// Vault encrypts third-party access and refresh tokens for string columns:
// "nvq-enc:" + base64(version-2 envelope). Values without the prefix are
// legacy plaintext and pass through Decrypt unchanged.
type Vault struct{ engine *PII }

// NewVault wraps the PII engine (Vapor shares the same keys for both).
func NewVault(engine *PII) *Vault { return &Vault{engine: engine} }

// VaultFromEnv builds a vault from the USER_PII_ENCRYPTION_* variables.
func VaultFromEnv(isProduction bool, logger *slog.Logger) (*Vault, error) {
	p, err := PIIFromEnv(isProduction, logger)
	if err != nil {
		return nil, err
	}
	return NewVault(p), nil
}

// Encrypt seals token for storage under ctx.
func (v *Vault) Encrypt(token string, ctx CredentialContext) (string, error) {
	env, err := v.engine.EncryptWithContext(token, ctx.AuthenticatedData())
	if err != nil {
		return "", err
	}
	return StoragePrefix + base64.StdEncoding.EncodeToString(env), nil
}

// Decrypt opens a stored value sealed under ctx. Legacy plaintext is
// returned as is.
func (v *Vault) Decrypt(stored string, ctx CredentialContext) (string, error) {
	if !IsEncrypted(stored) {
		return stored, nil
	}
	env, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, StoragePrefix))
	if err != nil {
		return "", ErrMalformedStoredValue
	}
	return v.engine.DecryptWithContext(env, ctx.AuthenticatedData())
}

// IsEncrypted reports whether stored carries the vault prefix.
func IsEncrypted(stored string) bool { return strings.HasPrefix(stored, StoragePrefix) }
