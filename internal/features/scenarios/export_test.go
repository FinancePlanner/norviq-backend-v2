package scenarios

import "time"

// SetHeartbeat shortens the lease-renewal period (tests).
func SetHeartbeat(w *RunWorker, d time.Duration) { w.heartbeat = d }

// HistoricalPreset exposes historicalPreset (tests).
func HistoricalPreset(configuration map[string]any) string { return historicalPreset(configuration) }
