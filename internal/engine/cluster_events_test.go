package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// fakePipelineBackend returns a scripted PipelineRunResult, exercising the
// engine's runViaPipelineBackend path — including emitClusterTaskEvents,
// which is what we're locking in here.
type fakePipelineBackend struct{ result backend.PipelineRunResult }

func (f *fakePipelineBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (f *fakePipelineBackend) Cleanup(_ context.Context) error                    { return nil }
func (f *fakePipelineBackend) RunTask(_ context.Context, _ backend.TaskInvocation) (backend.TaskResult, error) {
	return backend.TaskResult{}, nil
}
func (f *fakePipelineBackend) RunPipeline(_ context.Context, _ backend.PipelineRunInvocation) (backend.PipelineRunResult, error) {
	return f.result, nil
}

// TestClusterEngineEmitsTaskRetryEvents covers Track 2 #4: when the cluster
// backend reports a TaskOutcomeOnCluster with two RetryAttempts, the engine
// must emit one task-start, two task-retry events, and one task-end with
// Attempt: 3 — shape-equivalent to what the docker engine would emit live.
func TestClusterEngineEmitsTaskRetryEvents(t *testing.T) {
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
    - {name: t, taskRef: {name: t}, retries: 3}
`))
	if err != nil {
		t.Fatal(err)
	}

	fb := &fakePipelineBackend{result: backend.PipelineRunResult{
		Status: "succeeded",
		Tasks: map[string]backend.TaskOutcomeOnCluster{
			"t": {
				Status:   "succeeded",
				Attempts: 3,
				RetryAttempts: []backend.RetryAttempt{
					{Attempt: 1, Status: "failed", Message: "first failure"},
					{Attempt: 2, Status: "failed", Message: "second failure"},
				},
			},
		},
	}}
	sink := &sliceSink{}
	e := engine.New(fb, sink, engine.Options{})
	if _, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}

	starts, retries, ends := 0, 0, 0
	var endEvent reporter.Event
	for _, ev := range sink.events {
		switch ev.Kind {
		case reporter.EvtTaskStart:
			if ev.Task == "t" {
				starts++
			}
		case reporter.EvtTaskRetry:
			if ev.Task == "t" {
				retries++
				if ev.Attempt != retries {
					t.Errorf("retry[%d].Attempt = %d, want %d", retries-1, ev.Attempt, retries)
				}
			}
		case reporter.EvtTaskEnd:
			if ev.Task == "t" {
				ends++
				endEvent = ev
			}
		}
	}
	if starts != 1 || retries != 2 || ends != 1 {
		t.Errorf("starts=%d retries=%d ends=%d, want 1/2/1", starts, retries, ends)
	}
	if endEvent.Status != "succeeded" {
		t.Errorf("end status = %s, want succeeded", endEvent.Status)
	}
	if endEvent.Attempt != 3 {
		t.Errorf("end attempt = %d, want 3", endEvent.Attempt)
	}
}

// TestClusterEngineSurfacesReasonAndMessage: the engine must thread the
// cluster backend's terminal Reason/Message through to RunResult and onto
// the run-end event. Without this, a misclassified run shows up as
// `status = X` with no attribution — which is exactly the state that
// made the pipeline-timeout cluster-CI flake un-debuggable from CI logs
// alone (see fix(cluster): pipeline-timeout via skippedTasks).
func TestClusterEngineSurfacesReasonAndMessage(t *testing.T) {
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
	fb := &fakePipelineBackend{result: backend.PipelineRunResult{
		Status:  "failed",
		Reason:  "PipelineValidationFailed",
		Message: "Pipeline p/p can't be Run; it has an invalid spec",
		Tasks:   map[string]backend.TaskOutcomeOnCluster{},
	}}
	sink := &sliceSink{}
	res, err := engine.New(fb, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != "PipelineValidationFailed" {
		t.Errorf("RunResult.Reason = %q, want PipelineValidationFailed", res.Reason)
	}
	if res.Message != "Pipeline p/p can't be Run; it has an invalid spec" {
		t.Errorf("RunResult.Message = %q, want diagnostic substring", res.Message)
	}
	var saw bool
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtRunEnd {
			saw = true
			// The run-end event message must include the Reason so
			// `--output json` consumers can attribute the outcome.
			if ev.Message == "" || !strings.Contains(ev.Message, "PipelineValidationFailed") {
				t.Errorf("run-end message = %q, want substring 'PipelineValidationFailed'", ev.Message)
			}
		}
	}
	if !saw {
		t.Errorf("no run-end event in %d events", len(sink.events))
	}
}

// TestClusterEngineNoRetriesEmitsAttempt1: a one-attempt task should still
// carry Attempt: 1 on its task-end event so consumers don't have to guess
// between "missing" and "first try."
func TestClusterEngineNoRetriesEmitsAttempt1(t *testing.T) {
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
	fb := &fakePipelineBackend{result: backend.PipelineRunResult{
		Status: "succeeded",
		Tasks: map[string]backend.TaskOutcomeOnCluster{
			"t": {Status: "succeeded", Attempts: 1},
		},
	}}
	sink := &sliceSink{}
	if _, err := engine.New(fb, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtTaskEnd && ev.Task == "t" {
			if ev.Attempt != 1 {
				t.Errorf("end attempt = %d, want 1", ev.Attempt)
			}
			return
		}
	}
	t.Errorf("no task-end event for t in %d events", len(sink.events))
}
