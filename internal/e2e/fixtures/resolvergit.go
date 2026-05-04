package fixtures

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BuildBareRepoFromSeed walks `seedDir`, copies its full tree into a
// fresh working repo under `tmpDir/work`, initialises a bare repo at
// `tmpDir/repo.git`, commits, and pushes. Returns the file:// URL of
// the bare repo so the e2e fixture's pipeline.yaml can resolve against
// it. Used by the docker and cluster e2e harnesses to materialise the
// `resolver-git` fixture's source-of-truth without committing a binary
// .git tree into the repo. Mirrors the helper used by the unit-test
// `internal/refresolver/git_test.go` so the harness shape and the
// unit-test shape stay aligned.
//
// `tmpDir` should typically be `t.TempDir()` from a Go test.
func BuildBareRepoFromSeed(seedDir, tmpDir string) (string, error) {
	bare := filepath.Join(tmpDir, "repo.git")
	work := filepath.Join(tmpDir, "work")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	if err := copyTree(seedDir, work); err != nil {
		return "", err
	}
	hermeticEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=tkn-act-e2e",
		"GIT_AUTHOR_EMAIL=e2e@tkn-act.test",
		"GIT_COMMITTER_NAME=tkn-act-e2e",
		"GIT_COMMITTER_EMAIL=e2e@tkn-act.test",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	runGit := func(dir string, args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = hermeticEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
		return nil
	}
	if err := runGit(bare, "init", "--bare", "--initial-branch=main"); err != nil {
		return "", err
	}
	if err := runGit(work, "init", "--initial-branch=main"); err != nil {
		return "", err
	}
	if err := runGit(work, "remote", "add", "origin", bare); err != nil {
		return "", err
	}
	if err := runGit(work, "add", "."); err != nil {
		return "", err
	}
	if err := runGit(work, "commit", "-m", "seed", "--no-gpg-sign"); err != nil {
		return "", err
	}
	if err := runGit(work, "push", "origin", "main"); err != nil {
		return "", err
	}
	return "file://" + bare, nil
}

// copyTree replicates src into dst preserving the relative file
// structure. Symlinks are resolved (we don't preserve them); this is
// fine for committed seed dirs which are plain files.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, bytes, 0o644)
	})
}
