package budgeting

import (
	"context"
	"math"
	"net/url"
	"strconv"
	"time"

	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/store/budgetingdb"

	"github.com/google/uuid"
)

// Publisher is the slice of inbox.Publisher the evaluator uses.
type Publisher interface {
	PublishAndPush(ctx context.Context, m inbox.Message) (inbox.Event, error)
}

// Evaluator is BudgetDriftEvaluator: it records each scope's drift level in
// budget_drift_alert_states and notifies once per breach.
type Evaluator struct {
	Engine    *Engine
	Publisher Publisher
	// AlertsEnabled is BUDGET_DRIFT_ALERTS_ENABLED (default true). Vapor read
	// it on every evaluation; v2 reads it at boot.
	AlertsEnabled bool
}

type scope struct {
	key, title, level string
	amount            float64
}

// Evaluate is BudgetDriftEvaluator.evaluate. Callers treat it as best
// effort: they log and ignore its error.
func (v *Evaluator) Evaluate(ctx context.Context, userID uuid.UUID, monthStart time.Time, notify bool) error {
	e := v.Engine
	q := e.q(nil)
	snap, err := snapshotNear(ctx, q, userID, monthStart)
	if err != nil || snap == nil {
		return err
	}
	dash, err := e.Dashboard(ctx, nil, userID, snap.ID)
	if err != nil {
		return err
	}
	scopes := make([]scope, 0, len(dash.Categories)+1)
	for _, c := range dash.Categories {
		if snap.AlertOnUnbudgeted || c.TargetAmount > 0 {
			scopes = append(scopes, scope{c.ID, c.Title, c.Level, c.DriftAmount})
		}
	}
	scopes = append(scopes, scope{"total", "Total expenses", dash.TotalLevel, dash.TotalDriftAmount})

	for _, s := range scopes {
		state, err := q.AlertState(ctx, budgetingdb.AlertStateParams{SnapshotID: snap.ID, ScopeKey: s.key})
		isNew := noRows(err)
		if err != nil && !isNew {
			return err
		}
		wasRed := !isNew && state.Level == levelRed
		if isNew {
			state = budgetingdb.BudgetDriftAlertState{ID: uuid.New(), UserID: userID, SnapshotID: snap.ID, ScopeKey: s.key}
		}
		now := e.now()
		state.Level = s.level
		state.EvaluatedAt = &now
		switch {
		case s.level == levelRed:
			if !wasRed {
				state.BreachSequence++
			}
			if notify && snap.AlertsEnabled && v.AlertsEnabled && !wasRed && v.Publisher != nil {
				month := dateString(snap.MonthStart)
				// The scope can be an unbudgeted title ("unbudgeted-title:car & fuel"),
				// so it is query-escaped. (Vapor sent it raw.)
				link := "norviq://expenses?month=" + month + "&scope=" + url.QueryEscape(s.key)
				sid := uuidString(snap.ID)
				if _, err := v.Publisher.PublishAndPush(ctx, inbox.Message{
					UserID:           userID,
					Kind:             inbox.KindBudget,
					DeduplicationKey: "budget-drift:" + sid + ":" + s.key + ":" + strconv.FormatInt(state.BreachSequence, 10),
					Title:            "Budget drift needs attention",
					Body:             s.title + " is " + push.FormatPrice(math.Abs(s.amount), snap.CurrencyCode) + " over target.",
					DeepLink:         &link,
					Payload:          map[string]string{"snapshot_id": sid, "scope": s.key, "level": s.level},
				}); err != nil {
					return err
				}
				red := levelRed
				state.LastNotifiedLevel = &red
				notified := e.now()
				state.LastNotifiedAt = &notified
			}
			state.ClearedAt = nil
		case wasRed:
			cleared := e.now()
			state.ClearedAt = &cleared
			state.LastNotifiedLevel = nil
		}
		if isNew {
			err = q.InsertAlertState(ctx, budgetingdb.InsertAlertStateParams{
				ID: state.ID, UserID: userID, SnapshotID: snap.ID, ScopeKey: s.key, Level: state.Level,
				BreachSequence: state.BreachSequence, LastNotifiedLevel: state.LastNotifiedLevel,
				LastNotifiedAt: state.LastNotifiedAt, EvaluatedAt: state.EvaluatedAt, ClearedAt: state.ClearedAt,
			})
		} else {
			err = q.UpdateAlertState(ctx, budgetingdb.UpdateAlertStateParams{
				ID: state.ID, Level: state.Level, BreachSequence: state.BreachSequence,
				LastNotifiedLevel: state.LastNotifiedLevel, LastNotifiedAt: state.LastNotifiedAt,
				EvaluatedAt: state.EvaluatedAt, ClearedAt: state.ClearedAt,
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}
