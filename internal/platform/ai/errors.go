package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"norviq-api/internal/httpx"
)

var errTrailingData = errors.New("trailing data after JSON value")

// unavailableReason is the message every upstream failure surfaced as, so a
// caller of a single client or of the whole chain saw the same 502.
const unavailableReason = "AI service is unavailable. Please try again later."

// DisabledReason is DisabledOpenAIChatClient's 503 and the reason an
// exhausted chain with nothing tried reports.
const DisabledReason = "AI insights are not enabled on this server."

// UpstreamError is OpenAIChatUpstreamError: a non-2xx from a provider (or an
// Anthropic refusal, recorded as 200). It renders as Vapor's 502 with the
// generic reason; only code that inspects Status sees more.
type UpstreamError struct {
	Status int
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("ai provider returned status %d", e.Status)
}

// Unwrap exposes the HTTP surface, so httpx.ErrorHandler renders the 502.
func (e *UpstreamError) Unwrap() error {
	return httpx.Abort(http.StatusBadGateway, unavailableReason)
}

// IsAuthFailure is a rejected key (401 or 403).
func (e *UpstreamError) IsAuthFailure() bool { return e.Status == 401 || e.Status == 403 }

// IsRateLimit is a 429.
func (e *UpstreamError) IsRateLimit() bool { return e.Status == 429 }

// IsOutOfCredits is OpenRouter's zero-balance 402 (and Anthropic's spent
// workspace limit, reclassified as 402).
func (e *UpstreamError) IsOutOfCredits() bool { return e.Status == 402 }

// UnusableResponseError is OpenAIChatUnusableResponseError: a 200 with
// neither content nor a tool call.
type UnusableResponseError struct {
	Model string
}

func (e *UnusableResponseError) Error() string {
	return "ai provider returned no usable output (model " + e.Model + ")"
}

// Unwrap exposes the same 502 surface as UpstreamError.
func (e *UnusableResponseError) Unwrap() error {
	return httpx.Abort(http.StatusBadGateway, unavailableReason)
}

// AsUpstream returns the UpstreamError in err's chain, if any.
func AsUpstream(err error) (*UpstreamError, bool) {
	var up *UpstreamError
	if errors.As(err, &up) {
		return up, true
	}
	return nil, false
}

// statusLabel is FallbackChatClient.statusLabel: a short, log-safe
// description of why a rung was demoted.
func statusLabel(err error) string {
	if err == nil {
		return "none"
	}
	var unusable *UnusableResponseError
	if errors.As(err, &unusable) {
		return "200_no_usable_output"
	}
	if up, ok := AsUpstream(err); ok {
		switch {
		case up.IsOutOfCredits():
			return "402_out_of_credits"
		case up.IsAuthFailure():
			return fmt.Sprintf("%d_auth", up.Status)
		case up.IsRateLimit():
			return "429_rate_limited"
		}
		return fmt.Sprint(up.Status)
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "transport"
}

func errNoResult() error {
	return httpx.Abort(http.StatusBadGateway, "AI service returned no result.")
}

func errUnreadable() error {
	return httpx.Abort(http.StatusBadGateway, "AI service returned an unreadable response.")
}

func errDisabled() error {
	return httpx.Abort(http.StatusServiceUnavailable, DisabledReason)
}
