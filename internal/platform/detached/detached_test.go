package detached

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestGoAfterCloseIsRefused(t *testing.T) {
	var g Group
	var ran atomic.Bool
	if !g.Go(func() { ran.Store(true) }) {
		t.Fatal("open group refused work")
	}
	g.CloseAndWait()
	if !ran.Load() {
		t.Fatal("CloseAndWait returned before the work")
	}
	if g.Go(func() { t.Error("ran after close") }) {
		t.Fatal("closed group accepted work")
	}
}

// Handlers racing a shutdown: under -race, an Add that slipped past the close
// would be reported as WaitGroup misuse.
func TestConcurrentGoAndCloseAndWait(t *testing.T) {
	for range 50 {
		var g Group
		var started, finished atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if g.Go(func() { finished.Add(1) }) {
					started.Add(1)
				}
			})
		}
		g.CloseAndWait()
		wg.Wait()
		g.CloseAndWait()
		if started.Load() != finished.Load() {
			t.Fatalf("started %d, finished %d", started.Load(), finished.Load())
		}
	}
}
