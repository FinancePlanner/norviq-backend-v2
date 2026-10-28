package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/identitydb"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// webAuthnConfig is WebAuthnConfig.
type webAuthnConfig struct {
	RPID    string
	RPName  string
	Origins []string
}

// webAuthnFromEnv is WebAuthnConfig.fromEnvironment: nil disables passkeys.
func webAuthnFromEnv() *webAuthnConfig {
	rpID := strings.TrimSpace(os.Getenv("WEBAUTHN_RP_ID"))
	if rpID == "" {
		return nil
	}
	name := strings.TrimSpace(os.Getenv("WEBAUTHN_RP_NAME"))
	if name == "" {
		name = "Norviq"
	}
	raw := strings.TrimSpace(os.Getenv("WEBAUTHN_ORIGINS"))
	if raw == "" {
		raw = "http://localhost:6969,http://localhost:7000"
	}
	var origins []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	if len(origins) == 0 {
		return nil
	}
	return &webAuthnConfig{RPID: rpID, RPName: name, Origins: origins}
}

// coseAlgorithms is swift-webauthn's COSEAlgorithmIdentifier.allCases, the
// order beginRegistration advertises them in.
var coseAlgorithms = []int64{-7, -35, -36, -65535, -257, -258, -259, -37, -38, -39}

const webAuthnChallengeTTL = 5 * time.Minute

type loginOptions struct {
	Challenge        string `json:"challenge"`
	Timeout          int    `json:"timeout"`
	RPID             string `json:"rpId"`
	UserVerification string `json:"userVerification"`
}

type credParam struct {
	Type string `json:"type"`
	Alg  int64  `json:"alg"`
}

type creationOptions struct {
	Challenge string `json:"challenge"`
	User      struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	} `json:"user"`
	RP struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"rp"`
	PubKeyCredParams []credParam `json:"pubKeyCredParams"`
	Timeout          int         `json:"timeout"`
	Attestation      string      `json:"attestation"`
}

func (s *service) webAuthnOrigin(c *echo.Context) (string, error) {
	cfg := s.webauthn
	if o := strings.TrimSpace(c.Request().Header.Get("Origin")); o != "" && slices.Contains(cfg.Origins, o) {
		return o, nil
	}
	if len(cfg.Origins) == 1 {
		return cfg.Origins[0], nil
	}
	return "", httpx.BadRequest("Missing or disallowed Origin header for WebAuthn.")
}

func newChallenge() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

func (s *service) webAuthnLoginOptions(c *echo.Context) error {
	if s.webauthn == nil {
		return httpx.Abort(http.StatusServiceUnavailable, "Passkey sign-in is not enabled on the server.")
	}
	if _, err := s.webAuthnOrigin(c); err != nil {
		return err
	}
	challenge := newChallenge()
	now := s.now()
	if err := identitydb.New(s.d.DB).InsertWebAuthnLoginChallenge(c.Request().Context(), identitydb.InsertWebAuthnLoginChallengeParams{
		ID: uuid.New(), Challenge: challenge, ExpiresAt: now.Add(webAuthnChallengeTTL), CreatedAt: now,
	}); err != nil {
		return err
	}
	return httpx.OK(c, map[string]any{"publicKey": loginOptions{
		Challenge: base64.RawURLEncoding.EncodeToString(challenge), Timeout: 60000,
		RPID: s.webauthn.RPID, UserVerification: "preferred",
	}})
}

// decodeChallenge is URLEncodedBase64(challenge).urlDecoded.decoded.
func decodeChallenge(raw string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "="))
	if err != nil {
		return nil, httpx.BadRequest("Invalid WebAuthn challenge encoding.")
	}
	return b, nil
}

func readBody(c *echo.Context) ([]byte, error) {
	return io.ReadAll(io.LimitReader(c.Request().Body, 1<<20))
}

func webAuthnBadRequest(err error) error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return httpx.BadRequest(pe.Details)
	}
	return httpx.BadRequest(err.Error())
}

func (s *service) webAuthnLoginVerify(c *echo.Context) error {
	if s.webauthn == nil {
		return httpx.Abort(http.StatusServiceUnavailable, "Passkey sign-in is not enabled on the server.")
	}
	origin, err := s.webAuthnOrigin(c)
	if err != nil {
		return err
	}
	body, err := readBody(c)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return webAuthnBadRequest(err)
	}
	ctx := c.Request().Context()
	q := identitydb.New(s.d.DB)
	// Vapor looks the credential up by the id exactly as the client sent it
	// (base64url), while registration stored the standard-base64 spelling.
	// Ported as is: see the deviations note in the port report.
	stored, err := q.WebAuthnCredentialByCredentialID(ctx, parsed.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return unauthorized("Unknown passkey.")
	}
	if err != nil {
		return err
	}
	challenge, err := decodeChallenge(parsed.Response.CollectedClientData.Challenge)
	if err != nil {
		return err
	}
	row, err := q.WebAuthnLoginChallengeByBytes(ctx, challenge)
	if errors.Is(err, pgx.ErrNoRows) {
		return unauthorized("Passkey challenge expired or invalid.")
	}
	if err != nil {
		return err
	}
	if !row.ExpiresAt.After(s.now()) {
		_ = q.DeleteWebAuthnLoginChallenge(ctx, row.ID)
		return unauthorized("Passkey challenge expired or invalid.")
	}
	if err := parsed.Verify(base64.RawURLEncoding.EncodeToString(challenge), s.webauthn.RPID, "",
		[]string{origin}, nil, protocol.TopOriginDefaultVerificationMode, true, false, true, stored.PublicKey); err != nil {
		return err
	}
	newCount := parsed.Response.AuthenticatorData.Counter
	current := uint32(stored.SignCount)
	if (newCount > 0 || current > 0) && newCount <= current {
		return errors.New("potentialReplayAttack")
	}
	now := s.now()
	if err := q.SetWebAuthnSignCount(ctx, identitydb.SetWebAuthnSignCountParams{ID: stored.ID, SignCount: int32(newCount), UpdatedAt: &now}); err != nil {
		return err
	}
	if err := q.DeleteWebAuthnLoginChallenge(ctx, row.ID); err != nil {
		return err
	}
	u, err := s.requireUser(ctx, stored.UserID, "Passkey user not found.")
	if err != nil {
		return err
	}
	resp, err := s.makeAuthResponse(ctx, u)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (s *service) webAuthnRegisterOptions(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, sess.UserID, "Unauthorized")
	if err != nil {
		return err
	}
	if s.webauthn == nil {
		return httpx.Abort(http.StatusServiceUnavailable, "Passkey registration is not enabled on the server.")
	}
	if _, err := s.webAuthnOrigin(c); err != nil {
		return err
	}
	challenge := newChallenge()
	now := s.now()
	if err := identitydb.New(s.d.DB).InsertWebAuthnRegisterChallenge(ctx, identitydb.InsertWebAuthnRegisterChallengeParams{
		ID: uuid.New(), UserID: u.ID, Challenge: challenge, ExpiresAt: now.Add(webAuthnChallengeTTL), CreatedAt: now,
	}); err != nil {
		return err
	}
	opts := creationOptions{
		Challenge: base64.RawURLEncoding.EncodeToString(challenge), Timeout: 300000, Attestation: "none",
		PubKeyCredParams: make([]credParam, len(coseAlgorithms)),
	}
	opts.User.ID = base64.RawURLEncoding.EncodeToString([]byte(apijson.FormatUUID(u.ID)))
	opts.User.Name = u.Email
	opts.User.DisplayName = u.Email
	if u.Username != nil {
		opts.User.DisplayName = *u.Username
	}
	opts.RP.ID, opts.RP.Name = s.webauthn.RPID, s.webauthn.RPName
	for i, alg := range coseAlgorithms {
		opts.PubKeyCredParams[i] = credParam{Type: "public-key", Alg: alg}
	}
	return httpx.OK(c, map[string]any{"publicKey": opts})
}

func (s *service) webAuthnRegisterVerify(c *echo.Context) error {
	sess, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	u, err := s.requireUser(ctx, sess.UserID, "Unauthorized")
	if err != nil {
		return err
	}
	if s.webauthn == nil {
		return httpx.Abort(http.StatusServiceUnavailable, "Passkey registration is not enabled on the server.")
	}
	origin, err := s.webAuthnOrigin(c)
	if err != nil {
		return err
	}
	body, err := readBody(c)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return webAuthnBadRequest(err)
	}
	challenge, err := decodeChallenge(parsed.Response.CollectedClientData.Challenge)
	if err != nil {
		return err
	}
	q := identitydb.New(s.d.DB)
	row, err := q.WebAuthnRegisterChallengeByBytes(ctx, identitydb.WebAuthnRegisterChallengeByBytesParams{Challenge: challenge, UserID: u.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return unauthorized("Passkey challenge expired or invalid.")
	}
	if err != nil {
		return err
	}
	if !row.ExpiresAt.After(s.now()) {
		_ = q.DeleteWebAuthnRegisterChallenge(ctx, row.ID)
		return unauthorized("Passkey challenge expired or invalid.")
	}
	params := make([]protocol.CredentialParameter, len(coseAlgorithms))
	for i, alg := range coseAlgorithms {
		params[i] = protocol.CredentialParameter{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.COSEAlgorithmIdentifier(alg)}
	}
	if _, err := parsed.Verify(base64.RawURLEncoding.EncodeToString(challenge), s.webauthn.RPID,
		[]string{origin}, nil, protocol.TopOriginDefaultVerificationMode, true, false, true, nil, params); err != nil {
		return err
	}
	// swift-webauthn stores the id as `urlDecoded`: standard base64, padded.
	credentialID := base64.StdEncoding.EncodeToString(parsed.RawID)
	if _, err := q.WebAuthnCredentialByCredentialID(ctx, credentialID); err == nil {
		return errors.New("credentialIDAlreadyExists")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	att := parsed.Response.AttestationObject.AuthData
	now := s.now()
	if err := q.InsertWebAuthnCredential(ctx, identitydb.InsertWebAuthnCredentialParams{
		ID: uuid.New(), UserID: u.ID, CredentialID: credentialID, PublicKey: att.AttData.CredentialPublicKey,
		SignCount: int32(att.Counter), CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := q.DeleteWebAuthnRegisterChallenge(ctx, row.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusOK)
}
