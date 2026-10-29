package receipts

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	"norviq-api/internal/platform/ai"
)

// OCRProvider is ReceiptOCRProvider: extracts a draft from a receipt photo.
// Extract returns nil when nothing usable was recognized; implementations
// must not persist the image.
type OCRProvider interface {
	IsEnabled() bool
	Extract(ctx context.Context, image []byte, contentType string) (*Draft, error)
}

// DisabledOCRProvider is DisabledReceiptOCRProvider.
type DisabledOCRProvider struct{}

// IsEnabled implements OCRProvider.
func (DisabledOCRProvider) IsEnabled() bool { return false }

// Extract implements OCRProvider.
func (DisabledOCRProvider) Extract(context.Context, []byte, string) (*Draft, error) { return nil, nil }

// OCRProviderFromEnv is ReceiptOCRProviderBootstrap.fromEnvironment:
// RECEIPT_OCR_PROVIDER=openai|openai-vision|vision wires the vision provider
// on the assistant's credentials, with RECEIPT_OCR_MODEL overriding the
// chat model. Anything else is disabled.
func OCRProviderFromEnv(env ai.Env, cfg ai.ProviderConfig, client *http.Client, logger *slog.Logger) OCRProvider {
	raw, _ := env("RECEIPT_OCR_PROVIDER")
	configured := strings.TrimFunc(raw, unicode.IsSpace)
	switch strings.ToLower(configured) {
	case "openai", "openai-vision", "vision":
		rawModel, _ := env("RECEIPT_OCR_MODEL")
		model := strings.TrimFunc(rawModel, unicode.IsSpace)
		if model == "" {
			model = cfg.ChatModel
		}
		p := &VisionOCRProvider{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: model, Client: client, Logger: logger}
		if !p.IsEnabled() {
			logger.Warn("RECEIPT_OCR_PROVIDER=" + configured + " set but AI credentials/model are missing; receipt OCR disabled.")
			return DisabledOCRProvider{}
		}
		logger.Info("receipt_ocr configured provider=openai-vision model=" + model)
		return p
	case "", "disabled":
	default:
		logger.Warn("RECEIPT_OCR_PROVIDER=" + configured + " is not recognized; receipt OCR disabled.")
	}
	return DisabledOCRProvider{}
}

// VisionOCRProvider is OpenAIVisionReceiptOCRProvider: an OpenAI-compatible
// vision chat model asked for a strict JSON object. Like Vapor, it sends
// temperature 0 on that wire; it never goes through the Anthropic adapter.
type VisionOCRProvider struct {
	APIKey, BaseURL, Model string
	Client                 *http.Client
	Logger                 *slog.Logger
}

// ocrMaxTokens covers a long supermarket receipt's line items.
const ocrMaxTokens = 2000

// IsEnabled implements OCRProvider.
func (p *VisionOCRProvider) IsEnabled() bool {
	return p.APIKey != "" && p.BaseURL != "" && p.Model != ""
}

// Extract implements OCRProvider.
func (p *VisionOCRProvider) Extract(ctx context.Context, image []byte, contentType string) (*Draft, error) {
	if !p.IsEnabled() {
		return nil, nil
	}
	body := ai.NewVisionRequest(p.Model, []ai.VisionMessage{
		{Role: "system", Content: []ai.VisionPart{ai.VisionText(OCRSystemPrompt)}},
		{Role: "user", Content: []ai.VisionPart{
			ai.VisionText("Extract the fields from this receipt image and return the JSON object."),
			ai.VisionImage(image, contentType),
		}},
	}, 0, ocrMaxTokens)
	text, err := ai.CompleteVisionJSON(ctx, p.Client, body, p.APIKey, p.BaseURL, "receipt_ocr", p.Logger)
	if err != nil || text == nil {
		return nil, err
	}
	extracted, ok := DecodeExtracted([]byte(*text))
	if !ok || !extracted.HasContent() {
		return nil, nil
	}
	return extracted.ToDraft(), nil
}

// OCRSystemPrompt is OpenAIVisionReceiptOCRProvider.systemPrompt.
const OCRSystemPrompt = `You extract structured data from a photographed shop receipt. Respond with a ` +
	`single JSON object and nothing else, using exactly these keys (use null when a ` +
	`value is not clearly legible — never guess):
{"merchant": string|null, "total": number|null, "currency": string|null (ISO 4217, e.g. "EUR"), ` +
	`"date": string|null (YYYY-MM-DD), "taxId": string|null (merchant tax/VAT id), ` +
	`"taxTotal": number|null (total VAT/tax amount), ` +
	`"lineItems": [{"description": string, "amount": number, "quantity": number|null}]}
Amounts are numbers without currency symbols. If the image is not a receipt, ` +
	`return all null values and an empty lineItems array.

lineItems rules:
- One entry per purchased article, in the order printed.
- "amount" is the line total as printed, with quantity already applied. Do not ` +
	`multiply it yourself.
- "quantity" only when the receipt states units; otherwise null.
- Omit non-article lines: subtotals, totals, VAT summaries, discounts applied ` +
	`to the whole basket, loyalty points, change given, payment method lines.
- If the articles are not legible enough to read individually, return an empty ` +
	`array. A partial list is worse than none, because the totals will not reconcile.`

// ExtractedReceipt is the model's JSON object.
type ExtractedReceipt struct {
	Merchant, Currency, Date, TaxID *string
	Total, TaxTotal                 *float64
	LineItems                       []ExtractedLineItem
}

// ExtractedLineItem is one model-reported line.
type ExtractedLineItem struct {
	Description      *string
	Amount, Quantity *float64
}

// DecodeExtracted is `try? JSONDecoder().decode(ExtractedReceipt.self)`:
// exact keys, every present value of its declared type or null, else the
// whole object is rejected.
func DecodeExtracted(data []byte) (ExtractedReceipt, bool) {
	var out ExtractedReceipt
	obj, ok := object(data)
	if !ok {
		return out, false
	}
	if !optString(obj, "merchant", &out.Merchant) || !optDouble(obj, "total", &out.Total) ||
		!optString(obj, "currency", &out.Currency) || !optString(obj, "date", &out.Date) ||
		!optString(obj, "taxId", &out.TaxID) || !optDouble(obj, "taxTotal", &out.TaxTotal) {
		return out, false
	}
	if raw, ok := obj["lineItems"]; ok && !isNull(raw) {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || items == nil {
			return out, false
		}
		for _, it := range items {
			o, ok := object(it)
			if !ok {
				return out, false
			}
			var li ExtractedLineItem
			if !optString(o, "description", &li.Description) || !optDouble(o, "amount", &li.Amount) ||
				!optDouble(o, "quantity", &li.Quantity) {
				return out, false
			}
			out.LineItems = append(out.LineItems, li)
		}
	}
	return out, true
}

func isNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

func object(raw []byte) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

func optString(obj map[string]json.RawMessage, key string, dst **string) bool {
	raw, ok := obj[key]
	if !ok || isNull(raw) {
		return true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	*dst = &s
	return true
}

func optDouble(obj map[string]json.RawMessage, key string, dst **float64) bool {
	raw, ok := obj[key]
	if !ok || isNull(raw) {
		return true
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	*dst = &v
	return true
}

// HasContent is ExtractedReceipt.hasContent (currency and date alone don't
// count).
func (e ExtractedReceipt) HasContent() bool {
	return e.Merchant != nil || e.Total != nil || e.TaxID != nil || e.TaxTotal != nil || len(e.LineItems) > 0
}

// ToDraft is ExtractedReceipt.toDraft: trimmed strings (empty is nil), the
// currency upper-cased, confidence 0.6.
func (e ExtractedReceipt) ToDraft() *Draft {
	currency := trimmedPtr(e.Currency)
	if currency != nil {
		u := strings.ToUpper(*currency)
		currency = &u
	}
	return &Draft{
		Merchant: trimmedPtr(e.Merchant), Total: e.Total, Currency: currency,
		Date: trimmedPtr(e.Date), TaxID: trimmedPtr(e.TaxID), TaxTotal: e.TaxTotal,
		VATLines: []VATLine{}, LineItems: e.NormalizedLineItems(),
		Confidence: 0.6, Source: SourceOCR,
	}
}

// NormalizedLineItems drops lines without a description or an amount.
func (e ExtractedReceipt) NormalizedLineItems() []LineItem {
	out := []LineItem{}
	for _, raw := range e.LineItems {
		d := trimmedPtr(raw.Description)
		if d == nil || raw.Amount == nil {
			continue
		}
		out = append(out, LineItem{Description: *d, Amount: *raw.Amount, Quantity: raw.Quantity})
	}
	return out
}

func trimmedPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimFunc(*s, unicode.IsSpace)
	if t == "" {
		return nil
	}
	return &t
}
