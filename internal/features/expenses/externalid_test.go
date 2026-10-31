package expenses

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// I4 (docs/vapor-bugs/money.md): an import row's external id is stored on
// the expense, and a later import skips a row whose (user_id, external_id)
// is already stored. Vapor had no column, so it never matched them.

func (e *env) externalIDs(userID uuid.UUID) map[string]string {
	e.t.Helper()
	rows, err := e.app.Deps.DB.Query(context.Background(),
		`SELECT title, coalesce(external_id, '<null>') FROM expenses WHERE user_id = $1`, userID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var title, ext string
		if err := rows.Scan(&title, &ext); err != nil {
			e.t.Fatal(err)
		}
		out[title] = ext
	}
	return out
}

func TestCSVExternalIDIsStoredAndDedupedAgainstStoredRows(t *testing.T) {
	e := newEnv(t)
	token, userID := e.user()
	first := "title,amount,pillar,occurred_on,external_id\n" +
		"Coffee,4.50,fundamentals,2026-07-05,TX-1\n" +
		"Lunch,12.00,fun,2026-07-06,\n"
	if res := e.importCSV(token, "dry_run=false", first); res.Imported != 2 {
		t.Fatalf("first %+v", res)
	}
	if got := e.externalIDs(userID); got["Coffee"] != "TX-1" || got["Lunch"] != "<null>" {
		t.Fatalf("stored external ids %v", got)
	}

	// Same external id, but a different title, amount and a date far outside
	// the date window the title/amount/date dedupe looks at.
	again := "title,amount,pillar,occurred_on,external_id\n" +
		"Coffee (bank wording),4.55,fundamentals,2026-09-20,TX-1\n" +
		"Dinner,30,fun,2026-09-21,TX-3\n"
	for _, dry := range []string{"dry_run=true", "dry_run=false"} {
		res := e.importCSV(token, dry, again)
		if res.Imported != 1 || res.Skipped != 1 || res.Rows[0].Outcome != RowSkippedDuplicate || res.Rows[1].Outcome != RowImported {
			t.Fatalf("%s: %+v", dry, res)
		}
	}
	if got := e.externalIDs(userID); len(got) != 3 || got["Dinner"] != "TX-3" {
		t.Fatalf("after re-import %v", got)
	}

	// External ids are per user.
	other, otherID := e.user()
	if res := e.importCSV(other, "dry_run=false", again); res.Imported != 2 {
		t.Fatalf("other user %+v", res)
	}
	if got := e.externalIDs(otherID); got["Coffee (bank wording)"] != "TX-1" {
		t.Fatalf("other user ids %v", got)
	}
}

// A row whose external id was stored after the caller's dedupe ran (a
// concurrent import) hits the unique index and comes back as a duplicate,
// not as a failure; an over-long id fails on its own.
func TestBulkInsertReportsExternalIDConflictAsDuplicate(t *testing.T) {
	e := newEnv(t)
	token, userID := e.user()
	if res := e.importCSV(token, "dry_run=false", "title,amount,pillar,occurred_on,external_id\nCoffee,4.50,fundamentals,2026-07-05,TX-1\n"); res.Imported != 1 {
		t.Fatalf("seed %+v", res)
	}
	svc, err := SharedService(e.app.Deps)
	if err != nil {
		t.Fatal(err)
	}
	ext, empty, long := "TX-1", "", strings.Repeat("x", MaxExternalIDLength+1)
	fresh := "TX-2"
	candidates := []Candidate{
		{Reference: 1, Request: NewExpenseRequest("Coffee again", 4.5, PillarFundamentals, "2026-07-05", nil), ExternalID: &ext},
		{Reference: 2, Request: NewExpenseRequest("New", 1, PillarFundamentals, "2026-07-05", nil), ExternalID: &fresh},
		{Reference: 3, Request: NewExpenseRequest("No id", 1, PillarFundamentals, "2026-07-05", nil), ExternalID: &empty},
		{Reference: 4, Request: NewExpenseRequest("Too long", 1, PillarFundamentals, "2026-07-05", nil), ExternalID: &long},
	}
	out, err := NewBulkImporter(svc).Insert(context.Background(), svc.Pool(), userID, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if out.Imported != 2 || len(out.Duplicates) != 1 || out.Duplicates[0] != 1 || len(out.Failures) != 1 ||
		out.Failures[4] != "externalId must be at most 255 characters." {
		t.Fatalf("outcome %+v", out)
	}
	if got := e.externalIDs(userID); len(got) != 3 || got["New"] != "TX-2" || got["No id"] != "<null>" {
		t.Fatalf("stored %v", got)
	}

	keys, err := NewBulkImporter(svc).ExistingKeys(context.Background(), svc.Pool(), userID, candidates[:2])
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ext:TX-1", "ext:TX-2", "2026-07-05|4.5|coffee"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("ExistingKeys lacks %q: %v", k, keys)
		}
	}
}
