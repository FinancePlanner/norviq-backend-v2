package activity_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/activity"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"
)

type item struct {
	ID        string   `json:"id"`
	UserID    string   `json:"userId"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Subtitle  string   `json:"subtitle"`
	Amount    *float64 `json:"amount"`
	IsGrowth  bool     `json:"isGrowth"`
	Symbol    string   `json:"symbol"`
	CreatedAt string   `json:"createdAt"`
}

func f64(v float64) *float64 { return &v }

func record(t *testing.T, app *testutil.App, rec activity.Record, at time.Time) {
	t.Helper()
	if err := activity.NewRecorder(app.Deps.DB).Record(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	// Pin created_at so the order is deterministic.
	if _, err := app.Deps.DB.Exec(context.Background(),
		`UPDATE user_activities SET created_at = $1 WHERE user_id = $2 AND subtitle = $3`, at, rec.UserID, rec.Subtitle); err != nil {
		t.Fatal(err)
	}
}

func TestActivities(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{activity.Feature{}})
	user := app.CreateUser("a@example.com")
	other := app.CreateUser("b@example.com")
	token := app.Token(user)

	// "Get activities returns empty array for new user"
	r := app.Do("GET", "/v1/activities", token, nil)
	if r.Status != 200 || string(r.Body) != "[]" {
		t.Fatalf("empty: %d %s", r.Status, r.Body)
	}

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	record(t, app, activity.Record{UserID: user, Type: activity.TypeStockAdded, Title: "Stock Added", Subtitle: "AAPL - Apple Inc.",
		Amount: f64(5000), IsGrowth: true, Symbol: "chart.line.uptrend.xyaxis"}, base)
	record(t, app, activity.Record{UserID: user, Type: activity.TypeExpenseRecorded, Title: "Expense Added", Subtitle: "Groceries",
		Amount: f64(150), Symbol: "house.fill"}, base.Add(time.Minute))
	record(t, app, activity.Record{UserID: other, Type: activity.TypeStockAdded, Title: "Stock Added", Subtitle: "MSFT", Symbol: "x"}, base)

	// "Get activities returns user activities" (newest first, own only)
	var items []item
	r = app.Do("GET", "/v1/activities", token, nil)
	r.JSON(t, &items)
	if len(items) != 2 {
		t.Fatalf("items %s", r.Body)
	}
	if items[0].Type != "expense_recorded" || items[0].Subtitle != "Groceries" || *items[0].Amount != 150 || items[0].IsGrowth ||
		items[0].Symbol != "house.fill" || items[0].CreatedAt != "2026-10-01T12:01:00Z" {
		t.Fatalf("first %+v", items[0])
	}
	if items[1].Type != "stock_added" || !items[1].IsGrowth || items[1].UserID != apijson.FormatUUID(user) {
		t.Fatalf("second %+v", items[1])
	}

	// Amount nil is omitted, not null.
	record(t, app, activity.Record{UserID: user, Type: activity.TypeNewsViewed, Title: "News", Subtitle: "read", Symbol: "n"}, base.Add(2*time.Minute))
	r = app.Do("GET", "/v1/activities?limit=1", token, nil)
	if strings.Contains(string(r.Body), `"amount"`) {
		t.Fatalf("nil amount must be omitted: %s", r.Body)
	}

	// "Get activities respects limit parameter"; malformed limits fall back to 20.
	for q, want := range map[string]int{"?limit=2": 2, "?limit=0": 1, "?limit=-5": 1, "?limit=abc": 3, "?limit=500": 3} {
		items = nil
		app.Do("GET", "/v1/activities"+q, token, nil).JSON(t, &items)
		if len(items) != want {
			t.Errorf("%s: %d items, want %d", q, len(items), want)
		}
	}

	// "Get activities requires authentication"
	if r := app.Do("GET", "/v1/activities", "", nil); r.Status != 401 {
		t.Fatalf("auth: %d", r.Status)
	}
}

func TestRecorderDedupesReferenceKey(t *testing.T) {
	app := testutil.NewApp(t, []server.Feature{activity.Feature{}})
	user := app.CreateUser("a@example.com")
	key := "news:123"
	rec := activity.NewRecorder(app.Deps.DB)
	for range 3 {
		if err := rec.Record(context.Background(), activity.Record{UserID: user, Type: activity.TypeNewsViewed, Title: "t", Subtitle: "s", Symbol: "n", ReferenceKey: &key}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM user_activities WHERE user_id = $1`, user).Scan(&n)
	if n != 1 {
		t.Fatalf("recorded %d times", n)
	}
}

