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
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/features/feedback"
	"norviq-api/internal/features/market"
	"norviq-api/internal/features/notifications"
	"norviq-api/internal/server"
)

// All returns every ported feature, in Vapor's registration order where it
// matters (a route registered twice is an error, not an override).
func All() []server.Feature {
	return []server.Feature{
		// Earnings preferences wait for billing's premium gate
		// (notifications.Feature.Premium); assets search waits for market
		// data (assets.Feature.Search). Both stay on Vapor until wired.
		notifications.Feature{},
		activity.Feature{},
		badges.Feature{},
		feedback.Feature{},
		assets.Feature{},
		aiplatform.Feature{},
		// Pro-gated market routes wait for billing (market.Feature.Premium).
		market.Feature{},
		earnings.Feature{},
	}
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
		market.HistoryIngestionJob{},}
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
