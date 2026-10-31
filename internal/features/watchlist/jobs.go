package watchlist

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/features/market"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/inbox"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/notificationsdb"
	"norviq-api/internal/store/watchlistdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Vapor ran both pollers unconditionally (configure.swift had no switch for
// either). Their ticks take Vapor's JobLock names, so two replicas' ticks
// never overlap, and both are idempotent per event:
// alert_triggered_at and the inbox deduplication key for targets, the
// earnings_notification_deliveries row for earnings reminders.

// pollSeconds reads an Int64 environment variable as
// `Environment.get(name).flatMap(Int64.init(_:)) ?? def`.
func pollSeconds(name string, def int64) int64 {
	if raw, ok := os.LookupEnv(name); ok {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// wait sleeps d, reporting false when ctx ended first.
func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// runPoller is scheduleRepeatedTask(initialDelay:delay:) with one tick per
// interval under the job lock.
func runPoller(ctx context.Context, d *server.Deps, lock string, initial, interval time.Duration, tick func(context.Context)) {
	d.Logger.Info(lock + " starting")
	defer d.Logger.Info(lock + " shutdown")
	if !wait(ctx, initial) {
		return
	}
	for {
		joblock.RunAsLeader(ctx, d.DB, d.Logger, lock, tick)
		if !wait(ctx, interval) {
			return
		}
	}
}

// ---- TargetAlertPoller ------------------------------------------------------

const targetAlertLock = "target_alert_poller"

// TargetAlertJob is Vapor's TargetAlertPoller: every APNS_ALERT_POLL_SECONDS
// (default 300, minimum 30) after a 30 s initial delay, it checks every
// untriggered price target against a live quote.
type TargetAlertJob struct {
	// Interval and InitialDelay override the environment and the 30 s
	// delay (tests).
	Interval, InitialDelay time.Duration
}

// Name is the V2_JOBS key.
func (TargetAlertJob) Name() string { return targetAlertLock }

// Run polls until ctx ends.
func (j TargetAlertJob) Run(ctx context.Context, d *server.Deps) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Duration(max(pollSeconds("APNS_ALERT_POLL_SECONDS", 300), 30)) * time.Second
	}
	initial := j.InitialDelay
	if initial <= 0 {
		initial = 30 * time.Second
	}
	quotes, err := market.SharedService(d)
	if err != nil {
		d.Logger.Error("target_alert_poller disabled", "error", err)
		return
	}
	p, err := push.From(d)
	if err != nil {
		d.Logger.Error("target_alert_poller disabled", "error", err)
		return
	}
	sweep := TargetAlerts{DB: d.DB, Quotes: quotes, Push: p, Logger: d.Logger}
	runPoller(ctx, d, targetAlertLock, initial, interval, sweep.Run)
}

// Quoter is the part of the market data service the target poller reads.
type Quoter interface {
	Quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error)
}

// TargetAlerts is DefaultTargetAlertEvaluator.
type TargetAlerts struct {
	DB     *pgxpool.Pool
	Quotes Quoter
	Push   *push.Service
	Logger *slog.Logger
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (a TargetAlerts) now() time.Time {
	t := time.Now()
	if a.Now != nil {
		t = a.Now()
	}
	return t.UTC().Truncate(time.Microsecond)
}

// Run is evaluateUnresolvedTargets: one sweep.
func (a TargetAlerts) Run(ctx context.Context) {
	targets, err := watchlistdb.New(a.DB).UnresolvedTargets(ctx)
	if err != nil {
		a.Logger.Warn("target-alert query failed", "error", err)
		return
	}
	for i := range targets {
		a.evaluate(ctx, &targets[i])
	}
}

// shouldTrigger is DefaultTargetAlertEvaluator.shouldTrigger.
func shouldTrigger(t *watchlistdb.Target, price float64) bool {
	switch strings.ToLower(t.Scenario) {
	case "bear":
		return price <= t.TargetPrice
	case "base", "bull":
		return price >= t.TargetPrice
	}
	return false
}

func (a TargetAlerts) evaluate(ctx context.Context, t *watchlistdb.Target) {
	quote, err := a.Quotes.Quote(ctx, t.Symbol)
	if err != nil {
		a.Logger.Warn("target-alert quote lookup failed", "symbol", t.Symbol, "error", err)
		return
	}
	price := quote.CurrentPrice
	if !shouldTrigger(t, price) {
		return
	}
	id := apijson.FormatUUID(t.ID)
	link := "norviq://stocks/" + t.Symbol
	if _, err := inbox.Publish(ctx, notificationsdb.New(a.DB), inbox.Message{
		UserID:           t.UserID,
		Kind:             inbox.KindPriceTarget,
		DeduplicationKey: "price-target:" + id,
		Title:            "Price target reached",
		Body:             t.Symbol + " reached " + marketdata.SwiftDoubleString(price) + ".",
		DeepLink:         &link,
		Payload:          map[string]string{"target_id": id, "symbol": t.Symbol},
	}, a.now()); err != nil {
		a.Logger.Warn("target-alert inbox persistence failed", "symbol", t.Symbol)
		return
	}
	devices, err := a.Push.Devices.Active(ctx, t.UserID)
	if err != nil {
		a.Logger.Warn("target-alert device query failed", "userId", apijson.FormatUUID(t.UserID), "error", err)
		return
	}
	if len(devices) == 0 {
		return
	}
	summary := a.Push.Sender.Send(ctx, push.TargetHit(&t.ID, t.Symbol, t.Scenario, t.TargetPrice, price), devices)
	if summary.Delivered == 0 {
		a.Logger.Warn("target-alert no successful deliveries", "symbol", t.Symbol, "failed", summary.Failed)
		return
	}
	marked, err := a.markTriggered(ctx, t.ID, price)
	if err != nil {
		a.Logger.Warn("target-alert state update failed", "symbol", t.Symbol)
		return
	}
	if !marked {
		return
	}
	// alert_triggered_at is committed now: craft.target_hit can move.
	achievementhooks.Schedule(t.UserID)
	a.Logger.Info("target-alert push delivered", "symbol", t.Symbol, "currentPrice", price)
}

// markTriggered is markTriggeredIfNeeded.
func (a TargetAlerts) markTriggered(ctx context.Context, id uuid.UUID, price float64) (bool, error) {
	marked := false
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		q := watchlistdb.New(tx)
		current, err := q.TargetForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.AlertTriggeredAt != nil {
			return nil
		}
		now := a.now()
		if err := q.MarkTargetTriggered(ctx, watchlistdb.MarkTargetTriggeredParams{Now: &now, Price: &price, ID: id}); err != nil {
			return err
		}
		marked = true
		return nil
	})
	return marked, err
}

// ---- EarningsNotificationPoller --------------------------------------------

const earningsReminderLock = "earnings_notification_poller"

// earningsLeadDays is DefaultEarningsNotificationEvaluator.leadDays.
var earningsLeadDays = []int{7, 1}

// EarningsReminderJob is Vapor's EarningsNotificationPoller: every
// EARNINGS_ALERT_POLL_SECONDS (default 86400, minimum 3600) after a 60 s
// initial delay, it reminds holders and active watchers of a symbol 7 days
// and 1 day before its earnings date.
type EarningsReminderJob struct {
	// Interval and InitialDelay override the environment and the 60 s
	// delay (tests).
	Interval, InitialDelay time.Duration
}

// Name is the V2_JOBS key.
func (EarningsReminderJob) Name() string { return earningsReminderLock }

// Run polls until ctx ends.
func (j EarningsReminderJob) Run(ctx context.Context, d *server.Deps) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Duration(max(pollSeconds("EARNINGS_ALERT_POLL_SECONDS", 86400), 3600)) * time.Second
	}
	initial := j.InitialDelay
	if initial <= 0 {
		initial = 60 * time.Second
	}
	calendar, err := earnings.CalendarFromEnv(nil)
	if err != nil {
		d.Logger.Error("earnings_notification_poller disabled", "error", err)
		return
	}
	p, err := push.From(d)
	if err != nil {
		d.Logger.Error("earnings_notification_poller disabled", "error", err)
		return
	}
	sweep := EarningsReminders{
		DB: d.DB, Calendar: calendar, Premium: entitlements.NewFromEnv(d.DB), Push: p, Logger: d.Logger,
	}
	runPoller(ctx, d, earningsReminderLock, initial, interval, sweep.Run)
}

// CalendarSource is app.earningsService.getCalendar.
type CalendarSource interface {
	Fetch(ctx context.Context, q earnings.Query) ([]earnings.ItemResponse, error)
}

// PremiumGate is usageCounterService.requirePremium.
type PremiumGate interface {
	RequirePremium(ctx context.Context, feature entitlements.Feature, userID uuid.UUID) error
}

// EarningsReminders is DefaultEarningsNotificationEvaluator.
type EarningsReminders struct {
	DB       *pgxpool.Pool
	Calendar CalendarSource
	Premium  PremiumGate
	Push     *push.Service
	Logger   *slog.Logger
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (e EarningsReminders) now() time.Time {
	t := time.Now()
	if e.Now != nil {
		t = e.Now()
	}
	return t.UTC().Truncate(time.Microsecond)
}

// Run is evaluateUpcomingEarnings: one sweep.
func (e EarningsReminders) Run(ctx context.Context) {
	for _, lead := range earningsLeadDays {
		e.evaluateLead(ctx, lead)
	}
}

func (e EarningsReminders) evaluateLead(ctx context.Context, lead int) {
	date := e.now().AddDate(0, 0, lead).Format("2006-01-02")
	events, err := e.Calendar.Fetch(ctx, earnings.Query{From: &date, To: &date})
	if err != nil {
		e.Logger.Warn("earnings-reminder calendar lookup failed", "date", date, "leadDays", lead, "error", err)
		return
	}
	var symbols []string
	for _, ev := range events {
		if ev.Symbol == nil {
			continue
		}
		if s := strings.ToUpper(strings.TrimSpace(*ev.Symbol)); s != "" && !slices.Contains(symbols, s) {
			symbols = append(symbols, s)
		}
	}
	for _, s := range symbols {
		e.evaluateSymbol(ctx, s, date, lead)
	}
}

func (e EarningsReminders) evaluateSymbol(ctx context.Context, symbol, date string, lead int) {
	q := watchlistdb.New(e.DB)
	holders, err := q.HoldingUserIDsForSymbol(ctx, symbol)
	if err != nil {
		e.Logger.Warn("earnings-reminder eligible user lookup failed", "symbol", symbol, "error", err)
		return
	}
	watchers, err := q.ActiveWatchlistUserIDsForSymbol(ctx, symbol)
	if err != nil {
		e.Logger.Warn("earnings-reminder eligible user lookup failed", "symbol", symbol, "error", err)
		return
	}
	var users []uuid.UUID
	for _, id := range append(holders, watchers...) {
		if !slices.Contains(users, id) {
			users = append(users, id)
		}
	}
	for _, id := range users {
		e.evaluateUser(ctx, q, id, symbol, date, lead)
	}
}

func (e EarningsReminders) evaluateUser(ctx context.Context, q *watchlistdb.Queries, userID uuid.UUID, symbol, date string, lead int) {
	if err := e.Premium.RequirePremium(ctx, entitlements.FeatureTargetAlerts, userID); err != nil {
		return
	}
	uid := apijson.FormatUUID(userID)
	enabled, err := q.EarningsReminderEnabled(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		enabled, err = true, nil
	}
	if err != nil {
		e.Logger.Warn("earnings-reminder state lookup failed", "userId", uid, "symbol", symbol, "error", err)
		return
	}
	if !enabled {
		return
	}
	sent, err := q.EarningsReminderDelivered(ctx, watchlistdb.EarningsReminderDeliveredParams{
		UserID: userID, Symbol: symbol, EarningsDate: date, LeadDays: int64(lead),
	})
	if err != nil {
		e.Logger.Warn("earnings-reminder state lookup failed", "userId", uid, "symbol", symbol, "error", err)
		return
	}
	if sent > 0 {
		return
	}
	days := "days"
	if lead == 1 {
		days = "day"
	}
	link := "norviq://stocks/" + symbol
	now := e.now()
	if _, err := inbox.Publish(ctx, notificationsdb.New(e.DB), inbox.Message{
		UserID:           userID,
		Kind:             inbox.KindEarnings,
		DeduplicationKey: "earnings:" + symbol + ":" + date + ":" + strconv.Itoa(lead),
		Title:            "Upcoming earnings",
		Body:             symbol + " reports earnings in " + strconv.Itoa(lead) + " " + days + ".",
		DeepLink:         &link,
		Payload:          map[string]string{"symbol": symbol, "earnings_date": date, "lead_days": strconv.Itoa(lead)},
	}, now); err != nil {
		e.Logger.Warn("earnings-reminder inbox persistence failed", "userId", uid, "symbol", symbol)
		return
	}
	if err := q.InsertEarningsReminderDelivery(ctx, watchlistdb.InsertEarningsReminderDeliveryParams{
		ID: uuid.New(), UserID: userID, Symbol: symbol, EarningsDate: date, LeadDays: int64(lead), SentAt: now,
	}); err != nil {
		e.Logger.Warn("earnings-reminder inbox persistence failed", "userId", uid, "symbol", symbol)
		return
	}
	devices, err := e.Push.Devices.Active(ctx, userID)
	if err != nil {
		e.Logger.Warn("earnings-reminder device lookup failed", "userId", uid, "symbol", symbol, "error", err)
		return
	}
	if len(devices) == 0 {
		return
	}
	if summary := e.Push.Sender.Send(ctx, push.EarningsReminder(symbol, date, lead), devices); summary.Delivered == 0 {
		e.Logger.Warn("earnings-reminder push was not delivered", "userId", uid, "symbol", symbol)
	}
}
