//go:build integration

package refresolver_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// TestGitResolverIntegrationPublicRepo hits a real public repo to make
// sure the go-git default transports (HTTPS) work end-to-end. The
// fixture is a pinned tag in tektoncd/catalog; a network-flake should
// surface as a t.Skip rather than a failure (tag is immutable, so the
// only failure mode is connectivity loss).
//
// Build tag `integration` keeps this off the default test set; it runs
// in `docker-integration.yml`.
func TestGitResolverIntegrationPublicRepo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	r := refresolver.NewGit(t.TempDir())
	got, err := r.Resolve(ctx, refresolver.Request{
		Resolver: "git",
		Params: map[string]string{
			"url":        "https://github.com/tektoncd/catalog",
			"revision":   "main",
			"pathInRepo": "task/git-clone/0.9/git-clone.yaml",
		},
	})
	if err != nil {
		// Connectivity failures are environmental; turn them into a
		// skip so a flaky CI runner doesn't fail the build. A genuine
		// regression in the resolver shows up as a non-network error
		// (parse, schema, missing file) which is NOT skipped.
		if strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "i/o timeout") {
			t.Skipf("network unavailable: %v", err)
		}
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(string(got.Bytes), "kind: Task") {
		t.Errorf("bytes did not contain 'kind: Task'; first 200 bytes:\n%s", firstN(string(got.Bytes), 200))
	}
	if got.SHA256 == "" {
		t.Error("SHA256 should be populated")
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
