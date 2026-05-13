package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentGuideFreshness re-runs the generator into a tempdir and
// compares the result against the checked-in cmd/tkn-act/agentguide_data
// tree. A mismatch means somebody edited docs/agent-guide/ without
// running `go generate ./cmd/tkn-act/` (or `make agentguide`).
//
// This test lives in the default test set so the existing
// `tests-required` and `coverage` CI gates catch the drift.
func TestAgentGuideFreshness(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("cannot locate repo root: %v", err)
	}
	src := filepath.Join(repoRoot, "docs", "agent-guide")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("docs/agent-guide not present at %s: %v", src, err)
	}

	dst := t.TempDir()

	cmd := exec.Command("go", "run", "./internal/agentguide-gen",
		"-src", src,
		"-dst", dst,
	)
	cmd.Dir = filepath.Join(repoRoot, "cmd", "tkn-act")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("regenerate: %v\noutput:\n%s", err, out)
	}

	// Diff dst (freshly generated) against the embedded tree.
	embeddedDir := filepath.Join(repoRoot, "cmd", "tkn-act", agentGuideDataDir)
	diffs := compareTrees(t, dst, embeddedDir)
	if len(diffs) > 0 {
		t.Errorf("agent-guide data tree is stale (run: go generate ./cmd/tkn-act/):\n  %s",
			strings.Join(diffs, "\n  "))
	}
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
	}
	return "", os.ErrNotExist
}

func compareTrees(t *testing.T, a, b string) []string {
	t.Helper()
	aFiles := readTree(t, a)
	bFiles := readTree(t, b)

	var diffs []string
	for name, ab := range aFiles {
		bb, ok := bFiles[name]
		if !ok {
			diffs = append(diffs, "missing in committed tree: "+name)
			continue
		}
		if !bytes.Equal(ab, bb) {
			diffs = append(diffs, "byte-diff in "+name)
		}
	}
	for name := range bFiles {
		if _, ok := aFiles[name]; !ok {
			diffs = append(diffs, "stale in committed tree (not produced by generator): "+name)
		}
	}
	return diffs
}

func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = body
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}
