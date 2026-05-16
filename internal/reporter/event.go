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
	// Sidecar events surface long-lived helper containers' lifecycle
	// on the JSON event stream. The Step field carries the sidecar
	// name (no dedicated payload field — agents that need the
	// disambiguation can branch on Kind or Stream).
	EvtSidecarStart  EventKind = "sidecar-start"
	EvtSidecarEnd    EventKind = "sidecar-end"
	EvtSidecarLog    EventKind = "sidecar-log"
	EvtError         EventKind = "error"
	EvtResolverStart EventKind = "resolver-start"
	EvtResolverEnd   EventKind = "resolver-end"
	// EvtDebug is emitted only when --debug is set. Carries Component
	// (backend | resolver | engine), Message (short human summary),
	// and Fields (component-defined key/value payload). Existing
	// fields on Event remain optional and may be empty on debug
	// events.
	EvtDebug EventKind = "debug"
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

	// Resolver fields populate resolver-start / resolver-end events
	// emitted by the engine's lazy-dispatch path. All four are tagged
	// `omitempty` so non-resolver events don't carry empty keys; this
	// matches the convention every other optional Event field uses.

	// Resolver names the resolver protocol (git | hub | http | bundles |
	// cluster | <custom-in-remote-mode>).
	Resolver string `json:"resolver,omitempty"`
	// Cached is true when resolver-end fires for a per-run cache hit
	// (no fresh fetch happened). Surfaces in pretty output as "(cached)".
	Cached bool `json:"cached,omitempty"`
	// SHA256 is the hex digest of the resolved bytes. Used by the
	// on-disk cache invalidation diagnostics; agents can compare across
	// runs to detect upstream drift.
	SHA256 string `json:"sha256,omitempty"`
	// Source is a human-readable origin string for the resolver (e.g.
	// "git: github.com/tektoncd/catalog@abc123 -> task/...").
	Source string `json:"source,omitempty"`

	// Matrix is set on task-start / task-end / task-skip events
	// emitted from a PipelineTask.matrix expansion. Nil for
	// ordinary tasks (omitempty). Carries the original PipelineTask
	// name (Parent), the 0-based row index, the total expansion
	// count, and the row's matrix-contributed params.
	Matrix *MatrixEvent `json:"matrix,omitempty"`

	// Component is the source of a debug event (backend | resolver |
	// engine). Set only on EvtDebug.
	Component string `json:"component,omitempty"`
	// Fields carries the per-debug-event payload (e.g. {"id": "abc",
	// "image": "alpine"}). Set only on EvtDebug. Encoded as a JSON
	// object; values must be JSON-serializable.
	Fields map[string]any `json:"fields,omitempty"`
}

// MatrixEvent identifies one expansion of a matrix-fanned
// PipelineTask. Parent is the original PipelineTask name; Index is
// the 0-based row index in the expansion order; Of is the total
// number of expansions of this Parent; Params holds the row's
// matrix-contributed params (string-keyed string values). The
// engine constructs this from tektontypes.MatrixInfo before
// emitting the event.
type MatrixEvent struct {
	Parent string            `json:"parent"`
	Index  int               `json:"index"`
	Of     int               `json:"of"`
	Params map[string]string `json:"params,omitempty"`
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

// Reporter exposes the wrapped reporter so callers that need to emit
// non-log events (e.g. the cluster backend's matrix-fallback warning)
// can do so without taking a separate dependency on the reporter
// type. Returns nil when the LogSink was constructed without a
// backing reporter.
func (s *LogSink) Reporter() Reporter { return s.r }

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

// SidecarLog forwards a sidecar's stdout/stderr line as an
// EvtSidecarLog event. The sidecar name lands in Event.Step (reused
// from the step contract) and stream is one of "sidecar-stdout" /
// "sidecar-stderr" — never plain "stdout" / "stderr" — so consumers
// can attribute the line to a sidecar without re-parsing.
func (s *LogSink) SidecarLog(taskName, sidecarName, stream, line string) {
	s.r.Emit(Event{
		Kind:   EvtSidecarLog,
		Time:   time.Now(),
		Task:   taskName,
		Step:   sidecarName,
		Stream: stream,
		Line:   line,
	})
}

// EmitSidecarStart fires an EvtSidecarStart for the named sidecar.
// Backends that need to surface sidecar lifecycle on the JSON event
// stream (the docker backend; the cluster backend's pod-watch loop)
// invoke this directly. Reuses Event.Step for the sidecar name and
// sets Stream = "sidecar".
func (s *LogSink) EmitSidecarStart(taskName, sidecarName string) {
	s.r.Emit(Event{
		Kind:   EvtSidecarStart,
		Time:   time.Now(),
		Task:   taskName,
		Step:   sidecarName,
		Stream: "sidecar",
	})
}

// EmitSidecarEnd fires the terminal EvtSidecarEnd for a sidecar.
// status mirrors the Task* enum (succeeded / failed / infrafailed).
func (s *LogSink) EmitSidecarEnd(taskName, sidecarName string, exitCode int, status, message string) {
	s.r.Emit(Event{
		Kind:     EvtSidecarEnd,
		Time:     time.Now(),
		Task:     taskName,
		Step:     sidecarName,
		Stream:   "sidecar",
		Status:   status,
		ExitCode: exitCode,
		Message:  message,
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

