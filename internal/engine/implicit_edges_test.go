package engine_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// orderedBackend records the wall-clock order in which RunTask is
// invoked so tests can assert one task ran strictly before another.
type orderedBackend struct {
	mu       sync.Mutex
	order    []string
	scripted map[string]backend.TaskResult
}

func (b *orderedBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (b *orderedBackend) Cleanup(_ context.Context) error                    { return nil }
func (b *orderedBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	b.mu.Lock()
	b.order = append(b.order, inv.TaskName)
	b.mu.Unlock()
	if r, ok := b.scripted[inv.TaskName]; ok {
		return r, nil
	}
	return backend.TaskResult{Status: backend.TaskSucceeded, Results: map[string]string{}}, nil
}

// TestImplicitEdgeFromParamResultRef: a task whose params reference
// $(tasks.X.results.Y) must run strictly after X, even with no explicit
// runAfter. Mirrors upstream Tekton's controller behavior.
func TestImplicitEdgeFromParamResultRef(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params:
    - {name: rev, default: x}
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      params:
        - {name: rev, value: "$(tasks.checkout.results.commit)"}
    - name: checkout
      taskRef: {name: t}
`))
	if err != nil {
		t.Fatal(err)
	}
	fb := &orderedBackend{
		scripted: map[string]backend.TaskResult{
			"checkout": {Status: backend.TaskSucceeded, Results: map[string]string{"commit": "abc"}},
		},
	}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s", res.Status)
	}
	if len(fb.order) != 2 {
		t.Fatalf("order = %v, want 2 items", fb.order)
	}
	if fb.order[0] != "checkout" || fb.order[1] != "build" {
		t.Errorf("order = %v, want [checkout build]", fb.order)
	}
}

// TestImplicitEdgeIsAdditive: an explicit runAfter and an implicit
// result reference together produce the union — both predecessors are
// honored and the implicit edge is not double-counted with the explicit.
func TestImplicitEdgeIsAdditive(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params:
    - {name: x, default: ""}
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
    - {name: b, taskRef: {name: t}}
    - name: c
      taskRef: {name: t}
      runAfter: [a]
      params:
        - {name: x, value: "$(tasks.b.results.x)"}
`))
	if err != nil {
		t.Fatal(err)
	}
	fb := &orderedBackend{
		scripted: map[string]backend.TaskResult{
			"a": {Status: backend.TaskSucceeded, Results: map[string]string{}},
			"b": {Status: backend.TaskSucceeded, Results: map[string]string{"x": "ok"}},
		},
	}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s", res.Status)
	}
	// c must come after both a and b.
	posA, posB, posC := -1, -1, -1
	for i, n := range fb.order {
		switch n {
		case "a":
			posA = i
		case "b":
			posB = i
		case "c":
			posC = i
		}
	}
	if posA < 0 || posB < 0 || posC < 0 {
		t.Fatalf("missing tasks in order %v", fb.order)
	}
	if posC < posA || posC < posB {
		t.Errorf("c (%d) ran before a (%d) or b (%d): order = %v", posC, posA, posB, fb.order)
	}
}

// TestImplicitEdgeFromArrayAndObjectParams: the walker must scan array
// AND object param shapes too. Mirrors how internal/resolver substitution
// already iterates each shape.
func TestImplicitEdgeFromArrayAndObjectParams(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params:
    - {name: arr, type: array, default: []}
    - {name: obj, type: object, default: {}, properties: {k: {type: string}}}
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
    - name: b
      taskRef: {name: t}
      params:
        - {name: arr, value: ["one", "$(tasks.a.results.r)"]}
    - name: c
      taskRef: {name: t}
      params:
        - {name: obj, value: {k: "$(tasks.a.results.r)"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	fb := &orderedBackend{
		scripted: map[string]backend.TaskResult{
			"a": {Status: backend.TaskSucceeded, Results: map[string]string{"r": "v"}},
		},
	}
	rep := reporter.NewJSON(&strings.Builder{})
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s, tasks = %+v", res.Status, res.Tasks)
	}
	posA := -1
	posB := -1
	posC := -1
	for i, n := range fb.order {
		switch n {
		case "a":
			posA = i
		case "b":
			posB = i
		case "c":
			posC = i
		}
	}
	if posA < 0 || posB < 0 || posC < 0 {
		t.Fatalf("missing tasks in order %v", fb.order)
	}
	if posB < posA {
		t.Errorf("b (%d) before a (%d) — array param walker missed implicit edge: order=%v", posB, posA, fb.order)
	}
	if posC < posA {
		t.Errorf("c (%d) before a (%d) — object param walker missed implicit edge: order=%v", posC, posA, fb.order)
	}
}

// The resolver-params counterpart of TestImplicitEdgeFromParamResultRef
// — a task whose taskRef.resolver.params reference $(tasks.X.results.Y)
// must also be scheduled after X — lives in lazy_resolve_test.go (Task
// 5), where it can use the lazy-dispatch hook + the inline stub
// resolver to actually run the resolved task. Task 4 only pins the
// pt.Params walker; Task 5 extends the same walker to resolver.params.
