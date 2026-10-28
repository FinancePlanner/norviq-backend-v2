package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/netguard"
)

// User-supplied provider kinds (UserAIProvider).
const (
	UserProviderAnthropic  = "anthropic"
	UserProviderOpenAI     = "openai"
	UserProviderCompatible = "compatible"
)

// UserProviderDefaultBaseURL is UserAIProvider.defaultBaseURL; ok is false
// for "compatible", which needs an explicit endpoint, and for unknown kinds.
func UserProviderDefaultBaseURL(provider string) (string, bool) {
	switch provider {
	case UserProviderAnthropic:
		return "https://api.anthropic.com/v1", true
	case UserProviderOpenAI:
		return openAIBaseURL, true
	}
	return "", false
}

// IsUserProvider reports a known UserAIProvider raw value.
func IsUserProvider(provider string) bool {
	switch provider {
	case UserProviderAnthropic, UserProviderOpenAI, UserProviderCompatible:
		return true
	}
	return false
}

// Verify error codes (AIProviderVerifyErrorCode): stable codes, never
// provider error text, which can echo the key back.
const (
	VerifyInvalidKey  = "invalid_key"
	VerifyRestricted  = "restricted"
	VerifyRateLimited = "rate_limited"
	VerifyUnreachable = "unreachable"
)

// VerifyResult is AIProviderVerifyResult.
type VerifyResult struct {
	OK        bool
	ErrorCode string // "" when OK
	Detail    string
	LatencyMs int
}

// IsInconclusive: rate limiting and unreachability prove nothing about the
// key, so they must not brand it invalid.
func (r VerifyResult) IsInconclusive() bool {
	return r.ErrorCode == VerifyRateLimited || r.ErrorCode == VerifyUnreachable
}

// CredentialVerifier is AIProviderCredentialVerifier. A returned error is an
// HTTP error for the route (a blocked or missing base URL); provider
// failures are results, not errors.
type CredentialVerifier interface {
	Verify(ctx context.Context, provider, apiKey string, baseURL, defaultModel *string) (VerifyResult, error)
}

// DisabledVerifier is DisabledAIProviderCredentialVerifier.
type DisabledVerifier struct{}

// Verify reports verification as disabled.
func (DisabledVerifier) Verify(context.Context, string, string, *string, *string) (VerifyResult, error) {
	return VerifyResult{ErrorCode: VerifyUnreachable, Detail: "Credential verification is disabled."}, nil
}

// HTTPVerifier is DefaultAIProviderCredentialVerifier: the cheapest
// authenticated call each provider offers (GET /models, no inference tokens),
// with a one-token completion for compatible gateways without /models.
type HTTPVerifier struct {
	// Timeout is per call; Vapor always used 10s (its
	// AI_USER_CREDENTIAL_VERIFY_TIMEOUT_SECONDS setting was never wired).
	Timeout time.Duration
	HTTP    *http.Client
	Logger  *slog.Logger
}

func (v *HTTPVerifier) client() *http.Client {
	if v.HTTP != nil {
		return v.HTTP
	}
	return http.DefaultClient
}

func (v *HTTPVerifier) logger() *slog.Logger {
	if v.Logger != nil {
		return v.Logger
	}
	return slog.Default()
}

func (v *HTTPVerifier) timeout() time.Duration {
	if v.Timeout > 0 {
		return v.Timeout
	}
	return 10 * time.Second
}

// Verify checks a key.
func (v *HTTPVerifier) Verify(ctx context.Context, provider, apiKey string, baseURL, defaultModel *string) (VerifyResult, error) {
	started := time.Now()
	elapsed := func() int { return int(time.Since(started) / time.Millisecond) }

	base, err := resolveVerifyBaseURL(provider, baseURL)
	if err != nil {
		return VerifyResult{}, err
	}

	result, err := v.verifyModels(ctx, provider, apiKey, base, defaultModel, started)
	if err != nil {
		v.logger().Warn("ai_credential_verify_unreachable provider=" + provider)
		return VerifyResult{ErrorCode: VerifyUnreachable, Detail: "Could not reach the provider.", LatencyMs: elapsed()}, nil
	}
	return result, nil
}

func (v *HTTPVerifier) verifyModels(ctx context.Context, provider, apiKey, base string, defaultModel *string, started time.Time) (VerifyResult, error) {
	elapsed := func() int { return int(time.Since(started) / time.Millisecond) }
	reqCtx, cancel := context.WithTimeout(ctx, v.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/models", http.NoBody)
	if err != nil {
		return VerifyResult{}, err
	}
	if provider == UserProviderAnthropic {
		// Anthropic's native /models needs both headers; a missing version
		// header is a 400 easily mistaken for a bad key.
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", AnthropicAPIVersion)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return VerifyResult{}, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if err != nil {
		return VerifyResult{}, err
	}

	status := resp.StatusCode
	switch {
	case status >= 200 && status < 300:
		return VerifyResult{OK: true, Detail: "Key accepted.", LatencyMs: elapsed()}, nil
	case status == 401:
		v.logFailure(provider, apiKey, status, body)
		return VerifyResult{ErrorCode: VerifyInvalidKey, Detail: "The provider rejected this key.", LatencyMs: elapsed()}, nil
	case status == 403:
		v.logFailure(provider, apiKey, status, body)
		return VerifyResult{ErrorCode: VerifyRestricted, Detail: "The key is valid but not permitted to use this endpoint.", LatencyMs: elapsed()}, nil
	case status == 429:
		return VerifyResult{ErrorCode: VerifyRateLimited, Detail: "The provider is rate limiting us; the key is probably fine.", LatencyMs: elapsed()}, nil
	case (status == 404 || status == 405) && provider == UserProviderCompatible:
		// Plenty of self-hosted gateways never implement /models.
		return v.probeChatCompletion(ctx, base, apiKey, defaultModel, started)
	}
	v.logFailure(provider, apiKey, status, body)
	return VerifyResult{ErrorCode: VerifyUnreachable, Detail: fmt.Sprintf("The provider returned %d.", status), LatencyMs: elapsed()}, nil
}

func (v *HTTPVerifier) probeChatCompletion(ctx context.Context, base, apiKey string, model *string, started time.Time) (VerifyResult, error) {
	elapsed := func() int { return int(time.Since(started) / time.Millisecond) }
	if model == nil || *model == "" {
		return VerifyResult{
			ErrorCode: VerifyUnreachable,
			Detail:    "This endpoint has no /models list, so a default model is required to test it.",
			LatencyMs: elapsed(),
		}, nil
	}
	payload, err := json.Marshal(map[string]any{
		"model":      *model,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
	})
	if err != nil {
		return VerifyResult{}, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, v.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return VerifyResult{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := v.client().Do(req)
	if err != nil {
		return VerifyResult{}, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if err != nil {
		return VerifyResult{}, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return VerifyResult{OK: true, Detail: "Key accepted (verified with a one-token completion).", LatencyMs: elapsed()}, nil
	}
	v.logFailure(UserProviderCompatible, apiKey, resp.StatusCode, body)
	code := VerifyUnreachable
	if resp.StatusCode == 401 {
		code = VerifyInvalidKey
	}
	return VerifyResult{ErrorCode: code, Detail: fmt.Sprintf("The endpoint returned %d.", resp.StatusCode), LatencyMs: elapsed()}, nil
}

func (v *HTTPVerifier) logFailure(provider, apiKey string, status int, body []byte) {
	v.logger().Warn(fmt.Sprintf("ai_credential_verify_failed provider=%s status=%d body=%s",
		provider, status, redactBody(string(body), apiKey, 300)))
}

// resolveVerifyBaseURL re-checks an explicit base URL (the host may point
// elsewhere since it was saved) or takes the provider's default.
func resolveVerifyBaseURL(provider string, baseURL *string) (string, error) {
	if baseURL != nil {
		if explicit := strings.TrimSpace(*baseURL); explicit != "" {
			if _, err := netguard.ValidateForRequest(explicit); err != nil {
				return "", err
			}
			return strings.Trim(explicit, "/"), nil
		}
	}
	if def, ok := UserProviderDefaultBaseURL(provider); ok {
		return def, nil
	}
	return "", httpx.BadRequest("A base URL is required for this provider.")
}
