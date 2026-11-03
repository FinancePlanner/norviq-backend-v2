// Package detached runs work that outlives its request but not the process:
// background exports, push fan-out.
//
// A bare sync.WaitGroup is not enough for this. After http.Server.Shutdown
// times out, handlers still running can start new work while serve is already
// in Wait, and an Add that races a Wait on a zero counter is WaitGroup misuse.
// Group closes first, under the same mutex Go takes, so no Add can follow it.
package detached

import "sync"

// Group is a set of detached goroutines that can be closed to new work.
// The zero value is ready to use.
type Group struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// Go runs fn in a new goroutine unless the group is closed, and reports
// whether it did.
func (g *Group) Go(fn func()) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.wg.Go(fn)
	return true
}

// CloseAndWait refuses new work, then waits for the running work to return.
func (g *Group) CloseAndWait() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.wg.Wait()
}

// Wait waits for the running work without closing the group. Tests only:
// serve uses CloseAndWait.
func (g *Group) Wait() { g.wg.Wait() }
