package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
)

// hangBackend blocks every RunTask until ctx is cancelled, so that any
// per-phase deadline triggers immediately.
type hangBackend struct{}

func (hangBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (hangBackend) Cleanup(_ context.Context) error                    { return nil }
func (hangBackend) RunTask(ctx context.Context, _ backend.TaskInvocation) (backend.TaskResult, error) {
	<-ctx.Done()
	return backend.TaskResult{Status: backend.TaskInfraFailed, Err: ctx.Err()}, nil
}

func TestPipelineLevelTimeoutTriggers(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: 'sleep 60'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "200ms"}
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	sink := &sliceSink{}
	res, err := engine.New(hangBackend{}, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout", res.Status)
	}
}

func TestTasksTimeoutDoesNotKillFinally(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: hang}
spec:
  steps: [{name: s, image: alpine, script: 'sleep 60'}]
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: cleanup}
spec:
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {tasks: "100ms", finally: "5s"}
  tasks:
    - {name: a, taskRef: {name: hang}}
  finally:
    - {name: c, taskRef: {name: cleanup}}
`))
	if err != nil {
		t.Fatal(err)
	}
	// A backend that succeeds quickly for `cleanup` but hangs for `hang`.
	be := &recBackend{
		hangFor: map[string]time.Duration{"a": 60 * time.Second},
	}
	sink := &sliceSink{}
	start := time.Now()
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout", res.Status)
	}
	if oc, ok := res.Tasks["c"]; !ok || oc.Status != "succeeded" {
		t.Errorf("finally cleanup = %+v, want succeeded", res.Tasks["c"])
	}
	if elapsed > 4*time.Second {
		t.Errorf("elapsed %v, want under 4s (tasks budget should not block finally)", elapsed)
	}
}

func TestNoTimeoutsBackwardCompatible(t *testing.T) {
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
    - {name: a, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	sink := &sliceSink{}
	res, err := engine.New(&recBackend{}, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", res.Status)
	}
}
