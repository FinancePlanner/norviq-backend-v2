package digest

import (
	"context"
	"sync"

	"norviq-api/internal/features/budgeting"
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/features/market"
	"norviq-api/internal/server"

	"github.com/google/uuid"
)

// InflationSource is MacroService.personalInflation(userID:country:
// periodMonths: 12).
type InflationSource func(ctx context.Context, userID uuid.UUID, country string) (PersonalInflation, error)

// LiveSources is LiveDigestDataSources: the shared market data service, the
// earnings calendar and the budgeting engine, each resolved on first use so
// a missing provider key fails only its own section.
type LiveSources struct {
	Deps *server.Deps
	// Inflation is nil until the macro domain is ported; the inflation
	// section then logs digest.section_failed and is left out.
	Inflation InflationSource

	calendarOnce sync.Once
	calendar     *earnings.Calendar
	calendarErr  error
}

// DailyCloses reads the price history between from and to (yyyy-MM-dd).
func (s *LiveSources) DailyCloses(ctx context.Context, symbol, from, to string) ([]PriceBar, error) {
	svc, err := market.SharedService(s.Deps)
	if err != nil {
		return nil, err
	}
	h, err := svc.History(ctx, symbol, &from, &to)
	if err != nil {
		return nil, err
	}
	out := make([]PriceBar, 0, len(h.Bars))
	for i := range h.Bars {
		date := h.Bars[i].Date
		if r := []rune(date); len(r) > 10 {
			date = string(r[:10])
		}
		out = append(out, PriceBar{Date: date, Close: h.Bars[i].Close})
	}
	return out, nil
}

// Earnings is earningsService.getCalendar(query: .init(from:to:)).
func (s *LiveSources) Earnings(ctx context.Context, from, to string) ([]earnings.ItemResponse, error) {
	s.calendarOnce.Do(func() { s.calendar, s.calendarErr = earnings.CalendarFromEnv(nil) })
	if s.calendarErr != nil {
		return nil, s.calendarErr
	}
	return s.calendar.Fetch(ctx, earnings.Query{From: &from, To: &to})
}

// PersonalInflation calls Inflation, or fails while it is not wired.
func (s *LiveSources) PersonalInflation(ctx context.Context, userID uuid.UUID, country string) (PersonalInflation, error) {
	if s.Inflation == nil {
		return PersonalInflation{}, errInflationUnavailable
	}
	return s.Inflation(ctx, userID, country)
}

// BudgetDashboard is BudgetingEngineService.dashboard(userId:snapshotId:).
func (s *LiveSources) BudgetDashboard(ctx context.Context, userID, snapshotID uuid.UUID) (budgeting.BudgetDriftDashboard, error) {
	e, err := budgeting.NewEngine(s.Deps)
	if err != nil {
		return budgeting.BudgetDriftDashboard{}, err
	}
	return e.Dashboard(ctx, nil, userID, snapshotID)
}
