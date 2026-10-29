// Package receipts ports ReceiptsController (fiscal QR parse, photo OCR,
// batch scan) and ReceiptImportController (commit reviewed drafts).
package receipts

import (
	"norviq-api/internal/features/expenseimport"
)

// Receipt sources (ReceiptSource).
const (
	SourceQR  = "qr"
	SourceOCR = "ocr"
)

// VATLine is ReceiptVATLine.
type VATLine struct {
	Rate   string   `json:"rate"`
	Base   *float64 `json:"base,omitempty"`
	Amount *float64 `json:"amount,omitempty"`
}

// LineItem is ReceiptLineItem.
type LineItem struct {
	Description string   `json:"description"`
	Amount      float64  `json:"amount"`
	Quantity    *float64 `json:"quantity,omitempty"`
}

// Draft is ReceiptDraft. VATLines and LineItems are never nil.
type Draft struct {
	Merchant   *string    `json:"merchant,omitempty"`
	Total      *float64   `json:"total,omitempty"`
	Currency   *string    `json:"currency,omitempty"`
	Date       *string    `json:"date,omitempty"`
	TaxID      *string    `json:"taxId,omitempty"`
	TaxTotal   *float64   `json:"taxTotal,omitempty"`
	VATLines   []VATLine  `json:"vatLines"`
	LineItems  []LineItem `json:"lineItems"`
	Confidence float64    `json:"confidence"`
	Source     string     `json:"source"`
	RawPayload *string    `json:"rawPayload,omitempty"`
}

// DraftResponse is ReceiptDraftResponse.
type DraftResponse struct {
	Recognized bool   `json:"recognized"`
	Draft      *Draft `json:"draft,omitempty"`
}

func draftResponse(d *Draft) DraftResponse { return DraftResponse{Recognized: d != nil, Draft: d} }

// BatchScanResponse is ReceiptBatchScanResponse.
type BatchScanResponse struct {
	Results []DraftResponse `json:"results"`
}

// parseQRRequest is ReceiptParseQRRequest.
type parseQRRequest struct {
	Payload string `json:"payload"`
}

// splitMode is ExpenseSplitMode.
type splitMode string

func (splitMode) SwiftEnum() (string, []string, bool) {
	return "ExpenseSplitMode", []string{"personal", "shared"}, false
}

// receiptSource is ExpenseReceiptSource.
type receiptSource string

func (receiptSource) SwiftEnum() (string, []string, bool) {
	return "ExpenseReceiptSource", []string{SourceQR, SourceOCR}, false
}

// receiptMetadata is ExpenseReceiptMetadata (synthesized Codable).
type receiptMetadata struct {
	Source        receiptSource `json:"source"`
	Merchant      *string       `json:"merchant"`
	TaxIdentifier *string       `json:"taxIdentifier"`
	IssuedOn      *string       `json:"issuedOn"`
	Currency      *string       `json:"currency"`
	Total         *float64      `json:"total"`
	VatTotal      *float64      `json:"vatTotal"`
}

// expenseRequest is StockPlanShared.ExpenseRequest's init(from:): splitMode
// and userSharePercent are decodeIfPresent with defaults applied after.
type expenseRequest struct {
	Title            string               `json:"title"`
	Amount           float64              `json:"amount"`
	Pillar           expenseimport.Pillar `json:"pillar"`
	OccurredOn       string               `json:"occurredOn"`
	LinkedPlanItemID *string              `json:"linkedPlanItemId"`
	CategoryID       *string              `json:"categoryId"`
	SplitMode        *splitMode           `json:"splitMode"`
	UserSharePercent *float64             `json:"userSharePercent"`
	ForeignAmount    *float64             `json:"foreignAmount"`
	ForeignCurrency  *string              `json:"foreignCurrency"`
	ExchangeRate     *float64             `json:"exchangeRate"`
	Notes            *string              `json:"notes"`
	ReceiptMetadata  *receiptMetadata     `json:"receiptMetadata"`
}

// importItem is ReceiptImportItem.
type importItem struct {
	ExternalID *string        `json:"externalId"`
	Expense    expenseRequest `json:"expense"`
}

// commitRequest is ReceiptImportCommitRequest.
type commitRequest struct {
	Items []importItem `json:"items"`
}

// RowError is ReceiptImportRowError.
type RowError struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// CommitResponse is ReceiptImportCommitResponse.
type CommitResponse struct {
	Imported      int        `json:"imported"`
	Skipped       int        `json:"skipped"`
	Failed        int        `json:"failed"`
	MonthsTouched []string   `json:"monthsTouched"`
	Errors        []RowError `json:"errors"`
}
