package marketbrief

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"norviq-api/internal/features/marketbrief/export"
)

// The export routes are public-safe by construction: the handler holds no
// user, session or preference dependency.
func TestExportHandlerHasNoUserDependencies(t *testing.T) {
	typ := reflect.TypeOf(exportHandlers{})
	want := []string{"repo", "baseURL", "cache"}
	if typ.NumField() != len(want) {
		t.Fatalf("fields: %d, want %d", typ.NumField(), len(want))
	}
	for i, name := range want {
		if typ.Field(i).Name != name {
			t.Fatalf("field %d is %s, want %s", i, typ.Field(i).Name, name)
		}
	}
}

// SetRenderForTest swaps the renderer behind the export route and returns a
// restore; the test's cleanup restores it too.
func SetRenderForTest(t testing.TB, f func(export.Format, export.Doc) ([]byte, string, error)) (restore func()) {
	prev := renderExport
	renderExport = f
	restore = func() { renderExport = prev }
	t.Cleanup(restore)
	return restore
}

// SetReportPagesForTest swaps the manifest's page counter until the test
// ends.
func SetReportPagesForTest(t testing.TB, f func(export.Doc) int) {
	prev := reportPages
	reportPages = f
	t.Cleanup(func() { reportPages = prev })
}

// Cold renders of distinct keys share exportRenderSlots: a burst of them
// must not render all at once, or the pod runs out of memory.
func TestRenderConcurrencyIsBounded(t *testing.T) {
	c := newExportCache(64, nil)
	var inflight, peak atomic.Int32
	render := func() (exportFile, error) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		return exportFile{data: []byte("x"), mime: "image/png"}, nil
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if _, err := c.fill(context.Background(), fmt.Sprintf("key-%d", i), render); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if p := peak.Load(); p < 1 || p > exportRenderSlots || exportRenderSlots > 2 {
		t.Fatalf("peak in-flight renders %d, slots %d; want at most 2", p, exportRenderSlots)
	}
}
