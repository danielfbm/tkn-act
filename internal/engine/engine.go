// Package engine orchestrates a Tekton PipelineRun. It builds the DAG, resolves
// params and results, evaluates when-expressions, runs `finally` tasks, and
// drives a Backend. Pure of I/O except via the Backend and the Reporter.
package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/debug"
	"github.com/danielfbm/tkn-act/internal/engine/dag"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
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
	// Refresolver dispatches resolver-backed taskRefs at task-dispatch
	// time. nil-safe: when nil, any PipelineTask whose taskRef.resolver
	// is non-empty will fail with a clear error from lookupTaskSpecLazy.
	// The CLI builds this from --resolver-allow / --resolver-cache-dir /
	// --offline; tests inject a Registry with an inline stub resolver.
	Refresolver *refresolver.Registry
	// Debug is the verbose-trace emitter. The CLI builds this from the
	// --debug flag; nil means "no debug emissions" (the engine
	// substitutes debug.Nop in New). When non-nil, the engine propagates
	// it to the backend (via SetDebug if the backend implements
	// DebugSetter) and to Refresolver (via Registry.SetDebug) at
	// run-start, so all three components emit through the same channel.
	Debug debug.Emitter
}

// DebugSetter is implemented by backends that accept a debug.Emitter.
// The engine type-asserts the backend against this interface at
// run-start; backends that don't implement it stay silent on --debug.
type DebugSetter interface {
	SetDebug(d debug.Emitter)
}

// VolumeResolver is the engine's hook for the volumes package. Returns
// map[volumeName] -> hostPath.
type VolumeResolver func(taskName string, vs []tektontypes.Volume) (map[string]string, error)

type Engine struct {
	be   backend.Backend
	rep  reporter.Reporter
	opts Options
	dbg  debug.Emitter
}

func New(be backend.Backend, rep reporter.Reporter, opts Options) *Engine {
	if opts.MaxParallel <= 0 {
		opts.MaxParallel = 4
	}
	dbg := opts.Debug
	if dbg == nil {
		dbg = debug.Nop()
	}
	// Propagate the emitter so resolver and backend emit through the
	// same reporter the engine writes to. Done at New (not RunPipeline)
	// so callers that drive sub-flows directly (tests, the
	// remote-resolver dispatch) get the same wiring.
	if opts.Refresolver != nil {
		opts.Refresolver.SetDebug(dbg)
	}
	if ds, ok := be.(DebugSetter); ok {
		ds.SetDebug(dbg)
	}
	return &Engine{be: be, rep: rep, opts: opts, dbg: dbg}
}

func (e *Engine) RunPipeline(ctx context.Context, in PipelineInput) (RunResult, error) {
	// Eager top-level pipelineRef.resolver resolution. A PipelineRun
	// whose spec.pipelineRef carries a resolver block is resolved
	// SYNCHRONOUSLY at load time (spec §7) — the resolved Pipeline
	// replaces in.Name and gets injected into the bundle before DAG
	// build. Top-level resolution emits resolver-start / resolver-end
	// with an empty Task field; the consumer disambiguates "this is a
	// top-level pipelineRef resolution" from "this is a per-task
	// resolution" via the absence of the task field.
	if pl, ok := maybeResolveTopLevelPipelineRef(ctx, in, e.opts.Refresolver, e.rep); ok {
		// Inject a synthetic name if the PipelineRun didn't carry one,
		// or the resolved Pipeline's metadata.name differs.
		name := pl.Metadata.Name
		if name == "" {
			name = "resolved"
			pl.Metadata.Name = name
		}
		if in.Bundle.Pipelines == nil {
			in.Bundle.Pipelines = map[string]tektontypes.Pipeline{}
		}
		in.Bundle.Pipelines[name] = pl
		in.Name = name
	} else if in.Name == "" {
		// No top-level resolver and no name — surface the failure if
		// maybeResolveTopLevelPipelineRef reported one (it emitted a
		// run-end already), else fall through to the not-found branch.
		// We don't synthesize a "best guess" pipeline name here; the
		// caller (CLI) disambiguates that.
	}
	pl, ok := in.Bundle.Pipelines[in.Name]
	if !ok {
		// If the engine emitted a top-level resolver-end with status
		// failed, treat the run as already-terminated. Detect that by
		// checking whether maybeResolveTopLevelPipelineRef saw a
		// resolver block; if it did, we already emitted run-start /
		// run-end ourselves and can return cleanly.
		if hasTopLevelPipelineRefResolver(in.Bundle) {
			return RunResult{Status: "failed"}, nil
		}
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

	// Fan out PipelineTask.matrix into expansion children before
	// building the DAG. The DAG layer is matrix-unaware after this
	// pass — every expansion looks like an ordinary PipelineTask.
	pl, err = expandMatrix(pl, params)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}

	results := map[string]map[string]string{} // task → result name → value
	outcomes := map[string]TaskOutcome{}      // task → outcome

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunStart, Time: time.Now(),
		RunID: runID, Pipeline: pl.Metadata.Name,
		DisplayName: pl.Spec.DisplayName,
		Description: pl.Spec.Description,
	})

	// Pre-pull images.
	images := uniqueImages(in.Bundle, pl)
	if err := e.be.Prepare(ctx, backend.RunSpec{RunID: runID, Pipeline: pl.Metadata.Name, Images: images, Workspaces: in.Workspaces}); err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
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
		// Implicit edges from $(tasks.X.results.Y) references in
		// pt.Params and pt.TaskRef.ResolverParams. Mirrors upstream
		// Tekton; lazy-resolved taskRefs depend on this so a
		// resolver.params reference to an upstream result schedules
		// after the upstream task. Refs to tasks not in the main
		// DAG are silently dropped here (the validator catches
		// dangling refs separately).
		for _, dep := range implicitParamEdges(pt) {
			if _, ok := main[dep]; !ok {
				continue
			}
			if dep == pt.Name {
				continue
			}
			g.AddEdge(dep, pt.Name)
		}
	}
	levels, err := g.Levels()
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}

	overallStart := time.Now()
	overall := "succeeded"

	timeouts, err := parsePipelineTimeouts(pl.Spec.Timeouts)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
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
				e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: taskName, Message: "tasks timeout fired", DisplayName: main[taskName].DisplayName, Matrix: matrixEventFor(main[taskName])})
			}
			continue
		}
		eg, gctx := errgroup.WithContext(tasksCtx)
		eg.SetLimit(e.opts.MaxParallel)
		for _, taskName := range level {
			tname := taskName
			pt := main[tname]

			mu.Lock()
			anyAncestorBad := upstreamBlocksDispatch(g, tname, main, outcomes)
			mu.Unlock()
			if anyAncestorBad {
				mu.Lock()
				outcomes[tname] = TaskOutcome{Status: "not-run"}
				mu.Unlock()
				e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: tname, Message: "upstream failure", DisplayName: pt.DisplayName, Matrix: matrixEventFor(pt)})
				continue
			}

			// Resolve the Task spec (best-effort) so we can carry its
			// description on task-start. Any lookup error is handled by
			// runOne; it's safe to ignore here.
			taskSpec, _ := lookupTaskSpec(in.Bundle, pt)

			// Snapshot the results map under the mutex so the
			// per-task substitution sees a stable view. Concurrent
			// tasks at the same level write to `results` after their
			// runs end; without a snapshot a goroutine reading
			// resolver.Context.Results races with those writes.
			mu.Lock()
			resultsSnap := make(map[string]map[string]string, len(results))
			for k, v := range results {
				resultsSnap[k] = v
			}
			mu.Unlock()

			eg.Go(func() error {
				e.dbg.Emit(debug.Engine, func() (string, map[string]any) {
					return "task ready", map[string]any{"task": tname}
				})
				e.rep.Emit(reporter.Event{
					Kind: reporter.EvtTaskStart, Time: time.Now(), Task: tname,
					DisplayName: pt.DisplayName,
					Description: taskSpec.Description,
					Matrix:      matrixEventFor(pt),
				})
				oc := e.runOneWithPolicy(gctx, in, pl, pt, params, resultsSnap, runID, pipelineRunName)
				if pt.MatrixInfo != nil {
					oc.Matrix = pt.MatrixInfo
				}
				// Per-row when: a skipped expansion emits its own
				// task-skip under the expansion name with Matrix
				// populated, then we mark it not-run (so siblings can
				// still proceed) and skip the task-end emission.
				if oc.Status == "skipped" && pt.MatrixInfo != nil {
					e.rep.Emit(reporter.Event{
						Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: tname,
						Message: oc.Message, DisplayName: pt.DisplayName,
						Matrix: matrixEventFor(pt),
					})
					mu.Lock()
					outcomes[tname] = TaskOutcome{Status: "not-run", Message: oc.Message, Matrix: pt.MatrixInfo}
					maybeAggregateMatrix(pt, pl, outcomes, results)
					mu.Unlock()
					return nil
				}
				e.rep.Emit(reporter.Event{
					Kind: reporter.EvtTaskEnd, Time: time.Now(), Task: tname,
					Status: oc.Status, Duration: oc.Duration, Message: oc.Message, Attempt: oc.Attempt,
					DisplayName: pt.DisplayName,
					Matrix:      matrixEventFor(pt),
				})
				mu.Lock()
				outcomes[tname] = oc
				if oc.Results != nil {
					results[tname] = oc.Results
				}
				maybeAggregateMatrix(pt, pl, outcomes, results)
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
			e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name, Message: "finally timeout fired", DisplayName: pt.DisplayName, Matrix: matrixEventFor(pt)})
			continue
		}
		finallyTaskSpec, _ := lookupTaskSpec(in.Bundle, pt)
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskStart, Time: time.Now(), Task: pt.Name,
			DisplayName: pt.DisplayName,
			Description: finallyTaskSpec.Description,
			Matrix:      matrixEventFor(pt),
		})
		oc := e.runOneWithPolicy(finallyCtx, in, pl, pt, params, results, runID, pipelineRunName)
		if pt.MatrixInfo != nil {
			oc.Matrix = pt.MatrixInfo
		}
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
		// Per-row when: skipped finally expansion emits its own task-skip.
		if oc.Status == "skipped" && pt.MatrixInfo != nil {
			e.rep.Emit(reporter.Event{
				Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name,
				Message: oc.Message, DisplayName: pt.DisplayName,
				Matrix: matrixEventFor(pt),
			})
			outcomes[pt.Name] = TaskOutcome{Status: "not-run", Message: oc.Message, Matrix: pt.MatrixInfo}
			maybeAggregateMatrix(pt, pl, outcomes, results)
			continue
		}
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskEnd, Time: time.Now(), Task: pt.Name,
			Status: oc.Status, Duration: oc.Duration, Message: oc.Message, Attempt: oc.Attempt,
			DisplayName: pt.DisplayName,
			Matrix:      matrixEventFor(pt),
		})
		outcomes[pt.Name] = oc
		// Finally-task results must enter the same `results` map the
		// main loop populates: Pipeline.spec.results may reference
		// $(tasks.<finally>.results.<name>) and the resolver only
		// reads from this map. Before, finally results were absent
		// from the map and any spec.results entry that referenced
		// them was silently dropped.
		if oc.Results != nil {
			results[pt.Name] = oc.Results
		}
		maybeAggregateMatrix(pt, pl, outcomes, results)
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

	// Resolve Pipeline.spec.results once every task (incl. finally) is
	// terminal. Drops are non-fatal: each surfaces as an EvtError but
	// does not change overall status or the exit code.
	pipelineResults, resultErrs := resolvePipelineResults(pl, results)
	for _, err := range resultErrs {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtError, Time: time.Now(), Message: err.Error()})
	}

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunEnd, Time: time.Now(),
		Status: overall, Duration: time.Since(overallStart),
		Results:     pipelineResults,
		DisplayName: pl.Spec.DisplayName,
	})

	return RunResult{Status: overall, Tasks: outcomes, Results: pipelineResults}, nil
}

func (e *Engine) runOne(ctx context.Context, in PipelineInput, pl tektontypes.Pipeline, pt tektontypes.PipelineTask, params map[string]tektontypes.ParamValue, results map[string]map[string]string, runID, pipelineRunName string) TaskOutcome {
	// Build resolver context. For matrix-fanned expansions we layer
	// the row's matrix-contributed params on top of the pipeline-level
	// params so $(params.<matrix-name>) inside `when:` resolves to the
	// row's value (Tekton's per-row when semantics).
	rctxParams := flattenStringParams(params)
	if pt.MatrixInfo != nil {
		// pt.MatrixInfo.Params already holds the row's string-keyed
		// string values. Layer them onto the pipeline-level view.
		merged := make(map[string]string, len(rctxParams)+len(pt.MatrixInfo.Params))
		for k, v := range rctxParams {
			merged[k] = v
		}
		for k, v := range pt.MatrixInfo.Params {
			merged[k] = v
		}
		rctxParams = merged
	}
	rctx := resolver.Context{
		Params:       rctxParams,
		ArrayParams:  arrayParams(params),
		ObjectParams: objectParams(params),
		Results:      results,
		ContextVars: map[string]string{
			"pipelineRun.name": pipelineRunName,
			"pipeline.name":    pl.Metadata.Name,
			"taskRun.name":     pipelineRunName + "-" + pt.Name,
		},
	}

	// Emit a "params resolved" debug event with the count of resolved
	// keys and a truncated peek at each value. Useful diagnostic when
	// $(...) substitution surfaces a surprise upstream — and cheap when
	// disabled because the build closure short-circuits.
	e.dbg.Emit(debug.Engine, func() (string, map[string]any) {
		preview := make(map[string]string, len(rctx.Params))
		for k, v := range rctx.Params {
			preview[k] = truncate(v, 64)
		}
		return "params resolved", map[string]any{
			"task":              pt.Name,
			"count":             len(rctx.Params),
			"truncated_values":  preview,
		}
	})

	// Evaluate when expressions.
	pass, reason, err := evaluateWhen(pt.When, rctx)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}
	if !pass {
		// "task skipped" debug event carries the unevaluated when
		// expression alongside the reason — agents can correlate which
		// clause refused without re-parsing the message string.
		e.dbg.Emit(debug.Engine, func() (string, map[string]any) {
			expr := ""
			if len(pt.When) > 0 {
				expr = fmt.Sprintf("%v", pt.When)
			}
			return "task skipped", map[string]any{
				"task":       pt.Name,
				"reason":     reason,
				"expression": truncate(expr, 64),
			}
		})
		// For matrix-fanned tasks, the *expansion-name* skip is
		// emitted by the caller (RunPipeline's eg.Go closure) so
		// that Matrix is populated; suppress the inner skip event
		// here to avoid duplicates.
		if pt.MatrixInfo == nil {
			e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name, Message: reason, DisplayName: pt.DisplayName})
		}
		return TaskOutcome{Status: "skipped", Message: reason}
	}

	// Resolve task spec, then merge StepTemplate into each Step
	// before any further substitution / validation runs. If the
	// PipelineTask uses a resolver-backed taskRef, lazy-dispatch
	// kicks in: substitute resolver.params against rctx, call the
	// registry, validate the bytes, and return the inlined TaskSpec.
	var spec tektontypes.TaskSpec
	if pt.TaskRef != nil && pt.TaskRef.Resolver != "" {
		var lerr error
		spec, _, lerr = lookupTaskSpecLazy(ctx, pt, rctx, e.opts.Refresolver, e.rep)
		if lerr != nil {
			return TaskOutcome{Status: "failed", Message: lerr.Error()}
		}
	} else {
		var lerr error
		spec, lerr = lookupTaskSpec(in.Bundle, pt)
		if lerr != nil {
			return TaskOutcome{Status: "failed", Message: lerr.Error()}
		}
	}
	// Expand any Step.Ref → StepAction body BEFORE stepTemplate
	// inheritance and before substitution. After this point every
	// downstream pass sees a TaskSpec where every Step has a body.
	{
		var lerr error
		spec, lerr = resolveStepActions(spec, in.Bundle)
		if lerr != nil {
			return TaskOutcome{Status: "failed", Message: lerr.Error()}
		}
	}
	spec = applyStepTemplate(spec)

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

// upstreamBlocksDispatch decides whether `target` should be skipped
// due to upstream failure. For ordinary tasks the rule is the same as
// before: any non-success ancestor blocks. For matrix-fanned ancestors
// we treat the *parent* as a single logical node — if AT LEAST ONE
// expansion of the parent succeeded, downstream may proceed (matches
// upstream Tekton's per-row when semantics; see spec § 6.3).
//
// If every expansion of a parent is non-success, that parent is
// considered "bad" and downstream is skipped. Mixed-success matrix
// parents do NOT block downstream.
func upstreamBlocksDispatch(g *dag.Graph, target string, main map[string]tektontypes.PipelineTask, outcomes map[string]TaskOutcome) bool {
	// Group ancestors by matrix-parent name. Non-matrix ancestors are
	// keyed by their own name; matrix expansions group under their
	// parent. A group is "bad" iff every member is non-success.
	type group struct {
		members []string
		anyOK   bool
	}
	groups := map[string]*group{}
	keyFor := func(name string) string {
		pt, ok := main[name]
		if !ok {
			return name
		}
		if pt.MatrixInfo != nil {
			return "matrix:" + pt.MatrixInfo.Parent
		}
		return name
	}
	for _, ancestor := range upstream(g, target) {
		k := keyFor(ancestor)
		grp, ok := groups[k]
		if !ok {
			grp = &group{}
			groups[k] = grp
		}
		grp.members = append(grp.members, ancestor)
		oc, present := outcomes[ancestor]
		if present && oc.Status == "succeeded" {
			grp.anyOK = true
		}
	}
	for _, grp := range groups {
		// At least one outcome must be present (not-yet-run ancestors
		// shouldn't block dispatch — engine relies on level ordering).
		anyTerminal := false
		allBad := true
		for _, m := range grp.members {
			oc, ok := outcomes[m]
			if !ok {
				allBad = false
				continue
			}
			anyTerminal = true
			if oc.Status == "succeeded" {
				allBad = false
			}
		}
		if anyTerminal && allBad {
			return true
		}
	}
	return false
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
	// Sidecars start before any step in the Task and are torn down
	// after the last step, so step-result references don't apply
	// (use the plain Substitute, not SubstituteAllowStepRefs).
	if len(spec.Sidecars) > 0 {
		out.Sidecars = make([]tektontypes.Sidecar, len(spec.Sidecars))
		for i, sc := range spec.Sidecars {
			ns := sc
			ns.Image, _ = resolver.Substitute(sc.Image, ctx)
			if len(sc.Command) > 0 {
				ns.Command, _ = resolver.SubstituteArgs(sc.Command, ctx)
			}
			if len(sc.Args) > 0 {
				ns.Args, _ = resolver.SubstituteArgs(sc.Args, ctx)
			}
			ns.Script, _ = resolver.Substitute(sc.Script, ctx)
			ns.WorkingDir, _ = resolver.Substitute(sc.WorkingDir, ctx)
			ns.Env = make([]tektontypes.EnvVar, len(sc.Env))
			for j, e := range sc.Env {
				v, _ := resolver.Substitute(e.Value, ctx)
				ns.Env[j] = tektontypes.EnvVar{Name: e.Name, Value: v}
			}
			out.Sidecars[i] = ns
		}
	}
	return out
}

func uniqueImages(b *loader.Bundle, pl tektontypes.Pipeline) []string {
	seen := map[string]struct{}{}
	for _, pt := range append(append([]tektontypes.PipelineTask{}, pl.Spec.Tasks...), pl.Spec.Finally...) {
		var spec tektontypes.TaskSpec
		if pt.TaskRef != nil && pt.TaskRef.Resolver != "" {
			// Resolver-backed taskRef: bytes aren't available at
			// pre-pull time; the runtime image pull falls back to
			// per-step IfNotPresent semantics. Skip silently here
			// (resolver-end events surface what was fetched).
			continue
		}
		if pt.TaskRef != nil {
			if t, ok := b.Tasks[pt.TaskRef.Name]; ok {
				spec = t.Spec
			}
		} else if pt.TaskSpec != nil {
			spec = *pt.TaskSpec
		}
		// Expand StepAction refs first so referenced StepAction images
		// are pre-pulled, then merge stepTemplate so inherited images
		// count too. Errors here are silently skipped (the validator
		// catches missing-ref / ref+inline issues at exit 4 before
		// pre-pull runs); a Step with Ref pointing to a missing
		// StepAction would otherwise contribute the empty string.
		if expanded, err := resolveStepActions(spec, b); err == nil {
			spec = expanded
		}
		spec = applyStepTemplate(spec)
		// Pre-pull is best-effort: image strings still containing
		// `$(...)` (param / context / matrix-row references) are not
		// valid OCI references and would fail ImagePull immediately,
		// even though they would resolve correctly at task-dispatch
		// time after substituteSpec runs. Skip them here and rely on
		// the per-step ensureImage path in the backend, which sees
		// the substituted image and pulls IfNotPresent.
		for _, s := range spec.Steps {
			if s.Image != "" && !strings.Contains(s.Image, "$(") {
				seen[s.Image] = struct{}{}
			}
		}
		for _, sc := range spec.Sidecars {
			if sc.Image != "" && !strings.Contains(sc.Image, "$(") {
				seen[sc.Image] = struct{}{}
			}
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

	// Cluster-backend lazy-resolve. Local k3d's Tekton has no
	// resolver credentials and no access to --resolver-cache-dir, so
	// we resolve in tkn-act and inline the resulting TaskSpec into
	// the Pipeline before submission. Phase 1 handles resolver.params
	// that reference run-scope only (params, context); upstream-result
	// deps within a single submission are deferred to a Phase 2+
	// extension that submits one PipelineRun per dispatch level.
	rctx := resolver.Context{
		Params:       flattenStringParams(params),
		ArrayParams:  arrayParams(params),
		ObjectParams: objectParams(params),
		Results:      map[string]map[string]string{},
		ContextVars: map[string]string{
			"pipelineRun.name": pipelineRunName,
			"pipeline.name":    pl.Metadata.Name,
		},
	}
	if rerr := inlineResolverBackedTasks(ctx, &pl, rctx, e.opts.Refresolver, e.rep); rerr != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: rerr.Error()})
		return RunResult{Status: "failed"}, rerr
	}

	images := uniqueImages(in.Bundle, pl)
	if err := pb.Prepare(ctx, backend.RunSpec{RunID: runID, Pipeline: pl.Metadata.Name, Images: images, Workspaces: in.Workspaces}); err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunStart, Time: time.Now(),
		RunID: runID, Pipeline: pl.Metadata.Name,
		DisplayName: pl.Spec.DisplayName,
		Description: pl.Spec.Description,
	})

	var paramList []tektontypes.Param
	for k, v := range params {
		paramList = append(paramList, tektontypes.Param{Name: k, Value: v})
	}

	wsMap := map[string]backend.WorkspaceMount{}
	for k, host := range in.Workspaces {
		wsMap[k] = backend.WorkspaceMount{HostPath: host}
	}

	// Expand StepAction refs in every Task and every inline PipelineTask.taskSpec
	// before handing them to the pipeline backend. Cluster mode submits the
	// inlined Step shape — the cluster's Tekton controller never sees a
	// Ref-bearing Step (it would try to resolve `stepactions.tekton.dev/<name>`
	// from the per-run namespace, which we never apply). Errors here drop
	// the run with status:failed.
	expandedTasks, plExpanded, expandErr := expandBundleStepActions(in.Bundle, pl)
	if expandErr != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: expandErr.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, expandErr
	}

	start := time.Now()
	res, err := pb.RunPipeline(ctx, backend.PipelineRunInvocation{
		RunID:           runID,
		PipelineRunName: pipelineRunName,
		Pipeline:        plExpanded,
		Tasks:           expandedTasks,
		Params:          paramList,
		Workspaces:      wsMap,
		LogSink:         reporter.NewLogSink(e.rep),
	})
	dur := time.Since(start)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Duration: dur, Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}
	e.emitClusterTaskEvents(pl, in.Bundle, res.Tasks)
	// Cross-backend EvtError parity for dropped pipeline results: the
	// docker engine emits one EvtError per declared spec.results name
	// the engine couldn't resolve. The cluster backend reads
	// pr.status.results post-hoc, so missing entries surface here as
	// "declared by the Pipeline but absent from the backend's verdict."
	// Emit one EvtError per such drop, in stable name order, so a
	// silent regression on the cluster path can't slip past CI.
	for _, name := range droppedClusterResultNames(pl, res.Results) {
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtError, Time: time.Now(),
			Message: fmt.Sprintf(
				"pipeline result %q dropped: not produced by Tekton (referenced task may have failed or skipped the result)",
				name),
		})
	}
	// Surface the backend's terminal Reason/Message on the run-end
	// event so a misclassification (status doesn't match what the test
	// expected) can be attributed to a specific backend code path
	// without needing to re-run with extra logging.
	endMsg := res.Message
	if res.Reason != "" {
		if endMsg != "" {
			endMsg = res.Reason + ": " + endMsg
		} else {
			endMsg = res.Reason
		}
	}
	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: res.Status, Duration: dur, Message: endMsg, Results: res.Results, DisplayName: pl.Spec.DisplayName})
	out := RunResult{
		Status:  res.Status,
		Reason:  res.Reason,
		Message: res.Message,
		Results: res.Results,
		Tasks:   map[string]TaskOutcome{},
	}
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
//
// pl + bundle are the input source-of-truth for displayName /
// description on synthesised events: we read them from the YAML the user
// submitted, not from the controller verdict, so cluster mode mirrors
// docker mode for these UX fields.
func (e *Engine) emitClusterTaskEvents(pl tektontypes.Pipeline, bundle *loader.Bundle, tasks map[string]backend.TaskOutcomeOnCluster) {
	// Emit in pipeline-declared order (main tasks first, then finally),
	// mirroring docker's interleaved order. Without this, ranging over
	// the controller's per-task map gives Go's randomised iteration
	// order — agents asserting on "first task-start" would flake.
	emitOne := func(pt tektontypes.PipelineTask, taskKey string, oc backend.TaskOutcomeOnCluster) {
		spec, _ := lookupTaskSpec(bundle, pt)
		now := time.Now()
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskStart, Time: now, Task: taskKey,
			DisplayName: pt.DisplayName,
			Description: spec.Description,
			Matrix:      matrixEventFromInfo(oc.Matrix),
		})
		for _, r := range oc.RetryAttempts {
			t := r.Time
			if t.IsZero() {
				t = now
			}
			e.rep.Emit(reporter.Event{
				Kind:        reporter.EvtTaskRetry,
				Time:        t,
				Task:        taskKey,
				Status:      r.Status,
				Message:     r.Message,
				Attempt:     r.Attempt,
				DisplayName: pt.DisplayName,
				Matrix:      matrixEventFromInfo(oc.Matrix),
			})
		}
		attempt := oc.Attempts
		if attempt == 0 {
			attempt = 1
		}
		e.rep.Emit(reporter.Event{
			Kind:        reporter.EvtTaskEnd,
			Time:        time.Now(),
			Task:        taskKey,
			Status:      oc.Status,
			Message:     oc.Message,
			Attempt:     attempt,
			DisplayName: pt.DisplayName,
			Matrix:      matrixEventFromInfo(oc.Matrix),
		})
	}
	emit := func(pt tektontypes.PipelineTask) {
		// Non-matrix path: outcomes keyed by parent name.
		if pt.Matrix == nil {
			oc, ok := tasks[pt.Name]
			if !ok {
				return
			}
			emitOne(pt, pt.Name, oc)
			return
		}
		// Matrix path: walk every outcome whose Matrix.Parent == pt.Name
		// in row order so the JSON event sequence matches docker.
		type kv struct {
			name string
			oc   backend.TaskOutcomeOnCluster
		}
		var matches []kv
		for k, oc := range tasks {
			if oc.Matrix != nil && oc.Matrix.Parent == pt.Name {
				matches = append(matches, kv{name: k, oc: oc})
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			return matches[i].oc.Matrix.Index < matches[j].oc.Matrix.Index
		})
		for _, m := range matches {
			emitOne(pt, m.name, m.oc)
		}
	}
	for _, pt := range pl.Spec.Tasks {
		emit(pt)
	}
	for _, pt := range pl.Spec.Finally {
		emit(pt)
	}
}

// truncate clips s to at most max runes, appending an ellipsis when
// truncation actually happens. Used to keep debug field values
// bounded so a giant resolved param doesn't bloat events.jsonl. Works
// on runes (not bytes) so a multibyte UTF-8 boundary doesn't render
// a replacement character.
func truncate(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max-1]) + "…"
}
