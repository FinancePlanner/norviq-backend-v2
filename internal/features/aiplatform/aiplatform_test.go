package aiplatform_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

var quiet = slog.New(slog.DiscardHandler)

// fakeVerifier stands in for the provider round trip.
type fakeVerifier struct {
	result VerifyCall
	calls  []VerifyCall
}

// VerifyCall is one Verify invocation (and the canned answer).
type VerifyCall struct {
	Provider, APIKey string
	BaseURL          *string
	Result           ai.VerifyResult
}

func (f *fakeVerifier) Verify(_ context.Context, provider, apiKey string, baseURL, _ *string) (ai.VerifyResult, error) {
	f.calls = append(f.calls, VerifyCall{Provider: provider, APIKey: apiKey, BaseURL: baseURL})
	return f.result.Result, nil
}

// consentProbe mounts a route behind the reusable consent middleware, the
// way a wave-2 AI controller will.
type consentProbe struct{}

func (consentProbe) Name() string { return "consent-probe" }

func (consentProbe) Register(d *server.Deps, r *server.Routes) error {
	p, err := aiplatform.Platform(d)
	if err != nil {
		return err
	}
	r.V1.GET("/test/ai", func(c *echo.Context) error { return httpx.OK(c, map[string]bool{"ok": true}) },
		d.Auth.Session(), auth.Guard(), p.Consent.Middleware())
	return nil
}

type harness struct {
	*testutil.App
	p        *ai.Platform
	verifier *fakeVerifier
}

func newHarness(t *testing.T, env map[string]string) *harness {
	t.Helper()
	h := &harness{verifier: &fakeVerifier{result: VerifyCall{Result: ai.VerifyResult{OK: true, Detail: "Key accepted.", LatencyMs: 7}}}}
	h.App = testutil.NewApp(t, []server.Feature{aiplatform.Feature{}, consentProbe{}}, func(d *server.Deps) {
		p, err := ai.New(ai.Options{Env: ai.MapEnv(env), Logger: quiet, DB: d.DB, HTTP: &http.Client{}})
		if err != nil {
			t.Fatal(err)
		}
		p.Verifier = h.verifier
		h.p = p
		aiplatform.SetPlatform(d, p)
	})
	return h
}

type envelope struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func (h *harness) expectError(r *testutil.Response, status int, reason string) {
	h.T.Helper()
	if r.Status != status {
		h.T.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
	var e envelope
	r.JSON(h.T, &e)
	if !e.Error || e.Reason != reason {
		h.T.Fatalf("reason %q, want %q", e.Reason, reason)
	}
}

func (h *harness) pat(userID uuid.UUID, scopes ...string) string {
	h.T.Helper()
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := h.Deps.DB.Exec(context.Background(),
		`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		 VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`,
		uuid.New(), userID, auth.SHA256Hex(token), scopes)
	if err != nil {
		h.T.Fatal(err)
	}
	return token
}

// MARK: - consent

func TestConsentRoundTrip(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("consent@example.com")
	tok := h.Token(user)

	r := h.Do(http.MethodGet, "/v1/users/ai-consent", tok, nil)
	if r.Status != 200 || string(r.Body) != `{"consentedAt":null}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	var first, again struct{ ConsentedAt *string }
	h.Do(http.MethodPost, "/v1/users/ai-consent", tok, nil).JSON(t, &first)
	if first.ConsentedAt == nil || !strings.HasSuffix(*first.ConsentedAt, "Z") {
		t.Fatalf("%+v", first)
	}
	time.Sleep(1100 * time.Millisecond)
	h.Do(http.MethodPost, "/v1/users/ai-consent", tok, nil).JSON(t, &again)
	if *again.ConsentedAt != *first.ConsentedAt {
		t.Fatal("granting again must keep the original timestamp")
	}

	r = h.Do(http.MethodDelete, "/v1/users/ai-consent", tok, nil)
	if r.Status != 200 || string(r.Body) != `{"consentedAt":null}` {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if at, _ := h.p.Consent.ConsentedAt(context.Background(), user); at != nil {
		t.Fatal("withdraw should clear the column")
	}
}

func TestConsentAuth(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("scoped@example.com")

	h.expectError(h.Do(http.MethodGet, "/v1/users/ai-consent", "", nil), 401, "SessionToken not authenticated.")

	read := h.pat(user, "settings:read")
	if r := h.Do(http.MethodGet, "/v1/users/ai-consent", read, nil); r.Status != 200 {
		t.Fatalf("settings:read PAT may read consent: %d %s", r.Status, r.Body)
	}
	other := h.pat(user, "portfolio:read")
	if r := h.Do(http.MethodGet, "/v1/users/ai-consent", other, nil); r.Status != 403 {
		t.Fatalf("wrong scope: %d %s", r.Status, r.Body)
	}
	// Granting is a session act; a PAT never authenticates there.
	if r := h.Do(http.MethodPost, "/v1/users/ai-consent", read, nil); r.Status != 401 {
		t.Fatalf("PAT grant: %d %s", r.Status, r.Body)
	}

	ghost := h.Token(uuid.New())
	if r := h.Do(http.MethodGet, "/v1/users/ai-consent", ghost, nil); r.Status != 401 {
		t.Fatalf("unknown user: %d %s", r.Status, r.Body)
	}
}

func TestConsentEnforcement(t *testing.T) {
	h := newHarness(t, map[string]string{"AI_CONSENT_ENFORCED": "true", "AI_CONSENT_LEGACY_IOS_GRACE": "true"})
	user := h.CreateUser("gate@example.com")
	tok := h.Token(user)

	h.expectError(h.Do(http.MethodGet, "/v1/test/ai", tok, nil), 403, ai.ConsentRequiredReason)
	h.expectError(h.Do(http.MethodGet, "/v1/test/ai", "", nil), 401, "SessionToken not authenticated.")

	// A pre-consent iOS build (CFNetwork, no capability) is let through.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, h.Server.URL+"/v1/test/ai", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", "Norviq/77 CFNetwork/1568 Darwin/25.0.0")
	resp, err := h.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || h.p.Consent.LegacyGraceCount.Load() != 1 {
		t.Fatalf("legacy grace: %d", resp.StatusCode)
	}
	// One that declares the capability has the consent screen: no grace.
	req.Header.Set(ai.ClientCapabilitiesHeader, "foo, AI-Consent-V1")
	resp, err = h.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("capable client: %d", resp.StatusCode)
	}

	h.Do(http.MethodPost, "/v1/users/ai-consent", tok, nil)
	if r := h.Do(http.MethodGet, "/v1/test/ai", tok, nil); r.Status != 200 {
		t.Fatalf("after consent: %d %s", r.Status, r.Body)
	}
	ok, err := h.p.Consent.IsAllowed(context.Background(), user)
	if err != nil || !ok {
		t.Fatal("strict check after consent")
	}
}

func TestConsentNotEnforcedByDefault(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.Token(h.CreateUser("open@example.com"))
	if r := h.Do(http.MethodGet, "/v1/test/ai", tok, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// MARK: - credentials

type summary struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	Label         string  `json:"label"`
	KeyHint       string  `json:"keyHint"`
	BaseURL       *string `json:"baseURL"`
	DefaultModel  *string `json:"defaultModel"`
	Status        string  `json:"status"`
	LastErrorCode *string `json:"lastErrorCode"`
	CreatedAt     string  `json:"createdAt"`
}

func TestCredentialLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("byok@example.com")
	tok := h.Token(user)
	const key = "sk-ant-api03-supersecret-WXYZ"

	r := h.Do(http.MethodPost, "/v1/ai/credentials", tok, map[string]any{"provider": "Anthropic", "label": " Mine ", "apiKey": key})
	if r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if strings.Contains(string(r.Body), "supersecret") || strings.Contains(string(r.Body), "apiKey") {
		t.Fatalf("key echoed: %s", r.Body)
	}
	var created summary
	r.JSON(t, &created)
	if created.Provider != "anthropic" || created.Label != "Mine" || created.KeyHint != "WXYZ" || created.Status != "active" {
		t.Fatalf("%+v", created)
	}
	if created.ID != strings.ToUpper(created.ID) {
		t.Fatal("uuid case")
	}
	if len(h.verifier.calls) != 1 || h.verifier.calls[0].APIKey != key {
		t.Fatal("verify defaults to true and gets the plaintext")
	}

	// Encrypted at rest, decryptable by the platform cipher.
	var stored string
	if err := h.Deps.DB.QueryRow(context.Background(),
		`SELECT api_key_encrypted FROM user_ai_provider_credentials WHERE user_id = $1`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "nvq-enc:") || strings.Contains(stored, "supersecret") {
		t.Fatalf("stored %q", stored)
	}
	if plain, err := h.p.Cipher.Decrypt(stored, ai.CredentialContextAIProvider); err != nil || plain != key {
		t.Fatal("decrypt")
	}

	// Same provider again: an update (200), same id, verification skipped.
	h.verifier.calls = nil
	r = h.Do(http.MethodPost, "/v1/ai/credentials", tok, map[string]any{"provider": "anthropic", "label": "Renamed", "apiKey": "sk-new-1234", "verify": false})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var updated summary
	r.JSON(t, &updated)
	if updated.ID != created.ID || updated.Status != "unverified" || updated.KeyHint != "1234" || len(h.verifier.calls) != 0 {
		t.Fatalf("%+v", updated)
	}

	var list struct{ Items []summary }
	h.Do(http.MethodGet, "/v1/ai/credentials", tok, nil).JSON(t, &list)
	if len(list.Items) != 1 || list.Items[0].Label != "Renamed" {
		t.Fatalf("%+v", list)
	}

	// Verify: a rejected key goes invalid; the response is still 200.
	h.verifier.result.Result = ai.VerifyResult{ErrorCode: ai.VerifyInvalidKey, Detail: "The provider rejected this key.", LatencyMs: 3}
	r = h.Do(http.MethodPost, "/v1/ai/credentials/"+created.ID+"/verify", tok, nil)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var vr struct {
		Status    string  `json:"status"`
		ErrorCode *string `json:"errorCode"`
		Detail    string  `json:"detail"`
		LatencyMs int     `json:"latencyMs"`
	}
	r.JSON(t, &vr)
	if vr.Status != "invalid" || vr.ErrorCode == nil || *vr.ErrorCode != "invalid_key" || vr.LatencyMs != 3 {
		t.Fatalf("%+v", vr)
	}
	if h.verifier.calls[0].APIKey != "sk-new-1234" {
		t.Fatal("verify decrypts the stored key")
	}
	// Rate limiting proves nothing: status unchanged.
	h.verifier.result.Result = ai.VerifyResult{ErrorCode: ai.VerifyRateLimited, Detail: "busy"}
	h.Do(http.MethodPost, "/v1/ai/credentials/"+strings.ToLower(created.ID)+"/verify", tok, nil).JSON(t, &vr)
	if vr.Status != "invalid" || *vr.ErrorCode != "rate_limited" {
		t.Fatalf("%+v", vr)
	}

	// Another user can neither verify nor delete it.
	other := h.Token(h.CreateUser("other@example.com"))
	h.expectError(h.Do(http.MethodPost, "/v1/ai/credentials/"+created.ID+"/verify", other, nil), 404, "Credential not found.")
	h.expectError(h.Do(http.MethodDelete, "/v1/ai/credentials/"+created.ID, other, nil), 404, "Credential not found.")
	h.expectError(h.Do(http.MethodDelete, "/v1/ai/credentials/not-a-uuid", tok, nil), 400, "Invalid credential id.")

	if r := h.Do(http.MethodDelete, "/v1/ai/credentials/"+created.ID, tok, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	h.Do(http.MethodGet, "/v1/ai/credentials", tok, nil).JSON(t, &list)
	if len(list.Items) != 0 {
		t.Fatal("deleted")
	}
}

func TestCredentialValidation(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.Token(h.CreateUser("val@example.com"))
	post := func(body any) *testutil.Response { return h.Do(http.MethodPost, "/v1/ai/credentials", tok, body) }

	h.expectError(post(map[string]any{"provider": "hermes", "label": "x", "apiKey": "k"}), 400, "Unknown provider 'hermes'.")
	h.expectError(post(map[string]any{"provider": "openai", "label": "  ", "apiKey": "k"}), 400, "Label must be 1-100 characters.")
	h.expectError(post(map[string]any{"provider": "openai", "label": strings.Repeat("x", 101), "apiKey": "k"}), 400, "Label must be 1-100 characters.")
	h.expectError(post(map[string]any{"provider": "openai", "label": "x", "apiKey": strings.Repeat("k", 513)}), 400, "API key must be 1-512 characters.")
	h.expectError(post(map[string]any{"provider": "openai", "label": "x", "apiKey": "sk-ab cd"}), 400, "API key contains whitespace — check for a broken copy/paste.")
	h.expectError(post(map[string]any{"provider": "compatible", "label": "x", "apiKey": "k"}), 400, "A base URL is required for an OpenAI-compatible endpoint.")
	h.expectError(post(map[string]any{"provider": "compatible", "label": "x", "apiKey": "k", "baseURL": "https://llm.example/v1"}), 400, "A default model is required for an OpenAI-compatible endpoint.")
	h.expectError(post(map[string]any{"provider": "compatible", "label": "x", "apiKey": "k", "baseURL": "http://llm.example/v1", "defaultModel": "m"}), 400, "URL must use https.")
	h.expectError(post(map[string]any{"provider": "openai", "label": "x", "apiKey": "k", "baseURL": "https://169.254.169.254/"}), 400, "Host 169.254.169.254 is not allowed.")

	h.expectError(post(map[string]any{"label": "x", "apiKey": "k"}), 400,
		`No such key 'provider' at path ''. No value associated with key CodingKeys(stringValue: "provider", intValue: nil) ("provider").`)
	h.expectError(post(map[string]any{"provider": nil, "label": "x", "apiKey": "k"}), 400,
		"No value found (expected type 'String') at path 'provider'. Cannot get value of type String -- found null value instead.")
	h.expectError(post(map[string]any{"provider": 1, "label": "x", "apiKey": "k"}), 400,
		"Value was not of type 'String' at path 'provider'. Expected to decode String but found number instead.")
	h.expectError(post(map[string]any{"provider": "openai", "label": "x", "apiKey": "k", "verify": "yes"}), 400,
		"Value was not of type 'Bool' at path 'verify'. Expected to decode Bool but found a string instead.")
	h.expectError(post([]byte(`[1]`)), 400,
		"Value was not of type 'Dictionary<String, Any>' at path ''. Expected to decode Dictionary<String, Any> but found an array instead.")
	h.expectError(post([]byte(`{`)), 400, "Data corrupted at path ''. The given data was not valid JSON.")

	// A compatible endpoint that passes the guard is stored with its URL.
	r := post(map[string]any{"provider": "compatible", "label": "Gateway", "apiKey": "k", "baseURL": " https://llm.example/v1 ", "defaultModel": "llama", "verify": false})
	if r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var s summary
	r.JSON(t, &s)
	if s.BaseURL == nil || *s.BaseURL != "https://llm.example/v1" || *s.DefaultModel != "llama" || s.KeyHint != "k" {
		t.Fatalf("%+v", s)
	}
}

func TestCredentialAuthAndSwitch(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("auth@example.com")
	h.expectError(h.Do(http.MethodGet, "/v1/ai/credentials", "", nil), 401, "SessionToken not authenticated.")
	pat := h.pat(user, "settings:read", "settings:write")
	if r := h.Do(http.MethodGet, "/v1/ai/credentials", pat, nil); r.Status != 401 {
		t.Fatalf("PAT: %d %s", r.Status, r.Body)
	}

	off := newHarness(t, map[string]string{"AI_USER_CREDENTIALS_ENABLED": "false"})
	tok := off.Token(off.CreateUser("off@example.com"))
	off.expectError(off.Do(http.MethodGet, "/v1/ai/credentials", tok, nil), 503, "Bring-your-own-key is currently disabled.")
}

func TestVerifyRejectsPlaintextAndUnknownProvider(t *testing.T) {
	h := newHarness(t, nil)
	user := h.CreateUser("legacy@example.com")
	tok := h.Token(user)
	insert := func(provider, key string) string {
		id := uuid.New()
		_, err := h.Deps.DB.Exec(context.Background(),
			`INSERT INTO user_ai_provider_credentials (id, user_id, provider, label, api_key_encrypted, key_hint, key_fingerprint, status, created_at, updated_at)
			 VALUES ($1, $2, $3, 'x', $4, 'hint', 'fp', 'unverified', now(), now())`, id, user, provider, key)
		if err != nil {
			t.Fatal(err)
		}
		return strings.ToUpper(id.String())
	}
	h.expectError(h.Do(http.MethodPost, "/v1/ai/credentials/"+insert("openai", "sk-plain")+"/verify", tok, nil), 500, "Stored credential is not encrypted.")
	h.expectError(h.Do(http.MethodPost, "/v1/ai/credentials/"+insert("hermes", "x")+"/verify", tok, nil), 400, "Stored credential has an unrecognised provider.") //nolint:misspell // Vapor's wire text
}

// MARK: - platform against the database

type plans struct {
	pro bool
	err error
}

func (p plans) IsPro(context.Context, uuid.UUID) (bool, error) { return p.pro, p.err }

type named struct{ name string }

func (n named) Chat(context.Context, []ai.Message, []ai.Tool, string) (ai.Message, error) {
	return ai.Text("assistant", n.name), nil
}

func TestPlanRoutingAndOwnKey(t *testing.T) {
	h := newHarness(t, map[string]string{"OPENROUTER_API_KEY": "or", "AI_PROVIDER": "openrouter"})
	user := h.CreateUser("route@example.com")
	ctx := context.Background()
	h.p.Router = &ai.Router{Free: named{"free"}, Pro: named{"pro"}}

	for _, tc := range []struct {
		resolver ai.PlanResolver
		want     ai.Plan
	}{
		{nil, ai.PlanFree},
		{plans{pro: true}, ai.PlanPro},
		{plans{pro: false}, ai.PlanFree},
		{plans{pro: true, err: errors.New("billing down")}, ai.PlanFree},
	} {
		h.p.Plans = tc.resolver
		client, plan := h.p.ClientFor(ctx, user)
		if plan == nil || *plan != tc.want || client.(named).name != string(tc.want) {
			t.Fatalf("%v: %v", tc.resolver, plan)
		}
	}

	// The user's own key wins over the plan chain.
	h.p.Plans = plans{pro: true}
	tok := h.Token(user)
	r := h.Do(http.MethodPost, "/v1/ai/credentials", tok, map[string]any{"provider": "openai", "label": "x", "apiKey": "sk-own", "defaultModel": "gpt-own", "verify": false})
	if r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	resolved, err := h.p.ResolveAssistantClient(ctx, user)
	if err != nil || !resolved.UsesOwnKey() || resolved.Plan != nil {
		t.Fatalf("%+v %v", resolved, err)
	}
	own := resolved.Client.(*ai.OpenAIClient)
	if own.APIKey != "sk-own" || own.Model != "gpt-own" || own.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("%+v", own)
	}

	// A rejected key is recorded and stops being used.
	failure, ok := ai.ClassifyCredentialFailure(&ai.UpstreamError{Status: 401}, resolved.Credential)
	if !ok || failure.ErrorCode() != "user_credential_rejected" {
		t.Fatal(failure)
	}
	h.p.RecordCredentialFailure(ctx, resolved.Credential, failure)
	resolved, err = h.p.ResolveAssistantClient(ctx, user)
	if err != nil || resolved.UsesOwnKey() || resolved.Client.(named).name != "pro" {
		t.Fatalf("%+v %v", resolved, err)
	}
	var he *httpx.Error
	if !errors.As(error(failure), &he) || he.Status != http.StatusFailedDependency {
		t.Fatal("424")
	}
}

func TestUsageAccounting(t *testing.T) {
	h := newHarness(t, map[string]string{"AI_FREE_MONTHLY_LIMIT": "2"})
	user := h.CreateUser("usage@example.com")
	ctx := context.Background()
	limit := h.p.Settings.FreeMonthlyLimit
	for range 2 {
		if err := h.p.ConsumeAssistantTurn(ctx, user, false, limit); err != nil {
			t.Fatal(err)
		}
	}
	err := h.p.ConsumeAssistantTurn(ctx, user, false, limit)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 402 || he.Reason != "The free AI preview includes 2 requests per month." {
		t.Fatal(err)
	}
	// The refused turn was rolled back.
	if n, _ := h.p.MonthlyUsage(ctx, user, time.Now()); n != 2 {
		t.Fatalf("usage %d", n)
	}
	if err := h.p.ConsumeAssistantTurn(ctx, user, true, limit); err != nil {
		t.Fatal("pro is not capped")
	}
	if n, _ := h.p.MonthlyUsage(ctx, user, time.Now().AddDate(0, 1, 0)); n != 0 {
		t.Fatal("next month starts at zero")
	}
}
