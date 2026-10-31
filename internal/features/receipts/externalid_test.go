package receipts

import (
	"context"
	"net/http"
	"testing"
)

// I4: a receipt's externalId is stored, and a later commit with the same
// externalId is skipped even when its title, amount and date differ.
func TestReceiptCommitDedupesExternalIDAgainstStoredRows(t *testing.T) {
	e := newReceiptEnv(t, nil, nil)
	userID, token := e.proUser()
	ext := "PT-QR-ATCUD-1"
	commit := func(items ...any) CommitResponse {
		t.Helper()
		r := e.app.Do(http.MethodPost, "/v1/expenses/import/receipts/commit", token, map[string]any{"items": items})
		var resp CommitResponse
		r.JSON(t, &resp)
		if r.Status != http.StatusOK {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		return resp
	}
	if resp := commit(commitItem("Pingo Doce", 12.3, "2026-09-01", &ext)); resp.Imported != 1 {
		t.Fatalf("first %+v", resp)
	}
	var stored string
	if err := e.app.Deps.DB.QueryRow(context.Background(),
		`SELECT external_id FROM expenses WHERE user_id = $1`, userID).Scan(&stored); err != nil || stored != ext {
		t.Fatalf("stored external id %q %v", stored, err)
	}

	other := "PT-QR-ATCUD-2"
	resp := commit(
		commitItem("Pingo Doce (edited)", 12.5, "2026-10-15", &ext),
		commitItem("Continente", 7, "2026-10-15", &other),
	)
	if resp.Imported != 1 || resp.Skipped != 1 || resp.Failed != 0 {
		t.Fatalf("re-commit %+v", resp)
	}
	var n int
	if err := e.app.Deps.DB.QueryRow(context.Background(),
		`SELECT count(*) FROM expenses WHERE user_id = $1 AND external_id = $2`, userID, ext).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows with %s: %d %v", ext, n, err)
	}
}
