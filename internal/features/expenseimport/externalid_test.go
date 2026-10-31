package expenseimport

import (
	"context"
	"testing"

	"norviq-api/internal/features/expenses"
	"norviq-api/internal/platform/entitlements"

	"github.com/google/uuid"
)

// I4 (docs/vapor-bugs/money.md): a column mapped to externalId is stored on
// each imported expense, and a row whose external id is already stored, or
// repeats an earlier row's, is a duplicate. Vapor ignored the column.

func extSheet(rows ...StoredRow) []StoredSheet {
	return []StoredSheet{{Name: "Sheet1", Rows: rows}}
}

func extMapping() StoredMapping {
	pillar := "fundamentals"
	return StoredMapping{
		ColumnRoles: map[string]string{"A": "date", "B": "title", "C": "amount", "D": "category", "E": "externalId"},
		Categories:  []StoredCategory{{SourceValue: "Food", Pillar: &pillar, Source: "user"}},
		DateOrder:   string(DayFirst), DecimalSeparator: string(SeparatorDot), ExchangeRates: map[string]float64{},
	}
}

func row(n int, date, title, amount, ext string) StoredRow {
	cells := map[string]string{"A": date, "B": title, "C": amount, "D": "Food"}
	if ext != "" {
		cells["E"] = ext
	}
	return StoredRow{Row: n, Cells: cells}
}

func statuses(d derivation) []RowStatus {
	out := make([]RowStatus, len(d.rows))
	for i := range d.rows {
		out[i] = d.rows[i].status
	}
	return out
}

func candidatesOf(d derivation) []Candidate {
	var out []Candidate
	for i := range d.rows {
		if r := &d.rows[i]; r.status == StatusOK && r.request != nil {
			out = append(out, Candidate{Reference: r.reference, Request: *r.request})
		}
	}
	return out
}

func TestSpreadsheetExternalIDIsStoredAndDeduped(t *testing.T) {
	e := newImportEnv(t, nil, entitlements.Config{BypassBilling: true})
	ctx := context.Background()
	userID := e.app.CreateUser("xlsx_ext_" + uuid.NewString()[:8] + "@example.com")
	svc, err := expenses.SharedService(e.app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	adapter := bulkExpenses{svc: svc, bulk: expenses.NewBulkImporter(svc)}
	s := &Service{Expenses: adapter}

	first, err := s.derive(ctx, extSheet(
		row(2, "2026-07-05", "Coffee", "4.50", " TX-1 "),
		row(3, "2026-07-06", "Lunch", "12", "TX-2"),
		row(4, "2026-07-07", "Lunch, other day", "13", "TX-2"), // same external id
		row(5, "2026-07-08", "Snack", "2", ""),
	), extMapping(), map[string]RowOverride{}, userID)
	if err != nil {
		t.Fatal(err)
	}
	want := []RowStatus{StatusOK, StatusOK, StatusDuplicateInFile, StatusOK}
	if got := statuses(first); len(got) != 4 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("first statuses %v, want %v", got, want)
	}
	out, err := adapter.Insert(ctx, userID, candidatesOf(first))
	if err != nil || out.Imported != 3 {
		t.Fatalf("insert %+v %v", out, err)
	}
	stored := map[string]string{}
	rows, err := e.app.Deps.DB.Query(ctx, `SELECT title, coalesce(external_id, '<null>') FROM expenses WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var title, ext string
		if err := rows.Scan(&title, &ext); err != nil {
			t.Fatal(err)
		}
		stored[title] = ext
	}
	rows.Close()
	if stored["Coffee"] != "TX-1" || stored["Lunch"] != "TX-2" || stored["Snack"] != "<null>" {
		t.Fatalf("stored %v", stored)
	}

	// A later sheet: TX-1 again, with another title, amount and month.
	second, err := s.derive(ctx, extSheet(
		row(2, "2026-09-01", "Coffee shop", "5", "TX-1"),
		row(3, "2026-09-02", "New", "1", "TX-9"),
	), extMapping(), map[string]RowOverride{}, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(second); got[0] != StatusDuplicateExisting || got[1] != StatusOK ||
		second.rows[0].preview.Message == nil || *second.rows[0].preview.Message != duplicateExistingMessage {
		t.Fatalf("second statuses %v, message %v", got, second.rows[0].preview.Message)
	}

	// Stored between dedupe and insert (a concurrent import): a duplicate.
	tx2 := "TX-2"
	out, err = adapter.Insert(ctx, userID, []Candidate{{Reference: 7, Request: ExpenseDraft{
		Title: "Race", Amount: 1, Pillar: "fundamentals", OccurredOn: "2026-07-06", ExternalID: &tx2,
	}}})
	if err != nil || out.Imported != 0 || len(out.Duplicates) != 1 || out.Duplicates[0] != 7 || len(out.Failures) != 0 {
		t.Fatalf("race insert %+v %v", out, err)
	}
}
