package scenarios_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/scenarios"
	"norviq-api/internal/platform/metrics"
	"norviq-api/internal/store/scenariosdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// failRenew fails only the lease-renewal statement, as a database blip would.
type failRenew struct{ scenariosdb.DBTX }

func (f failRenew) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "RenewScenarioRunLease") {
		return pgconn.CommandTag{}, errors.New("renew: database blip")
	}
	return f.DBTX.Exec(ctx, sql, args...)
}

// hangRenew blocks the renewal until its context ends, as a saturated pool or a
// blackholed connection would.
type hangRenew struct{ scenariosdb.DBTX }

func (h hangRenew) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "RenewScenarioRunLease") {
		<-ctx.Done()
		return pgconn.CommandTag{}, ctx.Err()
	}
	return h.DBTX.Exec(ctx, sql, args...)
}

// A renewal that hangs counts as a failed one: the simulation stops instead of
// outliving its lease, and the worker's RunOnce does not stall behind it.
func TestHungRenewalCancelsTheSimulation(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("heartbeat-hang@example.com")
	list := e.portfolio(owner)
	sc := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "Big", "kind": "monte_carlo",
		"configuration": map[string]any{"values": map[string]any{"distribution": "normal", "path_count": 50000, "horizon_months": 600}},
	})
	snap := e.snapshot(owner, list, map[string]any{"total_value": 1000})
	run := e.createRun(owner, sc["id"].(string), snap, 1, 202)

	w := scenarios.NewRunWorker(e.Deps, scenarios.WorkerSettings{})
	scenarios.SetHeartbeat(w, 20*time.Millisecond)
	scenarios.SetRenewTimeout(w, 50*time.Millisecond)
	scenarios.SetQueries(w, scenariosdb.New(hangRenew{e.Deps.DB}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { w.RunOnce(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		<-done
		t.Fatal("a hung renewal stalled the worker")
	}
	if got := e.getRun(owner, run.ID); got.State != "running" || got.ErrorMessage != nil {
		t.Fatalf("run must stay leased, got %+v %v", got, got.ErrorMessage)
	}
}

// A heartbeat that cannot confirm the lease stops the simulation and leaves
// the run leased: not failed, so another worker can claim it after expiry.
func TestHeartbeatErrorCancelsAndLeavesTheRunReclaimable(t *testing.T) {
	e := newEnv(t, true)
	owner := e.proUser("heartbeat-error@example.com")
	list := e.portfolio(owner)
	sc := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "Big", "kind": "monte_carlo",
		"configuration": map[string]any{"values": map[string]any{"distribution": "normal", "path_count": 50000, "horizon_months": 600}},
	})
	snap := e.snapshot(owner, list, map[string]any{"total_value": 1000})
	run := e.createRun(owner, sc["id"].(string), snap, 1, 202)

	w := scenarios.NewRunWorker(e.Deps, scenarios.WorkerSettings{})
	scenarios.SetHeartbeat(w, 20*time.Millisecond)
	scenarios.SetQueries(w, scenariosdb.New(failRenew{e.Deps.DB}))
	done := make(chan struct{})
	go func() { w.RunOnce(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("simulation was not canceled by the failed heartbeat")
	}

	got := e.getRun(owner, run.ID)
	if got.State != "running" || got.ErrorMessage != nil || string(got.Result) != "null" {
		t.Fatalf("run must stay leased, got %+v %v", got, got.ErrorMessage)
	}
	e.exec(`UPDATE scenario_runs SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, run.ID)
	id, err := scenariosdb.New(e.Deps.DB).ClaimScenarioRun(context.Background(), "worker-b")
	if err != nil || id != uuid.MustParse(run.ID) {
		t.Fatalf("another worker could not reclaim the run: %v %v", id, err)
	}
}

// The worker publishes queue depth before it claims, only while Prometheus is on.
func TestWorkerPublishesQueueDepth(t *testing.T) {
	t.Setenv("PROMETHEUS_ENABLED", "true")
	e := newEnv(t, true)
	owner := e.proUser("queue-depth@example.com")
	list := e.portfolio(owner)
	sc := e.scenario(owner, map[string]any{
		"portfolioListId": list, "name": "MC", "kind": "monte_carlo",
		"configuration": map[string]any{"values": map[string]any{"distribution": "normal"}},
	})
	snap := e.snapshot(owner, list, map[string]any{"total_value": 1000})
	e.createRun(owner, sc["id"].(string), snap, 1, 202)

	metrics.Shared.SetScenarioQueueDepth(0)
	scenarios.NewRunWorker(e.Deps, scenarios.WorkerSettings{}).RunOnce(context.Background())
	if out := metrics.Shared.Render(); !strings.Contains(out, "scenario_queue_depth 1\n") {
		t.Fatal("queue depth not published from the worker")
	}
}
