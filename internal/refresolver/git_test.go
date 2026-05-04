package refresolver_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// makeBareRepo builds a tiny bare repo on disk and pushes one commit into
// it that adds the named files at given paths. Returns the file:// URL
// and the SHA of the single commit. All work happens under t.TempDir()
// so cleanup is automatic.
func makeBareRepo(t *testing.T, files map[string]string) (url, sha string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "repo.git")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Avoid letting the developer's gpgsign / commit hooks
		// interfere; test repos must be hermetic.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=tkn-act-test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=tkn-act-test",
			"GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit(bare, "init", "--bare", "--initial-branch=main")
	runGit(work, "init", "--initial-branch=main")
	runGit(work, "remote", "add", "origin", bare)
	for path, body := range files {
		full := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(work, "add", path)
	}
	runGit(work, "commit", "-m", "seed", "--no-gpg-sign")
	runGit(work, "push", "origin", "main")
	sha = runGit(work, "rev-parse", "HEAD")
	url = "file://" + bare
	return url, sha
}

// TestGitResolverHappyPath: a local bare repo with one task YAML;
// resolver returns the task bytes verbatim.
func TestGitResolverHappyPath(t *testing.T) {
	taskYAML := `apiVersion: tekton.dev/v1
kind: Task
metadata: {name: greet}
spec:
  steps:
    - {name: hi, image: alpine, script: 'echo hi'}
`
	url, sha := makeBareRepo(t, map[string]string{"task.yaml": taskYAML})

	r := refresolver.NewGit(t.TempDir())
	got, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": url, "revision": "main", "pathInRepo": "task.yaml"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(got.Bytes) != taskYAML {
		t.Errorf("bytes mismatch:\n got: %q\nwant: %q", string(got.Bytes), taskYAML)
	}
	if got.SHA256 == "" {
		t.Errorf("expected SHA256 to be populated")
	}
	if !strings.Contains(got.Source, "task.yaml") {
		t.Errorf("source = %q, want it to mention task.yaml", got.Source)
	}
	// SHA the resolver discovered must match the actual HEAD. The Source
	// string includes the SHA so we check by substring.
	if !strings.Contains(got.Source, sha[:7]) {
		t.Errorf("source = %q, want it to mention commit prefix %q", got.Source, sha[:7])
	}
}

// TestGitResolverMissingPath: repo cloned fine, but pathInRepo doesn't
// exist in it; expect a clear error.
func TestGitResolverMissingPath(t *testing.T) {
	url, _ := makeBareRepo(t, map[string]string{"task.yaml": "apiVersion: tekton.dev/v1\nkind: Task\nmetadata: {name: x}\nspec: {steps: []}\n"})
	r := refresolver.NewGit(t.TempDir())
	_, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": url, "revision": "main", "pathInRepo": "nope.yaml"},
	})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(err.Error(), "pathInRepo") {
		t.Errorf("err = %q, want it to mention pathInRepo", err)
	}
}

// TestGitResolverRevisionMismatch: a non-existent revision must surface
// a clear error mentioning the revision.
func TestGitResolverRevisionMismatch(t *testing.T) {
	url, _ := makeBareRepo(t, map[string]string{"task.yaml": "x"})
	r := refresolver.NewGit(t.TempDir())
	_, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": url, "revision": "nonexistent-branch", "pathInRepo": "task.yaml"},
	})
	if err == nil {
		t.Fatal("expected error for nonexistent revision")
	}
	if !strings.Contains(err.Error(), "nonexistent-branch") {
		t.Errorf("err = %q, want mention of 'nonexistent-branch'", err)
	}
}

// TestGitResolverDefaultsRevisionToMain: when no revision param is set,
// the resolver clones the default branch ("main"). The bare repo we built
// uses --initial-branch=main, so an empty revision must still succeed.
func TestGitResolverDefaultsRevisionToMain(t *testing.T) {
	url, _ := makeBareRepo(t, map[string]string{"task.yaml": "hello"})
	r := refresolver.NewGit(t.TempDir())
	got, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": url, "pathInRepo": "task.yaml"},
	})
	if err != nil {
		t.Fatalf("resolve with default revision: %v", err)
	}
	if string(got.Bytes) != "hello" {
		t.Errorf("bytes = %q, want hello", got.Bytes)
	}
}

// TestGitResolverMissingURL: empty url param fails fast (before any
// network or filesystem work).
func TestGitResolverMissingURL(t *testing.T) {
	r := refresolver.NewGit(t.TempDir())
	_, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"pathInRepo": "task.yaml"},
	})
	if err == nil {
		t.Fatal("expected error for missing url")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("err = %q, want mention of url", err)
	}
}

// TestGitResolverMissingPathInRepo: empty pathInRepo fails fast.
func TestGitResolverMissingPathInRepo(t *testing.T) {
	r := refresolver.NewGit(t.TempDir())
	_, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": "file:///nowhere"},
	})
	if err == nil {
		t.Fatal("expected error for missing pathInRepo")
	}
	if !strings.Contains(err.Error(), "pathInRepo") {
		t.Errorf("err = %q, want mention of pathInRepo", err)
	}
}

// TestGitResolverRejectsHTTPByDefault: a plain http:// URL is refused
// unless the resolver is constructed with AllowInsecureHTTP=true. (The
// refresolver.Options.AllowInsecureHTTP plumbing already exists in the
// Registry; the git resolver respects it.)
func TestGitResolverRejectsHTTPByDefault(t *testing.T) {
	r := refresolver.NewGit(t.TempDir())
	_, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": "http://example.com/repo.git", "pathInRepo": "task.yaml"},
	})
	if err == nil {
		t.Fatal("expected error for plain http url")
	}
	if !strings.Contains(err.Error(), "http") || !strings.Contains(err.Error(), "insecure") {
		t.Errorf("err = %q, want mention of http+insecure", err)
	}
}

// TestGitResolverHonorsCacheDir: a second call with identical params
// reuses the on-disk cache and does not re-clone. Strategy: clone once,
// then move the bare repo aside; the resolver's second call must succeed
// from the cache (otherwise it would fail to fetch the missing repo).
func TestGitResolverHonorsCacheDir(t *testing.T) {
	url, _ := makeBareRepo(t, map[string]string{"task.yaml": "cached-bytes"})
	cacheDir := t.TempDir()
	r := refresolver.NewGit(cacheDir)

	first, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": url, "revision": "main", "pathInRepo": "task.yaml"},
	})
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if first.Cached {
		t.Errorf("first call: Cached=true, want false")
	}

	// Move the repo aside so any re-fetch attempt would fail.
	parent := filepath.Dir(strings.TrimPrefix(url, "file://"))
	bare := strings.TrimPrefix(url, "file://")
	if err := os.Rename(bare, filepath.Join(parent, "moved.git")); err != nil {
		t.Fatal(err)
	}

	second, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": url, "revision": "main", "pathInRepo": "task.yaml"},
	})
	if err != nil {
		t.Fatalf("second resolve (should hit cache): %v", err)
	}
	if string(second.Bytes) != "cached-bytes" {
		t.Errorf("second bytes = %q, want cached-bytes", second.Bytes)
	}
	if !second.Cached {
		t.Errorf("second call: Cached=false, want true")
	}
}

// TestGitResolverNetworkFailure: a non-existent file:// path fails with
// an error that mentions "git" and "clone" (so the engine's task-end
// message stays informative).
func TestGitResolverNetworkFailure(t *testing.T) {
	r := refresolver.NewGit(t.TempDir())
	_, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": "file:///definitely/not/a/repo", "revision": "main", "pathInRepo": "x"},
	})
	if err == nil {
		t.Fatal("expected error for missing repo")
	}
	if !strings.Contains(err.Error(), "git") {
		t.Errorf("err = %q, want mention of git", err)
	}
}

// TestGitResolverName: the resolver's Name() returns "git" and matches
// the canonical resolver name used in TaskRef.Resolver.
func TestGitResolverName(t *testing.T) {
	if got := refresolver.NewGit(t.TempDir()).Name(); got != "git" {
		t.Errorf("Name() = %q, want git", got)
	}
}

// TestNewDefaultRegistryRegistersGit: NewDefaultRegistry must include
// the git resolver alongside the inline stub. A request for "git" with
// missing-required-params errors at the resolver layer (not at the
// registry's "not registered" layer).
func TestNewDefaultRegistryRegistersGit(t *testing.T) {
	reg := refresolver.NewDefaultRegistry(refresolver.Options{
		CacheDir: t.TempDir(),
	})
	_, err := reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{}, // missing url/pathInRepo
	})
	if err == nil {
		t.Fatal("expected error for missing params")
	}
	// Should NOT be ErrResolverNotRegistered — git is now registered.
	if errors.Is(err, refresolver.ErrResolverNotRegistered) {
		t.Errorf("err = %v, want a resolver-layer error (not ErrResolverNotRegistered)", err)
	}
}
