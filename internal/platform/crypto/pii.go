// Package crypto encrypts user PII and third-party credentials at rest,
// byte-compatible with Vapor's CryptoKit code (Security/UserPIIEncryption.swift
// and Security/CredentialEncryption.swift): Go opens what Vapor sealed and
// Vapor opens what Go seals, under the same environment keys.
//
// A sealed value is a JSON envelope {"version","keyID","combinedCiphertext"}
// whose ciphertext is CryptoKit's AES.GCM "combined" form, base64-encoded:
// 12-byte nonce || ciphertext || 16-byte tag. Version 1 has no additional
// authenticated data (user PII); version 2 binds a context string as AAD
// (credentials).
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"
)

// Errors mirror UserPIIEncryptionError.
var (
	ErrInvalidPayload    = errors.New("invalid encrypted payload")
	ErrDecryptionFailed  = errors.New("decryption failed")
	ErrMissingConfig     = errors.New("missing encryption configuration")
	ErrMalformedConfig   = errors.New("malformed encryption configuration")
	errUnknownKeyIDBase  = errors.New("unknown key identifier")
	defaultNonProdKeyID  = "dev-default"
	defaultNonProdKeyB64 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
)

// UnknownKeyError is returned when an envelope names a key this process
// does not hold (UserPIIEncryptionError.unknownKeyIdentifier).
type UnknownKeyError struct{ KeyID string }

func (e UnknownKeyError) Error() string { return "unknown key identifier " + e.KeyID }

func (e UnknownKeyError) Unwrap() error { return errUnknownKeyIDBase }

// PII seals and opens values with the active key, and opens values sealed
// under any previous key (rotation). Safe for concurrent use.
type PII struct {
	activeKeyID string
	aeads       map[string]cipher.AEAD
}

// NewPII builds the engine from raw 32-byte keys. previous may be nil.
func NewPII(activeKeyID string, activeKey []byte, previous map[string][]byte) (*PII, error) {
	p := &PII{activeKeyID: activeKeyID, aeads: map[string]cipher.AEAD{}}
	for id, key := range previous {
		a, err := newAEAD(key)
		if err != nil {
			return nil, err
		}
		p.aeads[id] = a
	}
	a, err := newAEAD(activeKey)
	if err != nil {
		return nil, err
	}
	p.aeads[activeKeyID] = a
	return p, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: key must be 32 bytes", ErrMalformedConfig)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// envelope is EnvelopeV1 in Swift. The field order matches the Swift
// declaration; Swift's JSONEncoder does not guarantee one, so readers on
// both sides parse JSON rather than compare bytes.
type envelope struct {
	Version            int    `json:"version"`
	KeyID              string `json:"keyID"`
	CombinedCiphertext string `json:"combinedCiphertext"`
}

// EncryptString seals value as a version-1 envelope (users.*_encrypted).
func (p *PII) EncryptString(value string) ([]byte, error) {
	return p.seal(value, 1, nil)
}

// DecryptString opens a version-1 envelope.
func (p *PII) DecryptString(payload []byte) (string, error) {
	return p.open(payload, 1, nil)
}

// EncryptWithContext seals value as a version-2 envelope with context as AAD.
func (p *PII) EncryptWithContext(value, context string) ([]byte, error) {
	return p.seal(value, 2, []byte(context))
}

// DecryptWithContext opens a version-2 envelope sealed with the same context.
func (p *PII) DecryptWithContext(payload []byte, context string) (string, error) {
	return p.open(payload, 2, []byte(context))
}

func (p *PII) seal(value string, version int, aad []byte) ([]byte, error) {
	a := p.aeads[p.activeKeyID]
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// CryptoKit's combined representation: nonce || ciphertext || tag.
	combined := a.Seal(nonce, nonce, []byte(value), aad)
	return marshalEnvelope(envelope{
		Version:            version,
		KeyID:              p.activeKeyID,
		CombinedCiphertext: base64.StdEncoding.EncodeToString(combined),
	})
}

// marshalEnvelope writes the envelope the way Swift's JSONEncoder does,
// slashes escaped as `\/`, so stored bytes look alike whichever side wrote
// them.
func marshalEnvelope(e envelope) ([]byte, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return bytes.ReplaceAll(raw, []byte("/"), []byte(`\/`)), nil
}

func (p *PII) open(payload []byte, version int, aad []byte) (string, error) {
	// Every key is required, as in Swift's synthesized Decodable.
	var raw struct {
		Version            *int    `json:"version"`
		KeyID              *string `json:"keyID"`
		CombinedCiphertext *string `json:"combinedCiphertext"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil ||
		raw.Version == nil || raw.KeyID == nil || raw.CombinedCiphertext == nil {
		return "", ErrInvalidPayload
	}
	e := envelope{Version: *raw.Version, KeyID: *raw.KeyID, CombinedCiphertext: *raw.CombinedCiphertext}
	a, known := p.aeads[e.KeyID]
	combined, b64err := base64.StdEncoding.DecodeString(e.CombinedCiphertext)
	if e.Version != version || !known || b64err != nil {
		if !known {
			return "", UnknownKeyError{KeyID: e.KeyID}
		}
		return "", ErrInvalidPayload
	}
	if len(combined) < a.NonceSize()+a.Overhead() {
		return "", ErrInvalidPayload
	}
	nonce, sealed := combined[:a.NonceSize()], combined[a.NonceSize():]
	plain, err := a.Open(nil, nonce, sealed, aad)
	if err != nil {
		return "", ErrDecryptionFailed
	}
	if !utf8.Valid(plain) {
		return "", ErrInvalidPayload
	}
	return string(plain), nil
}

// PIIFromEnv reads USER_PII_ENCRYPTION_ACTIVE_KEY_ID, USER_PII_ENCRYPTION_ACTIVE_KEY
// and USER_PII_ENCRYPTION_PREVIOUS_KEYS ("id:base64,id:base64"), exactly as
// UserPIIEncryptionBootstrap does. Outside production a missing key falls
// back to Vapor's fixed development key, so data written by a local Vapor
// database stays readable.
func PIIFromEnv(isProduction bool, logger *slog.Logger) (*PII, error) {
	return PIIFromLookup(isProduction, logger, os.Getenv)
}

// PIIFromLookup is PIIFromEnv over an arbitrary variable lookup.
func PIIFromLookup(isProduction bool, logger *slog.Logger, get func(string) string) (*PII, error) {
	keyID := strings.TrimSpace(get("USER_PII_ENCRYPTION_ACTIVE_KEY_ID"))
	keyRaw := strings.TrimSpace(get("USER_PII_ENCRYPTION_ACTIVE_KEY"))
	if keyID == "" || keyRaw == "" {
		if isProduction {
			return nil, fmt.Errorf("%w: Missing USER_PII_ENCRYPTION_ACTIVE_KEY_ID / USER_PII_ENCRYPTION_ACTIVE_KEY", ErrMissingConfig)
		}
		keyID, keyRaw = defaultNonProdKeyID, defaultNonProdKeyB64
		if logger != nil {
			logger.Warn("Using non-production fallback user PII encryption key.")
		}
	}
	active, err := decodeKey(keyRaw, "USER_PII_ENCRYPTION_ACTIVE_KEY")
	if err != nil {
		return nil, err
	}
	previous := map[string][]byte{}
	if raw := strings.TrimSpace(get("USER_PII_ENCRYPTION_PREVIOUS_KEYS")); raw != "" {
		for _, pair := range strings.Split(raw, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			id, key, ok := strings.Cut(pair, ":")
			if !ok {
				return nil, fmt.Errorf("%w: Invalid USER_PII_ENCRYPTION_PREVIOUS_KEYS entry format", ErrMalformedConfig)
			}
			id, key = strings.TrimSpace(id), strings.TrimSpace(key)
			if id == "" || key == "" {
				return nil, fmt.Errorf("%w: Invalid USER_PII_ENCRYPTION_PREVIOUS_KEYS entry", ErrMalformedConfig)
			}
			k, err := decodeKey(key, "USER_PII_ENCRYPTION_PREVIOUS_KEYS")
			if err != nil {
				return nil, err
			}
			previous[id] = k
		}
	}
	return NewPII(keyID, active, previous)
}

func decodeKey(value, envVar string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("%w: %s must be base64 for exactly 32 bytes", ErrMalformedConfig, envVar)
	}
	return k, nil
}
