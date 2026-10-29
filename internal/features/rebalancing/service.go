package rebalancing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/rebalancingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service is DefaultRebalancingService. Every method is exported, including
// the user-level dashboard/alerts/acknowledge routes that another package
// mounts.
type Service struct {
	DB     *pgxpool.Pool
	Access portfolios.Access
	Pricer Pricer
	Engine Engine
}

func (s *Service) q(db rebalancingdb.DBTX) *rebalancingdb.Queries { return rebalancingdb.New(db) }

func (s *Service) access(ctx context.Context, portfolioID, userID uuid.UUID, editing bool) (portfolios.AccessContext, error) {
	return s.Access.Require(ctx, s.DB, portfolioID, userID, portfolios.RequireOptions{Editing: editing})
}

func errModelNotFound() *httpx.Error { return httpx.NotFound("Allocation model not found.") }
func errPlanNotFound() *httpx.Error  { return httpx.NotFound("Rebalancing plan not found.") }

// mapEngineError is RebalancingService.mapEngineError.
func mapEngineError(err error) error {
	var e *EngineError
	if !errors.As(err, &e) {
		return httpx.Abort(http.StatusInternalServerError, "Rebalancing calculation failed.")
	}
	switch e.Case {
	case "staleModel":
		return httpx.Conflict("Allocation model changed. Reload before simulating.")
	case "invalidModel":
		return httpx.BadRequest(e.Detail)
	case "invalidHolding":
		return httpx.Unprocessable(e.Detail)
	case "unknownOverride":
		return httpx.BadRequest("Unknown or duplicate trade override: " + e.Detail + ".")
	case "oversell":
		return httpx.Unprocessable("A sell override exceeds the available " + e.Detail + " holding.")
	case "invalidCashFlow":
		return httpx.BadRequest("Cash flow must be a finite amount.")
	case "insufficientCash":
		return httpx.Unprocessable("The proposed trades would create a negative cash balance.")
	}
	return httpx.Abort(http.StatusInternalServerError, "Rebalancing calculation failed.")
}

// NormalizedName is `String(trimmed.prefix(120))` (runes, not graphemes).
func NormalizedName(raw string) string {
	v := portfolios.TrimSwift(raw)
	if utf8.RuneCountInString(v) <= 120 {
		return v
	}
	return string([]rune(v)[:120])
}

func optionalNormalizedName(raw *string) *string {
	if raw == nil {
		return nil
	}
	v := NormalizedName(*raw)
	if v == "" {
		return nil
	}
	return &v
}

// normalizedSymbol is the service's normalizedSymbol (24-character cap).
func normalizedSymbol(raw *string) *string {
	if raw == nil {
		return nil
	}
	v := strings.ToUpper(portfolios.TrimSwift(*raw))
	if v == "" {
		return nil
	}
	if utf8.RuneCountInString(v) > 24 {
		v = string([]rune(v)[:24])
	}
	return &v
}

func first[T any](rows []T, err error) (*T, error) {
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// Models is GET .../rebalancing/models.
func (s *Service) Models(ctx context.Context, portfolioID, userID uuid.UUID) ([]AllocationModel, error) {
	if _, err := s.access(ctx, portfolioID, userID, false); err != nil {
		return nil, err
	}
	records, err := s.q(s.DB).ListModels(ctx, portfolioID)
	if err != nil {
		return nil, err
	}
	out := make([]AllocationModel, 0, len(records))
	for i := range records {
		r := &records[i]
		m, err := s.makeModel(ctx, s.DB, *r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Service) validate(p AllocationModelUpsertRequest, portfolioID uuid.UUID, baseCurrency string) error {
	revision := 1
	if p.ExpectedRevision != nil {
		revision = *p.ExpectedRevision
	}
	m := AllocationModel{
		ID: apijson.FormatUUID(uuid.New()), PortfolioID: apijson.FormatUUID(portfolioID),
		Name: p.Name, GroupingMode: p.GroupingMode, IsActive: p.Activate, Revision: revision,
		BaseCurrency:                      baseCurrency,
		DefaultTargetThresholdBasisPoints: p.DefaultTargetThresholdBasisPoints,
		TotalThresholdBasisPoints:         p.TotalThresholdBasisPoints,
		FractionalSharesEnabled:           p.FractionalSharesEnabled,
		QuantityIncrement:                 p.QuantityIncrement,
		MinimumTradeAmount:                p.MinimumTradeAmount,
		FlatFee:                           p.FlatFee,
		VariableFeeBasisPoints:            p.VariableFeeBasisPoints,
		Buckets:                           p.Buckets,
	}
	if err := s.Engine.Validate(m); err != nil {
		return mapEngineError(err)
	}
	return nil
}

func quantityIncrement(p AllocationModelUpsertRequest) float64 {
	if p.FractionalSharesEnabled {
		return p.QuantityIncrement
	}
	return 1
}

// CreateModel is POST .../rebalancing/models.
func (s *Service) CreateModel(ctx context.Context, portfolioID, userID uuid.UUID, p AllocationModelUpsertRequest) (AllocationModel, error) {
	ac, err := s.access(ctx, portfolioID, userID, true)
	if err != nil {
		return AllocationModel{}, err
	}
	if err := s.validate(p, portfolioID, ac.Portfolio.BaseCurrency); err != nil {
		return AllocationModel{}, err
	}
	recordID := uuid.New()
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := s.q(tx)
		now := portfolios.Now()
		if p.Activate {
			if err := q.DeactivateModels(ctx, rebalancingdb.DeactivateModelsParams{Now: &now, PortfolioID: portfolioID}); err != nil {
				return err
			}
		}
		now = portfolios.Now()
		if err := q.InsertModel(ctx, rebalancingdb.InsertModelParams{
			ID: recordID, PortfolioID: portfolioID, CreatedByUserID: userID,
			Name: NormalizedName(p.Name), GroupingMode: p.GroupingMode, IsActive: p.Activate, Revision: 1,
			BaseCurrency:              ac.Portfolio.BaseCurrency,
			DefaultTargetThresholdBps: int64(p.DefaultTargetThresholdBasisPoints),
			TotalThresholdBps:         int64(p.TotalThresholdBasisPoints),
			FractionalSharesEnabled:   p.FractionalSharesEnabled,
			QuantityIncrement:         quantityIncrement(p),
			MinimumTradeAmount:        p.MinimumTradeAmount,
			FlatFee:                   p.FlatFee,
			VariableFeeBps:            int64(p.VariableFeeBasisPoints),
			Now:                       &now,
		}); err != nil {
			return err
		}
		return s.replaceTargets(ctx, q, recordID, p.Buckets)
	})
	if err != nil {
		return AllocationModel{}, err
	}
	rec, err := first(s.q(s.DB).GetModel(ctx, recordID))
	if err != nil {
		return AllocationModel{}, err
	}
	if rec == nil {
		return AllocationModel{}, httpx.Abort(http.StatusInternalServerError, "Allocation model was not created.")
	}
	return s.makeModel(ctx, s.DB, *rec)
}

// UpdateModel is PUT .../rebalancing/models/:modelId.
func (s *Service) UpdateModel(ctx context.Context, portfolioID, modelID, userID uuid.UUID, p AllocationModelUpsertRequest) (AllocationModel, error) {
	ac, err := s.access(ctx, portfolioID, userID, true)
	if err != nil {
		return AllocationModel{}, err
	}
	if err := s.validate(p, portfolioID, ac.Portfolio.BaseCurrency); err != nil {
		return AllocationModel{}, err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := s.q(tx)
		rec, err := first(q.GetModelInPortfolio(ctx, rebalancingdb.GetModelInPortfolioParams{ID: modelID, PortfolioID: portfolioID}))
		if err != nil {
			return err
		}
		if rec == nil {
			return errModelNotFound()
		}
		if p.ExpectedRevision == nil || int64(*p.ExpectedRevision) != rec.Revision {
			return httpx.Conflict("Allocation model changed. Reload before saving.")
		}
		now := portfolios.Now()
		if p.Activate {
			if err := q.DeactivateModels(ctx, rebalancingdb.DeactivateModelsParams{Now: &now, PortfolioID: portfolioID, ExceptID: &modelID}); err != nil {
				return err
			}
		}
		now = portfolios.Now()
		if err := q.UpdateModel(ctx, rebalancingdb.UpdateModelParams{
			ID: modelID, Name: NormalizedName(p.Name), GroupingMode: p.GroupingMode, IsActive: p.Activate,
			Revision:                  rec.Revision + 1,
			DefaultTargetThresholdBps: int64(p.DefaultTargetThresholdBasisPoints),
			TotalThresholdBps:         int64(p.TotalThresholdBasisPoints),
			FractionalSharesEnabled:   p.FractionalSharesEnabled,
			QuantityIncrement:         quantityIncrement(p),
			MinimumTradeAmount:        p.MinimumTradeAmount,
			FlatFee:                   p.FlatFee,
			VariableFeeBps:            int64(p.VariableFeeBasisPoints),
			Now:                       &now,
		}); err != nil {
			return err
		}
		if err := q.DeleteLeaves(ctx, modelID); err != nil {
			return err
		}
		if err := q.DeleteBuckets(ctx, modelID); err != nil {
			return err
		}
		if err := s.replaceTargets(ctx, q, modelID, p.Buckets); err != nil {
			return err
		}
		now = portfolios.Now()
		return q.ResolveOpenAlertsForModel(ctx, rebalancingdb.ResolveOpenAlertsForModelParams{ModelID: modelID, Now: &now})
	})
	if err != nil {
		return AllocationModel{}, err
	}
	return s.reload(ctx, modelID)
}

func (s *Service) reload(ctx context.Context, modelID uuid.UUID) (AllocationModel, error) {
	rec, err := first(s.q(s.DB).GetModel(ctx, modelID))
	if err != nil {
		return AllocationModel{}, err
	}
	if rec == nil {
		return AllocationModel{}, httpx.NotFound("")
	}
	return s.makeModel(ctx, s.DB, *rec)
}

// ActivateModel is POST .../rebalancing/models/:modelId/activate.
func (s *Service) ActivateModel(ctx context.Context, portfolioID, modelID, userID uuid.UUID) (AllocationModel, error) {
	if _, err := s.access(ctx, portfolioID, userID, true); err != nil {
		return AllocationModel{}, err
	}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := s.q(tx)
		rec, err := first(q.GetModelInPortfolio(ctx, rebalancingdb.GetModelInPortfolioParams{ID: modelID, PortfolioID: portfolioID}))
		if err != nil {
			return err
		}
		if rec == nil {
			return errModelNotFound()
		}
		now := portfolios.Now()
		if err := q.DeactivateModels(ctx, rebalancingdb.DeactivateModelsParams{Now: &now, PortfolioID: portfolioID, ExceptID: &modelID}); err != nil {
			return err
		}
		now = portfolios.Now()
		if err := q.ActivateModel(ctx, rebalancingdb.ActivateModelParams{ID: modelID, Revision: rec.Revision + 1, Now: &now}); err != nil {
			return err
		}
		now = portfolios.Now()
		return q.ResolveOpenAlertsForModel(ctx, rebalancingdb.ResolveOpenAlertsForModelParams{ModelID: modelID, Now: &now})
	})
	if err != nil {
		return AllocationModel{}, err
	}
	return s.reload(ctx, modelID)
}

// CopyModel is POST .../rebalancing/models/:modelId/copy. Not transactional
// across destinations, as in Swift: a later failure keeps earlier copies.
func (s *Service) CopyModel(ctx context.Context, portfolioID, modelID, userID uuid.UUID, p BulkCopyRequest) (BulkCopyResult, error) {
	if _, err := s.access(ctx, portfolioID, userID, false); err != nil {
		return BulkCopyResult{}, err
	}
	rec, err := first(s.q(s.DB).GetModelInPortfolio(ctx, rebalancingdb.GetModelInPortfolioParams{ID: modelID, PortfolioID: portfolioID}))
	if err != nil {
		return BulkCopyResult{}, err
	}
	if rec == nil {
		return BulkCopyResult{}, errModelNotFound()
	}
	source, err := s.makeModel(ctx, s.DB, *rec)
	if err != nil {
		return BulkCopyResult{}, err
	}
	ids := make([]uuid.UUID, 0, len(p.DestinationPortfolioIDs))
	seen := map[uuid.UUID]bool{}
	for _, raw := range p.DestinationPortfolioIDs {
		id, ok := portfolios.ParseSwiftUUID(raw)
		if !ok {
			return BulkCopyResult{}, httpx.BadRequest("Invalid destination portfolio id: " + raw)
		}
		ids = append(ids, id)
		seen[id] = true
	}
	if len(ids) == 0 || len(seen) != len(ids) {
		return BulkCopyResult{}, httpx.BadRequest("Destination portfolios must be unique and non-empty.")
	}
	result := BulkCopyResult{Models: []AllocationModel{}, Warnings: []ValuationWarning{}}
	for _, dest := range ids {
		if dest == portfolioID {
			return BulkCopyResult{}, httpx.BadRequest("The source portfolio cannot be a copy destination.")
		}
		ac, err := s.access(ctx, dest, userID, true)
		if err != nil {
			return BulkCopyResult{}, err
		}
		if ac.Portfolio.BaseCurrency != source.BaseCurrency {
			result.Warnings = append(result.Warnings, ValuationWarning{
				Code:    "base_currency_changed",
				Message: ac.Portfolio.Name + " uses " + ac.Portfolio.BaseCurrency + "; target percentages were copied without currency amounts.",
			})
		}
		created, err := s.CreateModel(ctx, dest, userID, AllocationModelUpsertRequest{
			Name: source.Name, GroupingMode: source.GroupingMode, Activate: p.Activate,
			DefaultTargetThresholdBasisPoints: source.DefaultTargetThresholdBasisPoints,
			TotalThresholdBasisPoints:         source.TotalThresholdBasisPoints,
			FractionalSharesEnabled:           source.FractionalSharesEnabled,
			QuantityIncrement:                 source.QuantityIncrement,
			MinimumTradeAmount:                source.MinimumTradeAmount,
			FlatFee:                           source.FlatFee,
			VariableFeeBasisPoints:            source.VariableFeeBasisPoints,
			Buckets:                           source.Buckets,
		})
		if err != nil {
			return BulkCopyResult{}, err
		}
		result.Models = append(result.Models, created)
	}
	return result, nil
}

func (s *Service) activeModel(ctx context.Context, db rebalancingdb.DBTX, portfolioID uuid.UUID) (*rebalancingdb.AllocationModel, error) {
	return first(s.q(db).ActiveModel(ctx, portfolioID))
}

// Overview is GET .../rebalancing/overview.
func (s *Service) Overview(ctx context.Context, portfolioID, userID uuid.UUID) (Overview, error) {
	ac, err := s.access(ctx, portfolioID, userID, false)
	if err != nil {
		return Overview{}, err
	}
	rec, err := s.activeModel(ctx, s.DB, portfolioID)
	if err != nil {
		return Overview{}, err
	}
	var model *AllocationModel
	if rec != nil {
		m, err := s.makeModel(ctx, s.DB, *rec)
		if err != nil {
			return Overview{}, err
		}
		model = &m
	}
	snap, err := s.valuation(ctx, portfolioID, ac.Portfolio.UserID, ac.Portfolio.BaseCurrency, model)
	if err != nil {
		return Overview{}, err
	}
	count, err := s.q(s.DB).CountOpenAlertsForPortfolioUser(ctx, rebalancingdb.CountOpenAlertsForPortfolioUserParams{PortfolioID: portfolioID, UserID: userID})
	if err != nil {
		return Overview{}, err
	}
	// Engine errors here are not mapped (Swift let them surface as 500s).
	return s.Engine.Overview(apijson.FormatUUID(portfolioID), model, snap, int(count))
}

// Simulate is POST .../rebalancing/simulate.
func (s *Service) Simulate(ctx context.Context, portfolioID, userID uuid.UUID, p SimulationRequest) (Simulation, error) {
	ac, err := s.access(ctx, portfolioID, userID, false)
	if err != nil {
		return Simulation{}, err
	}
	modelID, ok := portfolios.ParseSwiftUUID(p.ModelID)
	if !ok {
		return Simulation{}, errModelNotFound()
	}
	rec, err := first(s.q(s.DB).GetModelInPortfolio(ctx, rebalancingdb.GetModelInPortfolioParams{ID: modelID, PortfolioID: portfolioID}))
	if err != nil {
		return Simulation{}, err
	}
	if rec == nil {
		return Simulation{}, errModelNotFound()
	}
	model, err := s.makeModel(ctx, s.DB, *rec)
	if err != nil {
		return Simulation{}, err
	}
	snap, err := s.valuation(ctx, portfolioID, ac.Portfolio.UserID, ac.Portfolio.BaseCurrency, &model)
	if err != nil {
		return Simulation{}, err
	}
	if snap.PriceQuality == QualityIncomplete {
		return Simulation{}, httpx.Abort(http.StatusServiceUnavailable, "Simulation requires current prices and exchange rates for every target.")
	}
	sim, err := s.Engine.Simulate(apijson.FormatUUID(portfolioID), model, p, snap)
	if err != nil {
		return Simulation{}, mapEngineError(err)
	}
	return sim, nil
}

// CreatePlan is POST .../rebalancing/plans.
func (s *Service) CreatePlan(ctx context.Context, portfolioID, userID uuid.UUID, p PlanCreateRequest) (Plan, error) {
	if _, err := s.access(ctx, portfolioID, userID, true); err != nil {
		return Plan{}, err
	}
	sim, err := s.Simulate(ctx, portfolioID, userID, p.Simulation)
	if err != nil {
		return Plan{}, err
	}
	modelID, ok := portfolios.ParseSwiftUUID(sim.ModelID)
	if !ok {
		return Plan{}, httpx.Abort(http.StatusInternalServerError, "")
	}
	raw, err := json.Marshal(sim)
	if err != nil {
		return Plan{}, err
	}
	now := portfolios.Now()
	rec := rebalancingdb.RebalancePlan{
		ID: uuid.New(), PortfolioID: portfolioID, ModelID: modelID, CreatedByUserID: userID,
		ModelRevision: int64(sim.ModelRevision), Name: optionalNormalizedName(p.Name), Status: PlanDraft,
		BaseCurrency: sim.BaseCurrency, DriftBeforeBps: int64(sim.DriftBeforeBasisPoints),
		DriftAfterBps: int64(sim.DriftAfterBasisPoints), TotalValue: sim.TotalValueBefore,
		EstimatedFees: sim.EstimatedFees, EstimatedRealizedGainLoss: sim.EstimatedRealizedGainLoss,
		SimulationJson: string(raw), CreatedAt: &now, UpdatedAt: &now,
	}
	if err := s.q(s.DB).InsertPlan(ctx, rebalancingdb.InsertPlanParams{
		ID: rec.ID, PortfolioID: rec.PortfolioID, ModelID: rec.ModelID, CreatedByUserID: rec.CreatedByUserID,
		ModelRevision: rec.ModelRevision, Name: rec.Name, BaseCurrency: rec.BaseCurrency,
		DriftBeforeBps: rec.DriftBeforeBps, DriftAfterBps: rec.DriftAfterBps, TotalValue: rec.TotalValue,
		EstimatedFees: rec.EstimatedFees, EstimatedRealizedGainLoss: rec.EstimatedRealizedGainLoss,
		SimulationJson: rec.SimulationJson, Now: &now,
	}); err != nil {
		return Plan{}, err
	}
	return makePlan(rec)
}

// Plans is GET .../rebalancing/plans (newest 100).
func (s *Service) Plans(ctx context.Context, portfolioID, userID uuid.UUID) ([]Plan, error) {
	if _, err := s.access(ctx, portfolioID, userID, false); err != nil {
		return nil, err
	}
	records, err := s.q(s.DB).PlansForPortfolio(ctx, portfolioID)
	if err != nil {
		return nil, err
	}
	out := make([]Plan, 0, len(records))
	for i := range records {
		r := &records[i]
		p, err := makePlan(*r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// TransitionPlan is RebalancingService.transitionPlan.
func (s *Service) TransitionPlan(ctx context.Context, portfolioID, planID, userID uuid.UUID, status string, note *string) (Plan, error) {
	if _, err := s.access(ctx, portfolioID, userID, true); err != nil {
		return Plan{}, err
	}
	rec, err := first(s.q(s.DB).GetPlanInPortfolio(ctx, rebalancingdb.GetPlanInPortfolioParams{ID: planID, PortfolioID: portfolioID}))
	if err != nil {
		return Plan{}, err
	}
	if rec == nil {
		return Plan{}, errPlanNotFound()
	}
	if rec.Status != PlanDraft && rec.Status != PlanExported {
		return Plan{}, httpx.Conflict("Completed or cancelled plans are immutable.") //nolint:misspell // Vapor wire text
	}
	now := portfolios.Now()
	switch status {
	case PlanCompleted:
		rec.CompletedAt = &now
		rec.CompletionNote = optionalNormalizedName(note)
	case PlanCancelled:
		rec.CancelledAt = &now
	case PlanExported:
		rec.ExportedAt = &now
	default:
		return Plan{}, httpx.BadRequest("A plan cannot transition back to draft.")
	}
	rec.Status = status
	if err := s.q(s.DB).UpdatePlanTransition(ctx, rebalancingdb.UpdatePlanTransitionParams{
		ID: rec.ID, Status: rec.Status, CompletedAt: rec.CompletedAt, CompletionNote: rec.CompletionNote,
		CancelledAt: rec.CancelledAt, ExportedAt: rec.ExportedAt, Now: &now,
	}); err != nil {
		return Plan{}, err
	}
	return makePlan(*rec)
}

// swiftReference is Foundation's timeIntervalSinceReferenceDate for a
// timestamp read back through PostgresNIO, float operations included.
func swiftReference(micros int64) float64 {
	return -31622400.0 + float64(micros)/1e6
}

// History is GET .../rebalancing/history.
func (s *Service) History(ctx context.Context, portfolioID, userID uuid.UUID) (HistorySummary, error) {
	if _, err := s.access(ctx, portfolioID, userID, false); err != nil {
		return HistorySummary{}, err
	}
	records, err := s.q(s.DB).CompletedPlans(ctx, portfolioID)
	if err != nil {
		return HistorySummary{}, err
	}
	if len(records) == 0 {
		return HistorySummary{}, nil
	}
	before, after := int64(0), int64(0)
	var dates []int64 // microseconds since 2000-01-01, as Postgres stores them
	var last *string
	for i := range records {
		r := &records[i]
		before += r.DriftBeforeBps
		after += r.DriftAfterBps
		if r.CompletedAt != nil {
			dates = append(dates, r.CompletedAt.UnixMicro()-946684800000000)
			last = portfolios.FormatISODateTimePtr(r.CompletedAt)
		}
	}
	out := HistorySummary{
		CompletedCount:                len(records),
		AverageDriftBeforeBasisPoints: int(before / int64(len(records))),
		AverageDriftAfterBasisPoints:  int(after / int64(len(records))),
		LastCompletedAt:               last,
	}
	if len(dates) > 1 {
		sum := 0.0
		for i := 1; i < len(dates); i++ {
			sum += (swiftReference(dates[i]) - swiftReference(dates[i-1])) / 86400
		}
		avg := sum / float64(len(dates)-1)
		out.AverageDaysBetweenRebalances = &avg
	}
	return out, nil
}

// Alerts is GET /v1/rebalancing/alerts (not mounted here).
func (s *Service) Alerts(ctx context.Context, userID uuid.UUID) ([]Alert, error) {
	records, err := s.q(s.DB).AlertsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Alert, 0, len(records))
	for i := range records {
		r := &records[i]
		out = append(out, makeAlert(*r))
	}
	return out, nil
}

// Acknowledge is POST /v1/rebalancing/alerts/:alertId/acknowledge (not
// mounted here).
func (s *Service) Acknowledge(ctx context.Context, alertID, userID uuid.UUID) (Alert, error) {
	rec, err := first(s.q(s.DB).GetAlertForUser(ctx, rebalancingdb.GetAlertForUserParams{ID: alertID, UserID: userID}))
	if err != nil {
		return Alert{}, err
	}
	if rec == nil {
		return Alert{}, httpx.NotFound("Drift alert not found.")
	}
	if rec.Status == AlertResolved {
		return makeAlert(*rec), nil
	}
	now := portfolios.Now()
	if err := s.q(s.DB).AcknowledgeAlert(ctx, rebalancingdb.AcknowledgeAlertParams{ID: rec.ID, Now: &now}); err != nil {
		return Alert{}, err
	}
	rec.Status = AlertAcknowledged
	rec.AcknowledgedAt = &now
	return makeAlert(*rec), nil
}

// Preferences is GET .../rebalancing/preferences.
func (s *Service) Preferences(ctx context.Context, portfolioID, userID uuid.UUID) (NotificationPreferences, error) {
	if _, err := s.access(ctx, portfolioID, userID, false); err != nil {
		return NotificationPreferences{}, err
	}
	rec, err := first(s.q(s.DB).GetPreference(ctx, rebalancingdb.GetPreferenceParams{PortfolioID: portfolioID, UserID: userID}))
	if err != nil {
		return NotificationPreferences{}, err
	}
	return NotificationPreferences{PortfolioID: apijson.FormatUUID(portfolioID), PushEnabled: rec != nil && rec.PushEnabled}, nil
}

// UpdatePreferences is PUT .../rebalancing/preferences. Reading access is
// enough, as in Swift. Fluent's save after an assignment always writes, so
// an unchanged value still bumps updated_at.
func (s *Service) UpdatePreferences(ctx context.Context, portfolioID, userID uuid.UUID, p UpdatePreferencesRequest) (NotificationPreferences, error) {
	if _, err := s.access(ctx, portfolioID, userID, false); err != nil {
		return NotificationPreferences{}, err
	}
	q := s.q(s.DB)
	rec, err := first(q.GetPreference(ctx, rebalancingdb.GetPreferenceParams{PortfolioID: portfolioID, UserID: userID}))
	if err != nil {
		return NotificationPreferences{}, err
	}
	now := portfolios.Now()
	if rec != nil {
		err = q.UpdatePreference(ctx, rebalancingdb.UpdatePreferenceParams{ID: rec.ID, PushEnabled: p.PushEnabled, Now: &now})
	} else {
		err = q.InsertPreference(ctx, rebalancingdb.InsertPreferenceParams{
			ID: uuid.New(), PortfolioID: portfolioID, UserID: userID, PushEnabled: p.PushEnabled, Now: &now,
		})
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	return NotificationPreferences{PortfolioID: apijson.FormatUUID(portfolioID), PushEnabled: p.PushEnabled}, nil
}

// Dashboard is GET /v1/rebalancing/dashboard (not mounted here). Swift
// walked a Set of portfolio ids (random order); Go walks owned ids first,
// then memberships, deduplicated.
func (s *Service) Dashboard(ctx context.Context, userID uuid.UUID) (DashboardSummary, error) {
	q := s.q(s.DB)
	owned, err := q.OwnedOpenPortfolioIDs(ctx, userID)
	if err != nil {
		return DashboardSummary{}, err
	}
	member, err := q.ActiveMemberPortfolioIDs(ctx, userID)
	if err != nil {
		return DashboardSummary{}, err
	}
	seen := map[uuid.UUID]bool{}
	overviews := []Overview{}
	for _, id := range append(owned, member...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		active, err := s.activeModel(ctx, s.DB, id)
		if err != nil {
			return DashboardSummary{}, err
		}
		if active == nil {
			continue
		}
		if o, err := s.Overview(ctx, id, userID); err == nil {
			overviews = append(overviews, o)
		}
	}
	count, err := q.CountOpenAlertsForUser(ctx, userID)
	if err != nil {
		return DashboardSummary{}, err
	}
	breached := 0
	for i := range overviews {
		o := &overviews[i]
		if o.Severity == SeverityBreached {
			breached++
		}
	}
	return DashboardSummary{OpenAlertCount: int(count), BreachedPortfolioCount: breached, Portfolios: overviews}, nil
}

func (s *Service) replaceTargets(ctx context.Context, q *rebalancingdb.Queries, modelID uuid.UUID, buckets []AllocationTargetBucket) error {
	for bi, b := range buckets {
		bucketID := uuid.New()
		if err := q.InsertBucket(ctx, rebalancingdb.InsertBucketParams{
			ID: bucketID, ModelID: modelID, Name: NormalizedName(b.Name), TargetBps: int64(b.TargetBasisPoints),
			AlertThresholdBps: int64Ptr(b.AlertThresholdBasisPoints), SectorKey: optionalNormalizedName(b.SectorKey),
			SortOrder: int64(bi),
		}); err != nil {
			return err
		}
		for li, l := range b.Leaves {
			var symbol *string
			if l.Kind == KindSecurity {
				symbol = normalizedSymbol(l.Symbol)
			}
			if err := q.InsertLeaf(ctx, rebalancingdb.InsertLeafParams{
				ID: uuid.New(), ModelID: modelID, BucketID: bucketID, Kind: l.Kind, Symbol: symbol,
				Name: NormalizedName(l.Name), TargetBps: int64(l.TargetBasisPoints),
				AlertThresholdBps: int64Ptr(l.AlertThresholdBasisPoints), SortOrder: int64(li),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func int64Ptr(v *int) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

func intPtr(v *int64) *int {
	if v == nil {
		return nil
	}
	n := int(*v)
	return &n
}

func (s *Service) makeModel(ctx context.Context, db rebalancingdb.DBTX, r rebalancingdb.AllocationModel) (AllocationModel, error) {
	q := s.q(db)
	buckets, err := q.BucketsForModel(ctx, r.ID)
	if err != nil {
		return AllocationModel{}, err
	}
	leaves, err := q.LeavesForModel(ctx, r.ID)
	if err != nil {
		return AllocationModel{}, err
	}
	byBucket := map[uuid.UUID][]rebalancingdb.AllocationLeafe{}
	for _, l := range leaves {
		byBucket[l.BucketID] = append(byBucket[l.BucketID], l)
	}
	grouping := r.GroupingMode
	if grouping != GroupingHolding && grouping != GroupingSector && grouping != GroupingCustom {
		grouping = GroupingCustom
	}
	outBuckets := make([]AllocationTargetBucket, 0, len(buckets))
	for _, b := range buckets {
		ls := byBucket[b.ID]
		outLeaves := make([]AllocationTargetLeaf, 0, len(ls))
		for _, l := range ls {
			kind := l.Kind
			if kind != KindSecurity && kind != KindCash {
				kind = KindSecurity
			}
			outLeaves = append(outLeaves, AllocationTargetLeaf{
				ID: apijson.FormatUUID(l.ID), Kind: kind, Symbol: l.Symbol, Name: l.Name,
				TargetBasisPoints: int(l.TargetBps), AlertThresholdBasisPoints: intPtr(l.AlertThresholdBps),
				SortOrder: int(l.SortOrder),
			})
		}
		outBuckets = append(outBuckets, AllocationTargetBucket{
			ID: apijson.FormatUUID(b.ID), Name: b.Name, TargetBasisPoints: int(b.TargetBps),
			AlertThresholdBasisPoints: intPtr(b.AlertThresholdBps), SectorKey: b.SectorKey,
			SortOrder: int(b.SortOrder), Leaves: outLeaves,
		})
	}
	createdAt := ""
	if r.CreatedAt != nil {
		createdAt = portfolios.FormatISODateTime(*r.CreatedAt)
	}
	return AllocationModel{
		ID: apijson.FormatUUID(r.ID), PortfolioID: apijson.FormatUUID(r.PortfolioID), Name: r.Name,
		GroupingMode: grouping, IsActive: r.IsActive, Revision: int(r.Revision), BaseCurrency: r.BaseCurrency,
		DefaultTargetThresholdBasisPoints: int(r.DefaultTargetThresholdBps),
		TotalThresholdBasisPoints:         int(r.TotalThresholdBps),
		FractionalSharesEnabled:           r.FractionalSharesEnabled,
		QuantityIncrement:                 r.QuantityIncrement,
		MinimumTradeAmount:                r.MinimumTradeAmount,
		FlatFee:                           r.FlatFee,
		VariableFeeBasisPoints:            int(r.VariableFeeBps),
		Buckets:                           outBuckets,
		CreatedAt:                         createdAt,
		UpdatedAt:                         portfolios.FormatISODateTimePtr(r.UpdatedAt),
	}, nil
}

// valuation is RebalancingService.valuation.
func (s *Service) valuation(ctx context.Context, portfolioID, ownerID uuid.UUID, baseCurrency string, model *AllocationModel) (Snapshot, error) {
	q := s.q(s.DB)
	stocks, err := q.StocksForPortfolio(ctx, rebalancingdb.StocksForPortfolioParams{UserID: ownerID, PortfolioListID: portfolioID})
	if err != nil {
		return Snapshot{}, err
	}
	quantity := map[string]float64{}
	basis := map[string]float64{}
	for _, st := range stocks {
		sym := normalizedSymbol(&st.Symbol)
		if sym == nil {
			continue
		}
		quantity[*sym] += st.Shares
		basis[*sym] += st.Shares * st.BuyPrice
	}
	var targets []string
	if model != nil {
		for _, b := range model.Buckets {
			for _, l := range b.Leaves {
				if l.Kind == KindSecurity {
					if sym := normalizedSymbol(l.Symbol); sym != nil {
						targets = append(targets, *sym)
					}
				}
			}
		}
	}
	cash, err := s.cashBalances(ctx, portfolioID, ownerID)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Pricer.Snapshot(ctx, PricingInput{
		QuantityBySymbol: quantity, BasisBySymbol: basis, TargetSymbols: targets,
		CashByCurrency: cash, BaseCurrency: baseCurrency,
	}), nil
}

// cashBalances is RebalancingService.cashBalances: manual positions plus the
// latest balance per (account, currency), each floored at zero.
func (s *Service) cashBalances(ctx context.Context, portfolioID, userID uuid.UUID) (map[string]float64, error) {
	manual, err := s.q(s.DB).CashPositionsForPortfolio(ctx, portfolioID)
	if err != nil {
		return nil, err
	}
	totals := map[string]float64{}
	for _, m := range manual {
		totals[strings.ToUpper(m.Currency)] += max(0, m.Balance)
	}
	core := portfoliocoredb.New(s.DB)
	ids, err := core.AccountIDsForUserPortfolio(ctx, portfoliocoredb.AccountIDsForUserPortfolioParams{UserID: userID, PortfolioID: &portfolioID})
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return totals, nil
	}
	balances, err := core.CashBalancesForAccounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	latest := map[string]portfoliocoredb.CashBalance{}
	var order []string
	for _, b := range balances {
		key := b.AccountID.String() + ":" + strings.ToUpper(b.Currency)
		existing, ok := latest[key]
		if !ok {
			order = append(order, key)
		}
		if !ok || existing.AsOf.Before(b.AsOf) {
			latest[key] = b
		}
	}
	for _, k := range order {
		b := latest[k]
		totals[strings.ToUpper(b.Currency)] += max(0, b.Balance)
	}
	return totals, nil
}

func makePlan(r rebalancingdb.RebalancePlan) (Plan, error) {
	var sim Simulation
	if err := json.Unmarshal([]byte(r.SimulationJson), &sim); err != nil {
		return Plan{}, err
	}
	trades := sim.Trades
	if trades == nil {
		trades = []Trade{}
	}
	status := r.Status
	switch status {
	case PlanDraft, PlanExported, PlanCompleted, PlanCancelled:
	default:
		status = PlanDraft
	}
	createdAt := ""
	if r.CreatedAt != nil {
		createdAt = portfolios.FormatISODateTime(*r.CreatedAt)
	}
	return Plan{
		ID: apijson.FormatUUID(r.ID), PortfolioID: apijson.FormatUUID(r.PortfolioID),
		ModelID: apijson.FormatUUID(r.ModelID), ModelRevision: int(r.ModelRevision), Name: r.Name,
		Status: status, BaseCurrency: r.BaseCurrency,
		DriftBeforeBasisPoints: int(r.DriftBeforeBps), DriftAfterBasisPoints: int(r.DriftAfterBps),
		TotalValue: r.TotalValue, EstimatedFees: r.EstimatedFees, EstimatedRealizedGainLoss: r.EstimatedRealizedGainLoss,
		Trades: trades, CreatedAt: createdAt,
		ExportedAt:  portfolios.FormatISODateTimePtr(r.ExportedAt),
		CompletedAt: portfolios.FormatISODateTimePtr(r.CompletedAt),
		CancelledAt: portfolios.FormatISODateTimePtr(r.CancelledAt),
	}, nil
}

func makeAlert(r rebalancingdb.RebalancingAlert) Alert {
	status := r.Status
	switch status {
	case AlertOpen, AlertAcknowledged, AlertResolved:
	default:
		status = AlertOpen
	}
	return Alert{
		ID: apijson.FormatUUID(r.ID), PortfolioID: apijson.FormatUUID(r.PortfolioID),
		ModelID: apijson.FormatUUID(r.ModelID), ScopeID: r.ScopeID, ScopeName: r.ScopeName,
		DriftBasisPoints: int(r.DriftBps), ThresholdBasisPoints: int(r.ThresholdBps), Status: status,
		TriggeredAt:    portfolios.FormatISODateTime(r.TriggeredAt),
		AcknowledgedAt: portfolios.FormatISODateTimePtr(r.AcknowledgedAt),
		ResolvedAt:     portfolios.FormatISODateTimePtr(r.ResolvedAt),
	}
}
