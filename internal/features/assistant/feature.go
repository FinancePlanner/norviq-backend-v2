// Package assistant ports the AI feature controllers: AIAssistantController
// (conversations, turns, tips, preferences, usage, pending actions, position
// memos), ActionCatalogController, AIChatController, AIInsightsController and
// AIViewSummaryController, plus the assistant's background jobs.
package assistant

import (
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/tax"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/assistantdb"

	"github.com/labstack/echo/v5"
)

// maxMessageCharacters is AIAssistantTurnCoordinator.maxMessageCharacters.
const maxMessageCharacters = 12000

// Feature mounts the AI feature routes.
type Feature struct {
	// Now overrides the clock (tests).
	Now func() time.Time
	// Sources wires the other domains' services the AI surfaces read; see
	// Sources. Nil leaves every source-dependent route on Vapor.
	Sources SourcesFunc
}

// Name implements server.Feature.
func (Feature) Name() string { return "assistant" }

type handlers struct {
	d   *server.Deps
	q   *assistantdb.Queries
	pii *crypto.PII
	ai  *ai.Platform
	ent *entitlements.Service
	now func() time.Time

	src     Sources
	actions *actionServices
	reads   *readTools
	memo    memoGenerator
}

func newHandlers(d *server.Deps, now func() time.Time, sources SourcesFunc) (*handlers, error) {
	p, err := aiplatform.Platform(d)
	if err != nil {
		return nil, err
	}
	pii, err := crypto.PIIFromEnv(d.Config.IsProduction(), d.Logger)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	q := assistantdb.New(d.DB)
	var src Sources
	if sources != nil {
		if src, err = sources(d); err != nil {
			return nil, err
		}
	}
	if src.Tax == nil {
		if src.Tax, err = tax.SharedDashboard(d); err != nil {
			return nil, err
		}
	}
	acts, err := newActionServices(d, q, now)
	if err != nil {
		return nil, err
	}
	mkt, err := market.SharedService(d)
	if err != nil {
		return nil, err
	}
	h := &handlers{
		d: d, q: q, pii: pii, ai: p, ent: entitlements.NewFromEnv(d.DB), now: now, src: src, actions: acts,
	}
	h.reads = &readTools{src: &h.src, expenses: acts.expenses, market: mkt, db: d.DB, now: now}
	return h, nil
}

// Register mounts the routes as routes.swift and each boot(routes:) did.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h, err := newHandlers(d, f.Now, f.Sources)
	if err != nil {
		return err
	}

	// ActionCatalogController: any authenticated credential, no limiter.
	r.V1.GET("/actions/catalog", h.actionCatalog, d.Auth.Scoped(), auth.Guard())

	// AIAssistantController sits under routes.swift's `ratelimit:ai`, which
	// runs before the controller's authenticator (per client IP).
	aiLimit := d.Limiter.Limit(20, 60*time.Second, "ratelimit:ai")
	scoped := []echo.MiddlewareFunc{aiLimit, d.Auth.Scoped(), auth.Guard()}
	read := append(append([]echo.MiddlewareFunc{}, scoped...), auth.RequireScope(auth.ScopeAssistantRead))
	write := append(append([]echo.MiddlewareFunc{}, scoped...), auth.RequireScope(auth.ScopeAssistantWrite))

	a := "/ai/assistant"
	r.V1.GET(a+"/conversations", h.listConversations, read...)
	r.V1.GET(a+"/conversations/:id", h.getConversation, read...)
	r.V1.GET(a+"/preferences", h.getPreferences, read...)
	r.V1.GET(a+"/tips", h.listTips, read...)
	r.V1.GET(a+"/usage", h.getUsage, read...)
	r.V1.GET(a+"/actions", h.listPendingActions, read...)

	r.V1.POST(a+"/conversations", h.createConversation, write...)
	r.V1.DELETE(a+"/conversations/:id", h.deleteConversation, write...)
	r.V1.POST(a+"/conversations/:id/messages", h.createMessage, write...)
	r.V1.PUT(a+"/preferences", h.updatePreferences, write...)
	r.V1.POST(a+"/tips/:id/seen", h.markTipSeen, write...)
	r.V1.DELETE(a+"/tips/:id", h.dismissTip, write...)
	r.V1.POST(a+"/actions/:id/cancel", h.cancelAction, write...)

	// Turn surfaces read through AIReadToolRegistry and the memo writer, so
	// they move to v2 only once every read source and the memo writer are
	// wired; until then they stay on Vapor.
	if h.turnsReady() {
		r.V1.POST(a+"/conversations/:id/chat", h.chat, write...)
		r.V1.POST(a+"/conversations/:id/stream", h.streamChat, write...)
		// /v1/ai/chat: SessionToken only, first-party, no scope middleware.
		r.V1.POST("/ai/chat", h.aiChat, aiLimit, d.Auth.Session(), auth.Guard())
	}
	// Confirming executes whatever write the assistant proposed, so it stays
	// first-party: SessionToken, guard, FirstPartyOnly (no scope check).
	r.V1.POST(a+"/actions/:id/confirm", h.confirmAction, aiLimit, d.Auth.Session(), auth.Guard(), auth.FirstPartyOnly())

	r.V1.GET("/ai/memos", h.listMemos, read...)
	r.V1.GET("/ai/memos/:id", h.getMemo, read...)
	r.V1.POST("/ai/memos/:id/bookmark", h.bookmarkMemo, write...)
	r.V1.DELETE("/ai/memos/:id", h.deleteMemo, write...)
	return nil
}
