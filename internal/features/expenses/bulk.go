package expenses

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// BulkImporter is ExpenseBulkImporter: the shared write path for bulk
// expense imports (CSV, spreadsheet, receipts). Callers own parsing and
// validation; it owns dedup, snapshot creation, inserts and drift.
type BulkImporter struct {
	Service *Service
	// EvaluateDrift mirrors Swift's optional request: false skips drift
	// evaluation after insert (the spreadsheet commit retry path).
	EvaluateDrift bool
}

// NewBulkImporter is ExpenseBulkImporter(expensesService:request:) with a
// request (drift evaluated after insert).
func NewBulkImporter(s *Service) BulkImporter { return BulkImporter{Service: s, EvaluateDrift: true} }

// Candidate is a row a caller has already parsed and validated.
type Candidate struct {
	// Reference is echoed back in failures (CSV line, worksheet row, ...).
	Reference int
	Request   ExpenseRequest
	// ExternalID, when present, alone determines duplicate identity.
	ExternalID *string
}

// InsertOutcome is ExpenseBulkImporter.InsertOutcome.
type InsertOutcome struct {
	Imported int
	// Failures is keyed by Candidate.Reference.
	Failures map[int]string
	Months   []time.Time
	// Duplicates are the references of rows whose external id another
	// insert stored after the caller's dedupe ran (a concurrent import).
	// Callers report them as the duplicates they are, not as failures.
	Duplicates []int
}

// MaxExternalIDLength bounds a stored external id. The unique index entry
// must fit a btree page, and no real reference comes close.
const MaxExternalIDLength = 255

// externalIDIndex is the partial unique index on expenses
// (user_id, external_id) from migration 00003.
const externalIDIndex = "expenses_user_id_external_id_key"

// ExternalIDOf is the external id a candidate stores and dedupes on: nil
// when absent or empty, as DedupKey treats an empty one.
func ExternalIDOf(c *Candidate) *string {
	if c.ExternalID == nil || *c.ExternalID == "" {
		return nil
	}
	return c.ExternalID
}

// DedupKey is ExpenseBulkImporter.dedupKey: "ext:<id>" when an external id
// is present, else "occurredOn|amount|title" with Swift's Double
// description and a lowercased title.
func DedupKey(occurredOn string, amount float64, title string, externalID *string) string {
	if externalID != nil && *externalID != "" {
		return "ext:" + *externalID
	}
	return occurredOn + "|" + marketdata.SwiftDoubleString(amount) + "|" + strings.ToLower(title)
}

// DayRange is an inclusive span of days.
type DayRange struct{ From, To time.Time }

// OccurredOnRange is ExpenseBulkImporter.occurredOnRange(of:): nil when no
// candidate's occurredOn parses.
func OccurredOnRange(candidates []Candidate) *DayRange {
	var r *DayRange
	for k := range candidates {
		c := &candidates[k]
		d, ok := ParseDay(c.Request.OccurredOn)
		if !ok {
			continue
		}
		if r == nil {
			r = &DayRange{From: d, To: d}
			continue
		}
		if d.Before(r.From) {
			r.From = d
		}
		if d.After(r.To) {
			r.To = d
		}
	}
	return r
}

// ExistingDedupKeys preloads dedup keys for expenses the user already has,
// narrowed to the incoming document's day span padded by a day each side
// (capped at 10 000 rows, as in Vapor).
func (b BulkImporter) ExistingDedupKeys(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, r *DayRange) (map[string]struct{}, error) {
	var from, to *time.Time
	if r != nil {
		from = timePtr(r.From.Add(-24 * time.Hour))
		to = timePtr(r.To.Add(24 * time.Hour))
	}
	items, _, err := b.Service.GetExpenses(ctx, db, userID, from, to, 10000, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(items))
	for k := range items {
		e := &items[k]
		out[DedupKey(e.OccurredOn, e.Amount, e.Title, nil)] = struct{}{}
	}
	return out, nil
}

// ExistingExternalIDs returns which of ids the user already stored as an
// expense's external id, whatever its date. (Vapor had no external id
// column, so a row with one was never matched against stored expenses.)
func (b BulkImporter) ExistingExternalIDs(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, ids []string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if len(ids) == 0 {
		return out, nil
	}
	stored, err := q(db).ListExpenseExternalIDs(ctx, expensesdb.ListExpenseExternalIDsParams{UserID: userID, ExternalIds: ids})
	if err != nil {
		return nil, err
	}
	for _, id := range stored {
		if id != nil {
			out[*id] = struct{}{}
		}
	}
	return out, nil
}

// ExistingKeys is ExistingDedupKeys over the candidates' day span plus the
// "ext:<id>" DedupKey of every candidate external id already stored, so a
// candidate with an external id is matched against stored rows by it.
func (b BulkImporter) ExistingKeys(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, candidates []Candidate) (map[string]struct{}, error) {
	out, err := b.ExistingDedupKeys(ctx, db, userID, OccurredOnRange(candidates))
	if err != nil {
		return nil, err
	}
	var ids []string
	for k := range candidates {
		if ext := ExternalIDOf(&candidates[k]); ext != nil {
			ids = append(ids, *ext)
		}
	}
	stored, err := b.ExistingExternalIDs(ctx, db, userID, ids)
	if err != nil {
		return nil, err
	}
	for id := range stored {
		out[DedupKey("", 0, "", &id)] = struct{}{}
	}
	return out, nil
}

// Insert creates the accepted rows (each its own statement; a failing row
// does not affect the others), ensuring each touched month's snapshot once,
// then re-evaluates drift per month.
func (b BulkImporter) Insert(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, candidates []Candidate) (InsertOutcome, error) {
	out := InsertOutcome{Failures: map[int]string{}, Months: []time.Time{}, Duplicates: []int{}}
	if len(candidates) == 0 {
		return out, nil
	}
	seen := map[time.Time]bool{}
	for k := range candidates {
		c := &candidates[k]
		if m, ok := MonthStartOf(c.Request.OccurredOn); ok && !seen[m] {
			seen[m] = true
			out.Months = append(out.Months, m)
		}
	}
	sort.Slice(out.Months, func(i, j int) bool { return out.Months[i].Before(out.Months[j]) })
	for _, m := range out.Months {
		if err := b.Service.EnsureSnapshotExists(ctx, db, userID, m); err != nil {
			return InsertOutcome{}, err
		}
	}
	for k := range candidates {
		c := &candidates[k]
		err := b.insertExpense(ctx, db, userID, c.Request, ExternalIDOf(c))
		if isExternalIDConflict(err) {
			out.Duplicates = append(out.Duplicates, c.Reference)
			continue
		}
		if err != nil {
			out.Failures[c.Reference] = Friendly(err)
			continue
		}
		out.Imported++
	}
	if b.EvaluateDrift {
		b.Service.EvaluateDrift(ctx, userID, out.Months, "budget drift evaluation after bulk import failed")
	}
	return out, nil
}

// insertExpense validates and inserts one row with the same checks as a
// single create (amount >= 0, split normalisation, owned plan item and
// category) and keeps every field: foreign amount and currency, exchange
// rate, notes, receipt metadata and the linked plan item. Vapor's bulk path
// skipped the checks and dropped those fields. It also stores the row's
// external id, which Vapor had no column for.
func (b BulkImporter) insertExpense(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, req ExpenseRequest, externalID *string) error {
	if externalID != nil && utf8.RuneCountInString(*externalID) > MaxExternalIDLength {
		return httpx.BadRequest(fmt.Sprintf("externalId must be at most %d characters.", MaxExternalIDLength))
	}
	e, err := b.Service.newExpense(ctx, db, userID, req)
	if err != nil {
		return err
	}
	e.ExternalID = externalID
	return b.Service.insertExpense(ctx, db, &e)
}

// isExternalIDConflict is a unique violation on the external id index: the
// user already has an expense with this external id.
func isExternalIDConflict(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == externalIDIndex
}

// Friendly is ExpenseBulkImporter.friendly: an Abort's reason, else the
// error's description.
func Friendly(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		if he.Reason == "" {
			return http.StatusText(he.Status)
		}
		return he.Reason
	}
	return err.Error()
}
