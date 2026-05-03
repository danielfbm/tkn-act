// Package engine orchestrates a Tekton PipelineRun. It builds the DAG, resolves
// params and results, evaluates when-expressions, runs `finally` tasks, and
// drives a Backend. Pure of I/O except via the Backend and the Reporter.
package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine/dag"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"golang.org/x/sync/errgroup"
)

type Options struct {
	MaxParallel int
	// VolumeResolver materialises a Task's volumes onto host paths just
	// before the task runs. The CLI sets this; tests may leave it nil
	// (volumes are then unsupported in that test).
	VolumeResolver VolumeResolver
}

// VolumeResolver is the engine's hook for the volumes package. Returns
// map[volumeName] -> hostPath.
type VolumeResolver func(taskName string, vs []tektontypes.Volume) (map[string]string, error)

type Engine struct {
	be   backend.Backend
	rep  reporter.Reporter
	opts Options
}

func New(be backend.Backend, rep reporter.Reporter, opts Options) *Engine {
	if opts.MaxParallel <= 0 {
		opts.MaxParallel = 4
	}
	return &Engine{be: be, rep: rep, opts: opts}
}

func (e *Engine) RunPipeline(ctx context.Context, in PipelineInput) (RunResult, error) {
	pl, ok := in.Bundle.Pipelines[in.Name]
	if !ok {
		return RunResult{}, fmt.Errorf("pipeline %q not found", in.Name)
	}
	if pb, ok := e.be.(backend.PipelineBackend); ok {
		return e.runViaPipelineBackend(ctx, pb, in, pl)
	}
	runID := in.RunID
	if runID == "" {
		runID = newRunID()
	}
	pipelineRunName := pl.Metadata.Name + "-" + runID[:8]

	params, err := applyDefaults(pl.Spec.Params, in.Params)
	if err != nil {
		return RunResult{}, err
	}
	results := map[string]map[string]string{} // task → result name → value
	outcomes := map[string]TaskOutcome{}      // task → outcome

	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Now(), RunID: runID, Pipeline: pl.Metadata.Name})

	// Pre-pull images.
	images := uniqueImages(in.Bundle, pl)
	if err := e.be.Prepare(ctx, backend.RunSpec{RunID: runID, Pipeline: pl.Metadata.Name, Images: images, Workspaces: in.Workspaces}); err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error()})
		return RunResult{Status: "failed"}, err
	}
	defer func() { _ = e.be.Cleanup(context.Background()) }()

	// Build DAG (main only).
	g := dag.New()
	main := map[string]tektontypes.PipelineTask{}
	for _, pt := range pl.Spec.Tasks {
		g.AddNode(pt.Name)
		main[pt.Name] = pt
	}
	for _, pt := range pl.Spec.Tasks {
		for _, dep := range pt.RunAfter {
			g.AddEdge(dep, pt.Name)
		}
	}
	levels, err := g.Levels()
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error()})
		return RunResult{Status: "failed"}, err
	}

	overallStart := time.Now()
	overall := "succeeded"

	timeouts, err := parsePipelineTimeouts(pl.Spec.Timeouts)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error()})
		return RunResult{Status: "failed"}, err
	}
	pipeCtx, pipeCancel := withMaybeBudget(ctx, timeouts.Pipeline)
	defer pipeCancel()

	// Execute levels under the tasks budget.
	tasksCtx, tasksCancel := withMaybeBudget(pipeCtx, timeouts.tasksBudget())
	var mu sync.Mutex
levelLoop:
	for _, level := range levels {
		// If the tasks budget already fired, mark anything not yet
		// started as not-run and stop scheduling.
		if tasksCtx.Err() != nil {
			for _, taskName := range level {
				if _, alreadyDone := outcomes[taskName]; alreadyDone {
					continue
				}
				outcomes[taskName] = TaskOutcome{Status: "not-run"}
				e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: taskName, Message: "tasks timeout fired"})
			}
			continue
		}
		eg, gctx := errgroup.WithContext(tasksCtx)
		eg.SetLimit(e.opts.MaxParallel)
		for _, taskName := range level {
			tname := taskName
			pt := main[tname]

			mu.Lock()
			anyAncestorBad := false
			for _, ancestor := range upstream(g, tname) {
				if oc, ok := outcomes[ancestor]; ok {
					if oc.Status == "failed" || oc.Status == "not-run" || oc.Status == "skipped" {
						anyAncestorBad = true
						break
					}
				}
			}
			mu.Unlock()
			if anyAncestorBad {
				mu.Lock()
				outcomes[tname] = TaskOutcome{Status: "not-run"}
				mu.Unlock()
				e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: tname, Message: "upstream failure"})
				continue
			}

			eg.Go(func() error {
				e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Time: time.Now(), Task: tname})
				oc := e.runOneWithPolicy(gctx, in, pl, pt, params, results, runID, pipelineRunName)
				e.rep.Emit(reporter.Event{
					Kind: reporter.EvtTaskEnd, Time: time.Now(), Task: tname,
					Status: oc.Status, Duration: oc.Duration, Message: oc.Message, Attempt: oc.Attempt,
				})
				mu.Lock()
				outcomes[tname] = oc
				if oc.Results != nil {
					results[tname] = oc.Results
				}
				switch oc.Status {
				case "failed", "infrafailed":
					if overall != "timeout" {
						overall = "failed"
					}
				case "timeout":
					overall = "timeout"
				}
				mu.Unlock()
				return nil
			})
		}
		_ = eg.Wait()
		if tasksCtx.Err() != nil {
			overall = "timeout"
			tasksCancel()
			break levelLoop
		}
	}
	tasksCancel()

	// Finally tasks always run, under the finally budget (rooted at pipeCtx,
	// not tasksCtx — exhausting tasks must not shorten finally).
	finallyCtx, finallyCancel := withMaybeBudget(pipeCtx, timeouts.finallyBudget())
	defer finallyCancel()
	for _, pt := range pl.Spec.Finally {
		if finallyCtx.Err() != nil {
			outcomes[pt.Name] = TaskOutcome{Status: "not-run"}
			e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name, Message: "finally timeout fired"})
			continue
		}
		e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Time: time.Now(), Task: pt.Name})
		oc := e.runOneWithPolicy(finallyCtx, in, pl, pt, params, results, runID, pipelineRunName)
		// If the finally (or pipeline) budget fired during this task, the
		// backend returned a cancellation-class outcome ("infrafailed" /
		// "failed"). Re-classify it as "timeout" so the per-task event and
		// the overall run agree on what killed the task.
		if finallyCtx.Err() != nil && (oc.Status == "infrafailed" || oc.Status == "failed") {
			oc.Status = "timeout"
			if oc.Message == "" {
				oc.Message = "finally timeout exceeded"
			}
		}
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskEnd, Time: time.Now(), Task: pt.Name,
			Status: oc.Status, Duration: oc.Duration, Message: oc.Message, Attempt: oc.Attempt,
		})
		outcomes[pt.Name] = oc
		switch oc.Status {
		case "failed", "infrafailed":
			if overall != "timeout" {
				overall = "failed"
			}
		case "timeout":
			overall = "timeout"
		}
	}

	// Either budget firing means the run timed out, regardless of how
	// individual task outcomes shook out (a budget kill is a "timeout"
	// even if backends reported infrafailed mid-flight).
	if pipeCtx.Err() != nil || finallyCtx.Err() != nil {
		overall = "timeout"
	}

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunEnd, Time: time.Now(),
		Status: overall, Duration: time.Since(overallStart),
	})

	return RunResult{Status: overall, Tasks: outcomes}, nil
}

func (e *Engine) runOne(ctx context.Context, in PipelineInput, pl tektontypes.Pipeline, pt tektontypes.PipelineTask, params map[string]tektontypes.ParamValue, results map[string]map[string]string, runID, pipelineRunName string) TaskOutcome {
	// Build resolver context.
	rctx := resolver.Context{
		Params:       flattenStringParams(params),
		ArrayParams:  arrayParams(params),
		ObjectParams: objectParams(params),
		Results:      results,
		ContextVars: map[string]string{
			"pipelineRun.name": pipelineRunName,
			"pipeline.name":    pl.Metadata.Name,
			"taskRun.name":     pipelineRunName + "-" + pt.Name,
		},
	}

	// Evaluate when expressions.
	pass, reason, err := evaluateWhen(pt.When, rctx)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}
	if !pass {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name, Message: reason})
		return TaskOutcome{Status: "skipped", Message: reason}
	}

	// Resolve task spec.
	spec, err := lookupTaskSpec(in.Bundle, pt)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}

	// Resolve task-level params (PipelineTask params override Task defaults).
	taskParams := map[string]tektontypes.ParamValue{}
	for _, decl := range spec.Params {
		if decl.Default != nil {
			taskParams[decl.Name] = *decl.Default
		}
	}
	for _, p := range pt.Params {
		// substitute the value (which may reference $(params.x) or $(tasks.X.results.Y))
		switch p.Value.Type {
		case tektontypes.ParamTypeString, "":
			s, err := resolver.Substitute(p.Value.StringVal, rctx)
			if err != nil {
				return TaskOutcome{Status: "failed", Message: err.Error()}
			}
			taskParams[p.Name] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: s}
		case tektontypes.ParamTypeArray:
			out := []string{}
			for _, item := range p.Value.ArrayVal {
				s, err := resolver.Substitute(item, rctx)
				if err != nil {
					return TaskOutcome{Status: "failed", Message: err.Error()}
				}
				out = append(out, s)
			}
			taskParams[p.Name] = tektontypes.ParamValue{Type: tektontypes.ParamTypeArray, ArrayVal: out}
		case tektontypes.ParamTypeObject:
			out := map[string]string{}
			for k, v := range p.Value.ObjectVal {
				s, err := resolver.Substitute(v, rctx)
				if err != nil {
					return TaskOutcome{Status: "failed", Message: err.Error()}
				}
				out[k] = s
			}
			taskParams[p.Name] = tektontypes.ParamValue{Type: tektontypes.ParamTypeObject, ObjectVal: out}
		}
	}

	// Build a task-scoped resolver context (uses the task's own param view).
	taskCtx := resolver.Context{
		Params:       flattenStringParams(taskParams),
		ArrayParams:  arrayParams(taskParams),
		ObjectParams: objectParams(taskParams),
		Results:      results,
		ContextVars:  rctx.ContextVars,
	}

	// Substitute throughout the resolved Task spec.
	resolved := substituteSpec(spec, taskCtx)

	// Workspace mounts.
	wsMap := map[string]backend.WorkspaceMount{}
	for _, w := range pt.Workspaces {
		host, ok := in.Workspaces[w.Workspace]
		if !ok {
			return TaskOutcome{Status: "failed", Message: fmt.Sprintf("workspace %q not provisioned", w.Workspace)}
		}
		wsMap[w.Name] = backend.WorkspaceMount{HostPath: host, SubPath: w.SubPath}
	}

	// Allocate per-task results dir.
	resultsDir, err := provisionResultsDir(in.Workspaces["__results"], pt.Name) // sentinel key for the manager-supplied results parent
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}

	// Materialise Task volumes (emptyDir / hostPath / configMap / secret).
	var volumeHosts map[string]string
	if len(resolved.Volumes) > 0 {
		if e.opts.VolumeResolver == nil {
			return TaskOutcome{Status: "infrafailed", Message: "volumes declared but no VolumeResolver configured"}
		}
		var verr error
		volumeHosts, verr = e.opts.VolumeResolver(pt.Name, resolved.Volumes)
		if verr != nil {
			return TaskOutcome{Status: "infrafailed", Message: verr.Error()}
		}
	}

	taskRunName := pipelineRunName + "-" + pt.Name
	start := time.Now()
	res, err := e.be.RunTask(ctx, backend.TaskInvocation{
		RunID:       runID,
		PipelineRun: pipelineRunName,
		TaskName:    pt.Name,
		TaskRunName: taskRunName,
		Task:        resolved,
		Params:      taskParams,
		Workspaces:  wsMap,
		ContextVars: taskCtx.ContextVars,
		ResultsHost: resultsDir,
		VolumeHosts: volumeHosts,
		LogSink:     reporter.NewLogSink(e.rep),
	})
	dur := time.Since(start)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error(), Duration: dur}
	}
	status := string(res.Status)
	msg := ""
	if res.Err != nil {
		msg = res.Err.Error()
	}
	return TaskOutcome{Status: status, Message: msg, Results: res.Results, Duration: dur}
}

// upstream returns nodes that have a path to target.
func upstream(g *dag.Graph, target string) []string {
	// Reverse traversal: collect any node whose Descendants() includes target.
	var out []string
	for _, n := range g.Nodes() {
		if n == target {
			continue
		}
		for _, d := range g.Descendants(n) {
			if d == target {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func lookupTaskSpec(b *loader.Bundle, pt tektontypes.PipelineTask) (tektontypes.TaskSpec, error) {
	if pt.TaskSpec != nil {
		return *pt.TaskSpec, nil
	}
	if pt.TaskRef != nil {
		t, ok := b.Tasks[pt.TaskRef.Name]
		if !ok {
			return tektontypes.TaskSpec{}, fmt.Errorf("task %q not loaded", pt.TaskRef.Name)
		}
		return t.Spec, nil
	}
	return tektontypes.TaskSpec{}, fmt.Errorf("pipeline task %q has neither taskRef nor taskSpec", pt.Name)
}

func substituteSpec(spec tektontypes.TaskSpec, ctx resolver.Context) tektontypes.TaskSpec {
	out := spec
	out.Steps = make([]tektontypes.Step, len(spec.Steps))
	for i, st := range spec.Steps {
		ns := st
		ns.Image, _ = resolver.SubstituteAllowStepRefs(st.Image, ctx)
		if len(st.Command) > 0 {
			ns.Command, _ = resolver.SubstituteArgsAllowStepRefs(st.Command, ctx)
		}
		if len(st.Args) > 0 {
			ns.Args, _ = resolver.SubstituteArgsAllowStepRefs(st.Args, ctx)
		}
		ns.Script, _ = resolver.SubstituteAllowStepRefs(st.Script, ctx)
		ns.WorkingDir, _ = resolver.SubstituteAllowStepRefs(st.WorkingDir, ctx)
		ns.Env = make([]tektontypes.EnvVar, len(st.Env))
		for j, e := range st.Env {
			v, _ := resolver.SubstituteAllowStepRefs(e.Value, ctx)
			ns.Env[j] = tektontypes.EnvVar{Name: e.Name, Value: v}
		}
		out.Steps[i] = ns
	}
	return out
}

func uniqueImages(b *loader.Bundle, pl tektontypes.Pipeline) []string {
	seen := map[string]struct{}{}
	for _, pt := range append(append([]tektontypes.PipelineTask{}, pl.Spec.Tasks...), pl.Spec.Finally...) {
		var spec tektontypes.TaskSpec
		if pt.TaskRef != nil {
			if t, ok := b.Tasks[pt.TaskRef.Name]; ok {
				spec = t.Spec
			}
		} else if pt.TaskSpec != nil {
			spec = *pt.TaskSpec
		}
		for _, s := range spec.Steps {
			seen[s.Image] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for img := range seen {
		out = append(out, img)
	}
	return out
}

// provisionResultsDir is a thin wrapper that the CLI replaces with a real
// workspace.Manager closure. For unit tests with the fake backend, it returns
// an empty path which the fake ignores.
var provisionResultsDir = func(parent, taskName string) (string, error) { return "", nil }

// SetResultsDirProvisioner is called once by the CLI to wire the engine's
// per-task results dir creation into a workspace.Manager. Tests don't need
// this — the unit test fake backend ignores the empty path.
func SetResultsDirProvisioner(fn func(parent, taskName string) (string, error)) {
	provisionResultsDir = fn
}

func (e *Engine) runViaPipelineBackend(ctx context.Context, pb backend.PipelineBackend, in PipelineInput, pl tektontypes.Pipeline) (RunResult, error) {
	runID := in.RunID
	if runID == "" {
		runID = newRunID()
	}
	pipelineRunName := pl.Metadata.Name + "-" + runID[:8]

	params, err := applyDefaults(pl.Spec.Params, in.Params)
	if err != nil {
		return RunResult{}, err
	}

	images := uniqueImages(in.Bundle, pl)
	if err := pb.Prepare(ctx, backend.RunSpec{RunID: runID, Pipeline: pl.Metadata.Name, Images: images, Workspaces: in.Workspaces}); err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error()})
		return RunResult{Status: "failed"}, err
	}

	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Now(), RunID: runID, Pipeline: pl.Metadata.Name})

	var paramList []tektontypes.Param
	for k, v := range params {
		paramList = append(paramList, tektontypes.Param{Name: k, Value: v})
	}

	wsMap := map[string]backend.WorkspaceMount{}
	for k, host := range in.Workspaces {
		wsMap[k] = backend.WorkspaceMount{HostPath: host}
	}

	start := time.Now()
	res, err := pb.RunPipeline(ctx, backend.PipelineRunInvocation{
		RunID:           runID,
		PipelineRunName: pipelineRunName,
		Pipeline:        pl,
		Tasks:           in.Bundle.Tasks,
		Params:          paramList,
		Workspaces:      wsMap,
		LogSink:         reporter.NewLogSink(e.rep),
	})
	dur := time.Since(start)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Duration: dur, Message: err.Error()})
		return RunResult{Status: "failed"}, err
	}
	e.emitClusterTaskEvents(res.Tasks)
	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: res.Status, Duration: dur})
	out := RunResult{Status: res.Status, Tasks: map[string]TaskOutcome{}}
	for n, oc := range res.Tasks {
		out.Tasks[n] = TaskOutcome{Status: oc.Status, Message: oc.Message, Results: oc.Results}
	}
	return out, nil
}

// emitClusterTaskEvents synthesises the per-task event sequence the docker
// engine produces live (task-start, [task-retry]*, task-end) from the
// cluster backend's post-hoc per-TaskRun summary. Cluster-mode events
// arrive at the end of the run rather than interleaved with execution,
// but their *shape* matches docker so an agent listening to --output json
// doesn't have to special-case the backend.
func (e *Engine) emitClusterTaskEvents(tasks map[string]backend.TaskOutcomeOnCluster) {
	for n, oc := range tasks {
		now := time.Now()
		e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Time: now, Task: n})
		for _, r := range oc.RetryAttempts {
			t := r.Time
			if t.IsZero() {
				t = now
			}
			e.rep.Emit(reporter.Event{
				Kind:    reporter.EvtTaskRetry,
				Time:    t,
				Task:    n,
				Status:  r.Status,
				Message: r.Message,
				Attempt: r.Attempt,
			})
		}
		attempt := oc.Attempts
		if attempt == 0 {
			attempt = 1
		}
		e.rep.Emit(reporter.Event{
			Kind:    reporter.EvtTaskEnd,
			Time:    time.Now(),
			Task:    n,
			Status:  oc.Status,
			Message: oc.Message,
			Attempt: attempt,
		})
	}
}
