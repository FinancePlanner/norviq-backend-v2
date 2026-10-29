// Package features is the registry of ported Vapor controllers and
// background jobs. A route listed here is served by v2; every other route is
// proxied to the Vapor backend until it is ported.
package features

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/features/activity"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/assets"
	"norviq-api/internal/features/badges"
	"norviq-api/internal/features/billing"
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/features/feedback"
	"norviq-api/internal/features/identity"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/notifications"
	"norviq-api/internal/features/portfolio"
	"norviq-api/internal/features/portfoliomgmt"
	"norviq-api/internal/features/rebalancing"
	"norviq-api/internal/features/simulations"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/server"
)

// All returns every ported feature, in Vapor's registration order where it
// matters (a route registered twice is an error, not an override).
func All() []server.Feature {
	return []server.Feature{
		billing.Feature{},
		notifications.Feature{Premium: func(d *server.Deps) notifications.PremiumGate {
			return entitlements.NamedGate{Service: entitlements.NewFromEnv(d.DB)}
		}},
		activity.Feature{},
		badges.Feature{},
		feedback.Feature{},
		assets.Feature{Search: func(d *server.Deps) (assets.SymbolSearch, error) {
			svc, err := market.SharedService(d)
			if err != nil {
				return nil, err
			}
			return marketSearch{svc}, nil
		}},
		aiplatform.Feature{},
		market.Feature{Premium: func(d *server.Deps) market.PremiumGate {
			return entitlements.NamedGate{Service: entitlements.NewFromEnv(d.DB)}
		}},
		earnings.Feature{},
		// Identity: auth, tokens, OAuth server, profile and
		// onboarding.
		identity.Feature{},
		// PortfolioController, PortfolioShareController (authenticated
		// routes).
		portfolio.Feature{},
		// Portfolio management: /v1/portfolios CRUD, members,
		// invitations, cash positions, account assignment.
		portfoliomgmt.Feature{},
		// Portfolio rebalancing (portfolio-scoped routes) and simulations.
		// TODO(merge): set ScheduleAchievementCheck to achievementhooks.Schedule.
		rebalancing.Feature{},
		simulations.Feature{},
	}
}

// marketSearch adapts the market data service to assets.SymbolSearch; the
// result types share fields and tags.
type marketSearch struct{ svc *marketdata.Service }

func (m marketSearch) Search(ctx context.Context, q string) ([]assets.SearchResult, error) {
	rs, err := m.svc.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]assets.SearchResult, len(rs))
	for i, r := range rs {
		out[i] = assets.SearchResult(r)
	}
	return out, nil
}

// Job is a background loop: one Vapor LifecycleHandler.
type Job interface {
	Name() string
	// Run blocks until ctx is canceled.
	Run(ctx context.Context, d *server.Deps)
}

// AllJobs returns every ported background job.
func AllJobs() []Job {
	return []Job{
		identity.CleanupJob{},
		billing.TrialExpirationJob{},
		market.HistoryIngestionJob{},
		portfolio.SnapshotJob{},
	}
}

// Jobs tracks running jobs for shutdown.
type Jobs struct{ wg sync.WaitGroup }

// Wait blocks until every job returned.
func (j *Jobs) Wait() {
	done := make(chan struct{})
	go func() { j.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	}
}

// StartJobs starts the jobs named in V2_JOBS (comma-separated, or "all").
//
// During the migration Vapor still runs its own copies of these loops, and a
// job running in both processes would send every push and email twice. So a
// job runs in v2 only once it is listed here AND disabled on the Vapor side;
// the default is none.
func StartJobs(ctx context.Context, d *server.Deps) *Jobs {
	enabled := map[string]bool{}
	for _, n := range strings.Split(os.Getenv("V2_JOBS"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			enabled[n] = true
		}
	}
	j := &Jobs{}
	for _, job := range AllJobs() {
		if !enabled["all"] && !enabled[job.Name()] {
			continue
		}
		d.Logger.Info("job starting", "job", job.Name())
		j.wg.Add(1)
		go func() {
			defer j.wg.Done()
			job.Run(ctx, d)
		}()
	}
	return j
}
