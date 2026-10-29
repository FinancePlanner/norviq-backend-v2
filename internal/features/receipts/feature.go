package receipts

import (
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"

	"github.com/labstack/echo/v5"
)

// Feature mounts ReceiptsController and ReceiptImportController behind
// routes.swift's throttles.
type Feature struct {
	// Provider overrides RECEIPT_OCR_PROVIDER (tests).
	Provider OCRProvider
	// Service overrides the shared expenses service (tests).
	Service *expenses.Service
	// Entitlements overrides the environment's billing config (tests).
	Entitlements *entitlements.Config
}

// Name implements server.Feature.
func (Feature) Name() string { return "receipts" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	p, err := aiplatform.Platform(d)
	if err != nil {
		return err
	}
	svc := f.Service
	if svc == nil {
		if svc, err = expenses.SharedService(d); err != nil {
			return err
		}
	}
	provider := f.Provider
	if provider == nil {
		provider = OCRProviderFromEnv(ai.OSEnv, p.Config, p.HTTP, d.Logger)
	}
	ent := entitlements.NewFromEnv(d.DB)
	if f.Entitlements != nil {
		ent = entitlements.New(d.DB, *f.Entitlements)
	}
	h := &handlers{d: d, ai: p, ent: ent, provider: provider, expenses: svc}

	write := []echo.MiddlewareFunc{
		d.Limiter.Limit(30, time.Minute, "ratelimit:receipts"),
		d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeExpensesWrite),
	}
	r.V1.POST("/receipts/parse-qr", h.parseQR, write...)
	r.V1.POST("/receipts/ocr", h.ocr, write...)
	// Batch scan spends one vision call per image: first-party only. Three
	// photos at 8 MB each outrun the app-wide 10 MB body cap.
	r.V1.POST("/receipts/scan", h.scan, append(write, auth.FirstPartyOnly())...)
	r.AllowLargeBody("POST", "/v1/receipts/scan", ScanMaxBytes)

	r.V1.POST("/expenses/import/receipts/commit", h.commit,
		d.Limiter.Limit(10, time.Minute, "ratelimit:expense-import"),
		d.Limiter.Idempotency("idempotency:receipt-import"),
		d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeExpensesWrite))
	return nil
}
