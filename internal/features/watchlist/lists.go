package watchlist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// listResponse is StockPlanShared.WatchlistListResponse.
type listResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	IsDefault bool    `json:"isDefault"`
	CreatedAt *string `json:"createdAt,omitempty"`
	UpdatedAt *string `json:"updatedAt,omitempty"`
}

func makeListResponse(l watchlistdb.WatchlistList) listResponse {
	return listResponse{
		ID:        uuidString(l.ID),
		Name:      l.Name,
		IsDefault: l.IsDefault,
		CreatedAt: formatISOMillis(&l.CreatedAt),
		UpdatedAt: formatISOMillis(l.UpdatedAt),
	}
}

// ensureDefaultList is ensureDefaultWatchlistListId: the user's default
// watchlist, promoting or creating one when there is none. A watchlist a
// pilot follow manages is never the default (one flagged default is
// demoted).
func (h *handlers) ensureDefaultList(ctx context.Context, q *watchlistdb.Queries, userID uuid.UUID) (uuid.UUID, error) {
	followed, err := q.FollowedWatchlistListIDs(ctx, userID)
	if err != nil {
		return uuid.UUID{}, err
	}
	existing, err := q.DefaultWatchlistList(ctx, userID)
	switch {
	case err == nil:
		if !slices.Contains(followed, existing.ID) {
			return existing.ID, nil
		}
		if err := q.SetWatchlistListDefault(ctx, watchlistdb.SetWatchlistListDefaultParams{
			IsDefault: false, Now: new(h.now()), ID: existing.ID,
		}); err != nil {
			return uuid.UUID{}, err
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.UUID{}, err
	}

	fallback, err := q.OldestWatchlistListExcluding(ctx, watchlistdb.OldestWatchlistListExcludingParams{
		UserID: userID, Excluded: followed,
	})
	switch {
	case err == nil:
		if err := q.SetWatchlistListDefault(ctx, watchlistdb.SetWatchlistListDefaultParams{
			IsDefault: true, Now: new(h.now()), ID: fallback.ID,
		}); err != nil {
			return uuid.UUID{}, err
		}
		return fallback.ID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.UUID{}, err
	}

	names, err := q.WatchlistListNames(ctx, userID)
	if err != nil {
		return uuid.UUID{}, err
	}
	name := "Main Watchlist"
	for n := 2; slices.Contains(names, name); n++ {
		name = fmt.Sprintf("Main Watchlist %d", n)
	}
	created, err := q.InsertWatchlistList(ctx, watchlistdb.InsertWatchlistListParams{
		ID: uuid.New(), UserID: userID, Name: name, IsDefault: true, Now: h.now(),
	})
	if err != nil {
		return uuid.UUID{}, err
	}
	return created.ID, nil
}

// resolveListID is resolveWatchlistListId. It returns nil only when the
// request names no list and defaultWhenMissing is false.
func (h *handlers) resolveListID(ctx context.Context, q *watchlistdb.Queries, requested *string, userID uuid.UUID, defaultWhenMissing bool) (*uuid.UUID, error) {
	raw := ""
	if requested != nil {
		raw = strings.TrimSpace(*requested)
	}
	if raw == "" {
		if !defaultWhenMissing {
			return nil, nil
		}
		id, err := h.ensureDefaultList(ctx, q, userID)
		if err != nil {
			return nil, err
		}
		return &id, nil
	}
	id, ok := parseSwiftUUID(raw)
	if !ok {
		return nil, httpx.BadRequest("Invalid watchlistListId.")
	}
	if _, err := q.OwnedWatchlistList(ctx, watchlistdb.OwnedWatchlistListParams{ID: id, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Watchlist list not found.")
		}
		return nil, err
	}
	return &id, nil
}

// normalizeListName is ListSupport.normalizeListName.
func normalizeListName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", httpx.BadRequest("name is required.")
	}
	if utf8.RuneCountInString(name) > 80 {
		return "", httpx.BadRequest("name must be at most 80 characters.")
	}
	return name, nil
}

// uuidParam is requireUUIDParameter.
func uuidParam(c *echo.Context, name, reason string) (uuid.UUID, error) {
	id, ok := parseSwiftUUID(c.Param(name))
	if !ok {
		return uuid.UUID{}, httpx.BadRequest(reason)
	}
	return id, nil
}

type listRequest struct {
	Name string `json:"name"`
}

func (h *handlers) listWatchlistLists(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	n, err := h.q.CountWatchlistLists(ctx, s.UserID)
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := h.ensureDefaultList(ctx, h.q, s.UserID); err != nil {
			return err
		}
	}
	lists, err := h.q.ListWatchlistLists(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := make([]listResponse, len(lists))
	for i, l := range lists {
		out[i] = makeListResponse(l)
	}
	return httpx.OK(c, out)
}

func (h *handlers) createWatchlistList(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body listRequest
	if err := decodeBody(c, &body); err != nil {
		return err
	}
	name, err := normalizeListName(body.Name)
	if err != nil {
		return err
	}
	list, err := h.q.InsertWatchlistList(c.Request().Context(), watchlistdb.InsertWatchlistListParams{
		ID: uuid.New(), UserID: s.UserID, Name: name, IsDefault: false, Now: h.now(),
	})
	if err != nil {
		return listNameConflict(err)
	}
	return httpx.Created(c, makeListResponse(list))
}

func (h *handlers) updateWatchlistList(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := uuidParam(c, "watchlistListId", "Invalid watchlist list ID")
	if err != nil {
		return err
	}
	var body listRequest
	if err := decodeBody(c, &body); err != nil {
		return err
	}
	name, err := normalizeListName(body.Name)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if _, err := h.q.OwnedWatchlistList(ctx, watchlistdb.OwnedWatchlistListParams{ID: id, UserID: s.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("Watchlist list not found.")
		}
		return err
	}
	list, err := h.q.RenameWatchlistList(ctx, watchlistdb.RenameWatchlistListParams{Name: name, Now: new(h.now()), ID: id})
	if err != nil {
		return listNameConflict(err)
	}
	return httpx.OK(c, makeListResponse(list))
}

// deleteWatchlistList moves the list's items to the default list (merging
// into an existing item of the same symbol there), then deletes it, in one
// transaction.
func (h *handlers) deleteWatchlistList(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	listID, err := uuidParam(c, "watchlistListId", "Invalid watchlist list ID")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	err = pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		q := h.q.WithTx(tx)
		list, err := q.OwnedWatchlistList(ctx, watchlistdb.OwnedWatchlistListParams{ID: listID, UserID: s.UserID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("Watchlist list not found.")
			}
			return err
		}
		if list.IsDefault {
			return httpx.BadRequest("Default watchlist list cannot be deleted.")
		}
		defaultID, err := h.ensureDefaultList(ctx, q, s.UserID)
		if err != nil {
			return err
		}
		items, err := q.WatchlistItemsInList(ctx, watchlistdb.WatchlistItemsInListParams{UserID: s.UserID, WatchlistListID: listID})
		if err != nil {
			return err
		}
		for i := range items {
			item := &items[i]
			dup, err := q.DuplicateWatchlistItem(ctx, watchlistdb.DuplicateWatchlistItemParams{
				UserID: s.UserID, WatchlistListID: defaultID, Symbol: item.Symbol, ID: item.ID,
			})
			switch {
			case err == nil:
				if dup.Note == nil {
					dup.Note = item.Note
				}
				if dup.Status == statusArchived && item.Status != statusArchived {
					dup.Status = item.Status
				}
				if dup.LastReviewedAt == nil {
					dup.LastReviewedAt = item.LastReviewedAt
				}
				if dup.NextReviewAt == nil {
					dup.NextReviewAt = item.NextReviewAt
				}
				if _, err := q.UpdateWatchlistItem(ctx, updateParams(dup, h.now())); err != nil {
					return err
				}
				if err := q.DeleteWatchlistItem(ctx, item.ID); err != nil {
					return err
				}
				continue
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
			item.WatchlistListID = defaultID
			if _, err := q.UpdateWatchlistItem(ctx, updateParams(*item, h.now())); err != nil {
				return err
			}
		}
		return q.DeleteWatchlistList(ctx, listID)
	})
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
