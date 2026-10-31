package simulations

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/simulationsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaximumLegs is the per-simulation position cap.
const MaximumLegs = 250

// Service is DefaultPortfolioSimulationService.
type Service struct {
	DB     *pgxpool.Pool
	Access portfolios.Access
	Pricer rebalancing.Pricer
	Engine rebalancing.Engine
}

func (s *Service) q() *simulationsdb.Queries { return simulationsdb.New(s.DB) }

// checkSource is requireSourcePortfolio for clone mode. For fromScratch a
// source is optional, but one that parses is stored (FK to portfolio_lists),
// so it must name a portfolio the caller can read: Swift saved it unchecked
// and an unknown id was a 500.
func (s *Service) checkSource(ctx context.Context, p UpsertRequest, userID uuid.UUID) error {
	if p.Mode == ModeCloneCurrentPortfolio {
		return s.requireSourcePortfolio(ctx, p, userID)
	}
	id := sourceID(p)
	if id == nil {
		return nil
	}
	_, err := s.Access.Require(ctx, s.DB, *id, userID, portfolios.RequireOptions{})
	return err
}

func unprocessable(reason string) error { return httpx.Unprocessable(reason) }

// List is GET /v1/portfolio/simulations.
func (s *Service) List(ctx context.Context, userID uuid.UUID, limit int, cursor *string) (ListResponse, error) {
	pageSize := max(1, min(limit, 100))
	params := simulationsdb.ListSimulationsParams{UserID: userID, RowLimit: int32(pageSize + 1)}
	if cursor != nil {
		if at, id, ok := decodeCursor(*cursor); ok {
			params.AnchorCreatedAt = &at
			params.AnchorID = id
		}
	}
	records, err := s.q().ListSimulations(ctx, params)
	if err != nil {
		return ListResponse{}, err
	}
	page := records
	if len(page) > pageSize {
		page = page[:pageSize]
	}
	ids := make([]uuid.UUID, 0, len(page))
	for i := range page {
		ids = append(ids, page[i].ID)
	}
	byID := map[uuid.UUID][]simulationsdb.PortfolioSimulationLeg{}
	if len(ids) > 0 {
		legs, err := s.q().LegsForSimulations(ctx, ids)
		if err != nil {
			return ListResponse{}, err
		}
		for i := range legs {
			byID[legs[i].SimulationID] = append(byID[legs[i].SimulationID], legs[i])
		}
	}
	out := ListResponse{Items: make([]Simulation, 0, len(page))}
	for i := range page {
		out.Items = append(out.Items, dto(&page[i], byID[page[i].ID]))
	}
	if len(records) > pageSize && len(page) > 0 {
		out.NextCursor = encodeCursor(&page[len(page)-1])
	}
	return out, nil
}

// Detail is GET /v1/portfolio/simulations/:simulationId.
func (s *Service) Detail(ctx context.Context, simulationID, userID uuid.UUID) (Simulation, error) {
	rec, err := s.require(ctx, simulationID, userID)
	if err != nil {
		return Simulation{}, err
	}
	legs, err := s.q().LegsForSimulation(ctx, simulationID)
	if err != nil {
		return Simulation{}, err
	}
	return dto(rec, legs), nil
}

func orDefault[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func sourceID(p UpsertRequest) *uuid.UUID {
	if p.SourcePortfolioID == nil {
		return nil
	}
	if id, ok := portfolios.ParseSwiftUUID(*p.SourcePortfolioID); ok {
		return &id
	}
	return nil
}

// Create is POST /v1/portfolio/simulations. The record and its legs are
// written in one transaction (Swift saved the record first, so a failed leg
// left a simulation without positions).
func (s *Service) Create(ctx context.Context, userID uuid.UUID, p UpsertRequest) (Simulation, error) {
	legs, err := validate(p)
	if err != nil {
		return Simulation{}, err
	}
	if err := s.checkSource(ctx, p, userID); err != nil {
		return Simulation{}, err
	}
	now := portfolios.Now()
	rec := simulationsdb.PortfolioSimulation{
		ID: uuid.New(), UserID: userID, Mode: p.Mode, SourcePortfolioID: sourceID(p), Name: cleanName(p.Name),
		BaseCurrency: strings.ToUpper(p.BaseCurrency), TargetCapital: p.TargetCapital,
		FractionalSharesEnabled: p.FractionalSharesEnabled,
		QuantityIncrement:       orDefault(p.QuantityIncrement, 0.001),
		MinimumTradeAmount:      orDefault(p.MinimumTradeAmount, 1),
		FlatFee:                 orDefault(p.FlatFee, 0),
		VariableFeeBps:          int64(orDefault(p.VariableFeeBasisPoints, 0)),
		Revision:                1, ShareShowCapital: true, CreatedAt: &now, UpdatedAt: &now,
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := simulationsdb.New(tx)
		if err := q.InsertSimulation(ctx, simulationsdb.InsertSimulationParams{
			ID: rec.ID, UserID: userID, Mode: rec.Mode, SourcePortfolioID: rec.SourcePortfolioID, Name: rec.Name,
			BaseCurrency: rec.BaseCurrency, TargetCapital: rec.TargetCapital,
			FractionalSharesEnabled: rec.FractionalSharesEnabled, QuantityIncrement: rec.QuantityIncrement,
			MinimumTradeAmount: rec.MinimumTradeAmount, FlatFee: rec.FlatFee, VariableFeeBps: rec.VariableFeeBps,
			Now: &now,
		}); err != nil {
			return err
		}
		return replaceLegs(ctx, q, rec.ID, legs)
	})
	if err != nil {
		return Simulation{}, err
	}
	// Swift rendered the in-memory record (not a re-read) with fresh legs.
	return s.render(ctx, &rec)
}

func (s *Service) render(ctx context.Context, rec *simulationsdb.PortfolioSimulation) (Simulation, error) {
	legs, err := s.q().LegsForSimulation(ctx, rec.ID)
	if err != nil {
		return Simulation{}, err
	}
	return dto(rec, legs), nil
}

// Update is PUT /v1/portfolio/simulations/:simulationId. The record and its
// legs are written in one transaction (Swift's were not).
func (s *Service) Update(ctx context.Context, simulationID, userID uuid.UUID, p UpsertRequest) (Simulation, error) {
	legs, err := validate(p)
	if err != nil {
		return Simulation{}, err
	}
	rec, err := s.require(ctx, simulationID, userID)
	if err != nil {
		return Simulation{}, err
	}
	if p.ExpectedRevision != nil && int64(*p.ExpectedRevision) != rec.Revision {
		return Simulation{}, httpx.Conflict("This simulation was changed elsewhere. Reload and try again.")
	}
	if err := s.checkSource(ctx, p, userID); err != nil {
		return Simulation{}, err
	}
	now := portfolios.Now()
	rec.Mode = p.Mode
	rec.SourcePortfolioID = sourceID(p)
	rec.Name = cleanName(p.Name)
	rec.BaseCurrency = strings.ToUpper(p.BaseCurrency)
	rec.TargetCapital = p.TargetCapital
	rec.FractionalSharesEnabled = p.FractionalSharesEnabled
	rec.QuantityIncrement = orDefault(p.QuantityIncrement, rec.QuantityIncrement)
	rec.MinimumTradeAmount = orDefault(p.MinimumTradeAmount, rec.MinimumTradeAmount)
	rec.FlatFee = orDefault(p.FlatFee, rec.FlatFee)
	rec.VariableFeeBps = int64(orDefault(p.VariableFeeBasisPoints, int(rec.VariableFeeBps)))
	rec.Revision++
	rec.UpdatedAt = &now
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := simulationsdb.New(tx)
		if err := q.UpdateSimulation(ctx, simulationsdb.UpdateSimulationParams{
			ID: simulationID, Mode: rec.Mode, SourcePortfolioID: rec.SourcePortfolioID, Name: rec.Name,
			BaseCurrency: rec.BaseCurrency, TargetCapital: rec.TargetCapital,
			FractionalSharesEnabled: rec.FractionalSharesEnabled, QuantityIncrement: rec.QuantityIncrement,
			MinimumTradeAmount: rec.MinimumTradeAmount, FlatFee: rec.FlatFee, VariableFeeBps: rec.VariableFeeBps,
			Revision: rec.Revision, Now: &now,
		}); err != nil {
			return err
		}
		return replaceLegs(ctx, q, simulationID, legs)
	})
	if err != nil {
		return Simulation{}, err
	}
	return s.render(ctx, rec)
}

// Delete is DELETE /v1/portfolio/simulations/:simulationId (legs cascade).
func (s *Service) Delete(ctx context.Context, simulationID, userID uuid.UUID) error {
	if _, err := s.require(ctx, simulationID, userID); err != nil {
		return err
	}
	return s.q().DeleteSimulation(ctx, simulationID)
}

// Compute is POST /v1/portfolio/simulations/:simulationId/compute.
func (s *Service) Compute(ctx context.Context, simulationID, userID uuid.UUID, p ComputeRequest) (Result, error) {
	sim, err := s.Detail(ctx, simulationID, userID)
	if err != nil {
		return Result{}, err
	}
	if p.TargetCapitalOverride != nil {
		sim.TargetCapital = *p.TargetCapitalOverride
	}
	return s.run(ctx, sim, userID)
}

// Preview is POST /v1/portfolio/simulations/preview (never persisted).
func (s *Service) Preview(ctx context.Context, userID uuid.UUID, p UpsertRequest) (Result, error) {
	legs, err := validate(p)
	if err != nil {
		return Result{}, err
	}
	if p.Mode == ModeCloneCurrentPortfolio {
		if err := s.requireSourcePortfolio(ctx, p, userID); err != nil {
			return Result{}, err
		}
	}
	sim := Simulation{
		ID: apijson.FormatUUID(uuid.New()), Name: cleanName(p.Name), Mode: p.Mode,
		SourcePortfolioID: p.SourcePortfolioID, BaseCurrency: strings.ToUpper(p.BaseCurrency),
		TargetCapital: p.TargetCapital, FractionalSharesEnabled: p.FractionalSharesEnabled,
		QuantityIncrement:      orDefault(p.QuantityIncrement, 0.001),
		MinimumTradeAmount:     orDefault(p.MinimumTradeAmount, 1),
		FlatFee:                orDefault(p.FlatFee, 0),
		VariableFeeBasisPoints: orDefault(p.VariableFeeBasisPoints, 0),
		Revision:               1,
		Legs:                   make([]Leg, 0, len(legs)),
		ShareShowCapital:       true,
		CreatedAt:              portfolios.FormatISODateTime(portfolios.Now()),
	}
	for i, l := range legs {
		sim.Legs = append(sim.Legs, Leg{Symbol: l.Symbol, DisplayName: l.DisplayName, TargetBasisPoints: l.TargetBasisPoints, SortOrder: i})
	}
	return s.run(ctx, sim, userID)
}

func (s *Service) run(ctx context.Context, sim Simulation, userID uuid.UUID) (Result, error) {
	if math.IsInf(sim.TargetCapital, 0) || math.IsNaN(sim.TargetCapital) || sim.TargetCapital < 0 {
		return Result{}, unprocessable("Target capital must be a positive amount.")
	}
	model, err := MakeModel(sim)
	if err != nil {
		var me *ModelError
		if errors.As(err, &me) {
			return Result{}, abortFor(me)
		}
		return Result{}, err
	}
	input, err := s.pricingInput(ctx, sim, userID)
	if err != nil {
		return Result{}, err
	}
	snap := s.Pricer.Snapshot(ctx, input)
	if unpriced := rebalancing.UnpricedTargets(snap); len(unpriced) > 0 {
		return Result{}, unprocessable("No current price is available for " + strings.Join(unpriced, ", ") + ".")
	}
	portfolioID := sim.ID
	if sim.SourcePortfolioID != nil {
		portfolioID = *sim.SourcePortfolioID
	}
	// Swift let engine errors surface as 500s; v2 maps them as the
	// rebalancing simulate does.
	result, err := s.Engine.Simulate(portfolioID, model, MakeRequest(model, sim.TargetCapital), snap)
	if err != nil {
		return Result{}, rebalancing.MapEngineError(err)
	}
	spent := 0.0
	for i := range result.Trades {
		t := &result.Trades[i]
		if t.Side == rebalancing.SideBuy {
			spent += t.Notional
		} else {
			spent -= t.Notional
		}
	}
	cashNeeded := rebalancing.RoundedMoney(spent + result.EstimatedFees)
	leftover := rebalancing.RoundedMoney(max(0, sim.TargetCapital-cashNeeded))
	return Result{
		SimulationID: sim.ID, Revision: sim.Revision, Mode: sim.Mode,
		GeneratedAt: portfolios.FormatISODateTime(portfolios.Now()), Simulation: result,
		TotalCashNeeded: cashNeeded, LeftoverCash: leftover, CashBasisPoints: sim.CashBasisPoints(),
		Diff: diff(result.Before, result.After), Warnings: snap.Warnings,
	}, nil
}

func (s *Service) pricingInput(ctx context.Context, sim Simulation, userID uuid.UUID) (rebalancing.PricingInput, error) {
	targets := make([]string, 0, len(sim.Legs))
	for _, l := range sim.Legs {
		targets = append(targets, NormalizeSymbol(l.Symbol))
	}
	in := rebalancing.PricingInput{TargetSymbols: targets, BaseCurrency: sim.BaseCurrency}
	if sim.Mode != ModeCloneCurrentPortfolio || sim.SourcePortfolioID == nil {
		return in, nil
	}
	portfolioID, ok := portfolios.ParseSwiftUUID(*sim.SourcePortfolioID)
	if !ok {
		return in, nil
	}
	ac, err := s.Access.Require(ctx, s.DB, portfolioID, userID, portfolios.RequireOptions{})
	if err != nil {
		return in, err
	}
	stocks, err := s.q().StocksForPortfolio(ctx, simulationsdb.StocksForPortfolioParams{UserID: ac.Portfolio.UserID, PortfolioListID: portfolioID})
	if err != nil {
		return in, err
	}
	in.QuantityBySymbol = map[string]float64{}
	in.BasisBySymbol = map[string]float64{}
	for _, st := range stocks {
		sym := NormalizeSymbol(st.Symbol)
		if sym == "" {
			continue
		}
		in.QuantityBySymbol[sym] += st.Shares
		in.BasisBySymbol[sym] += st.Shares * st.BuyPrice
	}
	return in, nil
}

func (s *Service) require(ctx context.Context, simulationID, userID uuid.UUID) (*simulationsdb.PortfolioSimulation, error) {
	rows, err := s.q().GetSimulationForUser(ctx, simulationsdb.GetSimulationForUserParams{ID: simulationID, UserID: userID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, httpx.NotFound("Simulation not found.")
	}
	return &rows[0], nil
}

// requireSourcePortfolio 404s (never 403s) for a portfolio the caller cannot
// read, so it cannot probe which ids exist.
func (s *Service) requireSourcePortfolio(ctx context.Context, p UpsertRequest, userID uuid.UUID) error {
	if p.SourcePortfolioID == nil {
		return unprocessable("Cloning a portfolio requires a source portfolio.")
	}
	id, ok := portfolios.ParseSwiftUUID(*p.SourcePortfolioID)
	if !ok {
		return unprocessable("Cloning a portfolio requires a source portfolio.")
	}
	_, err := s.Access.Require(ctx, s.DB, id, userID, portfolios.RequireOptions{})
	return err
}

func replaceLegs(ctx context.Context, q *simulationsdb.Queries, simulationID uuid.UUID, legs []LegInput) error {
	if err := q.DeleteLegs(ctx, simulationID); err != nil {
		return err
	}
	for i, l := range legs {
		now := portfolios.Now()
		if err := q.InsertLeg(ctx, simulationsdb.InsertLegParams{
			ID: uuid.New(), SimulationID: simulationID, Symbol: NormalizeSymbol(l.Symbol),
			DisplayName: l.DisplayName, TargetBps: int64(l.TargetBasisPoints), SortOrder: int64(i), Now: &now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func dto(r *simulationsdb.PortfolioSimulation, legs []simulationsdb.PortfolioSimulationLeg) Simulation {
	mode := r.Mode
	if mode != ModeFromScratch && mode != ModeCloneCurrentPortfolio {
		mode = ModeFromScratch
	}
	var source *string
	if r.SourcePortfolioID != nil {
		v := apijson.FormatUUID(*r.SourcePortfolioID)
		source = &v
	}
	out := Simulation{
		ID: apijson.FormatUUID(r.ID), Name: r.Name, Mode: mode, SourcePortfolioID: source,
		BaseCurrency: r.BaseCurrency, TargetCapital: r.TargetCapital,
		FractionalSharesEnabled: r.FractionalSharesEnabled, QuantityIncrement: r.QuantityIncrement,
		MinimumTradeAmount: r.MinimumTradeAmount, FlatFee: r.FlatFee,
		VariableFeeBasisPoints: int(r.VariableFeeBps), Revision: int(r.Revision),
		Legs:         make([]Leg, 0, len(legs)),
		ShareEnabled: r.ShareEnabled, ShareShowCapital: r.ShareShowCapital,
		UpdatedAt: portfolios.FormatISODateTimePtr(r.UpdatedAt),
	}
	if r.ShareEnabled {
		out.ShareSlug = r.ShareSlug
	}
	if r.CreatedAt != nil {
		out.CreatedAt = portfolios.FormatISODateTime(*r.CreatedAt)
	}
	for i := range legs {
		l := &legs[i]
		out.Legs = append(out.Legs, Leg{Symbol: l.Symbol, DisplayName: l.DisplayName, TargetBasisPoints: int(l.TargetBps), SortOrder: int(l.SortOrder)})
	}
	return out
}

// cleanName is `String(trimmed.prefix(120))`.
func cleanName(raw string) string { return rebalancing.NormalizedName(raw) }

// validate is PortfolioSimulationService.validate.
func validate(p UpsertRequest) ([]LegInput, error) {
	if cleanName(p.Name) == "" {
		return nil, unprocessable("A simulation name is required.")
	}
	if math.IsInf(p.TargetCapital, 0) || math.IsNaN(p.TargetCapital) || p.TargetCapital < 0 {
		return nil, unprocessable("Target capital must be a positive amount.")
	}
	if len(p.Legs) == 0 {
		return nil, unprocessable("Add at least one position to simulate.")
	}
	if len(p.Legs) > MaximumLegs {
		return nil, unprocessable("A simulation can hold at most " + strconv.Itoa(MaximumLegs) + " positions.")
	}
	seen := map[string]bool{}
	total := 0
	for _, l := range p.Legs {
		symbol := NormalizeSymbol(l.Symbol)
		if !rebalancing.ValidSymbol(symbol) {
			return nil, unprocessable("\"" + l.Symbol + "\" is not a valid ticker.")
		}
		if seen[symbol] {
			return nil, unprocessable(symbol + " appears more than once.")
		}
		seen[symbol] = true
		if l.TargetBasisPoints <= 0 || l.TargetBasisPoints > 10000 {
			return nil, unprocessable(symbol + " needs a weight between 0.01% and 100%.")
		}
		total += l.TargetBasisPoints
	}
	if total > 10000 {
		return nil, unprocessable("Weights total more than 100%.")
	}
	return p.Legs, nil
}

// abortFor is PortfolioSimulationService.abort(for:).
func abortFor(e *ModelError) error {
	switch e.Case {
	case "weightsExceedTotal":
		return unprocessable("Weights total " + marketdata.SwiftDoubleString(float64(e.Total)/100) + "%, which is more than 100%.")
	case "noLegs":
		return unprocessable("Add at least one position to simulate.")
	case "duplicateSymbol":
		return unprocessable(e.Symbol + " appears more than once.")
	case "invalidSymbol":
		return unprocessable("\"" + e.Symbol + "\" is not a valid ticker.")
	case "invalidWeight":
		return unprocessable(e.Symbol + " needs a weight between 0.01% and 100%.")
	}
	return unprocessable("Target capital must be a positive amount.")
}

func flatten(rows []rebalancing.AllocationRow) []*rebalancing.AllocationRow {
	var out []*rebalancing.AllocationRow
	for i := range rows {
		out = append(out, &rows[i])
		out = append(out, flatten(rows[i].Children)...)
	}
	return out
}

func bySymbol(rows []rebalancing.AllocationRow) map[string]*rebalancing.AllocationRow {
	out := map[string]*rebalancing.AllocationRow{}
	for _, r := range flatten(rows) {
		if r.Symbol == nil {
			continue
		}
		if _, ok := out[*r.Symbol]; !ok {
			out[*r.Symbol] = r
		}
	}
	return out
}

func diff(before, after []rebalancing.AllocationRow) []DiffRow {
	b, a := bySymbol(before), bySymbol(after)
	keys := map[string]bool{}
	for k := range b {
		keys[k] = true
	}
	for k := range a {
		keys[k] = true
	}
	symbols := make([]string, 0, len(keys))
	for k := range keys {
		symbols = append(symbols, k)
	}
	sort.Strings(symbols)
	out := make([]DiffRow, 0, len(symbols))
	for _, sym := range symbols {
		row := DiffRow{Symbol: sym}
		if r := b[sym]; r != nil {
			row.CurrentBasisPoints = r.CurrentBasisPoints
			row.CurrentValue = r.CurrentValue
		}
		if r := a[sym]; r != nil {
			row.TargetBasisPoints = r.TargetBasisPoints
			row.TargetValue = r.CurrentValue
		}
		out = append(out, row)
	}
	return out
}

// encodeCursor is base64("\(createdAt.timeIntervalSince1970):\(id.uuidString)")
// with the Date PostgresNIO decoded (float operations included).
func encodeCursor(r *simulationsdb.PortfolioSimulation) *string {
	if r.CreatedAt == nil {
		return nil
	}
	micros := r.CreatedAt.UnixMicro() - 946684800000000
	reference := -31622400.0 + float64(micros)/1e6
	seconds := reference + 978307200.0
	text := marketdata.SwiftDoubleString(seconds) + ":" + apijson.FormatUUID(r.ID)
	out := base64.StdEncoding.EncodeToString([]byte(text))
	return &out
}

// decodeCursor parses a cursor and returns the anchor at the exact
// microsecond it was encoded from. The float seconds are a few tenths of a
// microsecond off; PostgresNIO (and the first port) truncated them, so an
// anchor could fall a microsecond early and the next page skipped the rows
// sharing its created_at.
func decodeCursor(raw string) (createdAt time.Time, id uuid.UUID, ok bool) {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || strings.ContainsAny(raw, "\r\n") {
		return time.Time{}, uuid.Nil, false
	}
	text := string(data)
	// split(separator: ":", maxSplits: 1) drops empty pieces.
	text = strings.TrimLeft(text, ":")
	head, tail, found := strings.Cut(text, ":")
	if !found || head == "" || tail == "" {
		return time.Time{}, uuid.Nil, false
	}
	seconds, err := strconv.ParseFloat(head, 64)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	id, ok = portfolios.ParseSwiftUUID(tail)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}, uuid.Nil, false
	}
	reference := seconds - 978307200.0
	micros := int64(math.Round((reference - -31622400.0) * 1e6))
	return time.UnixMicro(micros + 946684800000000).UTC(), id, true
}
