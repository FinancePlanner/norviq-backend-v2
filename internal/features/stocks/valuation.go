package stocks

import (
	"context"
	"strconv"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/stocksdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// PriceRange is StockPlanShared.PriceRange.
type PriceRange struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// StockValuationRequest is StockPlanShared.StockValuationRequest, which is
// also the response.
type StockValuationRequest struct {
	Symbol     string     `json:"symbol"`
	BearCase   PriceRange `json:"bearCase"`
	BaseCase   PriceRange `json:"baseCase"`
	BullCase   PriceRange `json:"bullCase"`
	Rationale  *string    `json:"rationale,omitempty"`
	TargetDate *string    `json:"targetDate,omitempty"`
}

func valuationResponse(v stocksdb.StockValuation) StockValuationRequest {
	r := StockValuationRequest{
		Symbol:    v.Symbol,
		BearCase:  PriceRange{v.BearLow, v.BearHigh},
		BaseCase:  PriceRange{v.BaseLow, v.BaseHigh},
		BullCase:  PriceRange{v.BullLow, v.BullHigh},
		Rationale: v.Rationale,
	}
	if v.TargetDate != nil {
		s := portfolios.FormatDateOnly(*v.TargetDate)
		r.TargetDate = &s
	}
	return r
}

func (h *handlers) findValuation(ctx context.Context, symbol string, userID uuid.UUID) (*stocksdb.StockValuation, error) {
	rows, err := stocksdb.New(h.db).GetValuation(ctx, stocksdb.GetValuationParams{UserID: userID, Symbol: normalizeSymbol(symbol)})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// premiumThenSymbol opens every valuation/insights handler: the premium gate
// runs before the path parameter is read.
func (h *handlers) premiumThenSymbol(c *echo.Context, feature entitlements.Feature) (uuid.UUID, string, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, "", err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), feature, s.UserID); err != nil {
		return uuid.Nil, "", err
	}
	symbol, err := requireSymbolParam(c)
	return s.UserID, symbol, err
}

// GET /v1/stocks/symbol/:symbol/valuation. A missing stock or valuation is a
// bare 404 with no body.
func (h *handlers) getStockValuation(c *echo.Context) error {
	userID, symbol, err := h.premiumThenSymbol(c, entitlements.FeatureValuationCases)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	normalized, err := validateSymbol(symbol)
	if err != nil {
		return err
	}
	stock, err := h.findStockBySymbol(ctx, normalized, userID)
	if err != nil {
		return err
	}
	if stock == nil {
		return emptyNotFound(c)
	}
	v, err := h.findValuation(ctx, normalized, userID)
	if err != nil {
		return err
	}
	if v == nil {
		return emptyNotFound(c)
	}
	return httpx.OK(c, valuationResponse(*v))
}

// normalizeValuationPayload is StockServiceImpl.normalizeValuationPayload.
func normalizeValuationPayload(pathSymbol string, p StockValuationRequest) (StockValuationRequest, error) {
	np, err := validateSymbol(pathSymbol)
	if err != nil {
		return p, err
	}
	nb, err := validateSymbol(p.Symbol)
	if err != nil {
		return p, err
	}
	if np != nb {
		return p, httpx.BadRequest("Body symbol must match the route symbol. " +
			"routeRaw=" + swiftReflecting(pathSymbol) + " " +
			"bodyRaw=" + swiftReflecting(p.Symbol) + " " +
			"routeNormalized=" + swiftReflecting(np) + " " +
			"bodyNormalized=" + swiftReflecting(nb))
	}
	for _, r := range []struct {
		name string
		v    PriceRange
	}{{"bearCase", p.BearCase}, {"baseCase", p.BaseCase}, {"bullCase", p.BullCase}} {
		if !(r.v.Low >= 0) {
			return p, httpx.BadRequest(r.name + ".low must be greater than or equal to 0.")
		}
		if !(r.v.High >= 0) {
			return p, httpx.BadRequest(r.name + ".high must be greater than or equal to 0.")
		}
		if !(r.v.Low <= r.v.High) {
			return p, httpx.BadRequest(r.name + ".low must be less than or equal to " + r.name + ".high.")
		}
	}
	out := StockValuationRequest{Symbol: np, BearCase: p.BearCase, BaseCase: p.BaseCase, BullCase: p.BullCase, Rationale: emptyToNil(p.Rationale)}
	// normalizeOptionalDateString re-formats the parsed date.
	td, err := parseOptionalDate(p.TargetDate, "targetDate")
	if err != nil {
		return p, err
	}
	if td != nil {
		s := portfolios.FormatDateOnly(*td)
		out.TargetDate = &s
	}
	return out, nil
}

func (h *handlers) decodeValuation(c *echo.Context, feature entitlements.Feature) (uuid.UUID, StockValuationRequest, error) {
	userID, symbol, err := h.premiumThenSymbol(c, feature)
	if err != nil {
		return uuid.Nil, StockValuationRequest{}, err
	}
	var p StockValuationRequest
	if err := decodeBody(c, valuationRequestSpec, &p); err != nil {
		return uuid.Nil, p, err
	}
	n, err := normalizeValuationPayload(symbol, p)
	if err != nil {
		return uuid.Nil, p, err
	}
	stock, err := h.findStockBySymbol(c.Request().Context(), n.Symbol, userID)
	if err != nil {
		return uuid.Nil, p, err
	}
	if stock == nil {
		return uuid.Nil, p, errStockNotFound()
	}
	return userID, n, nil
}

// POST /v1/stocks/symbol/:symbol/valuation
func (h *handlers) createStockValuation(c *echo.Context) error {
	userID, p, err := h.decodeValuation(c, entitlements.FeatureValuationCases)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	existing, err := h.findValuation(ctx, p.Symbol, userID)
	if err != nil {
		return err
	}
	if existing != nil {
		return errValuationExists()
	}
	q := stocksdb.New(h.db)
	n, err := q.CountValuations(ctx, userID)
	if err != nil {
		return err
	}
	if err := h.ent.EnforceResourceLimit(ctx, entitlements.FeatureValuationCases, userID, int(n), 1); err != nil {
		return err
	}
	td, err := parseOptionalDate(p.TargetDate, "targetDate")
	if err != nil {
		return err
	}
	v, err := q.InsertValuation(ctx, stocksdb.InsertValuationParams{
		ID: uuid.New(), UserID: userID, Symbol: normalizeSymbol(p.Symbol),
		BearLow: p.BearCase.Low, BearHigh: p.BearCase.High,
		BaseLow: p.BaseCase.Low, BaseHigh: p.BaseCase.High,
		BullLow: p.BullCase.Low, BullHigh: p.BullCase.High,
		Rationale: emptyToNil(p.Rationale), TargetDate: td, Now: portfolios.Now(),
	})
	if err != nil {
		return err
	}
	return httpx.Created(c, valuationResponse(v))
}

// PUT /v1/stocks/symbol/:symbol/valuation
func (h *handlers) updateStockValuation(c *echo.Context) error {
	userID, p, err := h.decodeValuation(c, entitlements.FeatureValuationCases)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	existing, err := h.findValuation(ctx, p.Symbol, userID)
	if err != nil {
		return err
	}
	if existing == nil {
		return errValuationNotFound()
	}
	td, err := parseOptionalDate(p.TargetDate, "targetDate")
	if err != nil {
		return err
	}
	now := portfolios.Now()
	v, err := stocksdb.New(h.db).UpdateValuation(ctx, stocksdb.UpdateValuationParams{
		ID: existing.ID, Symbol: normalizeSymbol(p.Symbol),
		BearLow: p.BearCase.Low, BearHigh: p.BearCase.High,
		BaseLow: p.BaseCase.Low, BaseHigh: p.BaseCase.High,
		BullLow: p.BullCase.Low, BullHigh: p.BullCase.High,
		Rationale: emptyToNil(p.Rationale), TargetDate: td, UpdatedAt: &now,
	})
	if err != nil {
		return err
	}
	return httpx.OK(c, valuationResponse(v))
}

// ---- query decoding ------------------------------------------------------------

// queryDecodeString reads a String? field of a `req.query.decode` struct.
func queryDecodeString(c *echo.Context, name string) *string { return httpx.QueryString(c, name) }

// queryDecodeInt reads an Int? field of a `req.query.decode` struct: absent
// is nil, a value that is not an Int fails the whole decode with a 400.
func queryDecodeInt(c *echo.Context, name string) (*int, error) {
	raw := httpx.QueryString(c, name)
	if raw == nil {
		return nil, nil
	}
	v, err := strconv.Atoi(*raw)
	if err != nil {
		return nil, httpx.BadRequest("Value was not of type 'Int' at path '" + name + "'. Data found at '" + name + "' was not Int.")
	}
	return &v, nil
}
