package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dfbmorinigo/tkn-act/internal/backend"
	"github.com/dfbmorinigo/tkn-act/internal/engine"
	"github.com/dfbmorinigo/tkn-act/internal/loader"
	"github.com/dfbmorinigo/tkn-act/internal/reporter"
	"github.com/dfbmorinigo/tkn-act/internal/tektontypes"
)

type fakeBackend struct {
	calls    []string
	scripted map[string]backend.TaskResult
}

func (f *fakeBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (f *fakeBackend) Cleanup(_ context.Context) error                    { return nil }
func (f *fakeBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	f.calls = append(f.calls, inv.TaskRunName)
	if r, ok := f.scripted[inv.TaskRunName]; ok {
		return r, nil
	}
	return backend.TaskResult{Status: backend.TaskSucceeded, Results: map[string]string{}}, nil
}

func TestEngineLinearOrder(t *testing.T) {
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
    - {name: b, taskRef: {name: t}, runAfter: [a]}
    - {name: c, taskRef: {name: t}, runAfter: [b]}
`))
	if err != nil {
		t.Fatal(err)
	}

	fb := &fakeBackend{}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Errorf("status = %s", res.Status)
	}
	if len(fb.calls) != 3 {
		t.Fatalf("calls = %v", fb.calls)
	}
	wantOrder := []string{"a", "b", "c"} // strict ordering for linear pipeline
	for i, w := range wantOrder {
		if !strings.Contains(fb.calls[i], w) {
			t.Errorf("call[%d] = %s, want contains %s", i, fb.calls[i], w)
		}
	}
}

func TestEngineFailurePropagation(t *testing.T) {
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
    - {name: b, taskRef: {name: t}, runAfter: [a]}
    - {name: c, taskRef: {name: t}, runAfter: [b]}
  finally:
    - {name: f, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}

	fb := &scriptedBackend{failSuffix: "-a"}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %s, want failed", res.Status)
	}
	if !fb.ranFinally {
		t.Error("finally task did not run")
	}
}

type scriptedBackend struct {
	failSuffix string
	ranFinally bool
}

func (s *scriptedBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (s *scriptedBackend) Cleanup(_ context.Context) error                    { return nil }
func (s *scriptedBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	if strings.HasSuffix(inv.TaskRunName, "-f") {
		s.ranFinally = true
	}
	if strings.HasSuffix(inv.TaskRunName, s.failSuffix) {
		return backend.TaskResult{Status: backend.TaskFailed}, nil
	}
	return backend.TaskResult{Status: backend.TaskSucceeded}, nil
}

func TestEngineWhenSkip(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: x, type: string}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params: [{name: env, type: string}]
  tasks:
    - name: a
      taskRef: {name: t}
      params: [{name: x, value: hi}]
      when:
        - input: $(params.env)
          operator: in
          values: [prod]
`))
	if err != nil {
		t.Fatal(err)
	}

	fb := &fakeBackend{}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4})
	_, err = e.RunPipeline(context.Background(), engine.PipelineInput{
		Bundle: b, Name: "p",
		Params: map[string]tektontypes.ParamValue{"env": {Type: tektontypes.ParamTypeString, StringVal: "dev"}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 calls (when skipped), got %v", fb.calls)
	}
}
