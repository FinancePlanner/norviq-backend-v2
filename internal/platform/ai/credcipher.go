package ai

import (
	"log/slog"

	"norviq-api/internal/platform/crypto"
)

// CredentialCipher is TokenEncryptionService: encrypts third-party secrets
// for string columns as `nvq-enc:<base64(envelope JSON)>`, bound to a
// context. Values without the prefix are legacy plaintext and Decrypt passes
// them through. The implementation is internal/platform/crypto's Vault,
// byte-compatible with Vapor's so stored keys stay readable by both backends.
type CredentialCipher interface {
	Encrypt(plaintext, context string) (string, error)
	Decrypt(stored, context string) (string, error)
	IsEncrypted(stored string) bool
}

// CredentialContextAIProvider is CredentialContext.aiProvider.
const CredentialContextAIProvider = string(crypto.ContextAIProvider)

// CipherFromEnv is UserPIIEncryptionBootstrap over env: the
// USER_PII_ENCRYPTION_* keys, required in production and defaulting to the
// shared dev key elsewhere.
func CipherFromEnv(env Env, production bool, logger *slog.Logger) (CredentialCipher, error) {
	p, err := crypto.PIIFromLookup(production, logger, env.str)
	if err != nil {
		return nil, err
	}
	return vaultCipher{crypto.NewVault(p)}, nil
}

type vaultCipher struct{ v *crypto.Vault }

func (c vaultCipher) Encrypt(plaintext, context string) (string, error) {
	return c.v.Encrypt(plaintext, crypto.CredentialContext(context))
}

func (c vaultCipher) Decrypt(stored, context string) (string, error) {
	return c.v.Decrypt(stored, crypto.CredentialContext(context))
}

func (vaultCipher) IsEncrypted(stored string) bool { return crypto.IsEncrypted(stored) }
