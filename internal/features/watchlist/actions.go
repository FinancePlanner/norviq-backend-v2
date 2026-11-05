package watchlist

import (
	"context"
	"errors"

	"norviq-api/internal/features/badges"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/badgesdb"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Actions exposes WatchlistService to the AI action catalog
// (ActionCatalog+Portfolio.swift), which called the service directly.
type Actions struct{ h *handlers }

// NewActions builds the service with Register's defaults.
func NewActions(d *server.Deps) *Actions {
	return &Actions{h: &handlers{
		d: d, q: watchlistdb.New(d.DB), ent: entitlements.NewFromEnv(d.DB),
		badges: &badges.Service{Q: badgesdb.New(d.DB), Logger: d.Logger.Warn},
	}}
}

// ItemResponse is WatchlistItemResponse, as WatchlistService.response(from:)
// builds it.
type ItemResponse = itemResponse

// List is WatchlistService.list: all of the user's items, by symbol.
func (a *Actions) List(ctx context.Context, userID uuid.UUID) ([]ItemResponse, error) {
	rows, err := a.h.q.ListAllWatchlistItemsBySymbol(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ItemResponse, len(rows))
	for i := range rows {
		out[i] = makeItemResponse(rows[i])
	}
	return out, nil
}

// Upsert is WatchlistService.upsert with WatchlistItemRequest(symbol:note:
// status:). status must already be a valid WatchlistStatus raw value.
func (a *Actions) Upsert(ctx context.Context, userID uuid.UUID, symbol string, note, status *string) (ItemResponse, error) {
	p := itemRequest{Symbol: symbol, Note: note}
	if status != nil {
		st := watchlistStatus(*status)
		p.Status = &st
	}
	res, err := a.h.upsert(ctx, p, userID)
	if err != nil {
		return ItemResponse{}, err
	}
	return makeItemResponse(res.item), nil
}

// Delete is WatchlistService.delete: no resource-count sync, and its own
// not-found reason.
func (a *Actions) Delete(ctx context.Context, id, userID uuid.UUID) error {
	item, err := a.h.q.OwnedWatchlistItem(ctx, watchlistdb.OwnedWatchlistItemParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("Watchlist entry not found.")
	}
	if err != nil {
		return err
	}
	return a.h.q.DeleteWatchlistItem(ctx, item.ID)
}

// TargetResponse is a price target as the routes render it.
type TargetResponse = targetResponse

// ListTargets lists the user's price targets. It has no Pro gate, unlike the
// route: the in-app assistant answers free users from this data by decision.
func (a *Actions) ListTargets(ctx context.Context, userID uuid.UUID, limit int) ([]TargetResponse, error) {
	targets, err := a.h.q.ListTargets(ctx, watchlistdb.ListTargetsParams{UserID: userID, Lim: int32(limit)}) // #nosec G115 -- callers pass a small constant
	if err != nil {
		return nil, err
	}
	out := make([]TargetResponse, len(targets))
	for i := range targets {
		out[i] = makeTargetResponse(targets[i])
	}
	return out, nil
}
