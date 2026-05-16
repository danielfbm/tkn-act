package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/debug"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// TestParamsResolved_RedactsSecretLikeNames: param values whose name
// matches the secret-like patterns must be replaced with <redacted>
// in the "params resolved" debug event so events.jsonl doesn't
// archive credentials.
func TestParamsResolved_RedactsSecretLikeNames(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params:
    - {name: GITHUB_TOKEN}
    - {name: API_KEY}
    - {name: USER_PASSWORD}
    - {name: REGION}
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params:
    - {name: GITHUB_TOKEN, default: "ghp_supersecret_1234567890"}
    - {name: API_KEY, default: "AKIA_supersecret"}
    - {name: USER_PASSWORD, default: "hunter2"}
    - {name: REGION, default: "us-west-2"}
  tasks:
    - name: a
      taskRef: {name: t}
      params:
        - {name: GITHUB_TOKEN, value: "$(params.GITHUB_TOKEN)"}
        - {name: API_KEY, value: "$(params.API_KEY)"}
        - {name: USER_PASSWORD, value: "$(params.USER_PASSWORD)"}
        - {name: REGION, value: "$(params.REGION)"}
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
	var found bool
	for _, ev := range sink.events {
		if ev.Kind != reporter.EvtDebug || ev.Message != "params resolved" {
			continue
		}
		found = true
		preview, ok := ev.Fields["truncated_values"].(map[string]string)
		if !ok {
			t.Fatalf("truncated_values is not map[string]string: %T", ev.Fields["truncated_values"])
		}
		for _, secretKey := range []string{"GITHUB_TOKEN", "API_KEY", "USER_PASSWORD"} {
			if v := preview[secretKey]; v != "<redacted>" {
				t.Errorf("%s leaked through: got %q, want <redacted>", secretKey, v)
			}
		}
		if v := preview["REGION"]; v != "us-west-2" {
			t.Errorf("REGION redacted unexpectedly: %q", v)
		}
	}
	if !found {
		t.Fatal("never saw a 'params resolved' debug event")
	}
}

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
