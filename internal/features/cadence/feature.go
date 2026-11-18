package cadence

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
	"norviq-api/internal/store/cadencedb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

const (
	scopeHoldings  = "holdings"
	scopeWatchlist = "watchlist"

	defaultYears = 5
	minYears     = 1
	maxYears     = 15

	maxSymbolLength = 32
)

// PremiumGate reports whether a user's plan includes a billing feature: nil
// when it does, an *entitlements.UpgradeRequiredError when it does not.
// entitlements.NamedGate satisfies it.
type PremiumGate interface {
	RequirePremium(ctx context.Context, feature string, userID uuid.UUID) error
}

// SurplusSource reports the user's investable surplus for the month.
// *budgeting.Engine satisfies it.
type SurplusSource interface {
	MonthSurplus(ctx context.Context, userID uuid.UUID) (budgeting.MonthSurplus, error)
}

// PlanMarket is the part of the market data service the plan reads.
// *marketdata.Service satisfies it.
type PlanMarket interface {
	Quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error)
	Fx(ctx context.Context, pair string) (marketdata.FxRateResponse, error)
}

// Feature mounts the cadence read routes. They only read cadence_zones; the
// zones job is the only writer and nothing here calls a market data provider.
type Feature struct {
	// Premium builds the billing gate that decides which timeframes of the
	// detail response are locked. Without one every user counts as free.
	Premium func(d *server.Deps) PremiumGate
	// Now defaults to time.Now (tests).
	Now func() time.Time
	// Surplus and Market build the plan's inputs; nil builds them from d
	// (budgeting.NewEngine, market.SharedService).
	Surplus func(d *server.Deps) (SurplusSource, error)
	Market  func(d *server.Deps) (PlanMarket, error)
}

// Name implements server.Feature.
func (Feature) Name() string { return "cadence" }

type handlers struct {
	d       *server.Deps
	q       *cadencedb.Queries
	premium PremiumGate
	now     func() time.Time
	surplus SurplusSource
	market  PlanMarket
}

// Register implements server.Feature. It mounts nothing unless
// CADENCE_ENABLED is true.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s, err := config.Load[ZonesJobSettings]()
	if err != nil {
		return err
	}
	if !config.Bool(s.Enabled, false) {
		return nil
	}
	h := &handlers{d: d, q: cadencedb.New(d.DB), now: f.Now}
	if h.now == nil {
		h.now = time.Now
	}
	if f.Premium != nil {
		h.premium = f.Premium(d)
	}
	if h.surplus, err = f.surplusSource(d); err != nil {
		return err
	}
	if h.market, err = f.marketSource(d); err != nil {
		return err
	}
	g := r.V1.Group("/cadence",
		d.Limiter.Limit(100, 60*time.Second, "ratelimit:cadence"),
		d.Auth.Scoped(), auth.Guard(),
		// After auth the limiter keys per user (market does the same).
		d.Limiter.Limit(120, 60*time.Second, "ratelimit:cadence"),
	)
	g.GET("/status", h.status)
	g.GET("/zones", h.list)
	g.GET("/zones/:symbol", h.detail, auth.RequireScope(auth.ScopeMarketRead))
	g.GET("/plan", h.plan, auth.RequireScope(auth.ScopePlanningRead), auth.RequireScope(auth.ScopeBudgetRead), auth.RequireScope(auth.ScopePortfolioRead))
	return nil
}

func (f Feature) surplusSource(d *server.Deps) (SurplusSource, error) {
	if f.Surplus != nil {
		return f.Surplus(d)
	}
	return budgeting.NewEngine(d)
}

func (f Feature) marketSource(d *server.Deps) (PlanMarket, error) {
	if f.Market != nil {
		return f.Market(d)
	}
	return market.SharedService(d)
}

func (h *handlers) status(c *echo.Context) error {
	return httpx.OK(c, map[string]bool{"enabled": true})
}

func (h *handlers) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	scope := scopeHoldings
	if _, ok := c.QueryParams()["scope"]; ok {
		scope = c.QueryParam("scope")
	}
	var symbols []string
	switch scope {
	case scopeHoldings:
		if err := auth.CheckScope(c, auth.ScopePortfolioRead); err != nil {
			return err
		}
		symbols, err = h.q.UserHoldingSymbols(ctx, s.UserID)
	case scopeWatchlist:
		if err := auth.CheckScope(c, auth.ScopeWatchlistRead); err != nil {
			return err
		}
		symbols, err = h.q.UserWatchlistSymbols(ctx, s.UserID)
	default:
		return httpx.BadRequest("`scope` must be holdings or watchlist.")
	}
	if err != nil {
		return err
	}
	items := make([]ListItemDTO, 0, len(symbols))
	if len(symbols) == 0 {
		return httpx.OK(c, items)
	}
	rows, err := h.q.LatestZonesForSymbols(ctx, cadencedb.LatestZonesForSymbolsParams{Timeframe: string(TimeframeMonth), Symbols: symbols})
	if err != nil {
		return err
	}
	bySymbol := map[string][]cadencedb.CadenceZone{}
	for i := range rows {
		bySymbol[rows[i].Symbol] = append(bySymbol[rows[i].Symbol], rows[i])
	}
	highs, err := h.bullHighs(c, s.UserID, symbols)
	if err != nil {
		return err
	}
	for _, sym := range symbols {
		item := ListItemDTO{Symbol: sym, Status: "pending"}
		if confirmed, forming := newestPair(bySymbol[sym]); confirmed != nil || forming != nil {
			item.Status = "ready"
			if confirmed != nil {
				item.Confirmed = zoneDTO(*confirmed, highs[sym])
			}
			if forming != nil {
				item.Forming = zoneDTO(*forming, highs[sym])
			}
		}
		items = append(items, item)
	}
	return httpx.OK(c, items)
}

func (h *handlers) detail(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	symbol, err := pathSymbol(c)
	if err != nil {
		return err
	}
	years := defaultYears
	if _, ok := c.QueryParams()["years"]; ok {
		n, err := httpx.QueryInt(c, "years")
		if err != nil {
			return err
		}
		if n == nil || *n < minYears || *n > maxYears {
			return httpx.BadRequest("`years` must be between 1 and 15.")
		}
		years = *n
	}
	ctx := c.Request().Context()
	pro, err := h.isPro(ctx, s.UserID)
	if err != nil {
		return err
	}
	highs, err := h.bullHighs(c, s.UserID, []string{symbol})
	if err != nil {
		return err
	}
	from := historyFrom(h.now(), years)
	out := DetailDTO{Symbol: symbol, Status: "pending", Timeframes: map[string]any{}, Rules: rulesDTO(DefaultRules)}
	for _, tf := range AllTimeframes() {
		if tf != TimeframeMonth && !pro {
			out.Timeframes[string(tf)] = LockedTimeframeDTO{Locked: true}
			continue
		}
		latest, err := h.q.LatestZonesForSymbols(ctx, cadencedb.LatestZonesForSymbolsParams{Timeframe: string(tf), Symbols: []string{symbol}})
		if err != nil {
			return err
		}
		hist, err := h.q.ZonesForSymbol(ctx, cadencedb.ZonesForSymbolParams{Symbol: symbol, Timeframe: string(tf), FromDate: from})
		if err != nil {
			return err
		}
		t := TimeframeDTO{History: historyPoints(hist)}
		confirmed, forming := newestPair(latest)
		if confirmed != nil {
			t.Confirmed = zoneDTO(*confirmed, highs[symbol])
		}
		if forming != nil {
			t.Forming = zoneDTO(*forming, highs[symbol])
		}
		if len(latest) > 0 {
			out.Status = "ready"
		}
		out.Timeframes[string(tf)] = t
	}
	return httpx.OK(c, out)
}

// isPro asks the billing gate. Its upgrade error means the plan is free; any
// other error is a failure, not a verdict.
func (h *handlers) isPro(ctx context.Context, userID uuid.UUID) (bool, error) {
	if h.premium == nil {
		return false, nil
	}
	err := h.premium.RequirePremium(ctx, string(entitlements.FeatureCadence), userID)
	if err == nil {
		return true, nil
	}
	var upgrade *entitlements.UpgradeRequiredError
	if errors.As(err, &upgrade) {
		return false, nil
	}
	return false, err
}

// bullHighs maps symbol to the user's bull range high, for symbols with a
// valuation.
func (h *handlers) bullHighs(c *echo.Context, userID uuid.UUID, symbols []string) (map[string]*float64, error) {
	rows, err := h.q.UserBullHighs(c.Request().Context(), cadencedb.UserBullHighsParams{UserID: userID, Symbols: symbols})
	if err != nil {
		return nil, err
	}
	out := make(map[string]*float64, len(rows))
	for _, r := range rows {
		v := r.BullHigh
		out[r.Symbol] = &v
	}
	return out, nil
}

// pathSymbol reads the :symbol parameter, upper-cased. It applies the rule
// the market data package uses for provider symbols
// (marketdata.normalizeProviderSymbol, unexported): trimmed, upper-cased, not
// empty.
func pathSymbol(c *echo.Context) (string, error) {
	raw := c.Param("symbol")
	if v, err := url.PathUnescape(raw); err == nil {
		raw = v
	}
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", httpx.BadRequest("Symbol is required.")
	}
	if len(s) > maxSymbolLength {
		return "", httpx.BadRequest("Symbol is too long.")
	}
	return s, nil
}
