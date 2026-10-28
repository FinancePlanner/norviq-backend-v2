package users

import (
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is Vapor's `app.passwords.use(.bcrypt(cost: 11))`.
const BcryptCost = 11

// bcrypt only reads the first 72 bytes. Vapor's implementation truncates
// silently; Go's refuses longer input, so truncate the same way.
func truncate72(password string) []byte {
	b := []byte(password)
	if len(b) > 72 {
		b = b[:72]
	}
	return b
}

// HashPassword returns a `$2b$11$...` hash, the revision Vapor writes. Go's
// `$2a$` is the same algorithm; the prefix only keeps stored rows uniform.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(truncate72(password), BcryptCost)
	if err != nil {
		return "", err
	}
	s := string(h)
	if strings.HasPrefix(s, "$2a$") {
		s = "$2b$" + s[4:]
	}
	return s, nil
}

// VerifyPassword checks password against a Vapor (`$2b$`, `$2a$`, `$2y$`) or
// Go hash. A malformed hash is an error, as in Vapor.
func VerifyPassword(password, hash string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(hash), truncate72(password))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return false, nil
	default:
		return false, err
	}
}
