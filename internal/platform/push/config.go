package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"strings"
)

// Config is APNSBootstrapConfiguration: token-based (.p8) APNS credentials.
type Config struct {
	TeamID       string
	KeyID        string
	PrivateKeyP8 string
	Topic        string
}

// ConfigFromEnv reads APNS_TEAM_ID, APNS_KEY_ID, APNS_PRIVATE_KEY_P8 and
// APNS_TOPIC, trimmed. A literal `\n` in the key becomes a newline, so the
// PEM survives single-line secret stores. It returns nil unless all four are
// set: push delivery is then disabled.
func ConfigFromEnv() *Config {
	get := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	cfg := Config{
		TeamID:       get("APNS_TEAM_ID"),
		KeyID:        get("APNS_KEY_ID"),
		Topic:        get("APNS_TOPIC"),
		PrivateKeyP8: strings.ReplaceAll(get("APNS_PRIVATE_KEY_P8"), `\n`, "\n"),
	}
	if cfg.TeamID == "" || cfg.KeyID == "" || cfg.Topic == "" || cfg.PrivateKeyP8 == "" {
		return nil
	}
	return &cfg
}

// PrivateKey parses the .p8 key (PKCS#8, or SEC 1 "EC PRIVATE KEY") as a
// P-256 signing key, as P256.Signing.PrivateKey(pemRepresentation:) did.
func (c Config) PrivateKey() (*ecdsa.PrivateKey, error) {
	return ParsePrivateKey(c.PrivateKeyP8)
}

// ParsePrivateKey parses a PEM P-256 private key.
func ParsePrivateKey(raw string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("apns: private key is not PEM")
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key = k
	default:
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		k, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("apns: private key is not an ECDSA key")
		}
		key = k
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("apns: private key is not P-256")
	}
	return key, nil
}
