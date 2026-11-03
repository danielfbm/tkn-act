package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danielfbm/tkn-act/internal/workspace"
)

func TestProvisionAllocatesTmpdir(t *testing.T) {
	tmp := t.TempDir()
	mgr := workspace.NewManager(tmp, "run-1")
	got, err := mgr.Provision("source", "")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got == "" {
		t.Fatal("empty path")
	}
	st, err := os.Stat(got)
	if err != nil || !st.IsDir() {
		t.Errorf("expected dir at %q: %v", got, err)
	}
	if filepath.Dir(filepath.Dir(got)) != filepath.Join(tmp, "run-1") {
		t.Errorf("workspace not under run dir: %s", got)
	}
}

// TestProvisionedDirsWorldWritable: Tekton mounts workspaces and
// results as 0777 emptyDirs, so images running as a non-root USER can
// write $(results.x.path). The docker backend must match regardless of
// the process umask.
func TestProvisionedDirsWorldWritable(t *testing.T) {
	mgr := workspace.NewManager(t.TempDir(), "run-1")
	ws, err := mgr.Provision("source", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := mgr.ProvisionResultsDir("build")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{ws, res} {
		st, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != 0o777 {
			t.Errorf("%s mode = %o, want 777", d, got)
		}
	}
}

func TestProvisionUsesUserPath(t *testing.T) {
	tmp := t.TempDir()
	user := filepath.Join(tmp, "my-source")
	if err := os.Mkdir(user, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := workspace.NewManager(tmp, "run-2")
	got, err := mgr.Provision("source", user)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got != user {
		t.Errorf("got %q, want %q", got, user)
	}
}

func TestCleanupRemovesProvisionedDirs(t *testing.T) {
	tmp := t.TempDir()
	mgr := workspace.NewManager(tmp, "run-3")
	got, err := mgr.Provision("source", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Errorf("expected %q gone, err=%v", got, err)
	}
}

func TestCleanupSkipsUserPaths(t *testing.T) {
	tmp := t.TempDir()
	user := filepath.Join(tmp, "keep")
	if err := os.Mkdir(user, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := workspace.NewManager(tmp, "run-4")
	if _, err := mgr.Provision("source", user); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(user); err != nil {
		t.Errorf("user path %q removed: %v", user, err)
	}
}
