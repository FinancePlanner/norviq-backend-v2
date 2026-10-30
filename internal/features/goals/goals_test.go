package goals_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/goals"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

func newApp(t *testing.T, checks *[]uuid.UUID) *testutil.App {
	t.Helper()
	return testutil.NewApp(t, []server.Feature{goals.Feature{ScheduleAchievementCheck: func(id uuid.UUID) {
		*checks = append(*checks, id)
	}}})
}

func expect(t *testing.T, r *testutil.Response, status int, contains string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
	if contains != "" && !strings.Contains(string(r.Body), contains) {
		t.Fatalf("body %s does not contain %q", r.Body, contains)
	}
}

// StockPlanBackendTests: create a goal, then complete it.
func TestCreateAndCompleteGoal(t *testing.T) {
	var checks []uuid.UUID
	app := newApp(t, &checks)
	user := app.CreateUser("goals@example.com")
	token := app.Token(user)

	r := app.Do(http.MethodPost, "/v1/goals", token, map[string]any{"title": "Review watchlist before earnings"})
	expect(t, r, http.StatusOK, `"status":"pending"`)
	var created goals.Response
	r.JSON(t, &created)
	if created.CompletedAt != nil || created.StatusUpdatedBy == nil || *created.StatusUpdatedBy != "manual" || created.CreatedAt == nil || created.UpdatedAt == nil {
		t.Fatalf("created %+v", created)
	}
	if len(checks) != 0 {
		t.Fatal("creating a goal schedules no achievement check")
	}

	r = app.Do(http.MethodPatch, "/v1/goals/"+created.ID+"/status", token, map[string]any{"status": "completed", "source": "manual"})
	expect(t, r, http.StatusOK, `"status":"completed"`)
	var updated goals.Response
	r.JSON(t, &updated)
	if updated.CompletedAt == nil || *updated.StatusUpdatedBy != "manual" {
		t.Fatalf("updated %+v", updated)
	}
	if len(checks) != 1 || checks[0] != user {
		t.Fatalf("achievement checks %v", checks)
	}

	// The stored row reads back the same.
	r = app.Do(http.MethodGet, "/v1/goals/"+created.ID, token, nil)
	expect(t, r, http.StatusOK, *updated.CompletedAt)

	r = app.Do(http.MethodPatch, "/v1/goals/"+created.ID+"/status", token, map[string]any{"status": "pending", "source": "system"})
	expect(t, r, http.StatusOK, `"statusUpdatedBy":"system"`)
	if strings.Contains(string(r.Body), "completedAt") {
		t.Fatalf("completedAt should be cleared: %s", r.Body)
	}
	// source is required (synthesized Codable).
	expect(t, app.Do(http.MethodPatch, "/v1/goals/"+created.ID+"/status", token, map[string]any{"status": "pending"}), http.StatusBadRequest, "source")
	expect(t, app.Do(http.MethodPatch, "/v1/goals/"+created.ID+"/status", token, map[string]any{"status": "done", "source": "manual"}), http.StatusBadRequest, "")

	r = app.Do(http.MethodPatch, "/v1/goals/"+created.ID, token, map[string]any{"title": "Renamed"})
	expect(t, r, http.StatusOK, `"title":"Renamed"`)

	r = app.Do(http.MethodGet, "/v1/goals", token, nil)
	expect(t, r, http.StatusOK, "")
	var list []goals.Response
	r.JSON(t, &list)
	if len(list) != 4 || list[0].ID != "00000000-0000-0000-0000-000000000001" || list[0].Title != "Add 3 stocks to your Watchlist (0/3)" ||
		list[0].Status != "pending" || list[0].CreatedAt != nil || list[0].UpdatedAt == nil || list[3].ID != created.ID {
		t.Fatalf("list %s", r.Body)
	}

	r = app.Do(http.MethodDelete, "/v1/goals/"+created.ID, token, nil)
	expect(t, r, http.StatusOK, "")
	if string(r.Body) != "{}" {
		t.Fatalf("delete body %s", r.Body)
	}
	expect(t, app.Do(http.MethodGet, "/v1/goals/"+created.ID, token, nil), http.StatusNotFound, `"reason":"Not Found"`)
	expect(t, app.Do(http.MethodGet, "/v1/goals/not-a-uuid", token, nil), http.StatusNotFound, "")
}

func TestSystemGoalsComplete(t *testing.T) {
	var checks []uuid.UUID
	app := newApp(t, &checks)
	user := app.CreateUser("system@example.com")
	ctx := context.Background()
	if _, err := app.Deps.DB.Exec(ctx, `INSERT INTO expenses (id, user_id, title, amount, pillar, occurred_on, created_at)
		VALUES ($1, $2, 'Coffee', 3, 'fun', CURRENT_DATE, now())`, uuid.New(), user); err != nil {
		t.Fatal(err)
	}
	r := app.Do(http.MethodGet, "/v1/goals", app.Token(user), nil)
	var list []goals.Response
	r.JSON(t, &list)
	if len(list) != 3 || list[2].Status != "completed" || list[2].CompletedAt == nil || list[1].Status != "pending" {
		t.Fatalf("list %s", r.Body)
	}
}

// MCPTokenAuthTests: goals answer to goals:read, not expenses:*.
func TestGoalsScopes(t *testing.T) {
	var checks []uuid.UUID
	app := newApp(t, &checks)
	user := app.CreateUser("scopes@example.com")
	pat := func(scopes ...string) string {
		token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		if _, err := app.Deps.DB.Exec(context.Background(), `INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
			VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`, uuid.New(), user, auth.SHA256Hex(token), scopes); err != nil {
			t.Fatal(err)
		}
		return token
	}
	expect(t, app.Do(http.MethodGet, "/v1/goals", pat("expenses:read", "expenses:write"), nil), http.StatusForbidden, "")
	expect(t, app.Do(http.MethodGet, "/v1/goals", pat("goals:read"), nil), http.StatusOK, "")
	expect(t, app.Do(http.MethodGet, "/v1/goals", "", nil), http.StatusUnauthorized, "")
}
