//go:build integration

package e2e_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/e2e/fixtures"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

func TestE2E(t *testing.T) {
	for _, f := range fixtures.All() {
		f := f
		if f.ClusterOnly {
			continue
		}
		t.Run(f.TestName(), func(t *testing.T) {
			runFixtureDocker(t, f)
		})
	}
}

func runFixtureDocker(t *testing.T, f fixtures.Fixture) {
	t.Helper()
	ctx := context.Background()
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

	mgr := workspace.NewManager(t.TempDir(), "e2e")
	wsHost := map[string]string{}
	for _, w := range b.Pipelines[f.Pipeline].Spec.Workspaces {
		p, err := mgr.Provision(w.Name, "")
		if err != nil {
			t.Fatal(err)
		}
		wsHost[w.Name] = p
	}

	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skipf("docker: %v", err)
	}

	rep := reporter.NewJSON(io.Discard)
	pmap := map[string]tektontypes.ParamValue{}
	for k, v := range f.Params {
		pmap[k] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: v}
	}

	cmStore := volumes.NewStore("")
	for name, kv := range f.ConfigMaps {
		for k, v := range kv {
			cmStore.Add(name, k, v)
		}
	}
	secStore := volumes.NewStore("")
	for name, kv := range f.Secrets {
		for k, v := range kv {
			secStore.Add(name, k, v)
		}
	}
	volResolver := func(taskName string, vs []tektontypes.Volume) (map[string]string, error) {
		volBase, perr := mgr.ProvisionVolumesDir(taskName)
		if perr != nil {
			return nil, perr
		}
		return volumes.MaterializeForTask(taskName, vs, volBase, cmStore, secStore)
	}

	res, err := engine.New(be, rep, engine.Options{MaxParallel: 4, VolumeResolver: volResolver}).RunPipeline(ctx, engine.PipelineInput{
		Bundle: b, Name: f.Pipeline, Params: pmap, Workspaces: wsHost,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.EqualFold(res.Status, f.WantStatus) {
		t.Errorf("status = %s, want %s (%s)", res.Status, f.WantStatus, f.Description)
	}
}
