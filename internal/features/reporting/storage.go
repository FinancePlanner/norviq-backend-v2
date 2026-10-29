package reporting

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/httpx"

	"github.com/google/uuid"
)

// Storage is LocalAdvancedReportStorage: one file per artifact under Root.
type Storage struct{ Root string }

var artifactKey = regexp.MustCompile(`^[a-f0-9-]+\.(pdf|xlsx)$`)

// Store writes data and returns its key ("<lowercase uuid>.<format>").
func (s Storage) Store(data []byte, artifactID uuid.UUID, format string) (string, error) {
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return "", err
	}
	key := artifactID.String() + "." + format
	tmp, err := os.CreateTemp(s.Root, "."+key+".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.Root, key)); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return key, nil
}

// Load reads an artifact; a key that is not one Store produced is a 400.
func (s Storage) Load(key string) ([]byte, error) {
	if !artifactKey.MatchString(key) {
		return nil, httpx.BadRequest("Invalid artifact key.")
	}
	return os.ReadFile(filepath.Join(s.Root, key))
}

// Delete removes an artifact; a missing file is not an error.
func (s Storage) Delete(key string) error {
	err := os.Remove(filepath.Join(s.Root, key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Signer is ReportDownloadSigner.
type Signer struct{ Secret string }

func (s Signer) payload(artifactID uuid.UUID, expiresAt time.Time, recipient *uuid.UUID) string {
	who := "owner"
	if recipient != nil {
		who = recipient.String()
	}
	return artifactID.String() + ":" + strconv.FormatInt(expiresAt.Unix(), 10) + ":" + who
}

// Signature is HMAC-SHA256 over the payload, base64url without padding.
func (s Signer) Signature(artifactID uuid.UUID, expiresAt time.Time, recipient *uuid.UUID) string {
	m := hmac.New(sha256.New, []byte(s.Secret))
	m.Write([]byte(s.payload(artifactID, expiresAt, recipient)))
	return strings.TrimRight(base64.URLEncoding.EncodeToString(m.Sum(nil)), "=")
}

// Verify checks expiry and the signature (ConstantTime.equals).
func (s Signer) Verify(signature string, artifactID uuid.UUID, expiresAt time.Time, recipient *uuid.UUID, now time.Time) bool {
	if !expiresAt.After(now) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(signature), []byte(s.Signature(artifactID, expiresAt, recipient))) == 1
}
