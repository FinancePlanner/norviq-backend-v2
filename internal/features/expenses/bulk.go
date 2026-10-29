package expenses

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/expensesdb"

	"github.com/google/uuid"
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

// Insert creates the accepted rows (each its own statement; a failing row
// does not affect the others), ensuring each touched month's snapshot once,
// then re-evaluates drift per month.
func (b BulkImporter) Insert(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, candidates []Candidate) (InsertOutcome, error) {
	out := InsertOutcome{Failures: map[int]string{}, Months: []time.Time{}}
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
		if err := b.insertExpense(ctx, db, userID, c.Request); err != nil {
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

func (b BulkImporter) insertExpense(ctx context.Context, db expensesdb.DBTX, userID uuid.UUID, req ExpenseRequest) error {
	occurredOn, ok := ParseDay(req.OccurredOn)
	if !ok {
		return httpx.BadRequest("Invalid occurredOn format. Expected YYYY-MM-DD.")
	}
	split := req.SplitMode
	if split == "" {
		split = SplitPersonal
	}
	e := expensesdb.Expense{
		ID: uuid.New(), UserID: userID, Title: req.Title, Amount: req.Amount, Pillar: string(req.Pillar),
		OccurredOn: occurredOn, SplitMode: expensesdb.ExpenseSplitMode(split), UserSharePercent: req.UserSharePercent,
		CategoryID: uuidPtrFromString(req.CategoryID),
	}
	return b.Service.insertExpense(ctx, db, &e)
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
