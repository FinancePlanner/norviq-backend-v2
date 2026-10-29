// Package sharing ports the public route of PortfolioShareController:
// GET /v1/public/portfolio-shares/:slug, the percent-only view of a shared
// portfolio that the web app renders at /p/{slug}. The owner-side
// share-link routes are in the portfolio feature; SharingController's HTML
// pages are in the stocks feature.
package sharing

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/portfoliodb"
	"norviq-api/internal/store/sharingdb"

	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/public/portfolio-shares/:slug.
type Feature struct {
	// Quotes overrides the environment-built market data service (tests).
	Quotes portfolio.QuoteSource
}

// Name implements server.Feature.
func (Feature) Name() string { return "sharing" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	quotes := f.Quotes
	if quotes == nil {
		svc, err := market.SharedService(d)
		if err != nil {
			return err
		}
		quotes = svc
	}
	h := &handler{
		d:         d,
		q:         sharingdb.New(d.DB),
		filters:   portfolio.FilterResolver{Access: portfolios.NewAccess(d.DB)},
		valuation: portfolio.ValuationService{Quotes: quotes, Logger: d.Logger},
	}
	// Unauthenticated, so the limiter keys by peer address: a pod-wide
	// ceiling on quote fan-out.
	r.V1.GET("/public/portfolio-shares/:slug", h.publicShare,
		d.Limiter.Limit(600, 60*time.Second, "ratelimit:public-share"))
	return nil
}

type handler struct {
	d         *server.Deps
	q         *sharingdb.Queries
	filters   portfolio.FilterResolver
	valuation portfolio.ValuationService
}

// holding is PortfolioShareHolding.
type holding struct {
	Symbol               string   `json:"symbol"`
	WeightPercent        float64  `json:"weightPercent"`
	UnrealizedPnlPercent *float64 `json:"unrealizedPnlPercent,omitempty"`
	DayChangePercent     *float64 `json:"dayChangePercent,omitempty"`
}

// totals is PortfolioShareTotals.
type totals struct {
	UnrealizedPnlPercent *float64 `json:"unrealizedPnlPercent,omitempty"`
	DayChangePercent     *float64 `json:"dayChangePercent,omitempty"`
	YTDPercent           *float64 `json:"ytdPercent,omitempty"`
}

// response is PublicPortfolioShareResponse.
type response struct {
	AsOf               string    `json:"asOf"`
	Totals             totals    `json:"totals"`
	Holdings           []holding `json:"holdings"`
	OtherWeightPercent *float64  `json:"otherWeightPercent,omitempty"`
}

// GET /v1/public/portfolio-shares/:slug
func (h *handler) publicShare(c *echo.Context) error {
	ctx := c.Request().Context()
	slug := c.Param("slug")
	links, err := h.q.ActiveShareLinkBySlug(ctx, slug)
	if err != nil {
		return err
	}
	if slug == "" || len(links) == 0 {
		return httpx.NotFound("")
	}
	link := links[0]

	// Re-resolved on every read with the owner check, so a portfolio that
	// was deleted or changed hands stops being visible through an old link.
	var requested *string
	if link.PortfolioListID != nil {
		s := apijson.FormatUUID(*link.PortfolioListID)
		requested = &s
	}
	filter, err := h.filters.Resolve(ctx, h.d.DB, requested, link.UserID, true)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusForbidden) {
			return httpx.NotFound("")
		}
		return err
	}
	valuation, err := filter.LoadValuation(ctx, h.d.DB, h.valuation)
	if err != nil {
		return err
	}
	snapshots, err := portfoliodb.New(h.d.DB).SnapshotsForUserLists(ctx, portfoliodb.SnapshotsForUserListsParams{
		UserID: filter.DataOwnerUserID, ListIds: filter.PortfolioIDs,
	})
	if err != nil {
		return err
	}
	days := portfolio.PerformanceDays(snapshots, filter.PortfolioIDs)
	changes := portfolio.PerformanceChanges(days, valuation.AsOf)
	dto := Build(valuation, changes, portfolios.FormatDateOnly(valuation.AsOf))
	c.Response().Header().Set("Cache-Control", "public, max-age=60")
	return httpx.OK(c, dto)
}

// maxHoldings is PublicPortfolioShareBuilder.maxHoldings.
const maxHoldings = 12

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func round2Ptr(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := round2(*p)
	return &v
}

// Build is PublicPortfolioShareBuilder.build: only ratios leave it. Every
// input amount is divided by the portfolio total before it is emitted.
func Build(v portfolio.Valuation, changes *portfolio.PortfolioChanges, asOf string) response {
	t := totals{
		UnrealizedPnlPercent: round2Ptr(v.UnrealizedPnlPercent),
		DayChangePercent:     round2Ptr(v.DayChangePercent),
	}
	// PortfolioChange.percent is a fraction; every other figure here is
	// already in percentage points.
	if changes != nil && changes.YTD != nil {
		y := round2(changes.YTD.Percent * 100)
		t.YTDPercent = &y
	}
	if v.TotalValue <= 0 {
		return response{AsOf: asOf, Totals: t, Holdings: []holding{}}
	}
	stocks := make([]holding, len(v.Holdings))
	for i, hv := range v.Holdings {
		stocks[i] = holding{
			Symbol:               hv.Symbol,
			WeightPercent:        hv.MarketValue / v.TotalValue * 100,
			UnrealizedPnlPercent: round2Ptr(hv.UnrealizedPnlPercent),
			DayChangePercent:     round2Ptr(hv.DayChangePercent),
		}
	}
	sort.SliceStable(stocks, func(a, b int) bool { return stocks[a].WeightPercent > stocks[b].WeightPercent })

	// Up to maxHoldings stocks are listed before anything folds into Other;
	// cash is its own row after them and never takes a stock slot.
	shown := make([]holding, 0, min(len(stocks), maxHoldings)+1)
	for i, s := range stocks {
		if i >= maxHoldings {
			break
		}
		s.WeightPercent = round1(s.WeightPercent)
		shown = append(shown, s)
	}
	if v.CashBalance > 0 {
		shown = append(shown, holding{Symbol: "CASH", WeightPercent: round1(v.CashBalance / v.TotalValue * 100)})
	}
	rest := 0.0
	for i := maxHoldings; i < len(stocks); i++ {
		rest += stocks[i].WeightPercent
	}
	out := response{AsOf: asOf, Totals: t, Holdings: shown}
	if rest > 0 {
		r := round1(rest)
		out.OtherWeightPercent = &r
	}
	return out
}
