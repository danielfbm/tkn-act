package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dfbmorinigo/tkn-act/internal/workspace"
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
