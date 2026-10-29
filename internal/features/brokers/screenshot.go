package brokers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/portfolios"

	"github.com/google/uuid"
)

// Extraction is ScreenshotExtraction: one image's rows, or why it gave none.
type Extraction struct {
	Kind      string
	Rows      []ExtractedRow
	Rejection *string
}

// ExtractedRow is ScreenshotExtractedRow.
type ExtractedRow struct {
	Symbol     string
	Shares     *float64
	BuyPrice   *float64
	BuyDate    *string
	Confidence *float64
}

func rejected(reason, kind string) Extraction { return Extraction{Kind: kind, Rejection: &reason} }

// Extractor is ScreenshotPortfolioExtractor.
type Extractor interface {
	IsEnabled() bool
	Extract(ctx context.Context, image []byte, contentType string) (Extraction, error)
}

// DisabledExtractor is DisabledScreenshotPortfolioExtractor.
type DisabledExtractor struct{}

// IsEnabled implements Extractor.
func (DisabledExtractor) IsEnabled() bool { return false }

// Extract implements Extractor.
func (DisabledExtractor) Extract(context.Context, []byte, string) (Extraction, error) {
	return rejected("Screenshot import is not available.", kindUnknown), nil
}

// ExtractorFromEnv is ScreenshotPortfolioExtractorBootstrap.fromEnvironment:
// on with RECEIPT_OCR_PROVIDER=openai|openai-vision|vision and complete AI
// credentials; the model is PORTFOLIO_SCREENSHOT_MODEL, else
// RECEIPT_OCR_MODEL, else the chat model.
func ExtractorFromEnv(env func(string) string, cfg ai.ProviderConfig, client *http.Client, logger *slog.Logger) Extractor {
	switch strings.ToLower(portfolios.TrimSwift(env("RECEIPT_OCR_PROVIDER"))) {
	case "openai", "openai-vision", "vision":
	default:
		return DisabledExtractor{}
	}
	model := ""
	for _, m := range []string{portfolios.TrimSwift(env("PORTFOLIO_SCREENSHOT_MODEL")), portfolios.TrimSwift(env("RECEIPT_OCR_MODEL")), cfg.ChatModel} {
		if m != "" {
			model = m
			break
		}
	}
	x := &VisionExtractor{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: model, Client: client, Logger: logger}
	if !x.IsEnabled() {
		logger.Warn("Portfolio screenshot import enabled but AI credentials/model are missing; disabled.")
		return DisabledExtractor{}
	}
	logger.Info("portfolio_screenshot configured model=" + model)
	return x
}

// VisionExtractor is OpenAIVisionScreenshotExtractor. It classifies before
// extracting: a holdings list's price column is market value, never a cost
// basis, so only trade confirmations keep buyPrice and buyDate.
type VisionExtractor struct {
	APIKey, BaseURL, Model string
	Client                 *http.Client
	Logger                 *slog.Logger
}

// screenshotMaxTokens covers a dense holdings table.
const screenshotMaxTokens = 3000

// IsEnabled implements Extractor.
func (x *VisionExtractor) IsEnabled() bool {
	return x.APIKey != "" && x.BaseURL != "" && x.Model != ""
}

// Extract implements Extractor.
func (x *VisionExtractor) Extract(ctx context.Context, image []byte, contentType string) (Extraction, error) {
	if !x.IsEnabled() {
		return rejected("Screenshot import is not available.", kindUnknown), nil
	}
	body := ai.NewVisionRequest(x.Model, []ai.VisionMessage{
		{Role: "system", Content: []ai.VisionPart{ai.VisionText(screenshotSystemPrompt)}},
		{Role: "user", Content: []ai.VisionPart{
			ai.VisionText("Classify this screenshot and extract its rows. Return the JSON object."),
			ai.VisionImage(image, contentType),
		}},
	}, 0, screenshotMaxTokens)
	text, err := ai.CompleteVisionJSON(ctx, x.Client, body, x.APIKey, x.BaseURL, "portfolio_screenshot", x.Logger)
	if err != nil {
		return Extraction{}, err
	}
	var parsed extractedScreenshot
	if text == nil || !parsed.decode([]byte(*text)) {
		x.Logger.Warn("portfolio_screenshot_undecodable model=" + x.Model)
		return rejected("Could not read this image. Try a sharper, uncropped screenshot.", kindUnknown), nil
	}
	return parsed.toExtraction(), nil
}

const screenshotSystemPrompt = `You read a screenshot from a stock broker or investing app and turn it into ` +
	`structured rows. Respond with a single JSON object and nothing else:
{"kind": "holdings" | "trades" | "unknown",
 "rows": [{"symbol": string, "shares": number|null, "buyPrice": number|null, ` +
	`"buyDate": string|null (YYYY-MM-DD), "confidence": number (0..1)}]}

First classify:
- "holdings": a positions or portfolio list — what the user owns right now.
- "trades": trade confirmations, order history or executions — individual ` +
	`buys and sells with a price and a date.
- "unknown": anything else (a chart, a news screen, a watchlist with no ` +
	`quantities, a bank statement), or an image too blurred or cropped to read. ` +
	`Return "unknown" with an empty rows array. Do not force a classification.

Then extract, one row per visible position or trade:
- "symbol" is the exchange ticker, uppercase, without the exchange prefix or ` +
	`suffix (write "AAPL", not "NASDAQ:AAPL" or "AAPL.US"). If only a company name ` +
	`is shown and you are not certain of its ticker, skip the row.
- "shares" is the quantity held or traded. Fractional values are normal.
- For kind "holdings": set "buyPrice" ONLY if the screen explicitly labels a ` +
	`cost basis, average cost or average price. A last price, market price, current ` +
	`value or day-change figure is NOT a buy price — set null. This is the single ` +
	`most damaging mistake you can make here.
- For kind "trades": "buyPrice" is the execution price per share and "buyDate" ` +
	`the execution date. Include only BUY rows; skip sells, dividends and fees.
- "confidence" is your own 0..1 certainty for that row's numbers.
- Never guess a number. Null is always better than a plausible invention — ` +
	`these values become someone's financial records.`

// extractedScreenshot is ExtractedScreenshot as a plain JSONDecoder reads
// it: exact keys, every field optional, a wrongly typed value fails the
// whole object.
type extractedScreenshot struct {
	kind *string
	rows []extractedRawRow
}

type extractedRawRow struct {
	symbol, buyDate              *string
	shares, buyPrice, confidence *float64
}

func exactObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

func optionalJSON[T any](obj map[string]json.RawMessage, key string) (*T, bool) {
	raw, ok := obj[key]
	if !ok || isJSONNull(raw) {
		return nil, true
	}
	var v T
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	return &v, true
}

func (e *extractedScreenshot) decode(data []byte) bool {
	obj, ok := exactObject(data)
	if !ok {
		return false
	}
	if e.kind, ok = optionalJSON[string](obj, "kind"); !ok {
		return false
	}
	rawRows, ok := optionalJSON[[]json.RawMessage](obj, "rows")
	if !ok {
		return false
	}
	if rawRows == nil {
		return true
	}
	for _, r := range *rawRows {
		ro, ok := exactObject(r)
		if !ok {
			return false
		}
		var row extractedRawRow
		var ok1, ok2, ok3, ok4, ok5 bool
		row.symbol, ok1 = optionalJSON[string](ro, "symbol")
		row.shares, ok2 = optionalJSON[float64](ro, "shares")
		row.buyPrice, ok3 = optionalJSON[float64](ro, "buyPrice")
		row.buyDate, ok4 = optionalJSON[string](ro, "buyDate")
		row.confidence, ok5 = optionalJSON[float64](ro, "confidence")
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
			return false
		}
		e.rows = append(e.rows, row)
	}
	return true
}

// toExtraction maps the model's JSON to rows: unknown screens are rejected,
// symbol-less rows dropped, and only trades keep a price and date.
func (e extractedScreenshot) toExtraction() Extraction {
	kind := kindUnknown
	if e.kind != nil {
		switch k := strings.ToLower(*e.kind); k {
		case kindHoldings, kindTrades:
			kind = k
		}
	}
	if kind == kindUnknown {
		return rejected("This doesn't look like a portfolio or trade screen. Upload a holdings list or a trade confirmation.", kindUnknown)
	}
	var rows []ExtractedRow
	for _, r := range e.rows {
		if r.symbol == nil {
			continue
		}
		symbol := strings.ToUpper(portfolios.TrimSwift(*r.symbol))
		if symbol == "" {
			continue
		}
		row := ExtractedRow{Symbol: symbol, Shares: r.shares, Confidence: r.confidence}
		if kind == kindTrades {
			row.BuyPrice, row.BuyDate = r.buyPrice, r.buyDate
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return rejected("No positions could be read from this image.", kind)
	}
	return Extraction{Kind: kind, Rows: rows}
}

type screenshotImage struct {
	data        []byte
	contentType string
}

// maxScreenshotImages bounds one request's vision spend.
const maxScreenshotImages = 3

// screenshotPreview is ScreenshotPortfolioImportService.preview.
func (s *service) screenshotPreview(ctx context.Context, images []screenshotImage, provider string, rawListID *string, userID uuid.UUID, consent func() error) (ScreenshotPreviewResponse, error) {
	if err := consent(); err != nil {
		return ScreenshotPreviewResponse{}, err
	}
	items, errs, kind, err := s.extractRows(ctx, images)
	if err != nil {
		return ScreenshotPreviewResponse{}, err
	}
	enriched, err := s.preview(ctx, items, errs, provider, rawListID, userID)
	if err != nil {
		return ScreenshotPreviewResponse{}, err
	}
	return ScreenshotPreviewResponse{
		Provider: enriched.Provider, Kind: kind, Items: enriched.Items, Errors: enriched.Errors, ImageCount: len(images),
	}, nil
}

// extractRows runs the images through the extractor (at most three at once)
// and flattens them, re-indexing lines across the batch.
func (s *service) extractRows(ctx context.Context, images []screenshotImage) ([]PreviewItem, []PreviewError, string, error) {
	if !s.extractor.IsEnabled() {
		return nil, nil, "", errScreenshotUnavailable()
	}
	extractions := make([]Extraction, len(images))
	errs := make([]error, len(images))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxScreenshotImages)
	for i, img := range images {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			extractions[i], errs[i] = s.extractor.Extract(ctx, img.data, img.contentType)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, nil, "", err
		}
	}
	items := []PreviewItem{}
	previewErrs := []PreviewError{}
	kinds := map[string]bool{}
	for i, x := range extractions {
		if x.Rejection != nil {
			msg := *x.Rejection
			if len(images) > 1 {
				msg = "Image " + itoa(i+1) + ": " + msg
			}
			previewErrs = append(previewErrs, PreviewError{Line: i, Message: msg})
			continue
		}
		kinds[x.Kind] = true
		for _, r := range x.Rows {
			items = append(items, PreviewItem{
				Line: len(items), Symbol: r.Symbol, Shares: r.Shares, BuyPrice: r.BuyPrice, BuyDate: r.BuyDate, Confidence: r.Confidence,
			})
		}
	}
	kind := kindUnknown
	if len(kinds) == 1 {
		for k := range kinds {
			kind = k
		}
	}
	return items, previewErrs, kind, nil
}

func errScreenshotUnavailable() error {
	return httpx.Abort(http.StatusServiceUnavailable, "Screenshot import is not available. Import a CSV or add positions manually.")
}
