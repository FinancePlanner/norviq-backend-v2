package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode"
)

// VisionRequest is the chat-completions body with a multimodal content array
// (receipt OCR, portfolio screenshots). Plain Message content cannot carry
// an image.
type VisionRequest struct {
	Model          string               `json:"model"`
	Messages       []VisionMessage      `json:"messages"`
	Temperature    float64              `json:"temperature"`
	MaxTokens      int                  `json:"max_tokens"`
	ResponseFormat responseFormatBody   `json:"response_format"`
	Provider       *ProviderPreferences `json:"provider,omitempty"`
}

// NewVisionRequest builds a JSON-mode vision request.
func NewVisionRequest(model string, messages []VisionMessage, temperature float64, maxTokens int) VisionRequest {
	return VisionRequest{
		Model: model, Messages: messages, Temperature: temperature, MaxTokens: maxTokens,
		ResponseFormat: responseFormatBody{Type: ResponseFormatJSON},
	}
}

// VisionMessage is one multimodal message.
type VisionMessage struct {
	Role    string       `json:"role"`
	Content []VisionPart `json:"content"`
}

// VisionPart is a text span or an image reference.
type VisionPart struct {
	Type     string          `json:"type"`
	Text     *string         `json:"text,omitempty"`
	ImageURL *visionImageURL `json:"image_url,omitempty"`
}

type visionImageURL struct {
	URL string `json:"url"`
}

// VisionText is a text part.
func VisionText(s string) VisionPart { return VisionPart{Type: "text", Text: &s} }

// VisionImageURL is an image part by URL.
func VisionImageURL(u string) VisionPart {
	return VisionPart{Type: "image_url", ImageURL: &visionImageURL{URL: u}}
}

// VisionImage wraps raw bytes as a base64 data URL, defaulting an unknown
// content type to JPEG as the upload endpoints do.
func VisionImage(data []byte, contentType string) VisionPart {
	mime := contentType
	if mime == "" || mime == "application/octet-stream" {
		mime = "image/jpeg"
	}
	return VisionImageURL("data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data))
}

// CompleteVisionJSON is VisionChatCaller.completeJSON: posts a vision
// request and returns the model's text (nil when the reply has none). A
// non-200 is a 502 naming the feature, never the provider's body.
func CompleteVisionJSON(ctx context.Context, client *http.Client, body VisionRequest, apiKey, baseURL, feature string, logger *slog.Logger) (*string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	body.Provider = ProviderPreferencesFor(baseURL)
	payload, err := marshalNoEscape(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		logger.Error(fmt.Sprintf("%s_error status=%d body=%s", feature, resp.StatusCode, redactBody(string(raw), apiKey, 300)))
		return nil, httpBadGateway(capitalizeWords(strings.ReplaceAll(feature, "_", " ")) + " is temporarily unavailable. Please try again.")
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := DecodeProviderJSON(raw, &decoded, logger, "VisionResponse"); err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, nil
	}
	return decoded.Choices[0].Message.Content, nil
}

// capitalizeWords is Foundation's `capitalized`: each word's first letter
// upper, the rest lower.
func capitalizeWords(s string) string {
	var b strings.Builder
	start := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			start = true
			b.WriteRune(r)
			continue
		}
		if start {
			b.WriteRune(unicode.ToUpper(r))
			start = false
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
