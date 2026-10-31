package portfoliomgmt_test

import (
	"net/http"
	"testing"
)

// POST /v1/portfolios makes the new portfolio the default when the user has
// no unarchived owned list. A user whose only default was archived (legacy
// data) then had two defaults; the archived one is now demoted in the same
// transaction, so there is exactly one.
func TestCreateDefaultDemotesArchivedDefault(t *testing.T) {
	e := newEnv(t)
	userID, token := e.user("mgmt-defaults@example.com")
	old := e.list(userID, "Old", true, "")
	e.exec(`UPDATE portfolio_lists SET archived_at = now() WHERE id = $1`, old)

	r := e.do(http.MethodPost, "/v1/portfolios", token, map[string]any{
		"name": "New", "purpose": "personal", "ownership": "individual", "mode": "actual", "baseCurrency": "USD", "copyRetirementPlan": false,
	})
	expectStatus(t, r, http.StatusCreated)
	var p portfolioDTO
	r.JSON(t, &p)
	if !p.IsDefault {
		t.Fatalf("new portfolio not default: %s", r.Body)
	}
	if n := e.count(`SELECT count(*) FROM portfolio_lists WHERE user_id = $1 AND is_default`, userID); n != 1 {
		t.Fatalf("defaults = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM portfolio_lists WHERE id = $1 AND is_default`, old); n != 0 {
		t.Fatal("archived default kept is_default")
	}
}
