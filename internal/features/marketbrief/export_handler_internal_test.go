package marketbrief

import (
	"reflect"
	"testing"

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
