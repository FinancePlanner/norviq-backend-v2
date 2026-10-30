// Package rebalancing ports RebalancingController
// (/v1/portfolios/:portfolioId/rebalancing/... and the user-level
// /v1/rebalancing/dashboard, /alerts, /alerts/:alertId/acknowledge),
// RebalancingService, RebalancingEngine, PortfolioSnapshotPricer and
// RebalancingAlertPoller (AlertJob).
package rebalancing

import (
	"fmt"
	"net/http"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/config"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/rebalancing/swiftjson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Settings are the controller's env switches.
type Settings struct {
	// Enabled is REBALANCING_ENABLED (default true); off, every route 404s.
	Enabled string `env:"REBALANCING_ENABLED"`
}

// Feature mounts the portfolio rebalancing routes.
type Feature struct {
	// Market overrides market.SharedService (tests).
	Market func(d *server.Deps) (MarketData, error)
	// ScheduleAchievementCheck is req.scheduleAchievementCheck(for:), run
	// after a plan completes. nil is a no-op (wire achievementhooks.Schedule).
	ScheduleAchievementCheck func(userID uuid.UUID)
}

// Name implements server.Feature.
func (Feature) Name() string { return "rebalancing" }

// NewService builds the service on d (the simulations package reuses its
// pricer and engine).
func NewService(d *server.Deps, md MarketData) *Service {
	return &Service{DB: d.DB, Access: portfolios.NewAccess(d.DB), Pricer: Pricer{Market: md}}
}

// SharedMarket is market.SharedService as a MarketData.
func SharedMarket(d *server.Deps) (MarketData, error) { return market.SharedService(d) }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s, err := config.Load[Settings]()
	if err != nil {
		return err
	}
	mk := f.Market
	if mk == nil {
		mk = SharedMarket
	}
	md, err := mk(d)
	if err != nil {
		return err
	}
	h := &handler{svc: NewService(d, md), enabled: config.Bool(s.Enabled, true), achievements: f.ScheduleAchievementCheck}

	userRead := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningRead)}
	r.V1.GET("/rebalancing/dashboard", h.dashboard, userRead...)
	r.V1.GET("/rebalancing/alerts", h.alerts, userRead...)
	r.V1.POST("/rebalancing/alerts/:alertId/acknowledge", h.acknowledgeAlert,
		d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningWrite))

	read := r.V1.Group("/portfolios/:portfolioId/rebalancing", d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningRead))
	read.GET("/models", h.models)
	read.GET("/overview", h.overview)
	read.GET("/plans", h.plans)
	read.GET("/plans/:planId/export.csv", h.exportCSV)
	read.GET("/plans/:planId/export.pdf", h.exportPDF)
	read.GET("/history", h.history)
	read.GET("/preferences", h.preferences)

	write := r.V1.Group("/portfolios/:portfolioId/rebalancing", d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopePlanningWrite))
	write.POST("/models", h.createModel)
	write.PUT("/models/:modelId", h.updateModel)
	write.POST("/models/:modelId/activate", h.activateModel)
	write.POST("/models/:modelId/copy", h.copyModel)
	write.POST("/simulate", h.simulate)
	write.POST("/plans", h.createPlan)
	write.POST("/plans/:planId/complete", h.completePlan)
	write.POST("/plans/:planId/cancel", h.cancelPlan)
	write.PUT("/preferences", h.updatePreferences)
	return nil
}

type handler struct {
	svc          *Service
	enabled      bool
	achievements func(uuid.UUID)
}

// begin is requireEnabled() then req.auth.require(SessionToken.self).
func (h *handler) begin(c *echo.Context) (uuid.UUID, error) {
	if !h.enabled {
		return uuid.Nil, httpx.NotFound("")
	}
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, err
	}
	return s.UserID, nil
}

// param is the controller's parameter(_:_:).
func param(c *echo.Context, name string) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param(name))
	if !ok {
		return uuid.Nil, httpx.BadRequest("Invalid " + name + ".")
	}
	return id, nil
}

func (h *handler) models(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	out, err := h.svc.Models(c.Request().Context(), pid, user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) createModel(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	var body AllocationModelUpsertRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.CreateModel(c.Request().Context(), pid, user, body)
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

func (h *handler) updateModel(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	mid, err := param(c, "modelId")
	if err != nil {
		return err
	}
	var body AllocationModelUpsertRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.UpdateModel(c.Request().Context(), pid, mid, user, body)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) activateModel(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	mid, err := param(c, "modelId")
	if err != nil {
		return err
	}
	out, err := h.svc.ActivateModel(c.Request().Context(), pid, mid, user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) copyModel(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	mid, err := param(c, "modelId")
	if err != nil {
		return err
	}
	var body BulkCopyRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.CopyModel(c.Request().Context(), pid, mid, user, body)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) overview(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	out, err := h.svc.Overview(c.Request().Context(), pid, user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) simulate(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	var body SimulationRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.Simulate(c.Request().Context(), pid, user, body)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) plans(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	out, err := h.svc.Plans(c.Request().Context(), pid, user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) createPlan(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	var body PlanCreateRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.CreatePlan(c.Request().Context(), pid, user, body)
	if err != nil {
		return err
	}
	return httpx.Created(c, out)
}

func (h *handler) completePlan(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	// `try? req.content.decode(...)`: any failure means no note.
	var body PlanCompletionRequest
	if swiftjson.DecodeRequest(c, &body) != nil {
		body = PlanCompletionRequest{}
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	planID, err := param(c, "planId")
	if err != nil {
		return err
	}
	out, err := h.svc.TransitionPlan(c.Request().Context(), pid, planID, user, PlanCompleted, body.Note)
	if err != nil {
		return err
	}
	if out.Status == PlanCompleted && h.achievements != nil {
		h.achievements(user)
	}
	return httpx.OK(c, out)
}

func (h *handler) cancelPlan(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	planID, err := param(c, "planId")
	if err != nil {
		return err
	}
	out, err := h.svc.TransitionPlan(c.Request().Context(), pid, planID, user, PlanCancelled, nil)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) history(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	out, err := h.svc.History(c.Request().Context(), pid, user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) preferences(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	out, err := h.svc.Preferences(c.Request().Context(), pid, user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) updatePreferences(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return err
	}
	var body UpdatePreferencesRequest
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	out, err := h.svc.UpdatePreferences(c.Request().Context(), pid, user, body)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

// exportedPlan finds the plan among the newest 100 (Swift's `plans`).
func (h *handler) exportedPlan(c *echo.Context) (Plan, error) {
	user, err := h.begin(c)
	if err != nil {
		return Plan{}, err
	}
	pid, err := param(c, "portfolioId")
	if err != nil {
		return Plan{}, err
	}
	planID, err := param(c, "planId")
	if err != nil {
		return Plan{}, err
	}
	available, err := h.svc.Plans(c.Request().Context(), pid, user)
	if err != nil {
		return Plan{}, err
	}
	want := apijson.FormatUUID(planID)
	for i := range available {
		p := &available[i]
		if p.ID == want {
			return *p, nil
		}
	}
	return Plan{}, errPlanNotFound()
}

func number(v float64) string { return fmt.Sprintf("%.4f", v) }

func basisPointsText(v int) string { return fmt.Sprintf("%.2f%%", float64(v)/100) }

func csvField(v string) string {
	if !strings.ContainsAny(v, ",\"\n") {
		return v
	}
	return "\"" + strings.ReplaceAll(v, "\"", "\"\"") + "\""
}

// PlanCSV is the export.csv body.
func PlanCSV(p Plan) string {
	lines := []string{"side,symbol,quantity,price,notional,estimated_fee,estimated_cost_basis,estimated_realized_gain_loss,currency"}
	for i := range p.Trades {
		t := &p.Trades[i]
		basis, gain := "", ""
		if t.EstimatedCostBasis != nil {
			basis = number(*t.EstimatedCostBasis)
		}
		if t.EstimatedRealizedGainLoss != nil {
			gain = number(*t.EstimatedRealizedGainLoss)
		}
		fields := []string{t.Side, t.Symbol, number(t.Quantity), number(t.Price), number(t.Notional), number(t.EstimatedFee), basis, gain, t.Currency}
		for i, f := range fields {
			fields[i] = csvField(f)
		}
		lines = append(lines, strings.Join(fields, ","))
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

func (h *handler) exportCSV(c *echo.Context) error {
	p, err := h.exportedPlan(c)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="rebalance-`+p.ID+`.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", []byte(PlanCSV(p)))
}

// PlanPDFLines are the text lines the PDF export prints.
func PlanPDFLines(p Plan) []string {
	lines := []string{
		"Norviq Rebalancing Plan",
		"Portfolio: " + p.PortfolioID,
		"Status: " + p.Status,
		"Drift: " + basisPointsText(p.DriftBeforeBasisPoints) + " -> " + basisPointsText(p.DriftAfterBasisPoints),
		"Estimated fees: " + p.BaseCurrency + " " + number(p.EstimatedFees),
		"Estimated realized gain/loss: " + p.BaseCurrency + " " + number(p.EstimatedRealizedGainLoss),
		"",
	}
	for i := range p.Trades {
		t := &p.Trades[i]
		lines = append(lines, strings.ToUpper(t.Side)+" "+t.Symbol+"  "+number(t.Quantity)+" @ "+number(t.Price)+"  "+t.Currency+" "+number(t.Notional))
	}
	return append(lines, "", "Planning record only. Norviq did not place these orders. Tax figures are estimates, not tax advice.")
}

// MinimalPDF is MinimalRebalancingPDF.make: a one-page Helvetica PDF with
// no timestamps, byte-identical to Vapor's.
func MinimalPDF(lines []string) []byte {
	if len(lines) > 52 {
		lines = lines[:52]
	}
	esc := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`)
	text := make([]string, len(lines))
	for i, line := range lines {
		e := esc.Replace(line)
		if i == 0 {
			text[i] = "BT /F1 18 Tf 54 760 Td (" + e + ") Tj ET"
		} else {
			text[i] = fmt.Sprintf("BT /F1 10 Tf 54 %d Td (%s) Tj ET", 738-i*13, e)
		}
	}
	stream := strings.Join(text, "\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n", len(stream)) + stream + "\nendstream",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for i, o := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n", i+1)
		b.WriteString(o)
		b.WriteString("\nendobj\n")
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", len(objects)+1, xref)
	return []byte(b.String())
}

func (h *handler) exportPDF(c *echo.Context) error {
	p, err := h.exportedPlan(c)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="rebalance-`+p.ID+`.pdf"`)
	return c.Blob(http.StatusOK, "application/pdf", MinimalPDF(PlanPDFLines(p)))
}

func (h *handler) dashboard(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Dashboard(c.Request().Context(), user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) alerts(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	out, err := h.svc.Alerts(c.Request().Context(), user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) acknowledgeAlert(c *echo.Context) error {
	user, err := h.begin(c)
	if err != nil {
		return err
	}
	id, err := param(c, "alertId")
	if err != nil {
		return err
	}
	out, err := h.svc.Acknowledge(c.Request().Context(), id, user)
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}
