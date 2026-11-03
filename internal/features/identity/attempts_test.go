package identity

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// wrongCodes returns n distinct six-digit codes, none equal to right.
func wrongCodes(right string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; len(out) < n; i++ {
		c := fmt.Sprintf("%06d", 100000+i)
		if c != right {
			out = append(out, c)
		}
	}
	return out
}

// burst fires every body at path at once and waits for all of them.
func (h *harness) burst(path string, bodies []map[string]any) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, b := range bodies {
		wg.Go(func() {
			<-start
			h.Do("POST", path, "", b)
		})
	}
	close(start)
	wg.Wait()
}

// C9: concurrent wrong guesses must not get more than five evaluations of
// one reset code. Each wrong guess now claims an attempt atomically, so after
// the burst the code is used up and the right code no longer works.
func TestResetAttemptCapHoldsUnderConcurrentGuesses(t *testing.T) {
	h := newHarness(t)
	for round := range 3 {
		email := fmt.Sprintf("burstreset%d@example.com", round)
		h.register(fmt.Sprintf("burstreset%d", round), email)
		r := h.Do("POST", "/v1/auth/forgot-password", "", map[string]any{"email": email})
		var fp forgotPasswordResponse
		r.JSON(t, &fp)
		var bodies []map[string]any
		for _, c := range wrongCodes(*fp.ResetCode, 25) {
			bodies = append(bodies, map[string]any{"email": email, "code": c, "newPassword": "N3w!Password"})
		}
		h.burst("/v1/auth/reset-password", bodies)
		expect(t, h.Do("POST", "/v1/auth/reset-password", "", map[string]any{"email": email, "code": *fp.ResetCode, "newPassword": "N3w!Password"}), 401, "Invalid reset code")
	}
}

// C9: the same bound for MFA challenges.
func TestMFAAttemptCapHoldsUnderConcurrentGuesses(t *testing.T) {
	h := newHarness(t, withSettings(func(s *Settings) { s.MFA.Enabled = true; s.MFA.AllowLegacyBypass = false }))
	capable := map[string]string{"X-StockPlan-Client-Capabilities": "mfa-auth-v1"}
	for round := range 3 {
		email := fmt.Sprintf("burstmfa%d@example.com", round)
		h.register(fmt.Sprintf("burstmfa%d", round), email)
		r := h.req("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": goodPassword}, capable)
		var out AuthLoginOutcome
		r.JSON(t, &out)
		code := strings.TrimPrefix(h.mail.last(t).Subject, "Your Norviq code is ")
		id := out.MFA.ChallengeID.String()
		var bodies []map[string]any
		for _, c := range wrongCodes(code, 25) {
			bodies = append(bodies, map[string]any{"challengeId": id, "code": c})
		}
		h.burst("/v1/auth/mfa/verify", bodies)
		expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": code}), 401, "Verification challenge is no longer valid.")
	}
}

// C9: a resend issues a new code but keeps the attempt count, so resending
// cannot buy more guesses against one challenge.
func TestMFAResendKeepsAttemptCount(t *testing.T) {
	now := time.Now()
	clock := &now
	h := newHarness(t, withSettings(func(s *Settings) { s.MFA.Enabled = true; s.MFA.AllowLegacyBypass = false }), func(f *Feature) {
		f.Now = func() time.Time { return *clock }
	})
	h.register("keepcount", "keepcount@example.com")
	capable := map[string]string{"X-StockPlan-Client-Capabilities": "mfa-auth-v1"}
	r := h.req("POST", "/v1/auth/login", "", map[string]any{"email": "keepcount@example.com", "password": goodPassword}, capable)
	var out AuthLoginOutcome
	r.JSON(t, &out)
	id := out.MFA.ChallengeID.String()
	code := strings.TrimPrefix(h.mail.last(t).Subject, "Your Norviq code is ")
	for _, c := range wrongCodes(code, 4) {
		expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": c}), 401, "Invalid verification code.")
	}
	*clock = clock.Add(31 * time.Second)
	expect(t, h.Do("POST", "/v1/auth/mfa/resend", "", map[string]any{"challengeId": id}), 200, "")
	code = strings.TrimPrefix(h.mail.last(t).Subject, "Your Norviq code is ")
	// The fifth failure overall consumes the challenge.
	expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": wrongCodes(code, 1)[0]}), 401, "Invalid verification code.")
	expect(t, h.Do("POST", "/v1/auth/mfa/verify", "", map[string]any{"challengeId": id, "code": code}), 401, "Verification challenge is no longer valid.")
}
