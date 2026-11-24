package marketbrief

import (
	"reflect"
	"testing"
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
