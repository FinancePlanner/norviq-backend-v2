package ai

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// CredentialCipher is TokenEncryptionService: encrypts third-party secrets
// for string columns as `nvq-enc:<base64(envelope JSON)>`, bound to a
// context. Values without the prefix are legacy plaintext and Decrypt passes
// them through.
//
// The identity port owns the canonical implementation
// (internal/platform/crypto). AESGCMCipher below is byte-compatible with
// Vapor's so stored keys stay readable by both backends; swap it for the
// identity package's once that lands.
type CredentialCipher interface {
	Encrypt(plaintext, context string) (string, error)
	Decrypt(stored, context string) (string, error)
	IsEncrypted(stored string) bool
}

// CredentialContextAIProvider is CredentialContext.aiProvider.
const CredentialContextAIProvider = "ai_provider"

// EncryptedPrefix is AESGCMTokenEncryptionService.storagePrefix.
const EncryptedPrefix = "nvq-enc:"

const (
	devKeyID = "dev-default"
	devKey   = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
)

// Cipher errors, as UserPIIEncryptionError.
var (
	ErrMalformedStoredValue = errors.New("malformed stored credential")
	ErrInvalidPayload       = errors.New("invalid encrypted payload")
	ErrDecryptionFailed     = errors.New("decryption failed")
)

// AESGCMCipher is AESGCMTokenEncryptionService over
// AESGCMUserPIIEncryptionService: AES-256-GCM, combined nonce||ciphertext||tag,
// authenticated with "norviq.credential.<context>", in a v2 JSON envelope.
type AESGCMCipher struct {
	activeKeyID string
	keys        map[string][]byte
}

type envelopeV1 struct {
	Version            int    `json:"version"`
	KeyID              string `json:"keyID"`
	CombinedCiphertext string `json:"combinedCiphertext"`
}

// NewAESGCMCipher builds a cipher. previous may be nil.
func NewAESGCMCipher(activeKeyID string, activeKey []byte, previous map[string][]byte) (*AESGCMCipher, error) {
	if len(activeKey) != 32 {
		return nil, errors.New("active key must be 32 bytes")
	}
	keys := map[string][]byte{}
	for id, k := range previous {
		keys[id] = k
	}
	keys[activeKeyID] = activeKey
	return &AESGCMCipher{activeKeyID: activeKeyID, keys: keys}, nil
}

// CipherFromEnv is UserPIIEncryptionBootstrap: USER_PII_ENCRYPTION_ACTIVE_KEY_ID
// and USER_PII_ENCRYPTION_ACTIVE_KEY (base64, 32 bytes), required in
// production and defaulting to the shared dev key elsewhere, plus
// USER_PII_ENCRYPTION_PREVIOUS_KEYS as `id:base64,...`.
func CipherFromEnv(env Env, production bool, logger *slog.Logger) (*AESGCMCipher, error) {
	id := strings.TrimSpace(env.str("USER_PII_ENCRYPTION_ACTIVE_KEY_ID"))
	raw := strings.TrimSpace(env.str("USER_PII_ENCRYPTION_ACTIVE_KEY"))
	if id == "" || raw == "" {
		if production {
			return nil, errors.New("missing USER_PII_ENCRYPTION_ACTIVE_KEY_ID / USER_PII_ENCRYPTION_ACTIVE_KEY")
		}
		id, raw = devKeyID, devKey
		if logger != nil {
			logger.Warn("Using non-production fallback user PII encryption key.")
		}
	}
	active, err := decodeKey(raw, "USER_PII_ENCRYPTION_ACTIVE_KEY")
	if err != nil {
		return nil, err
	}
	previous := map[string][]byte{}
	if prevRaw := strings.TrimSpace(env.str("USER_PII_ENCRYPTION_PREVIOUS_KEYS")); prevRaw != "" {
		for _, pair := range strings.Split(prevRaw, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			parts := strings.SplitN(pair, ":", 2)
			if len(parts) != 2 {
				return nil, errors.New("invalid USER_PII_ENCRYPTION_PREVIOUS_KEYS entry format")
			}
			kid, kraw := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if kid == "" || kraw == "" {
				return nil, errors.New("invalid USER_PII_ENCRYPTION_PREVIOUS_KEYS entry")
			}
			k, err := decodeKey(kraw, "USER_PII_ENCRYPTION_PREVIOUS_KEYS")
			if err != nil {
				return nil, err
			}
			previous[kid] = k
		}
	}
	return NewAESGCMCipher(id, active, previous)
}

func decodeKey(value, name string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("%s must be base64 for exactly 32 bytes", name)
	}
	return k, nil
}

func authenticatedData(context string) []byte {
	return []byte("norviq.credential." + context)
}

// Encrypt seals plaintext under the active key.
func (c *AESGCMCipher) Encrypt(plaintext, context string) (string, error) {
	gcm, err := newGCM(c.keys[c.activeKeyID])
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	combined := gcm.Seal(nonce, nonce, []byte(plaintext), authenticatedData(context))
	envelope, err := json.Marshal(envelopeV1{
		Version:            2,
		KeyID:              c.activeKeyID,
		CombinedCiphertext: base64.StdEncoding.EncodeToString(combined),
	})
	if err != nil {
		return "", err
	}
	return EncryptedPrefix + base64.StdEncoding.EncodeToString(envelope), nil
}

// Decrypt opens a stored value; plaintext without the prefix passes through.
func (c *AESGCMCipher) Decrypt(stored, context string) (string, error) {
	if !c.IsEncrypted(stored) {
		return stored, nil
	}
	envelopeJSON, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, EncryptedPrefix))
	if err != nil {
		return "", ErrMalformedStoredValue
	}
	var env envelopeV1
	if err := json.Unmarshal(envelopeJSON, &env); err != nil {
		return "", ErrInvalidPayload
	}
	key, ok := c.keys[env.KeyID]
	if !ok {
		return "", fmt.Errorf("unknown encryption key identifier %q", env.KeyID)
	}
	combined, err := base64.StdEncoding.DecodeString(env.CombinedCiphertext)
	if env.Version != 2 || err != nil {
		return "", ErrInvalidPayload
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(combined) < gcm.NonceSize()+gcm.Overhead() {
		return "", ErrInvalidPayload
	}
	nonce, sealed := combined[:gcm.NonceSize()], combined[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, sealed, authenticatedData(context))
	if err != nil {
		return "", ErrDecryptionFailed
	}
	return string(plain), nil
}

// IsEncrypted reports the storage prefix.
func (c *AESGCMCipher) IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, EncryptedPrefix)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
