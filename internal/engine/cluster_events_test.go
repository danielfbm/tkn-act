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

// TestClusterEngineEmitsErrorPerDroppedPipelineResult: when the cluster
// backend returns a RunResult whose Results map is missing some entries
// declared in Pipeline.spec.results, the engine must synthesize one
// EvtError per dropped name (matching the docker path's behavior). The
// EvtError sequence must precede EvtRunEnd so consumers see the drop
// reasons before the terminal event. PR #18 reviewer Imp-2.
func TestClusterEngineEmitsErrorPerDroppedPipelineResult(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: present
      value: $(tasks.a.results.v)
    - name: missing-one
      value: $(tasks.a.results.gone)
    - name: missing-two
      value: $(tasks.a.results.alsogone)
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakePipelineBackend{result: backend.PipelineRunResult{
		Status: "succeeded",
		// Backend supplies only "present"; the other two declared
		// results are absent and must be reported as dropped.
		Results: map[string]any{"present": "abc"},
		Tasks: map[string]backend.TaskOutcomeOnCluster{
			"a": {Status: "succeeded", Attempts: 1},
		},
	}}
	sink := &sliceSink{}
	if _, err := engine.New(fb, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}

	// Collect EvtError events in order, plus the index of the run-end.
	var errEvents []reporter.Event
	runEndIdx := -1
	for i, ev := range sink.events {
		if ev.Kind == reporter.EvtError {
			errEvents = append(errEvents, ev)
		}
		if ev.Kind == reporter.EvtRunEnd {
			runEndIdx = i
		}
	}
	if runEndIdx < 0 {
		t.Fatalf("no run-end event in %d events", len(sink.events))
	}
	if got := len(errEvents); got != 2 {
		t.Fatalf("error events = %d, want 2 (one per dropped result)", got)
	}
	// Drop messages must mention each dropped name. Ordering must be
	// deterministic — the engine sorts the dropped names so log diffs
	// stay stable.
	for i, want := range []string{"missing-one", "missing-two"} {
		if !strings.Contains(errEvents[i].Message, want) {
			t.Errorf("error[%d] = %q, want substring %q", i, errEvents[i].Message, want)
		}
	}
	// Every EvtError must precede EvtRunEnd so consumers can attribute
	// drops without buffering past the terminal event.
	for i, ev := range sink.events {
		if ev.Kind == reporter.EvtError && i > runEndIdx {
			t.Errorf("EvtError at index %d arrived after EvtRunEnd at %d", i, runEndIdx)
		}
	}
}

// TestClusterEngineNoErrorsWhenAllResultsResolve: a fully-populated
// Results map must produce zero EvtError events.
func TestClusterEngineNoErrorsWhenAllResultsResolve(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: r
      value: $(tasks.a.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakePipelineBackend{result: backend.PipelineRunResult{
		Status:  "succeeded",
		Results: map[string]any{"r": "xyz"},
		Tasks:   map[string]backend.TaskOutcomeOnCluster{"a": {Status: "succeeded", Attempts: 1}},
	}}
	sink := &sliceSink{}
	if _, err := engine.New(fb, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtError {
			t.Errorf("unexpected EvtError when all results resolved: %q", ev.Message)
		}
	}
}

// TestClusterEngineNoErrorsWhenNoSpecResults: a Pipeline that doesn't
// declare spec.results must not emit any EvtError, regardless of what
// the backend hands back.
func TestClusterEngineNoErrorsWhenNoSpecResults(t *testing.T) {
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
	fb := &fakePipelineBackend{result: backend.PipelineRunResult{
		Status: "succeeded",
		Tasks:  map[string]backend.TaskOutcomeOnCluster{"a": {Status: "succeeded", Attempts: 1}},
	}}
	sink := &sliceSink{}
	if _, err := engine.New(fb, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtError {
			t.Errorf("unexpected EvtError for pipeline with no spec.results: %q", ev.Message)
		}
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
