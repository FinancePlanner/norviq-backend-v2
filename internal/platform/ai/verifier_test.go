package ai

import (
	"context"
	"testing"
	"time"
)

func verifyAgainst(t *testing.T, provider string, model *string, replies ...stubReply) (VerifyResult, *stubServer) {
	t.Helper()
	stub := newStub(t, replies...)
	v := &HTTPVerifier{Logger: quiet}
	r, err := v.verifyModels(context.Background(), provider, "sk-test", stub.srv.URL, model, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r, stub
}

func TestVerifierStatusMapping(t *testing.T) {
	for status, want := range map[int]string{200: "", 401: VerifyInvalidKey, 403: VerifyRestricted, 429: VerifyRateLimited, 500: VerifyUnreachable} {
		r, _ := verifyAgainst(t, UserProviderOpenAI, nil, stubReply{status, `{}`})
		if r.ErrorCode != want || r.OK != (want == "") {
			t.Fatalf("%d: %+v", status, r)
		}
	}
	if !(VerifyResult{ErrorCode: VerifyRateLimited}).IsInconclusive() || (VerifyResult{ErrorCode: VerifyInvalidKey}).IsInconclusive() {
		t.Fatal("inconclusive")
	}
}

func TestVerifierHeaders(t *testing.T) {
	_, stub := verifyAgainst(t, UserProviderAnthropic, nil, stubReply{200, `{}`})
	req := stub.requests()[0]
	eq(t, req.path, "/models")
	eq(t, req.header.Get("x-api-key"), "sk-test")
	eq(t, req.header.Get("anthropic-version"), AnthropicAPIVersion)
	eq(t, req.header.Get("Authorization"), "")

	_, stub = verifyAgainst(t, UserProviderOpenAI, nil, stubReply{200, `{}`})
	eq(t, stub.requests()[0].header.Get("Authorization"), "Bearer sk-test")
}

func TestVerifierCompatibleProbe(t *testing.T) {
	r, stub := verifyAgainst(t, UserProviderCompatible, Str("llama"), stubReply{404, ``}, stubReply{200, `{}`})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	reqs := stub.requests()
	eq(t, reqs[1].path, "/chat/completions")
	body := stub.bodies()[1]
	eq(t, body["model"], "llama")
	eq(t, body["max_tokens"], float64(1))

	r, _ = verifyAgainst(t, UserProviderCompatible, nil, stubReply{405, ``})
	eq(t, r.ErrorCode, VerifyUnreachable)
	r, _ = verifyAgainst(t, UserProviderCompatible, Str("m"), stubReply{404, ``}, stubReply{401, ``})
	eq(t, r.ErrorCode, VerifyInvalidKey)
	// Only compatible gateways get the probe.
	r, _ = verifyAgainst(t, UserProviderOpenAI, Str("m"), stubReply{404, ``})
	eq(t, r.ErrorCode, VerifyUnreachable)
}

func TestVerifierRefusesBlockedBaseURL(t *testing.T) {
	v := &HTTPVerifier{Logger: quiet}
	_, err := v.Verify(context.Background(), UserProviderCompatible, "k", Str("https://10.0.0.1/v1"), nil)
	eq(t, httpStatus(t, err), 400)
	r, err := v.Verify(context.Background(), UserProviderCompatible, "k", Str("https://unreachable.invalid/v1"), nil)
	if err != nil || r.ErrorCode != VerifyUnreachable {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestUserProviders(t *testing.T) {
	for _, p := range []string{UserProviderAnthropic, UserProviderOpenAI, UserProviderCompatible} {
		if !IsUserProvider(p) {
			t.Fatal(p)
		}
	}
	if IsUserProvider("hermes") {
		t.Fatal("hermes")
	}
	if _, ok := UserProviderDefaultBaseURL(UserProviderCompatible); ok {
		t.Fatal("compatible has no default")
	}
}
