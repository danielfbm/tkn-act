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
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

func TestClusterE2EHello(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	files, _ := filepath.Glob("../../testdata/e2e/hello/*.yaml")
	b, err := loader.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}

	mgr := workspace.NewManager(t.TempDir(), "cluster-e2e")
	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	kubecfg := filepath.Join(t.TempDir(), "kubeconfig")
	cb := clusterbe.New(clusterbe.Options{
		CacheDir: t.TempDir(),
		Driver:   k3d.New(k3d.Options{ClusterName: "tkn-act-e2e", KubeconfigPath: kubecfg}),
	})
	t.Cleanup(func() { _ = cb.Cleanup(context.Background()) })

	rep := reporter.NewJSON(io.Discard)
	res, err := engine.New(cb, rep, engine.Options{}).RunPipeline(ctx, engine.PipelineInput{
		Bundle: b, Name: "hello",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Errorf("status = %s", res.Status)
	}
	_ = backend.Backend(cb) // compile-time check
}
