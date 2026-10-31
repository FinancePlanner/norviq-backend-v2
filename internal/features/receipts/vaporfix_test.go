package receipts

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Regression tests for the owner decisions in docs/vapor-bugs/money.md.

func (e *receiptEnv) proUser() (uuid.UUID, string) {
	e.t.Helper()
	id := e.app.CreateUser(fmt.Sprintf("rcpt_%s@example.com", uuid.NewString()[:8]))
	if _, err := e.app.Deps.DB.Exec(context.Background(),
		`INSERT INTO entitlements (id, user_id, level, created_at, updated_at) VALUES ($1, $2, 'pro', now(), now())`, uuid.New(), id); err != nil {
		e.t.Fatal(err)
	}
	return id, e.app.Token(id)
}

// I2: /ocr and /scan are Pro-gated but unmetered on purpose. Scanning never
// creates or changes the user's usage counters.
func TestReceiptScansAreNotMetered(t *testing.T) {
	p := &nothingRecognized{}
	e := newReceiptEnv(t, p, nil)
	userID, token := e.proUser()
	counters := func() string {
		t.Helper()
		var row string
		err := e.app.Deps.DB.QueryRow(context.Background(), `
			SELECT coalesce((SELECT concat_ws(',', holding_count, watchlist_item_count, csv_import_count, target_alert_count, report_generation_count)
			FROM usage_counters WHERE user_id = $1), 'none')`, userID).Scan(&row)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	before := counters()
	for range 3 {
		if r := e.raw("/v1/receipts/ocr", token, "image/jpeg", jpeg); r.Status != http.StatusOK {
			t.Fatalf("ocr %d %s", r.Status, r.Body)
		}
		body, ct := form(t, upload{"file", "a.jpg", jpeg}, upload{"file", "b.jpg", jpeg})
		if r := e.raw("/v1/receipts/scan", token, ct, body); r.Status != http.StatusOK {
			t.Fatalf("scan %d %s", r.Status, r.Body)
		}
	}
	if p.calls.Load() != 9 {
		t.Fatalf("provider calls %d", p.calls.Load())
	}
	if after := counters(); after != before {
		t.Fatalf("usage counters changed: %s -> %s", before, after)
	}
}
