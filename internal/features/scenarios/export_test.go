package scenarios

import (
	"time"

	"norviq-api/internal/store/scenariosdb"
)

// SetHeartbeat shortens the lease-renewal period (tests).
func SetHeartbeat(w *RunWorker, d time.Duration) { w.heartbeat = d }

// HistoricalPreset exposes historicalPreset (tests).
func HistoricalPreset(configuration map[string]any) string { return historicalPreset(configuration) }

// SetRenewTimeout shortens the bound on one lease renewal (tests).
func SetRenewTimeout(w *RunWorker, d time.Duration) { w.renewTimeout = d }

// SetQueries replaces the worker's queries, so a test can fail one statement.
func SetQueries(w *RunWorker, q *scenariosdb.Queries) { w.q = q }
