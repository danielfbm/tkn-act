package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// This file holds the substitution-context plumbing runOne needs beyond
// params/results: $(context.*) vars, $(tasks.status) / $(tasks.<t>.status)
// for finally tasks, and the retry attempt number. The per-dispatch
// values (finally statuses, retry count) travel on context.Context so the
// scheduling loops only need a one-line wrap at the call site, whatever
// shape those loops take.

type finallyStatusKey struct{}
type retryCountKey struct{}

// finallyStatus is the $(tasks.status) / $(tasks.<t>.status) view of the
// main DAG, computed once after every main task is terminal.
type finallyStatus struct {
	aggregate string
	perTask   map[string]string
}

// Tekton's execution-status vocabulary for $(tasks[.<t>].status).
const (
	execSucceeded = "Succeeded"
	execFailed    = "Failed"
	execCompleted = "Completed"
	execNone      = "None"
)

// withFinallyStatus attaches the $(tasks.status) view computed from the
// main tasks' outcomes. The finally loop calls this once, before
// dispatching any finally task; runOne reads it back via
// finallyStatusFrom. Main tasks never carry it, so $(tasks.status) in a
// main task errors (Tekton rejects it there too).
func withFinallyStatus(ctx context.Context, mainTasks []tektontypes.PipelineTask, outcomes map[string]TaskOutcome) context.Context {
	agg, per := computeFinallyStatus(mainTasks, outcomes)
	return context.WithValue(ctx, finallyStatusKey{}, &finallyStatus{aggregate: agg, perTask: per})
}

func finallyStatusFrom(ctx context.Context) *finallyStatus {
	fs, _ := ctx.Value(finallyStatusKey{}).(*finallyStatus)
	return fs
}

// computeFinallyStatus mirrors Tekton's PipelineRunFacts.GetPipelineTaskStatus:
//
//   - per pipeline task: Succeeded when every run (all matrix rows)
//     succeeded; Failed when any run failed (failed / infrafailed /
//     timeout / cancelled); None otherwise (skipped / not run).
//   - aggregate: None if any main task never reached a terminal outcome;
//     else Failed if any task failed; else Completed if any task was
//     skipped; else Succeeded.
//
// Matrix expansions are grouped under their parent PipelineTask name.
func computeFinallyStatus(mainTasks []tektontypes.PipelineTask, outcomes map[string]TaskOutcome) (string, map[string]string) {
	type agg struct{ all, ok, failed int }
	groups := map[string]*agg{}
	var order []string
	allDone := true
	for _, pt := range mainTasks {
		name := pt.Name
		if pt.MatrixInfo != nil {
			name = pt.MatrixInfo.Parent
		}
		g, ok := groups[name]
		if !ok {
			g = &agg{}
			groups[name] = g
			order = append(order, name)
		}
		g.all++
		oc, done := outcomes[pt.Name]
		if !done {
			allDone = false
			continue
		}
		switch oc.Status {
		case "succeeded":
			g.ok++
		case "failed", "infrafailed", "timeout", "cancelled":
			g.failed++
		}
	}
	per := make(map[string]string, len(groups))
	anyFailed, anySkipped := false, false
	for _, name := range order {
		g := groups[name]
		switch {
		case g.failed > 0:
			per[name] = execFailed
			anyFailed = true
		case g.ok == g.all:
			per[name] = execSucceeded
		default:
			per[name] = execNone
			anySkipped = true
		}
	}
	aggregate := execSucceeded
	switch {
	case !allDone:
		aggregate = execNone
	case anyFailed:
		aggregate = execFailed
	case anySkipped:
		aggregate = execCompleted
	}
	return aggregate, per
}

// withRetryCount records the zero-based retry count of the attempt about
// to run ($(context.task.retry-count)).
func withRetryCount(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, retryCountKey{}, n)
}

func retryCountFrom(ctx context.Context) int {
	n, _ := ctx.Value(retryCountKey{}).(int)
	return n
}

// defaultNamespace is what $(context.pipelineRun.namespace) and
// $(context.taskRun.namespace) resolve to on the docker backend (there is
// no namespace; Tekton's default is "default").
const defaultNamespace = "default"

// stableUID derives a UUID-shaped identifier from seed. Deterministic so
// every retry of one TaskRun sees the same $(context.taskRun.uid), and
// unique per run because seed includes the run ID.
func stableUID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	h[6] = (h[6] & 0x0f) | 0x50 // version 5-style marker
	h[8] = (h[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// taskContextVars builds the $(context.*) map for one PipelineTask
// dispatch (keys without the "context." prefix).
func taskContextVars(ctx context.Context, pl tektontypes.Pipeline, pt tektontypes.PipelineTask, runID, pipelineRunName string) map[string]string {
	taskRunName := pipelineRunName + "-" + pt.Name
	// Tekton: context.task.name is the referenced Task's name; for an
	// embedded taskSpec it is the TaskRun's own name.
	taskName := taskRunName
	if pt.TaskRef != nil && pt.TaskRef.Name != "" {
		taskName = pt.TaskRef.Name
	}
	return map[string]string{
		"pipelineRun.name":      pipelineRunName,
		"pipelineRun.namespace": defaultNamespace,
		"pipelineRun.uid":       stableUID("pipelinerun/" + runID),
		"pipeline.name":         pl.Metadata.Name,
		"taskRun.name":          taskRunName,
		"taskRun.namespace":     defaultNamespace,
		"taskRun.uid":           stableUID("taskrun/" + runID + "/" + taskRunName),
		"task.name":             taskName,
		"task.retry-count":      strconv.Itoa(retryCountFrom(ctx)),
		"pipelineTask.retries":  strconv.Itoa(pt.Retries),
	}
}

// applyFinallyStatus copies the finally-status view (if any) onto rctx.
func applyFinallyStatus(ctx context.Context, rctx *resolver.Context) {
	if fs := finallyStatusFrom(ctx); fs != nil {
		rctx.TasksStatus = fs.aggregate
		rctx.TaskStatuses = fs.perTask
	}
}

// boundWorkspaces reports, for every workspace the Task declares (and any
// the PipelineTask binds), whether the PipelineTask bound it — the
// $(workspaces.<name>.bound) view.
func boundWorkspaces(spec tektontypes.TaskSpec, pt tektontypes.PipelineTask) map[string]bool {
	out := map[string]bool{}
	for _, w := range spec.Workspaces {
		out[w.Name] = false
	}
	for _, w := range pt.Workspaces {
		out[w.Name] = true
	}
	return out
}

// bindTaskParamValue substitutes one PipelineTask param value against
// the pipeline-scope resolver context. A string value that is exactly one
// whole-array reference ($(params.a[*]) / $(tasks.t.results.r[*])) binds
// as an array param, and `[*]` items inside an array value are spliced
// in place — Tekton's array pass-through.
func bindTaskParamValue(v tektontypes.ParamValue, rctx resolver.Context) (tektontypes.ParamValue, error) {
	switch v.Type {
	case tektontypes.ParamTypeArray:
		out, err := resolver.SubstituteArgs(v.ArrayVal, rctx)
		if err != nil {
			return tektontypes.ParamValue{}, err
		}
		if out == nil {
			out = []string{}
		}
		return tektontypes.ParamValue{Type: tektontypes.ParamTypeArray, ArrayVal: out}, nil
	case tektontypes.ParamTypeObject:
		out := map[string]string{}
		for k, item := range v.ObjectVal {
			s, err := resolver.Substitute(item, rctx)
			if err != nil {
				return tektontypes.ParamValue{}, err
			}
			out[k] = s
		}
		return tektontypes.ParamValue{Type: tektontypes.ParamTypeObject, ObjectVal: out}, nil
	default:
		if resolver.IsArrayStarRef(v.StringVal) {
			out, err := resolver.SubstituteArgs([]string{v.StringVal}, rctx)
			if err != nil {
				return tektontypes.ParamValue{}, err
			}
			if out == nil {
				out = []string{}
			}
			return tektontypes.ParamValue{Type: tektontypes.ParamTypeArray, ArrayVal: out}, nil
		}
		s, err := resolver.Substitute(v.StringVal, rctx)
		if err != nil {
			return tektontypes.ParamValue{}, err
		}
		return tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: s}, nil
	}
}
