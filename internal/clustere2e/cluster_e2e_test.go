//go:build cluster

package clustere2e_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	clusterbe "github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/e2e/fixtures"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

// One k3d cluster + Tekton install is shared across the whole fixture
// table — the per-fixture cost should be one PipelineRun, not a fresh
// cluster bring-up. Each subtest creates an ephemeral namespace inside
// that shared cluster.
func TestClusterE2E(t *testing.T) {
	dir := t.TempDir()
	kubecfg := filepath.Join(dir, "kubeconfig")
	cb := clusterbe.New(clusterbe.Options{
		CacheDir: dir,
		Driver:   k3d.New(k3d.Options{ClusterName: "tkn-act-e2e", KubeconfigPath: kubecfg}),
	})
	t.Cleanup(func() { _ = cb.Cleanup(context.Background()) })

	for _, f := range fixtures.All() {
		f := f
		if f.DockerOnly {
			continue
		}
		t.Run(f.TestName(), func(t *testing.T) {
			runFixtureCluster(t, cb, f)
		})
	}

	_ = backend.Backend(cb) // compile-time check
}

func runFixtureCluster(t *testing.T, cb *clusterbe.Backend, f fixtures.Fixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "e2e", f.Dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixture files in %s", f.Dir)
	}
	b, err := loader.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}

	mgr := workspace.NewManager(t.TempDir(), "cluster-e2e")
	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	pmap := map[string]tektontypes.ParamValue{}
	for k, v := range f.Params {
		pmap[k] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: v}
	}

	rep := reporter.NewJSON(io.Discard)
	res, err := engine.New(cb, rep, engine.Options{}).RunPipeline(ctx, engine.PipelineInput{
		Bundle: b, Name: f.Pipeline, Params: pmap,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != f.WantStatus {
		t.Errorf("status = %s, want %s (%s)", res.Status, f.WantStatus, f.Description)
	}
}
