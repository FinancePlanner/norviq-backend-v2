package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// A software authenticator: "none" attestation, ES256, enough to drive
// registration and sign-in end to end without a browser.

const (
	rpID   = "localhost"
	origin = "http://localhost:6969"
)

var b64 = base64.RawURLEncoding

type softKey struct {
	key    *ecdsa.PrivateKey
	credID []byte
	count  uint32
}

func newSoftKey(t *testing.T, credID []byte) *softKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &softKey{key: k, credID: credID}
}

func clientData(typ string, challenge string) []byte {
	raw, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin})
	return raw
}

func (s *softKey) attestation(t *testing.T, challenge string) map[string]any {
	t.Helper()
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: s.key.X.FillBytes(make([]byte, 32)), -3: s.key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	rp := sha256.Sum256([]byte(rpID))
	auth := append(rp[:], 0x41, 0, 0, 0, 0)
	auth = append(auth, make([]byte, 16)...)
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(s.credID)))
	auth = append(auth, s.credID...)
	auth = append(auth, cose...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": b64.EncodeToString(s.credID), "rawId": b64.EncodeToString(s.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(clientData("webauthn.create", challenge)),
			"attestationObject": b64.EncodeToString(att),
		},
	}
}

func (s *softKey) assertion(t *testing.T, challenge string) map[string]any {
	t.Helper()
	s.count++
	rp := sha256.Sum256([]byte(rpID))
	auth := binary.BigEndian.AppendUint32(append(rp[:], 0x01), s.count)
	cd := clientData("webauthn.get", challenge)
	sum := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, auth...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, s.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": b64.EncodeToString(s.credID), "rawId": b64.EncodeToString(s.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(auth),
			"signature": b64.EncodeToString(sig),
		},
	}
}

func webAuthnOn(s *Settings) {
	s.WebAuthn = &webAuthnConfig{RPID: rpID, RPName: "Norviq", Origins: []string{origin}}
}

func TestWebAuthnDisabled(t *testing.T) {
	h := newHarness(t)
	a := h.register("nopasskey", "nopasskey@example.com")
	expect(t, h.Do("POST", "/v1/auth/webauthn/login/options", "", nil), 503, "Passkey sign-in is not enabled on the server.")
	expect(t, h.Do("POST", "/v1/auth/webauthn/login/verify", "", map[string]any{}), 503, "Passkey sign-in is not enabled on the server.")
	expect(t, h.Do("POST", "/v1/auth/webauthn/register/options", a.Token, nil), 503, "Passkey registration is not enabled on the server.")
	expect(t, h.Do("POST", "/v1/auth/webauthn/register/verify", a.Token, map[string]any{}), 503, "Passkey registration is not enabled on the server.")
	expect(t, h.Do("POST", "/v1/auth/webauthn/register/options", "", nil), 401, "")
}

func TestWebAuthnRegisterAndSignIn(t *testing.T) {
	h := newHarness(t, withSettings(webAuthnOn))
	a := h.register("passkey", "passkey@example.com")

	r := h.Do("POST", "/v1/auth/webauthn/register/options", a.Token, nil)
	var reg struct{ PublicKey creationOptions }
	r.JSON(t, &reg)
	uid, _ := b64.DecodeString(reg.PublicKey.User.ID)
	if string(uid) != a.UserID.String() || reg.PublicKey.User.DisplayName != "passkey" || reg.PublicKey.RP.ID != rpID ||
		reg.PublicKey.Timeout != 300000 || reg.PublicKey.Attestation != "none" || len(reg.PublicKey.PubKeyCredParams) != 10 {
		t.Fatalf("%s", r.Body)
	}
	// 18 zero bytes spell the same in base64 and base64url, so Vapor's
	// mismatched lookup (see webAuthnLoginVerify) still finds it.
	key := newSoftKey(t, make([]byte, 18))
	expect(t, h.req("POST", "/v1/auth/webauthn/register/verify", a.Token, key.attestation(t, b64.EncodeToString([]byte("not-issued"))), nil), 401, "Passkey challenge expired or invalid.")
	r = h.req("POST", "/v1/auth/webauthn/register/verify", a.Token, key.attestation(t, reg.PublicKey.Challenge), map[string]string{"Origin": origin})
	if r.Status != 200 || len(r.Body) != 0 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	r = h.Do("POST", "/v1/auth/webauthn/login/options", "", nil)
	var login struct{ PublicKey loginOptions }
	r.JSON(t, &login)
	if login.PublicKey.RPID != rpID || login.PublicKey.Timeout != 60000 || login.PublicKey.UserVerification != "preferred" {
		t.Fatalf("%s", r.Body)
	}
	r = h.Do("POST", "/v1/auth/webauthn/login/verify", "", key.assertion(t, login.PublicKey.Challenge))
	expect(t, r, 200, "")
	var out AuthResponse
	r.JSON(t, &out)
	if out.UserID != a.UserID {
		t.Fatalf("%s", r.Body)
	}
	// The challenge is single-use.
	expect(t, h.Do("POST", "/v1/auth/webauthn/login/verify", "", key.assertion(t, login.PublicKey.Challenge)), 401, "Passkey challenge expired or invalid.")
	var count int32
	_ = h.Deps.DB.QueryRow(context.Background(), `SELECT sign_count FROM webauthn_credentials WHERE user_id=$1`, a.UserID.Std()).Scan(&count)
	if count != 1 {
		t.Fatalf("sign count %d", count)
	}
}

func TestWebAuthnStoresStandardBase64CredentialID(t *testing.T) {
	h := newHarness(t, withSettings(webAuthnOn))
	a := h.register("passkey2", "passkey2@example.com")
	r := h.Do("POST", "/v1/auth/webauthn/register/options", a.Token, nil)
	var reg struct{ PublicKey creationOptions }
	r.JSON(t, &reg)
	id := []byte{0xfb, 0xff, 0xfe, 0x01} // base64 "+//+AQ==", base64url "-__-AQ"
	key := newSoftKey(t, id)
	expect(t, h.Do("POST", "/v1/auth/webauthn/register/verify", a.Token, key.attestation(t, reg.PublicKey.Challenge)), 200, "")
	var stored string
	_ = h.Deps.DB.QueryRow(context.Background(), `SELECT credential_id FROM webauthn_credentials WHERE user_id=$1`, a.UserID.Std()).Scan(&stored)
	if stored != "+//+AQ==" {
		t.Fatalf("stored %q", stored)
	}
	// Faithful to Vapor: sign-in looks the credential up by its base64url id.
	r = h.Do("POST", "/v1/auth/webauthn/login/options", "", nil)
	var login struct{ PublicKey loginOptions }
	r.JSON(t, &login)
	expect(t, h.Do("POST", "/v1/auth/webauthn/login/verify", "", key.assertion(t, login.PublicKey.Challenge)), 401, "Unknown passkey.")
}

func TestWebAuthnOrigin(t *testing.T) {
	h := newHarness(t, withSettings(func(s *Settings) {
		s.WebAuthn = &webAuthnConfig{RPID: rpID, RPName: "Norviq", Origins: []string{origin, "http://localhost:7000"}}
	}))
	expect(t, h.Do("POST", "/v1/auth/webauthn/login/options", "", nil), 400, "Missing or disallowed Origin header for WebAuthn.")
	expect(t, h.req("POST", "/v1/auth/webauthn/login/options", "", nil, map[string]string{"Origin": "http://localhost:7000"}), 200, "")
	t.Setenv("WEBAUTHN_RP_ID", "norviqa.io")
	t.Setenv("WEBAUTHN_ORIGINS", " https://norviqa.io , ")
	if c := webAuthnFromEnv(); c == nil || c.RPName != "Norviq" || len(c.Origins) != 1 {
		t.Fatalf("%+v", c)
	}
	t.Setenv("WEBAUTHN_RP_ID", "")
	if webAuthnFromEnv() != nil {
		t.Fatal("no RP id disables passkeys")
	}
}
