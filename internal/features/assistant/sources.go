package assistant

import (
	"context"

	"norviq-api/internal/features/earnings"
	"norviq-api/internal/features/tax"
	"norviq-api/internal/server"

	"github.com/google/uuid"
)

// Sources are the application services the AI surfaces read that live in
// other domains: DashboardService, MacroService, CryptoService and
// InsightsService. Each returns its Swift response DTO as a Go value that
// encodes to the same JSON; callers that need a figure from it (the highlight
// builders) re-decode that JSON, so any adapter that marshals correctly fits.
//
// A nil field means the source is not wired, and the routes that need it are
// not registered, so they answer 404.
type Sources struct {
	// Dashboard is dashboardService.dashboard(userId:req:on:).
	Dashboard func(ctx context.Context, userID uuid.UUID) (any, error)
	// DashboardInsights is dashboardService.insights(userId:req:on:).
	DashboardInsights func(ctx context.Context, userID uuid.UUID) (any, error)
	// Inflation, Economy and PolicyWatch are macroService.currentInflation,
	// .economy and .policyWatch; country is MacroCountry's raw value.
	Inflation   func(ctx context.Context, country string) (any, error)
	Economy     func(ctx context.Context, country string) (any, error)
	PolicyWatch func(ctx context.Context, country string) (any, error)
	// CryptoPortfolio is cryptoService.listPortfolio(userId:on:).
	CryptoPortfolio func(ctx context.Context, userID uuid.UUID) (any, error)
	// CryptoQuotesShort is cryptoService.batchQuotes(short: true, on:).
	CryptoQuotesShort func(ctx context.Context) (any, error)
	// InsightsSummary is insightsService.summary(days:on:).
	InsightsSummary func(ctx context.Context, days int) (any, error)

	// StockNews is marketNewsArchiveService.news(symbol:limit:), returning a
	// StockNews array. Nil reads the archived rows only (Vapor's behavior
	// with no news provider configured); the memo writer still runs.
	StockNews func(ctx context.Context, symbol string, limit int) (any, error)

	// ThesisFeed is thesisWatchService.feed(userId:scope: .forYou, sector:
	// nil, limit:cursor: nil), for the Telegram /news command.
	ThesisFeed func(ctx context.Context, userID uuid.UUID, limit int) (any, error)

	// Holdings lists the user's positions, across every portfolio unless
	// portfolioID is given. Portfolios lists their portfolios.
	Holdings   func(ctx context.Context, userID uuid.UUID, portfolioID *string, limit int) (any, error)
	Portfolios func(ctx context.Context, userID uuid.UUID) (any, error)
	// MarketBrief is the latest published brief in a language.
	MarketBrief func(ctx context.Context, language string) (any, error)
	// Earnings is the earnings calendar.
	Earnings func(ctx context.Context, q earnings.Query) ([]earnings.ItemResponse, error)

	// ExtendedReads are the read tools restricted to in-app sessions, by tool
	// name. A missing entry reports that tool as unavailable.
	ExtendedReads map[string]ExtendedRead

	// Tax is taxService.dashboard; Register fills it from the tax package.
	Tax tax.DashboardFunc
}

// SourcesFunc builds Sources at Register time.
// ExtendedRead reads one user's data for an extended tool.
type ExtendedRead func(ctx context.Context, userID uuid.UUID) (any, error)

type SourcesFunc func(d *server.Deps) (Sources, error)

func (s *Sources) hasDashboard() bool { return s.Dashboard != nil && s.DashboardInsights != nil }

func (s *Sources) hasMacro() bool {
	return s.Inflation != nil && s.Economy != nil && s.PolicyWatch != nil
}

func (s *Sources) hasCrypto() bool { return s.CryptoPortfolio != nil && s.CryptoQuotesShort != nil }

// readToolsReady: every AIReadToolRegistry tool has its source, so the turn
// surfaces (assistant chat/stream, /v1/ai/chat) can be served from v2.
func (s *Sources) readToolsReady() bool {
	return s.hasDashboard() && s.hasMacro() && s.hasCrypto() && s.InsightsSummary != nil
}

// viewSummaryReady: every AIViewSummaryScope has its source.
func (s *Sources) viewSummaryReady() bool {
	return s.hasDashboard() && s.hasMacro() && s.hasCrypto() && s.Tax != nil
}

// turnsReady: the assistant chat/stream and /v1/ai/chat can be served.
func (h *handlers) turnsReady() bool { return h.src.readToolsReady() && h.memo != nil }
