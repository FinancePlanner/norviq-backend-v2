package watchlist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// WatchlistStatus raw values.
const (
	statusActive   = "active"
	statusArchived = "archived"
)

var watchlistStatuses = []string{"active", "researching", "waiting", "ready", "archived", "exited"}

// watchlistStatus is StockPlanShared.WatchlistStatus. Its decoder is
// lenient: an unknown status decodes as active.
type watchlistStatus string

func (s *watchlistStatus) decodeSwift(raw json.RawMessage, path string) error {
	v, err := enumString(raw, path, "WatchlistStatus", watchlistStatuses)
	if err != nil {
		if jsonKind(raw) == "a string" {
			*s = statusActive
			return nil
		}
		return err
	}
	*s = watchlistStatus(v)
	return nil
}

func (s *watchlistStatus) decoded() string {
	if slices.Contains(watchlistStatuses, string(*s)) {
		return string(*s)
	}
	return statusActive
}

// itemResponse is StockPlanShared.WatchlistItemResponse.
type itemResponse struct {
	ID              string  `json:"id"`
	Symbol          string  `json:"symbol"`
	Note            *string `json:"note,omitempty"`
	Status          string  `json:"status"`
	CreatedAt       *string `json:"createdAt,omitempty"`
	UpdatedAt       *string `json:"updatedAt,omitempty"`
	LastReviewedAt  *string `json:"lastReviewedAt,omitempty"`
	NextReviewAt    *string `json:"nextReviewAt,omitempty"`
	WatchlistListID *string `json:"watchlistListId,omitempty"`
}

// makeItemResponse is makeWatchlistItemResponse: an unknown stored status
// reads as active.
func makeItemResponse(m watchlistdb.WatchlistItem) itemResponse {
	status := m.Status
	if !slices.Contains(watchlistStatuses, status) {
		status = statusActive
	}
	return itemResponse{
		ID:              uuidString(m.ID),
		Symbol:          m.Symbol,
		Note:            m.Note,
		Status:          status,
		CreatedAt:       formatISOMillis(&m.CreatedAt),
		UpdatedAt:       formatISOMillis(m.UpdatedAt),
		LastReviewedAt:  formatDateOnly(m.LastReviewedAt),
		NextReviewAt:    formatDateOnly(m.NextReviewAt),
		WatchlistListID: strPtr(uuidString(m.WatchlistListID)),
	}
}

func updateParams(m watchlistdb.WatchlistItem, now time.Time) watchlistdb.UpdateWatchlistItemParams {
	return watchlistdb.UpdateWatchlistItemParams{
		WatchlistListID: m.WatchlistListID,
		Note:            m.Note,
		Status:          m.Status,
		LastReviewedAt:  m.LastReviewedAt,
		NextReviewAt:    m.NextReviewAt,
		Now:             &now,
		ID:              m.ID,
	}
}

// itemRequest is StockPlanShared.WatchlistItemRequest.
type itemRequest struct {
	Symbol          string           `json:"symbol"`
	Note            *string          `json:"note"`
	Status          *watchlistStatus `json:"status"`
	NextReviewAt    *string          `json:"nextReviewAt"`
	WatchlistListID *string          `json:"watchlistListId"`
}

// itemUpdateRequest is StockPlanShared.WatchlistItemUpdateRequest.
type itemUpdateRequest struct {
	Note            *string          `json:"note"`
	Status          *watchlistStatus `json:"status"`
	LastReviewedAt  *string          `json:"lastReviewedAt"`
	NextReviewAt    *string          `json:"nextReviewAt"`
	WatchlistListID *string          `json:"watchlistListId"`
}

func (h *handlers) listWatchlist(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	listID, err := h.resolveListID(ctx, h.q, httpx.QueryString(c, "watchlistListId"), s.UserID, true)
	if err != nil {
		return err
	}
	items, err := h.q.ListWatchlistItems(ctx, watchlistdb.ListWatchlistItemsParams{UserID: s.UserID, WatchlistListID: *listID})
	if err != nil {
		return err
	}
	out := make([]itemResponse, len(items))
	for i, it := range items {
		out[i] = makeItemResponse(it)
	}
	return httpx.OK(c, out)
}

// upsertResult is WatchlistService.UpsertResult.
type upsertResult struct {
	item    watchlistdb.WatchlistItem
	created bool
}

// upsert is WatchlistService.upsert: an existing (user, list, symbol) is
// patched rather than duplicated, and an archived row is revived to active
// when the request names no status.
func (h *handlers) upsert(ctx context.Context, p itemRequest, userID uuid.UUID) (upsertResult, error) {
	symbol, err := normalizeSymbol(p.Symbol)
	if err != nil {
		return upsertResult{}, err
	}
	note := emptyToNil(p.Note)
	nextReviewAt, err := parseDateOnly(p.NextReviewAt, "nextReviewAt")
	if err != nil {
		return upsertResult{}, err
	}
	listID, err := h.resolveListID(ctx, h.q, p.WatchlistListID, userID, true)
	if err != nil {
		return upsertResult{}, err
	}

	existing, err := h.q.WatchlistItemBySymbol(ctx, watchlistdb.WatchlistItemBySymbolParams{
		UserID: userID, WatchlistListID: *listID, Symbol: symbol,
	})
	if err == nil {
		changed := false
		if p.Note != nil && !sameString(existing.Note, note) {
			existing.Note = note
			changed = true
		}
		if p.Status != nil {
			if st := p.Status.decoded(); existing.Status != st {
				existing.Status = st
				changed = true
			}
		} else if existing.Status == statusArchived {
			existing.Status = statusActive
			changed = true
		}
		if p.NextReviewAt != nil && !sameTime(existing.NextReviewAt, nextReviewAt) {
			existing.NextReviewAt = nextReviewAt
			changed = true
		}
		if changed {
			if existing, err = h.q.UpdateWatchlistItem(ctx, updateParams(existing, h.now())); err != nil {
				return upsertResult{}, err
			}
		}
		return upsertResult{item: existing}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return upsertResult{}, err
	}

	count, err := h.q.CountWatchlistItems(ctx, userID)
	if err != nil {
		return upsertResult{}, err
	}
	if err := h.ent.EnforceResourceLimit(ctx, entitlements.FeatureWatchlistItems, userID, int(count), 1); err != nil {
		return upsertResult{}, err
	}
	status := statusActive
	if p.Status != nil {
		status = p.Status.decoded()
	}
	item, err := h.q.InsertWatchlistItem(ctx, watchlistdb.InsertWatchlistItemParams{
		ID: uuid.New(), UserID: userID, WatchlistListID: *listID, Symbol: symbol,
		Note: note, Status: status, NextReviewAt: nextReviewAt, Now: h.now(),
	})
	if err != nil {
		return upsertResult{}, err
	}
	// try? in Swift: a failed sync does not fail the request.
	_ = h.ent.SyncResourceCount(ctx, entitlements.FeatureWatchlistItems, userID, int(count)+1)
	return upsertResult{item: item, created: true}, nil
}

func (h *handlers) createWatchlistItem(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body itemRequest
	if err := decodeBody(c, &body); err != nil {
		return err
	}
	res, err := h.upsert(c.Request().Context(), body, s.UserID)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if res.created {
		status = http.StatusCreated
	}
	return httpx.JSON(c, status, makeItemResponse(res.item))
}

func (h *handlers) updateWatchlistItem(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "watchlistId", "Invalid watchlist ID")
	if err != nil {
		return err
	}
	var p itemUpdateRequest
	if err := decodeBody(c, &p); err != nil {
		return err
	}
	ctx := c.Request().Context()
	item, err := h.q.OwnedWatchlistItem(ctx, watchlistdb.OwnedWatchlistItemParams{ID: id, UserID: s.UserID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("Watchlist item not found.")
		}
		return err
	}
	target, err := h.resolveListID(ctx, h.q, p.WatchlistListID, s.UserID, false)
	if err != nil {
		return err
	}
	targetID := item.WatchlistListID
	if target != nil {
		targetID = *target
	}

	if targetID != item.WatchlistListID {
		dup, err := h.q.DuplicateWatchlistItem(ctx, watchlistdb.DuplicateWatchlistItemParams{
			UserID: s.UserID, WatchlistListID: targetID, Symbol: item.Symbol, ID: item.ID,
		})
		switch {
		case err == nil:
			// Fluent saved only when a field was assigned.
			assigned := false
			if p.Note != nil {
				dup.Note = emptyToNil(p.Note)
				assigned = true
			}
			if p.Status != nil {
				dup.Status = p.Status.decoded()
				assigned = true
			}
			if p.LastReviewedAt != nil {
				if dup.LastReviewedAt, err = parseDateOnly(p.LastReviewedAt, "lastReviewedAt"); err != nil {
					return err
				}
				assigned = true
			}
			if p.NextReviewAt != nil {
				if dup.NextReviewAt, err = parseDateOnly(p.NextReviewAt, "nextReviewAt"); err != nil {
					return err
				}
				assigned = true
			}
			if assigned {
				if dup, err = h.q.UpdateWatchlistItem(ctx, updateParams(dup, h.now())); err != nil {
					return err
				}
			}
			if err := h.q.DeleteWatchlistItem(ctx, item.ID); err != nil {
				return err
			}
			return httpx.OK(c, makeItemResponse(dup))
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
	}

	changed := false
	if item.WatchlistListID != targetID {
		item.WatchlistListID = targetID
		changed = true
	}
	if p.Note != nil {
		if n := emptyToNil(p.Note); !sameString(item.Note, n) {
			item.Note = n
			changed = true
		}
	}
	if p.Status != nil {
		if st := p.Status.decoded(); item.Status != st {
			item.Status = st
			changed = true
		}
	}
	if p.LastReviewedAt != nil {
		v, err := parseDateOnly(p.LastReviewedAt, "lastReviewedAt")
		if err != nil {
			return err
		}
		if !sameTime(item.LastReviewedAt, v) {
			item.LastReviewedAt = v
			changed = true
		}
	}
	if p.NextReviewAt != nil {
		v, err := parseDateOnly(p.NextReviewAt, "nextReviewAt")
		if err != nil {
			return err
		}
		if !sameTime(item.NextReviewAt, v) {
			item.NextReviewAt = v
			changed = true
		}
	}
	if changed {
		if item, err = h.q.UpdateWatchlistItem(ctx, updateParams(item, h.now())); err != nil {
			return err
		}
	}
	return httpx.OK(c, makeItemResponse(item))
}

func (h *handlers) deleteWatchlistItem(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "watchlistId", "Invalid watchlist ID")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	item, err := h.q.OwnedWatchlistItem(ctx, watchlistdb.OwnedWatchlistItemParams{ID: id, UserID: s.UserID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("Watchlist item not found.")
		}
		return err
	}
	if err := h.q.DeleteWatchlistItem(ctx, item.ID); err != nil {
		return err
	}
	count, err := h.q.CountWatchlistItems(ctx, s.UserID)
	if err != nil {
		return err
	}
	_ = h.ent.SyncResourceCount(ctx, entitlements.FeatureWatchlistItems, s.UserID, int(count))
	return c.NoContent(http.StatusNoContent)
}
