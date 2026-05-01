// Package workspace materializes Tekton workspaces as host directories that
// the Docker backend can bind-mount into Step containers. User-provided paths
// are used as-is and never deleted; auto-allocated tmpdirs are cleaned up on
// request.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

type Manager struct {
	root        string // base cache dir, e.g. $XDG_CACHE_HOME/tkn-act
	runID       string
	allocated   []string
	resultsDirs []string
}

func NewManager(root, runID string) *Manager {
	return &Manager{root: root, runID: runID}
}

// Provision returns a host path for the given workspace name. If userPath is
// non-empty, it's used as-is (and not cleaned up). Otherwise, a fresh tmpdir
// under root/runID/workspaces/<name> is created.
func (m *Manager) Provision(name, userPath string) (string, error) {
	if userPath != "" {
		st, err := os.Stat(userPath)
		if err != nil {
			return "", fmt.Errorf("workspace %q path %q: %w", name, userPath, err)
		}
		if !st.IsDir() {
			return "", fmt.Errorf("workspace %q path %q is not a directory", name, userPath)
		}
		return userPath, nil
	}
	dir := filepath.Join(m.root, m.runID, "workspaces", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir workspace %q: %w", name, err)
	}
	m.allocated = append(m.allocated, dir)
	return dir, nil
}

// ProvisionResultsDir creates a fresh per-task results directory.
func (m *Manager) ProvisionResultsDir(taskName string) (string, error) {
	dir := filepath.Join(m.root, m.runID, "results", taskName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir results %q: %w", taskName, err)
	}
	m.resultsDirs = append(m.resultsDirs, dir)
	return dir, nil
}

// Cleanup removes only directories Provision/ProvisionResultsDir allocated.
// User-supplied paths are never touched.
func (m *Manager) Cleanup() error {
	var firstErr error
	for _, d := range append(append([]string{}, m.allocated...), m.resultsDirs...) {
		if err := os.RemoveAll(d); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Best-effort: remove the run-id parent dir if empty.
	runDir := filepath.Join(m.root, m.runID)
	_ = os.Remove(runDir) // ignores ENOTEMPTY
	return firstErr
}

// AllocatedPaths returns the set of host paths the manager created (for
// preserving on failure / printing to the user).
func (m *Manager) AllocatedPaths() []string {
	out := make([]string, 0, len(m.allocated)+len(m.resultsDirs))
	out = append(out, m.allocated...)
	out = append(out, m.resultsDirs...)
	return out
}
