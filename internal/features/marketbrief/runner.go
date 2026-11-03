package marketbrief

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/server"
	"norviq-api/internal/store/marketbriefdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Outcome is MarketBriefRunner.Outcome.
type Outcome struct {
	// Generated is false for .skippedExisting.
	Generated bool
	Degraded  bool
}

// Runner is MarketBriefRunner: fills one slot.
type Runner struct {
	Generator  Generator
	Repository Repository
}

// Run fills due unless it exists; replace deletes and regenerates it.
func (r Runner) Run(ctx context.Context, due Due, replace bool) (Outcome, error) {
	if replace {
		if err := r.Repository.Delete(ctx, due.TradingDate, due.Slot); err != nil {
			return Outcome{}, err
		}
	} else {
		exists, err := r.Repository.Exists(ctx, due.TradingDate, due.Slot)
		if err != nil {
			return Outcome{}, err
		}
		if exists {
			return Outcome{}, nil
		}
	}
	brief, err := r.Generator.Generate(ctx, due)
	if err != nil {
		return Outcome{}, err
	}
	if err := r.Repository.Save(ctx, brief.Responses, brief.Model, time.Now()); err != nil {
		// Another replica (or a manual run) wrote the slot while we were
		// generating. Theirs stands; this is not a failure to retry.
		if isConstraintFailure(err) {
			return Outcome{}, nil
		}
		return Outcome{}, err
	}
	degraded := false
	for _, b := range brief.Responses {
		degraded = degraded || b.Degraded
	}
	return Outcome{Generated: true, Degraded: degraded}, nil
}

// PreferenceService is DatabaseMarketBriefNotificationPreferenceService.
type PreferenceService struct{ DB *pgxpool.Pool }

// Get returns the caller's opt-in; no row means off, in English.
func (s PreferenceService) Get(ctx context.Context, userID uuid.UUID) (NotificationPreferences, error) {
	row, err := marketbriefdb.New(s.DB).GetMarketBriefNotificationPreference(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationPreferences{Enabled: false, Language: LanguageEn}, nil
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	return NotificationPreferences{Enabled: row.Enabled, Language: row.Language}, nil
}

// Update upserts the caller's opt-in with the language resolved.
func (s PreferenceService) Update(ctx context.Context, userID uuid.UUID, enabled bool, rawLanguage string) (NotificationPreferences, error) {
	language := ResolveLanguage(&rawLanguage)
	q := marketbriefdb.New(s.DB)
	row, err := q.GetMarketBriefNotificationPreference(ctx, userID)
	now := time.Now()
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = q.InsertMarketBriefNotificationPreference(ctx, marketbriefdb.InsertMarketBriefNotificationPreferenceParams{
			ID: uuid.New(), UserID: userID, Enabled: enabled, Language: language, CreatedAt: &now, UpdatedAt: &now,
		})
	case err == nil:
		err = q.UpdateMarketBriefNotificationPreference(ctx, marketbriefdb.UpdateMarketBriefNotificationPreferenceParams{
			ID: row.ID, Enabled: enabled, Language: language, UpdatedAt: &now,
		})
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	return NotificationPreferences{Enabled: enabled, Language: language}, nil
}

// pushMaxBodyLength is MarketBriefPushMessage.maxBodyLength: roughly what a
// lock screen shows before truncating anyway.
const pushMaxBodyLength = 180

// pushMessage is MarketBriefPushMessage(brief:): false when the brief has
// nothing worth a notification.
func pushMessage(b Response) (push.Notification, bool) {
	if b.Slot == nil || b.TradingDate == nil || len(b.Items) == 0 {
		return push.Notification{}, false
	}
	first := trimWhitespaceAndNewlines(b.Items[0].Text)
	if first == "" {
		return push.Notification{}, false
	}
	portuguese := ResolveLanguage(&b.Language) == LanguagePtPT
	var title string
	switch {
	case *b.Slot == SlotMorning && portuguese:
		title = "Briefing da manhã"
	case *b.Slot == SlotMorning:
		title = "Morning market brief"
	case portuguese:
		title = "Resumo do fecho"
	default:
		title = "Market close recap"
	}
	body := first
	if swiftCount(first) > pushMaxBodyLength {
		body = trimWhitespaces(string([]rune(first)[:pushMaxBodyLength-1])) + "…"
	}
	return push.MarketBrief(*b.Slot, *b.TradingDate, title, body), true
}

// maxPushDelayMinutes is MarketBriefNotifier.maxDelayMinutes: a brief that
// lands this long after its window opened is old news on a lock screen.
const maxPushDelayMinutes = 90

// Notifier is MarketBriefNotifier: fans a freshly written slot out to the
// users who opted in, one push per language. Called only for a
// `.generated` outcome, which one process ever sees per slot.
type Notifier struct {
	Repository Repository
	DB         *pgxpool.Pool
	Push       *push.Service
	Logger     *slog.Logger
}

// Notify sends the slot's pushes and returns the summary.
func (n Notifier) Notify(ctx context.Context, due Due, now time.Time) (push.Summary, error) {
	opened := morningStart
	if due.Slot != SlotMorning {
		opened = eveningStart
	}
	if LocalMinutes(now)-opened > maxPushDelayMinutes {
		n.Logger.Info("market_brief push skipped: late date=" + due.TradingDate + " slot=" + due.Slot)
		return push.Summary{}, nil
	}
	prefs, err := marketbriefdb.New(n.DB).EnabledMarketBriefNotificationPreferences(ctx)
	if err != nil {
		return push.Summary{}, err
	}
	var total push.Summary
	for _, language := range languages {
		var userIDs []uuid.UUID
		for _, p := range prefs {
			if p.Language == language {
				userIDs = append(userIDs, p.UserID)
			}
		}
		if len(userIDs) == 0 {
			continue
		}
		brief, err := n.Repository.Find(ctx, due.TradingDate, due.Slot, language)
		if err != nil {
			return total, err
		}
		if brief == nil {
			continue
		}
		msg, ok := pushMessage(*brief)
		if !ok {
			continue
		}
		devices, err := n.Push.Devices.ActiveForUsers(ctx, userIDs)
		if err != nil {
			return total, err
		}
		s := n.Push.Sender.Send(ctx, msg, devices)
		total.Delivered += s.Delivered
		total.Failed += s.Failed
	}
	n.Logger.Info("market_brief push date=" + due.TradingDate + " slot=" + due.Slot +
		" delivered=" + strconv.Itoa(total.Delivered) + " failed=" + strconv.Itoa(total.Failed))
	return total, nil
}

// JobLockName is the advisory-lock name Vapor's MarketBriefJob used.
const JobLockName = "market_brief_job"

// maxAttemptsPerSlot is MarketBriefJob.maxAttemptsPerSlot: a broken
// provider costs three paid calls per slot per process, not one every five
// minutes until noon.
const maxAttemptsPerSlot = 3

// attempts is MarketBriefAttempts: per-process failure counts per slot. In
// memory on purpose: a restart earning three fresh attempts is fine.
type attempts struct {
	mu     sync.Mutex
	counts map[Due]int
}

func (a *attempts) failures(d Due) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.counts[d]
}

func (a *attempts) recordFailure(d Due) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.counts == nil {
		a.counts = map[Due]int{}
	}
	a.counts[d]++
	return a.counts[d]
}

// Ticker runs MarketBriefJob.tick against one set of dependencies.
type Ticker struct {
	Deps        *server.Deps
	Generator   Generator
	Repository  Repository
	PushEnabled bool
	// Notifier sends the pushes when PushEnabled.
	Notifier *Notifier
	attempts attempts
}

// Tick is MarketBriefJob.tick: nothing outside a window or once the slot
// has failed three times in this process; otherwise one leader-elected
// run, and a push for a slot this tick wrote.
func (t *Ticker) Tick(ctx context.Context, now time.Time) {
	logger := t.Deps.Logger
	due, ok := DueSlot(now)
	if !ok || t.attempts.failures(due) >= maxAttemptsPerSlot {
		return
	}
	if t.Generator == nil {
		logger.Warn("market_brief skipped: no generator configured")
		return
	}
	runner := Runner{Generator: t.Generator, Repository: t.Repository}
	joblock.RunAsLeaderWithSession(ctx, t.Deps.DB, t.Deps.LockSession, logger, JobLockName, func(ctx context.Context) {
		outcome, err := runner.Run(ctx, due, false)
		if err != nil {
			failures := t.attempts.recordFailure(due)
			logger.Error("market_brief failed date=" + due.TradingDate + " slot=" + due.Slot +
				" attempt=" + strconv.Itoa(failures) + "/" + strconv.Itoa(maxAttemptsPerSlot) + " error=" + err.Error())
			return
		}
		if !outcome.Generated {
			return
		}
		degraded := "false"
		if outcome.Degraded {
			degraded = "true"
		}
		logger.Info("market_brief ok date=" + due.TradingDate + " slot=" + due.Slot + " degraded=" + degraded)
		if t.PushEnabled && t.Notifier != nil {
			// A failed push is logged, never retried: the brief itself is
			// written, and a retry would mean a second notification.
			if _, err := t.Notifier.Notify(ctx, due, now); err != nil {
				logger.Error("market_brief push failed date=" + due.TradingDate + " slot=" + due.Slot + " error=" + err.Error())
			}
		}
	})
}
