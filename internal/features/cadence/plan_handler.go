package cadence

import (
	"context"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/autobuys"
	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/autobuysdb"
	"norviq-api/internal/store/cadencedb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// plan serves this month's contribution plan. Quote and FX failures degrade
// the affected lines (they are logged and noted); database failures fail the
// request.
func (h *handlers) plan(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.requirePro(ctx, s.UserID); err != nil {
		return err
	}
	in := PlanInput{Now: h.now()}
	if in.Currency, err = autobuys.PlanCurrency(ctx, autobuysdb.New(h.d.DB), s.UserID); err != nil {
		return err
	}
	baselines, other, err := h.baselines(ctx, s.UserID)
	if err != nil {
		return err
	}
	in.OtherRecurringMonthly = rebalancing.RoundedMoney(other)
	held, err := h.q.UserHoldingSymbols(ctx, s.UserID)
	if err != nil {
		return err
	}
	if in.Symbols, err = h.symbolInputs(c, s.UserID, held, baselines); err != nil {
		return err
	}
	if in.Surplus, in.SurplusNote, err = h.surplusIn(ctx, s.UserID, in.Currency); err != nil {
		return err
	}
	h.attachQuotes(ctx, in.Currency, in.Symbols)
	sizing, err := h.q.UserTradeSizing(ctx, s.UserID)
	if err != nil {
		return err
	}
	if len(sizing) > 0 {
		in.QuantityIncrement, in.MinimumTrade = sizing[0].QuantityIncrement, sizing[0].MinimumTradeAmount
	}
	return httpx.OK(c, BuildPlan(in))
}

// requirePro returns the gate's error unchanged. Without a gate nobody is Pro.
func (h *handlers) requirePro(ctx context.Context, userID uuid.UUID) error {
	if h.premium == nil {
		return entitlements.UpgradeRequired(entitlements.FeatureCadence, "free")
	}
	return h.premium.RequirePremium(ctx, string(entitlements.FeatureCadence), userID)
}

// baselines sums the active autobuys' monthly equivalents per upper-cased
// ticker, in the autobuy's own amount currency (the plan currency). Rows
// without a ticker are summed apart.
func (h *handlers) baselines(ctx context.Context, userID uuid.UUID) (map[string]float64, float64, error) {
	rows, err := autobuysdb.New(h.d.DB).ListAutobuys(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	out := map[string]float64{}
	var other float64
	for i := range rows {
		r := &rows[i]
		if !r.Active {
			continue
		}
		m := autobuys.MonthlyEquivalent(r.Amount, r.Cadence, r.Percent)
		if m == nil {
			continue
		}
		ticker := ""
		if r.Ticker != nil {
			ticker = strings.ToUpper(strings.TrimSpace(*r.Ticker))
		}
		if ticker == "" {
			other += *m
			continue
		}
		out[ticker] += *m
	}
	return out, other, nil
}

// symbolInputs is one entry per holding or autobuy ticker, with its baseline
// and newest confirmed zone (after both guards).
func (h *handlers) symbolInputs(c *echo.Context, userID uuid.UUID, held []string, baselines map[string]float64) ([]PlanSymbolInput, error) {
	byTicker := make(map[string]*PlanSymbolInput, len(held)+len(baselines))
	var symbols []string
	add := func(sym string) *PlanSymbolInput {
		if p, ok := byTicker[sym]; ok {
			return p
		}
		p := &PlanSymbolInput{Symbol: sym}
		byTicker[sym] = p
		symbols = append(symbols, sym)
		return p
	}
	for _, sym := range held {
		add(sym).Held = true
	}
	for sym, v := range baselines {
		b := rebalancing.RoundedMoney(v)
		add(sym).Baseline = &b
	}
	if len(symbols) == 0 {
		return nil, nil
	}
	ctx := c.Request().Context()
	rows, err := h.q.LatestZonesForSymbols(ctx, cadencedb.LatestZonesForSymbolsParams{Timeframe: string(TimeframeMonth), Symbols: symbols})
	if err != nil {
		return nil, err
	}
	bySymbol := map[string][]cadencedb.CadenceZone{}
	for i := range rows {
		bySymbol[rows[i].Symbol] = append(bySymbol[rows[i].Symbol], rows[i])
	}
	highs, err := h.bullHighs(c, userID, symbols)
	if err != nil {
		return nil, err
	}
	out := make([]PlanSymbolInput, 0, len(symbols))
	for _, sym := range symbols {
		p := byTicker[sym]
		if confirmed, _ := newestPair(bySymbol[sym]); confirmed != nil {
			p.Zone = zoneDTO(*confirmed, highs[sym])
		}
		out = append(out, *p)
	}
	return out, nil
}

// surplusIn is the month's surplus in the plan currency. A user without a
// budget, or a surplus that cannot be converted, gets nil and the reason.
func (h *handlers) surplusIn(ctx context.Context, userID uuid.UUID, planCurrency string) (*float64, string, error) {
	sur, err := h.surplus.MonthSurplus(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if !sur.Found {
		return nil, NoteNoBudget, nil
	}
	amount := sur.Amount
	if !strings.EqualFold(sur.Currency, planCurrency) {
		rate := h.rate(ctx, sur.Currency, planCurrency)
		if rate == nil {
			return nil, NoteSurplusFxUnavailable, nil
		}
		amount = rebalancing.RoundedMoney(amount * *rate)
	}
	return &amount, "", nil
}

// rate is plan-currency units per 1 unit of from; nil when unavailable.
func (h *handlers) rate(ctx context.Context, from, to string) *float64 {
	if strings.EqualFold(from, to) {
		one := 1.0
		return &one
	}
	fx, err := h.market.Fx(ctx, strings.ToUpper(from)+"/"+strings.ToUpper(to))
	if err != nil || !(fx.Rate > 0) {
		h.d.Logger.WarnContext(ctx, "cadence.plan fx unavailable", "from", from, "to", to, "error", err)
		return nil
	}
	return &fx.Rate
}

// attachQuotes fetches each symbol's last price, and one rate per quote
// currency. A failure leaves that symbol's quote (or rate) nil.
func (h *handlers) attachQuotes(ctx context.Context, planCurrency string, symbols []PlanSymbolInput) {
	rates := map[string]*float64{}
	for i := range symbols {
		s := &symbols[i]
		q, err := h.market.Quote(ctx, s.Symbol)
		if err != nil {
			h.d.Logger.WarnContext(ctx, "cadence.plan quote unavailable", "symbol", s.Symbol, "error", err)
			continue
		}
		cur := strings.ToUpper(q.Currency)
		s.Quote = &PlanQuote{Price: q.CurrentPrice, Currency: cur, AsOf: time.Unix(0, int64(q.Timestamp*float64(time.Second)))}
		rate, ok := rates[cur]
		if !ok {
			rate = h.rate(ctx, cur, planCurrency)
			rates[cur] = rate
		}
		s.QuoteToPlan = rate
	}
}
