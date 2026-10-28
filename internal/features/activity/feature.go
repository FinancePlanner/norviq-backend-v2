// Package activity ports UserActivityController and UserActivityService:
// the activity feed (GET /v1/activities) and the recorder other domains call
// when a user adds a stock, records an expense or reads news.
package activity

import (
	"context"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/store/activitydb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// UserActivityType raw values.
const (
	TypeStockAdded      = activitydb.UserActivityTypeStockAdded
	TypeExpenseRecorded = activitydb.UserActivityTypeExpenseRecorded
	TypeStockUpdated    = activitydb.UserActivityTypeStockUpdated
	TypeExpenseUpdated  = activitydb.UserActivityTypeExpenseUpdated
	TypeNewsViewed      = activitydb.UserActivityTypeNewsViewed
)

// Feature mounts /v1/activities.
type Feature struct{}

func (Feature) Name() string { return "activity" }

func (Feature) Register(d *server.Deps, r *server.Routes) error {
	q := activitydb.New(d.DB)
	r.V1.GET("/activities", func(c *echo.Context) error { return list(c, q) },
		d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeSettingsRead))
	return nil
}

// Response is UserActivityResponse.
type Response struct {
	ID        apijson.UUID `json:"id"`
	UserID    apijson.UUID `json:"userId"`
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Subtitle  string       `json:"subtitle"`
	Amount    *float64     `json:"amount,omitempty"`
	IsGrowth  bool         `json:"isGrowth"`
	Symbol    string       `json:"symbol"`
	CreatedAt apijson.Time `json:"createdAt"`
}

// GET /v1/activities?limit= (default 20, clamped to 1...100), newest first.
func list(c *echo.Context, q activitydb.Querier) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	limit := min(max(httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 20), 1), 100)
	rows, err := q.ListUserActivities(c.Request().Context(), activitydb.ListUserActivitiesParams{
		UserID: s.UserID, Limit: int32(limit),
	})
	if err != nil {
		return err
	}
	out := make([]Response, 0, len(rows))
	for i := range rows {
		a := &rows[i]
		created := time.Now()
		if a.CreatedAt != nil {
			created = *a.CreatedAt
		}
		out = append(out, Response{
			ID:        apijson.NewUUID(a.ID),
			UserID:    apijson.NewUUID(a.UserID),
			Type:      string(a.Type),
			Title:     a.Title,
			Subtitle:  a.Subtitle,
			Amount:    a.Amount,
			IsGrowth:  a.IsGrowth,
			Symbol:    a.Symbol,
			CreatedAt: apijson.NewTime(created),
		})
	}
	return httpx.OK(c, out)
}

// Record is UserActivityRecord. With a ReferenceKey, the activity is recorded
// at most once per (user, type, key).
type Record struct {
	UserID       uuid.UUID
	Type         activitydb.UserActivityType
	Title        string
	Subtitle     string
	Amount       *float64
	IsGrowth     bool
	Symbol       string // SF Symbol name
	ReferenceKey *string
}

// Recorder is DatabaseUserActivityService.recordActivity, for other domains.
type Recorder struct{ Q activitydb.Querier }

// NewRecorder builds a recorder over db.
func NewRecorder(db activitydb.DBTX) Recorder { return Recorder{Q: activitydb.New(db)} }

// Record inserts the activity, silently skipping a duplicate reference key.
func (r Recorder) Record(ctx context.Context, rec Record) error {
	if rec.ReferenceKey != nil {
		exists, err := r.Q.UserActivityReferenceExists(ctx, activitydb.UserActivityReferenceExistsParams{
			UserID: rec.UserID, Type: rec.Type, ReferenceKey: rec.ReferenceKey,
		})
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	now := time.Now()
	return r.Q.InsertUserActivity(ctx, activitydb.InsertUserActivityParams{
		ID:           uuid.New(),
		UserID:       rec.UserID,
		Type:         rec.Type,
		Title:        rec.Title,
		Subtitle:     rec.Subtitle,
		Amount:       rec.Amount,
		IsGrowth:     rec.IsGrowth,
		Symbol:       rec.Symbol,
		ReferenceKey: rec.ReferenceKey,
		CreatedAt:    &now,
	})
}
