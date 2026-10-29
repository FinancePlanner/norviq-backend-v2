// Package terminalpositions ports TerminalPositionsController's
// /v1/terminal-positions routes, TerminalPositionsAIController
// (/v1/terminal-positions/ai/*, under routes.swift's `ratelimit:ai`
// limiter), TerminalPositionsService and TerminalAIAdvisor. The /v1/autobuys
// routes of the same controller are not ported here; the summary reads the
// autobuys table directly.
package terminalpositions

import (
	"os"
	"sync"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"

	"github.com/labstack/echo/v5"
)

// Feature mounts the routes.
type Feature struct {
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "terminalpositions" }

type handlers struct {
	d   *server.Deps
	svc *Service
	ent *entitlements.Service
	ai  *ai.Platform
}

// Register mounts the routes as the two Swift boot(routes:) built them.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	p, err := aiplatform.Platform(d)
	if err != nil {
		return err
	}
	svc := NewService(d.DB)
	svc.now = f.Now
	h := &handlers{d: d, svc: svc, ent: entitlements.NewFromEnv(d.DB), ai: p}

	// configure.swift builds app.terminalAIClient once and warns when it
	// is nil (the AI routes then answer 503).
	if _, ok := AIClient(d); !ok {
		model := lookupModel()
		reason := UnavailableReason(p.Config.Provider, p.Config.APIKey, p.Config.BaseURL, model)
		if reason == "" {
			reason = "unknown"
		}
		d.Logger.Warn("terminal_ai_disabled", "reason", reason)
	}

	protected := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard()}
	read := append(append([]echo.MiddlewareFunc{}, protected...), auth.RequireScope(auth.ScopePlanningRead))
	write := append(append([]echo.MiddlewareFunc{}, protected...), auth.RequireScope(auth.ScopePlanningWrite))

	// TerminalPositionsAIController, registered under the `ratelimit:ai`
	// limiter, which runs before the controller's own authenticator (so its
	// bucket is per client IP and shared with Vapor's).
	aiGroup := []echo.MiddlewareFunc{d.Limiter.Limit(20, 60*time.Second, "ratelimit:ai")}
	aiGroup = append(aiGroup, read...)
	r.V1.POST("/terminal-positions/ai/share-facts", h.shareFacts, aiGroup...)
	r.V1.POST("/terminal-positions/ai/scenario", h.scenario, aiGroup...)

	// TerminalPositionsController.
	r.V1.GET("/terminal-positions", h.index, read...)
	r.V1.GET("/terminal-positions/summary", h.summary, read...)
	r.V1.POST("/terminal-positions", h.create, write...)
	r.V1.PUT("/terminal-positions/order", h.reorder, write...)
	r.V1.PATCH("/terminal-positions/:id", h.update, write...)
	r.V1.DELETE("/terminal-positions/:id", h.delete, write...)
	r.V1.POST("/terminal-positions/:id/duplicate", h.duplicate, write...)
	return nil
}

// MARK: - app.terminalAIClient

type clientEntry struct {
	client ai.ChatClient
}

var (
	clientsMu sync.Mutex
	clients   = map[*server.Deps]clientEntry{}
)

func lookupModel() *string {
	if v, ok := os.LookupEnv("TERMINAL_AI_MODEL"); ok {
		return &v
	}
	return nil
}

// AIClient is `app.terminalAIClient`: the web-search chat client for terminal
// suggestions, built once per process from AI_PROVIDER's key and base URL
// and TERMINAL_AI_MODEL. ok is false (client nil) when Vapor's would be nil:
// no key or base URL, or a non-OpenRouter provider without TERMINAL_AI_MODEL.
func AIClient(d *server.Deps) (ai.ChatClient, bool) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if e, ok := clients[d]; ok {
		return e.client, e.client != nil
	}
	var client ai.ChatClient
	if p, err := aiplatform.Platform(d); err == nil {
		if c := LiveClient(p.Config, lookupModel(), p.HTTP, d.Logger); c != nil {
			client = c
		}
	}
	clients[d] = clientEntry{client: client}
	return client, client != nil
}

// SetAIClient replaces app.terminalAIClient for d; nil makes the AI routes
// answer 503 (tests).
func SetAIClient(d *server.Deps, c ai.ChatClient) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	clients[d] = clientEntry{client: c}
}
