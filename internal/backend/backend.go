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
}

type WorkspaceMount struct {
	HostPath string
	ReadOnly bool
	SubPath  string
}

// LogSink receives streamed step output. Implementations live in the reporter.
type LogSink interface {
	StepLog(taskName, stepName, stream string, line string)
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
	EmitEvent       func(taskName, status, message string, started, ended time.Time, results map[string]string)
}

// PipelineRunResult is what RunPipeline returns.
type PipelineRunResult struct {
	Status  string // succeeded | failed
	Tasks   map[string]TaskOutcomeOnCluster
	Started time.Time
	Ended   time.Time
}

type TaskOutcomeOnCluster struct {
	Status  string
	Message string
	Results map[string]string
}
