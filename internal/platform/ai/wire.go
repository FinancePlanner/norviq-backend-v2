// Package ai is the AI provider platform every AI feature calls: the chat
// provider chain (Anthropic-first trial rung, OpenRouter primary,
// AI_FALLBACK_PROVIDERS, plan routing), the daily cap and usage accounting,
// AI consent, and users' own provider credentials.
//
// It ports the provider, routing, consent, credential, cap and usage layers of
// the Vapor backend's `AI/` directory. The services keep speaking the OpenAI
// Chat Completions shape (Message, Tool); the Anthropic rung translates.
package ai

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Message is OpenAIMessage: one chat turn on the OpenAI Chat Completions
// wire. Optional fields are omitted when nil, as Swift's synthesized Encodable
// did.
type Message struct {
	Role       string     `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content    *string    `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID *string    `json:"tool_call_id,omitempty"`
	Name       *string    `json:"name,omitempty"`
	// ReasoningDetails are provider-owned blocks (OpenRouter's reasoning
	// details, or this package's Anthropic thinking marker). They round-trip
	// untouched, so they stay raw JSON.
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
	Reasoning        *string           `json:"reasoning,omitempty"`
	// Images ride on a user turn for one request only. They never marshal
	// as a field: MarshalJSON turns them into the content array.
	Images []Image `json:"-"`
	// Documents (PDFs) ride the same way, for one request only.
	Documents []Document `json:"-"`
}

// Document is one attached file (a PDF): raw bytes, the media type the
// provider is told and the filename. It is never persisted or logged.
type Document struct {
	MediaType string
	Data      []byte
	Name      string
}

// Image is one attached picture: raw bytes plus the media type the provider
// is told. It is never persisted or logged.
type Image struct {
	MediaType string
	Data      []byte
}

// MarshalJSON encodes as before unless the message carries images, then
// content becomes an OpenAI multimodal array (text first, then one data URL
// per image, then one file part per document). It encodes without HTML escaping, like the rest of the wire.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	if len(m.Images)+len(m.Documents) == 0 {
		return marshalNoEscape(plain(m))
	}
	parts := []VisionPart{}
	if text := m.ContentString(); text != "" {
		parts = append(parts, VisionText(text))
	}
	for _, img := range m.Images {
		parts = append(parts, VisionImage(img.Data, img.MediaType))
	}
	for _, doc := range m.Documents {
		parts = append(parts, VisionFile(doc.Data, doc.MediaType, doc.Name))
	}
	return marshalNoEscape(struct {
		plain
		Content []VisionPart `json:"content"`
	}{plain: plain(m), Content: parts})
}

// Text builds a message with only role and content.
func Text(role, content string) Message { return Message{Role: role, Content: &content} }

// Str returns a pointer to s, for optional message fields.
func Str(s string) *string { return &s }

// ContentString is the content, or "" when absent.
func (m Message) ContentString() string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// HasNoUsableOutput is `hasNoUsableOutput`: no text and no tool call, nothing
// a caller can show or act on. Blank content with a tool call is normal.
func (m Message) HasNoUsableOutput() bool {
	return strings.TrimSpace(m.ContentString()) == "" && len(m.ToolCalls) == 0
}

// ToolCall is OpenAIToolCall.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is OpenAIFunctionCall; Arguments is a JSON-encoded string.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is OpenAITool.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// NewTool builds a function tool, as OpenAITool's `type` default did.
func NewTool(name, description string, parameters JSONSchema) Tool {
	return Tool{Type: "function", Function: FunctionDef{Name: name, Description: description, Parameters: parameters}}
}

// FunctionDef is OpenAIFunctionDef.
type FunctionDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  JSONSchema `json:"parameters"`
}

// Parameter is one JSON Schema property (OpenAIParameter).
type Parameter struct {
	Description *string  `json:"description,omitempty"`
	EnumValues  []string `json:"enum,omitempty"`
	// Items is the element schema of an array parameter.
	Items any    `json:"items,omitempty"`
	Type  string `json:"type"`
}

// JSONSchema is OpenAIJSONSchema. A tool without parameters encodes as
// `{"type":"object","properties":{},"required":[]}`.
//
// Fields are declared in key order so the encoding matches the Anthropic
// client's sorted-keys encoder.
type JSONSchema struct {
	Properties map[string]Parameter `json:"properties"`
	Required   []string             `json:"required"`
	Type       string               `json:"type"`
}

// MarshalJSON keeps the Swift defaults: type "object", and an empty object
// and array rather than null.
func (s JSONSchema) MarshalJSON() ([]byte, error) {
	type plain JSONSchema
	out := plain(s)
	if out.Type == "" {
		out.Type = "object"
	}
	if out.Properties == nil {
		out.Properties = map[string]Parameter{}
	}
	if out.Required == nil {
		out.Required = []string{}
	}
	return json.Marshal(out)
}

// ResponseFormatJSON is the only response_format value callers use.
const ResponseFormatJSON = "json_object"

// marshalNoEscape encodes like Swift's encoder with withoutEscapingSlashes:
// no HTML escaping, no trailing newline. Map keys come out sorted, which is
// what the Anthropic client's sortedKeys encoder relied on.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

// decodeLoose decodes raw JSON into a generic value, keeping numbers exact.
func decodeLoose(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errTrailingData
	}
	return v, nil
}
