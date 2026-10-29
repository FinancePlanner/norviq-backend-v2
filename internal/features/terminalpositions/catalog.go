package terminalpositions

import (
	"context"

	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"

	"github.com/google/uuid"
)

// Catalog is the entry point for the AI action catalog's terminal actions
// (ActionCatalog+TerminalPositions.swift). Each method takes (ctx, userID,
// args) and returns the same DTOs the routes return. Errors Vapor threw as
// Abort are *httpx.Error (use .Reason as Vapor's catalog used abort.reason);
// the Swift catalog's own argument checks ("ticker is required") and
// errorPayload/statusPayload wrapping stay with the caller.
type Catalog struct {
	d   *server.Deps
	svc *Service
	ent *entitlements.Service
}

// NewCatalog builds the catalog entry point over d.
func NewCatalog(d *server.Deps) *Catalog {
	return &Catalog{d: d, svc: NewService(d.DB), ent: entitlements.NewFromEnv(d.DB)}
}

// Service is the underlying TerminalPositionsService.
func (c *Catalog) Service() *Service { return c.svc }

// List is get_terminal_positions: every row with the user's currency.
func (c *Catalog) List(ctx context.Context, userID uuid.UUID) (ListResponse, error) {
	rows, err := c.svc.List(ctx, userID, nil)
	if err != nil {
		return ListResponse{}, err
	}
	return c.svc.ListResponseFor(ctx, userID, rows)
}

// GetArgs are get_terminal_position's arguments.
type GetArgs struct {
	Ticker string
}

// Get is get_terminal_position: the first row for the normalised ticker, or
// nil (Vapor's statusPayload("none")) when there is none. An invalid ticker
// is a 422 *httpx.Error (the Swift action answered "ticker is required").
func (c *Catalog) Get(ctx context.Context, userID uuid.UUID, args GetArgs) (*PositionResponse, error) {
	symbol, err := NormalisedTicker(args.Ticker)
	if err != nil {
		return nil, err
	}
	rows, err := c.svc.List(ctx, userID, &symbol)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	out := ToResponse(rows[0])
	return &out, nil
}

// SetScenarioArgs are set_terminal_scenario's arguments.
type SetScenarioArgs struct {
	Ticker string
	Fields ScenarioFields
}

// SetScenario is set_terminal_scenario (create or update, "upsert"):
// UpsertScenario, then toResponse().
func (c *Catalog) SetScenario(ctx context.Context, userID uuid.UUID, args SetScenarioArgs) (PositionResponse, error) {
	row, err := c.svc.UpsertScenario(ctx, userID, args.Ticker, args.Fields)
	if err != nil {
		return PositionResponse{}, err
	}
	return ToResponse(row), nil
}

// CreateArgs are a full create, as POST /v1/terminal-positions takes it.
type CreateArgs = CreateRequest

// Create is POST /v1/terminal-positions without HTTP (not a Swift catalog
// action; additive). Like the route, it schedules an achievement check.
func (c *Catalog) Create(ctx context.Context, userID uuid.UUID, args CreateArgs) (PositionResponse, error) {
	row, err := c.svc.Create(ctx, userID, args)
	if err != nil {
		return PositionResponse{}, err
	}
	achievementhooks.Schedule(userID)
	return ToResponse(row), nil
}

// DeleteArgs are a delete's arguments.
type DeleteArgs struct {
	ID uuid.UUID
}

// Delete is DELETE /v1/terminal-positions/:id without HTTP (not a Swift
// catalog action; additive): 404 *httpx.Error for another user's row.
func (c *Catalog) Delete(ctx context.Context, userID uuid.UUID, args DeleteArgs) error {
	return c.svc.Delete(ctx, userID, args.ID)
}

// LookupShareFactsArgs are lookup_share_facts' arguments.
type LookupShareFactsArgs struct {
	Ticker string
}

// LookupShareFacts is lookup_share_facts, in the Swift action's order:
// ticker (422 *httpx.Error), then Pro (an *entitlements.UpgradeRequiredError;
// the Swift action mapped it to "lookup_share_facts needs Norviq Pro"), then
// the client (ErrAIUnavailable when app.terminalAIClient is nil; Swift:
// "AI lookup unavailable"), then currency and the advisor (503/422
// *httpx.Error). No AI consent check, as in the Swift catalog.
func (c *Catalog) LookupShareFacts(ctx context.Context, userID uuid.UUID, args LookupShareFactsArgs) (ShareFactsSuggestion, error) {
	symbol, err := NormalisedTicker(args.Ticker)
	if err != nil {
		return ShareFactsSuggestion{}, err
	}
	if err := c.ent.RequirePremium(ctx, entitlements.FeatureTerminalPositionAI, userID); err != nil {
		return ShareFactsSuggestion{}, err
	}
	client, ok := AIClient(c.d)
	if !ok {
		return ShareFactsSuggestion{}, ErrAIUnavailable
	}
	currency, err := c.svc.Currency(ctx, userID)
	if err != nil {
		return ShareFactsSuggestion{}, err
	}
	return Advisor{Client: client, Logger: c.d.Logger}.ShareFacts(ctx, symbol, currency)
}
