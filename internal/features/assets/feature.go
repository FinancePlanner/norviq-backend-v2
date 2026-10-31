// Package assets ports AssetsController (GET /v1/assets/search): market
// symbol search merged with the caller's own holdings, owned symbols first.
package assets

import (
	"context"
	"sort"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/store/assetsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// SearchResult is SearchResultResponse.
type SearchResult struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	Currency string `json:"currency"`
	Conid    string `json:"conid"`
}

// SymbolSearch is `marketDataService.search(query:on:)`, owned by the market
// data port. Its error is returned unchanged when nothing else matched.
type SymbolSearch interface {
	Search(ctx context.Context, query string) ([]SearchResult, error)
}

// Feature mounts /v1/assets/search once Search is wired; until then the
// route is not registered (404).
type Feature struct {
	Search func(d *server.Deps) (SymbolSearch, error)
}

func (Feature) Name() string { return "assets" }

func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	if f.Search == nil {
		return nil
	}
	searcher, err := f.Search(d)
	if err != nil {
		return err
	}
	h := &handler{d: d, searcher: searcher}
	r.V1.GET("/assets/search", h.search, d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeMarketRead))
	return nil
}

type handler struct {
	d        *server.Deps
	searcher SymbolSearch
}

type ownedStock struct {
	id     uuid.UUID
	symbol string
	notes  *string
}

func sortKey(symbol, query string) int {
	lower := strings.ToLower(symbol)
	switch {
	case lower == query:
		return 0
	case strings.HasPrefix(lower, query):
		return 1
	case strings.Contains(lower, query):
		return 2
	}
	return 3
}

func (h *handler) search(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	q := httpx.QueryString(c, "q")
	if q == nil || strings.TrimSpace(*q) == "" {
		return httpx.BadRequest("Missing query parameter `q`.")
	}
	raw := strings.TrimSpace(*q)
	limit := min(max(httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 20), 1), 50)
	query := strings.ToLower(raw)
	ctx := c.Request().Context()

	rows, err := assetsdb.New(h.d.DB).OwnedStocksForSearch(ctx, s.UserID)
	if err != nil {
		return err
	}
	ownedBySymbol := map[string]ownedStock{}
	owned := make([]ownedStock, 0, len(rows))
	for _, r := range rows {
		o := ownedStock{id: r.ID, symbol: r.Symbol, notes: r.Notes}
		owned = append(owned, o)
		ownedBySymbol[strings.ToUpper(o.symbol)] = o
	}

	market, searchErr := h.searcher.Search(ctx, raw)
	if searchErr != nil {
		market = nil
		h.d.Logger.Warn("assets.search market fallback", "query", raw, "error", searchErr)
	}
	marketBySymbol := map[string]SearchResult{}
	for _, m := range market {
		marketBySymbol[strings.ToUpper(m.Symbol)] = m
	}

	matched := map[string]struct{}{}
	for sym := range marketBySymbol {
		if _, ok := ownedBySymbol[sym]; ok {
			matched[sym] = struct{}{}
		}
	}
	for _, o := range owned {
		if strings.Contains(strings.ToLower(o.symbol), query) || (o.notes != nil && strings.Contains(strings.ToLower(*o.notes), query)) {
			matched[strings.ToUpper(o.symbol)] = struct{}{}
		}
	}
	ownedFirst := make([]string, 0, len(matched))
	for sym := range matched {
		ownedFirst = append(ownedFirst, sym)
	}
	sort.Slice(ownedFirst, func(i, j int) bool {
		ki, kj := sortKey(ownedFirst[i], query), sortKey(ownedFirst[j], query)
		if ki != kj {
			return ki < kj
		}
		return ownedFirst[i] < ownedFirst[j]
	})

	merged := make([]SearchResult, 0, limit)
	seen := map[string]struct{}{}
	for _, sym := range ownedFirst {
		if len(merged) >= limit {
			break
		}
		if m, ok := marketBySymbol[sym]; ok {
			merged = append(merged, m)
		} else if o, ok := ownedBySymbol[sym]; ok {
			merged = append(merged, SearchResult{
				Symbol: sym, Name: sym + " - owned asset", Exchange: "PORTFOLIO", Currency: "USD",
				Conid: "owned-" + apijson.FormatUUID(o.id),
			})
		}
		seen[sym] = struct{}{}
	}
	for _, m := range market {
		if len(merged) >= limit {
			break
		}
		sym := strings.ToUpper(m.Symbol)
		if _, dup := seen[sym]; dup {
			continue
		}
		merged = append(merged, m)
		seen[sym] = struct{}{}
	}
	if len(merged) == 0 && searchErr != nil {
		return searchErr
	}
	return httpx.OK(c, merged)
}
