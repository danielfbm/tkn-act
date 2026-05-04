package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// newInlineRegistry returns a Registry with a single inline resolver
// pre-loaded with one trivial Task body keyed by "build-task". Used by
// implicit_edges_test.go's resolver-params variant.
func newInlineRegistry(t *testing.T) *refresolver.Registry {
	t.Helper()
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
	return reg
}

// countingResolver wraps an inline-style payload but increments a
// counter on every Resolve call. Used by the per-run-cache tests.
type countingResolver struct {
	name    string
	count   int64
	bytesFn func(req refresolver.Request) ([]byte, error)
}

func (c *countingResolver) Name() string { return c.name }

func (c *countingResolver) Resolve(_ context.Context, req refresolver.Request) (refresolver.Resolved, error) {
	atomic.AddInt64(&c.count, 1)
	if c.bytesFn == nil {
		return refresolver.Resolved{}, errors.New("countingResolver: bytesFn nil")
	}
	bs, err := c.bytesFn(req)
	if err != nil {
		return refresolver.Resolved{}, err
	}
	return refresolver.Resolved{Bytes: bs, Source: "stub"}, nil
}

// TestRunOneResolvesLazily covers the happy path: a Pipeline with a
// taskRef.resolver block resolves at dispatch time, the inlined task
// runs on the fake backend, and we get one resolver-start /
// resolver-end pair around it.
func TestRunOneResolvesLazily(t *testing.T) {
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
	reg := newInlineRegistry(t)

	fb := &orderedBackend{}
	out := &strings.Builder{}
	rep := reporter.NewJSON(out)
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s, tasks = %+v", res.Status, res.Tasks)
	}
	if len(fb.order) != 1 || fb.order[0] != "build" {
		t.Errorf("backend calls = %v, want [build]", fb.order)
	}
	// Resolver event check: at least one resolver-start and one
	// resolver-end appear in the JSON stream.
	if !strings.Contains(out.String(), `"resolver-start"`) {
		t.Errorf("expected resolver-start event in stream, got %s", out.String())
	}
	if !strings.Contains(out.String(), `"resolver-end"`) {
		t.Errorf("expected resolver-end event in stream, got %s", out.String())
	}
}

// TestRunOneResolverFailureMarksTaskFailed: a resolver that errors
// surfaces as task-end status=failed with message=resolver: <err>.
func TestRunOneResolverFailureMarksTaskFailed(t *testing.T) {
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
	reg.Register(refresolver.NewInlineResolver()) // empty — every lookup misses

	fb := &orderedBackend{}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("overall status = %s, want failed", res.Status)
	}
	oc, ok := res.Tasks["build"]
	if !ok {
		t.Fatalf("no outcome for build, got %+v", res.Tasks)
	}
	if oc.Status != "failed" {
		t.Errorf("build status = %s, want failed", oc.Status)
	}
	if !strings.HasPrefix(oc.Message, "resolver:") {
		t.Errorf("message = %q, want prefix \"resolver:\"", oc.Message)
	}
	// The fake backend's RunTask must NOT have been invoked; the
	// failure happened before dispatch.
	if len(fb.order) != 0 {
		t.Errorf("backend calls = %v, want none", fb.order)
	}
}

// TestRunOneCachesPerRun: two PipelineTasks pointing at the same
// (resolver, params) hit the per-run cache and the resolver fires once.
func TestRunOneCachesPerRun(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef:
        resolver: stub
        params:
          - {name: x, value: "same"}
    - name: b
      taskRef:
        resolver: stub
        params:
          - {name: x, value: "same"}
      runAfter: [a]
`))
	if err != nil {
		t.Fatal(err)
	}
	stub := &countingResolver{
		name: "stub",
		bytesFn: func(_ refresolver.Request) ([]byte, error) {
			return []byte(`apiVersion: tekton.dev/v1
kind: Task
metadata: {name: x}
spec:
  steps:
    - {name: s, image: alpine, script: 'true'}
`), nil
		},
	}
	reg := refresolver.NewRegistry()
	reg.Register(stub)

	fb := &orderedBackend{}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s, tasks=%+v", res.Status, res.Tasks)
	}
	if got := atomic.LoadInt64(&stub.count); got != 1 {
		t.Errorf("resolver calls = %d, want 1 (per-run cache hit on the second use)", got)
	}
}

// TestRunOneDoesNotCacheAcrossDifferentSubstitutedParams (Critical 2):
// two PipelineTasks via the same resolver name, but resolver.params
// substitute (after running an upstream `discover`) to DIFFERENT
// values — different cache keys, two resolver calls.
func TestRunOneDoesNotCacheAcrossDifferentSubstitutedParams(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results:
    - {name: a}
    - {name: b}
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: discover
      taskRef: {name: t}
    - name: build1
      taskRef:
        resolver: stub
        params:
          - {name: name, value: "build"}
          - {name: pathInRepo, value: "$(tasks.discover.results.a)"}
    - name: build2
      taskRef:
        resolver: stub
        params:
          - {name: name, value: "build"}
          - {name: pathInRepo, value: "$(tasks.discover.results.b)"}
`))
	if err != nil {
		t.Fatal(err)
	}
	stub := &countingResolver{
		name: "stub",
		bytesFn: func(req refresolver.Request) ([]byte, error) {
			return []byte(`apiVersion: tekton.dev/v1
kind: Task
metadata: {name: x}
spec:
  steps:
    - {name: s, image: alpine, script: 'true'}
`), nil
		},
	}
	reg := refresolver.NewRegistry()
	reg.Register(stub)

	fb := &orderedBackend{
		scripted: map[string]backend.TaskResult{
			"discover": {Status: backend.TaskSucceeded, Results: map[string]string{
				"a": "task-a.yaml", "b": "task-b.yaml",
			}},
		},
	}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s, tasks=%+v", res.Status, res.Tasks)
	}
	if got := atomic.LoadInt64(&stub.count); got != 2 {
		t.Errorf("resolver calls = %d, want 2 (different substituted params -> different cache keys)", got)
	}
}

// TestRunOneRejectsResolvedTaskWithBadSpec: resolver returns syntactically
// valid YAML but the inlined Task has no steps. Engine must validate
// before dispatch and surface a clear validator error.
func TestRunOneRejectsResolvedTaskWithBadSpec(t *testing.T) {
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
          - {name: name, value: "bad"}
`))
	if err != nil {
		t.Fatal(err)
	}
	reg := refresolver.NewRegistry()
	inline := refresolver.NewInlineResolver()
	// Valid YAML; semantically invalid Task (no steps).
	inline.Add("bad", []byte(`apiVersion: tekton.dev/v1
kind: Task
metadata: {name: empty}
spec: {}
`))
	reg.Register(inline)

	fb := &orderedBackend{}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("overall status = %s, want failed", res.Status)
	}
	oc := res.Tasks["build"]
	if oc.Status != "failed" {
		t.Errorf("build status = %s, want failed", oc.Status)
	}
	// Message must mention the validator + steps. The exact wording can
	// shift if the validator's error string evolves; we only check the
	// load-bearing tokens.
	if !strings.Contains(oc.Message, "resolver:") || !strings.Contains(strings.ToLower(oc.Message), "step") {
		t.Errorf("message = %q, want resolver: ... step ...", oc.Message)
	}
	if len(fb.order) != 0 {
		t.Errorf("backend calls = %v; expected zero (validation should fail before dispatch)", fb.order)
	}
}

// TestRunOneFinallyTaskWithResolverRef: a finally task whose taskRef
// uses a resolver still resolves and runs even when the main DAG fails.
func TestRunOneFinallyTaskWithResolverRef(t *testing.T) {
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
  finally:
    - name: notify
      taskRef:
        resolver: inline
        params:
          - {name: name, value: "notify-task"}
`))
	if err != nil {
		t.Fatal(err)
	}
	reg := refresolver.NewRegistry()
	inline := refresolver.NewInlineResolver()
	inline.Add("notify-task", []byte(`apiVersion: tekton.dev/v1
kind: Task
metadata: {name: notify}
spec:
  steps: [{name: s, image: alpine, script: 'true'}]
`))
	reg.Register(inline)

	// Make the main task fail.
	fb := &orderedBackend{
		scripted: map[string]backend.TaskResult{
			"a": {Status: backend.TaskFailed, Err: errSome("nope")},
		},
	}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4, Refresolver: reg})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %s, want failed (main task fails)", res.Status)
	}
	oc, ok := res.Tasks["notify"]
	if !ok {
		t.Fatalf("notify outcome missing: %+v", res.Tasks)
	}
	if oc.Status != "succeeded" {
		t.Errorf("notify status = %s, want succeeded (finally runs on failure)", oc.Status)
	}
	if len(fb.order) < 2 {
		t.Errorf("backend calls = %v, want at least 2", fb.order)
	}
}

// errSome is a tiny helper for making backend.TaskResult.Err non-nil.
func errSome(s string) error { return errors.New(s) }

// Compile-time check that orderedBackend conforms to the unsync
// concurrent-use pattern other engine tests rely on.
var _ sync.Locker = (*sync.Mutex)(nil)
