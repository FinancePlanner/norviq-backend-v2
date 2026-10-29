package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

// Produced by CryptoKit (AES.GCM.seal + JSONEncoder, as UserPIIEncryption.swift
// does) under Vapor's non-production key "dev-default". Note the Swift key
// order and the `\/` escaping: readers must parse, not compare bytes.
const (
	swiftBio = `{"version":1,"combinedCiphertext":"UHfaJ2xk+SXwSCrYxD9mY6dnvUAwqSRFU54t0UUdx8P64eJrpnL8zq0Skobd251XZg==","keyID":"dev-default"}`
	swiftDOB = `{"version":1,"combinedCiphertext":"mQPqN\/cdjqjXt5HvJxcTnbFaWA4C+7pT+d7t9r51JcDkMo1JMwsyEQ==","keyID":"dev-default"}`
	// AESGCMTokenEncryptionService output for two contexts.
	swiftBrokerToken = "nvq-enc:eyJ2ZXJzaW9uIjoyLCJjb21iaW5lZENpcGhlcnRleHQiOiIyWVZ5bitYdVwvWFc0MjUzUXA4Q0kxNWhjTHlkdnpxb1pKK0d2MndrNCtEejdMRnBVRTNkRWdyVXBNbFZwVnhLODBHVFoiLCJrZXlJRCI6ImRldi1kZWZhdWx0In0="
	swiftAIToken     = "nvq-enc:eyJ2ZXJzaW9uIjoyLCJjb21iaW5lZENpcGhlcnRleHQiOiJ2NmNNUzFSQVdIdVV3ZURwb2NITHA3WkxBK1EwcFRmMU14YkFBOUdVUklpQWtZQUp5UXJDd2ZRK3BnPT0iLCJrZXlJRCI6ImRldi1kZWZhdWx0In0="
)

func devPII(t *testing.T) *PII {
	t.Helper()
	p, err := piiFromLookup(false, nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDecryptsCryptoKitCiphertext(t *testing.T) {
	p := devPII(t)
	for raw, want := range map[string]string{swiftBio: "Hello, Norviq bio ✓", swiftDOB: "946684800000"} {
		got, err := p.DecryptString([]byte(raw))
		if err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	}
	v := NewVault(p)
	if got, err := v.Decrypt(swiftBrokerToken, ContextBroker); err != nil || got != "broker-access-token-123" {
		t.Errorf("broker: %q %v", got, err)
	}
	if got, err := v.Decrypt(swiftAIToken, ContextAIProvider); err != nil || got != "sk-ant-test-key" {
		t.Errorf("ai: %q %v", got, err)
	}
}

func TestEnvelopeShapeMatchesSwift(t *testing.T) {
	out, err := devPII(t).EncryptString("x")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte(`{"version":1,"keyID":"dev-default","combinedCiphertext":"`)) {
		t.Errorf("envelope %s", out)
	}
	// 12-byte nonce + 1 byte + 16-byte tag.
	var e envelope
	if err := jsonUnmarshal(out, &e); err != nil {
		t.Fatal(err)
	}
	combined, _ := base64.StdEncoding.DecodeString(e.CombinedCiphertext)
	if len(combined) != 12+1+16 {
		t.Errorf("combined length %d", len(combined))
	}
}

// Ported from UserPIIEncryptionTests.swift.

func TestRoundTrip(t *testing.T) {
	p := devPII(t)
	enc, err := p.EncryptString("1990-05-12")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := p.DecryptString(enc); err != nil || got != "1990-05-12" {
		t.Errorf("got %q %v", got, err)
	}
}

func TestSameValueEncryptsDifferently(t *testing.T) {
	p := devPII(t)
	a, _ := p.EncryptString("same")
	b, _ := p.EncryptString("same")
	if bytes.Equal(a, b) {
		t.Error("nonce reuse")
	}
}

func TestWrongKeyFails(t *testing.T) {
	a, _ := NewPII("k", bytes.Repeat([]byte{1}, 32), nil)
	b, _ := NewPII("k", bytes.Repeat([]byte{2}, 32), nil)
	enc, _ := a.EncryptString("secret")
	if _, err := b.DecryptString(enc); !errors.Is(err, ErrDecryptionFailed) {
		t.Errorf("err %v", err)
	}
	other, _ := NewPII("other", bytes.Repeat([]byte{2}, 32), nil)
	var unknown UnknownKeyError
	if _, err := other.DecryptString(enc); !errors.As(err, &unknown) || unknown.KeyID != "k" {
		t.Errorf("err %v", err)
	}
}

func TestKeyRotation(t *testing.T) {
	oldKey := bytes.Repeat([]byte{7}, 32)
	old, _ := NewPII("v1", oldKey, nil)
	enc, _ := old.EncryptString("legacy")
	b64 := base64.StdEncoding.EncodeToString(oldKey)
	env := map[string]string{
		"USER_PII_ENCRYPTION_ACTIVE_KEY_ID": "v2",
		"USER_PII_ENCRYPTION_ACTIVE_KEY":    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
		"USER_PII_ENCRYPTION_PREVIOUS_KEYS": " v1:" + b64 + " ,",
	}
	rotated, err := piiFromLookup(true, nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rotated.DecryptString(enc); err != nil || got != "legacy" {
		t.Errorf("got %q %v", got, err)
	}
	fresh, _ := rotated.EncryptString("new")
	if !bytes.Contains(fresh, []byte(`"keyID":"v2"`)) {
		t.Errorf("not sealed with the active key: %s", fresh)
	}
}

func TestConfigErrors(t *testing.T) {
	if _, err := piiFromLookup(true, nil, func(string) string { return "" }); !errors.Is(err, ErrMissingConfig) {
		t.Errorf("production without key: %v", err)
	}
	env := map[string]string{"USER_PII_ENCRYPTION_ACTIVE_KEY_ID": "a", "USER_PII_ENCRYPTION_ACTIVE_KEY": "c2hvcnQ="}
	if _, err := piiFromLookup(false, nil, func(k string) string { return env[k] }); !errors.Is(err, ErrMalformedConfig) {
		t.Errorf("short key: %v", err)
	}
}

func TestEnvelopeErrors(t *testing.T) {
	p := devPII(t)
	for _, bad := range []string{"", "{}", `{"version":2,"keyID":"dev-default","combinedCiphertext":"AA=="}`, `{"version":1,"keyID":"dev-default","combinedCiphertext":"AA=="}`} {
		if _, err := p.DecryptString([]byte(bad)); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// Ported from CredentialEncryptionTests.swift.

func TestVault(t *testing.T) {
	v := NewVault(devPII(t))
	enc, err := v.Encrypt("access-token", ContextBroker)
	if err != nil || !IsEncrypted(enc) {
		t.Fatalf("%q %v", enc, err)
	}
	if got, err := v.Decrypt(enc, ContextBroker); err != nil || got != "access-token" {
		t.Errorf("round trip: %q %v", got, err)
	}
	if _, err := v.Decrypt(enc, ContextAIProvider); !errors.Is(err, ErrDecryptionFailed) {
		t.Errorf("cross-context open must fail: %v", err)
	}
	if got, err := v.Decrypt("legacy-plain", ContextBankPlaid); err != nil || got != "legacy-plain" {
		t.Errorf("legacy: %q %v", got, err)
	}
	if _, err := v.Decrypt("nvq-enc:%%%", ContextBroker); !errors.Is(err, ErrMalformedStoredValue) {
		t.Errorf("malformed: %v", err)
	}
	if ContextAIProvider.AuthenticatedData() != "norviq.credential.ai_provider" {
		t.Error("aad")
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
