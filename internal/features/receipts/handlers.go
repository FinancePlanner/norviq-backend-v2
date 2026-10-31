package receipts

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/multipartform"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/server"

	"github.com/labstack/echo/v5"
)

const (
	maxImageBytes      = 8 * 1024 * 1024
	maxImagesPerBatch  = 5
	maxConcurrentScans = 3
	// ScanMaxBytes is the scan route's body: .collect(maxSize: "26mb").
	ScanMaxBytes = 26 * 1024 * 1024
	// defaultMaxBodySize is configure.swift's routes.defaultMaxBodySize,
	// collected before every other route's handler.
	defaultMaxBodySize = 10 * 1024 * 1024
	maxCommitItems     = 300
)

type handlers struct {
	d        *server.Deps
	ai       *ai.Platform
	ent      *entitlements.Service
	provider OCRProvider
	expenses *expenses.Service
}

func (h *handlers) parseQR(c *echo.Context) error {
	if _, err := auth.Require(c); err != nil {
		return err
	}
	var req parseQRRequest
	if err := swiftjson.DecodeRequest(c, &req); err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, draftResponse(ParseQR(req.Payload)))
}

func (h *handlers) ocr(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	body, err := multipartform.ReadBody(c.Request(), defaultMaxBodySize)
	if err != nil {
		return err
	}
	if !h.provider.IsEnabled() {
		return httpx.Abort(http.StatusServiceUnavailable, "Receipt OCR is not available. Scan a QR code or enter the expense manually.")
	}
	// The photo goes to a third-party vision model.
	if err := h.ai.Consent.Require(c, s.UserID); err != nil {
		return err
	}
	ctx := c.Request().Context()
	// Pro/trial only, and unmetered on purpose: no receipt scan is counted
	// toward usage (owner decision I2, docs/vapor-bugs/money.md). The gate
	// is the only check; there is no per-scan quota.
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureReceiptScan, s.UserID); err != nil {
		return err
	}
	image, contentType, err := readImageUpload(c, body)
	if err != nil {
		return err
	}
	draft, err := h.provider.Extract(ctx, image, contentType)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, draftResponse(draft))
}

// scan returns one result per image, in upload order; an image that fails
// comes back recognized: false rather than failing the batch.
func (h *handlers) scan(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	body, err := multipartform.ReadBody(c.Request(), ScanMaxBytes)
	if err != nil {
		return err
	}
	if !h.provider.IsEnabled() {
		return httpx.Abort(http.StatusServiceUnavailable, "Receipt scanning is not available. Scan a QR code or enter the expense manually.")
	}
	if err := h.ai.Consent.Require(c, s.UserID); err != nil {
		return err
	}
	images, err := readImageUploads(c, body)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	// Pro/trial only, and unmetered on purpose, like /ocr: the images are not
	// counted toward usage (owner decision I2). Vapor's comment here said they
	// were "counted per image below", but nothing ever counted them.
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureReceiptScan, s.UserID); err != nil {
		return err
	}

	results := make([]DraftResponse, len(images))
	sem := make(chan struct{}, maxConcurrentScans)
	var wg sync.WaitGroup
	for i, img := range images {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			draft, err := h.provider.Extract(ctx, img.Body, img.ContentType)
			if err != nil {
				h.d.Logger.Warn(fmt.Sprintf("receipt_scan_image_failed index=%d error=%v", i, err))
				return
			}
			results[i] = draftResponse(draft)
		}()
	}
	wg.Wait()
	recognized := 0
	for _, r := range results {
		if r.Recognized {
			recognized++
		}
	}
	h.d.Logger.Info(fmt.Sprintf("receipt_scan_batch images=%d recognized=%d", len(images), recognized))
	return httpx.JSON(c, http.StatusOK, BatchScanResponse{Results: results})
}

const multipartRequired = "Upload receipt photos as multipart/form-data."

func readImageUploads(c *echo.Context, body []byte) ([]multipartform.Part, error) {
	boundary, isMultipart, hasBoundary := multipartform.Boundary(c.Request().Header.Get(echo.HeaderContentType))
	if !isMultipart {
		return nil, httpx.Abort(http.StatusUnsupportedMediaType, multipartRequired)
	}
	if !hasBoundary {
		return nil, httpx.BadRequest(multipartRequired)
	}
	if len(body) == 0 {
		return nil, httpx.BadRequest("Missing image field in multipart body.")
	}
	parts, err := multipartform.Parse(body, boundary)
	switch {
	case errors.Is(err, multipartform.ErrEmptyBoundary):
		return nil, httpx.BadRequest("Upload the form as multipart/form-data.")
	case err != nil:
		return nil, httpx.BadRequest("Could not read the uploaded form.")
	}
	files := multipartform.ImageFiles(parts)
	if len(files) == 0 {
		return nil, httpx.BadRequest("Missing image field in multipart body.")
	}
	if len(files) > maxImagesPerBatch {
		return nil, httpx.BadRequest(fmt.Sprintf("Scan at most %d receipts at a time.", maxImagesPerBatch))
	}
	for _, f := range files {
		if len(f.Body) > maxImageBytes {
			return nil, httpx.Abort(http.StatusRequestEntityTooLarge, "Each receipt image must be 8 MB or smaller.")
		}
	}
	return files, nil
}

// readImageUpload accepts multipart (a `file` or `image` part) or a raw
// body, each capped at 8 MB. (Vapor's collect(max: 8MB) was a no-op on an
// already collected body, so a raw body up to the 10 MB app cap got
// through.)
func readImageUpload(c *echo.Context, body []byte) ([]byte, string, error) {
	contentType := c.Request().Header.Get(echo.HeaderContentType)
	if _, isMultipart, _ := multipartform.Boundary(contentType); isMultipart {
		parts, err := multipartform.DecodeForm(contentType, body)
		if err != nil {
			return nil, "", err
		}
		file, err := multipartform.FileField(parts, "file")
		if err != nil {
			return nil, "", err
		}
		image, err := multipartform.FileField(parts, "image")
		if err != nil {
			return nil, "", err
		}
		if file == nil {
			file = image
		}
		if file == nil {
			return nil, "", httpx.BadRequest("Missing image field in multipart body.")
		}
		if len(file.Body) > maxImageBytes {
			return nil, "", httpx.Abort(http.StatusRequestEntityTooLarge, "Receipt image must be 8 MB or smaller.")
		}
		ct := file.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		return file.Body, ct, nil
	}
	if len(body) == 0 {
		return nil, "", httpx.BadRequest("Missing image body.")
	}
	if len(body) > maxImageBytes {
		return nil, "", httpx.Abort(http.StatusRequestEntityTooLarge, "Receipt image must be 8 MB or smaller.")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return body, contentType, nil
}

// commit is ReceiptImportController.commit: every row through
// ExpenseBulkImporter, after dropping rows the user already has and
// duplicates within the batch.
func (h *handlers) commit(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	// Re-checked at commit so a downgrade between scanning and confirming
	// cannot slip a Pro-scanned batch through.
	if err := h.ent.RequirePremium(ctx, entitlements.FeatureReceiptScan, s.UserID); err != nil {
		return err
	}
	var payload commitRequest
	if err := swiftjson.DecodeRequest(c, &payload); err != nil {
		return err
	}
	if len(payload.Items) == 0 {
		return httpx.BadRequest("No expenses to import.")
	}
	if len(payload.Items) > maxCommitItems {
		return httpx.BadRequest(fmt.Sprintf("Import at most %d expenses at a time.", maxCommitItems))
	}
	candidates := make([]expenses.Candidate, len(payload.Items))
	for i := range payload.Items {
		item := &payload.Items[i]
		candidates[i] = expenses.Candidate{Reference: i, Request: item.Expense.toRequest(), ExternalID: item.ExternalID}
	}

	importer := expenses.NewBulkImporter(h.expenses)
	db := h.expenses.Pool()
	existing, err := importer.ExistingKeys(ctx, db, s.UserID, candidates)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	accepted := []expenses.Candidate{}
	skipped := 0
	for i := range candidates {
		cand := &candidates[i]
		key := expenses.DedupKey(cand.Request.OccurredOn, cand.Request.Amount, cand.Request.Title, cand.ExternalID)
		if _, dup := existing[key]; dup || seen[key] {
			skipped++
			continue
		}
		seen[key] = true
		accepted = append(accepted, *cand)
	}
	outcome, err := importer.Insert(ctx, db, s.UserID, accepted)
	if err != nil {
		return err
	}
	skipped += len(outcome.Duplicates)
	errs := make([]RowError, 0, len(outcome.Failures))
	for idx, msg := range outcome.Failures {
		errs = append(errs, RowError{Index: idx, Message: msg})
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Index < errs[j].Index })
	months := make([]string, len(outcome.Months))
	for i, m := range outcome.Months {
		months[i] = m.UTC().Format("2006-01")
	}
	sort.Strings(months)
	resp := CommitResponse{Imported: outcome.Imported, Skipped: skipped, Failed: len(errs), MonthsTouched: months, Errors: errs}
	h.d.Logger.Info(fmt.Sprintf("receipt_import_commit imported=%d skipped=%d failed=%d", resp.Imported, resp.Skipped, resp.Failed))
	return httpx.JSON(c, http.StatusOK, resp)
}

func (r expenseRequest) toRequest() expenses.ExpenseRequest {
	out := expenses.NewExpenseRequest(r.Title, r.Amount, expenses.BudgetPillar(r.Pillar), r.OccurredOn, r.CategoryID)
	out.LinkedPlanItemID = r.LinkedPlanItemID
	if r.SplitMode != nil {
		out.SplitMode = string(*r.SplitMode)
	}
	if r.UserSharePercent != nil {
		out.UserSharePercent = *r.UserSharePercent
	}
	out.ForeignAmount, out.ForeignCurrency, out.ExchangeRate, out.Notes = r.ForeignAmount, r.ForeignCurrency, r.ExchangeRate, r.Notes
	if m := r.ReceiptMetadata; m != nil {
		out.ReceiptMetadata = &expenses.ExpenseReceiptMetadata{
			Source: string(m.Source), Merchant: m.Merchant, TaxIdentifier: m.TaxIdentifier,
			IssuedOn: m.IssuedOn, Currency: m.Currency, Total: m.Total, VatTotal: m.VatTotal,
		}
	}
	return out
}
