package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
)

// TestStepActionResolvedBeforeBackend pins the engine wiring contract:
// resolveStepActions runs between lookupTaskSpec and applyStepTemplate /
// substituteSpec, so by the time the backend sees the Step, every Ref
// has been expanded into a concrete inlined body.
func TestStepActionResolvedBeforeBackend(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params:
    - {name: who, default: world}
  image: alpine:3
  script: 'echo hello $(params.who)'
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - name: g
      ref: {name: greet}
      params:
        - {name: who, value: tekton}
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
		t.Errorf("image = %q, want alpine:3 (inlined from StepAction)", got)
	}
	if got := inv.Task.Steps[0].Script; got != "echo hello tekton" {
		t.Errorf("script = %q, want with caller-param applied", got)
	}
	if inv.Task.Steps[0].Ref != nil {
		t.Errorf("backend received Ref-bearing Step; resolveStepActions did not run before submit")
	}
}

// TestStepActionUnknownRefFailsTask: when a Step.Ref points to a name
// not in the bundle, resolveStepActions errors out and the engine
// surfaces it as a task failure.
func TestStepActionUnknownRefFailsTask(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: g, ref: {name: nope}}
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
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed (unknown StepAction should fail the task)", res.Status)
	}
}
