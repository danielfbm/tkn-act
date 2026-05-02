package loader_test

import (
	"path/filepath"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
)

// TestLimitationsFixturesParse confirms that the illustrative pipelines under
// testdata/limitations all parse — they exist precisely because the docker
// backend drops fields at parse time without erroring. If a future schema
// tightening starts rejecting `onError`, `sidecars`, `retries`, etc., this
// test will fail and force the README's claims to be revisited.
func TestLimitationsFixturesParse(t *testing.T) {
	repoRoot, _ := filepath.Abs("../../")
	dirs := []struct {
		name     string
		pipeline string
	}{
		{"step-state", "leaky"},
		{"sidecars", "with-redis"},
	}
	for _, d := range dirs {
		t.Run(d.name, func(t *testing.T) {
			files, _ := filepath.Glob(filepath.Join(repoRoot, "testdata", "limitations", d.name, "*.yaml"))
			if len(files) == 0 {
				t.Fatalf("no yaml files in %s", d.name)
			}
			b, err := loader.LoadFiles(files)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if _, ok := b.Pipelines[d.pipeline]; !ok {
				got := make([]string, 0, len(b.Pipelines))
				for k := range b.Pipelines {
					got = append(got, k)
				}
				t.Fatalf("expected pipeline %q, got %v", d.pipeline, got)
			}
		})
	}
}
