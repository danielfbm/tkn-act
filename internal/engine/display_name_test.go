package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

func TestEngineEmitsDisplayNameOnRunAndTaskEvents(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: tk}
spec:
  displayName: "Unit-test runner"
  description: "Runs go test."
  steps:
    - name: s
      displayName: "Compile"
      description: "Compile."
      image: alpine:3
      script: 'true'
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  displayName: "Build & test"
  description: "Build then test."
  tasks:
    - name: t1
      displayName: "Compile binary"
      taskRef: {name: tk}
`))
	if err != nil {
		t.Fatal(err)
	}
	sink := &sliceSink{}
	be := &captureBackend{}
	if _, err := engine.New(be, sink, engine.Options{}).RunPipeline(
		context.Background(),
		engine.PipelineInput{Bundle: b, Name: "p"},
	); err != nil {
		t.Fatal(err)
	}

	got := map[reporter.EventKind]reporter.Event{}
	for _, e := range sink.events {
		// keep the first of each kind
		if _, ok := got[e.Kind]; !ok {
			got[e.Kind] = e
		}
	}

	if e := got[reporter.EvtRunStart]; e.DisplayName != "Build & test" || e.Description != "Build then test." {
		t.Errorf("run-start display_name=%q description=%q", e.DisplayName, e.Description)
	}
	if e := got[reporter.EvtRunEnd]; e.DisplayName != "Build & test" {
		t.Errorf("run-end display_name=%q", e.DisplayName)
	}
	if e := got[reporter.EvtTaskStart]; e.DisplayName != "Compile binary" || e.Description != "Runs go test." {
		t.Errorf("task-start display_name=%q description=%q", e.DisplayName, e.Description)
	}
	if e := got[reporter.EvtTaskEnd]; e.DisplayName != "Compile binary" {
		t.Errorf("task-end display_name=%q", e.DisplayName)
	}
}
