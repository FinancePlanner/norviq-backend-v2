package dataexport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBaseDir(t *testing.T) {
	t.Setenv("DATA_EXPORT_DIR", "")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := BaseDir(), filepath.Join(wd, ".build", "exports"); got != want {
		t.Fatalf("default BaseDir = %q, want %q", got, want)
	}
	t.Setenv("DATA_EXPORT_DIR", " /tmp/norviq-exports ")
	if got := BaseDir(); got != "/tmp/norviq-exports" {
		t.Fatalf("override BaseDir = %q", got)
	}
}
