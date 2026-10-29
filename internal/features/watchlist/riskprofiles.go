package watchlist

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Holding risk profiles (ScenarioController, /v1/holding-risk-profiles*).

var riskAssetCategories = []string{"stock", "etf", "mutual_fund", "crypto", "cash", "bond", "real_estate", "commodity"}

// riskProfileInput is RiskProfileInput.
type riskProfileInput struct {
	HoldingID       uuid.UUID     `json:"holdingId"`
	AssetCategory   string        `json:"assetCategory"`
	Sector          *string       `json:"sector"`
	Region          *string       `json:"region"`
	BenchmarkProxy  *string       `json:"benchmarkProxy"`
	ManualValue     *float64      `json:"manualValue"`
	Duration        *float64      `json:"duration"`
	Convexity       *float64      `json:"convexity"`
	FactorOverrides *scenarioJSON `json:"factorOverrides"`
}

// riskProfileResponse is HoldingRiskProfileModel encoded as a Fluent model:
// every property is present, nil optionals as null.
type riskProfileResponse struct {
	ID              string       `json:"id"`
	UserID          string       `json:"userId"`
	HoldingID       string       `json:"holdingId"`
	AssetCategory   string       `json:"assetCategory"`
	Sector          *string      `json:"sector"`
	Region          *string      `json:"region"`
	BenchmarkProxy  *string      `json:"benchmarkProxy"`
	ManualValue     *float64     `json:"manualValue"`
	Duration        *float64     `json:"duration"`
	Convexity       *float64     `json:"convexity"`
	FactorOverrides scenarioJSON `json:"factorOverrides"`
	CreatedAt       *string      `json:"createdAt"`
	UpdatedAt       *string      `json:"updatedAt"`
}

// loadRiskProfile is Fluent loading a holding_risk_profiles row.
func loadRiskProfile(m watchlistdb.HoldingRiskProfile) (riskProfileResponse, error) {
	overrides, err := loadScenarioJSON(m.FactorOverrides, "factor_overrides")
	if err != nil {
		return riskProfileResponse{}, err
	}
	return riskProfileResponse{
		ID:              uuidString(m.ID),
		UserID:          uuidString(m.UserID),
		HoldingID:       uuidString(m.HoldingID),
		AssetCategory:   m.AssetCategory,
		Sector:          m.Sector,
		Region:          m.Region,
		BenchmarkProxy:  m.BenchmarkProxy,
		ManualValue:     m.ManualValue,
		Duration:        m.Duration,
		Convexity:       m.Convexity,
		FactorOverrides: overrides,
		CreatedAt:       formatISOSeconds(m.CreatedAt),
		UpdatedAt:       formatISOSeconds(m.UpdatedAt),
	}, nil
}

// authorizeScenarioPlanning is ScenarioController.authorize.
func (h *handlers) authorizeScenarioPlanning(c *echo.Context) (*auth.Session, error) {
	if !h.scenarioPlanning {
		return nil, httpx.NotFound("")
	}
	s, err := auth.Require(c)
	if err != nil {
		return nil, err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureScenarioPlanning, s.UserID); err != nil {
		return nil, err
	}
	return s, nil
}

func (h *handlers) listRiskProfiles(c *echo.Context) error {
	s, err := h.authorizeScenarioPlanning(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListHoldingRiskProfiles(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	out := make([]riskProfileResponse, 0, len(rows))
	for i := range rows {
		r, err := loadRiskProfile(rows[i])
		if err != nil {
			return err
		}
		out = append(out, r)
	}
	return httpx.OK(c, out)
}

// nilIfBlank is String.nilIfBlank: trimmed, nil when empty.
func nilIfBlank(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil
	}
	return &v
}

func (h *handlers) saveRiskProfile(c *echo.Context) error {
	s, err := h.authorizeScenarioPlanning(c)
	if err != nil {
		return err
	}
	var in riskProfileInput
	if err := decodeBody(c, &in); err != nil {
		return err
	}
	ctx := c.Request().Context()
	if _, err := h.q.OwnedHolding(ctx, watchlistdb.OwnedHoldingParams{ID: in.HoldingID, UserID: s.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("")
		}
		return err
	}
	if !slices.Contains(riskAssetCategories, in.AssetCategory) ||
		(in.ManualValue != nil && !(*in.ManualValue > 0)) ||
		(in.Duration != nil && !(*in.Duration >= 0)) ||
		(in.Convexity != nil && !(*in.Convexity >= 0)) {
		return httpx.BadRequest("Invalid risk profile")
	}
	existing, err := h.q.HoldingRiskProfileByHolding(ctx, watchlistdb.HoldingRiskProfileByHoldingParams{UserID: s.UserID, HoldingID: in.HoldingID})
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if found {
		if _, err := loadRiskProfile(existing); err != nil {
			return err
		}
	}
	overrides := emptyScenarioJSON()
	if in.FactorOverrides != nil {
		overrides = *in.FactorOverrides
	}
	overridesJSON, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	benchmark := nilIfBlank(in.BenchmarkProxy)
	if benchmark != nil {
		v := strings.ToUpper(*benchmark)
		benchmark = &v
	}
	now := h.now()
	var saved watchlistdb.HoldingRiskProfile
	if found {
		saved, err = h.q.UpdateHoldingRiskProfile(ctx, watchlistdb.UpdateHoldingRiskProfileParams{
			AssetCategory:   in.AssetCategory,
			Sector:          nilIfBlank(in.Sector),
			Region:          nilIfBlank(in.Region),
			BenchmarkProxy:  benchmark,
			ManualValue:     in.ManualValue,
			Duration:        in.Duration,
			Convexity:       in.Convexity,
			FactorOverrides: overridesJSON,
			Now:             &now,
			ID:              existing.ID,
		})
	} else {
		saved, err = h.q.InsertHoldingRiskProfile(ctx, watchlistdb.InsertHoldingRiskProfileParams{
			ID:              uuid.New(),
			UserID:          s.UserID,
			HoldingID:       in.HoldingID,
			AssetCategory:   in.AssetCategory,
			Sector:          nilIfBlank(in.Sector),
			Region:          nilIfBlank(in.Region),
			BenchmarkProxy:  benchmark,
			ManualValue:     in.ManualValue,
			Duration:        in.Duration,
			Convexity:       in.Convexity,
			FactorOverrides: overridesJSON,
			Now:             &now,
		})
	}
	if err != nil {
		return err
	}
	out, err := loadRiskProfile(saved)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handlers) deleteRiskProfile(c *echo.Context) error {
	s, err := h.authorizeScenarioPlanning(c)
	if err != nil {
		return err
	}
	id, ok := parseSwiftUUID(c.Param("id"))
	if !ok {
		return httpx.BadRequest("Invalid id")
	}
	ctx := c.Request().Context()
	m, err := h.q.OwnedHoldingRiskProfile(ctx, watchlistdb.OwnedHoldingRiskProfileParams{ID: id, UserID: s.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if _, err := loadRiskProfile(m); err != nil {
		return err
	}
	if err := h.q.DeleteHoldingRiskProfile(ctx, m.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
