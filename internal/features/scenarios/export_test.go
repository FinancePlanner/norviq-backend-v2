package scenarios

import "time"

// SetHeartbeat shortens the lease-renewal period (tests).
func SetHeartbeat(w *RunWorker, d time.Duration) { w.heartbeat = d }
