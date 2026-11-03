package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danielfbm/tkn-act/internal/workspace"
)

// A retried task re-provisions its results dir under the same name; the
// new attempt must not see the previous attempt's result files.
func TestProvisionResultsDirIsFreshPerCall(t *testing.T) {
	m := workspace.NewManager(t.TempDir(), "run1")
	d, err := m.ProvisionResultsDir("build")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "out"), []byte("attempt-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	d2, err := m.ProvisionResultsDir("build")
	if err != nil {
		t.Fatal(err)
	}
	if d2 != d {
		t.Fatalf("dir changed: %q vs %q", d, d2)
	}
	if _, err := os.Stat(filepath.Join(d2, "out")); !os.IsNotExist(err) {
		t.Errorf("stale result file survived re-provision (err=%v)", err)
	}
	if n := len(m.AllocatedPaths()); n != 1 {
		t.Errorf("AllocatedPaths has %d entries after re-provision, want 1", n)
	}
}
