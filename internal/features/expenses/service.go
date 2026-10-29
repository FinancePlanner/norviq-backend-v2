package expenses

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/activity"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/server"
	"norviq-api/internal/store/activitydb"
	"norviq-api/internal/store/badgesdb"
	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DriftEvaluator is BudgetDriftEvaluator(req:).evaluate(userId:monthStart:notify:),
// which lives with the budgeting engine. Expenses only call it best-effort.
type DriftEvaluator interface {
	Evaluate(ctx context.Context, userID uuid.UUID, monthStart time.Time, notify bool) error
}

// Service is DefaultExpensesService. Every method that took `on db` in
// Swift takes db here, so callers may pass a pgx.Tx; side effects Vapor ran
// against req.db (drift evaluation) use the pool.
type Service struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	users  *users.Store
	drift  atomic.Pointer[DriftEvaluator]
	now    func() time.Time
}

var shared sync.Map // *server.Deps -> *Service

// SharedService returns the process's expenses service for d, building it
// once (USER_PII_ENCRYPTION_* from the environment, as identity reads them).
// Other domains (spreadsheet/receipt import, banking, dashboards) should take
// it from here so they share the drift evaluator the feature wires in.
func SharedService(d *server.Deps) (*Service, error) {
	if v, ok := shared.Load(d); ok {
		return v.(*Service), nil
	}
	pii, err := crypto.PIIFromEnv(d.Config.IsProduction(), d.Logger)
	if err != nil {
		return nil, err
	}
	v, _ := shared.LoadOrStore(d, NewService(d.DB, d.Logger, pii))
	return v.(*Service), nil
}

// NewService builds a service. Prefer SharedService outside tests.
func NewService(pool *pgxpool.Pool, logger *slog.Logger, pii *crypto.PII) *Service {
	return &Service{pool: pool, logger: logger, users: users.NewStore(pool, pii), now: time.Now}
}

// SetDriftEvaluator wires the budgeting engine's drift evaluator. Nil
// disables drift evaluation.
func (s *Service) SetDriftEvaluator(e DriftEvaluator) {
	if e == nil {
		s.drift.Store(nil)
		return
	}
	s.drift.Store(&e)
}

// WithClock overrides the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Pool is the pool the service was built on (Vapor's req.db).
func (s *Service) Pool() *pgxpool.Pool { return s.pool }

func q(db expensesdb.DBTX) *expensesdb.Queries { return expensesdb.New(db) }

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func symbolFor(p BudgetPillar) string {
	switch p {
	case PillarFundamentals:
		return "house.fill"
	case PillarFutureYou:
		return "chart.line.uptrend.xyaxis"
	case PillarFun:
		return "sparkles"
	}
	return "square.stack.3d.up.fill"
}

// DefaultTargetShare is defaultTargetShare(for:).
func DefaultTargetShare(p BudgetPillar) float64 {
	switch p {
	case PillarFundamentals:
		return 0.50
	case PillarFutureYou:
		return 0.20
	case PillarFun:
		return 0.30
	}
	return 0
}

func normalizeBudgetKey(v string) string {
	return strings.ToLower(strings.TrimFunc(v, unicode.IsSpace))
}

func normalizePartnerName(v *string) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimFunc(*v, unicode.IsSpace)
	if t == "" {
		return nil
	}
	return &t
}

func normalizeSplit(mode string, share float64) (string, float64, error) {
	if !(share >= 0 && share <= 100) {
		return "", 0, httpx.BadRequest("userSharePercent must be between 0 and 100.")
	}
	if mode == SplitShared {
		return SplitShared, share, nil
	}
	return SplitPersonal, 100, nil
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func validateBudgetSettings(currency *string, category, total *float64) error {
	if currency != nil {
		ok := utf8.RuneCountInString(*currency) == 3
		for _, r := range *currency {
			if !unicode.IsLetter(r) {
				ok = false
			}
		}
		if !ok {
			return httpx.BadRequest("currencyCode must be a three-letter ISO 4217 code.")
		}
	}
	for _, v := range []*float64{category, total} {
		if v != nil && !(isFinite(*v) && *v >= 0 && *v <= 1000) {
			return httpx.BadRequest("Drift thresholds must be between 0 and 1000.")
		}
	}
	return nil
}

func userPortion(amount float64, mode expensesdb.ExpenseSplitMode, share float64) float64 {
	if mode == expensesdb.ExpenseSplitModeShared {
		return amount * (share / 100)
	}
	return amount
}

func partnerPortion(amount float64, mode expensesdb.ExpenseSplitMode, share float64) float64 {
	return amount - userPortion(amount, mode, share)
}

// parseUUIDString is Swift's UUID(uuidString:): canonical 8-4-4-4-12 hex,
// any case.
func parseUUIDString(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.UUID{}, false
	}
	u, err := uuid.Parse(s)
	return u, err == nil
}

func uuidPtrFromString(s *string) *uuid.UUID {
	if s == nil {
		return nil
	}
	u, ok := parseUUIDString(*s)
	if !ok {
		return nil
	}
	return &u
}

func formatUUIDPtr(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := apijson.FormatUUID(*u)
	return &s
}

// --- Household partner ------------------------------------------------------

func (s *Service) requireUser(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID) (*users.User, error) {
	u, err := s.users.ByID(ctx, db, userID)
	if isNoRows(err) || (err == nil && u == nil) {
		return nil, httpx.NotFound("User not found.")
	}
	return u, err
}

// GetHouseholdPartner is getHouseholdPartner.
func (s *Service) GetHouseholdPartner(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID) (HouseholdPartnerProfileResponse, error) {
	u, err := s.requireUser(ctx, db, userID)
	if err != nil {
		return HouseholdPartnerProfileResponse{}, err
	}
	return HouseholdPartnerProfileResponse{
		DisplayName:             normalizePartnerName(u.HouseholdPartnerDisplayName),
		DefaultUserSharePercent: u.HouseholdDefaultUserSharePercent,
	}, nil
}

// UpdateHouseholdPartner is updateHouseholdPartner.
func (s *Service) UpdateHouseholdPartner(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, req HouseholdPartnerProfileRequest) (HouseholdPartnerProfileResponse, error) {
	u, err := s.requireUser(ctx, db, userID)
	if err != nil {
		return HouseholdPartnerProfileResponse{}, err
	}
	u.HouseholdPartnerDisplayName = normalizePartnerName(req.DisplayName)
	share := req.DefaultUserSharePercent
	if share != nil && !(*share >= 0 && *share <= 100) {
		return HouseholdPartnerProfileResponse{}, httpx.BadRequest("defaultUserSharePercent must be between 0 and 100.")
	}
	now := s.now()
	if err := s.users.RewritePII(ctx, db, u, now); err != nil {
		return HouseholdPartnerProfileResponse{}, err
	}
	if err := q(db).SetHouseholdDefaultShare(ctx, expensesdb.SetHouseholdDefaultShareParams{
		ID: userID, HouseholdDefaultUserSharePercent: share, UpdatedAt: &now,
	}); err != nil {
		return HouseholdPartnerProfileResponse{}, err
	}
	return HouseholdPartnerProfileResponse{DisplayName: u.HouseholdPartnerDisplayName, DefaultUserSharePercent: share}, nil
}

// --- Snapshots --------------------------------------------------------------

func decodeShares(raw json.RawMessage) map[string]float64 {
	out := map[string]float64{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]float64{}
	}
	return out
}

func encodeShares(m map[string]float64) json.RawMessage {
	if m == nil {
		m = map[string]float64{}
	}
	b, _ := json.Marshal(m)
	return b
}

// MapSnapshot is mapSnapshot.
func MapSnapshot(m expensesdb.BudgetSnapshot) BudgetSnapshotResponse {
	return BudgetSnapshotResponse{
		ID:                     apijson.FormatUUID(m.ID),
		MonthStart:             FormatDay(m.MonthStart),
		NetSalary:              m.NetSalary,
		TargetShares:           decodeShares(m.TargetShares),
		CurrencyCode:           m.CurrencyCode,
		CategoryDriftThreshold: m.CategoryDriftThreshold,
		TotalDriftThreshold:    m.TotalDriftThreshold,
		AlertsEnabled:          m.AlertsEnabled,
		AlertOnUnbudgeted:      m.AlertOnUnbudgeted,
		Revision:               m.Revision,
		CreatedAt:              isoPtr(m.CreatedAt),
		UpdatedAt:              isoPtr(m.UpdatedAt),
	}
}

// GetSnapshots is getSnapshots: every snapshot, optionally filtered by
// calendar year/month (server time zone, UTC), sorted by month.
func (s *Service) GetSnapshots(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, year, month *int) ([]BudgetSnapshotResponse, error) {
	rows, err := q(db).ListSnapshotsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]BudgetSnapshotResponse, 0, len(rows))
	for k := range rows {
		r := &rows[k]
		ms := r.MonthStart.UTC()
		if year != nil && ms.Year() != *year {
			continue
		}
		if month != nil && int(ms.Month()) != *month {
			continue
		}
		out = append(out, MapSnapshot(*r))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MonthStart < out[j].MonthStart })
	return out, nil
}

// FindSnapshot is findSnapshot(userId:matchingMonth:).
func (s *Service) FindSnapshot(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, monthStart time.Time) (*expensesdb.BudgetSnapshot, error) {
	rows, err := q(db).ListSnapshotsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if sameMonth(rows[i].MonthStart, monthStart) {
			return &rows[i], nil
		}
	}
	return nil, nil
}

func applyBudgetSettings(req BudgetSnapshotRequest, m *expensesdb.BudgetSnapshot) {
	if req.CurrencyCode != nil {
		m.CurrencyCode = strings.ToUpper(*req.CurrencyCode)
	}
	if req.CategoryDriftThreshold != nil {
		m.CategoryDriftThreshold = *req.CategoryDriftThreshold
	}
	if req.TotalDriftThreshold != nil {
		m.TotalDriftThreshold = *req.TotalDriftThreshold
	}
	if req.AlertsEnabled != nil {
		m.AlertsEnabled = *req.AlertsEnabled
	}
	if req.AlertOnUnbudgeted != nil {
		m.AlertOnUnbudgeted = *req.AlertOnUnbudgeted
	}
}

func (s *Service) saveSnapshot(ctx context.Context, db expensesdb.DBTX, m *expensesdb.BudgetSnapshot) error {
	now := s.now()
	m.UpdatedAt = &now
	return q(db).UpdateSnapshot(ctx, expensesdb.UpdateSnapshotParams{
		ID: m.ID, MonthStart: m.MonthStart, NetSalary: m.NetSalary, TargetShares: m.TargetShares,
		CurrencyCode: m.CurrencyCode, CategoryDriftThreshold: m.CategoryDriftThreshold,
		TotalDriftThreshold: m.TotalDriftThreshold, AlertsEnabled: m.AlertsEnabled,
		AlertOnUnbudgeted: m.AlertOnUnbudgeted, Revision: m.Revision, UpdatedAt: m.UpdatedAt,
	})
}

func (s *Service) insertSnapshot(ctx context.Context, db expensesdb.DBTX, m *expensesdb.BudgetSnapshot) error {
	now := s.now()
	m.CreatedAt, m.UpdatedAt = &now, &now
	return q(db).InsertSnapshot(ctx, expensesdb.InsertSnapshotParams{
		ID: m.ID, UserID: m.UserID, MonthStart: m.MonthStart, NetSalary: m.NetSalary,
		TargetShares: m.TargetShares, CurrencyCode: m.CurrencyCode,
		CategoryDriftThreshold: m.CategoryDriftThreshold, TotalDriftThreshold: m.TotalDriftThreshold,
		AlertsEnabled: m.AlertsEnabled, AlertOnUnbudgeted: m.AlertOnUnbudgeted, Revision: m.Revision,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	})
}

// CreateBudgetSnapshot is createBudgetSnapshot: upserts the month's snapshot.
func (s *Service) CreateBudgetSnapshot(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, req BudgetSnapshotRequest) (BudgetSnapshotResponse, error) {
	if err := validateBudgetSettings(req.CurrencyCode, req.CategoryDriftThreshold, req.TotalDriftThreshold); err != nil {
		return BudgetSnapshotResponse{}, err
	}
	if !(isFinite(req.NetSalary) && req.NetSalary >= 0) {
		return BudgetSnapshotResponse{}, httpx.BadRequest("netSalary must be finite and non-negative.")
	}
	raw, ok := ParseDay(req.MonthStart)
	if !ok {
		return BudgetSnapshotResponse{}, httpx.BadRequest("Invalid monthStart format. Expected YYYY-MM-DD.")
	}
	monthStart := MonthStart(raw)
	existing, err := s.FindSnapshot(ctx, db, userID, monthStart)
	if err != nil {
		return BudgetSnapshotResponse{}, err
	}
	update := func(m *expensesdb.BudgetSnapshot, setMonth bool) (BudgetSnapshotResponse, error) {
		if setMonth {
			m.MonthStart = monthStart
		}
		m.NetSalary = req.NetSalary
		m.TargetShares = encodeShares(req.TargetShares)
		applyBudgetSettings(req, m)
		m.Revision++
		if err := s.saveSnapshot(ctx, db, m); err != nil {
			return BudgetSnapshotResponse{}, err
		}
		return MapSnapshot(*m), nil
	}
	if existing != nil {
		return update(existing, true)
	}
	currency := "USD"
	if req.CurrencyCode != nil {
		currency = strings.ToUpper(*req.CurrencyCode)
	}
	m := expensesdb.BudgetSnapshot{
		ID: uuid.New(), UserID: userID, MonthStart: monthStart, NetSalary: req.NetSalary,
		TargetShares: encodeShares(req.TargetShares), CurrencyCode: currency,
		CategoryDriftThreshold: derefOr(req.CategoryDriftThreshold, 15),
		TotalDriftThreshold:    derefOr(req.TotalDriftThreshold, 10),
		AlertsEnabled:          derefBool(req.AlertsEnabled, true),
		AlertOnUnbudgeted:      derefBool(req.AlertOnUnbudgeted, true),
	}
	if err := s.insertSnapshot(ctx, db, &m); err != nil {
		// A concurrent expense import may have created this month's snapshot
		// after the lookup. Treat the unique row as the winner.
		if !isUniqueViolation(err) {
			return BudgetSnapshotResponse{}, err
		}
		concurrent, ferr := s.FindSnapshot(ctx, db, userID, monthStart)
		if ferr != nil || concurrent == nil {
			return BudgetSnapshotResponse{}, err
		}
		return update(concurrent, false)
	}
	return MapSnapshot(m), nil
}

func derefOr(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func derefBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// UpdateSnapshot is updateSnapshot.
func (s *Service) UpdateSnapshot(ctx context.Context, db expensesdb.DBTX, userID, snapshotID uuid.UUID, req BudgetSnapshotRequest) (BudgetSnapshotResponse, error) {
	if err := validateBudgetSettings(req.CurrencyCode, req.CategoryDriftThreshold, req.TotalDriftThreshold); err != nil {
		return BudgetSnapshotResponse{}, err
	}
	m, err := q(db).GetSnapshot(ctx, snapshotID)
	if isNoRows(err) {
		return BudgetSnapshotResponse{}, httpx.NotFound("")
	}
	if err != nil {
		return BudgetSnapshotResponse{}, err
	}
	if m.UserID != userID {
		return BudgetSnapshotResponse{}, httpx.Forbidden("")
	}
	raw, ok := ParseDay(req.MonthStart)
	if !ok {
		return BudgetSnapshotResponse{}, httpx.BadRequest("Invalid monthStart format. Expected YYYY-MM-DD.")
	}
	m.MonthStart = MonthStart(raw)
	m.NetSalary = req.NetSalary
	m.TargetShares = encodeShares(req.TargetShares)
	applyBudgetSettings(req, &m)
	m.Revision++
	if err := s.saveSnapshot(ctx, db, &m); err != nil {
		return BudgetSnapshotResponse{}, err
	}
	return MapSnapshot(m), nil
}

// DeleteSnapshot is deleteSnapshot.
func (s *Service) DeleteSnapshot(ctx context.Context, db expensesdb.DBTX, userID, snapshotID uuid.UUID) error {
	m, err := q(db).GetSnapshot(ctx, snapshotID)
	if isNoRows(err) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if m.UserID != userID {
		return httpx.Forbidden("")
	}
	return q(db).DeleteSnapshot(ctx, snapshotID)
}

func (s *Service) bumpRevision(ctx context.Context, db expensesdb.DBTX, m expensesdb.BudgetSnapshot) error {
	now := s.now()
	return q(db).SetSnapshotRevision(ctx, expensesdb.SetSnapshotRevisionParams{ID: m.ID, Revision: m.Revision + 1, UpdatedAt: &now})
}

// --- Plan items -------------------------------------------------------------

// MapPlanItem is mapPlanItem.
func MapPlanItem(m expensesdb.BudgetPlanItem) BudgetPlanItemResponse {
	return BudgetPlanItemResponse{
		ID:                         apijson.FormatUUID(m.ID),
		SnapshotID:                 apijson.FormatUUID(m.SnapshotID),
		Title:                      m.Title,
		PlannedAmount:              m.PlannedAmount,
		Pillar:                     pillarFromDB(m.Pillar),
		CategoryID:                 formatUUIDPtr(m.CategoryID),
		SplitMode:                  string(m.SplitMode),
		UserSharePercent:           m.UserSharePercent,
		TargetType:                 m.TargetType,
		IncomePercentage:           m.IncomePercentage,
		ThresholdOverride:          m.ThresholdOverride,
		AllocationKind:             m.AllocationKind,
		ReallocationEligible:       m.ReallocationEligible,
		DestinationFinancialGoalID: formatUUIDPtr(m.DestinationFinancialGoalID),
		DestinationPortfolioListID: formatUUIDPtr(m.DestinationPortfolioListID),
		CreatedAt:                  isoPtr(m.CreatedAt),
		UpdatedAt:                  isoPtr(m.UpdatedAt),
	}
}

func mapPlanItems(rows []expensesdb.BudgetPlanItem) []BudgetPlanItemResponse {
	out := make([]BudgetPlanItemResponse, len(rows))
	for i := range rows {
		r := &rows[i]
		out[i] = MapPlanItem(*r)
	}
	return out
}

// GetAllPlanItems is getAllPlanItems.
func (s *Service) GetAllPlanItems(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID) ([]BudgetPlanItemResponse, error) {
	rows, err := q(db).ListPlanItemsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return mapPlanItems(rows), nil
}

// GetPlanItems is getPlanItems(snapshotId:).
func (s *Service) GetPlanItems(ctx context.Context, db expensesdb.DBTX, userID, snapshotID uuid.UUID) ([]BudgetPlanItemResponse, error) {
	rows, err := q(db).ListPlanItemsByUserSnapshot(ctx, expensesdb.ListPlanItemsByUserSnapshotParams{UserID: userID, SnapshotID: snapshotID})
	if err != nil {
		return nil, err
	}
	return mapPlanItems(rows), nil
}

func (s *Service) validatePlanItemRequest(ctx context.Context, db expensesdb.DBTX, req BudgetPlanItemRequest, userID uuid.UUID) error {
	if !(isFinite(req.PlannedAmount) && req.PlannedAmount >= 0) {
		return httpx.BadRequest("plannedAmount must be finite and non-negative.")
	}
	if req.TargetType == TargetPercentageIncome {
		p := req.IncomePercentage
		if p == nil || !isFinite(*p) || !(*p >= 0 && *p <= 1000) {
			return httpx.BadRequest("incomePercentage is required and must be between 0 and 1000.")
		}
	}
	if t := req.ThresholdOverride; t != nil && (!isFinite(*t) || !(*t >= 0 && *t <= 1000)) {
		return httpx.BadRequest("thresholdOverride must be between 0 and 1000.")
	}
	if req.CategoryID != nil {
		if ok, err := s.categoryOwned(ctx, db, *req.CategoryID, userID); err != nil || !ok {
			if err != nil {
				return err
			}
			return httpx.BadRequest("Invalid categoryId.")
		}
	}
	if req.DestinationFinancialGoalID != nil {
		id, ok := parseUUIDString(*req.DestinationFinancialGoalID)
		owned := false
		if ok {
			var err error
			if owned, err = q(db).FinancialGoalOwned(ctx, expensesdb.FinancialGoalOwnedParams{UserID: userID, ID: id}); err != nil {
				return err
			}
		}
		if !owned {
			return httpx.BadRequest("Invalid destinationFinancialGoalId.")
		}
	}
	if req.DestinationPortfolioListID != nil {
		id, ok := parseUUIDString(*req.DestinationPortfolioListID)
		owned := false
		if ok {
			var err error
			if owned, err = q(db).PortfolioListOwned(ctx, expensesdb.PortfolioListOwnedParams{ID: id, UserID: userID}); err != nil {
				return err
			}
		}
		if !owned {
			return httpx.BadRequest("Invalid destinationPortfolioListId.")
		}
	}
	return nil
}

func (s *Service) categoryOwned(ctx context.Context, db expensesdb.DBTX, raw string, userID uuid.UUID) (bool, error) {
	id, ok := parseUUIDString(raw)
	if !ok {
		return false, nil
	}
	c, err := q(db).GetCategory(ctx, id)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return c.UserID == userID, nil
}

func plannedAmountFor(req BudgetPlanItemRequest, netSalary float64) float64 {
	if req.TargetType == TargetPercentageIncome {
		return netSalary * derefOr(req.IncomePercentage, 0) / 100
	}
	return req.PlannedAmount
}

// CreatePlanItem is createPlanItem.
func (s *Service) CreatePlanItem(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, req BudgetPlanItemRequest) (BudgetPlanItemResponse, error) {
	if err := s.validatePlanItemRequest(ctx, db, req, userID); err != nil {
		return BudgetPlanItemResponse{}, err
	}
	snapshotID, ok := parseUUIDString(req.SnapshotID)
	if !ok {
		return BudgetPlanItemResponse{}, httpx.BadRequest("Invalid snapshotId.")
	}
	snap, err := q(db).GetSnapshot(ctx, snapshotID)
	if isNoRows(err) || (err == nil && snap.UserID != userID) {
		return BudgetPlanItemResponse{}, httpx.NotFound("Snapshot not found.")
	}
	if err != nil {
		return BudgetPlanItemResponse{}, err
	}
	mode, share, err := normalizeSplit(req.SplitMode, req.UserSharePercent)
	if err != nil {
		return BudgetPlanItemResponse{}, err
	}
	now := s.now()
	item := expensesdb.BudgetPlanItem{
		ID: uuid.New(), SnapshotID: snapshotID, UserID: userID, Title: req.Title,
		PlannedAmount: plannedAmountFor(req, snap.NetSalary), Pillar: string(req.Pillar),
		SplitMode: expensesdb.ExpenseSplitMode(mode), UserSharePercent: share,
		CategoryID: uuidPtrFromString(req.CategoryID), TargetType: req.TargetType,
		IncomePercentage: req.IncomePercentage, ThresholdOverride: req.ThresholdOverride,
		AllocationKind: req.AllocationKind, ReallocationEligible: req.ReallocationEligible,
		DestinationFinancialGoalID: uuidPtrFromString(req.DestinationFinancialGoalID),
		DestinationPortfolioListID: uuidPtrFromString(req.DestinationPortfolioListID),
		CreatedAt:                  &now, UpdatedAt: &now,
	}
	if err := s.insertPlanItem(ctx, db, item); err != nil {
		return BudgetPlanItemResponse{}, err
	}
	if err := s.bumpRevision(ctx, db, snap); err != nil {
		return BudgetPlanItemResponse{}, err
	}
	return MapPlanItem(item), nil
}

func (s *Service) insertPlanItem(ctx context.Context, db expensesdb.DBTX, m expensesdb.BudgetPlanItem) error {
	return q(db).InsertPlanItem(ctx, expensesdb.InsertPlanItemParams{
		ID: m.ID, SnapshotID: m.SnapshotID, UserID: m.UserID, Title: m.Title,
		PlannedAmount: m.PlannedAmount, Pillar: m.Pillar, SplitMode: m.SplitMode,
		UserSharePercent: m.UserSharePercent, CategoryID: m.CategoryID, TargetType: m.TargetType,
		IncomePercentage: m.IncomePercentage, ThresholdOverride: m.ThresholdOverride,
		AllocationKind: m.AllocationKind, ReallocationEligible: m.ReallocationEligible,
		DestinationFinancialGoalID: m.DestinationFinancialGoalID,
		DestinationPortfolioListID: m.DestinationPortfolioListID,
		CreatedAt:                  m.CreatedAt, UpdatedAt: m.UpdatedAt,
	})
}

// newPlanItem is BudgetPlanItem's init with its defaults (fixed, expense,
// not reallocation-eligible).
func (s *Service) newPlanItem(snapshotID, userID uuid.UUID, src expensesdb.BudgetPlanItem) expensesdb.BudgetPlanItem {
	now := s.now()
	return expensesdb.BudgetPlanItem{
		ID: uuid.New(), SnapshotID: snapshotID, UserID: userID, Title: src.Title,
		PlannedAmount: src.PlannedAmount, Pillar: src.Pillar, SplitMode: src.SplitMode,
		UserSharePercent: src.UserSharePercent, CategoryID: src.CategoryID,
		TargetType: TargetFixed, AllocationKind: AllocationExpense,
		CreatedAt: &now, UpdatedAt: &now,
	}
}

// UpdatePlanItem is updatePlanItem. The request's snapshotId is ignored, as
// in Vapor: an item never moves between snapshots.
func (s *Service) UpdatePlanItem(ctx context.Context, db expensesdb.DBTX, userID, itemID uuid.UUID, req BudgetPlanItemRequest) (BudgetPlanItemResponse, error) {
	if err := s.validatePlanItemRequest(ctx, db, req, userID); err != nil {
		return BudgetPlanItemResponse{}, err
	}
	item, err := q(db).GetPlanItem(ctx, itemID)
	if isNoRows(err) {
		return BudgetPlanItemResponse{}, httpx.NotFound("")
	}
	if err != nil {
		return BudgetPlanItemResponse{}, err
	}
	if item.UserID != userID {
		return BudgetPlanItemResponse{}, httpx.Forbidden("")
	}
	mode, share, err := normalizeSplit(req.SplitMode, req.UserSharePercent)
	if err != nil {
		return BudgetPlanItemResponse{}, err
	}
	snap, err := q(db).GetSnapshot(ctx, item.SnapshotID)
	if isNoRows(err) {
		return BudgetPlanItemResponse{}, httpx.NotFound("Snapshot not found.")
	}
	if err != nil {
		return BudgetPlanItemResponse{}, err
	}
	now := s.now()
	item.Title = req.Title
	item.PlannedAmount = plannedAmountFor(req, snap.NetSalary)
	item.Pillar = string(req.Pillar)
	item.SplitMode = expensesdb.ExpenseSplitMode(mode)
	item.UserSharePercent = share
	item.TargetType = req.TargetType
	item.IncomePercentage = req.IncomePercentage
	item.ThresholdOverride = req.ThresholdOverride
	item.AllocationKind = req.AllocationKind
	item.ReallocationEligible = req.ReallocationEligible
	item.DestinationFinancialGoalID = uuidPtrFromString(req.DestinationFinancialGoalID)
	item.DestinationPortfolioListID = uuidPtrFromString(req.DestinationPortfolioListID)
	item.CategoryID = uuidPtrFromString(req.CategoryID)
	item.UpdatedAt = &now
	if err := q(db).UpdatePlanItem(ctx, expensesdb.UpdatePlanItemParams{
		ID: item.ID, Title: item.Title, PlannedAmount: item.PlannedAmount, Pillar: item.Pillar,
		SplitMode: item.SplitMode, UserSharePercent: item.UserSharePercent, CategoryID: item.CategoryID,
		TargetType: item.TargetType, IncomePercentage: item.IncomePercentage,
		ThresholdOverride: item.ThresholdOverride, AllocationKind: item.AllocationKind,
		ReallocationEligible:       item.ReallocationEligible,
		DestinationFinancialGoalID: item.DestinationFinancialGoalID,
		DestinationPortfolioListID: item.DestinationPortfolioListID, UpdatedAt: item.UpdatedAt,
	}); err != nil {
		return BudgetPlanItemResponse{}, err
	}
	if err := s.bumpRevision(ctx, db, snap); err != nil {
		return BudgetPlanItemResponse{}, err
	}
	return MapPlanItem(item), nil
}

// DeletePlanItem is deletePlanItem.
func (s *Service) DeletePlanItem(ctx context.Context, db expensesdb.DBTX, userID, itemID uuid.UUID) error {
	item, err := q(db).GetPlanItem(ctx, itemID)
	if isNoRows(err) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if item.UserID != userID {
		return httpx.Forbidden("")
	}
	if err := q(db).DeletePlanItem(ctx, itemID); err != nil {
		return err
	}
	snap, err := q(db).GetSnapshot(ctx, item.SnapshotID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.bumpRevision(ctx, db, snap)
}

// --- Expenses ---------------------------------------------------------------

// MapExpense is mapExpense.
func MapExpense(m expensesdb.Expense) ExpenseResponse {
	var meta *ExpenseReceiptMetadata
	if len(m.ReceiptMetadata) > 0 && string(m.ReceiptMetadata) != "null" {
		var v ExpenseReceiptMetadata
		if json.Unmarshal(m.ReceiptMetadata, &v) == nil {
			meta = &v
		}
	}
	return ExpenseResponse{
		ID:               apijson.FormatUUID(m.ID),
		Title:            m.Title,
		Amount:           m.Amount,
		Pillar:           pillarFromDB(m.Pillar),
		OccurredOn:       FormatDay(m.OccurredOn),
		LinkedPlanItemID: formatUUIDPtr(m.LinkedItemID),
		CategoryID:       formatUUIDPtr(m.CategoryID),
		SplitMode:        string(m.SplitMode),
		UserSharePercent: m.UserSharePercent,
		ForeignAmount:    m.ForeignAmount,
		ForeignCurrency:  m.ForeignCurrency,
		ExchangeRate:     m.ExchangeRate,
		Notes:            m.Notes,
		ReceiptMetadata:  meta,
		CreatedAt:        isoPtr(m.CreatedAt),
		UpdatedAt:        isoPtr(m.UpdatedAt),
	}
}

func mapExpenses(rows []expensesdb.Expense) []ExpenseResponse {
	out := make([]ExpenseResponse, len(rows))
	for i := range rows {
		r := &rows[i]
		out[i] = MapExpense(*r)
	}
	return out
}

func encodeMeta(m *ExpenseReceiptMetadata) json.RawMessage {
	if m == nil {
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

// GetExpenses is getExpenses: newest first, at most limit rows, with a next
// cursor (the last row's day as an ISO timestamp) when more exist and limit
// is under 200.
func (s *Service) GetExpenses(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, from, to *time.Time, limit int, cursor *time.Time) ([]ExpenseResponse, *string, error) {
	var through *time.Time
	if cursor != nil {
		// occurred_on is a DATE column. Move to the preceding calendar day
		// so timezone conversion cannot re-include the cursor day.
		through = timePtr(cursor.UTC().AddDate(0, 0, -1))
	}
	rowLimit := int32(min(max(limit, 0), 100000) + 1)
	rows, err := q(db).ListExpensesPage(ctx, expensesdb.ListExpensesPageParams{
		UserID: userID, FromAt: from, ToAt: to, ThroughAt: through, RowLimit: rowLimit,
	})
	if err != nil {
		return nil, nil, err
	}
	const maxLimit = 200
	if len(rows) > limit && limit < maxLimit {
		page := rows[:limit]
		if len(page) == 0 {
			return mapExpenses(page), nil, nil
		}
		next := FormatISOMillis(page[len(page)-1].OccurredOn)
		return mapExpenses(page), &next, nil
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return mapExpenses(rows), nil, nil
}

// SearchExpenses is searchExpenses.
func (s *Service) SearchExpenses(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, f ExpenseSearchFilters) ([]ExpenseResponse, *string, error) {
	var pillar *string
	if f.Pillar != nil {
		p := string(*f.Pillar)
		pillar = &p
	}
	rows, err := q(db).SearchExpenses(ctx, expensesdb.SearchExpensesParams{
		UserID: userID, FromAt: f.From, ToAt: f.To, CategoryID: f.CategoryID, Pillar: pillar, BeforeAt: f.Cursor,
	})
	if err != nil {
		return nil, nil, err
	}
	if f.Query != nil {
		if search := strings.TrimFunc(*f.Query, unicode.IsSpace); search != "" {
			needle := strings.ToLower(search)
			kept := rows[:0]
			for k := range rows {
				r := &rows[k]
				if strings.Contains(strings.ToLower(r.Title), needle) ||
					(r.Notes != nil && strings.Contains(strings.ToLower(*r.Notes), needle)) {
					kept = append(kept, *r)
				}
			}
			rows = kept
		}
	}
	page := rows
	if len(page) > f.Limit+1 {
		page = page[:f.Limit+1]
	}
	visible := page
	if len(visible) > f.Limit {
		visible = visible[:f.Limit]
	}
	var next *string
	if len(page) > f.Limit && len(visible) > 0 {
		v := FormatISOMillis(visible[len(visible)-1].OccurredOn)
		next = &v
	}
	return mapExpenses(visible), next, nil
}

func (s *Service) planItemOwned(ctx context.Context, db expensesdb.DBTX, raw string, userID uuid.UUID) (uuid.UUID, error) {
	id, ok := parseUUIDString(raw)
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Invalid linkedPlanItemId.")
	}
	item, err := q(db).GetPlanItem(ctx, id)
	if isNoRows(err) || (err == nil && item.UserID != userID) {
		return uuid.UUID{}, httpx.BadRequest("Invalid linkedPlanItemId.")
	}
	if err != nil {
		return uuid.UUID{}, err
	}
	return id, nil
}

func (s *Service) categoryFor(ctx context.Context, db expensesdb.DBTX, raw string, userID uuid.UUID) (*uuid.UUID, error) {
	ok, err := s.categoryOwned(ctx, db, raw, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, httpx.BadRequest("Invalid categoryId.")
	}
	id, _ := parseUUIDString(raw)
	return &id, nil
}

func trimmedNotes(n *string) *string {
	if n == nil {
		return nil
	}
	t := strings.TrimFunc(*n, unicode.IsSpace)
	return &t
}

// CreateExpense is createExpense, including its side effects (activity,
// drift evaluation, badge reconciliation). The daily XP award lives in the
// controller, as in Vapor.
func (s *Service) CreateExpense(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, req ExpenseRequest) (ExpenseResponse, error) {
	if !(isFinite(req.Amount) && req.Amount >= 0) {
		return ExpenseResponse{}, httpx.BadRequest("amount must be finite and non-negative.")
	}
	occurredOn, ok := ParseDay(req.OccurredOn)
	if !ok {
		return ExpenseResponse{}, httpx.BadRequest("Invalid occurredOn format. Expected YYYY-MM-DD.")
	}
	monthStart := MonthStart(occurredOn)
	if err := s.EnsureSnapshotExists(ctx, db, userID, monthStart); err != nil {
		return ExpenseResponse{}, err
	}
	var linked *uuid.UUID
	if req.LinkedPlanItemID != nil {
		id, err := s.planItemOwned(ctx, db, *req.LinkedPlanItemID, userID)
		if err != nil {
			return ExpenseResponse{}, err
		}
		linked = &id
	}
	mode, share, err := normalizeSplit(req.SplitMode, req.UserSharePercent)
	if err != nil {
		return ExpenseResponse{}, err
	}
	e := expensesdb.Expense{
		ID: uuid.New(), UserID: userID, Title: req.Title, Amount: req.Amount, Pillar: string(req.Pillar),
		OccurredOn: occurredOn, LinkedItemID: linked, SplitMode: expensesdb.ExpenseSplitMode(mode), UserSharePercent: share,
	}
	if req.CategoryID != nil {
		if e.CategoryID, err = s.categoryFor(ctx, db, *req.CategoryID, userID); err != nil {
			return ExpenseResponse{}, err
		}
	}
	if req.ForeignAmount != nil {
		if req.ExchangeRate == nil || !(*req.ExchangeRate > 0) {
			return ExpenseResponse{}, httpx.BadRequest("exchangeRate must be > 0 when foreignAmount is provided.")
		}
		e.ForeignAmount, e.ForeignCurrency, e.ExchangeRate = req.ForeignAmount, req.ForeignCurrency, req.ExchangeRate
	}
	e.Notes = trimmedNotes(req.Notes)
	e.ReceiptMetadata = encodeMeta(req.ReceiptMetadata)
	if err := s.insertExpense(ctx, db, &e); err != nil {
		return ExpenseResponse{}, err
	}
	s.recordActivity(ctx, db, userID, activitydb.UserActivityTypeExpenseRecorded, "Expense Added", req)
	s.evaluateDriftBestEffort(ctx, userID, monthStart)
	s.ReconcileBadges(ctx, db, userID)
	return MapExpense(e), nil
}

func (s *Service) insertExpense(ctx context.Context, db expensesdb.DBTX, e *expensesdb.Expense) error {
	now := s.now()
	e.CreatedAt, e.UpdatedAt = &now, &now
	return q(db).InsertExpense(ctx, expensesdb.InsertExpenseParams{
		ID: e.ID, UserID: e.UserID, Title: e.Title, Amount: e.Amount, Pillar: e.Pillar,
		OccurredOn: e.OccurredOn, LinkedItemID: e.LinkedItemID, SplitMode: e.SplitMode,
		UserSharePercent: e.UserSharePercent, CategoryID: e.CategoryID, ForeignAmount: e.ForeignAmount,
		ForeignCurrency: e.ForeignCurrency, ExchangeRate: e.ExchangeRate, Notes: e.Notes,
		ReceiptMetadata: e.ReceiptMetadata, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	})
}

func (s *Service) recordActivity(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, typ activitydb.UserActivityType, title string, req ExpenseRequest) {
	amount := req.Amount
	// try? in Swift: failures are swallowed.
	_ = activity.NewRecorder(db).Record(ctx, activity.Record{
		UserID: userID, Type: typ, Title: title, Subtitle: req.Title, Amount: &amount,
		IsGrowth: false, Symbol: symbolFor(req.Pillar),
	})
}

// ReconcileBadges is req.reconcileBadges: best-effort badge evaluation.
// (Vapor also queued an achievement check here; achievements are not
// ported yet.)
func (s *Service) ReconcileBadges(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID) {
	svc := badges.Service{Q: badgesdb.New(db), Now: s.now}
	if _, err := svc.Evaluate(ctx, userID); err != nil {
		s.logger.Warn("badge reconciliation failed", "user", userID, "error", err)
	}
}

// EvaluateDrift runs the wired drift evaluator best-effort for each month.
func (s *Service) EvaluateDrift(ctx context.Context, userID uuid.UUID, months []time.Time, logMessage string) {
	p := s.drift.Load()
	if p == nil {
		return
	}
	for _, m := range months {
		if err := (*p).Evaluate(ctx, userID, m, true); err != nil {
			s.logger.Warn(logMessage, "userId", apijson.FormatUUID(userID), "month", FormatDay(m), "error", err)
		}
	}
}

func (s *Service) evaluateDriftBestEffort(ctx context.Context, userID uuid.UUID, monthStart time.Time) {
	s.EvaluateDrift(ctx, userID, []time.Time{monthStart}, "budget drift evaluation failed")
}

// UpdateExpense is updateExpense.
func (s *Service) UpdateExpense(ctx context.Context, db expensesdb.DBTX, userID, expenseID uuid.UUID, req ExpenseRequest) (ExpenseResponse, error) {
	e, err := q(db).GetExpense(ctx, expenseID)
	if isNoRows(err) {
		return ExpenseResponse{}, httpx.NotFound("")
	}
	if err != nil {
		return ExpenseResponse{}, err
	}
	if e.UserID != userID {
		return ExpenseResponse{}, httpx.Forbidden("")
	}
	previousMonth := MonthStart(e.OccurredOn)
	occurredOn, ok := ParseDay(req.OccurredOn)
	if !ok {
		return ExpenseResponse{}, httpx.BadRequest("Invalid occurredOn format. Expected YYYY-MM-DD.")
	}
	var linked *uuid.UUID
	if req.LinkedPlanItemID != nil {
		id, err := s.planItemOwned(ctx, db, *req.LinkedPlanItemID, userID)
		if err != nil {
			return ExpenseResponse{}, err
		}
		linked = &id
	}
	mode, share, err := normalizeSplit(req.SplitMode, req.UserSharePercent)
	if err != nil {
		return ExpenseResponse{}, err
	}
	e.Title, e.Amount, e.Pillar, e.OccurredOn = req.Title, req.Amount, string(req.Pillar), occurredOn
	e.SplitMode, e.UserSharePercent, e.LinkedItemID = expensesdb.ExpenseSplitMode(mode), share, linked
	e.CategoryID = nil
	if req.CategoryID != nil {
		if e.CategoryID, err = s.categoryFor(ctx, db, *req.CategoryID, userID); err != nil {
			return ExpenseResponse{}, err
		}
	}
	if req.ForeignAmount != nil {
		if req.ExchangeRate == nil || !(*req.ExchangeRate > 0) {
			return ExpenseResponse{}, httpx.BadRequest("exchangeRate must be > 0 when foreignAmount is provided.")
		}
		e.ForeignAmount, e.ForeignCurrency, e.ExchangeRate = req.ForeignAmount, req.ForeignCurrency, req.ExchangeRate
	} else {
		e.ForeignAmount, e.ForeignCurrency, e.ExchangeRate = nil, nil, nil
	}
	e.Notes = trimmedNotes(req.Notes)
	e.ReceiptMetadata = encodeMeta(req.ReceiptMetadata)
	now := s.now()
	e.UpdatedAt = &now
	if err := q(db).UpdateExpense(ctx, expensesdb.UpdateExpenseParams{
		ID: e.ID, Title: e.Title, Amount: e.Amount, Pillar: e.Pillar, OccurredOn: e.OccurredOn,
		LinkedItemID: e.LinkedItemID, SplitMode: e.SplitMode, UserSharePercent: e.UserSharePercent,
		CategoryID: e.CategoryID, ForeignAmount: e.ForeignAmount, ForeignCurrency: e.ForeignCurrency,
		ExchangeRate: e.ExchangeRate, Notes: e.Notes, ReceiptMetadata: e.ReceiptMetadata, UpdatedAt: e.UpdatedAt,
	}); err != nil {
		return ExpenseResponse{}, err
	}
	currentMonth := MonthStart(occurredOn)
	s.evaluateDriftBestEffort(ctx, userID, previousMonth)
	if !currentMonth.Equal(previousMonth) {
		s.evaluateDriftBestEffort(ctx, userID, currentMonth)
	}
	s.recordActivity(ctx, db, userID, activitydb.UserActivityTypeExpenseUpdated, "Expense Updated", req)
	s.ReconcileBadges(ctx, db, userID)
	return MapExpense(e), nil
}

// DeleteExpense is deleteExpense.
func (s *Service) DeleteExpense(ctx context.Context, db expensesdb.DBTX, userID, expenseID uuid.UUID) error {
	e, err := q(db).GetExpense(ctx, expenseID)
	if isNoRows(err) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if e.UserID != userID {
		return httpx.Forbidden("")
	}
	if err := q(db).DeleteExpense(ctx, expenseID); err != nil {
		return err
	}
	s.evaluateDriftBestEffort(ctx, userID, MonthStart(e.OccurredOn))
	s.ReconcileBadges(ctx, db, userID)
	return nil
}

// EnsureSnapshotExists creates the month's budget snapshot only if none
// exists (non-destructive), seeded from the latest snapshot, and carries the
// latest snapshot's "Subscriptions" plan items forward.
func (s *Service) EnsureSnapshotExists(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, monthStart time.Time) error {
	existing, err := s.FindSnapshot(ctx, db, userID, monthStart)
	if err != nil || existing != nil {
		return err
	}
	var template *expensesdb.BudgetSnapshot
	if t, err := q(db).LatestSnapshotByUser(ctx, userID); err == nil {
		template = &t
	} else if !isNoRows(err) {
		return err
	}
	shares := map[string]float64{}
	for _, p := range AllPillars {
		shares[string(p)] = DefaultTargetShare(p)
	}
	netSalary := 0.0
	if template != nil {
		shares = decodeShares(template.TargetShares)
		netSalary = template.NetSalary
	}
	created, err := s.CreateBudgetSnapshot(ctx, db, userID, BudgetSnapshotRequest{
		MonthStart: FormatDay(monthStart), NetSalary: netSalary, TargetShares: shares,
	})
	if err != nil {
		return err
	}
	if template == nil {
		return nil
	}
	items, err := q(db).ListPlanItemsWithCategoryName(ctx, expensesdb.ListPlanItemsWithCategoryNameParams{
		SnapshotID: template.ID, UserID: userID,
	})
	if err != nil {
		return err
	}
	newID, ok := parseUUIDString(created.ID)
	if !ok {
		newID = uuid.New()
	}
	for k := range items {
		it := &items[k]
		if it.CategoryName == nil || !strings.EqualFold(*it.CategoryName, "subscriptions") {
			continue
		}
		if err := s.insertPlanItem(ctx, db, s.newPlanItem(newID, userID, it.BudgetPlanItem)); err != nil {
			return err
		}
	}
	return nil
}

// EnsureCurrentMonthRollover rolls the budget into the current UTC month:
// clones the most recent past snapshot (salary, targets, settings, every
// plan item) when the current month has none. Returns the created snapshot,
// nil when nothing was created. db must be a pool (it opens a transaction).
func (s *Service) EnsureCurrentMonthRollover(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID, now time.Time) (*expensesdb.BudgetSnapshot, error) {
	current := MonthStart(now)
	latestPast := func(rows []expensesdb.BudgetSnapshot) *expensesdb.BudgetSnapshot {
		var best *expensesdb.BudgetSnapshot
		for i := range rows {
			if rows[i].MonthStart.Before(current) && (best == nil || rows[i].MonthStart.After(best.MonthStart)) {
				best = &rows[i]
			}
		}
		return best
	}
	hasCurrent := func(rows []expensesdb.BudgetSnapshot) bool {
		for k := range rows {
			r := &rows[k]
			if sameMonth(r.MonthStart, current) {
				return true
			}
		}
		return false
	}
	existing, err := q(db).ListSnapshotsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if hasCurrent(existing) || latestPast(existing) == nil {
		return nil, nil
	}
	var created *expensesdb.BudgetSnapshot
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		qt := q(tx)
		// Two devices foregrounding at once must not both clone the plan.
		if err := qt.AdvisoryXactLock(ctx, "budget_rollover:"+apijson.FormatUUID(userID)); err != nil {
			return err
		}
		rows, err := qt.ListSnapshotsByUser(ctx, userID)
		if err != nil {
			return err
		}
		if hasCurrent(rows) {
			return nil
		}
		tmpl := latestPast(rows)
		if tmpl == nil {
			return nil
		}
		snap := expensesdb.BudgetSnapshot{
			ID: uuid.New(), UserID: userID, MonthStart: current, NetSalary: tmpl.NetSalary,
			TargetShares: tmpl.TargetShares, CurrencyCode: tmpl.CurrencyCode,
			CategoryDriftThreshold: tmpl.CategoryDriftThreshold, TotalDriftThreshold: tmpl.TotalDriftThreshold,
			AlertsEnabled: tmpl.AlertsEnabled, AlertOnUnbudgeted: tmpl.AlertOnUnbudgeted,
		}
		if err := s.insertSnapshot(ctx, tx, &snap); err != nil {
			return err
		}
		items, err := qt.ListPlanItemsByUserSnapshot(ctx, expensesdb.ListPlanItemsByUserSnapshotParams{UserID: userID, SnapshotID: tmpl.ID})
		if err != nil {
			return err
		}
		for k := range items {
			it := &items[k]
			clone := *it
			clone.ID = uuid.New()
			clone.SnapshotID = snap.ID
			ts := s.now()
			clone.CreatedAt, clone.UpdatedAt = &ts, &ts
			if err := s.insertPlanItem(ctx, tx, clone); err != nil {
				return err
			}
		}
		created = &snap
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// --- Categories -------------------------------------------------------------

var defaultCategories = []struct {
	name   string
	pillar BudgetPillar
}{
	{"Groceries", PillarFundamentals},
	{"Rent", PillarFundamentals},
	{"Mortgage", PillarFundamentals},
	{"Utilities", PillarFundamentals},
	{"Transport", PillarFundamentals},
	{"Insurance", PillarFundamentals},
	{"Phone Bill", PillarFundamentals},
	{"Savings", PillarFutureYou},
	{"Investments", PillarFutureYou},
	{"Emergency Fund", PillarFutureYou},
	{"Dining Out", PillarFun},
	{"Entertainment", PillarFun},
	{"Travel", PillarFun},
	{"Subscriptions", PillarFun},
}

// MapCategory is mapCategory.
func MapCategory(m expensesdb.ExpenseCategory) ExpenseCategoryResponse {
	var pillar *BudgetPillar
	if m.Pillar != nil {
		if p, ok := ParsePillar(*m.Pillar); ok {
			pillar = &p
		}
	}
	return ExpenseCategoryResponse{ID: apijson.FormatUUID(m.ID), Name: m.Name, Pillar: pillar, IsDefault: m.IsDefault}
}

// GetCategories is getCategories: seeds the default categories on first
// access (returned in seed order), otherwise sorted by name.
func (s *Service) GetCategories(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID) ([]ExpenseCategoryResponse, error) {
	rows, err := q(db).ListCategories(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		now := s.now()
		out := make([]ExpenseCategoryResponse, 0, len(defaultCategories))
		for _, d := range defaultCategories {
			p := string(d.pillar)
			m := expensesdb.ExpenseCategory{ID: uuid.New(), UserID: userID, Name: d.name, Pillar: &p, IsDefault: true, CreatedAt: &now}
			if err := q(db).InsertCategory(ctx, expensesdb.InsertCategoryParams{
				ID: m.ID, UserID: m.UserID, Name: m.Name, Pillar: m.Pillar, IsDefault: true, CreatedAt: m.CreatedAt,
			}); err != nil {
				return nil, err
			}
			out = append(out, MapCategory(m))
		}
		return out, nil
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	out := make([]ExpenseCategoryResponse, len(rows))
	for i, r := range rows {
		out[i] = MapCategory(r)
	}
	return out, nil
}

// CreateCategory is createCategory.
func (s *Service) CreateCategory(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, req ExpenseCategoryRequest) (ExpenseCategoryResponse, error) {
	now := s.now()
	var pillar *string
	if req.Pillar != nil {
		p := string(*req.Pillar)
		pillar = &p
	}
	m := expensesdb.ExpenseCategory{ID: uuid.New(), UserID: userID, Name: req.Name, Pillar: pillar, CreatedAt: &now}
	if err := q(db).InsertCategory(ctx, expensesdb.InsertCategoryParams{
		ID: m.ID, UserID: userID, Name: m.Name, Pillar: m.Pillar, IsDefault: false, CreatedAt: m.CreatedAt,
	}); err != nil {
		return ExpenseCategoryResponse{}, err
	}
	return MapCategory(m), nil
}

// DeleteCategory is deleteCategory.
func (s *Service) DeleteCategory(ctx context.Context, db expensesdb.DBTX, userID, categoryID uuid.UUID) error {
	c, err := q(db).GetCategory(ctx, categoryID)
	if isNoRows(err) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if c.UserID != userID {
		return httpx.Forbidden("")
	}
	if c.IsDefault {
		return httpx.BadRequest("Cannot delete default categories.")
	}
	return q(db).DeleteCategory(ctx, categoryID)
}

// --- Recurring templates ----------------------------------------------------

// MapRecurringTemplate is mapRecurringTemplate.
func MapRecurringTemplate(m expensesdb.RecurringTemplate) RecurringTemplateResponse {
	freq := m.Frequency
	if freq != FrequencyWeekly && freq != FrequencyMonthly {
		freq = FrequencyMonthly
	}
	return RecurringTemplateResponse{
		ID: apijson.FormatUUID(m.ID), Title: m.Title, Amount: m.Amount, Pillar: pillarFromDB(m.Pillar),
		CategoryID: formatUUIDPtr(m.CategoryID), Frequency: freq, SplitMode: string(m.SplitMode),
		UserSharePercent: m.UserSharePercent, CreatedAt: isoPtr(m.CreatedAt),
	}
}

// GetRecurringTemplates is getRecurringTemplates.
func (s *Service) GetRecurringTemplates(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID) ([]RecurringTemplateResponse, error) {
	rows, err := q(db).ListRecurringTemplates(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]RecurringTemplateResponse, len(rows))
	for i := range rows {
		r := &rows[i]
		out[i] = MapRecurringTemplate(*r)
	}
	return out, nil
}

// CreateRecurringTemplate is createRecurringTemplate. categoryId is not
// validated (an unknown id fails the insert on its foreign key), as in Vapor.
func (s *Service) CreateRecurringTemplate(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, req RecurringTemplateRequest) (RecurringTemplateResponse, error) {
	mode, share, err := normalizeSplit(req.SplitMode, req.UserSharePercent)
	if err != nil {
		return RecurringTemplateResponse{}, err
	}
	now := s.now()
	m := expensesdb.RecurringTemplate{
		ID: uuid.New(), UserID: userID, Title: req.Title, Amount: req.Amount, Pillar: string(req.Pillar),
		CategoryID: uuidPtrFromString(req.CategoryID), Frequency: req.Frequency,
		SplitMode: expensesdb.ExpenseSplitMode(mode), UserSharePercent: share, CreatedAt: &now,
	}
	if err := q(db).InsertRecurringTemplate(ctx, expensesdb.InsertRecurringTemplateParams{
		ID: m.ID, UserID: userID, Title: m.Title, Amount: m.Amount, Pillar: m.Pillar, CategoryID: m.CategoryID,
		Frequency: m.Frequency, SplitMode: m.SplitMode, UserSharePercent: m.UserSharePercent, CreatedAt: m.CreatedAt,
	}); err != nil {
		return RecurringTemplateResponse{}, err
	}
	return MapRecurringTemplate(m), nil
}

// UpdateRecurringTemplate is updateRecurringTemplate.
func (s *Service) UpdateRecurringTemplate(ctx context.Context, db expensesdb.DBTX, userID, templateID uuid.UUID, req RecurringTemplateRequest) (RecurringTemplateResponse, error) {
	m, err := q(db).GetRecurringTemplate(ctx, templateID)
	if isNoRows(err) {
		return RecurringTemplateResponse{}, httpx.NotFound("")
	}
	if err != nil {
		return RecurringTemplateResponse{}, err
	}
	if m.UserID != userID {
		return RecurringTemplateResponse{}, httpx.Forbidden("")
	}
	mode, share, err := normalizeSplit(req.SplitMode, req.UserSharePercent)
	if err != nil {
		return RecurringTemplateResponse{}, err
	}
	m.Title, m.Amount, m.Pillar = req.Title, req.Amount, string(req.Pillar)
	m.CategoryID, m.Frequency = uuidPtrFromString(req.CategoryID), req.Frequency
	m.SplitMode, m.UserSharePercent = expensesdb.ExpenseSplitMode(mode), share
	if err := q(db).UpdateRecurringTemplate(ctx, expensesdb.UpdateRecurringTemplateParams{
		ID: m.ID, Title: m.Title, Amount: m.Amount, Pillar: m.Pillar, CategoryID: m.CategoryID,
		Frequency: m.Frequency, SplitMode: m.SplitMode, UserSharePercent: m.UserSharePercent,
	}); err != nil {
		return RecurringTemplateResponse{}, err
	}
	return MapRecurringTemplate(m), nil
}

// DeleteRecurringTemplate is deleteRecurringTemplate.
func (s *Service) DeleteRecurringTemplate(ctx context.Context, db expensesdb.DBTX, userID, templateID uuid.UUID) error {
	m, err := q(db).GetRecurringTemplate(ctx, templateID)
	if isNoRows(err) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	if m.UserID != userID {
		return httpx.Forbidden("")
	}
	return q(db).DeleteRecurringTemplate(ctx, templateID)
}
