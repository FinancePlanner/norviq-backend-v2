package scenarios

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/scenariosdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// DefinitionModel is ScenarioDefinitionModel as Fluent encoded it.
type DefinitionModel struct {
	ID              apijson.UUID  `json:"id"`
	UserID          apijson.UUID  `json:"userId"`
	PortfolioListID apijson.UUID  `json:"portfolioListId"`
	FinancialGoalID *apijson.UUID `json:"financialGoalId"`
	Name            string        `json:"name"`
	Kind            string        `json:"kind"`
	Configuration   ScenarioJSON  `json:"configuration"`
	IsSaved         bool          `json:"isSaved"`
	CreatedAt       *apijson.Time `json:"createdAt"`
	UpdatedAt       *apijson.Time `json:"updatedAt"`
}

// parentRef is an unloaded Fluent @Parent: {"id": …}.
type parentRef struct {
	ID apijson.UUID `json:"id"`
}

// RunModel is ScenarioRunModel as Fluent encoded it.
type RunModel struct {
	ID                apijson.UUID  `json:"id"`
	UserID            apijson.UUID  `json:"userId"`
	Scenario          parentRef     `json:"scenario"`
	Snapshot          parentRef     `json:"snapshot"`
	State             string        `json:"state"`
	Progress          float64       `json:"progress"`
	Seed              string        `json:"seed"`
	DeduplicationHash string        `json:"deduplicationHash"`
	EngineVersion     string        `json:"engineVersion"`
	CatalogVersion    string        `json:"catalogVersion"`
	Result            *ScenarioJSON `json:"result"`
	ErrorMessage      *string       `json:"errorMessage"`
	LeaseOwner        *string       `json:"leaseOwner"`
	LeaseExpiresAt    *apijson.Time `json:"leaseExpiresAt"`
	CreatedAt         *apijson.Time `json:"createdAt"`
	StartedAt         *apijson.Time `json:"startedAt"`
	CompletedAt       *apijson.Time `json:"completedAt"`
	ExpiresAt         *apijson.Time `json:"expiresAt"`
}

// ComparisonPoint is ScenarioComparisonPoint.
type ComparisonPoint struct {
	ElapsedDays      float64 `json:"elapsedDays"`
	Value            float64 `json:"value"`
	PercentageChange float64 `json:"percentageChange"`
}

// ComparisonSeries is ScenarioComparisonSeries.
type ComparisonSeries struct {
	RunID  apijson.UUID      `json:"runId"`
	Points []ComparisonPoint `json:"points"`
}

// ComparisonResponse is ComparisonResponse.
type ComparisonResponse struct {
	Runs   []RunModel         `json:"runs"`
	Series []ComparisonSeries `json:"series"`
}

// scenarioInput is the controller's private ScenarioInput.
type scenarioInput struct {
	PortfolioListID apijson.UUID  `json:"portfolioListId"`
	FinancialGoalID *apijson.UUID `json:"financialGoalId"`
	Name            string        `json:"name"`
	Kind            string        `json:"kind"`
	Configuration   ScenarioJSON  `json:"configuration"`
	IsSaved         *bool         `json:"isSaved"`
}

// runInput is the controller's private RunInput.
type runInput struct {
	SnapshotID apijson.UUID `json:"snapshotId"`
	Seed       *uint64      `json:"seed"`
}

// compareInput is the controller's private CompareInput.
type compareInput struct {
	RunIDs []apijson.UUID `json:"runIds"`
}

// ResultCacheKey is the Redis key the worker caches a completed run's
// result under (ScenarioRunWorker.cache / getResult).
func ResultCacheKey(runID uuid.UUID) string {
	return "scenario:result:" + strings.ToLower(runID.String())
}

func definitionModel(row scenariosdb.ScenarioDefinition) (DefinitionModel, error) {
	cfg, err := ParseScenarioJSON(row.Configuration)
	if err != nil {
		return DefinitionModel{}, err
	}
	return DefinitionModel{
		ID: apijson.UUID(row.ID), UserID: apijson.UUID(row.UserID), PortfolioListID: apijson.UUID(row.PortfolioListID),
		FinancialGoalID: apijson.UUIDPtr(row.FinancialGoalID), Name: row.Name, Kind: row.Kind, Configuration: cfg,
		IsSaved: row.IsSaved, CreatedAt: apijson.TimePtr(row.CreatedAt), UpdatedAt: apijson.TimePtr(row.UpdatedAt),
	}, nil
}

func runModel(row scenariosdb.ScenarioRun) (RunModel, error) {
	m := RunModel{
		ID: apijson.UUID(row.ID), UserID: apijson.UUID(row.UserID),
		Scenario: parentRef{apijson.UUID(row.ScenarioID)}, Snapshot: parentRef{apijson.UUID(row.SnapshotID)},
		State: row.State, Progress: row.Progress, Seed: row.Seed, DeduplicationHash: row.DeduplicationHash,
		EngineVersion: row.EngineVersion, CatalogVersion: row.CatalogVersion,
		ErrorMessage: row.ErrorMessage, LeaseOwner: row.LeaseOwner, LeaseExpiresAt: apijson.TimePtr(row.LeaseExpiresAt),
		CreatedAt: apijson.TimePtr(row.CreatedAt), StartedAt: apijson.TimePtr(row.StartedAt),
		CompletedAt: apijson.TimePtr(row.CompletedAt), ExpiresAt: apijson.TimePtr(row.ExpiresAt),
	}
	if result, ok, err := runResult(row); err != nil {
		return RunModel{}, err
	} else if ok {
		m.Result = &result
	}
	return m, nil
}

// runResult decodes the nullable result column.
func runResult(row scenariosdb.ScenarioRun) (ScenarioJSON, bool, error) {
	if len(row.Result) == 0 || string(row.Result) == "null" {
		return ScenarioJSON{}, false, nil
	}
	r, err := ParseScenarioJSON(row.Result)
	if err != nil {
		return ScenarioJSON{}, false, err
	}
	return r, true, nil
}

func (h *handler) requireID(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return uuid.Nil, httpx.BadRequest("Invalid id")
	}
	return id, nil
}

func (h *handler) ownedScenario(c *echo.Context, userID uuid.UUID) (scenariosdb.ScenarioDefinition, error) {
	id, err := h.requireID(c)
	if err != nil {
		return scenariosdb.ScenarioDefinition{}, err
	}
	row, err := h.q.GetScenarioDefinition(c.Request().Context(), scenariosdb.GetScenarioDefinitionParams{UserID: userID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, httpx.NotFound("")
	}
	return row, err
}

func (h *handler) ownedRun(c *echo.Context, userID uuid.UUID) (scenariosdb.ScenarioRun, error) {
	id, err := h.requireID(c)
	if err != nil {
		return scenariosdb.ScenarioRun{}, err
	}
	row, err := h.q.GetScenarioRun(c.Request().Context(), scenariosdb.GetScenarioRunParams{UserID: userID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, httpx.NotFound("")
	}
	return row, err
}

func (h *handler) catalog(c *echo.Context) error {
	if _, err := h.authorize(c); err != nil {
		return err
	}
	return httpx.OK(c, Catalog())
}

func (h *handler) listScenarios(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListScenarioDefinitions(c.Request().Context(), userID)
	if err != nil {
		return err
	}
	out := make([]DefinitionModel, 0, len(rows))
	for i := range rows {
		m, err := definitionModel(rows[i])
		if err != nil {
			return err
		}
		out = append(out, m)
	}
	return httpx.OK(c, out)
}

func (h *handler) getScenario(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	row, err := h.ownedScenario(c, userID)
	if err != nil {
		return err
	}
	m, err := definitionModel(row)
	if err != nil {
		return err
	}
	return httpx.OK(c, m)
}

func (h *handler) createScenario(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	return h.persistScenario(c, userID, nil)
}

func (h *handler) updateScenario(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	existing, err := h.ownedScenario(c, userID)
	if err != nil {
		return err
	}
	return h.persistScenario(c, userID, &existing)
}

func (h *handler) deleteScenario(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	row, err := h.ownedScenario(c, userID)
	if err != nil {
		return err
	}
	if err := h.q.DeleteScenarioDefinition(c.Request().Context(), row.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *handler) persistScenario(c *echo.Context, userID uuid.UUID, existing *scenariosdb.ScenarioDefinition) error {
	var input scenarioInput
	if err := portfolios.DecodeSwiftBody(c, &input); err != nil {
		return err
	}
	if err := validateScenario(input); err != nil {
		return err
	}
	ctx := c.Request().Context()
	listID := input.PortfolioListID.Std()
	owned, err := h.q.OwnedPortfolioListExists(ctx, scenariosdb.OwnedPortfolioListExistsParams{ID: listID, UserID: userID})
	if err != nil {
		return err
	}
	if !owned {
		return httpx.NotFound("")
	}
	var goalID *uuid.UUID
	if input.FinancialGoalID != nil {
		id := input.FinancialGoalID.Std()
		goalID = &id
		goal, err := h.q.GetOwnedFinancialGoal(ctx, scenariosdb.GetOwnedFinancialGoalParams{UserID: userID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("Financial goal not found")
		}
		if err != nil {
			return err
		}
		if goal.PortfolioListID != listID {
			return httpx.Abort(http.StatusUnprocessableEntity, "Financial goal and scenario must use the same portfolio")
		}
	}
	isSaved := true
	if input.IsSaved != nil {
		isSaved = *input.IsSaved
	}
	configuration, err := apijson.Marshal(input.Configuration)
	if err != nil {
		return err
	}
	now := h.now()
	row := scenariosdb.ScenarioDefinition{
		UserID: userID, PortfolioListID: listID, FinancialGoalID: goalID, Name: input.Name, Kind: input.Kind,
		Configuration: configuration, IsSaved: isSaved, UpdatedAt: &now,
	}
	if existing == nil {
		row.ID = uuid.New()
		row.CreatedAt = &now
		err = h.q.InsertScenarioDefinition(ctx, scenariosdb.InsertScenarioDefinitionParams{
			ID: row.ID, UserID: userID, PortfolioListID: listID, FinancialGoalID: goalID, Name: input.Name,
			Kind: input.Kind, Configuration: configuration, IsSaved: isSaved, CreatedAt: &now, UpdatedAt: &now,
		})
	} else {
		row.ID = existing.ID
		row.UserID = existing.UserID
		row.CreatedAt = existing.CreatedAt
		err = h.q.UpdateScenarioDefinition(ctx, scenariosdb.UpdateScenarioDefinitionParams{
			ID: existing.ID, PortfolioListID: listID, FinancialGoalID: goalID, Name: input.Name, Kind: input.Kind,
			Configuration: configuration, IsSaved: isSaved, UpdatedAt: &now,
		})
	}
	if err != nil {
		return err
	}
	return httpx.OK(c, DefinitionModel{
		ID: apijson.UUID(row.ID), UserID: apijson.UUID(row.UserID), PortfolioListID: apijson.UUID(listID),
		FinancialGoalID: apijson.UUIDPtr(goalID), Name: input.Name, Kind: input.Kind, Configuration: input.Configuration,
		IsSaved: isSaved, CreatedAt: apijson.TimePtr(row.CreatedAt), UpdatedAt: apijson.TimePtr(&now),
	})
}

func inClosed(v, lo, hi float64) bool { return lo <= v && v <= hi }

func isWholeNumber(v float64) bool { return isFinite(v) && math.Round(v) == v }

// validateScenario is ScenarioController.validateScenario.
func validateScenario(input scenarioInput) error {
	if (input.Kind != "historical" && input.Kind != "custom" && input.Kind != "monte_carlo") ||
		portfolios.TrimSwift(input.Name) == "" {
		return httpx.BadRequest("Invalid scenario")
	}
	values := input.Configuration.Values
	switch input.Kind {
	case "historical":
		catalogID, ok := jsonString(values["catalogId"])
		if !ok {
			catalogID, ok = jsonString(values["catalog_id"])
		}
		supported := false
		for _, p := range Catalog().HistoricalScenarios {
			supported = supported || p.ID == catalogID
		}
		if !ok || !supported {
			return httpx.BadRequest("Unknown historical scenario")
		}
	case "custom":
		months := numberOr(values["horizon_months"], 1)
		rateShift := numberOr(values["parallel_rate_shift_bps"], 0)
		volatility := numberOr(values["volatility_multiplier"], 1)
		recovery := stringOr(values["recovery"], "none")
		if !isWholeNumber(months) || !inClosed(months, 1, 120) ||
			!isFinite(rateShift) || !inClosed(rateShift, -5000, 5000) ||
			!isFinite(volatility) || !inClosed(volatility, 0, 10) ||
			(recovery != "none" && recovery != "linear" && recovery != "mean_reverting") ||
			!validShockList(values, "holding_shocks") || !validShockList(values, "sector_shocks") ||
			!validShockList(values, "region_shocks") || !validShockList(values, "currency_shocks") ||
			!validShockList(values, "asset_class_shocks") {
			return httpx.BadRequest("Invalid custom scenario configuration")
		}
	case "monte_carlo":
		paths := numberOr(values["path_count"], 10000)
		months := numberOr(values["horizon_months"], 360)
		degrees := numberOr(values["degrees_of_freedom"], 5)
		block := numberOr(values["bootstrap_block_months"], 6)
		distribution := stringOr(values["distribution"], "block_bootstrap")
		if !isWholeNumber(paths) || !inClosed(paths, 1, 50000) ||
			!isWholeNumber(months) || !inClosed(months, 1, 600) ||
			!isFinite(degrees) || !(degrees > 2) || !(degrees <= 100) ||
			!isWholeNumber(block) || !inClosed(block, 1, 120) ||
			(distribution != "block_bootstrap" && distribution != "normal" && distribution != "student_t") ||
			!validMonteCarloAssumptions(values) {
			return httpx.BadRequest("Invalid Monte Carlo configuration")
		}
	}
	return nil
}

func validShockList(values map[string]any, key string) bool {
	v, present := values[key]
	if !present {
		return true
	}
	shocks, ok := jsonArray(v)
	if !ok {
		return false
	}
	for _, item := range shocks {
		o, ok := jsonObject(item)
		if !ok {
			return false
		}
		target, ok := jsonString(o["target"])
		if !ok {
			return false
		}
		percentage, ok := jsonNumber(o["percentage"])
		if !ok {
			return false
		}
		if portfolios.TrimSwift(target) == "" || !isFinite(percentage) || !inClosed(percentage, -1, 10) {
			return false
		}
	}
	return true
}

func validMonteCarloAssumptions(values map[string]any) bool {
	weights := numbers(values["asset_weights"])
	returns := numbers(values["asset_annual_returns"])
	covariance := numberRows(values["annual_covariance"])
	if len(weights) == 0 && len(returns) == 0 && len(covariance) == 0 {
		return true
	}
	count := len(weights)
	if count < 2 || count > 50 || len(returns) != count || len(covariance) != count {
		return false
	}
	for _, row := range covariance {
		if len(row) != count {
			return false
		}
	}
	sum := 0.0
	for _, w := range weights {
		if !isFinite(w) || !inClosed(w, 0, 1) {
			return false
		}
		sum += w
	}
	if !(math.Abs(sum-1) <= 0.001) {
		return false
	}
	for _, r := range returns {
		if !isFinite(r) || !inClosed(r, -1, 10) {
			return false
		}
	}
	for row := range count {
		for _, v := range covariance[row] {
			if !isFinite(v) {
				return false
			}
		}
		if !(covariance[row][row] >= 0) {
			return false
		}
		for column := range count {
			if math.Abs(covariance[row][column]-covariance[column][row]) > 0.000_001 {
				return false
			}
		}
	}
	return true
}

func (h *handler) listRuns(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListScenarioRuns(c.Request().Context(), userID)
	if err != nil {
		return err
	}
	out := make([]RunModel, 0, len(rows))
	for i := range rows {
		m, err := runModel(rows[i])
		if err != nil {
			return err
		}
		out = append(out, m)
	}
	return httpx.OK(c, out)
}

func (h *handler) getRun(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	row, err := h.ownedRun(c, userID)
	if err != nil {
		return err
	}
	m, err := runModel(row)
	if err != nil {
		return err
	}
	return httpx.OK(c, m)
}

func (h *handler) createRun(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	scenario, err := h.ownedScenario(c, userID)
	if err != nil {
		return err
	}
	var input runInput
	if err := portfolios.DecodeSwiftBody(c, &input); err != nil {
		return err
	}
	ctx := c.Request().Context()
	snapshot, err := h.q.GetScenarioSnapshot(ctx, scenariosdb.GetScenarioSnapshotParams{UserID: userID, ID: input.SnapshotID.Std()})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if snapshot.PortfolioListID != scenario.PortfolioListID {
		return httpx.Abort(http.StatusUnprocessableEntity, "Snapshot and scenario must use the same portfolio")
	}
	var seed uint64
	if input.Seed != nil {
		seed = *input.Seed
	} else {
		seed = rand.Uint64() //nolint:gosec // Vapor's UInt64.random: a simulation seed, not a secret.
	}
	configuration, err := ParseScenarioJSON(scenario.Configuration)
	if err != nil {
		return err
	}
	payload, err := ParseScenarioJSON(snapshot.Payload)
	if err != nil {
		return err
	}
	warnings, err := ParseScenarioJSON(snapshot.Warnings)
	if err != nil {
		return err
	}
	hash := DeduplicationHash(configuration.Values, payload.Values, warnings.Values, seed)
	existing, err := h.q.FindCompletedScenarioRunByHash(ctx, scenariosdb.FindCompletedScenarioRunByHashParams{UserID: userID, DeduplicationHash: hash})
	switch {
	case err == nil:
		m, err := runModel(existing)
		if err != nil {
			return err
		}
		return httpx.OK(c, m)
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	now := h.now()
	var expiry *time.Time
	if !scenario.IsSaved {
		e := now.Add(90 * 86400 * time.Second)
		expiry = &e
	}
	run := scenariosdb.ScenarioRun{
		ID: uuid.New(), UserID: userID, ScenarioID: scenario.ID, SnapshotID: snapshot.ID, State: "queued",
		Progress: 0, Seed: strconv.FormatUint(seed, 10), DeduplicationHash: hash, EngineVersion: EngineVersion,
		CatalogVersion: CatalogVersion, CreatedAt: &now, ExpiresAt: expiry,
	}
	if err := h.q.InsertScenarioRun(ctx, scenariosdb.InsertScenarioRunParams{
		ID: run.ID, UserID: run.UserID, ScenarioID: run.ScenarioID, SnapshotID: run.SnapshotID, State: run.State,
		Progress: run.Progress, Seed: run.Seed, DeduplicationHash: run.DeduplicationHash, EngineVersion: run.EngineVersion,
		CatalogVersion: run.CatalogVersion, CreatedAt: run.CreatedAt, ExpiresAt: run.ExpiresAt,
	}); err != nil {
		return err
	}
	m, err := runModel(run)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusAccepted, m)
}

func (h *handler) cancelRun(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	run, err := h.ownedRun(c, userID)
	if err != nil {
		return err
	}
	if run.State != "queued" && run.State != "running" {
		return httpx.Abort(http.StatusConflict, "Run is already terminal")
	}
	if err := h.q.CancelScenarioRun(c.Request().Context(), scenariosdb.CancelScenarioRunParams{ID: run.ID, CompletedAt: ptr(h.now())}); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *handler) getResult(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	run, err := h.ownedRun(c, userID)
	if err != nil {
		return err
	}
	if run.State != "completed" {
		return httpx.Abort(http.StatusConflict, "Result is not ready")
	}
	if cached, ok := h.cachedResult(c.Request().Context(), run.ID); ok {
		return httpx.OK(c, cached)
	}
	result, ok, err := runResult(run)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.Abort(http.StatusConflict, "Result is not ready")
	}
	return httpx.OK(c, result)
}

// cachedResult is getResult's Redis read; every failure falls through to
// the database, as Swift's `try?` did.
func (h *handler) cachedResult(ctx context.Context, runID uuid.UUID) (ScenarioJSON, bool) {
	if h.d.Redis == nil {
		return ScenarioJSON{}, false
	}
	value, err := h.d.Redis.Get(ctx, ResultCacheKey(runID)).Result()
	if err != nil {
		return ScenarioJSON{}, false
	}
	cached, err := ParseScenarioJSON([]byte(value))
	if err != nil {
		return ScenarioJSON{}, false
	}
	return cached, true
}

func (h *handler) compareRuns(c *echo.Context) error {
	userID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var input compareInput
	if err := portfolios.DecodeSwiftBody(c, &input); err != nil {
		return err
	}
	if len(input.RunIDs) < 1 || len(input.RunIDs) > 4 {
		return httpx.BadRequest("Compare between one and four runs")
	}
	ids := make([]uuid.UUID, len(input.RunIDs))
	unique := map[uuid.UUID]bool{}
	for i, id := range input.RunIDs {
		ids[i] = id.Std()
		unique[id.Std()] = true
	}
	fetched, err := h.q.ListScenarioRunsByIDs(c.Request().Context(), scenariosdb.ListScenarioRunsByIDsParams{UserID: userID, Ids: ids})
	if err != nil {
		return err
	}
	if len(fetched) != len(unique) {
		return httpx.NotFound("")
	}
	byID := make(map[uuid.UUID]scenariosdb.ScenarioRun, len(fetched))
	for i := range fetched {
		byID[fetched[i].ID] = fetched[i]
	}
	runs := make([]RunModel, 0, len(ids))
	results := make([]ScenarioJSON, 0, len(ids))
	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			continue
		}
		m, err := runModel(row)
		if err != nil {
			return err
		}
		if row.State != "completed" || m.Result == nil {
			return httpx.Abort(http.StatusConflict, "Only completed runs can be compared")
		}
		runs = append(runs, m)
		results = append(results, *m.Result)
	}
	series := make([]ComparisonSeries, len(runs))
	for i := range runs {
		series[i] = normalizedComparisonSeries(runs[i].ID, results[i].Values)
	}
	return httpx.OK(c, ComparisonResponse{Runs: runs, Series: series})
}

// normalizedComparisonSeries is the controller's private helper: the
// timeline (or the percentile bands' p50) in elapsed days.
func normalizedComparisonSeries(id apijson.UUID, result map[string]any) ComparisonSeries {
	source := objects(result["timeline"])
	valueKey := "value"
	if len(source) == 0 {
		source = objects(result["percentile_bands"])
		valueKey = "p50"
	}
	type point struct{ days, value float64 }
	raw := make([]point, 0, len(source))
	for _, p := range source {
		value, ok := jsonNumber(p[valueKey])
		if !ok {
			continue
		}
		if days, ok := jsonNumber(p["elapsed_days"]); ok {
			raw = append(raw, point{days, value})
			continue
		}
		months, ok := jsonNumber(p["elapsed_months"])
		if !ok {
			continue
		}
		raw = append(raw, point{months * 30.4375, value})
	}
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].days < raw[j].days })
	points := make([]ComparisonPoint, 0, len(raw))
	if len(raw) == 0 {
		return ComparisonSeries{RunID: id, Points: points}
	}
	initial := raw[0].value
	for _, p := range raw {
		change := 0.0
		if initial != 0 {
			change = p.value/initial - 1
		}
		points = append(points, ComparisonPoint{ElapsedDays: p.days, Value: p.value, PercentageChange: change})
	}
	return ComparisonSeries{RunID: id, Points: points}
}

// stateCanceled is the scenario_runs.state value of a canceled run.
const stateCanceled = "cancelled" //nolint:misspell // database and wire value
