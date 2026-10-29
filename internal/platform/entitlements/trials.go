package entitlements

import (
	"context"
	"math"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/store/billingdb"

	"github.com/google/uuid"
)

// Trial is the trial part of a users row: trial_started_at, trial_days,
// trial_tier. A user is "on trial" only when all three are set.
type Trial struct {
	StartedAt *time.Time
	Days      *int64
	Tier      *string
}

func trialOf(startedAt *time.Time, days *int64, tier *string) Trial {
	return Trial{StartedAt: startedAt, Days: days, Tier: tier}
}

func (t Trial) expiresAt() (time.Time, bool) {
	if t.StartedAt == nil || t.Days == nil || t.Tier == nil {
		return time.Time{}, false
	}
	return t.StartedAt.Add(time.Duration(*t.Days) * 24 * time.Hour), true
}

// hasActiveTrial is DefaultEntitlementResolver.hasActiveTrial.
func hasActiveTrial(t Trial, now time.Time) bool {
	if t.StartedAt == nil || t.Days == nil || *t.Days <= 0 {
		return false
	}
	return now.Before(t.StartedAt.Add(time.Duration(*t.Days) * 24 * time.Hour))
}

// TrialState is TrialStatus's case.
type TrialState int

// TrialStatus cases.
const (
	TrialNotOnTrial TrialState = iota
	TrialActive
	TrialExpiringSoon
	TrialExpired
)

// TrialStatus is TrialService.checkTrialStatus's result. DaysRemaining is
// set for Active and ExpiringSoon.
type TrialStatus struct {
	State         TrialState
	DaysRemaining int
}

// IsActive reports the .active and .expiringSoon cases, which the billing
// context and coupon redemption both treat as "trial active".
func (s TrialStatus) IsActive() bool {
	return s.State == TrialActive || s.State == TrialExpiringSoon
}

// DaysRemainingPtr is the days left while active, else nil.
func (s TrialStatus) DaysRemainingPtr() *int {
	if !s.IsActive() {
		return nil
	}
	d := s.DaysRemaining
	return &d
}

func ceilDays(remaining time.Duration) int {
	return int(math.Ceil(remaining.Seconds() / 86400))
}

// CheckTrialStatus is TrialService.checkTrialStatus with the default
// three-day warning window.
func CheckTrialStatus(t Trial, now time.Time) TrialStatus {
	return checkTrialStatus(t, now, 3)
}

func checkTrialStatus(t Trial, now time.Time, warnDays int) TrialStatus {
	expires, ok := t.expiresAt()
	if !ok {
		return TrialStatus{State: TrialNotOnTrial}
	}
	remaining := expires.Sub(now)
	days := ceilDays(remaining)
	switch {
	case remaining <= 0:
		return TrialStatus{State: TrialExpired}
	case days <= warnDays:
		return TrialStatus{State: TrialExpiringSoon, DaysRemaining: max(0, days)}
	default:
		return TrialStatus{State: TrialActive, DaysRemaining: days}
	}
}

// IsTrialExpired is TrialService.isTrialExpired: the trial fields are set
// and the window has lapsed (the sweep has not cleared them yet).
func IsTrialExpired(t Trial, now time.Time) bool {
	expires, ok := t.expiresAt()
	return ok && !now.Before(expires)
}

// TrialDaysRemaining is TrialService.getDaysRemaining.
func TrialDaysRemaining(t Trial, now time.Time) *int {
	expires, ok := t.expiresAt()
	if !ok {
		return nil
	}
	d := ceilDays(expires.Sub(now))
	d = max(0, d)
	return &d
}

// InitializeTrial is TrialService.initializeTrial: starts a trial of
// trialDays (1...31) on tier now. It returns the stored trial so the caller
// can compute its status, as Swift kept the mutated User in memory.
func (s *Service) InitializeTrial(ctx context.Context, userID uuid.UUID, trialDays int, tier string) (Trial, error) {
	if trialDays < 1 || trialDays > 31 {
		return Trial{}, httpx.BadRequest("Trial duration is invalid.")
	}
	now := s.now()
	days := int64(trialDays)
	if err := s.q.SetUserTrial(ctx, billingdb.SetUserTrialParams{
		ID: userID, TrialStartedAt: &now, TrialDays: &days, TrialTier: &tier, UpdatedAt: &now,
	}); err != nil {
		return Trial{}, err
	}
	return Trial{StartedAt: &now, Days: &days, Tier: &tier}, nil
}

// CheckTrialStatus uses the service clock and warning window.
func (s *Service) CheckTrialStatus(t Trial) TrialStatus {
	return checkTrialStatus(t, s.now(), s.warnDays)
}

// ProcessExpiredTrials is TrialService.processExpiredTrials: it marks every
// lapsed trial among (at most) 500 users with trial fields and returns
// their ids.
func (s *Service) ProcessExpiredTrials(ctx context.Context) ([]uuid.UUID, error) {
	candidates, err := s.q.ExpiredTrialCandidates(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	expired := []uuid.UUID{}
	for _, u := range candidates {
		if u.TrialStartedAt == nil || u.TrialDays == nil {
			continue
		}
		expiresAt := u.TrialStartedAt.Add(time.Duration(*u.TrialDays) * 24 * time.Hour)
		if !now.Before(expiresAt) {
			if err := s.MarkTrialExpired(ctx, u.ID); err != nil {
				return nil, err
			}
			expired = append(expired, u.ID)
		}
	}
	return expired, nil
}

// MarkTrialExpired is TrialService.markTrialExpired: clears the trial
// fields, stamps had_trial, and records one "expired" trial warning.
func (s *Service) MarkTrialExpired(ctx context.Context, userID uuid.UUID) error {
	exists, err := s.q.TrialWarningExists(ctx, billingdb.TrialWarningExistsParams{
		UserID: userID, WarningType: billingdb.TrialWarningTypeExpired,
	})
	if err != nil {
		return err
	}
	now := s.now()
	if err := s.q.ClearUserTrial(ctx, billingdb.ClearUserTrialParams{ID: userID, UpdatedAt: &now}); err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.q.InsertTrialWarning(ctx, billingdb.InsertTrialWarningParams{
		ID: uuid.New(), UserID: userID, WarningType: billingdb.TrialWarningTypeExpired, SentAt: now, CreatedAt: &now,
	})
}

// SendExpiringWarning is TrialService.sendExpiringWarning: records the
// one "expiring_soon" warning per user and stamps trial_warning_sent_at.
// It reports whether this call recorded it. Vapor never called it and it
// sends nothing; it is here so the API is complete.
func (s *Service) SendExpiringWarning(ctx context.Context, userID uuid.UUID) (bool, error) {
	exists, err := s.q.TrialWarningExists(ctx, billingdb.TrialWarningExistsParams{
		UserID: userID, WarningType: billingdb.TrialWarningTypeExpiringSoon,
	})
	if err != nil || exists {
		return false, err
	}
	now := s.now()
	if err := s.q.InsertTrialWarning(ctx, billingdb.InsertTrialWarningParams{
		ID: uuid.New(), UserID: userID, WarningType: billingdb.TrialWarningTypeExpiringSoon, SentAt: now, CreatedAt: &now,
	}); err != nil {
		return false, err
	}
	if err := s.q.SetTrialWarningSentAt(ctx, billingdb.SetTrialWarningSentAtParams{ID: userID, TrialWarningSentAt: &now}); err != nil {
		return false, err
	}
	return true, nil
}
