// Package entitlements answers "is this user Pro, on which plan, and in what
// trial state" exactly as the Vapor backend computed it
// (Billing/EntitlementResolver.swift, BillingContextService.swift,
// TrialService.swift, TaxPackEntitlementService.swift).
//
// Every domain that gates a feature on the plan uses this package:
//
//	svc := entitlements.New(d.DB, cfg)            // once, in Register
//	snap, err := svc.Resolve(ctx, userID)         // entitlementResolver.resolve
//	if !snap.IsPro() { return entitlements.UpgradeRequired(entitlements.FeatureCrypto, snap.Level) }
//	err := svc.RequirePremium(ctx, entitlements.FeatureCrypto, userID)  // usageCounterService.requirePremium
//
// An *UpgradeRequiredError returned from a handler renders Vapor's
// BillingErrorMiddleware body (403, code "upgrade_required"), not the
// generic error envelope.
package entitlements

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/store/billingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Snapshot is EntitlementSnapshot: the user's effective level.
type Snapshot struct {
	UserID uuid.UUID
	Level  string
}

// IsPro is EntitlementSnapshot.isPro. Trial users ("temporary") are Pro.
func (s Snapshot) IsPro() bool {
	l := s.Level
	return l == "pro" || strings.HasPrefix(l, "pro_") || l == "premium" || strings.HasPrefix(l, "premium_") || l == "temporary"
}

// IsPremium is EntitlementSnapshot.isPremium, an alias of IsPro.
func (s Snapshot) IsPremium() bool { return s.IsPro() }

// Config is the resolver's environment.
type Config struct {
	// BypassBilling is BYPASS_BILLING == "true" (exact string, as Vapor
	// compared it): everyone resolves to "pro".
	BypassBilling bool
	// PremiumEmails is BILLING_PREMIUM_EMAILS, lowercased: verified
	// addresses listed here resolve to "pro".
	PremiumEmails map[string]struct{}
}

// LoadConfig reads BYPASS_BILLING and BILLING_PREMIUM_EMAILS.
func LoadConfig() Config {
	return Config{
		BypassBilling: os.Getenv("BYPASS_BILLING") == "true",
		PremiumEmails: ParsePremiumEmails(os.Getenv("BILLING_PREMIUM_EMAILS")),
	}
}

// ParsePremiumEmails splits the comma-separated list, trimming spaces and
// tabs (Swift's .whitespaces, which keeps newlines) and lowercasing.
func ParsePremiumEmails(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		e := strings.ToLower(strings.TrimFunc(part, func(r rune) bool {
			return r == '\t' || unicode.Is(unicode.Zs, r)
		}))
		if e != "" {
			out[e] = struct{}{}
		}
	}
	return out
}

// Service resolves entitlements and keeps usage counters. It is safe for
// concurrent use.
type Service struct {
	db  billingdb.DBTX
	q   *billingdb.Queries
	cfg Config
	now func() time.Time
	// WarnDaysBeforeExpiry is TrialService's warnDaysBeforeExpiry (3).
	warnDays int
}

// New builds a service over db (a pool or a transaction).
func New(db billingdb.DBTX, cfg Config) *Service {
	return &Service{db: db, q: billingdb.New(db), cfg: cfg, now: time.Now, warnDays: 3}
}

// NewFromEnv is New with LoadConfig.
func NewFromEnv(db billingdb.DBTX) *Service { return New(db, LoadConfig()) }

// WithDB returns a copy running its queries on db, e.g. inside a pgx.Tx,
// as Swift passed `on: tx`.
func (s *Service) WithDB(db billingdb.DBTX) *Service {
	c := *s
	c.db = db
	c.q = billingdb.New(db)
	return &c
}

// WithClock returns a copy that reads the time from now (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	c := *s
	c.now = now
	return &c
}

// Now is the service clock.
func (s *Service) Now() time.Time { return s.now() }

// Config returns the resolver configuration.
func (s *Service) Config() Config { return s.cfg }

// Queries exposes the billing queries on the service's handle.
func (s *Service) Queries() *billingdb.Queries { return s.q }

// Resolve is DefaultEntitlementResolver.resolve. A user that does not
// exist resolves to "free", not an error.
func (s *Service) Resolve(ctx context.Context, userID uuid.UUID) (Snapshot, error) {
	// 1. Explicit local/test bypass.
	if s.cfg.BypassBilling {
		return Snapshot{UserID: userID, Level: "pro"}, nil
	}

	user, err := s.q.ResolverUser(ctx, userID)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, err
	}

	// 2. Premium email bypass, only for a verified address.
	if len(s.cfg.PremiumEmails) > 0 && found && user.EmailVerifiedAt != nil {
		if _, ok := s.cfg.PremiumEmails[strings.ToLower(user.Email)]; ok {
			return Snapshot{UserID: userID, Level: "pro"}, nil
		}
	}

	// 3. Explicit entitlement (e.g. from a subscription).
	ent, err := s.q.EntitlementByUser(ctx, userID)
	hasEnt := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, err
	}

	// 4. An active trial overrides a missing or "free" entitlement.
	if found && user.TrialTier != nil && hasActiveTrial(trialOf(user.TrialStartedAt, user.TrialDays, user.TrialTier), s.now()) {
		if !hasEnt || ent.Level == "free" {
			return Snapshot{UserID: userID, Level: *user.TrialTier}, nil
		}
	}

	if hasEnt {
		return Snapshot{UserID: userID, Level: ent.Level}, nil
	}
	return Snapshot{UserID: userID, Level: "free"}, nil
}

// RequirePremium is usageCounterService.requirePremium: an
// *UpgradeRequiredError unless the user resolves to Pro.
func (s *Service) RequirePremium(ctx context.Context, feature Feature, userID uuid.UUID) error {
	snap, err := s.Resolve(ctx, userID)
	if err != nil {
		return err
	}
	if !snap.IsPro() {
		return UpgradeRequired(feature, snap.Level)
	}
	return nil
}

// LimitsFor is usageCounterService.limits(for:).
func LimitsFor(s Snapshot) Limits {
	if s.IsPro() {
		return ProLimits
	}
	return FreeLimits
}

// EnforceResourceLimit is usageCounterService.enforceResourceLimit: fails
// when currentCount+adding exceeds the plan's limit for feature. Vapor's
// default for adding is 1.
func (s *Service) EnforceResourceLimit(ctx context.Context, feature Feature, userID uuid.UUID, currentCount, adding int) error {
	snap, err := s.Resolve(ctx, userID)
	if err != nil {
		return err
	}
	limit := LimitsFor(snap).Limit(feature)
	if limit == nil {
		return nil
	}
	if currentCount+adding > *limit {
		return UpgradeLimit(feature, "free", *limit, currentCount)
	}
	return nil
}

// UsageCounter is the user's usage_counters row.
type UsageCounter = billingdb.UsageCounter

// MonthStart is the first instant of t's month in UTC.
func MonthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Counter is usageCounterService.counter: the user's counter row, created
// on first use and reset (CSV imports, report generations) when a new month
// began.
func (s *Service) Counter(ctx context.Context, userID uuid.UUID) (UsageCounter, error) {
	now := s.now()
	periodStart := MonthStart(now)
	existing, err := s.q.UsageCounterByUser(ctx, userID)
	if err == nil {
		if existing.PeriodStart.Before(periodStart) {
			if err := s.q.ResetUsageCounterPeriod(ctx, billingdb.ResetUsageCounterPeriodParams{
				ID: existing.ID, PeriodStart: periodStart, UpdatedAt: &now,
			}); err != nil {
				return UsageCounter{}, err
			}
			existing.PeriodStart = periodStart
			existing.CsvImportCount = 0
			existing.ReportGenerationCount = 0
			existing.UpdatedAt = &now
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return UsageCounter{}, err
	}
	if err := s.q.InsertUsageCounterIfMissing(ctx, billingdb.InsertUsageCounterIfMissingParams{
		ID: uuid.New(), UserID: userID, PeriodStart: periodStart,
	}); err != nil {
		return UsageCounter{}, err
	}
	return s.q.UsageCounterByUser(ctx, userID)
}

// IncrementUsage is usageCounterService.incrementUsage. Features without a
// counter column only check the limit (they have none) and write nothing.
func (s *Service) IncrementUsage(ctx context.Context, feature Feature, userID uuid.UUID, amount int) error {
	if amount <= 0 {
		return nil
	}
	snap, err := s.Resolve(ctx, userID)
	if err != nil {
		return err
	}
	usage, err := s.Counter(ctx, userID)
	if err != nil {
		return err
	}
	current := usageValue(feature, usage)
	if limit := LimitsFor(snap).Limit(feature); limit != nil && current+amount > *limit {
		return UpgradeLimit(feature, "free", *limit, current)
	}
	return s.setUsage(ctx, feature, usage, current+amount)
}

// SyncResourceCount is usageCounterService.syncResourceCount.
func (s *Service) SyncResourceCount(ctx context.Context, feature Feature, userID uuid.UUID, count int) error {
	usage, err := s.Counter(ctx, userID)
	if err != nil {
		return err
	}
	return s.setUsage(ctx, feature, usage, max(0, count))
}

func (s *Service) setUsage(ctx context.Context, feature Feature, usage UsageCounter, value int) error {
	col := usageColumn(feature)
	if col == "" {
		return nil
	}
	now := s.now()
	return s.q.SetUsageCount(ctx, billingdb.SetUsageCountParams{
		Col: col, Value: int64(value), UpdatedAt: &now, ID: usage.ID,
	})
}

func usageColumn(f Feature) string {
	switch f {
	case FeatureHoldings:
		return "holding_count"
	case FeatureWatchlistItems:
		return "watchlist_item_count"
	case FeatureCSVImports:
		return "csv_import_count"
	case FeatureTargetAlerts:
		return "target_alert_count"
	case FeatureReportGenerations:
		return "report_generation_count"
	}
	return ""
}

func usageValue(f Feature, u UsageCounter) int {
	switch f {
	case FeatureHoldings:
		return int(u.HoldingCount)
	case FeatureWatchlistItems:
		return int(u.WatchlistItemCount)
	case FeatureCSVImports:
		return int(u.CsvImportCount)
	case FeatureTargetAlerts:
		return int(u.TargetAlertCount)
	case FeatureReportGenerations:
		return int(u.ReportGenerationCount)
	}
	return 0
}
