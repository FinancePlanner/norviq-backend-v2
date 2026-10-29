// Package simulations ports PortfolioSimulationController,
// PortfolioSimulationService and SimulationAllocationModelBuilder
// (/v1/portfolio/simulations). Pricing and the trade math are the
// rebalancing package's PortfolioSnapshotPricer and RebalancingEngine.
package simulations

import (
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/features/rebalancing/swiftjson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/portfolio/simulations.
type Feature struct {
	// Market overrides market.SharedService (tests).
	Market func(d *server.Deps) (rebalancing.MarketData, error)
}

// Name implements server.Feature.
func (Feature) Name() string { return "simulations" }

// Register implements server.Feature. routes.swift wraps the whole
// controller in RateLimitMiddleware(30/60s, "ratelimit:simulation") OUTSIDE
// its authenticator, so the bucket is keyed by client IP, never by user.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	mk := f.Market
	if mk == nil {
		mk = rebalancing.SharedMarket
	}
	md, err := mk(d)
	if err != nil {
		return err
	}
	h := &handler{svc: &Service{DB: d.DB, Access: portfolios.NewAccess(d.DB), Pricer: rebalancing.Pricer{Market: md}}}
	limit := d.Limiter.Limit(30, 60*time.Second, "ratelimit:simulation")

	read := r.V1.Group("/portfolio/simulations", limit, d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningRead))
	read.GET("", h.list)
	read.GET("/:simulationId", h.detail)

	write := r.V1.Group("/portfolio/simulations", limit, d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningWrite))
	write.POST("", h.create)
	write.PUT("/:simulationId", h.update)
	write.DELETE("/:simulationId", h.delete)
	write.POST("/preview", h.preview)
	write.POST("/:simulationId/compute", h.compute)
	return nil
}

type handler struct{ svc *Service }

func identifier(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("simulationId"))
	if !ok {
		return uuid.Nil, httpx.BadRequest("A valid simulation id is required.")
	}
	return id, nil
}

func (h *handler) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := h.svc.List(c.Request().Context(), s.UserID, httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 25), httpx.QueryString(c, "cursor"))
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) detail(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := identifier(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Detail(c.Request().Context(), id, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) create(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body UpsertRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.Create(c.Request().Context(), s.UserID, body)
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

func (h *handler) update(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := identifier(c)
	if err != nil {
		return err
	}
	var body UpsertRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.Update(c.Request().Context(), id, s.UserID, body)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) delete(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := identifier(c)
	if err != nil {
		return err
	}
	if err := h.svc.Delete(c.Request().Context(), id, s.UserID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handler) compute(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	// `(try? req.content.decode(...)) ?? PortfolioSimulationComputeRequest()`
	var body ComputeRequest
	if swiftjson.DecodeRequest(c, &body) != nil {
		body = ComputeRequest{}
	}
	id, err := identifier(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Compute(c.Request().Context(), id, s.UserID, body)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) preview(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body UpsertRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.Preview(c.Request().Context(), s.UserID, body)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
