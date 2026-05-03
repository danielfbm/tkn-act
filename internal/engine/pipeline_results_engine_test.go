package engine_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// resultsBackend lets tests script per-task results.
type resultsBackend struct {
	results map[string]map[string]string // taskName → result name → value
	failFor map[string]bool              // taskName → return TaskFailed
}

func (b *resultsBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (b *resultsBackend) Cleanup(_ context.Context) error                    { return nil }
func (b *resultsBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	if b.failFor[inv.TaskName] {
		return backend.TaskResult{Status: backend.TaskFailed}, nil
	}
	return backend.TaskResult{
		Status:  backend.TaskSucceeded,
		Results: b.results[inv.TaskName],
	}, nil
}

func TestPipelineResultsSurfacedOnRunEnd(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: emit}
spec:
  results: [{name: commit}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: revision
      value: $(tasks.checkout.results.commit)
  tasks:
    - {name: checkout, taskRef: {name: emit}}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &resultsBackend{results: map[string]map[string]string{"checkout": {"commit": "abc123"}}}
	sink := &sliceSink{}
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Results["revision"]; got != "abc123" {
		t.Errorf("RunResult.Results[revision] = %v, want abc123", got)
	}
	// run-end event must carry the same map.
	var endEvt *reporter.Event
	for i := range sink.events {
		if sink.events[i].Kind == reporter.EvtRunEnd {
			ev := sink.events[i]
			endEvt = &ev
		}
	}
	if endEvt == nil {
		t.Fatalf("no run-end event in stream")
	}
	if got := endEvt.Results["revision"]; got != "abc123" {
		t.Errorf("run-end event Results[revision] = %v, want abc123", got)
	}
}

func TestPipelineResultsDroppedOnTaskFailure(t *testing.T) {
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
    - name: surfaced
      value: $(tasks.ok.results.v)
    - name: dropped
      value: $(tasks.bad.results.v)
  tasks:
    - {name: ok,  taskRef: {name: t}}
    - {name: bad, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &resultsBackend{
		results: map[string]map[string]string{"ok": {"v": "yes"}},
		failFor: map[string]bool{"bad": true},
	}
	sink := &sliceSink{}
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed (a task failed; result drop must not change run status)", res.Status)
	}
	if got := res.Results["surfaced"]; got != "yes" {
		t.Errorf("Results[surfaced] = %v, want yes (task succeeded; result must surface)", got)
	}
	if _, present := res.Results["dropped"]; present {
		t.Errorf("Results[dropped] should be omitted, got %v", res.Results["dropped"])
	}
	// One EvtError event for the dropped result.
	var errEvts int
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtError {
			errEvts++
		}
	}
	if errEvts != 1 {
		t.Errorf("EvtError count = %d, want 1 (one dropped result)", errEvts)
	}
}

func TestPipelineResultsArrayAndObject(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: emit}
spec:
  results: [{name: a}, {name: b}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: list
      value:
        - $(tasks.t.results.a)
        - $(tasks.t.results.b)
    - name: meta
      value:
        first:  $(tasks.t.results.a)
        second: $(tasks.t.results.b)
  tasks:
    - {name: t, taskRef: {name: emit}}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &resultsBackend{results: map[string]map[string]string{"t": {"a": "alpha", "b": "beta"}}}
	sink := &sliceSink{}
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	gotList, ok := res.Results["list"].([]string)
	if !ok {
		t.Fatalf("Results[list] type = %T, want []string", res.Results["list"])
	}
	if !reflect.DeepEqual(gotList, []string{"alpha", "beta"}) {
		t.Errorf("Results[list] = %v, want [alpha beta]", gotList)
	}
	gotMeta, ok := res.Results["meta"].(map[string]string)
	if !ok {
		t.Fatalf("Results[meta] type = %T, want map[string]string", res.Results["meta"])
	}
	if gotMeta["first"] != "alpha" || gotMeta["second"] != "beta" {
		t.Errorf("Results[meta] = %v, want first=alpha second=beta", gotMeta)
	}
}
