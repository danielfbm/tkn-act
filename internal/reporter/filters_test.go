package reporter_test

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/reporter"
)

type capSink struct{ events []reporter.Event }

func (c *capSink) Emit(e reporter.Event) { c.events = append(c.events, e) }
func (c *capSink) Close() error          { return nil }

// TestFilter_TaskOnly: --task=build keeps only events whose Task is
// "build"; events for other tasks are dropped.
func TestFilter_TaskOnly(t *testing.T) {
	inner := &capSink{}
	f := reporter.NewFilter(inner, []string{"build"}, nil)
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Line: "yes"})
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "deploy", Line: "no"})
	if len(inner.events) != 1 || inner.events[0].Task != "build" {
		t.Errorf("got %+v", inner.events)
	}
}

// TestFilter_AlwaysPassRunBoundary: run-start, run-end, and error
// events bypass the filter so the user always sees the run
// envelope and any out-of-band failures, even when filters are
// narrow.
func TestFilter_AlwaysPassRunBoundary(t *testing.T) {
	inner := &capSink{}
	f := reporter.NewFilter(inner, []string{"build"}, nil)
	f.Emit(reporter.Event{Kind: reporter.EvtRunStart})
	f.Emit(reporter.Event{Kind: reporter.EvtError, Message: "oops"})
	f.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "succeeded"})
	if len(inner.events) != 3 {
		t.Errorf("run-start/error/run-end must always pass; got %d events: %+v", len(inner.events), inner.events)
	}
}

// TestFilter_TaskAndStep: --task=build --step=compile keeps only
// events for that exact (task, step) pair; other steps under the
// same task are dropped.
func TestFilter_TaskAndStep(t *testing.T) {
	inner := &capSink{}
	f := reporter.NewFilter(inner, []string{"build"}, []string{"compile"})
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Step: "compile", Line: "y"})
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Step: "lint", Line: "n"})
	if len(inner.events) != 1 || inner.events[0].Step != "compile" {
		t.Errorf("got %+v", inner.events)
	}
}

// TestFilter_StepEmptyEventPasses: events that don't carry a step
// (task-start, task-end, sidecar-start, etc.) pass the step filter
// — the step filter only refuses events that DO carry a Step that's
// not on the list. Otherwise filtering by step would suppress every
// non-step-log event for the matching task.
func TestFilter_StepEmptyEventPasses(t *testing.T) {
	inner := &capSink{}
	f := reporter.NewFilter(inner, []string{"build"}, []string{"compile"})
	f.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Task: "build"})
	f.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "build", Status: "succeeded"})
	if len(inner.events) != 2 {
		t.Errorf("task-start/task-end without a Step must pass; got %+v", inner.events)
	}
}

// TestFilter_EmptyFilters_Passthrough: with both filter lists nil,
// every event reaches the inner reporter (no-op wrapping).
func TestFilter_EmptyFilters_Passthrough(t *testing.T) {
	inner := &capSink{}
	f := reporter.NewFilter(inner, nil, nil)
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Step: "compile"})
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "deploy", Step: "kubectl"})
	if len(inner.events) != 2 {
		t.Errorf("empty filters dropped events: %+v", inner.events)
	}
}

// TestFilter_DropsNonMatchingTaskEnd: task-end for a task that is
// not on the --task list must be dropped, not just step-log. This
// pins the per-task lifecycle behavior: a user narrowing to "build"
// should not see "deploy succeeded" task-end events.
func TestFilter_DropsNonMatchingTaskEnd(t *testing.T) {
	inner := &capSink{}
	f := reporter.NewFilter(inner, []string{"build"}, nil)
	f.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Task: "build"})
	f.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "deploy", Status: "succeeded"})
	f.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "build", Status: "succeeded"})
	if len(inner.events) != 2 || inner.events[1].Task != "build" {
		t.Errorf("task-end for non-matching task must be dropped; got %+v", inner.events)
	}
}

// TestFilter_StepOnly_CrossTask: --step=compile with no --task must
// pass events for that step across any task and drop non-matching
// step-events regardless of task.
func TestFilter_StepOnly_CrossTask(t *testing.T) {
	inner := &capSink{}
	f := reporter.NewFilter(inner, nil, []string{"compile"})
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Step: "compile", Line: "y"})
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Step: "lint", Line: "n"})
	f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "deploy", Step: "compile", Line: "y"})
	if len(inner.events) != 2 {
		t.Errorf("step-only filter should accept matching step across tasks; got %+v", inner.events)
	}
	for _, e := range inner.events {
		if e.Step != "compile" {
			t.Errorf("non-matching step leaked: %+v", e)
		}
	}
}

// TestFilter_TaskEmpty_EnvelopeEventsPass: an event with no Task
// (top-level resolver-start / resolver-end for pipeline-ref
// resolution is the canonical example) must pass even with a narrow
// --task filter — otherwise the user filtering to one task never
// sees the pre-task resolution envelope or its failure.
func TestFilter_TaskEmpty_EnvelopeEventsPass(t *testing.T) {
	inner := &capSink{}
	f := reporter.NewFilter(inner, []string{"build"}, nil)
	f.Emit(reporter.Event{Kind: reporter.EvtResolverStart, Resolver: "git"})
	f.Emit(reporter.Event{Kind: reporter.EvtResolverEnd, Resolver: "git", Status: "succeeded"})
	if len(inner.events) != 2 {
		t.Errorf("top-level resolver-start / resolver-end with empty Task must pass; got %+v", inner.events)
	}
}

// TestFilter_Close_DelegatesToInner: Close on the filter wrapper
// must invoke Close on the inner reporter so persistence sinks
// flush their files.
type closeSink struct {
	capSink
	closed bool
}

func (c *closeSink) Close() error { c.closed = true; return nil }

func TestFilter_Close_DelegatesToInner(t *testing.T) {
	inner := &closeSink{}
	f := reporter.NewFilter(inner, nil, nil)
	if err := f.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if !inner.closed {
		t.Errorf("inner Close not invoked")
	}
}
