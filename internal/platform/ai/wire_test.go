package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var tinyPNG = Image{MediaType: "image/png", Data: []byte{1, 2}}

func marshalString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMessageMarshalWithoutImagesIsUnchanged(t *testing.T) {
	eq(t, marshalString(t, Text("user", "hi")), `{"role":"user","content":"hi"}`)
}

func TestMessageMarshalWithImagesUsesContentArray(t *testing.T) {
	m := Text("user", "hi")
	m.Images = []Image{tinyPNG}
	eq(t, marshalString(t, m), `{"role":"user","content":[{"type":"text","text":"hi"},`+
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,AQI="}}]}`)
}

func TestBuildTurnsPutsImageBlockBeforeText(t *testing.T) {
	m := Text("user", "what is this")
	m.Images = []Image{tinyPNG}
	req := build([]Message{m}, nil, "", 4096, thinkingOn)
	turns := req.Messages
	eq(t, len(turns), 1)
	eq(t, blockTypes(turns[0]), []string{"image", "text"})
	eq(t, string(turns[0].Content[0]), `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQI="}}`)
	body := encoded(t, req)
	for _, key := range []string{`"temperature"`, `"top_p"`, `"top_k"`} {
		if strings.Contains(body, key) {
			t.Fatalf("request carries %s: %s", key, body)
		}
	}
}

func TestBuildTurnsImageWithEmptyTextIsAValidTurn(t *testing.T) {
	m := Text("user", "")
	m.Images = []Image{tinyPNG}
	req := build([]Message{m}, nil, "", 4096, thinkingOn)
	turns := req.Messages
	eq(t, len(turns), 1)
	eq(t, turns[0].Role, "user")
	eq(t, blockTypes(turns[0]), []string{"image"})
}

func TestWithoutAnthropicReasoningKeepsImages(t *testing.T) {
	m := anthropicToolTurn()
	m.Images = []Image{tinyPNG}
	got := m.WithoutAnthropicReasoning()
	eq(t, got.Images, []Image{tinyPNG})
	eq(t, len(got.ReasoningDetails), 0)
}

func TestProviderErrorLogNeverContainsImageData(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x12}, 200)
	b64 := base64.StdEncoding.EncodeToString(data)
	m := Text("user", "hi")
	m.Images = []Image{{MediaType: "image/png", Data: data}}

	for _, name := range []string{"anthropic", "openai"} {
		t.Run(name, func(t *testing.T) {
			// The stub echoes the request body, image first, as a 400.
			var stub *stubServer
			var client ChatClient
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			payload, _ := json.Marshal(map[string]string{"error": "bad request", "echo": b64})
			stub = newStub(t, stubReply{status: 400, body: string(payload)})
			if name == "anthropic" {
				client = &AnthropicClient{APIKey: "k", Model: "claude-haiku-5-5", BaseURL: stub.srv.URL, MaxTokens: 100, Logger: logger}
			} else {
				client = &OpenAIClient{APIKey: "k", Model: "m", BaseURL: stub.srv.URL, MaxTokens: 100, Logger: logger}
			}
			if _, err := client.Chat(context.Background(), []Message{m}, nil, ""); err == nil {
				t.Fatal("want an error")
			}
			if logs.Len() == 0 {
				t.Fatal("expected a log line")
			}
			if strings.Contains(logs.String(), b64[:60]) {
				t.Fatalf("log contains image data: %s", logs.String())
			}
		})
	}
}

func TestClassifyImageRejection(t *testing.T) {
	cred := &Credential{ID: uuid.New(), Provider: "openai"}
	for _, status := range []int{400, 415, 422} {
		f, ok := ClassifyImageRejection(&UpstreamError{Status: status}, cred)
		if !ok || f.Kind != "image_unsupported" || f.CredentialID != cred.ID {
			t.Fatalf("status %d: %#v %v", status, f, ok)
		}
		eq(t, f.UserFacingMessage(), "Your openai model can't read images. Try without the image, or change the model in Settings → AI providers.")
		eq(t, f.ErrorCode(), "user_credential_image_unsupported")
	}
	for _, status := range []int{401, 429, 500} {
		if _, ok := ClassifyImageRejection(&UpstreamError{Status: status}, cred); ok {
			t.Fatalf("status %d should not classify", status)
		}
	}
	if _, ok := ClassifyImageRejection(&UpstreamError{Status: 400}, nil); ok {
		t.Fatal("nil credential should not classify")
	}
}

func TestExcludeFreeRungs(t *testing.T) {
	paid := newStub(t, stubReply{status: 200, body: `{"model":"p","choices":[{"message":{"role":"assistant","content":"paid"}}]}`})
	free := newStub(t, stubReply{status: 200, body: `{"model":"f","choices":[{"message":{"role":"assistant","content":"free"}}]}`})
	mk := func(label, model string, s *stubServer) Rung {
		tier := Tier{Label: label, APIKey: "k", BaseURL: s.srv.URL, Model: model, SupportsResponseFormat: true}
		return Rung{Tier: tier, Client: ClientForTier(tier, ChainOptions{Logger: quiet})}
	}
	chain := &FallbackClient{Rungs: []Rung{mk("paid", "gpt-x", paid), mk("free", "x:free", free)}, Logger: quiet}

	filtered := ExcludeFreeRungs(chain)
	fc, ok := filtered.(*FallbackClient)
	if !ok {
		t.Fatalf("got %T", filtered)
	}
	eq(t, len(fc.Rungs), 1)
	eq(t, len(chain.Rungs), 2)
	eq(t, chain.Rungs[1].Tier.Model, "x:free")
	if _, err := filtered.Chat(context.Background(), []Message{Text("user", "hi")}, nil, ""); err != nil {
		t.Fatal(err)
	}
	eq(t, len(paid.requests()), 1)
	eq(t, len(free.requests()), 0)

	onlyFree := &FallbackClient{Rungs: []Rung{mk("free", "x:free", free)}, Logger: quiet}
	if _, ok := ExcludeFreeRungs(onlyFree).(DisabledClient); !ok {
		t.Fatal("all-free chain should be disabled")
	}
	if _, ok := ExcludeFreeRungs(&OpenAIClient{Model: "x:free"}).(DisabledClient); !ok {
		t.Fatal("bare free client should be disabled")
	}
	paidClient := &OpenAIClient{Model: "gpt-x"}
	if ExcludeFreeRungs(paidClient) != ChatClient(paidClient) {
		t.Fatal("paid client should pass through")
	}
	anth := &AnthropicClient{Model: "claude-haiku-5-5"}
	if ExcludeFreeRungs(anth) != ChatClient(anth) {
		t.Fatal("anthropic client should pass through")
	}
}
