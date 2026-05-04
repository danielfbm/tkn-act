// Package backend defines the contract between the engine and an execution
// substrate (Docker, k3d, ...).
package backend

import (
	"context"
	"time"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// Backend executes Tasks. The engine drives one Backend per run.
type Backend interface {
	// Prepare runs once before any task. Pulls images, creates networks, etc.
	Prepare(ctx context.Context, run RunSpec) error

	// RunTask executes a single TaskInvocation and returns its result.
	// Implementations stream logs through inv.LogSink.
	RunTask(ctx context.Context, inv TaskInvocation) (TaskResult, error)

	// Cleanup runs once after all tasks (success or failure). Removes containers,
	// temporary networks, etc. Workspace cleanup is the engine's responsibility.
	Cleanup(ctx context.Context) error
}

// RunSpec describes the whole run that's about to start.
type RunSpec struct {
	RunID      string            // stable per-invocation
	Pipeline   string            // pipeline name (or task name in single-task mode)
	Images     []string          // images to pre-pull
	Workspaces map[string]string // workspace name → host path
}

// TaskInvocation is one PipelineTask, fully resolved (params substituted).
type TaskInvocation struct {
	RunID       string
	PipelineRun string
	TaskName    string                            // pipeline-task name (e.g. "build")
	TaskRunName string                            // <pipelineRun>-<task>
	Task        tektontypes.TaskSpec              // resolved spec — no $(params.x) left
	Params      map[string]tektontypes.ParamValue // already-evaluated params
	Workspaces  map[string]WorkspaceMount         // task-workspace-name → host mount
	ContextVars map[string]string                 // $(context.taskRun.name) etc.
	LogSink     LogSink                           // engine-supplied sink
	ResultsHost string                            // host dir bind-mounted as /tekton/results
	// VolumeHosts maps each Task-level Volume name to its materialised host
	// path (emptyDir tmpdir, hostPath, or a configMap/secret projection).
	// Step.VolumeMounts entries reference these names.
	VolumeHosts map[string]string
}

type WorkspaceMount struct {
	HostPath string
	ReadOnly bool
	SubPath  string
}

// LogSink receives streamed step output. Implementations live in the reporter.
//
// stepDisplayName is the Step's displayName (Tekton v1) — the renderer and
// the JSON event stream prefer this over stepName when non-empty. Empty
// string is the no-displayName state; consumers must fall back to stepName.
type LogSink interface {
	StepLog(taskName, stepName, stepDisplayName, stream, line string)
}

// TaskResult is what RunTask returns.
type TaskResult struct {
	Status  TaskStatus
	Started time.Time
	Ended   time.Time
	Steps   []StepResult
	Results map[string]string // result name → value (read from /tekton/results/<name>)
	Err     error             // populated when Status is TaskInfraFailed
}

type StepResult struct {
	Name     string
	Status   StepStatus
	ExitCode int
	Started  time.Time
	Ended    time.Time
}

type TaskStatus string

const (
	TaskSucceeded   TaskStatus = "succeeded"
	TaskFailed      TaskStatus = "failed"      // a step exited non-zero
	TaskInfraFailed TaskStatus = "infrafailed" // backend/env error before/around step
	TaskNotRun      TaskStatus = "not-run"     // skipped because a dep failed
	TaskSkipped     TaskStatus = "skipped"     // when expression false
	TaskTimeout     TaskStatus = "timeout"     // task wall-clock timeout exceeded
)

type StepStatus string

const (
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

// PipelineBackend is an optional interface a Backend may implement when it
// wants to execute a whole PipelineRun itself (rather than have the engine
// orchestrate one Task at a time). The cluster backend uses this so the real
// Tekton controller drives the DAG.
type PipelineBackend interface {
	Backend
	RunPipeline(ctx context.Context, in PipelineRunInvocation) (PipelineRunResult, error)
}

// PipelineRunInvocation is what the engine passes when delegating an entire
// PipelineRun.
type PipelineRunInvocation struct {
	RunID           string
	PipelineRunName string
	Pipeline        tektontypes.Pipeline
	Tasks           map[string]tektontypes.Task
	Params          []tektontypes.Param
	Workspaces      map[string]WorkspaceMount
	LogSink         LogSink
}

// PipelineRunResult is what RunPipeline returns.
type PipelineRunResult struct {
	Status  string // succeeded | failed | timeout
	Tasks   map[string]TaskOutcomeOnCluster
	Started time.Time
	Ended   time.Time
	// Reason is the Tekton condition reason verbatim (e.g.
	// "PipelineRunTimeout", "PipelineValidationFailed", "Failed").
	// Surfaced for diagnostic logging — not part of the user-visible
	// status enum, which is normalized in Status. Empty for backends
	// that don't expose a condition reason.
	Reason string
	// Message is the Tekton condition message verbatim. Same purpose
	// as Reason — surfaced so failure logs can attribute a misclassified
	// run to a specific Tekton path.
	Message string
	// Results holds the resolved Pipeline.spec.results values the
	// backend extracted from the controller (cluster) or computed
	// locally (docker — populated by the engine, not by the docker
	// backend itself). Same value shape as RunResult.Results.
	Results map[string]any
}

// TaskOutcomeOnCluster is the per-task summary the cluster backend hands
// back. Attempts counts every attempt the controller made for this task
// (1 if no retries); RetryAttempts is the list of failed attempts that
// preceded the final outcome — one entry per retry, in order. The engine
// uses these to emit task-retry / task-end events shape-equivalent to the
// docker side.
type TaskOutcomeOnCluster struct {
	Status        string
	Message       string
	Results       map[string]string
	Attempts      int
	RetryAttempts []RetryAttempt
}

type RetryAttempt struct {
	Attempt int    // 1-based; the attempt that just failed
	Status  string // failed | infrafailed
	Message string
	Time    time.Time
}
