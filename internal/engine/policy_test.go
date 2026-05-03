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
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// recBackend records every TaskInvocation and returns scripted outcomes by
// (call-count) so tests can simulate "fail twice, succeed third time" retry
// scenarios.
type recBackend struct {
	mu    sync.Mutex
	calls []string
	// outcomes[task] is consumed in order; each call to RunTask for that task
	// returns the next outcome. Empty -> always succeed.
	outcomes map[string][]backend.TaskStatus
	// hangFor, if set, makes RunTask block for hangFor before returning. Used
	// to test task timeouts.
	hangFor map[string]time.Duration
}

func (r *recBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (r *recBackend) Cleanup(_ context.Context) error                    { return nil }
func (r *recBackend) RunTask(ctx context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, inv.TaskName)
	idx := 0
	for _, c := range r.calls {
		if c == inv.TaskName {
			idx++
		}
	}
	idx--
	r.mu.Unlock()

	if d, ok := r.hangFor[inv.TaskName]; ok {
		select {
		case <-time.After(d):
			// fall through with success
		case <-ctx.Done():
			return backend.TaskResult{Status: backend.TaskInfraFailed, Err: ctx.Err()}, nil
		}
	}

	if list := r.outcomes[inv.TaskName]; idx < len(list) {
		return backend.TaskResult{Status: list[idx]}, nil
	}
	return backend.TaskResult{Status: backend.TaskSucceeded}, nil
}

type sliceSink struct {
	mu     sync.Mutex
	events []reporter.Event
}

func (s *sliceSink) Emit(e reporter.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}
func (s *sliceSink) Close() error { return nil }

func TestEngineRetriesUntilSuccess(t *testing.T) {
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
    - {name: a, taskRef: {name: t}, retries: 3}
`))
	if err != nil {
		t.Fatal(err)
	}
	fb := &recBackend{outcomes: map[string][]backend.TaskStatus{"a": {backend.TaskFailed, backend.TaskFailed, backend.TaskSucceeded}}}
	sink := &sliceSink{}
	e := engine.New(fb, sink, engine.Options{MaxParallel: 1})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s", res.Status)
	}
	fb.mu.Lock()
	gotCalls := len(fb.calls)
	fb.mu.Unlock()
	if gotCalls != 3 {
		t.Errorf("calls = %d, want 3", gotCalls)
	}
	// One task-start, two task-retry, one task-end.
	starts, retries, ends := 0, 0, 0
	var endEvent reporter.Event
	for _, e := range sink.events {
		switch e.Kind {
		case reporter.EvtTaskStart:
			if e.Task == "a" {
				starts++
			}
		case reporter.EvtTaskRetry:
			if e.Task == "a" {
				retries++
			}
		case reporter.EvtTaskEnd:
			if e.Task == "a" {
				ends++
				endEvent = e
			}
		}
	}
	if starts != 1 || retries != 2 || ends != 1 {
		t.Errorf("starts=%d retries=%d ends=%d, want 1/2/1", starts, retries, ends)
	}
	if endEvent.Status != "succeeded" {
		t.Errorf("end status = %s", endEvent.Status)
	}
	if endEvent.Attempt != 3 {
		t.Errorf("end attempt = %d, want 3", endEvent.Attempt)
	}
}

func TestEngineRetriesAllFail(t *testing.T) {
	b, _ := loader.LoadBytes([]byte(`
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
    - {name: a, taskRef: {name: t}, retries: 2}
`))
	fb := &recBackend{outcomes: map[string][]backend.TaskStatus{"a": {backend.TaskFailed, backend.TaskFailed, backend.TaskFailed}}}
	sink := &sliceSink{}
	e := engine.New(fb, sink, engine.Options{MaxParallel: 1})
	res, _ := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if res.Status != "failed" {
		t.Errorf("status = %s, want failed", res.Status)
	}
	if got := len(fb.calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestEngineTaskTimeout(t *testing.T) {
	b, _ := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  timeout: 50ms
  steps: [{name: s, image: alpine, script: 'sleep 5'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	fb := &recBackend{hangFor: map[string]time.Duration{"a": 500 * time.Millisecond}}
	sink := &sliceSink{}
	e := engine.New(fb, sink, engine.Options{MaxParallel: 1})
	res, _ := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if res.Status != "timeout" {
		t.Errorf("status = %s, want timeout", res.Status)
	}
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtTaskEnd && ev.Task == "a" {
			if ev.Status != "timeout" {
				t.Errorf("task-end status = %s, want timeout", ev.Status)
			}
			if !strings.Contains(ev.Message, "timeout") {
				t.Errorf("task-end message = %q, want contains 'timeout'", ev.Message)
			}
			return
		}
	}
	t.Error("no task-end event for 'a'")
}

func TestEngineTimeoutNotRetried(t *testing.T) {
	b, _ := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  timeout: 30ms
  steps: [{name: s, image: alpine, script: 'sleep 5'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}, retries: 5}
`))
	fb := &recBackend{hangFor: map[string]time.Duration{"a": 500 * time.Millisecond}}
	sink := &sliceSink{}
	e := engine.New(fb, sink, engine.Options{MaxParallel: 1})
	_, _ = e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if got := len(fb.calls); got != 1 {
		t.Errorf("calls = %d, want 1 (timeouts are not retried)", got)
	}
}

