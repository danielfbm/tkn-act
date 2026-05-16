package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/debug"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// TestEngine_EmitsTaskReadyDebugEvent: with a non-Nop debug emitter,
// the engine fires a "task ready" event for each PipelineTask it
// dispatches. Uses the sliceSink as both Reporter and indirect debug
// capture (debug events flow through the same reporter via
// debug.New(rep, true)).
func TestEngine_EmitsTaskReadyDebugEvent(t *testing.T) {
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
	be := &captureBackend{}
	sink := &sliceSink{}
	dbg := debug.New(sink, true)
	_, err = engine.New(be, sink, engine.Options{Debug: dbg}).
		RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}

	var sawTaskReady, sawParamsResolved bool
	for _, ev := range sink.events {
		if ev.Kind != reporter.EvtDebug || ev.Component != "engine" {
			continue
		}
		switch ev.Message {
		case "task ready":
			sawTaskReady = true
		case "params resolved":
			sawParamsResolved = true
		}
	}
	if !sawTaskReady {
		t.Errorf("missing 'task ready' debug event in %+v", sink.events)
	}
	if !sawParamsResolved {
		t.Errorf("missing 'params resolved' debug event in %+v", sink.events)
	}
}

// TestEngine_DebugDisabled_NoEvents: with Options.Debug nil (the
// default), no EvtDebug events appear from the engine.
func TestEngine_DebugDisabled_NoEvents(t *testing.T) {
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
	be := &captureBackend{}
	sink := &sliceSink{}
	_, err = engine.New(be, sink, engine.Options{}).
		RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtDebug {
			t.Errorf("unexpected debug event when --debug disabled: %+v", ev)
		}
	}
}

// TestEngine_WhenSkipEmitsDebug: a task that the when:-evaluator
// rejects fires a "task skipped" debug event with the reason and the
// truncated expression.
func TestEngine_WhenSkipEmitsDebug(t *testing.T) {
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
    - name: skipme
      taskRef: {name: t}
      when:
        - input: "no"
          operator: in
          values: ["yes"]
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &captureBackend{}
	sink := &sliceSink{}
	dbg := debug.New(sink, true)
	_, err = engine.New(be, sink, engine.Options{Debug: dbg}).
		RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtDebug && ev.Component == "engine" && ev.Message == "task skipped" {
			saw = true
			if ev.Fields["task"] != "skipme" {
				t.Errorf("task field = %v, want skipme", ev.Fields["task"])
			}
		}
	}
	if !saw {
		t.Errorf("missing 'task skipped' debug event in %+v", sink.events)
	}
}
