package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
)

// captureBackend records the resolved Step every time RunTask fires,
// so tests can assert that StepTemplate inheritance happened *before*
// the backend got the spec.
type captureBackend struct {
	steps map[string][]backend.TaskInvocation
}

func (c *captureBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (c *captureBackend) Cleanup(_ context.Context) error                    { return nil }
func (c *captureBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	if c.steps == nil {
		c.steps = map[string][]backend.TaskInvocation{}
	}
	c.steps[inv.TaskName] = append(c.steps[inv.TaskName], inv)
	return backend.TaskResult{Status: backend.TaskSucceeded}, nil
}

func TestStepTemplateAppliedBeforeBackend(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  stepTemplate:
    image: alpine:3
    env:
      - {name: SHARED, value: hello}
  steps:
    - {name: a, script: 'true'}
    - {name: b, image: busybox, script: 'true'}
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
	be := &captureBackend{}
	sink := &sliceSink{}
	if _, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	inv := be.steps["t"][0]
	if got := inv.Task.Steps[0].Image; got != "alpine:3" {
		t.Errorf("step a image = %q, want alpine:3 (inherited)", got)
	}
	if got := inv.Task.Steps[1].Image; got != "busybox" {
		t.Errorf("step b image = %q, want busybox (override)", got)
	}
	if len(inv.Task.Steps[0].Env) != 1 || inv.Task.Steps[0].Env[0].Value != "hello" {
		t.Errorf("step a env = %+v, want SHARED=hello inherited", inv.Task.Steps[0].Env)
	}
}
