package expenseimport

import (
	"context"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/expenses"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/expenseimportdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts ExpenseImportController (AI-assisted .xlsx import).
type Feature struct {
	// Provider overrides SPREADSHEET_IMPORT_AI (tests).
	Provider AnalysisProvider
	// Service overrides the shared expenses service (tests).
	Service *expenses.Service
	// Entitlements overrides the environment's billing config (tests).
	Entitlements *entitlements.Config
	Now          func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "expenseimport" }

// Register implements server.Feature, mirroring routes.swift's throttled
// group (rate limit, then idempotency) around ExpenseImportController.boot.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	p, err := aiplatform.Platform(d)
	if err != nil {
		return err
	}
	pii, err := crypto.PIIFromEnv(d.Config.IsProduction(), d.Logger)
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
		provider = ProviderFromEnv(ai.OSEnv, p.Config, p.HTTP, d.Logger)
	}
	ent := entitlements.NewFromEnv(d.DB)
	if f.Entitlements != nil {
		ent = entitlements.New(d.DB, *f.Entitlements)
	}
	queries := expenseimportdb.New(d.DB)
	h := &handlers{
		d: d, ai: p, ent: ent,
		svc: &Service{
			Provider: provider,
			Store:    &SessionStore{Queries: queries, PII: pii, Now: f.Now},
			Expenses: bulkExpenses{svc: svc, bulk: expenses.NewBulkImporter(svc)},
			Queries:  queries,
		},
	}

	mw := []echo.MiddlewareFunc{
		d.Limiter.Limit(10, time.Minute, "ratelimit:expense-import"),
		d.Limiter.Idempotency("idempotency:expense-import"),
		d.Auth.Scoped(), auth.Guard(),
		auth.RequireScope(auth.ScopeExpensesWrite), auth.FirstPartyOnly(),
	}
	const base = "/expenses/import/spreadsheet"
	r.V1.POST(base, h.analyze, mw...)
	r.V1.GET(base+"/:sessionID", h.fetch, mw...)
	r.V1.POST(base+"/:sessionID/preview", h.preview, mw...)
	r.V1.POST(base+"/:sessionID/commit", h.commit, mw...)
	r.V1.DELETE(base+"/:sessionID", h.discard, mw...)
	return nil
}

// bulkExpenses is Expenses over ExpensesService and ExpenseBulkImporter,
// both on req.db (the pool), as Vapor ran them.
type bulkExpenses struct {
	svc  *expenses.Service
	bulk expenses.BulkImporter
}

func (b bulkExpenses) Categories(ctx context.Context, userID uuid.UUID) ([]Category, error) {
	rows, err := b.svc.GetCategories(ctx, b.svc.Pool(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Category, len(rows))
	for i, c := range rows {
		out[i] = Category{ID: c.ID, Name: c.Name}
		if c.Pillar != nil {
			p := string(*c.Pillar)
			out[i].Pillar = &p
		}
	}
	return out, nil
}

func (b bulkExpenses) CreateCategory(ctx context.Context, userID uuid.UUID, name string, pillar *string) (Category, error) {
	req := expenses.ExpenseCategoryRequest{Name: name}
	if pillar != nil {
		p := expenses.BudgetPillar(*pillar)
		req.Pillar = &p
	}
	c, err := b.svc.CreateCategory(ctx, b.svc.Pool(), userID, req)
	if err != nil {
		return Category{}, err
	}
	out := Category{ID: c.ID, Name: c.Name}
	if c.Pillar != nil {
		p := string(*c.Pillar)
		out.Pillar = &p
	}
	return out, nil
}

func (b bulkExpenses) ExistingDedupKeys(ctx context.Context, userID uuid.UUID, from, to *time.Time) (map[string]bool, error) {
	var r *expenses.DayRange
	if from != nil && to != nil {
		r = &expenses.DayRange{From: *from, To: *to}
	}
	keys, err := b.bulk.ExistingDedupKeys(ctx, b.svc.Pool(), userID, r)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(keys))
	for k := range keys {
		out[k] = true
	}
	return out, nil
}

func (b bulkExpenses) ExistingExternalIDs(ctx context.Context, userID uuid.UUID, ids []string) (map[string]bool, error) {
	stored, err := b.bulk.ExistingExternalIDs(ctx, b.svc.Pool(), userID, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(stored))
	for id := range stored {
		out[id] = true
	}
	return out, nil
}

func (b bulkExpenses) Insert(ctx context.Context, userID uuid.UUID, candidates []Candidate) (InsertOutcome, error) {
	in := make([]expenses.Candidate, len(candidates))
	for i, c := range candidates {
		r := c.Request
		req := expenses.NewExpenseRequest(r.Title, r.Amount, expenses.BudgetPillar(r.Pillar), r.OccurredOn, r.CategoryID)
		req.ForeignAmount, req.ForeignCurrency, req.ExchangeRate, req.Notes = r.ForeignAmount, r.ForeignCurrency, r.ExchangeRate, r.Notes
		in[i] = expenses.Candidate{Reference: c.Reference, Request: req, ExternalID: r.ExternalID}
	}
	out, err := b.bulk.Insert(ctx, b.svc.Pool(), userID, in)
	if err != nil {
		return InsertOutcome{}, err
	}
	return InsertOutcome(out), nil
}

func (bulkExpenses) DedupKey(occurredOn string, amount float64, title string) string {
	return expenses.DedupKey(occurredOn, amount, title, nil)
}
