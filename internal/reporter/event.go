// Package reporter formats engine events for the user. Two sinks are provided:
// pretty (human, default) and json (one event per line, for scripting).
package reporter

import (
	"time"
)

type EventKind string

const (
	EvtRunStart  EventKind = "run-start"
	EvtRunEnd    EventKind = "run-end"
	EvtTaskStart EventKind = "task-start"
	EvtTaskEnd   EventKind = "task-end"
	EvtTaskSkip  EventKind = "task-skip"
	EvtTaskRetry EventKind = "task-retry"
	EvtStepStart EventKind = "step-start"
	EvtStepEnd   EventKind = "step-end"
	EvtStepLog   EventKind = "step-log"
	EvtError     EventKind = "error"
)

// Status values that can appear on task-end. Existing values are unchanged;
// "timeout" is new in v1.2.
const (
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusInfraFailed = "infrafailed"
	StatusSkipped     = "skipped"
	StatusNotRun      = "not-run"
	StatusTimeout     = "timeout"
)

type Event struct {
	Kind     EventKind     `json:"kind"`
	Time     time.Time     `json:"time"`
	RunID    string        `json:"runId,omitempty"`
	Pipeline string        `json:"pipeline,omitempty"`
	Task     string        `json:"task,omitempty"`
	Step     string        `json:"step,omitempty"`
	Stream   string        `json:"stream,omitempty"` // stdout|stderr
	Line     string        `json:"line,omitempty"`
	Status   string        `json:"status,omitempty"`
	ExitCode int           `json:"exitCode,omitempty"`
	Duration time.Duration `json:"durationMs,omitempty"`
	Message  string        `json:"message,omitempty"`
	// Attempt is 1-based and present on task-retry (the attempt that just
	// failed) and on task-end (the attempt that produced the final outcome).
	Attempt int `json:"attempt,omitempty"`
	// Results holds resolved Pipeline.spec.results on a run-end event.
	// Map values are one of: string, []string, map[string]string. Empty
	// or nil when the pipeline declared no results.
	Results map[string]any `json:"results,omitempty"`
	// DisplayName is the human-readable label for the entity this event
	// describes. Carried on:
	//   - run-start, run-end:    Pipeline.spec.displayName
	//   - task-start/end/skip/retry: PipelineTask.displayName
	//   - step-log:              Step.displayName
	// Empty when the source YAML didn't set a displayName. Agents
	// should fall back to the corresponding `pipeline` / `task` / `step`
	// name field.
	//
	// Note: step-start / step-end are defined as event kinds but are
	// not emitted by production code today (v1.5). If they are added
	// later, they will carry DisplayName the same way step-log does.
	DisplayName string `json:"display_name,omitempty"`

	// Description carries:
	//   - run-start: Pipeline.spec.description
	//   - task-start: TaskSpec.description (the resolved Task)
	// Omitted from terminal events to keep line size down — the start
	// event already carried it. Also omitted from step-log to avoid
	// ballooning every line of streamed output.
	Description string `json:"description,omitempty"`
}

// Reporter consumes events.
type Reporter interface {
	Emit(e Event)
	Close() error
}

// LogSink is the engine-facing log forwarder; reporters expose this so the
// backend interface stays decoupled from the reporter type.
type LogSink struct {
	r Reporter
}

func NewLogSink(r Reporter) *LogSink { return &LogSink{r: r} }

func (s *LogSink) StepLog(taskName, stepName, stepDisplayName, stream, line string) {
	s.r.Emit(Event{
		Kind:        EvtStepLog,
		Time:        time.Now(),
		Task:        taskName,
		Step:        stepName,
		DisplayName: stepDisplayName,
		Stream:      stream,
		Line:        line,
	})
}

// Tee writes each event to all underlying reporters.
type Tee struct{ rs []Reporter }

func NewTee(rs ...Reporter) *Tee { return &Tee{rs: rs} }
func (t *Tee) Emit(e Event) {
	for _, r := range t.rs {
		r.Emit(e)
	}
}
func (t *Tee) Close() error {
	var first error
	for _, r := range t.rs {
		if err := r.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

