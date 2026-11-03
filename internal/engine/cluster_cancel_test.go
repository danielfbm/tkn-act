package engine_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// cancellingPipelineBackend mimics the cluster backend on Ctrl-C: the
// root context is cancelled mid-run and RunPipeline returns a wrapped
// context.Canceled.
type cancellingPipelineBackend struct{ cancel context.CancelFunc }

func (c *cancellingPipelineBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (c *cancellingPipelineBackend) Cleanup(_ context.Context) error                    { return nil }
func (c *cancellingPipelineBackend) RunTask(_ context.Context, _ backend.TaskInvocation) (backend.TaskResult, error) {
	return backend.TaskResult{}, nil
}
func (c *cancellingPipelineBackend) RunPipeline(ctx context.Context, _ backend.PipelineRunInvocation) (backend.PipelineRunResult, error) {
	c.cancel()
	return backend.PipelineRunResult{}, fmt.Errorf("watch pipelinerun: %w", ctx.Err())
}

// TestClusterCancelEmitsCancelledRunEnd: a cluster run interrupted by
// the user must end with run-end status "cancelled" (matching the
// docker engine and exit 130), not "failed".
func TestClusterCancelEmitsCancelledRunEnd(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: t, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &sliceSink{}
	e := engine.New(&cancellingPipelineBackend{cancel: cancel}, sink, engine.Options{})
	res, _ := e.RunPipeline(ctx, engine.PipelineInput{Bundle: b, Name: "p"})
	if res.Status != "cancelled" {
		t.Errorf("RunResult.Status = %q, want cancelled", res.Status)
	}
	var got string
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtRunEnd {
			got = ev.Status
		}
	}
	if got != "cancelled" {
		t.Errorf("run-end status = %q, want cancelled", got)
	}
}
