package engine_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// schedBackend is a recording fake for the scheduling tests: per-task
// sleep / status / results scripting plus start and end timestamps so
// tests can assert strict ordering and concurrency.
type schedBackend struct {
	mu      sync.Mutex
	sleep   map[string]time.Duration
	status  map[string]backend.TaskStatus
	results map[string]map[string]string
	starts  map[string]time.Time
	ends    map[string]time.Time
	order   []string
}

func (b *schedBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (b *schedBackend) Cleanup(_ context.Context) error                    { return nil }
func (b *schedBackend) RunTask(ctx context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	b.mu.Lock()
	if b.starts == nil {
		b.starts = map[string]time.Time{}
		b.ends = map[string]time.Time{}
	}
	b.starts[inv.TaskName] = time.Now()
	b.order = append(b.order, inv.TaskName)
	d := b.sleep[inv.TaskName]
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.ends[inv.TaskName] = time.Now()
		b.mu.Unlock()
	}()
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return backend.TaskResult{Status: backend.TaskInfraFailed, Err: ctx.Err()}, nil
		}
	}
	st := backend.TaskSucceeded
	if s, ok := b.status[inv.TaskName]; ok {
		st = s
	}
	return backend.TaskResult{Status: st, Results: b.results[inv.TaskName]}, nil
}

func (b *schedBackend) ran(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.starts[name]
	return ok
}

// startedAfterEnd reports whether `later` started at or after `earlier` ended.
func (b *schedBackend) startedAfterEnd(t *testing.T, earlier, later string) bool {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok1 := b.ends[earlier]
	s, ok2 := b.starts[later]
	if !ok1 || !ok2 {
		t.Fatalf("missing timestamps: %s ended=%v, %s started=%v (order %v)", earlier, ok1, later, ok2, b.order)
	}
	return !s.Before(e)
}

func runSched(ctx context.Context, t *testing.T, yaml string, be backend.Backend, opts engine.Options) (engine.RunResult, *sliceSink) {
	t.Helper()
	b, err := loader.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	sink := &sliceSink{}
	res, err := engine.New(be, sink, opts).RunPipeline(ctx, engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, sink
}

func eventsFor(sink *sliceSink, task string) []reporter.Event {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []reporter.Event
	for _, ev := range sink.events {
		if ev.Task == task {
			out = append(out, ev)
		}
	}
	return out
}

func kindsOf(evs []reporter.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, string(e.Kind))
	}
	return out
}

func countKind(evs []reporter.Event, k reporter.EventKind) int {
	n := 0
	for _, e := range evs {
		if e.Kind == k {
			n++
		}
	}
	return n
}

const schedTask = `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: x, default: ""}]
  results: [{name: out}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
`

// C2.1: a when-skipped task does not block runAfter dependents
// (Tekton: "ordering-dependent Tasks will be executed"). A task that
// consumes a result of the skipped task is skipped ("Results were
// missing") and that skip DOES cascade to its own runAfter dependents.
func TestWhenSkippedDoesNotBlockRunAfterDependents(t *testing.T) {
	be := &schedBackend{}
	res, sink := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: t}
      when: [{input: "no", operator: in, values: ["yes"]}]
    - {name: b, taskRef: {name: t}, runAfter: [a]}
    - {name: c, taskRef: {name: t}, runAfter: [b]}
    - name: d
      taskRef: {name: t}
      params: [{name: x, value: $(tasks.a.results.out)}]
    - {name: e, taskRef: {name: t}, runAfter: [d]}
`, be, engine.Options{})
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded; tasks=%+v", res.Status, res.Tasks)
	}
	if be.ran("a") {
		t.Errorf("a should be when-skipped")
	}
	for _, n := range []string{"b", "c"} {
		if !be.ran(n) {
			t.Errorf("%s (runAfter chain of when-skipped a) did not run; tasks=%+v", n, res.Tasks)
		}
	}
	if got := res.Tasks["a"].Status; got != "skipped" {
		t.Errorf("a status = %q, want skipped", got)
	}
	for _, n := range []string{"d", "e"} {
		if be.ran(n) {
			t.Errorf("%s should have been skipped", n)
		}
		if got := res.Tasks[n].Status; got != "not-run" {
			t.Errorf("%s status = %q, want not-run", n, got)
		}
		if evs := eventsFor(sink, n); countKind(evs, reporter.EvtTaskSkip) != 1 {
			t.Errorf("%s events = %v, want one task-skip", n, kindsOf(evs))
		}
	}
	if msg := res.Tasks["d"].Message; !strings.Contains(msg, "Results were missing") {
		t.Errorf("d message = %q, want Results were missing", msg)
	}
}

// C2.1: a matrix whose every row is when-skipped does not block runAfter
// dependents of the matrix parent.
func TestAllRowsSkippedMatrixDoesNotBlockRunAfter(t *testing.T) {
	be := &schedBackend{}
	res, _ := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      when: [{input: "no", operator: in, values: ["yes"]}]
      matrix:
        params: [{name: x, value: [a, b]}]
    - {name: publish, taskRef: {name: t}, runAfter: [build]}
`, be, engine.Options{})
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", res.Status)
	}
	if !be.ran("publish") {
		t.Errorf("publish did not run; tasks=%+v", res.Tasks)
	}
	for _, n := range []string{"build-0", "build-1"} {
		if got := res.Tasks[n].Status; got != "skipped" {
			t.Errorf("%s status = %q, want skipped", n, got)
		}
	}
}

// Regression guard: a *failed* upstream still blocks runAfter dependents.
func TestFailedUpstreamStillBlocksRunAfter(t *testing.T) {
	be := &schedBackend{status: map[string]backend.TaskStatus{"a": backend.TaskFailed}}
	res, _ := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
    - {name: b, taskRef: {name: t}, runAfter: [a]}
`, be, engine.Options{})
	if res.Status != "failed" || be.ran("b") || res.Tasks["b"].Status != "not-run" {
		t.Errorf("status=%q ran(b)=%v b=%+v; want failed / false / not-run", res.Status, be.ran("b"), res.Tasks["b"])
	}
	if res.Tasks["b"].Message != "upstream failure" {
		t.Errorf("b message = %q, want upstream failure", res.Tasks["b"].Message)
	}
}

// C2.2: a `when` input referencing an upstream result adds an implicit
// DAG edge.
func TestImplicitEdgeFromWhenInput(t *testing.T) {
	be := &schedBackend{
		sleep:   map[string]time.Duration{"a": 50 * time.Millisecond},
		results: map[string]map[string]string{"a": {"out": "yes"}},
	}
	res, _ := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
    - name: b
      taskRef: {name: t}
      when: [{input: $(tasks.a.results.out), operator: in, values: ["yes"]}]
    - name: c
      taskRef: {name: t}
      when: [{input: "yes", operator: in, values: ["$(tasks.a.results.out)"]}]
`, be, engine.Options{})
	if res.Status != "succeeded" {
		t.Fatalf("status = %q, tasks=%+v", res.Status, res.Tasks)
	}
	for _, n := range []string{"b", "c"} {
		if !be.startedAfterEnd(t, "a", n) {
			t.Errorf("%s started before a ended (order %v)", n, be.order)
		}
	}
}

// C2.2: `[*]` / `[N]` suffixed result refs add implicit edges.
func TestImplicitEdgeFromIndexedResultRef(t *testing.T) {
	be := &schedBackend{sleep: map[string]time.Duration{"a": 50 * time.Millisecond}}
	res, sink := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
    - name: b
      taskRef: {name: t}
      params: [{name: x, value: "$(tasks.a.results.out[*])"}]
    - name: c
      taskRef: {name: t}
      params: [{name: x, value: "$(tasks.a.results.out[0])"}]
`, be, engine.Options{})
	// Substitution of indexed refs is WS-C1's concern; this test only
	// asserts scheduling, via the task-start / task-end event times.
	aEnd := eventTime(t, sink, "a", reporter.EvtTaskEnd)
	for _, n := range []string{"b", "c"} {
		if s := eventTime(t, sink, n, reporter.EvtTaskStart); s.Before(aEnd) {
			t.Errorf("%s task-start (%v) before a task-end (%v); tasks=%+v", n, s, aEnd, res.Tasks)
		}
	}
}

func eventTime(t *testing.T, sink *sliceSink, task string, k reporter.EventKind) time.Time {
	t.Helper()
	for _, ev := range eventsFor(sink, task) {
		if ev.Kind == k {
			return ev.Time
		}
	}
	t.Fatalf("no %s event for %s", k, task)
	return time.Time{}
}

// C2.2: a result ref naming a matrix parent waits on every expansion.
func TestImplicitEdgeFromMatrixParentResultRef(t *testing.T) {
	be := &schedBackend{
		sleep: map[string]time.Duration{"build-0": 30 * time.Millisecond, "build-1": 60 * time.Millisecond},
		results: map[string]map[string]string{
			"build-0": {"out": "a"}, "build-1": {"out": "b"},
		},
	}
	res, sink := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params: [{name: x, value: [a, b]}]
    - name: consume
      taskRef: {name: t}
      params: [{name: x, value: "$(tasks.build.results.out[*])"}]
`, be, engine.Options{})
	// `[*]` substitution is WS-C1's concern; assert scheduling only.
	start := eventTime(t, sink, "consume", reporter.EvtTaskStart)
	for _, n := range []string{"build-0", "build-1"} {
		if end := eventTime(t, sink, n, reporter.EvtTaskEnd); start.Before(end) {
			t.Errorf("consume started before %s ended; tasks=%+v", n, res.Tasks)
		}
	}
}

// C2.3: when timeouts.tasks fires, the in-flight task is reported
// `timeout` and every not-yet-started task gets a task-skip and a
// not-run entry in RunResult.Tasks.
func TestTasksTimeoutMarksRemainingLevels(t *testing.T) {
	be := &schedBackend{sleep: map[string]time.Duration{"a": time.Minute}}
	res, sink := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {tasks: 200ms}
  tasks:
    - {name: a, taskRef: {name: t}}
    - {name: b, taskRef: {name: t}, runAfter: [a]}
    - {name: c, taskRef: {name: t}, runAfter: [b]}
`, be, engine.Options{})
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout", res.Status)
	}
	if got := res.Tasks["a"].Status; got != "timeout" {
		t.Errorf("a status = %q, want timeout", got)
	}
	var aEnd *reporter.Event
	for _, ev := range eventsFor(sink, "a") {
		if ev.Kind == reporter.EvtTaskEnd {
			ev := ev
			aEnd = &ev
		}
	}
	if aEnd == nil || aEnd.Status != "timeout" || !strings.Contains(aEnd.Message, "tasks timeout") {
		t.Errorf("a task-end = %+v, want status timeout + tasks-timeout message", aEnd)
	}
	for _, n := range []string{"b", "c"} {
		oc, ok := res.Tasks[n]
		if !ok || oc.Status != "not-run" {
			t.Errorf("%s outcome = %+v (present=%v), want not-run", n, oc, ok)
		}
		if evs := eventsFor(sink, n); countKind(evs, reporter.EvtTaskSkip) != 1 {
			t.Errorf("%s events = %v, want exactly one task-skip", n, kindsOf(evs))
		}
	}
}

// C2.3: root-context cancellation marks remaining levels too.
func TestCancellationMarksRemainingLevels(t *testing.T) {
	be := &schedBackend{sleep: map[string]time.Duration{"a": time.Minute}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for !be.ran("a") {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	res, sink := runSched(ctx, t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
    - {name: b, taskRef: {name: t}, runAfter: [a]}
  finally:
    - {name: f, taskRef: {name: t}}
`, be, engine.Options{})
	if res.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", res.Status)
	}
	for _, n := range []string{"b", "f"} {
		oc, ok := res.Tasks[n]
		if !ok || oc.Status != "not-run" || !strings.Contains(oc.Message, "cancelled") {
			t.Errorf("%s outcome = %+v (present=%v), want not-run / cancelled message", n, oc, ok)
		}
		if evs := eventsFor(sink, n); countKind(evs, reporter.EvtTaskSkip) != 1 {
			t.Errorf("%s events = %v, want exactly one task-skip", n, kindsOf(evs))
		}
	}
}

// C2.4: finally tasks run concurrently (bounded by MaxParallel), so two
// 300ms finally tasks fit in a 500ms finally budget.
func TestFinallyTasksRunConcurrently(t *testing.T) {
	be := &schedBackend{sleep: map[string]time.Duration{"f1": 300 * time.Millisecond, "f2": 300 * time.Millisecond}}
	res, _ := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {finally: 500ms}
  tasks:
    - {name: a, taskRef: {name: t}}
  finally:
    - {name: f1, taskRef: {name: t}}
    - {name: f2, taskRef: {name: t}}
`, be, engine.Options{MaxParallel: 4})
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded; tasks=%+v", res.Status, res.Tasks)
	}
}

// C2.4: MaxParallel=1 still serialises finally tasks.
func TestFinallyRespectsMaxParallel(t *testing.T) {
	be := &schedBackend{sleep: map[string]time.Duration{"f1": 50 * time.Millisecond, "f2": 50 * time.Millisecond}}
	res, _ := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
  finally:
    - {name: f1, taskRef: {name: t}}
    - {name: f2, taskRef: {name: t}}
`, be, engine.Options{MaxParallel: 1})
	if res.Status != "succeeded" {
		t.Fatalf("status = %q", res.Status)
	}
	first, second := be.order[1], be.order[2]
	if !be.startedAfterEnd(t, first, second) {
		t.Errorf("with MaxParallel=1, %s started before %s ended", second, first)
	}
}

// C2.4: a finally task consuming a result of a failed task is skipped
// ("Results were missing"), not failed.
func TestFinallyMissingResultIsSkipped(t *testing.T) {
	be := &schedBackend{status: map[string]backend.TaskStatus{"a": backend.TaskFailed}}
	res, sink := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
  finally:
    - name: f
      taskRef: {name: t}
      params: [{name: x, value: $(tasks.a.results.out)}]
    - {name: g, taskRef: {name: t}}
`, be, engine.Options{})
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed", res.Status)
	}
	if be.ran("f") {
		t.Errorf("f should not run")
	}
	if oc := res.Tasks["f"]; oc.Status != "not-run" || !strings.Contains(oc.Message, "Results were missing") {
		t.Errorf("f outcome = %+v, want not-run / Results were missing", oc)
	}
	evs := eventsFor(sink, "f")
	if countKind(evs, reporter.EvtTaskSkip) != 1 || countKind(evs, reporter.EvtTaskEnd) != 0 {
		t.Errorf("f events = %v, want one task-skip and no task-end", kindsOf(evs))
	}
	if !be.ran("g") {
		t.Errorf("g (independent finally) should still run")
	}
}

// C2.5(d): a non-matrix when-skip emits task-skip but no task-end, the
// same shape as a matrix row skip.
func TestWhenSkipEmitsNoTaskEnd(t *testing.T) {
	be := &schedBackend{}
	_, sink := runSched(context.Background(), t, schedTask+`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: t}
      when: [{input: "no", operator: in, values: ["yes"]}]
    - name: m
      taskRef: {name: t}
      when: [{input: "no", operator: in, values: ["yes"]}]
      matrix:
        params: [{name: x, value: [one]}]
`, be, engine.Options{})
	for _, n := range []string{"a", "m-0"} {
		evs := eventsFor(sink, n)
		if countKind(evs, reporter.EvtTaskSkip) != 1 || countKind(evs, reporter.EvtTaskEnd) != 0 {
			t.Errorf("%s events = %v, want exactly one task-skip and no task-end", n, kindsOf(evs))
		}
	}
}

// C2.5(c): a resolver-backed taskRef's resolved Task.spec.timeout is
// honoured.
func TestResolverBackedTaskTimeoutHonoured(t *testing.T) {
	reg := refresolver.NewRegistry()
	inline := refresolver.NewInlineResolver()
	inline.Add("slow", []byte(`apiVersion: tekton.dev/v1
kind: Task
metadata: {name: slow}
spec:
  timeout: 100ms
  steps:
    - {name: s, image: alpine, script: 'sleep 60'}
`))
	reg.Register(inline)
	be := &schedBackend{sleep: map[string]time.Duration{"a": 10 * time.Second}}
	start := time.Now()
	res, _ := runSched(context.Background(), t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef:
        resolver: inline
        params: [{name: name, value: slow}]
`, be, engine.Options{Refresolver: reg})
	if time.Since(start) > 5*time.Second {
		t.Errorf("task timeout not applied: run took %v", time.Since(start))
	}
	if res.Status != "timeout" || res.Tasks["a"].Status != "timeout" {
		t.Errorf("status = %q, a = %+v; want timeout/timeout", res.Status, res.Tasks["a"])
	}
}
