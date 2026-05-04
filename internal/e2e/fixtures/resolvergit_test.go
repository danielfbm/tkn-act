package fixtures_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/e2e/fixtures"
)

// TestBuildBareRepoFromSeed exercises the helper end-to-end: a seed dir
// with one nested file produces a working bare repo whose first commit
// is reachable via `git ls-tree HEAD`. Used by the docker and cluster
// e2e harnesses to materialise the resolver-git fixture's bare repo.
func TestBuildBareRepoFromSeed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(filepath.Join(seed, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "tasks", "x.yaml"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	url, err := fixtures.BuildBareRepoFromSeed(seed, t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.HasPrefix(url, "file://") {
		t.Errorf("url = %q, want file:// prefix", url)
	}
	bare := strings.TrimPrefix(url, "file://")

	// The bare repo's HEAD tree must contain tasks/x.yaml.
	cmd := exec.Command("git", "--git-dir="+bare, "ls-tree", "-r", "main")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-tree: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "tasks/x.yaml") {
		t.Errorf("ls-tree main = %q, want tasks/x.yaml entry", out)
	}
}

// TestBuildBareRepoFromSeedMissingDir surfaces a clear error when the
// seed dir doesn't exist (caller misconfiguration, not a silent empty
// commit).
func TestBuildBareRepoFromSeedMissingDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	_, err := fixtures.BuildBareRepoFromSeed(filepath.Join(t.TempDir(), "missing"), t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing seed dir")
	}
}
