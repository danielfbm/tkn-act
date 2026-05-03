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

func (s *LogSink) StepLog(taskName, stepName, stream, line string) {
	s.r.Emit(Event{
		Kind:   EvtStepLog,
		Time:   time.Now(),
		Task:   taskName,
		Step:   stepName,
		Stream: stream,
		Line:   line,
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

