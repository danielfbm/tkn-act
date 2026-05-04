package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
)

func TestSidecarParamsSubstitutedBeforeBackend(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params:
    - {name: redis_pass, default: "hunter2"}
  sidecars:
    - name: redis
      image: redis:7-alpine
      env:
        - {name: PASS, value: $(params.redis_pass)}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
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
	if _, err := engine.New(be, sink, engine.Options{}).RunPipeline(
		context.Background(), engine.PipelineInput{Bundle: b, Name: "p"},
	); err != nil {
		t.Fatal(err)
	}
	inv := be.steps["t"][0]
	if len(inv.Task.Sidecars) != 1 {
		t.Fatalf("sidecars = %d, want 1", len(inv.Task.Sidecars))
	}
	if got := inv.Task.Sidecars[0].Env[0].Value; got != "hunter2" {
		t.Errorf("sidecar env value = %q, want hunter2 (substituted)", got)
	}
}

func TestSidecarImageSubstituted(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params:
    - {name: image_tag, default: "7-alpine"}
  sidecars:
    - name: redis
      image: redis:$(params.image_tag)
  steps:
    - {name: s, image: alpine:3, script: 'true'}
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
	if _, err := engine.New(be, sink, engine.Options{}).RunPipeline(
		context.Background(), engine.PipelineInput{Bundle: b, Name: "p"},
	); err != nil {
		t.Fatal(err)
	}
	inv := be.steps["t"][0]
	if got := inv.Task.Sidecars[0].Image; got != "redis:7-alpine" {
		t.Errorf("sidecar image = %q, want redis:7-alpine (substituted)", got)
	}
}
