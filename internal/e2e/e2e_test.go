//go:build integration

package e2e_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

func runFixture(t *testing.T, fixture, pipelineName string, params map[string]string, wantStatus string) {
	t.Helper()
	ctx := context.Background()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "e2e", fixture, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixture files in %s", fixture)
	}
	b, err := loader.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}

	mgr := workspace.NewManager(t.TempDir(), "e2e")
	wsHost := map[string]string{}
	for _, w := range b.Pipelines[pipelineName].Spec.Workspaces {
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
	for k, v := range params {
		pmap[k] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: v}
	}
	res, err := engine.New(be, rep, engine.Options{MaxParallel: 4}).RunPipeline(ctx, engine.PipelineInput{
		Bundle: b, Name: pipelineName, Params: pmap, Workspaces: wsHost,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.EqualFold(res.Status, wantStatus) {
		t.Errorf("status = %s, want %s", res.Status, wantStatus)
	}
}

func TestE2EHello(t *testing.T)              { runFixture(t, "hello", "hello", nil, "succeeded") }
func TestE2EMultilog(t *testing.T)            { runFixture(t, "multilog", "multilog", nil, "succeeded") }
func TestE2EParamsAndResults(t *testing.T)   { runFixture(t, "params-and-results", "chain", nil, "succeeded") }
func TestE2EWorkspaces(t *testing.T)         { runFixture(t, "workspaces", "ws-chain", nil, "succeeded") }
func TestE2EWhenSkipsDev(t *testing.T)       { runFixture(t, "when-and-finally", "whens", map[string]string{"env": "dev"}, "succeeded") }
func TestE2EWhenRunsProd(t *testing.T)       { runFixture(t, "when-and-finally", "whens", map[string]string{"env": "prod"}, "succeeded") }
func TestE2EFailurePropagation(t *testing.T) { runFixture(t, "failure-propagation", "failprop", nil, "failed") }
func TestE2EOnErrorContinue(t *testing.T)    { runFixture(t, "onerror", "best-effort", nil, "succeeded") }
func TestE2ERetries(t *testing.T)            { runFixture(t, "retries", "retries", nil, "succeeded") }
func TestE2ETimeout(t *testing.T)            { runFixture(t, "timeout", "hangs", nil, "timeout") }
