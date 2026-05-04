package engine_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// recordingPipelineBackend captures the Pipeline as the engine submits
// it, so tests can assert resolver-backed taskRefs were inlined into
// taskSpec before submission.
type recordingPipelineBackend struct {
	mu       sync.Mutex
	received tektontypes.Pipeline
	tasks    map[string]tektontypes.Task
	result   backend.PipelineRunResult
}

func (r *recordingPipelineBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (r *recordingPipelineBackend) Cleanup(_ context.Context) error                    { return nil }
func (r *recordingPipelineBackend) RunTask(_ context.Context, _ backend.TaskInvocation) (backend.TaskResult, error) {
	return backend.TaskResult{}, nil
}
func (r *recordingPipelineBackend) RunPipeline(_ context.Context, in backend.PipelineRunInvocation) (backend.PipelineRunResult, error) {
	r.mu.Lock()
	r.received = in.Pipeline
	r.tasks = in.Tasks
	r.mu.Unlock()
	if r.result.Status == "" {
		r.result = backend.PipelineRunResult{
			Status: "succeeded",
			Tasks: map[string]backend.TaskOutcomeOnCluster{
				"build": {Status: "succeeded"},
			},
		}
	}
	return r.result, nil
}

// TestClusterBackendInlinesResolverBackedRefs: the cluster backend
// receives a Pipeline whose taskRef.resolver block has been replaced
// with an inlined TaskSpec — so local k3d's Tekton (which has no
// resolver creds) doesn't try to fetch.
func TestClusterBackendInlinesResolverBackedRefs(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: inline
        params:
          - {name: name, value: "build-task"}
`))
	if err != nil {
		t.Fatal(err)
	}
	reg := refresolver.NewRegistry()
	inline := refresolver.NewInlineResolver()
	inline.Add("build-task", []byte(`apiVersion: tekton.dev/v1
kind: Task
metadata: {name: build}
spec:
  steps:
    - {name: s, image: alpine:3, script: 'true'}
`))
	reg.Register(inline)

	rec := &recordingPipelineBackend{}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(rec, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s", res.Status)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.received.Spec.Tasks) != 1 {
		t.Fatalf("submitted Pipeline has %d tasks, want 1", len(rec.received.Spec.Tasks))
	}
	pt := rec.received.Spec.Tasks[0]
	if pt.TaskRef != nil && pt.TaskRef.Resolver != "" {
		t.Errorf("submitted PipelineTask still carries resolver block: %+v", pt.TaskRef)
	}
	if pt.TaskSpec == nil || len(pt.TaskSpec.Steps) == 0 {
		t.Errorf("expected inlined TaskSpec with steps, got %+v", pt.TaskSpec)
	}
	if pt.TaskSpec != nil && pt.TaskSpec.Steps[0].Image != "alpine:3" {
		t.Errorf("step image = %q, want alpine:3", pt.TaskSpec.Steps[0].Image)
	}
}

// TestClusterBackendResolverFailureSurfacesAsRunFailed: a stub that
// errors must abort the run before the backend sees it; the run-end
// status is failed.
func TestClusterBackendResolverFailureSurfacesAsRunFailed(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: inline
        params:
          - {name: name, value: "missing"}
`))
	if err != nil {
		t.Fatal(err)
	}
	reg := refresolver.NewRegistry()
	reg.Register(refresolver.NewInlineResolver()) // empty -> ErrInlineNoData

	rec := &recordingPipelineBackend{}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(rec, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, _ := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if res.Status == "succeeded" {
		t.Errorf("status = succeeded, want failed")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.received.Spec.Tasks) != 0 {
		t.Errorf("backend received submitted pipeline with %d tasks; should not have been submitted at all", len(rec.received.Spec.Tasks))
	}
}

// TestClusterBackendResolverParamsSubstitutionFromPipelineParams: a
// resolver.params value that references $(params.X) must be substituted
// against the run's params before the resolver is called. The cluster
// path inlines the result.
func TestClusterBackendResolverParamsSubstitutionFromPipelineParams(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  params:
    - {name: which, default: "build-task"}
  tasks:
    - name: build
      taskRef:
        resolver: inline
        params:
          - {name: name, value: "$(params.which)"}
`))
	if err != nil {
		t.Fatal(err)
	}
	reg := refresolver.NewRegistry()
	inline := refresolver.NewInlineResolver()
	inline.Add("build-task", []byte(`apiVersion: tekton.dev/v1
kind: Task
metadata: {name: build}
spec:
  steps:
    - {name: s, image: alpine, script: 'true'}
`))
	reg.Register(inline)

	rec := &recordingPipelineBackend{}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(rec, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{
		Bundle: b, Name: "p",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s", res.Status)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.received.Spec.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(rec.received.Spec.Tasks))
	}
	pt := rec.received.Spec.Tasks[0]
	if pt.TaskSpec == nil || len(pt.TaskSpec.Steps) == 0 {
		t.Errorf("substitution failed: expected inlined TaskSpec, got %+v", pt)
	}
}
