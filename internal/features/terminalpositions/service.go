package terminalpositions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/boards"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/terminalpositionsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Position is one stored terminal scenario (TerminalPositionRecord). Every
// derived number comes from ToResponse at read time.
type Position = terminalpositionsdb.TerminalPosition

// Limits from TerminalPositionsService.
const (
	MaxNotesLength = 1000
	// MaxInput is the largest accepted value for any numeric input, so sums
	// of a user's rows stay finite.
	MaxInput = 1e18
)

var clearablePositionFields = map[string]bool{"sharesOutstanding": true, "currentSharePrice": true, "notes": true}

// ScenarioFields are the fields set_terminal_scenario may write
// (TerminalPositionsService.ScenarioFields). All optional; a new row needs
// TerminalShareCount, TerminalMarketCap and ValueWanted.
type ScenarioFields struct {
	TerminalShareCount *float64
	TerminalMarketCap  *float64
	ValueWanted        *float64
	SharesOwned        *float64
	SharesOutstanding  *float64
	CurrentSharePrice  *float64
}

// Service is TerminalPositionsService: rules and persistence for terminal
// position sizing. The controllers, the assistant's action catalog and the
// tests all go through it. Errors Vapor threw as Abort are *httpx.Error.
type Service struct {
	db  *pgxpool.Pool
	q   *terminalpositionsdb.Queries
	now func() time.Time
}

// NewService builds the service over db.
func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db, q: terminalpositionsdb.New(db)}
}

// timestamp is Fluent's Date() for @Timestamp fields, at the microsecond
// precision Postgres keeps.
func (s *Service) timestamp() time.Time {
	t := time.Now()
	if s.now != nil {
		t = s.now()
	}
	return t.UTC().Truncate(time.Microsecond)
}

// MARK: - Validation

var (
	upper         = cases.Upper(language.Und)
	tickerPattern = regexp.MustCompile(`^[A-Z0-9.\-]{1,12}$`)
)

// swiftUpper is String.uppercased(): full Unicode case mapping.
func swiftUpper(s string) string { return upper.String(s) }

// NormalisedTicker is TerminalPositionsService.normalisedTicker: trimmed,
// uppercased, 1-12 of A-Z 0-9 . -, else a 422 *httpx.Error.
func NormalisedTicker(raw string) (string, error) {
	ticker := swiftUpper(portfolios.TrimSwift(raw))
	if !tickerPattern.MatchString(ticker) {
		return "", httpx.Unprocessable("ticker must be 1-12 letters, digits, '.' or '-'")
	}
	return ticker, nil
}

func checkFinite(v float64, field string) (float64, error) {
	if !finite(v) {
		return 0, httpx.Unprocessable(field + " must be a number")
	}
	if math.Abs(v) > MaxInput {
		return 0, httpx.Unprocessable(field + " is too large")
	}
	return v, nil
}

func checkNonNegative(v float64, field string) (float64, error) {
	if _, err := checkFinite(v, field); err != nil {
		return 0, err
	}
	if !(v >= 0) {
		return 0, httpx.Unprocessable(field + " must be zero or more")
	}
	return v, nil
}

func checkPositive(v float64, field string) (float64, error) {
	if _, err := checkFinite(v, field); err != nil {
		return 0, err
	}
	if !(v > 0) {
		return 0, httpx.Unprocessable(field + " must be greater than zero")
	}
	return v, nil
}

func optionalPositive(v *float64, field string) (*float64, error) {
	if v == nil {
		return nil, nil
	}
	out, err := checkPositive(*v, field)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func checkNotes(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := portfolios.TrimSwift(*raw)
	if trimmed == "" {
		return nil, nil
	}
	if boards.CharCount(trimmed) > MaxNotesLength {
		return nil, httpx.Unprocessable(fmt.Sprintf("notes must be %d characters or fewer", MaxNotesLength))
	}
	return &trimmed, nil
}

func clearFields(raw *[]string) (map[string]bool, error) {
	requested := map[string]bool{}
	if raw != nil {
		for _, f := range *raw {
			requested[f] = true
		}
	}
	var unknown []string
	for f := range requested {
		if !clearablePositionFields[f] {
			unknown = append(unknown, f)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, httpx.Unprocessable("cannot clear: " + strings.Join(unknown, ", "))
	}
	return requested, nil
}

// MARK: - Positions

// List is TerminalPositionsService.list(userId:ticker:on:): the user's rows
// by sortOrder then createdAt. A non-blank ticker (trimmed, uppercased, not
// validated) filters to that ticker.
func (s *Service) List(ctx context.Context, userID uuid.UUID, ticker *string) ([]Position, error) {
	if ticker != nil {
		if t := portfolios.TrimSwift(*ticker); t != "" {
			return s.q.ListTerminalPositionsByTicker(ctx, terminalpositionsdb.ListTerminalPositionsByTickerParams{
				UserID: userID, Ticker: swiftUpper(t),
			})
		}
	}
	return s.q.ListTerminalPositions(ctx, userID)
}

func (s *Service) find(ctx context.Context, userID, id uuid.UUID) (Position, error) {
	row, err := s.q.OwnedTerminalPosition(ctx, terminalpositionsdb.OwnedTerminalPositionParams{UserID: userID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Position{}, httpx.NotFound("Terminal position not found.")
	}
	return row, err
}

// Create is TerminalPositionsService.create: validated, appended after the
// user's last row.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, in CreateRequest) (Position, error) {
	nextOrder := int64(0)
	last, err := s.q.LastTerminalPosition(ctx, userID)
	switch {
	case err == nil:
		nextOrder = last.SortOrder + 1
	case !errors.Is(err, pgx.ErrNoRows):
		return Position{}, err
	}
	// Field order is the Swift initializer's argument order, which decides
	// which validation error a client sees first.
	ticker, err := NormalisedTicker(in.Ticker)
	if err != nil {
		return Position{}, err
	}
	sharesOutstanding, err := optionalPositive(in.SharesOutstanding, "sharesOutstanding")
	if err != nil {
		return Position{}, err
	}
	shareCount, err := checkFinite(in.TerminalShareCount, "terminalShareCount")
	if err != nil {
		return Position{}, err
	}
	marketCap, err := checkFinite(in.TerminalMarketCap, "terminalMarketCap")
	if err != nil {
		return Position{}, err
	}
	valueWanted, err := checkNonNegative(in.ValueWanted, "valueWanted")
	if err != nil {
		return Position{}, err
	}
	owned := 0.0
	if in.SharesOwned != nil {
		owned = *in.SharesOwned
	}
	sharesOwned, err := checkNonNegative(owned, "sharesOwned")
	if err != nil {
		return Position{}, err
	}
	price, err := optionalPositive(in.CurrentSharePrice, "currentSharePrice")
	if err != nil {
		return Position{}, err
	}
	notes, err := checkNotes(in.Notes)
	if err != nil {
		return Position{}, err
	}
	now := s.timestamp()
	row := Position{
		ID:                 uuid.New(),
		UserID:             userID,
		Ticker:             ticker,
		SharesOutstanding:  sharesOutstanding,
		TerminalShareCount: shareCount,
		TerminalMarketCap:  marketCap,
		ValueWanted:        valueWanted,
		SharesOwned:        sharesOwned,
		CurrentSharePrice:  price,
		Notes:              notes,
		SortOrder:          nextOrder,
		CreatedAt:          &now,
		UpdatedAt:          &now,
	}
	if err := s.q.InsertTerminalPosition(ctx, terminalpositionsdb.InsertTerminalPositionParams(row)); err != nil {
		return Position{}, err
	}
	return row, nil
}

// Update is TerminalPositionsService.update: only sent fields change, then
// Clear nils the clearable ones. Like Fluent, a request that assigns nothing
// writes nothing and leaves updatedAt alone.
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, in UpdateRequest) (Position, error) {
	row, err := s.find(ctx, userID, id)
	if err != nil {
		return Position{}, err
	}
	clear, err := clearFields(in.Clear)
	if err != nil {
		return Position{}, err
	}
	changed := false
	if in.Ticker != nil {
		if row.Ticker, err = NormalisedTicker(*in.Ticker); err != nil {
			return Position{}, err
		}
		changed = true
	}
	if in.SharesOutstanding != nil {
		if row.SharesOutstanding, err = optionalPositive(in.SharesOutstanding, "sharesOutstanding"); err != nil {
			return Position{}, err
		}
		changed = true
	}
	if in.TerminalShareCount != nil {
		if row.TerminalShareCount, err = checkFinite(*in.TerminalShareCount, "terminalShareCount"); err != nil {
			return Position{}, err
		}
		changed = true
	}
	if in.TerminalMarketCap != nil {
		if row.TerminalMarketCap, err = checkFinite(*in.TerminalMarketCap, "terminalMarketCap"); err != nil {
			return Position{}, err
		}
		changed = true
	}
	if in.ValueWanted != nil {
		if row.ValueWanted, err = checkNonNegative(*in.ValueWanted, "valueWanted"); err != nil {
			return Position{}, err
		}
		changed = true
	}
	if in.SharesOwned != nil {
		if row.SharesOwned, err = checkNonNegative(*in.SharesOwned, "sharesOwned"); err != nil {
			return Position{}, err
		}
		changed = true
	}
	if in.CurrentSharePrice != nil {
		if row.CurrentSharePrice, err = optionalPositive(in.CurrentSharePrice, "currentSharePrice"); err != nil {
			return Position{}, err
		}
		changed = true
	}
	if in.Notes != nil {
		if row.Notes, err = checkNotes(in.Notes); err != nil {
			return Position{}, err
		}
		changed = true
	}
	if clear["sharesOutstanding"] {
		row.SharesOutstanding = nil
		changed = true
	}
	if clear["currentSharePrice"] {
		row.CurrentSharePrice = nil
		changed = true
	}
	if clear["notes"] {
		row.Notes = nil
		changed = true
	}
	if !changed {
		return row, nil
	}
	now := s.timestamp()
	row.UpdatedAt = &now
	err = s.q.UpdateTerminalPosition(ctx, terminalpositionsdb.UpdateTerminalPositionParams{
		Ticker:             row.Ticker,
		SharesOutstanding:  row.SharesOutstanding,
		TerminalShareCount: row.TerminalShareCount,
		TerminalMarketCap:  row.TerminalMarketCap,
		ValueWanted:        row.ValueWanted,
		SharesOwned:        row.SharesOwned,
		CurrentSharePrice:  row.CurrentSharePrice,
		Notes:              row.Notes,
		UpdatedAt:          row.UpdatedAt,
		ID:                 row.ID,
	})
	if err != nil {
		return Position{}, err
	}
	return row, nil
}

// Delete is TerminalPositionsService.delete.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	row, err := s.find(ctx, userID, id)
	if err != nil {
		return err
	}
	return s.q.DeleteTerminalPosition(ctx, row.ID)
}

// Duplicate is TerminalPositionsService.duplicate: the copy goes right
// after its source; later rows shift down by one.
func (s *Service) Duplicate(ctx context.Context, userID, id uuid.UUID) (Position, error) {
	source, err := s.find(ctx, userID, id)
	if err != nil {
		return Position{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Position{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	err = q.ShiftTerminalPositionsAfter(ctx, terminalpositionsdb.ShiftTerminalPositionsAfterParams{
		UpdatedAt: ptr(s.timestamp()), UserID: userID, SortOrder: source.SortOrder,
	})
	if err != nil {
		return Position{}, err
	}
	now := s.timestamp()
	cp := source
	cp.ID = uuid.New()
	cp.SortOrder = source.SortOrder + 1
	cp.CreatedAt, cp.UpdatedAt = &now, &now
	if err := q.InsertTerminalPosition(ctx, terminalpositionsdb.InsertTerminalPositionParams(cp)); err != nil {
		return Position{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Position{}, err
	}
	return cp, nil
}

// Reorder is TerminalPositionsService.reorder: ids must list every row
// exactly once; the list afterwards is returned.
func (s *Service) Reorder(ctx context.Context, userID uuid.UUID, ids []string) ([]Position, error) {
	uuids := make([]uuid.UUID, 0, len(ids))
	for _, raw := range ids {
		id, ok := parseSwiftUUID(raw)
		if !ok {
			return nil, httpx.Unprocessable("invalid id " + raw)
		}
		uuids = append(uuids, id)
	}
	rows, err := s.q.OwnedTerminalPositions(ctx, userID)
	if err != nil {
		return nil, err
	}
	owned := map[uuid.UUID]bool{}
	for i := range rows {
		owned[rows[i].ID] = true
	}
	requested := map[uuid.UUID]bool{}
	for _, id := range uuids {
		requested[id] = true
	}
	same := len(requested) == len(owned)
	for id := range requested {
		if !owned[id] {
			same = false
		}
	}
	if len(uuids) != len(rows) || len(requested) != len(uuids) || !same {
		return nil, httpx.Unprocessable("ids must list every terminal position exactly once")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	for i, id := range uuids {
		err := q.SetTerminalPositionSortOrder(ctx, terminalpositionsdb.SetTerminalPositionSortOrderParams{
			SortOrder: int64(i), UpdatedAt: ptr(s.timestamp()), ID: id,
		})
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.List(ctx, userID, nil)
}

// UpsertScenario is TerminalPositionsService.upsertScenario (assistant/MCP
// write): update the first row for the ticker, or create one, which needs
// TerminalShareCount, TerminalMarketCap and ValueWanted.
func (s *Service) UpsertScenario(ctx context.Context, userID uuid.UUID, ticker string, fields ScenarioFields) (Position, error) {
	symbol, err := NormalisedTicker(ticker)
	if err != nil {
		return Position{}, err
	}
	existing, err := s.List(ctx, userID, &symbol)
	if err != nil {
		return Position{}, err
	}
	if len(existing) > 0 {
		return s.Update(ctx, userID, existing[0].ID, UpdateRequest{
			SharesOutstanding:  fields.SharesOutstanding,
			TerminalShareCount: fields.TerminalShareCount,
			TerminalMarketCap:  fields.TerminalMarketCap,
			ValueWanted:        fields.ValueWanted,
			SharesOwned:        fields.SharesOwned,
			CurrentSharePrice:  fields.CurrentSharePrice,
		})
	}
	if fields.TerminalShareCount == nil || fields.TerminalMarketCap == nil || fields.ValueWanted == nil {
		return Position{}, httpx.Unprocessable("a new scenario needs terminalShareCount, terminalMarketCap and valueWanted")
	}
	return s.Create(ctx, userID, CreateRequest{
		Ticker:             symbol,
		SharesOutstanding:  fields.SharesOutstanding,
		TerminalShareCount: *fields.TerminalShareCount,
		TerminalMarketCap:  *fields.TerminalMarketCap,
		ValueWanted:        *fields.ValueWanted,
		SharesOwned:        fields.SharesOwned,
		CurrentSharePrice:  fields.CurrentSharePrice,
	})
}

// MARK: - Currency and summary

// Currency is TerminalPositionsService.currency: there is no user-level
// currency, so the default portfolio's, then any portfolio's, then
// MARKET_DEFAULT_CURRENCY (read per call, as Vapor did), then "USD".
func (s *Service) Currency(ctx context.Context, userID uuid.UUID) (string, error) {
	c, err := s.q.DefaultPortfolioCurrency(ctx, userID)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	c, err = s.q.AnyPortfolioCurrency(ctx, userID)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if v, ok := os.LookupEnv("MARKET_DEFAULT_CURRENCY"); ok {
		return v, nil
	}
	return "USD", nil
}

// ListResponseFor builds TerminalPositionsListResponse for rows.
func (s *Service) ListResponseFor(ctx context.Context, userID uuid.UUID, rows []Position) (ListResponse, error) {
	currency, err := s.Currency(ctx, userID)
	if err != nil {
		return ListResponse{}, err
	}
	out := ListResponse{Currency: currency, Positions: make([]PositionResponse, 0, len(rows))}
	for i := range rows {
		out.Positions = append(out.Positions, ToResponse(rows[i]))
	}
	return out, nil
}

// monthlyAutobuyTotal is TerminalPositionsService.monthlyTotal.
func (s *Service) monthlyAutobuyTotal(ctx context.Context, userID uuid.UUID) (float64, error) {
	rows, err := s.q.ListAutobuys(ctx, userID)
	if err != nil {
		return 0, err
	}
	values := make([]float64, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		v := 0.0
		if r.Active {
			if m, ok := monthlyEquivalent(r.Amount, r.Cadence, r.Percent); ok {
				v = m
			}
		}
		values = append(values, v)
	}
	return finiteSum(values), nil
}

// Summary is TerminalPositionsService.summary: totals over valid rows.
func (s *Service) Summary(ctx context.Context, userID uuid.UUID) (SummaryResponse, error) {
	rows, err := s.List(ctx, userID, nil)
	if err != nil {
		return SummaryResponse{}, err
	}
	positions := make([]PositionResponse, 0, len(rows))
	for i := range rows {
		positions = append(positions, ToResponse(rows[i]))
	}
	var valid []PositionResponse
	var wanted, gaps, priced []float64
	for i := range positions {
		p := &positions[i]
		if p.ScenarioError != nil {
			continue
		}
		valid = append(valid, *p)
		wanted = append(wanted, p.ValueWanted)
		gap := 0.0
		if p.GapValueAtTerminal != nil {
			gap = *p.GapValueAtTerminal
		}
		gaps = append(gaps, gap)
		if p.CapitalAtTodayPrice != nil {
			priced = append(priced, *p.CapitalAtTodayPrice)
		}
	}
	monthly, err := s.monthlyAutobuyTotal(ctx, userID)
	if err != nil {
		return SummaryResponse{}, err
	}
	currency, err := s.Currency(ctx, userID)
	if err != nil {
		return SummaryResponse{}, err
	}
	var totalCapital *float64
	if len(priced) > 0 {
		totalCapital = ptr(finiteSum(priced))
	}
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].ValueWanted > valid[j].ValueWanted })
	top := make([]PositionResponse, 0, 3)
	for i := 0; i < len(valid) && i < 3; i++ {
		top = append(top, valid[i])
	}
	return SummaryResponse{
		Currency:                 currency,
		PositionCount:            len(positions),
		TotalValueWanted:         finiteSum(wanted),
		TotalGapValueAtTerminal:  finiteSum(gaps),
		TotalCapitalAtTodayPrice: totalCapital,
		PricedPositionCount:      len(priced),
		MonthlyAutobuyTotal:      monthly,
		TopPositions:             top,
	}, nil
}

// ToResponse is TerminalPositionRecord.toResponse(): the stored inputs plus
// the TerminalMath numbers, or scenarioError instead of them.
func ToResponse(r Position) PositionResponse {
	res, scenarioErr := evaluate(scenarioInput{
		terminalShareCount: r.TerminalShareCount,
		terminalMarketCap:  r.TerminalMarketCap,
		valueWanted:        r.ValueWanted,
		sharesOwned:        r.SharesOwned,
		currentSharePrice:  r.CurrentSharePrice,
	})
	created := time.Now()
	if r.CreatedAt != nil {
		created = *r.CreatedAt
	}
	updated := created
	if r.UpdatedAt != nil {
		updated = *r.UpdatedAt
	}
	out := PositionResponse{
		ID:                 apijson.FormatUUID(r.ID),
		Ticker:             r.Ticker,
		SharesOutstanding:  r.SharesOutstanding,
		TerminalShareCount: r.TerminalShareCount,
		TerminalMarketCap:  r.TerminalMarketCap,
		ValueWanted:        r.ValueWanted,
		SharesOwned:        r.SharesOwned,
		CurrentSharePrice:  r.CurrentSharePrice,
		Notes:              r.Notes,
		SortOrder:          int(r.SortOrder),
		CreatedAt:          apijson.FormatTime(created),
		UpdatedAt:          apijson.FormatTime(updated),
	}
	if scenarioErr != "" {
		out.ScenarioError = &scenarioErr
		return out
	}
	out.TerminalSharePrice = ptr(res.terminalSharePrice)
	out.SharesNeeded = ptr(res.sharesNeeded)
	out.CapitalAtTodayPrice = res.capitalAtTodayPrice
	out.Progress = ptr(res.progress)
	out.SharesStillNeeded = ptr(res.sharesStillNeeded)
	out.GapValueAtTerminal = ptr(res.gapValueAtTerminal)
	return out
}

func ptr[T any](v T) *T { return &v }

// parseSwiftUUID is UUID(uuidString:): the 36-character hyphenated form
// only, any case.
func parseSwiftUUID(raw string) (uuid.UUID, bool) {
	if len(raw) != 36 {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
}
