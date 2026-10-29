package identity

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/store/identitydb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

const (
	testFlightEmail     = "test@stockplan.app"
	testFlightMagicCode = "123456"
)

// AuthMFAChallengeResponse is StockPlanShared.AuthMFAChallengeResponse.
type AuthMFAChallengeResponse struct {
	ChallengeID       apijson.UUID `json:"challengeId"`
	Channel           string       `json:"channel"`
	MaskedDestination string       `json:"maskedDestination"`
	ExpiresIn         int          `json:"expiresIn"`
	ResendAvailableIn int          `json:"resendAvailableIn"`
}

type mfaVerifyRequest struct {
	ChallengeID *apijson.UUID `json:"challengeId"`
	Code        *string       `json:"code"`
}

type mfaResendRequest struct {
	ChallengeID *apijson.UUID `json:"challengeId"`
}

var sixDigits = regexp.MustCompile(`^[0-9]{6}$`)

func (s *service) testFlightApplies(email string) bool {
	return s.cfg.TestFlightBypass && strings.EqualFold(email, testFlightEmail)
}

func (s *service) mfaVerify(c *echo.Context) error {
	var p mfaVerifyRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if p.ChallengeID == nil {
		return keyNotFound("challengeId")
	}
	if p.Code == nil {
		return keyNotFound("code")
	}
	ctx := c.Request().Context()
	code := strings.TrimSpace(*p.Code)
	if !sixDigits.MatchString(code) {
		return httpx.BadRequest("Invalid verification code format.")
	}
	now := s.now()
	q := identitydb.New(s.d.DB)
	ch, err := s.activeChallenge(ctx, q, p.ChallengeID.Std(), now)
	if err != nil {
		return err
	}
	if ch.FailedAttempts >= int64(s.cfg.MFA.MaxVerifyAttempts) {
		return unauthorized("Too many invalid verification attempts.")
	}
	bypass := s.testFlightApplies(ch.Destination) && code == testFlightMagicCode
	if !bypass && ch.CodeHash != users.HashToken(code) {
		ch.FailedAttempts++
		if ch.FailedAttempts >= int64(s.cfg.MFA.MaxVerifyAttempts) {
			ch.ConsumedAt = &now
		}
		if err := s.saveChallenge(ctx, q, ch, now); err != nil {
			return err
		}
		s.log.Warn("auth.mfa verify_failed", "challenge_id", apijson.FormatUUID(ch.ID), "attempts", ch.FailedAttempts)
		return unauthorized("Invalid verification code.")
	}
	ch.ConsumedAt = &now
	if err := s.saveChallenge(ctx, q, ch, now); err != nil {
		return err
	}
	s.log.Info("auth.mfa verify_succeeded", "challenge_id", apijson.FormatUUID(ch.ID))
	u, err := s.requireUser(ctx, ch.UserID, "User not found.")
	if err != nil {
		return err
	}
	resp, err := s.makeAuthResponse(ctx, u)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

func (s *service) mfaResend(c *echo.Context) error {
	var p mfaResendRequest
	if err := httpx.Decode(c, &p); err != nil {
		return err
	}
	if p.ChallengeID == nil {
		return keyNotFound("challengeId")
	}
	ctx := c.Request().Context()
	now := s.now()
	q := identitydb.New(s.d.DB)
	ch, err := s.activeChallenge(ctx, q, p.ChallengeID.Std(), now)
	if err != nil {
		return err
	}
	if ch.ResendCount >= int64(s.cfg.MFA.MaxResends) {
		return httpx.Abort(http.StatusTooManyRequests, "Maximum resend attempts reached.")
	}
	if now.Before(ch.LastSentAt.Add(time.Duration(s.cfg.MFA.ResendCooldownSeconds) * time.Second)) {
		return httpx.Abort(http.StatusTooManyRequests, "Please wait before requesting another verification code.")
	}
	code := sixDigitCode()
	ch.CodeHash = users.HashToken(code)
	ch.ExpiresAt = now.Add(time.Duration(s.cfg.MFA.CodeTTLSeconds) * time.Second)
	ch.ResendCount++
	ch.LastSentAt = now
	ch.FailedAttempts = 0
	if err := s.saveChallenge(ctx, q, ch, now); err != nil {
		return err
	}
	s.log.Info("auth.mfa resent", "challenge_id", apijson.FormatUUID(ch.ID), "resend_count", ch.ResendCount)
	if err := s.sendMFACode(ctx, ch.Destination, code, ch.ID, ch.Purpose); err != nil {
		return err
	}
	return httpx.OK(c, s.challengeResponse(ch, now))
}

func (s *service) activeChallenge(ctx context.Context, q *identitydb.Queries, id uuid.UUID, now time.Time) (identitydb.MfaChallenge, error) {
	ch, err := q.MFAChallengeByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ch, unauthorized("Invalid or expired verification challenge.")
	}
	if err != nil {
		return ch, err
	}
	if ch.ConsumedAt != nil {
		return ch, unauthorized("Verification challenge is no longer valid.")
	}
	if !ch.ExpiresAt.After(now) {
		return ch, unauthorized("Verification challenge expired.")
	}
	return ch, nil
}

func (s *service) saveChallenge(ctx context.Context, q *identitydb.Queries, ch identitydb.MfaChallenge, now time.Time) error {
	return q.SaveMFAChallenge(ctx, identitydb.SaveMFAChallengeParams{
		ID: ch.ID, CodeHash: ch.CodeHash, ExpiresAt: ch.ExpiresAt, ConsumedAt: ch.ConsumedAt,
		FailedAttempts: ch.FailedAttempts, ResendCount: ch.ResendCount, LastSentAt: ch.LastSentAt, UpdatedAt: &now,
	})
}

// issueMFAChallenge ends any open challenge for the purpose and mails a new code.
func (s *service) issueMFAChallenge(ctx context.Context, u *users.User, purpose string) (*AuthMFAChallengeResponse, error) {
	destination := users.NormalizeEmail(u.Email)
	now := s.now()
	q := identitydb.New(s.d.DB)
	if err := q.ConsumeActiveMFAChallenges(ctx, identitydb.ConsumeActiveMFAChallengesParams{
		UserID: u.ID, Purpose: purpose, ConsumedAt: &now,
	}); err != nil {
		return nil, err
	}
	code := sixDigitCode()
	ch := identitydb.MfaChallenge{
		ID: uuid.New(), UserID: u.ID, Purpose: purpose, Channel: "email", Destination: destination,
		CodeHash: users.HashToken(code), ExpiresAt: now.Add(time.Duration(s.cfg.MFA.CodeTTLSeconds) * time.Second),
		LastSentAt: now, CreatedAt: &now,
	}
	if err := q.InsertMFAChallenge(ctx, identitydb.InsertMFAChallengeParams{
		ID: ch.ID, UserID: ch.UserID, Purpose: ch.Purpose, Channel: ch.Channel, Destination: ch.Destination,
		CodeHash: ch.CodeHash, ExpiresAt: ch.ExpiresAt, LastSentAt: ch.LastSentAt, CreatedAt: ch.CreatedAt,
	}); err != nil {
		return nil, err
	}
	if !s.testFlightApplies(destination) {
		if err := s.sendMFACode(ctx, destination, code, ch.ID, purpose); err != nil {
			return nil, err
		}
	}
	r := s.challengeResponse(ch, now)
	return &r, nil
}

func (s *service) sendMFACode(ctx context.Context, to, code string, challengeID uuid.UUID, purpose string) error {
	action := "finish signing in"
	if purpose == "oauth" {
		action = "finish sign in with your provider"
	} else {
		purpose = "login"
	}
	return s.mail.Send(ctx, mailer.Message{
		To:      to,
		Subject: "Your Norviq code is " + code,
		Body: fmt.Sprintf("Use this code to %s: %s. This code expires in %d minutes. Requested at %s.",
			action, code, s.cfg.MFA.CodeTTLSeconds/60, apijson.FormatTime(s.now())),
		Purpose:     "mfa_" + purpose,
		ChallengeID: &challengeID,
	})
}

func ceilSeconds(d time.Duration) int { return max(0, int(math.Ceil(d.Seconds()))) }

func (s *service) challengeResponse(ch identitydb.MfaChallenge, now time.Time) AuthMFAChallengeResponse {
	channel := ch.Channel
	if channel != "sms" {
		channel = "email"
	}
	cooldown := time.Duration(s.cfg.MFA.ResendCooldownSeconds) * time.Second
	return AuthMFAChallengeResponse{
		ChallengeID:       apijson.NewUUID(ch.ID),
		Channel:           channel,
		MaskedDestination: maskDestination(ch.Destination, channel),
		ExpiresIn:         ceilSeconds(ch.ExpiresAt.Sub(now)),
		ResendAvailableIn: ceilSeconds(ch.LastSentAt.Add(cooldown).Sub(now)),
	}
}

func maskDestination(destination, channel string) string {
	if channel == "sms" {
		r := []rune(destination)
		if len(r) <= 4 {
			return "••••"
		}
		return "••••" + string(r[len(r)-4:])
	}
	name, domain, ok := strings.Cut(destination, "@")
	if !ok {
		return "••••"
	}
	prefix := func(s string, n int) string {
		r := []rune(s)
		return string(r[:min(n, len(r))])
	}
	tld := ""
	if i := strings.LastIndex(domain, "."); i >= 0 {
		tld = domain[i+1:]
	} else {
		tld = domain
	}
	return prefix(name, 2) + "•••@" + prefix(domain, 1) + "•••." + tld
}
