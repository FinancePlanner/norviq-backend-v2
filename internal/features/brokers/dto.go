package brokers

import (
	"norviq-api/internal/apijson"
	"norviq-api/internal/features/stocks"
	"norviq-api/internal/store/brokersdb"
)

// ConnectionResponse is StockPlanShared.BrokerConnectionResponse.
type ConnectionResponse struct {
	ID              string        `json:"id"`
	Provider        string        `json:"provider"`
	Status          string        `json:"status"`
	DisplayName     *string       `json:"displayName,omitempty"`
	StatusDetail    *string       `json:"statusDetail,omitempty"`
	ConnectedAt     *apijson.Time `json:"connectedAt,omitempty"`
	LastSyncedAt    *apijson.Time `json:"lastSyncedAt,omitempty"`
	PortfolioListID *string       `json:"portfolioListId,omitempty"`
}

// connectionResponse is BrokerConnectionResponse(from:).
func connectionResponse(c brokersdb.BrokerConnection) ConnectionResponse {
	r := ConnectionResponse{
		ID:           apijson.FormatUUID(c.ID),
		Provider:     c.Provider,
		Status:       c.Status,
		DisplayName:  c.DisplayName,
		StatusDetail: c.StatusDetail,
		ConnectedAt:  apijson.TimePtr(c.ConnectedAt),
		LastSyncedAt: apijson.TimePtr(c.LastSyncedAt),
	}
	if c.PortfolioListID != nil {
		s := apijson.FormatUUID(*c.PortfolioListID)
		r.PortfolioListID = &s
	}
	return r
}

// HoldingResponse is StockPlanShared.BrokerHoldingResponse.
type HoldingResponse struct {
	Symbol   string  `json:"symbol"`
	Quantity float64 `json:"quantity"`
	Currency string  `json:"currency"`
}

// SyncResponse is StockPlanShared.BrokerSyncResponse.
type SyncResponse struct {
	RunID    string `json:"runId"`
	Status   string `json:"status"`
	Inserted int    `json:"inserted"`
	Updated  int    `json:"updated"`
	Removed  int    `json:"removed"`
}

// SyncStatusResponse is StockPlanShared.BrokerSyncStatusResponse.
type SyncStatusResponse struct {
	Status       string        `json:"status"`
	LastSyncedAt *apijson.Time `json:"lastSyncedAt,omitempty"`
	IsStale      bool          `json:"isStale"`
	StatusDetail *string       `json:"statusDetail,omitempty"`
}

// connectStartRequest is StockPlanShared.BrokerConnectStartRequest.
type connectStartRequest struct {
	RedirectURI     string  `json:"redirectURI"`
	PortfolioListID *string `json:"portfolioListId"`
}

// ConnectStartResponse is StockPlanShared.BrokerConnectStartResponse.
type ConnectStartResponse struct {
	FlowID           string `json:"flowId"`
	AuthorizationURL string `json:"authorizationURL"`
	ExpiresIn        int    `json:"expiresIn"`
}

// connectCredentialsRequest is StockPlanShared.BrokerConnectCredentialsRequest.
type connectCredentialsRequest struct {
	Token           string  `json:"token"`
	QueryID         string  `json:"queryId"`
	PortfolioListID *string `json:"portfolioListId"`
}

// Existing position kinds (CsvImportExistingPositionKind).
const (
	kindNone     = "none"
	kindManual   = "manual"
	kindImported = "imported"
	kindMixed    = "mixed"
)

// PreviewItem is StockPlanShared.CsvImportPreviewItem.
type PreviewItem struct {
	Line                      int      `json:"line"`
	Symbol                    string   `json:"symbol"`
	Shares                    *float64 `json:"shares,omitempty"`
	BuyPrice                  *float64 `json:"buyPrice,omitempty"`
	BuyDate                   *string  `json:"buyDate,omitempty"`
	Notes                     *string  `json:"notes,omitempty"`
	ExistingPositionKind      string   `json:"existingPositionKind"`
	WillReplaceExistingImport bool     `json:"willReplaceExistingImport"`
	Confidence                *float64 `json:"confidence,omitempty"`
}

// PreviewError is StockPlanShared.CsvImportPreviewError.
type PreviewError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// PreviewResponse is StockPlanShared.CsvImportPreviewResponse.
type PreviewResponse struct {
	Provider string         `json:"provider"`
	Items    []PreviewItem  `json:"items"`
	Errors   []PreviewError `json:"errors"`
}

// CommitResponse is StockPlanShared.CsvImportCommitResponse.
type CommitResponse struct {
	Provider          string                 `json:"provider"`
	Inserted          []stocks.StockResponse `json:"inserted"`
	Updated           []stocks.StockResponse `json:"updated"`
	Errors            []PreviewError         `json:"errors"`
	ReplacedSymbols   []string               `json:"replacedSymbols"`
	ImportedLotsCount int                    `json:"importedLotsCount"`
}

// Screenshot import kinds (ScreenshotImportKind).
const (
	kindHoldings = "holdings"
	kindTrades   = "trades"
	kindUnknown  = "unknown"
)

// ScreenshotPreviewResponse is StockPlanShared.ScreenshotImportPreviewResponse.
type ScreenshotPreviewResponse struct {
	Provider   string         `json:"provider"`
	Kind       string         `json:"kind"`
	Items      []PreviewItem  `json:"items"`
	Errors     []PreviewError `json:"errors"`
	ImageCount int            `json:"imageCount"`
}

// commitItem is CsvImportPreviewItem as its lenient decoder reads it.
type commitItem struct {
	Line                      int      `json:"line"`
	Symbol                    string   `json:"symbol"`
	Shares                    *float64 `json:"shares"`
	BuyPrice                  *float64 `json:"buyPrice"`
	BuyDate                   *string  `json:"buyDate"`
	Notes                     *string  `json:"notes"`
	ExistingPositionKind      *string  `json:"existingPositionKind"`
	WillReplaceExistingImport *bool    `json:"willReplaceExistingImport"`
	Confidence                *float64 `json:"confidence"`
}

// screenshotCommitRequest is StockPlanShared.ScreenshotImportCommitRequest.
type screenshotCommitRequest struct {
	Provider             string       `json:"provider"`
	PortfolioListID      *string      `json:"portfolioListId"`
	Items                []commitItem `json:"items"`
	ConfirmMergeExisting *bool        `json:"confirmMergeExisting"`
}
